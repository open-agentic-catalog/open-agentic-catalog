"""Two real Catalogs consume a short-lived source credential supplied by a separate workload."""
import datetime
import io
import json
import os
import subprocess
import tarfile
import time

import tools
from fixture import DOMAIN, ISSUER, TENANTS, Fixture, certificates, prepare, until
from scenario import check, member, refusal
from tutorial_oidc import (A, B, activate_publication, assert_auth_failure, assert_snapshot, assert_test,
                           check_index, check_source_refusal, publish_reviewed_release)

MANIFEST = "/.well-known/ard.json"
SOURCES = "/api/v1/federation/sources"
SOURCE_BINDING = {"networkProfile": "tutorial-a", "credentialBinding": "catalog-a-reader"}
B_AUDIENCE = "open-agentic-catalog-b"
SOURCE_PHASES = ("startup", "published-and-imported", "mixed-generation-retained", "renewed-and-read-back",
                 "peer-refusal-and-outage", "interrupted-delivery-and-expiry", "private-ca-rollout", "disabled-resolution")


class SourceFixture(Fixture):
    def resolve(self):
        self.env.update(PEER_UID=str(os.getuid()), PEER_GID=str(os.getgid()))
        overlay = tools.HERE / "source-compose.yaml"
        return self.dc("-f", overlay, "config", "--format", "json").stdout

    def start(self):
        path = self.path
        for dir_name in ("b", "b/source-credentials", "controls", "alternate", "alternate/trust", "egress"):
            (path / dir_name).mkdir()
        certificates(path / "alternate", (("catalog-a", "catalog-a.test"),))
        prepare.certificate(path / "trust", "postgres-b", ["postgres-b"])
        prepare.write(path / "egress/ca.pem", (path / "trust/ca.pem").read_text(), 0o444)
        super().start()
        self.server("entry", "create", "-parentID", DOMAIN + "agent", "-spiffeID", DOMAIN + "source-reader",
                    "-selector", "unix:uid:11007", "-jwtSVIDTTL", "100")
        self.dc("up", "-d", "peer")
        self.dc("run", "--rm", "--no-deps", "volume-init")
        address = self.inspect("peer", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}")
        policy = {"schemaVersion": 1, "policyId": "spire-b", "revision": 1, "public": {"mode": "disabled"},
                  "privateProfiles": [{"name": "tutorial-a", "origins": [A], "addressRanges": [address + "/32"],
                                       "assignedTenantIds": [TENANTS["b"]], "transportRef": "direct",
                                       "trustBundleFile": "/egress/ca.pem"}]}
        target = path / "b"
        prepare.document(target / "federation-policy.json", policy, 0o644)
        db, passwords = prepare.database_inputs(target, "b")
        for role in ("api", "worker", "owner"):
            config = json.loads((path / "a" / (role + ".json")).read_text())
            config["http"]["publicBaseUrl"] = B
            config["auth"].update(audience=B_AUDIENCE, dedicatedTenantId=TENANTS["b"])
            config["discovery"]["hosts"] = [h for h in config["discovery"]["hosts"] if h["host"] == "catalog-b.test"]
            config["federation"].update(policyFile="/inputs/b/federation-policy.json", requestTimeout="5s")
            if role != "owner":
                config["credentialResolution"] = {"enabled": True, "directory": "/credentials"}
            config["jobs"].update(maxAttempts=2, retryCeiling="5s")
            login = "catalog_" + role
            dsn = f"postgres://{login}@postgres-b/{db}?sslmode=verify-full"
            config["database"]["dsn"] = dsn
            prepare.document(target / (role + ".json"), config, 0o644)
            prepare.write(target / (role + ".dsn"), dsn.replace(login+"@", login+":"+passwords[login]+"@") +
                          "&sslrootcert=/trust/ca.pem", 0o444)
        self.dc("up", "-d", "--wait", "postgres-b")
        owner = "/inputs/b/owner.json"
        self.dc("run", "--rm", "-T", "owner-b", "migrate", "apply", "--config", owner)
        self.dc("run", "--rm", "-T", "owner-b", "identity", "provision", "--config", owner, "--tenant", TENANTS["b"],
                "--tenant-name", "Importing Catalog", "--issuer", ISSUER, "--subject", DOMAIN + "control",
                "--actor-type", "service", "--roles", "tenant_admin,reader,source_admin", "--operator", "spire-test",
                "--reason", "Manage owned indexed source")
        try:
            self.dc("up", "-d", "--wait", "api-b", "worker-b")
        except subprocess.CalledProcessError:
            # Runtime startup diagnostics are bounded and exclude configuration values.
            print(self.dc("logs", "--tail", "5", "api-b", "worker-b").stdout.decode(), flush=True)
            raise

    def inspect(self, service, template):
        container = self.dc("ps", "-q", service).stdout.decode().strip()
        return subprocess.check_output(["docker", "inspect", "-f", template, container], text=True, timeout=10).strip()

    def generation(self):
        container = self.dc("ps", "-q", "api-b").stdout.decode().strip()
        result = subprocess.run(["docker", "cp", container + ":/credentials/catalog-a-reader.json", "-"],
                                timeout=10, capture_output=True)
        if result.returncode:
            return None
        with tarfile.open(fileobj=io.BytesIO(result.stdout)) as archive:
            data = json.load(archive.extractfile("catalog-a-reader.json"))
        return data["revision"], datetime.datetime.fromisoformat(data["expiresAt"]).timestamp()

    def successor(self, expiry, timeout=75):
        def newer():
            active = self.generation()
            return active if active and active[1] > expiry else None
        return until(newer, timeout)

    def requests(self):
        lines = self.dc("logs", "--no-log-prefix", "peer").stdout.splitlines()
        return [json.loads(line) for line in lines if line.startswith(b'{"path"')]

    def processes(self):
        return {role: self.inspect(role, "{{.Id}} {{.State.StartedAt}}") for role in ("api-b", "worker-b")}

    def check_binding_mounts(self):
        shared_volumes = set()
        for role in ("writer", "api-b", "worker-b"):
            mounts = {m["Destination"]: m for m in json.loads(self.inspect(role, "{{json .Mounts}}"))}
            binding = mounts["/credentials"]
            check(binding["Type"] == "volume" and binding["RW"] == (role == "writer"),
                  "Incorrect source credential mount on " + role)
            check(("/workload" in mounts) == (role == "writer"), "Unexpected Workload API access on " + role)
            shared_volumes.add(binding["Name"])
        check(len(shared_volumes) == 1, "The writer and consumers must share the same credential volume")


