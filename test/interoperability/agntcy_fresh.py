"""Native inventory and Catalog qualification in the supervisor-owned fresh cluster."""

import argparse
import hashlib
import json
from pathlib import Path
import signal
import sys
import urllib.parse

from agntcy_contract import DISPOSITIONS, check_cases, document_digest, raw_entry_evidence
from agntcy_lab import ASSETS, LIMIT, LocalHTTP, environment, read_json
import agntcy_imports
import agntcy_runner as mvp
import agntcy_tools
from common import run
from peer_contract import A2A, MCP

ORIGIN = "https://directory-gateway.agntcy-qualification.svc.cluster.local:8443"
UNKNOWN = "baeareiaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"


def loopback(origin, scheme):
    """Only explicit loopback forwards are test-client destinations."""
    parsed = urllib.parse.urlsplit(origin)
    if (parsed.scheme != scheme or parsed.hostname != "127.0.0.1" or not parsed.port
            or parsed.username or parsed.password or parsed.path or parsed.query or parsed.fragment):
        raise ValueError("expected an owned loopback origin")
    return origin


class FreshLab:
    """Keep native client state private; do not select or inspect the retained lab."""

    def __init__(self, settings):
        self.lock = {**read_json(ASSETS / "deployment.json"), "tls_origin": ORIGIN}
        self.certificate = Path(settings["certificate"])
        self.env = environment(Path(settings["private"]))
        self.cli = agntcy_tools.TOOLS / "dirctl-linux-amd64"
        self.grpc = loopback("http://" + settings["grpc"], "http").removeprefix("http://")

    def native(self, *arguments, check=True):
        return run([str(self.cli), "--server-addr", self.grpc, "--auth-mode", "insecure", *arguments],
                   env=self.env, timeout=30, limit=LIMIT, check=check)


class Captures(LocalHTTP):
    """Retain original bounded peer response bytes, without credentials or payload repair."""

    def __init__(self, settings, output):
        origins = {key: loopback(settings[key], scheme) for key, scheme in
                   (("catalog", "http"), ("native", "http"), ("peer", "https"))}
        super().__init__(origins, settings["certificate"], settings["token"])
        self.output, self.captures = output, []

    def request(self, peer, path, body=None, **options):
        raw, headers = super().request(peer, path, body, **options)
        if peer != "catalog":
            if len(self.captures) >= 128:
                raise ValueError("native capture count exceeded")
            name = f"wire-{len(self.captures):03d}-{peer}.json"
            (self.output / name).write_bytes(raw)
            self.captures.append(dict(file=name, peer=peer, path=path, status=options.get("expected", 200),
                sha256=hashlib.sha256(raw).hexdigest(), bytes=len(raw), content_type=headers.get("content-type", "")))
        return raw, headers


def page(api, token="", expression=""):
    parameters = {"pageSize": 1}
    if token:
        parameters["pageToken"] = token
    if expression:
        parameters["filter"] = expression
    return api.document("peer", "/v1/agents?" + urllib.parse.urlencode(parameters))[0]


def walk(api, expected, expression=""):
    """Walk every one-item native page; ordering is not a shared ARD guarantee."""
    entries, tokens, token = {}, [], ""
    for _ in range(8):
        result = page(api, token, expression)
        assert set(result) == {"results", "nextPageToken", "totalCount"}, "native List envelope changed"
        assert type(result["totalCount"]) is int and result["totalCount"] == len(expected), "native total differs"
        rows, following = result["results"], result["nextPageToken"]
        assert isinstance(rows, list) and len(rows) == min(1, len(expected)), "native page size differs"
        assert isinstance(following, str), "native page token is not a string"
        for entry in rows:
            key = entry["identifier"]
            assert key in expected and key not in entries and entry["type"] == expected[key], "missing, duplicate or wrong projection"
            entries[key] = entry
        tokens.append(following)
        if not following:
            break
        assert following not in tokens[:-1], "native continuation cycle"
        token = following
    else:
        raise AssertionError("native pagination exceeds fixture bound")
    assert set(entries) == set(expected), "incomplete native membership"
    return entries, tokens


