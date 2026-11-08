"""Qualify the prepared two-Catalog Keycloak tutorial with owned disposable inputs."""

import argparse
import base64
from contextlib import ExitStack
from datetime import datetime, timezone
import hashlib
import http.client
import importlib.util
import ipaddress
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import signal
import socket
import ssl
import subprocess
import sys
import tempfile
import time
from types import SimpleNamespace
from unittest.mock import patch
import uuid

ROOT = Path(__file__).resolve().parents[2]
TUTORIAL = ROOT / "examples/tutorials/local-oidc"
# These are the shipped tutorial owners, not a second deployment topology.
sys.path.insert(0, str(TUTORIAL))
import prepare  # noqa: E402
import compose  # noqa: E402
import local_auth  # noqa: E402

spec = importlib.util.spec_from_file_location("tutorial_service_token", TUTORIAL / "service-token.py")
service_token = importlib.util.module_from_spec(spec)
spec.loader.exec_module(service_token)
A, B = "https://catalog-a.test:8443", "https://catalog-b.test:8445"
TENANTS = {"a": "00000000-0000-7000-8000-000000000001", "b": "00000000-0000-7000-8000-000000000002"}
PHASES = ("prepared", "ready", "publication", "initial-sync", "audience-origin-refusals", "grant-withdrawal", "renewal", "cleanup")


class Interrupted(ValueError):
    def __init__(self, signum):
        super().__init__("Owned tutorial qualification interrupted")
        self.signum = signum


class NativeFailure(ValueError):
    def __init__(self, returncode, stderr):
        super().__init__("Required owned tutorial command failed")
        self.returncode = returncode
        # This exact bootstrap refusal contains no supplied values. Unknown
        # command output stays private rather than becoming report diagnostics.
        known = b"catalogctl: identity: INTERNAL: initial identity provision requires tenant, tenant-name, issuer, subject, operator, reason, and tenant_admin in roles"
        self.diagnostic = known.decode() if stderr.strip() == known else None


def native(arguments, *, env=None, seconds=180):
    result = subprocess.run([str(a) for a in arguments], cwd=ROOT, env=env, capture_output=True, timeout=seconds, check=False)
    if result.returncode:
        raise NativeFailure(result.returncode, result.stderr)
    return result.stdout


class API:
    def __init__(self, directory, side, token):
        self.side, self.token = side, token
        self.context = ssl.create_default_context(cafile=str(directory / "trust/ca.pem"))
        self.requests = 0

    def request(self, method, path, body=None, expected=200, etag=None):
        if not path.startswith(("/api/v1/", "/.well-known/", "/readyz", "/version")):
            raise ValueError("Unselected tutorial request path")
        host, port = "catalog-" + self.side + ".test", 8443 if self.side == "a" else 8445
        connection = http.client.HTTPSConnection(host, port, timeout=15, context=self.context)
        connection.connect = lambda: setattr(connection, "sock", self.context.wrap_socket(
            socket.create_connection(("127.0.0.1", port), timeout=15), server_hostname=host))
        headers = {"Authorization": "Bearer " + self.token} if self.token else {}
        if method == "POST":
            headers.update({"Content-Type": "application/json", "Idempotency-Key": "tutorial-test-" + uuid.uuid4().hex})
        if etag:
            headers["If-Match"] = etag
        try:
            connection.request(method, path, None if body is None else json.dumps(body), headers)
            response = connection.getresponse()
            raw = response.read((1 << 20) + 1)
            self.requests += 1
            if response.status != expected or len(raw) > 1 << 20:
                route = re.sub(r"[0-9a-f]{8}-[0-9a-f-]{27,}", ":id", path.split("?", 1)[0])
                raise ValueError("Tutorial API " + self.side + " " + method + " " + route
                                 + " returned " + str(response.status) + "; expected " + str(expected) + " within its byte bound")
            return (json.loads(raw) if raw else None), response.getheader("ETag")
        finally:
            connection.close()


