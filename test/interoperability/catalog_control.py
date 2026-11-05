"""Shared fixed-origin Catalog setup, CLI reads and authority controls for owned fixtures."""

import json
import os
import uuid

from runner import ROOT, docker

CATALOG_DENIALS = {"missing": (None, 401, "UNAUTHENTICATED"), "invalid": ("invalid", 401, "UNAUTHENTICATED"),
                  "wrong-tenant": ("other-tenant-reader", 404, "NOT_FOUND")}


def validate_search_record(item, record):
    """Bind compact Search metadata to the separately validated exact record."""
    summary = item["local"]
    expected = {"resource", "version", "versionRevision", "listing", "listingRevision", "release", "evidence"}
    assert set(summary) in (expected, expected | {"display"})
    assert summary.get("display") == record.get("display"), "Search/detail display mismatch"
    assert "representations" not in summary["versionRevision"] and "findings" not in summary["evidence"]
    for section, fields in summary.items():
        if section == "display":
            continue
        assert fields["id"] == record[section]["id"], "changed " + section + " identity"
        for key, value in fields.items():
            if section == "evidence" and key in ("trust", "legalRights"):
                assert value == {"state": record[section][key]["state"]}
            else:
                assert value == record[section][key], "Search/detail mismatch: " + section + "." + key
    for key in ("displayName", "description", "tags"):
        assert item[key] == record["listingRevision"][key]
    assert item["publisher"] == record["listingRevision"]["publisherId"]


def catalog_denial(case, result):
    assert result.code == 1 and not result.stdout.strip(), "denied CLI read produced a successful envelope"
    if case == "missing":
        assert result.stderr.strip() == "catalogctl: remote: INTERNAL: remote: supply a nonempty bearer token of at most 16 KiB through CATALOG_REMOTE_TOKEN or a token file"
    else:
        error = json.loads(result.stderr)
        _, status, code = CATALOG_DENIALS[case]
        assert error["status"] == status and error["code"] == code and error["requestId"]


class CatalogControl:
    """One private API workflow; no discovered URL is an API request destination."""

    request_command = ("/commands/fixture", "catalog-request")
    key_prefix = "mcp-"

    def __init__(self, directory, container):
        self.directory = directory
        self.container = container
        self.tokens = {}

    def configuration(self):
        config = json.loads((ROOT / "config/local.json").read_text())
        actors = config["auth"]["developmentTokens"]
        actors["other-tenant-reader"] = dict(actors["local-reader"], subject="other-reader",
                                           tenantId=actors["other-tenant-admin"]["tenantId"])
        self.tokens = {name: os.urandom(32).hex() for name in actors}
        config["auth"]["developmentTokens"] = {self.tokens[name]: actor for name, actor in actors.items()}
        self.write("config.json", json.dumps(config))
        # This token is deliberately absent from the written authentication map.
        self.tokens["invalid"] = os.urandom(32).hex()
        self.write("reader-token", self.tokens["local-reader"])

    def write(self, name, content):
        path = self.directory / name
        path.write_text(content)
        path.chmod(0o600)

    def request(self, path, body=None, *, actor="local-admin", etag=None, status=200):
        request = dict(path=path, method="GET" if body is None else "POST", token=self.tokens[actor] if actor else "",
                       body=body, etag=etag or "", key=self.key_prefix + uuid.uuid4().hex if body is not None else "")
        self.write("request.json", json.dumps(request))
        result = docker("exec", self.container, *self.request_command, timeout=15, limit=2*1024*1024, check=False)
        assert result.code == 0, "fixed-origin Catalog request failed"
        assert not any(token in result.stdout + result.stderr for token in self.tokens.values()), "Catalog request leaked a credential"
        response = json.loads(result.stdout)
        assert response["status"] == status, f"Catalog {path}: expected {status}, got {response['status']}"
        return response["body"], response["etag"]

    def cli_result(self, action, *arguments, actor="local-reader"):
        self.write("reader-token", self.tokens[actor] if actor else "")
        result = docker("exec", self.container, "/commands/catalogctl", "remote", action, "--base-url", "http://127.0.0.1:8080",
                        "--token-file", "/catalog/reader-token", "--timeout", "10s", *arguments, timeout=15, limit=2*1024*1024, check=False)
        assert not any(token in result.stdout + result.stderr for token in self.tokens.values()), "Catalog CLI leaked a credential"
        return result

    def cli(self, action, *arguments, actor="local-reader"):
        result = self.cli_result(action, *arguments, actor=actor)
        assert result.code == 0, "Catalog CLI read failed"
        envelope = json.loads(result.stdout)
        assert envelope["meta"]["requestId"] and envelope["data"] is not None
        return envelope

    def denial(self, case, release):
        actor, status, code = CATALOG_DENIALS[case]
        if actor == "other-tenant-reader":
            assert "catalog.search" in self.cli("capabilities", actor=actor)["data"]["operations"], "wrong-tenant credential is not a valid reader"
        problem, _ = self.request("/api/v1/catalog/resources/" + release, actor=actor, status=status)
        assert problem["status"] == status and problem["code"] == code and "data" not in problem
        result = self.cli_result("record", "--id", release, actor=actor)
        catalog_denial(case, result)
        # RFC 9457 instance deliberately echoes the requested path; detail must not disclose the record.
        assert release not in result.stdout + result.stderr + problem["detail"], "denial disclosed release identity"
        return {"api_status": status, "api_code": code, "cli_exit": result.code,
                "cli_error": result.stderr.strip(), "outcome": "denied", "handoff_created": False}
