"""Isolated campaign evidence decisions; no Catalog, client, subprocess or network."""

import copy
import json
import hashlib
import unittest

from pi_campaign import validate_campaign


def fixture():
    mcp = "application/mcp-server-card+json"
    entries = [
        {"identifier": "urn:air:fixture.example:mcp:one", "type": mcp},
        {"identifier": "urn:air:fixture.example:mcp:two", "type": mcp},
        {"identifier": "urn:air:fixture.example:agent:one", "type": "application/a2a-agent-card+json"},
        {"identifier": "urn:air:fixture.example:agent:two", "type": "application/a2a-agent-card+json"},
        {"identifier": "urn:air:fixture.example:skill:one", "type": 'text/markdown; profile="urn:air:agent-skills"'},
        {"identifier": "urn:air:fixture.example:skill:two", "type": 'text/markdown; profile="urn:air:agent-skills"'},
        {"identifier": "urn:air:fixture.example:registry:one", "type": "application/ai-registry+json"},
    ]
    manifest = {"entries": entries}
    query = {"text": "order", "filter": {"type": [mcp, "application/mcp-server+json"]}}
    first = {"query": query, "federation": "referrals", "pageSize": 1}
    second = dict(first, pageToken="actual-current-token")
    source = "http://127.0.0.1:8080/ard/v1/"

    def http(method, path, request, status, response):
        return {
            "method": method,
            "path": path,
            "request": request,
            "status": status,
            "response": json.dumps(response),
            "authorization": False,
        }

    real = [
        http("GET", "/.well-known/ard.json", None, 200, manifest),
        http(
            "POST",
            "/ard/v1/search",
            first,
            200,
            {"results": [dict(entries[0], source=source)], "pageToken": "actual-current-token"},
        ),
        http("POST", "/ard/v1/search", second, 200, {"results": [dict(entries[1], source=source)]}),
        http(
            "POST",
            "/ard/v1/search",
            dict(second, query=dict(query, text="unrelated")),
            400,
            {"errorCode": "INVALID_ARGUMENT"},
        ),
        http("GET", "/api/v1/catalog/resources/owned", None, 401, {"code": "UNAUTHENTICATED"}),
    ]
    groups = {
        "ard_catalog": {"catalog": False, "private-read": True, "untrusted-catalog": True},
        "ard_install": {"declined": False, "unknown": True, "canonical-skill": False, "no-ui": True},
        "ard_search": {
            "search-first": False,
            "search-next": False,
            "changed-query": True,
            "unconfigured": True,
            "unsafe-url": True,
            "synthetic": False,
            "forbidden": True,
            "bad-json": True,
            "missing-results": True,
            "timeout": True,
            "synthetic-no-ui": False,
        },
    }
    return {
        "isolation": {
            "readOnlyRoot": True,
            "network": "loopback-only",
            "externalConnectErrno": 101,
            "memoryMax": 2147483648,
            "cpuCores": 2,
            "tasksMax": 256,
            "swapMax": 0,
            "fileLimit": 8388608,
            "openFiles": 256,
        },
        "native": {
            "cases": [
                {
                    "case": name,
                    "tool": tool,
                    "text": "native outcome",
                    "isError": error,
                    "durationMs": 15000 if name == "timeout" else 10,
                }
                for tool, cases in groups.items()
                for name, error in cases.items()
            ],
            "toolCalls": 18,
            "confirms": [{"title": "UNTRUSTED", "body": "owned confirmation"}],
            "activeTools": ["ard_catalog", "ard_install", "ard_search"],
            "fetchUnchanged": True,
            "dispatcherVersion": "8.12.3",
            "offline": True,
            "httpTimeoutMs": 15000,
            "httpBytes": 1000000,
            "artifactFiles": 0,
            "mcpRegistrations": 0,
        },
        "resources": [{"identifier": e["identifier"]} for e in entries[:6]],
        "manifest": manifest,
        "real": real,
        "synthetic": [
            {"path": p, "authorization": False}
            for p in ("/synthetic", "/synthetic", "/forbidden", "/bad-json", "/missing-results", "/timeout")
        ],
        "snapshotStable": True,
        "installState": {
            agent: {name: False for name in ("skills", "mcp.json", "pi-ard/lock.json")} for agent in ("agent", "no-ui")
        },
        "snapshotETag": '"owned-etag"',
        "snapshotDigest": hashlib.sha256(json.dumps(manifest).encode()).hexdigest(),
        "catalogCounters": [
            ["GET", "/.well-known/ard.json", "2xx", 1],
            ["GET", "/api/v1/catalog/resources/{catalogRecordId}", "4xx", 1],
            ["POST", "/ard/v1/search", "2xx", 2],
            ["POST", "/ard/v1/search", "4xx", 1],
        ],
    }


