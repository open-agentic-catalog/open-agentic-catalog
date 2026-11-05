"""Qualify native Gateway HTTP contracts from an invocation-owned, isolated Pod."""

import base64
from collections import Counter
import hashlib
import json
import os
from pathlib import Path
import ssl
import sys
import urllib.error
import urllib.parse
import urllib.request

import jsonschema
from referencing import Registry, Resource
from common import run

ORIGIN = "https://gateway.gateway-qualification.svc.cluster.local"
MCP = "application/mcp-server-card+json"
DIRECTORY = Path(__file__).parent
LIMIT = 1024 * 1024


def identities(entries):
    if not isinstance(entries, list):
        raise ValueError("native entries must be an array")
    ids = [entry["identifier"] for entry in entries]
    if len(ids) != len(set(ids)):
        raise ValueError("duplicate native identity")
    return set(ids)


def expected(*names):
    return {
        "urn:air:gateway.gateway-qualification.svc.cluster.local:server:" + name
        for name in names
    }


def validate(instance, definition):
    """Resolve only the two mounted, pinned local schemas; never fetch a schema URL."""
    entry = json.loads((DIRECTORY / "spec/schemas/ard-entry.schema.json").read_text())
    api = json.loads((DIRECTORY / "spec/schemas/ard.openapi.json").read_text())
    entry_uri, api_uri = "urn:gateway-test:entry", "urn:gateway-test:api"

    def rewrite(value):
        if isinstance(value, dict):
            return {
                k: (
                    entry_uri + "#" + v.split("#", 1)[1]
                    if k == "$ref" and v.startswith("./ard-entry.schema.json#")
                    else rewrite(v)
                )
                for k, v in value.items()
            }
        if isinstance(value, list):
            return [rewrite(v) for v in value]
        return value

    api = rewrite(api)
    # OpenAPI's schema objects use the same local reference graph. No remote resolver exists.
    api["$schema"] = "https://json-schema.org/draft/2020-12/schema"
    entry["$schema"] = api["$schema"]
    root = entry if definition in ("ArdEntry", "ArdManifest") else api
    reference = (
        "#/$defs/" + definition
        if root is entry
        else "#/components/schemas/" + definition
    )
    registry = Registry().with_resources(
        [
            (entry_uri, Resource.from_contents(entry)),
            (api_uri, Resource.from_contents(api)),
        ]
    )
    validator = jsonschema.Draft202012Validator(
        {**root, "$ref": reference},
        registry=registry,
        format_checker=jsonschema.FormatChecker(),
    )
    return sorted(
        [
            {
                "path": "/" + "/".join(map(str, error.absolute_path)),
                "validator": error.validator,
                "message": error.message,
                "null": error.instance is None,
            }
            for error in validator.iter_errors(instance)
        ],
        key=lambda row: (row["path"], row["message"]),
    )


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise ValueError("native qualification refuses redirects")


def descriptor_errors(document, name):
    schema = json.loads(
        (DIRECTORY / ("spec/schemas/" + name + ".schema.json")).read_text()
    )
    if name == "mcp-card":
        schema["$ref"] = "#/$defs/ServerCard"
    validator = jsonschema.validators.validator_for(schema)(
        schema, registry=Registry(), format_checker=jsonschema.FormatChecker()
    )
    return sorted(
        [
            {
                "path": "/" + "/".join(map(str, e.absolute_path)),
                "validator": e.validator,
                "message": e.message,
            }
            for e in validator.iter_errors(document)
        ],
        key=lambda row: (row["path"], row["message"]),
    )