def inspect_source(b):
    return b.request("POST", "/api/v1/federation/source-inspections", {"url": A + MANIFEST, **SOURCE_BINDING}, 201)[0]


def connect(b, revision):
    inspection = inspect_source(b)
    check(inspection["credentialRevision"] == revision, "Inspection used a different credential")
    return create_source(b, inspection)


def create_source(b, inspection):
    nest = {"enabled": True, "maxDepth": 2, "allowedOrigins": [A]}
    return b.request("POST", SOURCES, {**SOURCE_BINDING, "inspectionId": inspection["id"], "displayName": "Published Agents from A",
        "retrievalMode": "indexed", "updateMode": "manual_review", "scope": {"kinds": ["agent"]},
        "live": {"enabled": False}, "referrals": {"enabled": False}, "nesting": nest,
        "discoveryAssignments": [{"host": "catalog-b.test", "audience": "authenticated"}],
        "reason": "Qualify the approved peer"}, 201)[0]["id"]


def test_source(b, sid, revision):
    check(tested_revision(b, sid) == revision, "Unexpected credential in Source test")


def tested_revision(b, sid):
    path = SOURCES + "/" + sid
    before, etag = b.request("GET", path)
    tested, tested_etag = b.request("POST", path + ":test", {}, etag=etag)
    observed = tested.get("credentialRevision")
    assert_test(tested, observed)
    after, stored_etag = b.request("GET", path)
    check(tested["sourceId"] == after["id"] == sid and tested["sourceRevision"] == after["revision"] == before["revision"] + 1
          and after.get("credentialRevision") == observed and tested_etag and tested_etag == stored_etag and tested_etag != etag,
          "Source test and stored binding revision or ETag disagree")
    return observed


