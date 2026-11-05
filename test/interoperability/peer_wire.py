"""Direct bounded HTTPS and schema controls against invocation-owned HF origins."""

import json
import re
import ssl
import time
import urllib.error
import urllib.request

from peer_contract import A2A, MCP, REGISTRY, validate_search


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class PeerHTTP:
    """Only the supervisor-generated origins, using its private CA and no proxy."""

    def __init__(self, origins, certificate):
        self.origins = origins
        context = ssl.create_default_context(cafile=certificate)
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect(),
                                                 urllib.request.HTTPSHandler(context=context))

    def request(self, peer, path, body=None, expected=200):
        if peer not in self.origins or not path.startswith("/") or path.startswith("//"):
            raise ValueError("peer origin/path rejected")
        raw = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request(self.origins[peer] + path, raw, {"Content-Type": "application/json"})
        try:
            response = self.opener.open(req, timeout=5)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            data = response.read(2 * 1024 * 1024 + 1)
            assert len(data) <= 2 * 1024 * 1024, "peer body exceeds bound"
            assert response.status == expected, f"{peer} {path}: expected HTTP {expected}, got {response.status}"
            if not data or expected == 500:
                return None
            return json.loads(data)

    def ready(self):
        for peer in self.origins:
            deadline = time.monotonic() + 30
            while True:
                try:
                    self.request(peer, "/health")
                    break
                except (OSError, AssertionError):
                    if time.monotonic() >= deadline:
                        raise RuntimeError(peer + " HTTPS readiness failed") from None
                    time.sleep(0.2)


def query(**options):
    return {"query": {"text": "challenge"}, "federation": "none", "pageSize": 20, **options}


def qualify(http, validator, record):
    """Independent wire assertions; raw payloads stay synthetic and bounded."""
    manifests = {}
    for peer, count in (("challenge", 8), ("primary", 2)):
        def manifest(peer=peer, count=count):
            http.request(peer, "/.well-known/ard.json", expected=404)
            document = http.request(peer, "/.well-known/ai-catalog.json")
            assert len(document["entries"]) == count
            manifests[peer] = document
            if peer == "challenge":
                response = validate_search(http.request(peer, "/search", query()), validator)
                assert {e["identifier"] for e in response["results"]} == {e["identifier"] for e in document["entries"]}
                assert all(e["source"] == "discover:challenge" for e in response["results"])
                assert not response.get("referrals")
            else:
                http.request(peer, "/search", query(), expected=500)
                empty = http.request(peer, "/search", query(query={"text": "challenge", "filter": {"type": A2A}}))
                validate_search(empty, validator)
                assert empty["results"] == []
            return {"entries": count, "manifest": document, "current_status": 404,
                    "search_status": 200 if peer == "challenge" else 500}
        record("wire-" + peer, manifest)

    def filters():
        rows = []
        for name, selected, count in (
            ("type", {"type": MCP}, 1), ("type-empty", {"type": "application/not-present+json"}, 0),
            ("publisher", {"publisher": "discover.dev"}, 8), ("publisher-empty", {"publisher": "other.example"}, 0),
            ("tag", {"tags": "mcp"}, 1), ("tag-empty", {"tags": "not-present"}, 0),
            ("metadata", {"metadata.sourceType": "challenge-mcp"}, 1),
            ("metadata-empty", {"metadata.sourceType": "not-present"}, 0),
            ("or", {"type": [MCP, A2A]}, 2), ("and", {"type": MCP, "tags": "skill"}, 0),
        ):
            response = validate_search(http.request("challenge", "/search", query(query={"text": "challenge", "filter": selected})), validator)
            entries = response["results"]
            assert len(entries) == count and not response.get("referrals"), name
            for entry in entries:
                for key, value in selected.items():
                    actual = entry.get("metadata", {}).get(key.removeprefix("metadata.")) if key.startswith("metadata.") else entry.get(key)
                    if key == "publisher":
                        actual = entry["identifier"].split(":")[2]
                    allowed = value if isinstance(value, list) else [value]
                    assert any(v in actual if isinstance(actual, list) else v == actual for v in allowed), name
            rows.append({"name": name, "count": count, "identifiers": [e["identifier"] for e in entries]})
        one = validate_search(http.request("challenge", "/search", query(pageSize=1)), validator)
        assert len(one["results"]) == 1
        return {"filters": rows, "page_size_one": 1}
    record("wire-filters", filters)

    def contexts():
        for peer in http.origins:
            q = {"text": "challenge"}
            if peer == "primary":
                q["filter"] = {"type": A2A}
            validate_search(http.request(peer, "/search", query(query=q)), validator)
            rejected = http.request(peer, "/search", query(query={**q, "@context": {}}), expected=422)
            assert any(item.get("loc") == ["body", "query", "@context"] and item.get("type") == "extra_forbidden" for item in rejected["detail"])
        return {"challenge": 422, "primary": 422, "paired_controls": 200, "field": "query.@context"}
    record("wire-context", contexts)

    def referrals():
        root = validate_search(http.request("challenge", "/search", query(federation="referrals")), validator)
        nested = validate_search(http.request("challenge", "/registries/nested/search", query()), validator)
        assert {r["url"] for r in root["referrals"]} == {http.origins["challenge"] + f"/registries/{kind}/search" for kind in ("tools", "skills", "nested")}
        assert len(nested["referrals"]) == 1 and nested["referrals"][0]["url"] == http.origins["challenge"] + "/registries/deep/search"
        return {"root_referrals": root["referrals"], "nested_none_referrals": nested["referrals"]}
    record("wire-referrals", referrals)

    def optional():
        for peer in http.origins:
            http.request(peer, "/agents", expected=404)
            http.request(peer, "/explore", query(), expected=501)
        return {"list": 404, "explore": 501, "peers": 2}
    record("wire-optional", optional)

    def bases():
        routes = [e["url"] for e in manifests["challenge"]["entries"] if e["type"] == REGISTRY]
        assert len(routes) == 4
        for url in routes:
            assert url.startswith(http.origins["challenge"] + "/")
            path = url.removeprefix(http.origins["challenge"])
            assert path.endswith("/search")
            http.request("challenge", path + "/search", query(), expected=404)
            validate_search(http.request("challenge", path, query()), validator)
        primary = manifests["primary"]["entries"]
        assert len(primary) == 2 and all(e["type"] == REGISTRY and not e["url"].endswith("/search") for e in primary)
        return {"challenge_route_bases": 4, "primary_operational_bases": 2}
    record("wire-bases", bases)
    return manifests


def checker_evidence(result):
    clean = re.sub(r"\x1b\[[0-9;]*m", "", result.stdout)
    summary = re.search(r"Validated with 0 critical specification errors and (\d+) warnings\.", clean)
    return {"exit_code": result.code, "output": result.stdout, "stderr": result.stderr,
            "summary_warnings": int(summary[1]) if summary else None,
            "warnings": [line.strip() for line in clean.splitlines() if line.strip().startswith("⚠")]}