class Native:
    def __init__(self, secrets, *, origin=ORIGIN, certificate="/trust/ca.pem"):
        if origin != ORIGIN:
            parsed = urllib.parse.urlsplit(origin)
            if (
                parsed.scheme != "https"
                or parsed.hostname != "127.0.0.1"
                or not parsed.port
                or parsed.username
                or parsed.password
                or parsed.path
                or parsed.query
                or parsed.fragment
            ):
                raise ValueError(
                    "native client requires the fixed peer or an owned loopback forward"
                )
        self.origin = origin
        self.secrets, self.captures, self.evidence = secrets, [], {}
        self.http = urllib.request.build_opener(
            urllib.request.ProxyHandler({}),
            NoRedirect(),
            urllib.request.HTTPSHandler(
                context=ssl.create_default_context(cafile=certificate)
            ),
        )

    def request(self, path, *, credential="admin", body=None, form=None, status=200):
        if (
            not path.startswith("/")
            or path.startswith("//")
            or len(self.captures) >= 128
        ):
            raise ValueError("native request exceeds its exact origin or count bound")
        headers, data = {"Host": urllib.parse.urlsplit(ORIGIN).netloc}, None
        if credential:
            headers["Authorization"] = "Bearer " + self.secrets[credential]
        if body is not None:
            headers["Content-Type"] = "application/json"
            data = json.dumps(body).encode()
        elif form is not None:
            headers["Content-Type"] = "application/x-www-form-urlencoded"
            data = urllib.parse.urlencode(form).encode()
        request = urllib.request.Request(self.origin + path, data=data, headers=headers)
        try:
            response = self.http.open(request, timeout=30)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            raw = response.read(LIMIT + 1)
            if len(raw) > LIMIT or response.geturl() != self.origin + path:
                raise ValueError("native response exceeds the byte or redirect bound")
            if any(secret.encode() in raw for secret in self.secrets.values()):
                raise ValueError(
                    "native response contains a synthetic credential; body not retained"
                )
            document = (
                json.loads(raw)
                if "json" in response.headers.get("Content-Type", "")
                else raw.decode()
            )
            capture = {
                "path": path,
                "credential": credential or "missing",
                "status": response.status,
                "content_type": response.headers.get("Content-Type", ""),
                "sha256": hashlib.sha256(raw).hexdigest(),
                "raw_base64": base64.b64encode(raw).decode(),
            }
            if response.status >= 400 and path.split("?", 1)[0] in (
                "/api/ard/agents",
                "/api/ard/search",
            ):
                capture["schema_errors"] = validate(document, "Error")
            self.captures.append(capture)
            assert response.status == status, (
                f"native {path} returned {response.status}, expected {status}: {raw[:500]!r}"
            )
            return document

    def walk(self, kind, wanted, credential="admin", filters=None):
        token, seen, tokens, schemas = "", set(), [], []
        for _ in range(8):
            if kind == "list":
                params = [("pageSize", "1")]
                if token:
                    params.append(("pageToken", token))
                params.extend(
                    ("filter", k + "=" + v) for k, v in (filters or {}).items()
                )
                page = self.request(
                    "/api/ard/agents?" + urllib.parse.urlencode(params),
                    credential=credential,
                )
                rows = page["items"]
                assert type(page["total"]) is int and page["total"] == len(wanted), (
                    "native List total changed"
                )
                schemas.extend(validate(page, "ListResponse"))
            else:
                query = {"text": "qualified"}
                if filters:
                    query["filter"] = filters
                page = self.request(
                    "/api/ard/search",
                    credential=credential,
                    body={
                        "query": query,
                        "pageSize": 1,
                        "federation": "none",
                        **({"pageToken": token} if token else {}),
                    },
                )
                assert page["referrals"] == [], (
                    "native own-index Search returned referrals"
                )
                rows = page["results"]
                schemas.extend(validate(page, "SearchResponse"))
            selected = identities(rows)
            assert len(rows) <= 1 and selected <= wanted and not seen & selected, (
                "native page membership changed"
            )
            for row in rows:
                assert row["type"] == MCP and row["url"].startswith(
                    ORIGIN + "/api/public/servers/"
                ), "native descriptor origin changed"
                if kind == "search":
                    assert (
                        row["source"] == ORIGIN + "/api/ard/search"
                        and type(row["score"]) is int
                    ), "native Search attribution changed"
            seen |= selected
            token = page.get("pageToken")
            if not token:
                break
            assert isinstance(token, str) and token not in tokens, (
                "native continuation cycle"
            )
            tokens.append(token)
        else:
            raise AssertionError("native page count exceeds fixture bound")
        assert seen == wanted, (
            f"native {kind} membership differs: {seen} versus {wanted}"
        )
        return {
            "identifiers": sorted(seen),
            "continuations": tokens,
            "terminal": token,
            "schema_errors": schemas,
        }


