"""Author the synthetic corpus through the independent registry's native API."""

from http.client import HTTPConnection
import json
from pathlib import Path
from urllib.parse import urlencode

from peer_catalog import wait_for

PUBLISHER = "registry.example.test"
ORIGIN = "https://federation-fixture:8443"
MANIFEST = "/.well-known/ard.json"
SEARCH = "/api/v1/ard/search"
LIST = "/api/v1/ard/agents"
REGISTRY = f"urn:air:{PUBLISHER}:registry:observal"
SKILL = "publication-compass"
PROMPT = "release-checklist"
MCP = "release-index"
RESOURCE_MEDIA = {SKILL: "application/ai-skill+md", MCP: "application/mcp-server-card+json", PROMPT: "application/vnd.observal.prompt+json"}
QUERY = {"query": {"text": SKILL}, "federation": "none", "pageSize": 20}


def exchange(method, path, body=None, token=None):
    """One fixed private destination, no ambient credentials, redirects or proxies."""
    conn = HTTPConnection("arr-api", 8000, timeout=10)
    headers = {"Content-Type": "application/json"} if body is not None else {}
    if token:
        headers["Authorization"] = "Bearer " + token
    try:
        conn.request(method, path, body, headers)
        res = conn.getresponse()
        raw = res.read((2 << 20) + 1)
        assert len(raw) <= 2 << 20, "native response exceeded fixture limit"
        return res.status, res.getheader("Content-Type"), raw
    finally:
        conn.close()


def request(method, path, body=None, *, token=None, status=200):
    code, _, raw = exchange(method, path, None if body is None else json.dumps(body).encode(), token)
    if code != status:
        detail = json.loads(raw).get("detail") if raw.startswith(b"{") and "auth/" not in path else None
        raise AssertionError(f"native {method} {path}: expected {status}, got {code}: {detail}")
    return json.loads(raw)


class Registry:
    def __init__(self, password):
        def login(actor):
            return request("POST", "/api/v1/auth/login", {"email": actor + "@registry.example", "password": password})["access_token"]
        self.author = login("author")
        self.curator = login("curator")

    def setting(self, key, value):
        request("PUT", "/api/v1/admin/settings/" + key, {"value": value}, token=self.curator)

    def approve(self, submitted):
        request("POST", "/api/v1/reviews/" + str(submitted["review_number"]) + "/publish",
                {"override_reason": "Approve synthetic interoperability fixture"}, token=self.curator)

    def submit(self, kind, name, team=None):
        body = {"name": name, "version": "1.0.0", "description": f"Documentation for {name}", "owner": "author"}
        if team:
            body.update(visibility="team", team_id=team)
        if kind == "skill":
            body.update(task_type="documentation", delivery_mode="registry_direct",
                        skill_md_content=f"---\nname: {name}\ndescription: Documentation for {name}\n---\n\nRead the release notes.\n")
        elif kind == "mcp":
            body.update(category="developer-tools", transport="stdio", command="example-only")
        else:
            body.update(category="documentation", template="Read the release notes.")
        return request("POST", "/api/v1/" + kind + "s/submit", body, token=self.author)

    def archive(self, skill):
        request("PATCH", "/api/v1/skills/" + skill["id"] + "/archive", token=self.author)

    def seed(self):
        for key, value in (("usage_ping.enabled", "false"), ("deployment.public_url", ORIGIN),
                           ("discovery.publisher_domain", PUBLISHER), ("deployment.public_registry_enabled", "true")):
            self.setting(key, value)
        team = request("POST", "/api/v1/teams", {"name": "Release editors", "visibility": "private"}, token=self.author)
        records = {}
        for name, kind, visibility, status in (
            (SKILL, "skill", "public", "approved"), (MCP, "mcp", "public", "approved"), (PROMPT, "prompt", "public", "approved"),
            ("private-manual", "skill", "team", "approved"), ("pending-manual", "skill", "public", "pending"),
            ("archived-manual", "skill", "public", "archived"),
        ):
            row = self.submit(kind, name, team["id"] if visibility == "team" else None)
            if status != "pending":
                self.approve(row)
            if status == "archived":
                self.archive(row)
            control = request("GET", "/api/v1/" + kind + "s/" + row["id"], token=self.author)
            assert (control["id"], control["name"], control["version"], control["visibility"], control["status"]) == (
                row["id"], name, "1.0.0", visibility, status), (name, control["status"], control["visibility"])
            records[name] = {"id": row["id"], "identifier": f"urn:air:{PUBLISHER}:{kind}:{row['id']}"}
        return records

    def successor(self, skill):
        path = "/api/v1/skills/" + skill["id"]
        base = request("GET", path + "/approved-base", token=self.author)
        assert base["version"] == "1.0.0" and base["listing_id"] == skill["id"]
        draft = request("POST", path + "/drafts", {"base_version_id": base["version_id"], "observed_base_revision": base["revision"],
                        "version": "1.1.0", "description": "Updated documentation for " + SKILL}, token=self.author)
        pending = request("POST", path + "/versions/" + draft["version_id"] + "/submit",
                          {"observed_revision": draft["revision"]}, token=self.author)
        assert pending["version_id"] != base["version_id"] and pending["listing_id"] == skill["id"]
        reviews = request("GET", "/api/v1/reviews?type=skill&state=open&limit=25", token=self.curator)
        assert not reviews.get("next_cursor")
        review, = [row for row in reviews["items"] if row["subject_id"] == skill["id"] and row["version"] == "1.1.0"]
        assert review["state"] == "open"
        return {"review_number": review["number"]}


