"""Bounded ARD reads from configured endpoints; returned URLs grant no authority."""

from __future__ import annotations

import asyncio
from dataclasses import dataclass, field
import ipaddress
import json
import os
from pathlib import Path
import re
import ssl
import time
from urllib.parse import urlsplit

import httpx
from pydantic import BaseModel, ConfigDict

SUBJECT = "http://purl.org/dc/terms/subject"
PAGE_SIZE = 10
MAX_PAGES = 3
MAX_REQUESTS = 16
MAX_BYTES = 2 * 1024 * 1024
DEADLINE = 300


class ARDError(Exception):
    """Closed diagnostics, without dependency messages or response contents."""


def literal(value, maximum, *, empty=False):
    if (
        not isinstance(value, str)
        or len(value) > maximum
        or not empty
        and not value.strip()
        or any(
            (ord(c) < 32 and c not in "\t\r\n")
            or 0xD800 <= ord(c) <= 0xDFFF
            or ord(c) == 127
            for c in value
        )
    ):
        raise ARDError("invalid_input")
    return value


def strict_json(raw):
    def invalid_constant(_):
        raise ValueError()

    def pairs(values):
        result = {}
        for key, value in values:
            if key in result:
                raise ValueError()
            result[key] = value
        return result

    try:
        value = json.loads(
            raw.decode("utf-8") if isinstance(raw, bytes) else raw,
            object_pairs_hook=pairs,
            parse_constant=invalid_constant,
        )
        # Reject invalid Unicode even inside open metadata, not just projected fields.
        json.dumps(value, ensure_ascii=False, allow_nan=False).encode("utf-8")
        return value
    except (ValueError, UnicodeError, RecursionError, TypeError):
        raise ARDError("invalid_json") from None


class Service(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True, frozen=True)
    search_url: str
    explore_url: str | None = None
    token_env: str | None = None
    ca_file: str | None = None


class Settings(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True, frozen=True)
    primary: Service
    referral: Service | None = None
    disclose_to_referral: bool = False
    allow_loopback_http: bool = False


def endpoint(value, allow_http=False):
    literal(value, 2048)
    try:
        parsed = urlsplit(value)
        if (
            not parsed.hostname
            or parsed.username is not None
            or parsed.password is not None
            or parsed.query
            or parsed.fragment
            or not parsed.path.startswith("/")
            or parsed.port == 0
            or "\\" in value
            or any(c.isspace() for c in value)
        ):
            raise ValueError()
        if parsed.scheme != "https":
            if (
                parsed.scheme != "http"
                or not allow_http
                or not ipaddress.ip_address(parsed.hostname).is_loopback
            ):
                raise ValueError()
        return (
            parsed.scheme,
            parsed.hostname,
            parsed.port or (443 if parsed.scheme == "https" else 80),
        )
    except ValueError:
        raise ARDError("invalid_endpoint") from None


def load_settings(path):
    try:
        with Path(path).open("rb") as stream:
            raw = stream.read(65537)
        if len(raw) > 65536:
            raise ValueError()
        return Settings.model_validate(strict_json(raw))
    except (OSError, ValueError, ARDError, TypeError):
        raise ARDError("invalid_configuration") from None


def query(text, types=(), categories=(), *, empty=False):
    value = {"text": literal(text, 500, empty=empty)}
    filters = {}
    for key, terms in [("type", types), (SUBJECT, categories)]:
        if not isinstance(terms, (list, tuple)) or len(terms) > 8:
            raise ARDError("invalid_input")
        if terms:
            filters[key] = [literal(term, 512) for term in terms]
    if filters:
        value["filter"] = filters
    return value


