"""Exercise unchanged native ANS APIs through invocation-owned loopback forwards."""

import argparse
from collections import Counter
import hashlib
import json
import os
from pathlib import Path
import ssl
import sys
import tempfile
import time
import traceback
import urllib.error
import urllib.parse
import urllib.request

import jsonschema
from referencing import Registry, Resource

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[2]
sys.path.insert(0, str(HERE.parent))
from common import run  # noqa: E402
from agntcy_lab import NoRedirect  # noqa: E402

PROFILE = ROOT / "schemas/upstream/ard/ard-0.91-aa3e598"
API_SCHEMA = None
MCP, A2A = "application/mcp-server-card+json", "application/a2a-agent-card+json"


def validate(instance, definition):
    """Resolve pinned local schemas only, with strict formats and no remote retriever."""
    entry = json.loads((PROFILE / "spec/schemas/ard-entry.schema.json").read_text())
    api = API_SCHEMA or {}
    uri = "urn:ans-test:entry"

    def rewrite(value):
        if isinstance(value, dict):
            return {key: uri + "#" + item.split("#", 1)[1]
                    if key == "$ref" and item.startswith("./ard-entry.schema.json#") else rewrite(item)
                    for key, item in value.items()}
        if isinstance(value, list):
            return [rewrite(item) for item in value]
        return value

    api = rewrite(api)
    api["$schema"] = entry["$schema"] = "https://json-schema.org/draft/2020-12/schema"
    root = entry if definition in ("ArdEntry", "EntryFields") else api
    ref = "#/$defs/" + definition if root is entry else "#/components/schemas/" + definition
    registry = Registry().with_resources([(uri, Resource.from_contents(entry))])
    validator = jsonschema.Draft202012Validator({**root, "$ref": ref}, registry=registry, format_checker=jsonschema.FormatChecker())
    return [{"path": "/" + "/".join(map(str, e.absolute_path)), "rule": e.validator, "message": e.message}
            for e in sorted(validator.iter_errors(instance), key=lambda e: str(e.absolute_path))]


class Native:
    def __init__(self, settings):
        self.settings, self.captures = settings, []
        for name in ("ra", "tl", "finder", "finder-edge"):
            parsed = urllib.parse.urlsplit(settings[name])
            if (parsed.hostname != "127.0.0.1" or not parsed.port or parsed.path or parsed.query or parsed.fragment
                    or parsed.username or parsed.password or parsed.scheme != ("https" if name == "finder-edge" else "http")):
                raise ValueError("ANS client requires an owned loopback forward")
        self.http = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect(),
            urllib.request.HTTPSHandler(context=ssl.create_default_context(cafile=settings["ca"])))

    def request(self, peer, path, body=None, *, expected=200, auth=False):
        if peer not in ("ra", "tl", "finder", "finder-edge") or not path.startswith("/") or path.startswith("//") or "\\" in path:
            raise ValueError("unselected ANS endpoint")
        headers = {"Accept": "application/json"}
        if auth:
            if peer == "ra":
                credential = self.settings["key"]
            elif peer == "tl" and path == "/internal/v1/producer-keys" and body is not None and self.settings.get("tl_key"):
                credential = self.settings["tl_key"]
            else:
                raise ValueError("RA credential cannot leave the selected registration service")
            headers["Authorization"] = "Bearer " + credential
        if body is not None:
            headers["Content-Type"] = "application/json"
        request = urllib.request.Request(self.settings[peer] + path, None if body is None else json.dumps(body).encode(), headers)
        try:
            response = self.http.open(request, timeout=12)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            raw = response.read((2 << 20) + 1)
            if len(raw) > 2 << 20:
                raise ValueError("ANS response exceeds the fixture bound")
            self.captures.append({"peer": peer, "path": path, "status": response.status, "sha256": hashlib.sha256(raw).hexdigest()})
            assert response.status == expected, f"{peer} {path}: expected {expected}, got {response.status}: {raw[:1500]!r}"
            return raw

    def document(self, *args, **kwargs):
        return json.loads(self.request(*args, **kwargs))