def assert_test(document, revision):
    if document.get("status") != "succeeded" or document.get("credentialRevision") != revision:
        raise ValueError("Source test did not succeed with the replacement credential generation")


def source_tests(api, source_id):
    page, _ = api.request("GET", "/api/v1/audit-events?action=source.tested&limit=100")
    if page.get("nextCursor"):
        raise ValueError("Owned tutorial source-test audit exceeded its bounded selection")
    return {r["id"]: r for r in page["items"] if r["targetId"] == source_id}


def assert_source_refusal(problem, records, source_id, revision, credential_revision=None):
    if problem.get("code") != "DEPENDENCY_UNAVAILABLE" or len(records) != 1:
        raise ValueError("Source refusal omitted its exact retained failure evidence")
    record = records[0]
    facts = record.get("details", {}).get("facts", {})
    if (record.get("action") != "source.tested" or record.get("targetId") != source_id
            or record.get("tenantId") != TENANTS["b"] or facts.get("beforeRevision") != revision
            or facts.get("afterRevision") != revision or facts.get("stateChanged") is not False):
        raise ValueError("Source refusal audit did not bind the exact unchanged source revision")
    if credential_revision is None:
        if facts.get("outcome") != "transport_error" or any(k in facts for k in ("httpClass", "bodyDigest", "credentialRevision")):
            raise ValueError("Wrong-origin credential was not refused before HTTP/credential disclosure")
    elif (facts.get("outcome") != "http_error" or facts.get("httpClass") != "4xx"
          or facts.get("credentialRevision") != credential_revision
          or not re.fullmatch(r"sha256:[0-9a-f]{64}", facts.get("bodyDigest", ""))):
        raise ValueError("Wrong-audience credential did not retain the upstream HTTP refusal")


def assert_snapshot(snapshot, source, expected_revision, credential_revision, source_id):
    if (snapshot.get("state") != "complete" or snapshot.get("enumerationComplete") is not True
            or snapshot.get("sourceId") != source_id or snapshot.get("sourceRevision") != expected_revision
            or snapshot.get("credentialRevision") != credential_revision or source.get("lastCompleteSnapshotId") != snapshot.get("id")):
        raise ValueError("Source snapshot did not bind current complete enumeration and credential generation")


def generation(directory, *, origin=A, token_stem="upstream-a"):
    """Publish one complete immutable generation into the existing mounted directory."""
    root = directory / "b"
    claims = json.loads((root / (token_stem + ".claims.json")).read_text())
    if datetime.fromisoformat(claims["expiresAt"]) <= datetime.now(timezone.utc):
        raise ValueError("Tutorial source token is expired")
    leaf = "source-a-" + uuid.uuid4().hex
    target = root / "source-credentials"
    prepare.write(target / (leaf + ".token"), (root / (token_stem + ".token")).read_text(), 0o444)
    metadata = {"schemaVersion": 1, "tenantId": TENANTS["b"], "revision": leaf, "expiresAt": claims["expiresAt"],
                "origin": origin, "type": "bearer", "secretFile": leaf + ".token"}
    descriptor, temporary = tempfile.mkstemp(prefix=".generation-", dir=target)
    try:
        with os.fdopen(descriptor, "w") as stream:
            json.dump(metadata, stream)
            stream.flush()
            os.fsync(stream.fileno())
            os.fchmod(stream.fileno(), 0o444)
        os.replace(temporary, target / "catalog-a-reader.json")
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)
    return leaf


def acquire(directory, side, client, secret, stem, audience):
    # Test-only DNS admission routes just the fixed tutorial issuer to loopback;
    # the unchanged helper still performs real TLS/SNI/issuer/audience checks.
    lookup = socket.getaddrinfo

    def resolve(host, port, *args, **kwargs):
        if host != "identity.test" or port != 8447:
            raise ValueError("Tutorial token helper attempted an unselected destination")
        return lookup("127.0.0.1", port, *args, **kwargs)

    with patch("socket.getaddrinfo", side_effect=resolve):
        service_token.acquire(SimpleNamespace(issuer=prepare.ISSUER, client_id=client, audience=audience,
            secret_file=str(directory / side / secret), ca_file=str(directory / "trust/ca.pem"), output_stem=str(directory / side / stem)))
    return (directory / side / (stem + ".token")).read_text().strip()