def queue(b, sid):
    path = SOURCES + "/" + sid
    source, etag = b.request("GET", path)
    accepted, _ = b.request("POST", path + ":sync", {}, 202, etag)
    return accepted["job"]["id"], source["revision"]


def finish(b, job_id):
    def terminal():
        job, _ = b.request("GET", "/api/v1/jobs/" + job_id)
        return job if job["state"] in ("succeeded", "failed", "cancelled", "dead_letter") else None
    job = until(terminal, 90)
    check(job["id"] == job_id and job["kind"] == "source.sync" and job["tenantId"] == TENANTS["b"],
          "Terminal Job does not match the queued synchronization on B")
    return job


def sync(b, sid, revision, identifier):
    snapshot = indexed_snapshot(b, sid, identifier)
    check(snapshot["credentialRevision"] == revision, "Unexpected credential in snapshot")
    return snapshot


def indexed_snapshot(b, sid, identifier):
    job_id, source_revision = queue(b, sid)
    job = finish(b, job_id)
    if job["state"] != "succeeded":
        latest = b.request("GET", SOURCES+"/"+sid+"/snapshots?limit=1")[0]["items"]
        findings = b.request("GET", "/api/v1/federation/snapshots/"+latest[0]["id"])[0].get("findings", []) if latest else []
        raise ValueError("Sync failed: " + str((job["state"], job.get("failureClass"), findings)))
    result = job["result"]
    check(result["type"] == "source_snapshot" and result["state"] == "complete" and result["sourceId"] == sid,
          "Job result does not name the expected source and complete snapshot")
    snap = b.request("GET", "/api/v1/federation/snapshots/" + result["snapshotId"])[0]
    check(snap["id"] == result["snapshotId"] and snap["tenantId"] == TENANTS["b"], "Job points to a different snapshot")
    observed = snap.get("credentialRevision")
    assert_snapshot(snap, b.request("GET", SOURCES + "/" + sid)[0], source_revision, observed, sid)
    check_index(b, sid, identifier)
    return {"jobId": job_id, "snapshotId": snap["id"], "sourceRevision": source_revision,
            "credentialRevision": observed, "retrievals": [{k: r[k] for k in ("status", "receivedBody", "reused")}
                                                          for r in snap["retrievals"]]}


def preserve(b, sid, prior, identifier):
    source = b.request("GET", SOURCES + "/" + sid)[0]
    check(source["lastCompleteSnapshotId"] == prior["snapshotId"], "Failure displaced a complete snapshot")
    check_index(b, sid, identifier)
    return source


def local_refusal(f, b, sid, prior, identifier):
    before = len(f.requests())
    check_source_refusal(b, sid)
    job = finish(b, queue(b, sid)[0])
    check(job["state"] in ("failed", "dead_letter") and len(f.requests()) == before,
          "Bound operation unexpectedly reached A or completed")
    preserve(b, sid, prior, identifier)


def repair_missing_leaf(f, b, sid, prior, identifier, expiry):
    # The writer is stopped. Keep the original bytes private so the repair can
    # prove that it reprojected the cached JWT, rather than claiming a renewal.
    setup = ("import json, sys\nsys.path.insert(0, '/opt')\nimport renew\nd = renew.DIRECTORY\n"
             "def active_leaf():\n    return d / json.loads((d / renew.BINDING).read_bytes())['secretFile']\n")

    def writer_command(code):
        f.dc("run", "--rm", "--no-deps", "-T", "--entrypoint", "python", "writer", "-Bc", setup + code)

    writer_command("active_leaf().rename(d / '.held-token')")
    local_refusal(f, b, sid, prior, identifier)
    writer_command("renew.refresh()\nheld = d / '.held-token'\n"
                   "if held.read_bytes() != active_leaf().read_bytes():\n    sys.exit(1)\nheld.unlink()")
    repaired, repaired_expiry = f.generation()
    check(repaired != prior["credentialRevision"] and repaired_expiry == expiry,
          "Repair must change the binding revision and retain the cached JWT expiry")
    test_source(b, sid, repaired)
    return sync(b, sid, repaired, identifier)