def check_registry_nulls(result, kind):
    """Accept only the independently measured native optional-null failures."""
    count = len(result["identifiers"])
    fields = (
        ["capabilities"] if kind == "list" else ["capabilities", "updatedAt", "version"]
    )
    member = "items" if kind == "list" else "results"
    wanted = Counter(
        {("/" + member + "/0/" + field, "type"): count for field in fields}
    )
    wanted[("/pageToken", "type")] = 1
    errors = result["schema_errors"]
    assert Counter((e["path"], e["validator"]) for e in errors) == wanted and all(
        e["null"] for e in errors
    ), "native Registry null serialization changed: " + kind


def check_characterization(evidence, captures):
    """Require the measured pinned outcomes, not any arbitrary conformance failure."""
    assert evidence["publisher"]["schema_errors"] == [], (
        "native manifest schema changed"
    )
    for descriptor in evidence["descriptors"].values():
        assert descriptor["mcp_registry_errors"] == [], (
            "native Registry descriptor schema changed"
        )
        assert [(e["path"], e["validator"]) for e in descriptor["mcp_card_errors"]] == [
            ("/$schema", "pattern")
        ], "native advertised MCP Card mismatch changed"
    for name in ("list", "search"):
        for suffix in (
            "",
            "_reader",
            "_private",
            "_types",
            "_media",
            "_tag",
            "_after_denial",
        ):
            check_registry_nulls(evidence[name + suffix], name)
    for name in ("list_no_match", "no_match"):
        assert [
            (e["path"], e["validator"], e["null"])
            for e in evidence[name]["schema_errors"]
        ] == [("/pageToken", "type", True)], (
            "native empty response schema changed: " + name
        )
    refusals = [
        c for c in captures if c["status"] in (400, 401) and "schema_errors" in c
    ]
    assert len(refusals) == 7 and all(c["schema_errors"] == [] for c in refusals), (
        "native refusal envelopes changed"
    )
    for name, code, marker, summary in (
        (
            "manifest",
            0,
            "CONFORMANCE STATUS: PASS",
            "Validated with 0 critical specification errors and 1 warnings.",
        ),
        (
            "publisher",
            0,
            "predecessor path",
            "Validated with 0 critical specification errors and 2 warnings.",
        ),
        (
            "registry",
            1,
            "POST /explore returned server error: 405",
            "Found 1 critical specification errors.",
        ),
    ):
        result = evidence["official"][name]
        assert (
            result["exit"] == code
            and marker in result["output"]
            and summary in result["output"]
        ), "official native outcome changed: " + name