def claims(token):
    value = token.split(".")[1]
    return json.loads(base64.urlsafe_b64decode(value + "=" * (-len(value) % 4)))


def qualification_reviewer(directory, configuration):
    """Keep prepared inputs immutable; replace only this invocation's realm mount."""
    mounts = [m for m in configuration["services"]["identity"]["volumes"]
              if m.get("target") == "/opt/keycloak/data/import/catalog-realm.json"]
    original = directory / "catalog-realm.json"
    if len(mounts) != 1 or mounts[0].get("type") != "bind" or mounts[0].get("source") != str(original):
        raise ValueError("The prepared issuer realm mount changed")
    realm = json.loads(original.read_text())
    secret = secrets.token_urlsafe(32)
    prepare.write(directory / "a/reviewer-client-secret", secret)
    realm["clients"].append(prepare.client("catalog-reviewer-qualification", "open-agentic-catalog", secret=secret, service=True))
    selected = directory / "qualification-realm.json"
    prepare.document(selected, realm)
    mounts[0]["source"] = str(selected)


def publish(api, reviewer):
    namespace, _ = api.request("POST", "/api/v1/namespaces", {"slug": "tutorial-qualification", "displayName": "Federation tutorial"}, 201)
    publisher, etag = api.request("POST", "/api/v1/publishers", {"displayName": "Tutorial publisher", "domains": ["tutorial.catalog.example"]}, 201)
    api.request("POST", "/api/v1/publishers/" + publisher["id"] + "/publication-domains/tutorial.catalog.example:approve", {"reason": "Allow owned tutorial Publisher"}, etag=etag)
    identifier = "urn:air:tutorial.catalog.example:agents:federation-qualification"
    resource, _ = api.request("POST", "/api/v1/resources", {"namespaceId": namespace["id"], "canonicalIdentifier": identifier, "kind": "agent"}, 201)
    version, _ = api.request("POST", "/api/v1/resources/" + resource["id"] + "/versions", {"version": "1.0.0", "representations": [
        {"role": "primary", "mediaType": "application/a2a-agent-card+json", "data": json.loads((ROOT / "samples/cards/order-agent.json").read_text())}]}, 201)
    validation, _ = api.request("POST", "/api/v1/version-revisions/" + version["revision"]["id"] + ":validate", {}, 201)
    if validation["evidence"]["state"] != "complete":
        raise ValueError("Tutorial native Agent validation incomplete")
    listing, etag = api.request("POST", "/api/v1/catalog-listings", {"namespaceId": namespace["id"], "resourceId": resource["id"]}, 201)
    api.request("POST", "/api/v1/catalog-listings/" + listing["id"] + "/revisions", {"versionRevisionId": version["revision"]["id"],
        "displayName": "Federation tutorial Agent", "description": "Owned federation tutorial metadata", "publisherId": publisher["id"],
        "tags": ["federation", "tutorial"], "categories": ["development", "examples"],
        "representativeQueries": ["Find the federation tutorial Agent", "Find setup metadata for the federation tutorial"],
        "license": "Apache-2.0", "visibility": "tenant"}, 201, etag)
    submission, etag = api.request("POST", "/api/v1/catalog-listings/" + listing["id"] + "/submissions", {}, 201)
    reviewer.request("POST", "/api/v1/review-submissions/" + submission["id"] + ":approve", {"reason": "Independent owned tutorial review"}, etag=etag)
    _, etag = api.request("GET", "/api/v1/catalog-listings/" + listing["id"])
    api.request("POST", "/api/v1/catalog-listings/" + listing["id"] + ":publish", {"submissionId": submission["id"]}, 201, etag)
    snapshot, etag = api.request("POST", "/api/v1/publication/snapshots", {"host": "catalog-a.test"}, 201)
    assert_publication(snapshot, identifier)
    api.request("POST", "/api/v1/publication/snapshots/" + snapshot["id"] + ":activate", {"host": "catalog-a.test", "reason": "Owned tutorial activation"}, etag=etag)
    return identifier