def switch_during_import(f, b, sid, identifier, first, expiry):
    controls = f.path / "controls"
    # The candidate is already issued and valid before the bounded source operation starts.
    probe = """import sys,time
sys.path.insert(0, '/opt')
import renew
deadline = time.monotonic() + 99
while time.monotonic() < deadline:
    if renew.lifetime(renew.acquire(), time.time()) > int(sys.argv[1]):
        sys.exit(0)
    time.sleep(1)
sys.exit(1)
"""
    f.dc("run", "--rm", "--no-deps", "-T", "--entrypoint", "python", "writer", "-Bc", probe, str(int(expiry)))
    # Kill a one-shot copy of the real writer after the successor and metadata
    # have been fsynced but immediately before the atomic rename.
    interrupt = ("import os,sys; sys.path.insert(0,'/opt'); import renew; "
                 "renew.os.replace=lambda *_: os._exit(73); renew.refresh()")
    interrupted = f.dc("run", "--rm", "--no-deps", "-T", "--entrypoint", "python", "writer", "-Bc", interrupt, check=False)
    check(interrupted.returncode == 73 and f.generation()[0] == first, "Staging interruption activated a credential")
    previous = sync(b, sid, first, identifier)
    check(expiry > time.time()+5, "First token expired before the controlled handoff")
    start = len(f.requests())
    snapshots_path = SOURCES + "/" + sid + "/snapshots"
    before = {s["id"] for s in b.request("GET", snapshots_path)[0]["items"]}
    (controls / "hold").touch()
    job_id, _ = queue(b, sid)
    until((controls / "held").exists, 15)
    f.dc("start", "writer")
    second, next_expiry = f.successor(expiry, timeout=8)
    check(second != first, "New material reused the old revision")
    (controls / "block-retry").touch()
    (controls / "hold").unlink()
    f.dc("stop", "-t", "1", "writer")
    mixed = finish(b, job_id)
    reads = f.requests()[start:]
    check([(r["path"], r["status"], r.get("acceptedExpiry")) for r in reads[:2]]
          == [(MANIFEST, 200, expiry), ("/child.json", 200, next_expiry)]
          and all(r["authenticated"] for r in reads) and mixed["state"] in ("failed", "dead_letter"),
          "Unexpected mixed-generation Job " + str((mixed["state"], mixed["attempt"], mixed.get("failureClass"), reads)))
    preserve(b, sid, previous, identifier)
    attempts = [s for s in b.request("GET", snapshots_path)[0]["items"] if s["id"] not in before]
    mixed_snapshots = [s["id"] for s in attempts if s.get("credentialRevision") == first
                       and s["state"] == "failed" and not s["enumerationComplete"] and not s["withdrawalAuthority"]]
    check(len(mixed_snapshots) == 1 and all(s["state"] == "failed" for s in attempts),
          "Missing failed A1 snapshot for the mixed attempt")
    (controls / "block-retry").unlink()
    recovered_nested = sync(b, sid, second, identifier)
    check(recovered_nested["snapshotId"] != previous["snapshotId"], "Nested recovery did not advance")
    return second, next_expiry, {"id": job_id, "failedSnapshotId": mixed_snapshots[0],
                                 "peerAcceptedExpiries": [r["acceptedExpiry"] for r in reads[:2]],
                                 "recovery": recovered_nested}