class CampaignUnits(unittest.TestCase):
    def test_complete_membership_http_and_native_evidence(self):
        value = fixture()
        self.assertIs(validate_campaign(value), value)

    def test_token_query_alias_and_membership_changes_are_refused(self):
        for mutation in ("token", "query", "alias", "duplicate", "source", "private"):
            value = fixture()
            if mutation == "token":
                value["real"][2]["request"]["pageToken"] = "reconstructed-token"
            elif mutation == "query":
                value["real"][2]["request"] = dict(value["real"][2]["request"], pageSize=2)
            elif mutation == "alias":
                value["real"][1]["request"]["query"]["filter"]["type"] = ["application/mcp-server-card+json"]
            elif mutation in ("duplicate", "source"):
                result = json.loads(value["real"][2]["response"])
                result["results"][0]["identifier" if mutation == "duplicate" else "source"] = (
                    value["manifest"]["entries"][0]["identifier"] if mutation == "duplicate" else "http://wrong.invalid"
                )
                value["real"][2]["response"] = json.dumps(result)
            else:
                value["real"][-1]["status"] = 200
            with self.subTest(mutation=mutation), self.assertRaises(ValueError):
                validate_campaign(value)

    def test_missing_cases_authority_side_effects_and_isolation_fail(self):
        for mutation in (
            "case",
            "calls",
            "offline",
            "confirm",
            "artifacts",
            "referral",
            "auth",
            "counters",
            "isolation",
            "outcome",
            "state",
            "snapshot",
            "bytes",
            "timeout",
            "mcp",
            "mcp-type",
        ):
            value = copy.deepcopy(fixture())
            if mutation == "case":
                value["native"]["cases"].pop()
            elif mutation in ("calls", "offline", "artifacts"):
                field, replacement = {
                    "calls": ("toolCalls", True),
                    "offline": ("offline", False),
                    "artifacts": ("artifactFiles", 1),
                }[mutation]
                value["native"][field] = replacement
            elif mutation == "confirm":
                value["native"]["confirms"] = []
            elif mutation == "referral":
                value["synthetic"].append({"path": "/referral/search", "authorization": False})
            elif mutation == "auth":
                value["real"][0]["authorization"] = True
            elif mutation == "counters":
                value["catalogCounters"] = []
            elif mutation == "isolation":
                value["isolation"]["readOnlyRoot"] = False
            elif mutation == "state":
                value["installState"]["agent"]["skills"] = 0
            elif mutation == "snapshot":
                value["snapshotStable"] = False
            elif mutation == "bytes":
                value["snapshotDigest"] = "0" * 64
            elif mutation == "timeout":
                next(case for case in value["native"]["cases"] if case["case"] == "timeout")["durationMs"] = 1
            elif mutation in ("mcp", "mcp-type"):
                value["native"]["mcpRegistrations"] = 1 if mutation == "mcp" else False
            else:
                value["native"]["cases"][0]["isError"] = True
            with self.subTest(mutation=mutation), self.assertRaises(ValueError):
                validate_campaign(value)