def seed_definition(name, protocol, *, shared=False):
    host = name + ".ans.test"
    endpoint = {"agentUrl": "https://" + host + "/invoke", "metaDataUrl": "https://" + host + "/.well-known/card.json",
                "protocol": protocol, "transports": ["JSON-RPC" if protocol == "A2A" else "REST" if protocol == "HTTP-API" else "SSE"],
                "functions": [{"id": "forecast", "name": "Forecast", "tags": ["weather", name]}]}
    endpoints = [endpoint]
    if shared:
        endpoint["functions"] = [{"id": "shared", "name": "SharedFunction", "tags": ["shared"]}]
        endpoints.append({**endpoint, "protocol": "A2A", "metaDataUrl": "https://" + host + "/.well-known/agent-card.json", "transports": ["JSON-RPC"]})
    return {"agentDisplayName": name, "agentDescription": ("endpoint compatibility shared" if shared else "order weather forecast tools baseline " + name), "version": "1.0.0", "agentHost": host, "endpoints": endpoints}


def csr(host, directory, name):
    key, request = directory / (name + ".key"), directory / (name + ".csr")
    common_name = "ans://v1.0.0." + host if name == "identity" else host
    san = "URI:" + common_name if name == "identity" else "DNS:" + host
    config = directory / (name + ".cnf")
    config.write_text("[req]\ndistinguished_name=dn\nreq_extensions=ext\nprompt=no\n[dn]\nCN=" + common_name + "\n[ext]\nsubjectAltName=" + san + "\n")
    run(["openssl", "req", "-new", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256", "-nodes",
         "-keyout", str(key), "-out", str(request), "-config", str(config)], timeout=15)
    key.chmod(0o600)
    return request.read_text()


def register(api, definition, directory):
    with tempfile.TemporaryDirectory(prefix="csr-", dir=directory) as temporary:
        request = {**definition, "identityCsrPEM": csr(definition["agentHost"], Path(temporary), "identity"),
                   "serverCsrPEM": csr(definition["agentHost"], Path(temporary), "server")}
        pending = api.document("ra", "/v1/agents/register", request, auth=True, expected=202)
    identifier = pending["agentId"]
    api.document("ra", "/v1/agents/" + identifier + "/verify-acme", {}, auth=True, expected=202)
    api.document("ra", "/v1/agents/" + identifier + "/verify-dns", {}, auth=True, expected=202)
    active = api.document("ra", "/v1/agents/" + identifier, auth=True)
    assert active["agentStatus"] == "ACTIVE", "native registration did not become ACTIVE"
    return {"agentId": identifier, "host": definition["agentHost"], "status": active["agentStatus"],
            "definition": definition, "ansName": pending["ansName"]}


def traverse(api, query, *, peer="finder-edge"):
    results, tokens, pages = [], set(), []
    request = {"query": query, "federation": "none", "pageSize": 100}
    for _ in range(10):
        raw = api.request(peer, "/v1/search", request)
        document = json.loads(raw)
        assert not validate(document, "SearchResponse"), "native SearchResponse violates pinned schema"
        assert len(document["results"]) <= 1, "configured maximum page size was not enforced"
        results.extend(document["results"])
        pages.append(document)
        token = document.get("pageToken")
        if not token:
            return results, pages
        assert token not in tokens, "native continuation repeated"
        tokens.add(token)
        request["pageToken"] = token
    raise AssertionError("native page traversal exceeded its bound")


def assert_entries(entries, registrations, settings):
    expected = {"urn:air:" + row["host"] + ":agents:" + row["definition"]["agentDisplayName"]: row
                for row in registrations if row["definition"]["endpoints"][0]["protocol"] != "HTTP-API"}
    actual = [entry["identifier"] for entry in entries]
    assert len(actual) == len(set(actual)) and set(actual) == set(expected), "native exact membership differs"
    for entry in entries:
        row = expected[entry["identifier"]]
        endpoint = row["definition"]["endpoints"][0]
        assert not validate(entry, "ArdEntry"), "native entry violates pinned schema"
        assert entry["version"] == "1.0.0" and entry["url"] == endpoint["metaDataUrl"]
        assert entry["type"] == (MCP if endpoint["protocol"] == "MCP" else A2A)
        assert entry["source"] == settings["origin"] and 0 <= entry["score"] <= 100
        assert entry["tags"] == sorted(["weather", row["definition"]["agentDisplayName"]]) and entry["capabilities"] == ["Forecast"]
        assert entry["metadata"]["agentUrl"] == endpoint["agentUrl"] and entry["metadata"]["ansName"] == row["ansName"]
        assert entry["metadata"]["logId"]
        assert entry["trustManifest"] == {"identity": "https://" + row["host"], "identityType": "https", "attestations": [{"type": "ANS-Registration",
            "uri": settings["tl_origin"] + "/v1/agents/" + row["agentId"] + "/receipt", "mediaType": "application/scitt-receipt+cose"}]}