def inventory(api, lab):
    corpus = read_json(ASSETS / "corpus.json")
    records = []
    for item in corpus:
        path = ASSETS / item["file"]
        native = lab.native("push", str(path), "--output", "raw")
        assert native.stdout.strip() == item["cid"], "deterministic native CID changed: " + item["name"]
        retained = json.loads(lab.native("pull", item["cid"], "--output", "json").stdout)
        assert retained == read_json(path), "native content changed: " + item["name"]
        records.append({**item, "content_sha256": document_digest(retained)})
    rejections = {}
    for name, required in (("missing-required", ('created_at',)),
                           ("skill", ('source_locator', 'validation', 'artifact'))):
        rejected = lab.native("push", str(ASSETS / "corpus" / (name + ".json")), "--output", "raw", check=False)
        assert rejected.code != 0 and "code = InvalidArgument" in rejected.stderr, "server validation did not reject " + name + ": " + rejected.stderr[:4000]
        assert all(field in rejected.stderr for field in required), "server rejection changed " + name + ": " + rejected.stderr[:4000]
        rejections[name] = dict(exit_code=rejected.code, stderr=rejected.stderr[:4000])
    unknown = lab.native("pull", UNKNOWN, "--output", "json", check=False)
    assert unknown.code != 0 and "NotFound" in unknown.stderr, "unknown native record did not fail"
    api.request("peer", "/v1/agents/" + UNKNOWN, expected=404)
    plain = next(item["cid"] for item in corpus if item["name"] == "plain")
    api.request("peer", "/v1/agents/" + plain, expected=404)
    def indexed_native_records():
        rows = json.loads(lab.native("search", "--name", "https://example.test/agntcy-lab/*", "--limit", "7", "--output", "json").stdout)
        assert isinstance(rows, list) and len(rows) == len(set(rows)), "duplicate native indexed records"
        assert set(rows) <= {item["cid"] for item in corpus}, "unselected native indexed record"
        return rows if len(rows) == 6 else None

    indexed = mvp.wait_for(indexed_native_records, "six indexed native records", timeout=60)
    # The plain record is present in both storage and the index, but has no catalog projection.
    api.request("peer", "/v1/agents/" + plain, expected=404)
    expected = {"urn:ai:org.agntcy:cid:" + item["cid"]: item["type"] for item in corpus if item["type"]}
    mvp.wait_for(lambda: True if page(api)["totalCount"] == 5 else None, "six-record indexing", timeout=60)
    entries, tokens = walk(api, expected)
    # Repeated reads compare stable identity/type/content; usage counters can change.
    for item in corpus:
        if item["type"]:
            entry = api.document("peer", "/v1/agents/" + item["cid"])[0]
            assert entry["identifier"] in entries and entry["type"] == item["type"]
            assert entry["data"] == entries[entry["identifier"]]["data"], "detail differs from paginated projection"
    filters = {}
    memberships = {A2A: {key: media for key, media in expected.items() if media != MCP},
                   MCP: {key: media for key, media in expected.items() if media in (MCP, "application/ai-catalog+json")}}
    for name, expression, selected in (
        ("a2a-unquoted", "type = " + A2A, memberships[A2A]),
        ("a2a-double-quoted", 'type = "' + A2A + '"', memberships[A2A]),
        ("a2a-single-quoted", "type = '" + A2A + "'", {}),
        ("mcp-unquoted", "type = " + MCP, memberships[MCP]),
        ("unknown-type", "type = application/absent+json", {}),
    ):
        rows, continuation = walk(api, selected, expression)
        filters[name] = dict(expression=expression, identifiers=sorted(rows), tokens=continuation)
    return dict(records=records, stored=6, indexed=sorted(indexed), projected=5, identifiers=sorted(entries), tokens=tokens,
                unknown_status=404, nonprojectable_status=404, server_rejections=rejections, filters=filters)


def rejected_artifacts(api, lab):
    corpus = {item["name"]: item for item in read_json(ASSETS / "corpus.json")}
    old_card = json.loads(lab.native("export", corpus["a2a"]["cid"], "--format", "a2a").stdout)
    assert old_card == read_json(ASSETS / "corpus/a2a.json")["modules"][0]["data"]["card_data"]
    mcp_data = api.document("peer", "/v1/agents/" + corpus["mcp"]["cid"])[0]["data"]
    assert mcp_data == read_json(ASSETS / "corpus/mcp.json")["modules"][0]["data"]
    current_data = api.document("peer", "/v1/agents/" + corpus["current-a2a"]["cid"])[0]["data"]
    assert current_data == read_json(ASSETS / "record.json")["modules"][0]["data"], "current A2A projection changed payload"
    namespace, _ = api.document("catalog", "/api/v1/namespaces", {"slug": "rejected-artifacts", "displayName": "Peer format controls"}, expected=201)
    outcomes = {}
    for name, kind, media, data, code in (
        ("old-a2a", "agent", A2A, old_card, "A2A_PROTOJSON_INVALID"),
        ("projected-a2a", "agent", A2A, current_data, "A2A_PROTOJSON_INVALID"),
        ("mcp", "mcp_server", MCP, mcp_data, "MCP_SERVER_CARD_SCHEMA_INVALID"),
    ):
        resource, _ = api.document("catalog", "/api/v1/resources", dict(namespaceId=namespace["id"], kind=kind,
            canonicalIdentifier="urn:air:catalog.example.com:default:agntcy-" + name), expected=201)
        result, _ = api.document("catalog", "/api/v1/resources/" + resource["id"] + "/versions", dict(version="1.0.0",
            representations=[dict(role="primary", mediaType=media, data=data)]), expected=201)
        representation = result["revision"]["representations"][0]
        codes = [finding["code"] for finding in result["evidence"]["findings"]]
        assert result["evidence"]["state"] == "blocked" and representation["validationState"] == "invalid" and code in codes, "invalid peer format accepted"
        retained, _ = api.document("catalog", "/api/v1/version-revisions/" + result["revision"]["id"])
        assert retained["revision"]["representations"][0]["data"] == data and retained["evidence"]["state"] == "blocked"
        outcomes[name] = dict(state="blocked", codes=codes, revision_id=result["revision"]["id"])
    return dict(agent=dict(disposition="supported-overlap", rejected_older=outcomes["old-a2a"], rejected_projection=outcomes["projected-a2a"]),
                mcp_server=dict(disposition="rejected", evidence=outcomes["mcp"]),
                skill=dict(disposition="rejected-by-peer-schema", stage="native push"),
                plugin=dict(disposition="unavailable", reason="No selected native Plugin projection"))