def result_projection(value):
    if (
        not isinstance(value, dict)
        or not {"identifier", "score", "source"} <= value.keys()
    ):
        raise ARDError("invalid_response")
    output = {
        "identifier": literal(value["identifier"], 2048),
        "source": literal(value["source"], 2048),
    }
    if (
        not re.fullmatch(
            r"urn:air:[a-zA-Z0-9.-]+(:[a-zA-Z0-9._-]+)+", output["identifier"]
        )
        or not urlsplit(output["source"]).scheme
        or any(c.isspace() for c in output["source"])
        or type(value["score"]) is not int
        or not 0 <= value["score"] <= 100
    ):
        raise ARDError("invalid_response")
    output["score"] = value["score"]
    for key, maximum in [
        ("displayName", 512),
        ("type", 512),
        ("version", 512),
        ("description", 4096),
        ("url", 2048),
    ]:
        if key in value:
            output[key] = literal(value[key], maximum, empty=True)
    for key in ["tags", "capabilities", SUBJECT]:
        if key in value:
            terms = value[key]
            if key == SUBJECT and isinstance(terms, str):
                terms = [terms]
            if not isinstance(terms, list) or len(terms) > 32:
                raise ARDError("invalid_response")
            output[key] = [literal(term, 512, empty=True) for term in terms]
    if "trustManifest" in value:
        trust = value["trustManifest"]
        if not isinstance(trust, dict) or "identity" not in trust:
            raise ARDError("invalid_response")
        literal(trust["identity"], 2048)
        if len(json.dumps(trust, ensure_ascii=False).encode()) > 8192:
            raise ARDError("invalid_response")
        output["trustManifest"] = trust
        output["trustStatus"] = "unverified"
    return output