def qualify(api, settings):
    directory = Path(settings["output"])
    phases = {}
    api.request("ra", "/v1/agents/not-a-real-id", expected=401)
    registrations = [register(api, seed_definition(name, protocol), directory) for name, protocol in
                     (("alpha", "MCP"), ("beta", "MCP"), ("gamma", "A2A"), ("excluded", "HTTP-API"))]
    phases["registrations"] = registrations
    deadline = time.monotonic() + 90
    while True:
        feed = api.document("ra", "/v1/agents/events?limit=100")
        registered = {event["agentId"]: event for event in feed["items"] if event["eventType"] == "AGENT_REGISTERED"}
        if set(registered) == {row["agentId"] for row in registrations}:
            break
        assert time.monotonic() < deadline, "TL-acknowledged events did not arrive"
        time.sleep(0.5)
    for row in registrations:
        event = registered[row["agentId"]]
        assert event["agentHost"] == row["host"] and event["version"] == "1.0.0" and event["ansName"] == row["ansName"]
        assert event["agentDisplayName"] == row["definition"]["agentDisplayName"] and event["endpoints"] == row["definition"]["endpoints"]
    phases["feed"] = list(registered.values())
    while True:
        entries, pages = traverse(api, {"text": "weather"})
        if len(entries) == 3:
            break
        assert time.monotonic() < deadline, "Finder did not ingest three projected identities"
        time.sleep(0.5)
    assert_entries(entries, registrations, settings)
    for entry in entries:
        row = next(row for row in registrations if row["ansName"] == entry["metadata"]["ansName"])
        assert entry["metadata"]["logId"] == registered[row["agentId"]]["logId"]
    phases["search"] = pages
    # Query the exact same stable input through the native listener and HTTPS terminator.
    request = {"query": {"text": "weather"}, "federation": "none", "pageSize": 1}
    direct = api.request("finder", "/v1/search", request)
    assert direct == api.request("finder-edge", "/v1/search", request), "HTTPS edge changed native response bytes"
    untrusted = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    try:
        untrusted.open(settings["finder-edge"] + "/healthz", timeout=3)
    except urllib.error.URLError as error:
        assert isinstance(error.reason, ssl.SSLCertVerificationError), "TLS negative failed for another reason"
    else:
        raise AssertionError("private TLS unexpectedly trusted")
    phases["tls"] = {"byte_exact": True, "untrusted_rejected": True}
    phases["filters"] = {}
    for key, value, expected_names in (("type", MCP, ["alpha", "beta"]), ("type", A2A, ["gamma"]), ("tags", "alpha", ["alpha"]), ("capabilities", "Forecast", ["alpha", "beta", "gamma"])):
        filtered, _ = traverse(api, {"text": "weather", "filter": {key: [value]}})
        assert {e["displayName"] for e in filtered} == set(expected_names), "native filter membership differs"
        phases["filters"][key + ":" + value] = filtered
    empty, _ = traverse(api, {"text": "doesnotexistqualification"})
    assert empty == []
    facets = {"query": {"text": "weather"}, "resultType": {"facets": [{"field": "type"}, {"field": "tags"}, {"field": "capabilities"}]}}
    explore = api.document("finder-edge", "/v1/explore", facets)
    assert not validate(explore, "ExploreResponse")
    expected_facets = {"type": Counter({MCP: 2, A2A: 1}), "tags": Counter({"weather": 3, "alpha": 1, "beta": 1, "gamma": 1}), "capabilities": Counter({"Forecast": 3})}
    for field, counts in expected_facets.items():
        assert Counter({b["value"]: b["count"] for b in explore["facets"][field]["buckets"]}) == counts
        assert not explore["facets"][field].get("otherCount")
    phases["explore"] = explore
    malformed = api.document("finder-edge", "/v1/search", {"query": {"text": ""}}, expected=400)
    valid_context = {"query": {"text": "weather", "@context": {"identifier": "urn:example:identifier"}}}
    assert not validate(valid_context, "SearchRequest"), "context control is not valid ARD input"
    rejected_context = api.document("finder-edge", "/v1/search", valid_context, expected=400)
    assert malformed["code"] == "INVALID_ARGUMENT"
    assert rejected_context["code"] == "INVALID_ARGUMENT" and 'unknown field "@context"' in rejected_context["detail"]
    token = pages[0]["pageToken"]
    mismatch = api.document("finder-edge", "/v1/search", {"query": {"text": "alpha"}, "pageToken": token}, expected=400)
    unsupported = {path: api.request("finder", path, expected=404).decode() for path in ("/.well-known/ard.json", "/.well-known/ai-catalog.json", "/v1/agents")}
    phases["negative"] = {"no_match": empty, "malformed": malformed, "context": rejected_context, "query_token": mismatch, "unavailable": unsupported}
    calibration = validate({"results": [{"identifier": "invalid", "score": -1, "source": "invalid"}]}, "SearchResponse")
    assert calibration, "schema validation accepted deliberately invalid evidence"
    phases["schemas"] = {"entries": 3, "search_pages": len(pages), "explore": "pass", "invalid_rejected": calibration,
                           "trust_note": "lowercase trustManifest remains an extension; receipt metadata is not proof verification"}
    env = {"PATH": os.environ["PATH"], "SSL_CERT_FILE": settings["ca"], "PYTHONOPTIMIZE": "0", "PYTHONDONTWRITEBYTECODE": "1"}
    checked = run([sys.executable, str(PROFILE / "conformance/bin/conformance-test"), "registry", settings["finder-edge"] + "/v1"], timeout=45, limit=131072, env=env, check=False)
    output = checked.stdout + checked.stderr
    assert checked.code == 0 and "CONFORMANCE STATUS:" in output and "Skipping" not in output, "native official Registry checker did not complete successfully: " + output
    phases["official"] = {"registry": {"exit": checked.code, "output": output}, "manifest": "unavailable (404)", "publisher": "unavailable (404)"}
    shared = register(api, seed_definition("shared", "MCP", shared=True), directory)
    deadline = time.monotonic() + 60
    while True:
        shared_entries, _ = traverse(api, {"text": "shared"})
        if len(shared_entries) == 2:
            break
        assert time.monotonic() < deadline, "shared native identity projection did not arrive"
        time.sleep(0.5)
    assert {e["identifier"] for e in shared_entries} == {"urn:air:" + shared["host"] + ":agents:shared"}
    assert {e["type"] for e in shared_entries} == {MCP, A2A} and {e["version"] for e in shared_entries} == {"1.0.0"}
    phases["shared_identity"] = {"registration": shared, "entries": shared_entries, "disposition": "two endpoint projections share one identity/version; no fabricated identifiers"}
    return {"status": "pass", "counts": {"registered": 4, "discoverable": 3, "http_api_excluded": 1, "shared_registration": 1, "shared_projections": 2}, "phases": phases, "captures": api.captures}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--settings", required=True, type=Path)
    args = parser.parse_args()
    settings = json.loads(args.settings.read_text())
    global API_SCHEMA
    API_SCHEMA = json.loads(Path(settings["schema"]).read_text())
    api = Native(settings)
    try:
        if not __debug__:
            raise ValueError("ANS assertions must be enabled")
        report = qualify(api, settings)
    except (AssertionError, OSError, ValueError, KeyError, RuntimeError) as error:
        report = {"status": "fail", "error": f"{type(error).__name__} at native.py:{traceback.extract_tb(error.__traceback__)[-1].lineno}: {error}", "captures": api.captures}
    raw = json.dumps(report, indent=2)
    destination = Path(settings["output"]) / "qualification.json"
    destination.write_text(raw + "\n")
    destination.chmod(0o600)
    print(raw)
    sys.exit(0 if report["status"] == "pass" else 1)


if __name__ == "__main__":
    main()