def recover_writer(f, b, sid, identifier, previous, next_expiry, processes, report):
    # Use the sole writer UID for the missing-binding control, while its loop is stopped.
    def move_binding(reverse):
        old, new = ("catalog-a-reader.json", ".saved")
        if reverse:
            old, new = new, old
        f.dc("run", "--rm", "--no-deps", "-T", "--entrypoint", "python", "writer", "-Bc",
             f"import os; os.rename('/credentials/{old}', '/credentials/{new}')")
    move_binding(False)
    local_refusal(f, b, sid, previous, identifier)
    move_binding(True)
    until(lambda: time.time() >= next_expiry+1, 110)
    local_refusal(f, b, sid, previous, identifier)
    f.dc("start", "writer")
    third, third_expiry = f.successor(next_expiry)
    test_source(b, sid, third)
    current = sync(b, sid, third, identifier)
    check(current["snapshotId"] != previous["snapshotId"], "Writer recovery did not advance")
    check(f.processes() == processes, "Writer recovery restarted importer")
    report["resumed"] = current
    # Leave the writer running across a full native Agent renewal interval.
    # No manual file operation or process lifecycle transition selects this successor.
    active_writer = f.inspect("writer", "{{.Id}} {{.State.StartedAt}}")
    fourth, expiry = f.successor(third_expiry, timeout=110)
    test_source(b, sid, fourth)
    current = sync(b, sid, fourth, identifier)
    check(f.inspect("writer", "{{.Id}} {{.State.StartedAt}}") == active_writer and f.processes() == processes,
          "A process restarted during unattended renewal")
    report["unattendedRenewal"] = current
    report["usableSecondsAfterUnattendedRenewal"] = int(expiry-time.time())


def verify_disabled(f, b, sid, identifier, prior):
    for role in ("api", "worker"):
        config_file = f.path / "b" / (role + ".json")
        config = json.loads(config_file.read_text())
        config["credentialResolution"] = {"enabled": False}
        config_file.write_text(json.dumps(config))
    f.dc("restart", "api-b", "worker-b")
    until(lambda: b.request("GET", "/readyz"))
    capabilities = b.request("GET", "/api/v1/capabilities")[0]
    check(capabilities["optionalCapabilities"]["credentialResolution"] == "disabled", "Resolver still advertised after disabling")
    local_refusal(f, b, sid, prior, identifier)
    start = len(f.requests())
    public, _ = b.request("POST", "/api/v1/federation/source-inspections",
                          {"url": A + "/public.json", "networkProfile": "tutorial-a"}, 201)
    check(public["entryCount"] == 1 and not public.get("credentialRevision"), "Unexpected public inspection")
    source, _ = b.request("POST", SOURCES, {"inspectionId": public["id"], "networkProfile": "tutorial-a",
        "displayName": "Public Agent", "retrievalMode": "indexed", "updateMode": "manual_review",
        "scope": {"kinds": ["agent"]}, "reason": "Verify public imports with credential resolution disabled"}, 201)
    public_id = source["id"]
    test_source(b, public_id, None)
    job_id, source_revision = queue(b, public_id)
    job = finish(b, job_id)
    check(job["state"] == "succeeded" and job["result"]["sourceId"] == public_id, "Uncredentialed import did not complete")
    snapshot_id = job["result"]["snapshotId"]
    snapshot, _ = b.request("GET", "/api/v1/federation/snapshots/" + snapshot_id)
    stored, _ = b.request("GET", SOURCES + "/" + public_id)
    assert_snapshot(snapshot, stored, source_revision, None, public_id)
    check(snapshot["id"] == snapshot_id and snapshot["memberCount"] == 1, "Public snapshot did not contain the inspected member")
    page, _ = b.request("POST", "/api/v1/catalog/search", {"origins": ["indexed"], "kinds": ["agent"], "limit": 10})
    check(any(item["identifier"] == "urn:air:example.org:default:public" and public_id in item["indexed"]["sourceIds"]
              for item in page["items"]), "Public member missing from B's index")
    reads = f.requests()[start:]
    check(reads and all(r["path"] == "/public.json" and r["status"] == 200 and not r["authenticated"] for r in reads),
          "Uncredentialed source used an unexpected peer route or credential")
    preserve(b, sid, prior, identifier)
    return {"sourceId": public_id, "jobId": job_id, "snapshotId": snapshot_id}