def qualify(api):
    evidence = api.evidence
    untrusted = urllib.request.build_opener(
        urllib.request.ProxyHandler({}),
        NoRedirect(),
        urllib.request.HTTPSHandler(context=ssl.create_default_context()),
    )
    try:
        with untrusted.open(ORIGIN + "/.well-known/ai-catalog.json", timeout=10):
            raise AssertionError("native private issuer was accepted without its CA")
    except urllib.error.URLError as error:
        assert isinstance(error.reason, ssl.SSLCertVerificationError), (
            "untrusted TLS control failed for another reason"
        )
    evidence["tls"] = "private-issuer-refused-without-ca"
    # The native initialization script seeds privileged scope mappings before either service starts.
    evidence["groups"] = {}
    for scope, names in (
        ("qualified-reader", ["qualified-alpha"]),
        ("qualified-private", ["qualified-private"]),
    ):
        imported = api.request(
            "/api/servers/groups/import",
            body={
                "scope_name": scope,
                "group_mappings": [scope],
                "ui_permissions": {"list_service": names},
                "server_access": [
                    {
                        "server": name,
                        "methods": ["initialize", "tools/list"],
                        "tools": [],
                    }
                    for name in names
                ],
                "create_in_idp": False,
            },
        )
        assert (
            imported["auth_server_reloaded"] is True
            and imported["idp_created"] is False
        ), "native scopes did not reload or attempted IdP provisioning"
        evidence["groups"][scope] = imported
    for name, visibility in (
        ("alpha", "public"),
        ("beta", "public"),
        ("disabled", "public"),
        ("private", "group-restricted"),
    ):
        fields = {
            "name": "qualified-" + name,
            "description": "qualified " + name + " metadata fixture",
            "path": "/qualified-" + name,
            "tags": "qualified," + name,
            "license": "Apache-2.0",
            "version": "1.0.0",
            "deployment": "local",
            "local_runtime": json.dumps(
                {
                    "type": "command",
                    "package": "/not-installed/qualification-only",
                    "args": [],
                    "env": {},
                    "required_env": [],
                }
            ),
            "visibility": visibility,
        }
        if visibility == "group-restricted":
            fields["allowed_groups"] = "qualified-private"
        api.request("/api/servers/register", form=fields, status=201)
        toggled = api.request(
            "/api/servers/toggle",
            form={
                "path": fields["path"],
                "new_state": "false" if name == "disabled" else "true",
            },
        )
        assert toggled["new_enabled_state"] is (name != "disabled"), (
            "native enabled state differs"
        )
    api.request("/.well-known/ard.json", credential="", status=404)
    evidence["current_discovery"] = "absent-404"
    manifest = api.request("/.well-known/ai-catalog.json", credential="")
    assert len(manifest["entries"]) == 3, (
        "native Publisher contains unexpected seed records"
    )
    resources = [entry for entry in manifest["entries"] if entry["type"] == MCP]
    assert identities(resources) == expected("qualified-alpha", "qualified-beta"), (
        "Publisher leaked private/disabled membership"
    )
    registries = [
        entry
        for entry in manifest["entries"]
        if entry["type"] == "application/ai-registry+json"
    ]
    assert len(registries) == 1 and registries[0]["url"] == ORIGIN + "/api/ard", (
        "native Registry advertisement differs"
    )
    assert manifest["host"]["trustManifest"]["identity"] == ORIGIN, (
        "native Publisher identity differs"
    )
    evidence["descriptors"] = {}
    for entry in resources:
        assert entry["url"].startswith(ORIGIN + "/api/public/servers/"), (
            "Publisher used a noncanonical origin"
        )
        document = api.request(urllib.parse.urlsplit(entry["url"]).path, credential="")
        evidence["descriptors"][entry["identifier"]] = {
            "advertised_type": entry["type"],
            "declared_schema": document.get("$schema"),
            "mcp_card_errors": descriptor_errors(document, "mcp-card"),
            "mcp_registry_errors": descriptor_errors(document, "mcp-registry"),
        }
    evidence["publisher"] = {
        "identifiers": sorted(identities(resources)),
        "schema_errors": validate(manifest, "ArdManifest"),
    }
    for name in ("private", "disabled"):
        api.request(
            "/api/public/servers/qualified-" + name + "/server.json",
            credential="",
            status=404,
        )
    all_enabled = expected("qualified-alpha", "qualified-beta", "qualified-private")
    for kind in ("list", "search"):
        evidence[kind] = api.walk(kind, all_enabled)
        evidence[kind + "_reader"] = api.walk(
            kind, expected("qualified-alpha"), credential="reader"
        )
        evidence[kind + "_private"] = api.walk(
            kind, expected("qualified-private"), credential="private"
        )
        short = api.walk(kind, all_enabled, filters={"type": "mcp_server"})
        media = api.walk(kind, all_enabled, filters={"type": MCP})
        assert short["identifiers"] == media["identifiers"], (
            "native short/media filters differ"
        )
        evidence[kind + "_types"] = short
        evidence[kind + "_media"] = media
        evidence[kind + "_tag"] = api.walk(
            kind, expected("qualified-alpha"), filters={"tags": "alpha"}
        )
    for credential in ("", "invalid"):
        api.request("/api/ard/agents", credential=credential, status=401)
        api.request(
            "/api/ard/search",
            credential=credential,
            body={"query": {"text": "qualified"}},
            status=401,
        )
    api.request(
        "/api/servers/toggle",
        credential="reader",
        form={"path": "/qualified-alpha", "new_state": "false"},
        status=403,
    )
    # A refused mutation must preserve both discovery views and their raw schema outcomes.
    for kind in ("list", "search"):
        evidence[kind + "_after_denial"] = api.walk(kind, all_enabled)
    evidence["list_no_match"] = api.walk(
        "list", set(), filters={"tags": "nonexistentzzzz"}
    )
    absent = api.request(
        "/api/ard/search",
        body={"query": {"text": "nonexistentzzzz"}, "federation": "none"},
    )
    assert absent["results"] == [] and not absent.get("pageToken"), (
        "native no-match Search differs"
    )
    evidence["no_match"] = {
        "results": absent["results"],
        "schema_errors": validate(absent, "SearchResponse"),
    }
    api.request("/api/ard/agents?filter=type%3Dunsupported", status=400)
    api.request(
        "/api/ard/search",
        body={"query": {"text": "qualified", "filter": {"unknown": "x"}}},
        status=400,
    )
    api.request("/api/ard/agents?pageToken=not-base64", status=400)
    api.request("/api/ard/explore", body={"query": {}}, status=405)
    evidence["explore"] = "absent-405"
    checker = DIRECTORY / "conformance/bin/conformance-test"
    checker_env = {
        "PATH": os.environ["PATH"],
        "SSL_CERT_FILE": "/trust/ca.pem",
        "PYTHONOPTIMIZE": "0",
        "PYTHONDONTWRITEBYTECODE": "1",
    }
    evidence["official"] = {}
    for name, arguments in (
        ("manifest", ["manifest", ORIGIN + "/.well-known/ai-catalog.json"]),
        ("publisher", ["publisher", ORIGIN.removeprefix("https://")]),
        (
            "registry",
            [
                "registry",
                ORIGIN + "/api/ard",
                "--header",
                "Authorization: Bearer " + api.secrets["admin"],
            ],
        ),
    ):
        completed = run(
            [sys.executable, str(checker), *arguments],
            timeout=45,
            limit=131072,
            env=checker_env,
            check=False,
        )
        output = completed.stdout + completed.stderr
        assert completed.code in (0, 1), "incomplete official conformance execution"
        assert (
            "CONFORMANCE STATUS:" in output
            and "Skipping" not in output
            and "Failed to run JSON Schema validator" not in output
        ), "incomplete official schema/checker execution"
        evidence["official"][name] = {"exit": completed.code, "output": output}
    check_characterization(evidence, api.captures)
    evidence["characterization"] = "exact-pinned-schema-and-checker-outcomes"
    return evidence


def main():
    api = Native(json.loads(Path("/credentials/keys.json").read_text()))
    report = {"status": "fail", "captures": api.captures, "evidence": api.evidence}
    try:
        if not __debug__:
            raise ValueError("native qualification requires assertions")
        report["evidence"] = qualify(api)
        report["status"] = "pass"
    except Exception as error:
        report["error"] = type(error).__name__ + ": " + str(error)
    raw = json.dumps(report, sort_keys=True)
    for secret in api.secrets.values():
        raw = raw.replace(secret, "[redacted]")
    print(raw)
    return 0 if report["status"] == "pass" else 1


if __name__ == "__main__":
    sys.exit(main())
