"""Run inside the owned two-API PostgreSQL fixture, through its response-loss proxy."""
import hashlib
import http.client
import json
from pathlib import Path
import subprocess
import sys
import tempfile
from urllib.parse import urlsplit
from uuid import uuid4

from fixture import Fixture


def qualify(root, cli, address, proxy, namespace, publisher):
    f = Fixture(root, cli)
    f.origin = proxy
    host, port = address.split(":")
    assert host == "127.0.0.1" and urlsplit(proxy).hostname == host

    def request(method, path, token="", body=None, etag=None, expected=200):
        headers = {"Authorization": "Bearer " + token} if token else {}
        if body is not None:
            headers.update({"Content-Type": "application/json", "Idempotency-Key": str(uuid4())})
        if etag:
            headers["If-Match"] = etag
        conn = http.client.HTTPConnection(host, int(port), timeout=10)
        try:
            conn.request(method, path, json.dumps(body) if body is not None else None, headers)
            response = conn.getresponse()
            raw = response.read()
            assert response.status == expected, (method, path, response.status, raw[:500])
            return raw, response.getheader("ETag")
        finally:
            conn.close()

    def api(*args, **kwargs):
        return json.loads(request(*args, **kwargs)[0])

    admin, service = "local-admin", "local-template-service-client"
    who = api("GET", "/api/v1/me", service)
    role = api("POST", "/api/v1/access/roles", admin, {
        "name": "Git draft author", "reason": "Owned delivery fixture",
        "permissions": ["namespace.read", "resource.create", "resource.read", "resource.read_content", "resource.read_history", "resource.edit"],
    }, expected=201)
    grant = api("POST", "/api/v1/access/assignments", admin, {
        "roleDefinitionId": role["id"], "membershipId": who["membershipId"],
        "namespaceId": namespace, "reason": "Allow draft delivery in this Namespace",
    }, expected=201)
    publication = api("POST", "/api/v1/publication/snapshots", "local-publisher", {"host": host}, expected=201)
    api("POST", "/api/v1/publication/snapshots/" + publication["id"] + ":activate", "local-publisher",
        {"host": host, "reason": "Observe draft-only delivery"}, etag=publication["etag"])
    manifest = request("GET", "/.well-known/ard.json")
    assert manifest[0] and manifest[1]

    def result(commit, name="result.json"):
        return json.loads((f.state / commit / name).read_bytes())

    draft = json.loads(f.draft.read_bytes())
    draft["listing"] = {"displayName": "Reviewed Git fixture", "description": "Local delivery", "visibility": "private"}
    f.draft.write_text(json.dumps(draft))
    first = f.save("Include Listing")
    initial = ["--namespace-id", namespace, "--publisher-id", publisher]
    error = f.run("apply", *initial, commit=first, success=False)
    assert result(first, "last-failure.json")["disposition"] == "uncertain" and not error.stdout
    # Advance Git while an already committed write is still unacknowledged.
    draft["version"] = "0.2.0"
    card = json.loads(draft["representations"][0]["input"]["text"])
    card["version"] = draft["version"]
    draft["representations"][0]["input"]["text"] = json.dumps(card)
    f.draft.write_text(json.dumps(draft))
    second = f.save("Prepare a new Version")
    assert "unresolved delivery" in f.run("apply", *initial, commit=second, success=False).stderr
    fresh = root / "other-checkout"
    subprocess.run(["git", "clone", "-q", "--depth=1", "--no-checkout", f.repo.as_uri(), fresh], check=True)
    assert subprocess.run(["git", "-C", fresh, "cat-file", "-e", first], capture_output=True).returncode != 0
    receipt = f.state / first / "receipt.json"
    original_receipt = receipt.read_bytes()
    receipt.write_text("{}")
    f.run("resume", *initial, commit=first, repo=fresh, success=False)
    assert (f.state / "pending").read_text().strip() == first
    receipt.write_bytes(original_receipt)
    data = f.state / first / "inputs/resources/agent.json"
    retained = data.read_bytes()
    data.unlink()
    assert "original draft is missing" in f.run("resume", *initial, commit=first, repo=fresh, success=False).stderr
    data.write_bytes(retained)
    f.run("resume", *initial, commit=first, repo=fresh)
    steps = [s["ack"] for s in result(first)["items"][0]["steps"]]
    resource, revision, listing, listing_revision = steps[0]["id"], steps[1]["revisionId"], steps[2]["id"], steps[3]["id"]
    assert all((resource, revision, listing, listing_revision))
    assert api("GET", "/api/v1/resources/"+resource, "local-contributor")["canonicalIdentifier"] == draft["canonicalIdentifier"]
    assert api("GET", "/api/v1/version-revisions/"+revision, "local-contributor")["revision"]["contentSetDigest"] == steps[1]["digest"]
    assert request("GET", "/.well-known/ard.json") == manifest
    assert not (f.state / "pending").exists()
    exact = ["--resource-id", resource, "--listing-id", listing, "--publisher-id", publisher]
    f.run("apply", *exact, commit=second)
    later = [s["ack"] for s in result(second)["items"][0]["steps"]]
    assert len(later) == 2
    f.run("resume", *initial, commit=first, repo=fresh)
    assert result(first)["items"][0]["steps"][1]["ack"]["revisionId"] == revision
    # The contributor submits what is current. The older handoff must be rejected by the reviewer.
    sub = api("POST", "/api/v1/catalog-listings/"+listing+"/submissions", "local-contributor", {}, expected=201)
    review = api("GET", "/api/v1/review-submissions/"+sub["id"], "local-reviewer")
    actual = (review["versionRevisionId"], review["listingRevisionId"])
    assert actual == (later[0]["revisionId"], later[1]["id"]) and actual != (revision, listing_revision)

    # The package path stays beneath the draft. Untracked files and CRLF checkout conversions are absent.
    artifact = f.add_skill()
    skill_args = ["--namespace-id", namespace, "--package-directory", "1=review-notes"]
    f.run("apply", *skill_args)
    skill_steps = result(f.commit)["items"][0]["steps"]
    rep = api("GET", "/api/v1/version-revisions/"+skill_steps[1]["ack"]["revisionId"], "local-contributor")["revision"]["representations"][0]
    assert rep["handoff"]["git"]["commit"] == artifact
    files = rep["package"]["files"]
    script = next(file for file in files if file["path"].endswith("/scripts/example.sh"))
    assert script["executable"]
    expected_doc = f.git("rev-parse", artifact + ":resources/review-notes/SKILL.md")
    committed = f.repo / "resources/review-notes/SKILL.md"
    assert b"\r\n" in committed.read_bytes()
    # Compare the independent committed LF bytes to the server's inspected file digest.
    skill_doc = next(file for file in files if file["path"].endswith("/SKILL.md"))
    raw = subprocess.check_output(["git", "-C", f.repo, "cat-file", "blob", expected_doc])
    assert skill_doc["digest"] == "sha256:" + hashlib.sha256(raw).hexdigest()
    control = root / "control"
    # Independent materialization of the two known fixture blobs and their expected modes.
    # Use catalogctl's own packager to obtain the reference ZIP digest.
    for name in ("agent.json", "review-notes/SKILL.md", "review-notes/scripts/example.sh"):
        path = control / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(subprocess.check_output(["git", "-C", f.repo, "show", f.commit + ":resources/" + name]))
        path.chmod(0o700 if name.endswith(".sh") else 0o600)
    preview = json.loads(subprocess.check_output([cli, "import", "preview", "--file", control / "agent.json", *skill_args]))
    asset, = preview["resources"][0]["assets"]
    assert rep["contentDigest"] == asset["digest"] and asset["scope"].startswith("generated-zip-bytes")
    assert request("GET", "/.well-known/ard.json") == manifest
    # Finished deliveries remain verifiable after independent review. Keep a successful
    # private handoff if a later principal cannot read the acknowledged records.
    f.run("resume", *exact, commit=second)
    f.token.write_text("local-contributor")
    assert f.run("resume", *exact, commit=second, success=False).stdout == ""
    f.token.write_text(service)
    api("POST", "/api/v1/access/assignments/"+grant["id"]+":revoke", admin,
        {"reason": "End scoped Git delivery"}, etag='"rev-1"')
    assert f.run("resume", *exact, commit=second, success=False).stdout == ""
    assert result(second)["disposition"] == "complete"
    api("POST", "/api/v1/access/assignments", admin, {
        "roleDefinitionId": role["id"], "membershipId": who["membershipId"],
        "namespaceId": namespace, "reason": "Test an already allocated Version label",
    }, expected=201)
    # A new commit that reuses the existing Version label must fail without creating another Version.
    f.draft.write_text(json.dumps(draft))
    duplicate = f.save("Restore an already delivered Version label")
    f.run("apply", *exact, commit=duplicate, success=False)
    assert result(duplicate, "last-failure.json")["disposition"] != "complete"
    assert request("GET", "/.well-known/ard.json") == manifest
    api("POST", "/api/v1/publication:unpublish", "local-publisher", {"host": host, "reason": "End disposable Git delivery"})


if __name__ == "__main__":
    if not __debug__:
        sys.exit("The process qualification requires Python assertions.")
    with tempfile.TemporaryDirectory() as tmp:
        qualify(Path(tmp), Path(sys.argv[1]), *sys.argv[2:])