def all_raw_entries(api, expected_errors=None):
    schema = read_json(mvp.ARD / "spec/schemas/ard-entry.schema.json")
    results = []
    for item in read_json(ASSETS / "corpus.json"):
        if item["type"]:
            entry = api.document("peer", "/v1/agents/" + item["cid"])[0]
            results.append(raw_entry_evidence(entry, schema, item["cid"], item["type"], expected_errors))
    return dict(entries=5, valid=0, errors=sum(len(row["errors"]) for row in results), results=results)


def execute(settings, output):
    report = dict(status="running", cases=[dict(name=name, status="unrun", disposition=disposition)
                  for name, disposition in DISPOSITIONS.items()])

    def save():
        staged = output / "qualification.json.new"
        staged.write_text(json.dumps(report, indent=2) + "\n")
        staged.chmod(0o600)
        staged.replace(output / "qualification.json")

    save()
    active = None
    api = None
    try:
        if settings["origin"] != ORIGIN:
            raise ValueError("unexpected cluster Directory origin")
        lab, api = FreshLab(settings), Captures(settings, output)
        report["peer"] = dict(release=lab.lock["release"], revision=lab.lock["revision"], expected=lab.lock["expected"])
        for case in report["cases"]:
            case["disposition"] = lab.lock["expected"]["dispositions"][case["name"]]
        report["inventory"] = inventory(api, lab)
        card, reused = mvp.native_artifact(lab)
        assert reused, "current seed absent after native inventory"
        for case in report["cases"]:
            active = case
            print("AGNTCY qualification: " + case["name"], flush=True)
            if case["name"] == "artifact":
                case["evidence"] = mvp.artifact(api, lab, card)
                report["kinds"] = rejected_artifacts(api, lab)
            elif case["name"] == "discovery":
                case["evidence"] = mvp.discovery(api, lab, "functional-fixture")
            elif case["name"] == "search":
                case["evidence"] = mvp.search(api, lab, "functional-fixture")
            else:
                case["evidence"] = mvp.conformance(api, lab, output, settings["peer"])
                report["raw_entries"] = all_raw_entries(api, lab.lock["expected"]["entry_errors"])
            case["status"] = "pass"
            save()
        check_cases(report["cases"], lab.lock["cid"], lab.lock["expected"])
        active = None
        report["native_imports"] = {}
        agntcy_imports.qualify(api, lab, Path(settings["private"]), output, report["native_imports"], save)
        if settings.get("trust") == "true":
            counted = api.document("peer", "/_qualification/requests")[0]
            assert counted["overflow"] is False and counted["evidenceRequests"] == 0
            # A known rejected route proves the observer is active.
            api.request("peer", "/checkpoint", expected=404)
            after = api.document("peer", "/_qualification/requests")[0]
            assert after["evidenceRequests"] == 1
            report["trust_evidence_requests"] = dict(discovery=0, control=1)
        report["status"] = "pass"
        return 0
    except (Exception, KeyboardInterrupt) as error:  # Preserve failed and unrun groups for the owning supervisor.
        if active is not None:
            active["status"] = "fail"
        elif report.get("native_imports", {}).get("status") == "running":
            report["native_imports"]["status"] = "fail"
            for case in report["native_imports"].get("cases", []):
                if case["status"] == "running":
                    case["status"] = "fail"
        report["status"], report["error"] = "fail", str(error).replace(settings.get("token", "unselected"), "[redacted]")[:4000]
        print(report["error"], file=sys.stderr)
        return 1
    finally:
        if api is not None:
            report["captures"] = api.captures
        save()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("settings", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    if not __debug__:
        parser.error("qualification requires Python assertions")
    # The Go supervisor owns cluster/process cleanup; interrupt bounded native children first.
    def interrupted(_number, _frame):
        raise KeyboardInterrupt("owned qualification interrupted")
    signal.signal(signal.SIGTERM, interrupted)
    return execute(read_json(args.settings), args.output)


if __name__ == "__main__":
    raise SystemExit(main())