def inventory(entries):
    rows = {e["identifier"]: e for e in entries}
    assert len(rows) == len(entries), "duplicate native identifier"
    return rows


def matched_result(item, version):
    assert item["version"] == version and item["source"] == ORIGIN + "/api/v1/ard", "wrong version or Registry URI"
    assert type(item["score"]) is int and 0 <= item["score"] <= 100, "invalid native relevance"


def check_publication(entry, name, version):
    assert entry["type"] == RESOURCE_MEDIA[name], f"unexpected native media type for {name}"
    assert (entry["displayName"], entry["version"]) == (name, version)
    assert (entry["obs:lifecycle"], entry["obs:visibility"]) == ("approved", "public")
    assert entry["obs:nativeRef"] == f"author/{name}@{version}"
    assert type(entry["obs:recommended"]) is bool, "native obs:recommended must be a JSON boolean"


def check_registry_item(item, full):
    assert item["identifier"] == full["identifier"] and item["obs:approval"] == full["obs:lifecycle"]
    assert item["obs:recommended"] is full["obs:recommended"], "native Registry projection changed obs:recommended"
    for key in ("version", "type", "displayName", "obs:nativeRef"):
        assert item[key] == full[key], f"native Registry projection changed {key}"


def observe(directory, phase, records, version, recommended=False, *, enabled=True, mcp_public=True):
    skill_id = records[SKILL]["identifier"]
    expected = {REGISTRY}
    if version:
        expected.add(skill_id)
    if enabled:
        expected.add(records[PROMPT]["identifier"])
        if mcp_public:
            expected.add(records[MCP]["identifier"])

    def projected():
        status, _, raw = exchange("GET", MANIFEST)
        assert status == 200
        rows = inventory(json.loads(raw)["entries"])
        if set(rows) != expected:
            return None
        if version and (rows[skill_id]["version"], rows[skill_id]["obs:recommended"]) != (version, recommended):
            return None
        return raw

    raw = wait_for("native projection " + phase, projected)
    published = inventory(json.loads(raw)["entries"])
    assert published[REGISTRY]["type"] == "application/ai-registry+json" and published[REGISTRY]["url"] == ORIGIN + "/api/v1/ard"
    for name in RESOURCE_MEDIA:
        identifier = records[name]["identifier"]
        if identifier in published:
            check_publication(published[identifier], name, version if name == SKILL else "1.0.0")
    page = request("POST", SEARCH, QUERY)
    items = inventory(page["results"])
    assert set(items) == ({skill_id} if version else set())
    if version:
        matched_result(items[skill_id], version)
    positive = request("POST", SEARCH, {**QUERY, "query": {"text": MCP}})
    matched = inventory(positive["results"])
    assert set(matched) == ({records[MCP]["identifier"]} if enabled and mcp_public else set())
    if enabled and mcp_public:
        matched_result(matched[records[MCP]["identifier"]], "1.0.0")
    controls = {}
    if phase == "initial":
        for name in (PROMPT, "pending-manual", "archived-manual", "private-manual"):
            response = request("POST", SEARCH, {**QUERY, "query": {"text": name}})
            expected_match = {records[name]["identifier"]} if name == PROMPT else set()
            assert set(inventory(response["results"])) == expected_match, name
            if expected_match:
                matched_result(response["results"][0], "1.0.0")
            controls[name] = response
    pages, members, tokens = [], [], set()
    query = {"pageSize": 1}
    for _ in range(6):
        listing = request("GET", LIST + "?" + urlencode(query))
        pages.append(listing)
        assert len(listing["items"]) <= 1 and listing["total"] == len(expected) - 1
        members.extend(listing["items"])
        token = listing.get("pageToken")
        if not token:
            break
        assert token not in tokens and len(listing["items"]) == 1
        tokens.add(token)
        query["pageToken"] = token
    else:
        raise AssertionError("native List did not terminate")
    assert set(inventory(members)) == expected - {REGISTRY}
    for response in (page, positive, *controls.values()):
        assert "pageToken" not in response, "a single-name Search did not finish in one page"
        for item in response["results"]:
            check_registry_item(item, published[item["identifier"]])
    for item in members:
        check_registry_item(item, published[item["identifier"]])
    directory = Path(directory)
    (directory / (phase + "-manifest.json")).write_bytes(raw)
    (directory / (phase + "-registry.json")).write_text(json.dumps({"skill": page, "mcp": positive, "list": pages, "controls": controls}))
    return raw, {SKILL: items, MCP: matched}