def assert_publication(snapshot, identifier):
    if snapshot.get("audience") != "authenticated" or snapshot.get("state") != "candidate" or snapshot.get("host") != "catalog-a.test":
        raise ValueError("Tutorial publication host/audience/state changed")
    if not any(e.get("identifier") == identifier and e.get("version") == "1.0.0" for e in snapshot.get("ard", {}).get("entries", [])):
        codes = [reason for e in snapshot.get("exclusions", []) for reason in e.get("reasons", ["unclassified"])]
        raise ValueError("Tutorial candidate omitted the exact Agent/version; exclusion codes=" + json.dumps(codes))


def synchronize(api, source_id, credential_revision, *, succeeds=True):
    source, etag = api.request("GET", "/api/v1/federation/sources/" + source_id)
    expected_revision = source["revision"]
    accepted, _ = api.request("POST", "/api/v1/federation/sources/" + source_id + ":sync", {}, 202, etag)
    deadline = time.monotonic() + 120
    while True:
        job, _ = api.request("GET", "/api/v1/jobs/" + accepted["job"]["id"])
        if job["state"] in {"succeeded", "failed", "cancelled", "dead_letter"}:
            break
        if time.monotonic() >= deadline:
            raise ValueError("Tutorial source sync did not reach a terminal state")
        time.sleep(0.2)
    if succeeds:
        if job["state"] != "succeeded" or job["result"].get("type") != "source_snapshot" or job["result"].get("state") != "complete":
            raise ValueError("Tutorial source sync failed or was incomplete")
        snapshot, _ = api.request("GET", "/api/v1/federation/snapshots/" + job["result"]["snapshotId"])
        source, _ = api.request("GET", "/api/v1/federation/sources/" + source_id)
        assert_snapshot(snapshot, source, expected_revision, credential_revision, source_id)
        return snapshot
    if job["state"] == "succeeded":
        raise ValueError("Withdrawn source authority unexpectedly synchronized")
    return job


def check_index(api, source_id, identifier):
    page, _ = api.request("POST", "/api/v1/catalog/search", {"origins": ["indexed"], "kinds": ["agent"], "text": "federation tutorial", "limit": 10})
    matches = [i for i in page["items"] if i["identifier"] == identifier]
    if (len(matches) != 1 or page["partial"] is not False or matches[0]["type"] != "indexed" or matches[0]["kind"] != "agent"
            or matches[0].get("versionPresent") is not True or matches[0].get("version") != "1.0.0"
            or source_id not in matches[0]["indexed"]["sourceIds"] or "local" in matches[0]):
        projection = [{k: i.get(k) for k in ("identifier", "type", "kind", "versionPresent", "version")}
                      | {"sourceIds": i.get("indexed", {}).get("sourceIds", []), "hasLocal": "local" in i}
                      for i in page["items"]]
        raise ValueError("Tutorial exact indexed record/provenance mismatch: " + json.dumps({"partial": page.get("partial"), "matches": len(matches), "items": projection}, sort_keys=True))