@dataclass
class ARDClient:
    settings: Settings
    transport: httpx.AsyncBaseTransport | None = field(default=None, repr=False)
    requests: int = 0
    started: float = field(default_factory=time.monotonic)
    bindings: dict = field(default_factory=dict, init=False, repr=False)

    def __post_init__(self):
        try:
            if (
                not isinstance(self.settings, Settings)
                or self.settings.primary.explore_url is None
            ):
                raise ValueError()
            if self.settings.disclose_to_referral and self.settings.referral is None:
                raise ValueError()
            for name, service in [
                ("primary", self.settings.primary),
                ("referral", self.settings.referral),
            ]:
                if service is None:
                    continue
                origin = endpoint(service.search_url, self.settings.allow_loopback_http)
                if (
                    service.explore_url is not None
                    and endpoint(service.explore_url, self.settings.allow_loopback_http)
                    != origin
                ):
                    raise ValueError()
                token = None
                if service.token_env is not None:
                    if not re.fullmatch(r"[A-Z][A-Z0-9_]{0,99}", service.token_env):
                        raise ValueError()
                    token = os.environ.get(service.token_env)
                    if (
                        not token
                        or len(token) > 8192
                        or not re.fullmatch(r"[\x21-\x7e]+", token)
                    ):
                        raise ValueError()
                # Let HTTPX select its public CA bundle with trust_env=False.
                # An implicit Python SSL context would honor SSL_CERT_FILE/DIR.
                trust = True
                if service.ca_file is not None:
                    trust = ssl.create_default_context(cafile=service.ca_file)
                    trust.minimum_version = ssl.TLSVersion.TLSv1_2
                self.bindings[name] = (service, token, trust)
            if (
                self.settings.referral
                and self.settings.referral.search_url
                == self.settings.primary.search_url
            ):
                raise ValueError()
        except (ValueError, OSError, ARDError, TypeError):
            raise ARDError("invalid_configuration") from None

    async def request(self, name, operation, body):
        if name not in self.bindings or operation not in ("search", "explore"):
            raise ARDError("invalid_operation")
        if self.requests >= MAX_REQUESTS:
            raise ARDError("request_budget_exhausted")
        remaining = DEADLINE - (time.monotonic() - self.started)
        if remaining <= 0:
            raise ARDError("deadline_exceeded")
        service, token, trust = self.bindings[name]
        url = service.search_url if operation == "search" else service.explore_url
        if url is None:
            raise ARDError("invalid_operation")
        headers = {"Accept": "application/json", "Accept-Encoding": "identity"}
        if token:
            headers["Authorization"] = "Bearer " + token
        self.requests += 1  # Admission has no await; parallel tools share this budget.
        try:
            async with asyncio.timeout(min(10, remaining)):
                # Per-request clients do not retain cookies, auth or mutable request state.
                async with httpx.AsyncClient(
                    verify=trust,
                    trust_env=False,
                    follow_redirects=False,
                    timeout=min(10, remaining),
                    transport=self.transport,
                ) as client:
                    async with client.stream(
                        "POST", url, json=body, headers=headers
                    ) as response:
                        if response.status_code != 200:
                            raise ARDError(
                                {401: "unauthenticated", 403: "forbidden"}.get(
                                    response.status_code,
                                    "redirect_refused"
                                    if 300 <= response.status_code < 400
                                    else "service_unavailable",
                                )
                            )
                        if (
                            response.headers.get("content-type", "")
                            .split(";", 1)[0]
                            .strip()
                            .lower()
                            != "application/json"
                        ):
                            raise ARDError("invalid_response")
                        if (
                            response.headers.get("content-encoding", "identity").lower()
                            != "identity"
                        ):
                            raise ARDError("invalid_response")
                        raw = bytearray()
                        async for block in response.aiter_raw():
                            if len(raw) + len(block) > MAX_BYTES:
                                raise ARDError("response_too_large")
                            raw.extend(block)
                        partial = bool(
                            re.search(
                                r'(?:^|,)\s*federation\s*;\s*desc\s*=\s*"partial"\s*(?:,|$)',
                                response.headers.get("server-timing", ""),
                            )
                        )
                        return strict_json(bytes(raw)), partial
        except ARDError:
            raise
        except (TimeoutError, httpx.TimeoutException):
            raise ARDError("deadline_exceeded") from None
        except (httpx.HTTPError, OSError, ValueError):
            raise ARDError("service_unavailable") from None

    async def pages(self, name, admitted_query, mode):
        items, issues, referrals, seen_tokens, seen_rows = [], [], [], set(), set()
        cursor = None
        for index in range(MAX_PAGES):
            body = {"query": admitted_query, "federation": mode, "pageSize": PAGE_SIZE}
            if cursor is not None:
                body["pageToken"] = cursor
            try:
                value, partial = await self.request(name, "search", body)
                if (
                    not isinstance(value, dict)
                    or not isinstance(value.get("results"), list)
                    or len(value["results"]) > PAGE_SIZE
                    or set(value) - {"results", "referrals", "pageToken"}
                ):
                    raise ARDError("invalid_response")
                # Validate the entire page before publishing any of its rows or referrals.
                projected = [result_projection(row) for row in value["results"]]
                next_token = value.get("pageToken", "")
                literal(next_token, 4096, empty=True)
                offered = value.get("referrals", [])
                if not isinstance(offered, list) or len(offered) > 32:
                    raise ARDError("invalid_response")
                for referral in offered:
                    if (
                        not isinstance(referral, dict)
                        or not {"identifier", "displayName", "type", "url"}
                        <= referral.keys()
                        or referral["type"]
                        not in (
                            "application/ai-registry",
                            "application/ai-registry+json",
                        )
                    ):
                        raise ARDError("invalid_response")
                    for key in ["identifier", "displayName", "url"]:
                        literal(referral[key], 2048)
                for row in projected:
                    for key, terms in admitted_query.get("filter", {}).items():
                        if key in row:
                            disclosed = (
                                row[key] if isinstance(row[key], list) else [row[key]]
                            )
                            if not set(disclosed).intersection(terms):
                                raise ARDError("filter_contradiction")
                fingerprints = [
                    json.dumps(row, sort_keys=True, ensure_ascii=False)
                    for row in value["results"]
                ]
                if len(set(fingerprints)) != len(fingerprints) or any(
                    row in seen_rows for row in fingerprints
                ):
                    raise ARDError("repeated_result")
                for row in projected:
                    items.append(
                        {
                            **row,
                            "responder": self.bindings[name][0].search_url,
                            "service": name,
                            "rank": len(items) + 1,
                        }
                    )
                seen_rows.update(fingerprints)
                referrals.extend(offered)
                if partial and not any(
                    issue["code"] == "service_partial" for issue in issues
                ):
                    issues.append({"service": name, "code": "service_partial"})
                if not next_token:
                    break
                if next_token in seen_tokens:
                    raise ARDError("repeated_page_token")
                seen_tokens.add(next_token)
                cursor = next_token
                if index == MAX_PAGES - 1:
                    raise ARDError("page_budget_exhausted")
            except (ARDError, ValueError, TypeError) as error:
                code = (
                    str(error)
                    if isinstance(error, ARDError) and str(error) != "invalid_input"
                    else "invalid_response"
                )
                issues.append({"service": name, "code": code})
                break
        return items, referrals, issues

    async def search(self, text, types=(), categories=()):
        admitted = query(text, types, categories)
        mode = "referrals" if self.settings.disclose_to_referral else "none"
        items, referrals, issues = await self.pages("primary", admitted, mode)
        omitted, followed = [], False
        for referral in referrals:
            url = referral["url"]
            approved = self.settings.referral
            if (
                self.settings.disclose_to_referral
                and approved
                and url == approved.search_url
            ):
                if followed:
                    continue
                followed = True
                found, recursive, failures = await self.pages(
                    "referral", admitted, "none"
                )
                items.extend(found)
                issues.extend(failures)
                omitted.extend(
                    {"url": value["url"], "reason": "hop_limit"} for value in recursive
                )
            else:
                omitted.append(
                    {
                        "url": url,
                        "reason": "not_approved"
                        if self.settings.disclose_to_referral
                        else "disclosure_disabled",
                    }
                )
        complete = not issues and not omitted
        return {
            "status": "complete" if complete else "partial" if items else "failed",
            "results": items,
            "issues": issues,
            "omitted_referrals": omitted,
        }

    async def explore(self, text="", types=(), categories=()):
        body = {
            "query": query(text, types, categories, empty=True),
            "resultType": {
                "facets": [
                    {"field": name, "limit": 20, "minCount": 1}
                    for name in ["type", SUBJECT]
                ]
            },
        }
        try:
            value, partial = await self.request("primary", "explore", body)
            if (
                not isinstance(value, dict)
                or value.get("resultType") != "facets"
                or set(value) != {"resultType", "facets"}
                or not isinstance(value.get("facets"), dict)
                or set(value["facets"]) != {"type", SUBJECT}
            ):
                raise ARDError("invalid_response")
            for facet in value["facets"].values():
                if (
                    not isinstance(facet, dict)
                    or set(facet) - {"buckets", "otherCount"}
                    or not isinstance(facet.get("buckets"), list)
                    or len(facet["buckets"]) > 20
                ):
                    raise ARDError("invalid_response")
                for bucket in facet["buckets"]:
                    if (
                        not isinstance(bucket, dict)
                        or set(bucket) != {"value", "count"}
                        or type(bucket.get("count")) is not int
                        or not 0 <= bucket["count"] <= 2**63 - 1
                    ):
                        raise ARDError("invalid_response")
                    literal(bucket.get("value"), 512, empty=True)
                if "otherCount" in facet and (
                    type(facet["otherCount"]) is not int
                    or not 0 <= facet["otherCount"] <= 2**63 - 1
                ):
                    raise ARDError("invalid_response")
            return {
                "status": "partial" if partial else "complete",
                "responder": self.settings.primary.explore_url,
                "facets": value["facets"],
                **(
                    {"issues": [{"service": "primary", "code": "service_partial"}]}
                    if partial
                    else {}
                ),
            }
        except (ARDError, ValueError, TypeError) as error:
            code = (
                str(error)
                if isinstance(error, ARDError) and str(error) != "invalid_input"
                else "invalid_response"
            )
            return {
                "status": "failed",
                "issues": [{"service": "primary", "code": code}],
            }