def exercise_sources(f, report):
    done = report["completed"]

    def phase(name):
        report["stage"] = name
        print("Checking", name, flush=True)

    phase("published-and-imported")
    a = f.client(until(lambda: f.issue("admin")))
    operator = f.issue("control", audience=B_AUDIENCE)
    b = f.client(operator, side="b")
    check(b.request("GET", "/api/v1/me")[0]["tenantId"] == TENANTS["b"], "Incorrect operator tenant")
    for role in ("reviewer", "reader"):
        member(a, "reviewer", role)
    identifier, _ = publish_reviewed_release(a, f.client(f.issue("reviewer")))
    activate_publication(a, identifier)
    f.client(operator).request("GET", MANIFEST, expected=401)
    f.client(f.issue("control"), side="b").request("GET", "/api/v1/me", expected=401)
    membership = member(a, "source-reader", "service_client")
    role, _ = a.request("POST", "/api/v1/access/roles", {"name": "Publication only", "permissions": ["discovery.read"],
                       "reason": "Read the approved source"}, 201)
    grants = "/api/v1/access/assignments"
    grant = {"roleDefinitionId": role["id"], "membershipId": membership["membershipId"], "reason": "Read publication"}
    assignment, grant_etag = a.request("POST", grants, grant, 201)
    f.issue("control", selected="source-reader", denied=True)
    reader_member = member(b, "reviewer", "service_client")
    reader_role, _ = b.request("POST", "/api/v1/access/roles", {"name": "Indexed discovery only",
                               "permissions": ["federation.discover"], "reason": "Search imported metadata"}, 201)
    b.request("POST", grants, {"roleDefinitionId": reader_role["id"], "membershipId": reader_member["membershipId"],
                              "reason": "Read the retained index"}, 201)
    reader = f.client(f.issue("reviewer", audience=B_AUDIENCE), side="b")
    processes = f.processes()
    f.dc("up", "-d", "writer")
    f.check_binding_mounts()
    first, expiry = until(f.generation)
    report["initialUsableSeconds"] = int(expiry - time.time())
    report["writerImage"] = f.inspect("writer", "{{.Image}}")
    f.dc("stop", "-t", "1", "writer")
    check(f.generation()[0] == first, "First generation changed before pause")
    sid = connect(b, first)
    test_source(b, sid, first)
    original = sync(b, sid, first, identifier)
    report["cachedTokenRepair"] = repair_missing_leaf(f, b, sid, original, identifier, expiry)
    first = report["cachedTokenRepair"]["credentialRevision"]
    controls = f.path / "controls"
    (controls / "nested").touch()
    previous = sync(b, sid, first, identifier)
    check(len(previous["retrievals"]) == 2, "Nested baseline did not fetch both documents")
    report.update(sourceId=sid, identifier=identifier, version="1.0.0",
                  initial=original, sourceReadIdentity=DOMAIN + "source-reader", importerProcesses=processes,
                  publisherTenant=TENANTS["a"], importerTenant=TENANTS["b"])
    done.append(report["stage"])

    phase("mixed-generation-retained")
    second, next_expiry, report["mixedJob"] = switch_during_import(f, b, sid, identifier, first, expiry)
    done.append(report["stage"])

    (controls / "nested").unlink()
    phase("renewed-and-read-back")
    test_source(b, sid, second)
    start = len(f.requests())
    current = sync(b, sid, second, identifier)
    repeated = sync(b, sid, second, identifier)
    reads = f.requests()[start:]
    check(current["sourceRevision"] == repeated["sourceRevision"] and current["snapshotId"] != original["snapshotId"]
          and all(s["retrievals"] == [{"status": 200, "receivedBody": True, "reused": False}] for s in (current, repeated))
          and len(reads) == 2 and all(r["hasETag"] and r["acceptedExpiry"] == next_expiry and not r["conditional"] and r["status"] == 200 and
          {"authorization", "accept-encoding"} <= {v.strip().lower() for line in r["vary"] for v in line.split(",")}
          for r in reads), "Authenticated manifest was not read in full twice at the same revision")
    prepare.write(f.path / "operator.jwt", operator, 0o444)
    cli = f.dc("run", "--rm", "--no-deps", "-T", "-v", str(f.path/"operator.jwt")+":/inputs/operator.jwt:ro",
               "--entrypoint", "/usr/local/bin/catalogctl", "reader", "remote", "job", "--id", repeated["jobId"],
               "--base-url", B, "--token-file", "/inputs/operator.jwt", "--ca-file", "/trust/ca.pem")
    cli_job = json.loads(cli.stdout)["data"]
    check(cli_job["id"] == repeated["jobId"] and cli_job["result"]["snapshotId"] == repeated["snapshotId"],
          "Installed catalogctl read a different Job")
    check(f.processes() == processes, "Importer restarted during credential renewal")
    report.update(renewed=current, sameRevisionRead=repeated, usableSecondsAfterHandoff=int(next_expiry-time.time()))
    done.append(report["stage"])

    phase("peer-refusal-and-outage")
    a.request("POST", grants+"/"+assignment["id"]+":revoke", {"reason": "Withdraw publication read"}, etag=grant_etag)
    check_source_refusal(b, sid, second)
    start = len(f.requests())
    refused = finish(b, queue(b, sid)[0])
    peer_after_job = f.requests()
    refusal_reads = [(r["path"], r["authenticated"], r["status"]) for r in peer_after_job[start:]]
    refused_manifest = (MANIFEST, True, 403)
    legacy_probe = ("/.well-known/ai-catalog.json", True, 404)
    check(refusal_reads in ([refused_manifest], [refused_manifest, legacy_probe]),
          "Unexpected peer requests for the refused import: " + str(refusal_reads))
    assert_auth_failure(refused, preserve(b, sid, repeated, identifier), repeated["snapshotId"])
    check_index(reader, sid, identifier)
    refusal(reader, SOURCES + "/" + sid, 403, "FORBIDDEN")
    extra = [(r["path"], r["authenticated"], r["status"]) for r in f.requests()[len(peer_after_job):]]
    check(not extra, "Reading retained metadata contacted the peer: " + str(extra))
    a.request("POST", grants, grant, 201)
    restored = sync(b, sid, second, identifier)
    (controls / "outage").touch()
    start = len(f.requests())
    outage = finish(b, queue(b, sid)[0])
    reads = f.requests()[start:]
    check(outage["state"] == "dead_letter" and outage["attempt"] == 2 and outage["failureClass"] == "transient_upstream"
          and [r["status"] for r in reads if r["path"] == MANIFEST] == [503, 503], "503 did not use the bounded transient retry")
    preserve(b, sid, restored, identifier)
    (controls / "outage").unlink()
    current = sync(b, sid, second, identifier)
    report["peerFailures"] = {"refusal": refused["failureClass"], "outage": outage["failureClass"], "attempts": outage["attempt"],
                              "refusalRequests": refusal_reads, "indexedReadIdentity": DOMAIN + "reviewer"}
    done.append(report["stage"])

    phase("interrupted-delivery-and-expiry")
    recover_writer(f, b, sid, identifier, current, next_expiry, processes, report)
    done.append(report["stage"])

    phase("private-ca-rollout")
    # Keep the same still-valid bearer on both sides of the server CA change.
    f.dc("stop", "-t", "1", "writer")
    fixed, expiry = f.generation()
    test_source(b, sid, fixed)
    report["trustBaseline"] = sync(b, sid, fixed, identifier)
    (controls / "alternate-ca").touch()
    local_refusal(f, b, sid, report["trustBaseline"], identifier)
    trust = f.path / "egress/ca.pem"
    replacement = trust.with_suffix(".next")
    prepare.write(replacement, trust.read_text()+(f.path/"alternate/trust/ca.pem").read_text(), 0o444)
    replacement.replace(trust)
    f.dc("restart", "api-b", "worker-b")
    until(lambda: b.request("GET", "/readyz"))
    test_source(b, sid, fixed)
    report["trustRecovery"] = sync(b, sid, fixed, identifier)
    check(report["trustRecovery"]["snapshotId"] != report["trustBaseline"]["snapshotId"], "CA recovery did not advance the snapshot")
    rolled = f.processes()
    check(all(rolled[role] != prior for role, prior in processes.items()), "Egress trust rollout missed a consumer")
    done.append(report["stage"])

    phase("disabled-resolution")
    report["publicImport"] = verify_disabled(f, b, sid, identifier, report["trustRecovery"])
    check(f.generation() == (fixed, expiry) and time.time() < expiry,
          "CA rollover and disabled-resolution controls require one unexpired credential")
    done.append(report["stage"])