def run(args):
    local_auth.local_engine(os.environ.copy())
    root = ROOT / "output/tutorial-oidc-federation"
    root.mkdir(parents=True, exist_ok=True)
    report = root / ("run-" + uuid.uuid4().hex + ".json")
    outcome = {"status": "failed", "stage": "input-inventory", "completed": [], "cleanup": "unrun", "source": {}, "images": {}}
    outcome["revision"] = native(["git", "rev-parse", "HEAD"]).decode().strip()
    for folder in (TUTORIAL, ROOT / "test/functional"):
        for path in sorted(folder.iterdir()):
            if path.is_file() and path.suffix in {".py", ".yaml", ".go", ".json"}:
                outcome["source"][str(path.relative_to(ROOT))] = hashlib.sha256(path.read_bytes()).hexdigest()
    command = None
    environment = None
    code = 1
    try:
        # Keep private inputs if resource cleanup fails, so operators can
        # still stop exactly this invocation. No retained project is selected.
        with ExitStack() as ownership:
            temp = Path(tempfile.mkdtemp(prefix="catalog-oidc-federation-"))
            directory = temp / "inputs"

            def remove_inputs():
                if command is None or outcome["cleanup"] == "passed":
                    shutil.rmtree(temp)
                else:
                    outcome["retainedInputs"] = str(directory)
                    outcome["stopCommand"] = command + ["down", "--volumes", "--remove-orphans"]

            ownership.callback(remove_inputs)
            outcome["stage"] = "prepare-inputs"
            prepare.prepare(SimpleNamespace(directory=str(directory), backend_image=args.backend_image, ui_image=args.ui_image,
                                             subnet=args.subnet, frontend_address=args.frontend_address, single=False))
            outcome["stage"] = "ports-and-network-preflight"
            for port in (8443, 8445, 8447):
                local_auth.available_port(port)
            desired = ipaddress.ip_network(args.subnet)
            networks = native(["docker", "network", "ls", "-q"]).decode().split()
            if networks:
                for network in json.loads(native(["docker", "network", "inspect", *networks])):
                    for entry in network.get("IPAM", {}).get("Config") or []:
                        if entry.get("Subnet"):
                            actual = ipaddress.ip_network(entry["Subnet"])
                            if actual.version == desired.version and actual.overlaps(desired):
                                raise ValueError("Select an unused private tutorial qualification subnet")
            outcome["stage"] = "render-prepared-compose"
            base, environment = compose.invocation(directory, ["config", "--format", "json"])
            configuration = json.loads(native(base, env=environment))
            for service in configuration["services"].values():
                service["pull_policy"] = "never"
            # Use a separate ordinary service account for the independent review.
            outcome["stage"] = "qualification-reviewer-realm"
            qualification_reviewer(directory, configuration)
            outcome["stage"] = "write-qualification-compose"
            selected = directory / "compose.qualification.json"
            prepare.document(selected, configuration, 0o600)
            command = ["docker", "compose", "--project-name", environment["COMPOSE_PROJECT_NAME"], "--env-file", str(directory / "compose.env"), "--file", str(selected)]

            def dc(*arguments, seconds=180):
                return native(command + list(arguments), env=environment, seconds=seconds)

            frontend_built = False
            original_failure = None
            try:
                # No implicit pulls. Missing prerequisites fail before startup.
                outcome["stage"] = "image-preflight"
                for name in ("api-a", "identity", "postgres-a"):
                    image = configuration["services"][name]["image"]
                    outcome["images"][name] = json.loads(native(["docker", "image", "inspect", image]))[0]["Id"]
                outcome["completed"].append("prepared")
                outcome["stage"] = "frontend-build"
                dc("build", "frontend", seconds=300)
                frontend_built = True
                outcome["stage"] = "postgresql-startup"
                dc("up", "-d", "--wait", "--wait-timeout", "120", "postgres-a", "postgres-b")
                outcome["stage"] = "issuer-startup"
                dc("up", "-d", "identity", "frontend")
                local_auth.wait_for(lambda: local_auth.https_get(directory, "identity.test", 8447, "/realms/catalog/.well-known/openid-configuration"))
                tokens = {}
                for side, audience in (("a", "open-agentic-catalog"), ("b", "open-agentic-catalog-b")):
                    outcome["stage"] = "migrate-and-provision-" + side
                    dc("run", "--rm", "owner-" + side, "migrate", "apply", "--config", "/inputs/" + side + "/owner.json")
                    tokens[side] = acquire(directory, side, "catalog-automation-" + side, "automation-client-secret", "automation", audience)
                    dc("run", "--rm", "owner-" + side, "identity", "provision", "--config", "/inputs/" + side + "/owner.json", "--tenant", TENANTS[side],
                       "--tenant-name", "Owned tutorial " + side, "--issuer", prepare.ISSUER, "--subject", claims(tokens[side])["sub"], "--actor-type", "service",
                       "--roles", "tenant_admin,contributor,publisher,reader,source_admin", "--operator", "tutorial-qualification", "--reason", "Owned tutorial administration")
                outcome["stage"] = "acquire-reviewer-token"
                review_token = acquire(directory, "a", "catalog-reviewer-qualification", "reviewer-client-secret", "reviewer", "open-agentic-catalog")
                outcome["stage"] = "catalog-startup-and-identity"
                dc("up", "-d", "--wait", "--wait-timeout", "120", "api-a", "worker-a", "api-b", "worker-b")
                identifiers = dc("ps", "-q").decode().split()
                workloads = json.loads(native(["docker", "container", "inspect", *identifiers]))
                outcome["workloads"] = {}
                for workload in workloads:
                    name = workload["Config"]["Labels"]["com.docker.compose.service"]
                    selected_image = json.loads(native(["docker", "image", "inspect", configuration["services"][name]["image"]]))[0]["Id"]
                    if workload["Image"] != selected_image:
                        raise ValueError("Tutorial workload image identity changed")
                    outcome["workloads"][name] = workload["Image"]
                a, b, reviewer = API(directory, "a", tokens["a"]), API(directory, "b", tokens["b"]), API(directory, "a", review_token)
                outcome["catalogBuilds"] = {}
                for api in (a, b):
                    local_auth.wait_for(lambda api=api: api.request("GET", "/readyz"))
                    api.request("GET", "/api/v1/me")
                    build, _ = api.request("GET", "/version")
                    if build.get("commit") != outcome["revision"]:
                        raise ValueError("Tutorial image was not built from the selected source revision")
                    outcome["catalogBuilds"][api.side] = build
                outcome["stage"] = "grant-reviewer-membership"
                for role in ("reviewer", "reader"):
                    a.request("POST", "/api/v1/role-assignments", {"subject": claims(review_token)["sub"], "actorType": "service", "role": role,
                        "reason": "Independent owned tutorial review"}, 201)
                reviewer.request("GET", "/api/v1/me")
                outcome["completed"].append("ready")
                outcome["stage"] = "review-and-publication"
                identifier = publish(a, reviewer)
                outcome["stage"] = "source-credential-generation"
                source_token = acquire(directory, "b", "catalog-source-b-to-a", "source-client-secret", "upstream-a", "open-agentic-catalog")
                member, _ = a.request("POST", "/api/v1/role-assignments", {"subject": claims(source_token)["sub"], "actorType": "service", "role": "service_client", "reason": "Owned tutorial source membership"}, 201)
                roles, _ = a.request("GET", "/api/v1/access/roles?limit=100")
                role = next(r for r in roles["items"] if r.get("builtin") and r["key"] == "discovery_reader" and r["state"] == "active")
                a.request("POST", "/api/v1/access/assignments", {"roleDefinitionId": role["id"], "membershipId": member["membershipId"], "reason": "Owned tutorial discovery caller"}, 201)
                source_api = API(directory, "a", source_token)
                manifest, _ = source_api.request("GET", "/.well-known/ard.json")
                if not any(e.get("identifier") == identifier and e.get("version") == "1.0.0" for e in manifest.get("entries", [])):
                    raise ValueError("Authenticated source manifest omitted the exact published Agent/version")
                revision = generation(directory)
                outcome["completed"].append("publication")
                outcome["stage"] = "source-inspection-and-connect"
                inspection, _ = b.request("POST", "/api/v1/federation/source-inspections", {"url": A + "/.well-known/ard.json", "networkProfile": "tutorial-a", "credentialBinding": "catalog-a-reader"}, 201)
                if inspection.get("credentialRevision") != revision:
                    raise ValueError("Tutorial inspection credential generation mismatch")
                source, _ = b.request("POST", "/api/v1/federation/sources", {"inspectionId": inspection["id"], "displayName": "Owned tutorial A", "retrievalMode": "indexed",
                    "scope": {"kinds": ["agent"]}, "updateMode": "manual_review", "networkProfile": "tutorial-a", "credentialBinding": "catalog-a-reader", "live": {"enabled": False},
                    "referrals": {"enabled": False}, "nesting": {"enabled": False}, "discoveryAssignments": [{"host": "catalog-b.test", "audience": "authenticated"}], "reason": "Owned tutorial federation"}, 201)
                sid = source["id"]
                _, etag = b.request("GET", "/api/v1/federation/sources/" + sid)
                tested, _ = b.request("POST", "/api/v1/federation/sources/" + sid + ":test", {}, etag=etag)
                assert_test(tested, revision)
                outcome["stage"] = "initial-sync"
                snapshot = synchronize(b, sid, revision)
                check_index(b, sid, identifier)
                outcome["completed"].append("initial-sync")
                # A-issued tokens are never valid at B and vice versa. Also
                # exercise the resolver's exact origin before any fresh sync.
                # Provision each negative subject in the destination Catalog;
                # absent membership must not mask the wrong-audience guard.
                outcome["stage"] = "audience-and-origin-refusals"
                for side, token in (("a", tokens["b"]), ("b", source_token)):
                    administrator = a if side == "a" else b
                    administrator.request("POST", "/api/v1/role-assignments", {"subject": claims(token)["sub"], "actorType": "service", "role": "reader",
                        "reason": "Isolate wrong audience from missing membership"}, 201)
                    API(directory, side, token).request("GET", "/api/v1/me", expected=401)
                generation(directory, origin=B)
                selected_source, etag = b.request("GET", "/api/v1/federation/sources/" + sid)
                before_tests = source_tests(b, sid)
                refused, _ = b.request("POST", "/api/v1/federation/sources/" + sid + ":test", {}, expected=503, etag=etag)
                after_tests = source_tests(b, sid)
                assert_source_refusal(refused, [r for key, r in after_tests.items() if key not in before_tests], sid, selected_source["revision"])
                bad_revision = generation(directory, token_stem="automation")
                # The exact same B token gets a direct 401 from A's manifest;
                # retained source evidence discloses only bounded HTTP class.
                API(directory, "a", tokens["b"]).request("GET", "/.well-known/ard.json", expected=401)
                selected_source, etag = b.request("GET", "/api/v1/federation/sources/" + sid)
                before_tests = source_tests(b, sid)
                refused, _ = b.request("POST", "/api/v1/federation/sources/" + sid + ":test", {}, expected=503, etag=etag)
                after_tests = source_tests(b, sid)
                assert_source_refusal(refused, [r for key, r in after_tests.items() if key not in before_tests], sid, selected_source["revision"], bad_revision)
                revision = generation(directory)
                _, etag = b.request("GET", "/api/v1/federation/sources/" + sid)
                tested, _ = b.request("POST", "/api/v1/federation/sources/" + sid + ":test", {}, etag=etag)
                assert_test(tested, revision)
                outcome["completed"].append("audience-origin-refusals")
                outcome["stage"] = "current-membership-withdrawal"
                path = "/api/v1/memberships/" + member["membershipId"]
                _, etag = a.request("GET", path)
                _, etag = a.request("POST", path + ":suspend", {"reason": "Withdraw current tutorial source authority"}, etag=etag)
                source_api.request("GET", "/.well-known/ard.json", expected=401)
                failed_job = synchronize(b, sid, revision, succeeds=False)
                retained, _ = b.request("GET", "/api/v1/federation/sources/" + sid)
                if retained["lastCompleteSnapshotId"] != snapshot["id"]:
                    raise ValueError("Failed tutorial synchronization replaced the last complete snapshot")
                check_index(b, sid, identifier)
                outcome["completed"].append("grant-withdrawal")
                outcome["stage"] = "external-renewal-and-explicit-recovery"
                a.request("POST", path + ":reactivate", {"reason": "Restore current tutorial source authority"}, etag=etag)
                acquire(directory, "b", "catalog-source-b-to-a", "source-client-secret", "upstream-a", "open-agentic-catalog")
                revision = generation(directory)
                _, etag = b.request("GET", "/api/v1/federation/sources/" + sid)
                tested, _ = b.request("POST", "/api/v1/federation/sources/" + sid + ":test", {}, etag=etag)
                assert_test(tested, revision)
                renewed = synchronize(b, sid, revision)
                if renewed["credentialRevision"] == snapshot["credentialRevision"]:
                    raise ValueError("Tutorial renewal reused an old credential generation")
                retained_job, _ = b.request("GET", "/api/v1/jobs/" + failed_job["id"])
                if retained_job["state"] != failed_job["state"] or retained_job["attempt"] != failed_job["attempt"]:
                    raise ValueError("Credential replacement revived a terminal Job")
                check_index(b, sid, identifier)
                outcome["completed"].append("renewal")
                outcome["requests"] = {"a": a.requests, "b": b.requests, "reviewer": reviewer.requests, "source": source_api.requests}
                outcome["status"] = "passed"
                code = 0
            except (Exception, KeyboardInterrupt) as error:
                original_failure = error
                outcome["failureStage"] = outcome["stage"]
                raise
            finally:
                try:
                    dc("down", "--volumes", "--remove-orphans", seconds=120)
                    for kind in ("container", "network", "volume"):
                        listing = ["docker", kind, "ls", "-q", "--filter", "label=com.docker.compose.project=" + environment["COMPOSE_PROJECT_NAME"]]
                        if kind == "container":
                            listing.append("--all")
                        identifiers = native(listing).strip()
                        if identifiers:
                            raise ValueError("Owned tutorial resources remain after cleanup")
                    # The dynamically built TLS frontend is invocation-owned too.
                    if frontend_built:
                        native(["docker", "image", "rm", environment["CATALOG_TUTORIAL_FRONTEND_IMAGE"]])
                    outcome["cleanup"] = "passed"
                    outcome["completed"].append("cleanup")
                except (OSError, ValueError, subprocess.SubprocessError) as error:
                    outcome["cleanup"] = "failed"
                    outcome["cleanupError"] = str(error) if isinstance(error, ValueError) else type(error).__name__
                    if original_failure is None:
                        raise
        if outcome["completed"] != list(PHASES):
            raise ValueError("Tutorial qualification selection was incomplete")
        if any(hashlib.sha256((ROOT / p).read_bytes()).hexdigest() != digest for p, digest in outcome["source"].items()):
            raise ValueError("Tutorial qualification source changed during execution")
    except (OSError, ValueError, KeyError, StopIteration, subprocess.SubprocessError, http.client.HTTPException, KeyboardInterrupt) as error:
        outcome["status"] = "failed"
        outcome["error"] = str(error) if isinstance(error, ValueError) else type(error).__name__
        outcome.setdefault("failureStage", outcome["stage"])
        if isinstance(error, OSError):
            outcome["errorErrno"] = error.errno
        if isinstance(error, NativeFailure):
            outcome["commandExitCode"] = error.returncode
            if error.diagnostic:
                outcome["commandDiagnostic"] = error.diagnostic
        code = 128 + error.signum if isinstance(error, Interrupted) else 130 if isinstance(error, KeyboardInterrupt) else 1
    report.write_text(json.dumps(outcome, indent=2) + "\n")
    print("Two-Catalog OIDC tutorial " + outcome["status"] + ": " + str(report.relative_to(ROOT)))
    return code


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--backend-image", required=True)
    parser.add_argument("--ui-image", required=True)
    parser.add_argument("--subnet", default="172.29.246.0/24")
    parser.add_argument("--frontend-address", default="172.29.246.10")
    args = parser.parse_args()

    def interrupt(signum, _frame):
        raise Interrupted(signum)

    previous = {signum: signal.signal(signum, interrupt) for signum in (signal.SIGTERM, signal.SIGINT)}
    try:
        return run(args)
    finally:
        for signum, handler in previous.items():
            signal.signal(signum, handler)


if __name__ == "__main__":
    sys.exit(main())
