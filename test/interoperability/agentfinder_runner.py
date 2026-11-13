"""Run real Catalog source lifecycle against unchanged owned HTTPS publication bytes."""

import argparse
import ipaddress
import json
import os
from pathlib import Path
import secrets
import shutil
import signal
import sys
import tempfile
import time
import urllib.parse

from agntcy_lab import LocalHTTP, UnexpectedStatus
from common import run
from peer_catalog import sync_attempt, wait_for
import runner as shared
import agentfinder_tools as tools
import agentfinder_contract as contract

ROOT = tools.ROOT
OUTPUT = ROOT / "output/interoperability/agentfinder"
PREFIX = "/api/v1/federation/"
ORIGIN = "https://federation-fixture:8443"


def source_inputs():
    """Select the application, fixture and shared harness inputs used by this run."""
    interop = ROOT / "test/interoperability"
    return (*shared.CATALOG_INPUTS, ROOT / "Containerfile", ROOT / ".dockerignore", ROOT / "LICENSE",
            ROOT / "deployments/container/licenses.sh", ROOT / "deployments/postgres/init.sql",
            ROOT / "compose.yaml", ROOT / "Makefile", ROOT / "config/postgres.json",
            ROOT / "test/functional/fixture", ROOT / "test/functional/Containerfile", ROOT / "test/functional/compose.yaml",
            ROOT / "examples/webhookreceiver", interop / "agentfinder", interop / "Containerfile", *sorted(interop.glob("*.py")))


def source_fingerprint():
    """Bind source names and bytes with the maintained interoperability helper."""
    return shared.fingerprint(source_inputs())


class Catalog:
    """Fixed loopback HTTP control; never a destination from imported metadata."""
    def __init__(self, origin, fixture_origin, certificate):
        self.http = LocalHTTP({"catalog": origin, "fixture": fixture_origin}, certificate)
        self.unexpected = []

    def request(self, path, body=None, *, expected=200, etag=None, token="local-admin"):
        """Keep each selected actor scoped to one bounded local request."""
        saved = self.http.token
        try:
            self.http.token = token
            return self.http.document("catalog", path, body, expected=expected, etag=etag)
        except UnexpectedStatus as error:
            self.unexpected.append({"path": path, "status": error.status, "problem": json.loads(error.raw)})
            raise
        finally:
            self.http.token = saved

    def pages(self, path, **query):
        """Collect every administrative page with finite work and unique item IDs."""
        items, cursors = [], set()
        for _ in range(30):
            page, _ = self.request(path + "?" + urllib.parse.urlencode(dict(query, limit=100)))
            items.extend(page["items"])
            cursor = page.get("nextCursor")
            if not cursor:
                assert len({item["id"] for item in items}) == len(items), "duplicate paged item"
                return items
            assert cursor not in cursors, "pagination cycle"
            cursors.add(cursor)
            query["cursor"] = cursor
        raise AssertionError("administrative page budget exceeded")

    def enqueue(self, source_id):
        """Queue fresh work against the currently read source ETag."""
        path = PREFIX + "sources/" + source_id
        _, headers = self.request(path)
        receipt, _ = self.request(path + ":sync", {}, etag=headers["etag"], expected=202)
        assert receipt["existing"] is False, "synchronization reused another Job"
        return receipt["job"]

    def complete(self, job_id):
        """Wait for this exact Job and bind its completed snapshot and audit."""
        def terminal():
            job, _ = self.request("/api/v1/jobs/" + job_id)
            assert job["state"] not in ("failed", "dead_letter", "cancelled"), "synchronization terminated"
            return job if job["state"] == "succeeded" else None
        job = wait_for("Agent Finder synchronization", terminal, timeout=180)
        snapshot, _ = self.request(PREFIX + "snapshots/" + job["result"]["snapshotId"])
        assert snapshot["state"] == "complete" and snapshot["enumerationComplete"] is True
        assert snapshot["memberCount"] == tools.TOTAL_ENTRIES and snapshot["bodyDigest"] == "sha256:" + tools.EXPECTED_SHA256
        assert sync_attempt(snapshot, job_id, request=self.request) == job["attempt"]
        return job, snapshot

    def members(self, source_id):
        """Read the complete current administrative source population."""
        return self.pages("/api/v1/observations", sourceId=source_id, freshness="current")

    def search_excluded(self):
        """Require an authorized reader response with no unsupported projection."""
        page, _ = self.request("/api/v1/catalog/search", {"origins": ["indexed"], "limit": 100}, token="local-reader")
        assert page["items"] == [] and not page.get("nextCursor") and page["partial"] is False


def persistence(sql, source_id, inspection_id, job, snapshot, current_snapshot):
    """Owner SQL is read-only and binds one source, Job, snapshot and every occurrence."""
    source_id, inspection_id, job_id, snapshot_id = map(contract.uuid_literal, (source_id, inspection_id, job["id"], snapshot["id"]))
    value = sql(f"""SELECT json_build_object(
 'source_bound',s.revision=n.source_revision AND s.status='active' AND s.endpoint='{ORIGIN}/ai-catalog.json' AND s.configuration->>'profileId'='{contract.LEGACY}',
 'inspection_bound',i.document->>'bodyDigest'=n.body_digest AND i.document->>'profileId'=s.configuration->>'profileId' AND (i.document->>'entryCount')::int={tools.TOTAL_ENTRIES},
 'connection_bound',(SELECT count(*)=1 FROM audit_events a WHERE a.tenant_id=s.tenant_id AND a.target_id=s.id::text AND a.action='source.connected' AND a.details->'facts'->>'inspectionId'=i.id::text),
 'job_bound',j.kind='source.sync' AND j.state='succeeded' AND j.payload->>'sourceId'=s.id::text AND (j.payload->>'sourceRevision')::bigint=s.revision AND j.result->>'id'=n.id::text AND j.attempt={int(job['attempt'])},
 'snapshot_bound',n.state='complete' AND n.enumeration_complete AND n.withdrawal_authority AND n.member_count={tools.TOTAL_ENTRIES} AND n.body_digest='sha256:{tools.EXPECTED_SHA256}' AND s.last_complete_snapshot_id='{contract.uuid_literal(current_snapshot)}',
 'audit_bound',(SELECT count(*)=1 FROM audit_events a WHERE a.tenant_id=s.tenant_id AND a.target_id=n.id::text AND a.action='source.sync.completed' AND a.details->'facts'->>'jobId'=j.id::text AND (a.details->'facts'->>'jobAttempt')::int=j.attempt),
 'no_adoption',NOT EXISTS(SELECT 1 FROM resources WHERE tenant_id=s.tenant_id) AND NOT EXISTS(SELECT 1 FROM listing_releases WHERE tenant_id=s.tenant_id) AND NOT EXISTS(SELECT 1 FROM publication_snapshots WHERE tenant_id=s.tenant_id))
 FROM federation_sources s JOIN source_snapshots n ON n.tenant_id=s.tenant_id AND n.source_id=s.id AND n.id='{snapshot_id}'
 JOIN jobs j ON j.tenant_id=s.tenant_id AND j.id='{job_id}'
 JOIN source_inspections i ON i.tenant_id=s.tenant_id AND i.id='{inspection_id}'
 WHERE s.tenant_id='{contract.TENANT}' AND s.id='{source_id}'""")
    contract.check_bindings(value)
    members = sql(f"""SELECT coalesce(json_agg(json_build_object('id',o.id,'tenantId',o.tenant_id,'sourceId',o.source_id,
 'exactEntryDigest',o.exact_entry_digest,'raw',replace(encode(o.raw_entry_bytes,'base64'),chr(10),''),'entry',convert_from(o.raw_entry_bytes,'UTF8')::json,
 'versionPresent',o.version_present,'version',o.version,'snapshotId',c.snapshot_id,'sourceRevision',c.source_revision,
 'retrievalDigest',c.retrieval_digest,'entryLocation',c.entry_location,'evidenceComplete',c.evidence_complete)), '[]'::json)
 FROM observations o JOIN observation_occurrences c ON c.tenant_id=o.tenant_id AND c.source_id=o.source_id AND c.observation_id=o.id
 WHERE o.tenant_id='{contract.TENANT}' AND o.source_id='{source_id}' AND c.snapshot_id='{snapshot_id}'""")
    return value, members


def lifecycle(api, entries, raw, report, checkpoint):
    """Exercise real source approval, worker retention and explicit failure controls."""
    checkpoint("inspection")
    inspection, _ = api.request(PREFIX + "source-inspections", {"url": ORIGIN + "/ai-catalog.json", "networkProfile": "functional-fixture"}, expected=201)
    report["inspection"] = inspection
    assert inspection["profileId"] == contract.LEGACY and inspection["mediaType"] == "application/ai-catalog+json"
    assert inspection["bodyDigest"] == contract.digest(raw) and inspection["entryCount"] == len(entries)
    assert inspection["capabilities"]["enumerable"] is True
    generic, _ = api.request(PREFIX + "source-inspections", {"url": ORIGIN + "/generic/ai-catalog.json", "networkProfile": "functional-fixture"}, expected=400)
    assert generic["code"] == "INVALID_ARGUMENT", "unexpected generic-manifest disposition"
    report["genericTransportRefusal"] = generic
    retained, _ = api.request(PREFIX + "source-inspections/" + inspection["id"])
    assert retained == inspection
    source, _ = api.request(PREFIX + "sources", {"inspectionId": inspection["id"], "displayName": "Owned Agent Finder publication",
        "retrievalMode": "indexed", "updateMode": "manual_review", "networkProfile": "functional-fixture",
        "scope": {"publishers": ["github.com", "registry.modelcontextprotocol.io"]}, "reason": "Qualify unchanged pinned publication"}, expected=201)
    report["sourceId"] = source["id"]
    report["phases"]["inspection"] = "passed"

    checkpoint("retention")
    job, snapshot = api.complete(api.enqueue(source["id"])["id"])
    members = api.members(source["id"])
    contract.check_members(members, entries, source["id"])
    by_identity = {(m["identifier"], m["versionPresent"], m.get("version", "")): m for m in members}
    for entry in entries:
        member = by_identity[contract.identity(entry)]
        exact, headers = api.http.request("catalog", "/api/v1/observations/" + member["id"] + "/raw")
        contract.check_raw(exact, entry, member, raw)
        assert headers.get("content-digest") and headers.get("content-disposition", "").startswith("attachment;")
    api.search_excluded()
    report["retention"] = {"job": job, "snapshot": snapshot, "memberCount": len(members),
                            "searchable": 0, "support": "unknown", "rawMetadataVerified": len(entries)}
    report["phases"]["retention"] = "passed"
    prior_snapshot = api.request(PREFIX + "snapshots/" + snapshot["id"])[0]

    def stable():
        """Compare retained identities and the original immutable snapshot after each attempt."""
        current = api.members(source["id"])
        fields = ("id", "sourceKey", "identifier", "version", "versionPresent", "mediaType", "exactEntryDigest", "identityDigest", "compatibility", "firstSeenAt")
        def project(values):
            return sorted(json.dumps({k: v.get(k) for k in fields}, sort_keys=True) for v in values)
        assert project(current) == project(members), "unchanged sync changed retained identities/evidence"
        old = api.request(PREFIX + "snapshots/" + snapshot["id"])[0]
        assert old == prior_snapshot, "immutable snapshot changed"
        return current

    checkpoint("repeat")
    repeated_job, repeated_snapshot = api.complete(api.enqueue(source["id"])["id"])
    assert repeated_snapshot["id"] != snapshot["id"] and all(repeated_snapshot[k] == 0 for k in ("added", "updated", "withdrawn"))
    stable()
    report["repeat"] = {"job": repeated_job, "snapshot": repeated_snapshot}
    report["phases"]["repeat"] = "passed"

    checkpoint("outage")
    api.http.document("fixture", "/control", {"unavailable": True})
    outage = api.enqueue(source["id"])
    def waiting():
        """Observe the current failed attempt before permitting recovery."""
        row = api.request("/api/v1/jobs/" + outage["id"])[0]
        assert row["state"] not in ("failed", "dead_letter", "cancelled", "succeeded"), "outage Job unexpectedly terminated"
        return row if row["state"] == "waiting" and row["attempt"] > 0 else None
    waiting_job = wait_for("observed failed retrieval", waiting, timeout=90)
    failures = api.pages(PREFIX + "sources/" + source["id"] + "/snapshots")
    failed = next(row for row in failures if row["state"] == "failed" and sync_attempt(row, outage["id"], request=api.request) == waiting_job["attempt"])
    assert failed["sourceId"] == source["id"] and failed["sourceRevision"] == source["revision"]
    assert api.request(PREFIX + "sources/" + source["id"])[0]["lastCompleteSnapshotId"] == repeated_snapshot["id"]
    stable()
    api.search_excluded()
    report["outage"] = {"job": waiting_job, "snapshot": failed, "preservedMembers": len(members)}
    report["phases"]["outage"] = "passed"

    checkpoint("recovery")
    api.http.document("fixture", "/control", {"unavailable": False})
    recovered_job, recovered_snapshot = api.complete(outage["id"])
    assert recovered_job["attempt"] > waiting_job["attempt"] and all(recovered_snapshot[k] == 0 for k in ("added", "updated", "withdrawn"))
    stable()
    report["recovery"] = {"job": recovered_job, "snapshot": recovered_snapshot}
    report["phases"]["recovery"] = "passed"

    checkpoint("refusals")
    for path in (PREFIX + "sources/" + source["id"], PREFIX + "snapshots/" + snapshot["id"], "/api/v1/observations/" + members[0]["id"] + "/occurrences", "/api/v1/observations/" + members[0]["id"] + "/raw"):
        problem, _ = api.request(path, token="other-tenant-admin", expected=404)
        assert problem["code"] == "NOT_FOUND"
    denied, _ = api.request(PREFIX + "source-inspections", {"url": ORIGIN + "/ai-catalog.json", "networkProfile": "functional-fixture"}, token="other-tenant-admin", expected=403)
    assert denied["code"] == "SOURCE_POLICY_DENIED"
    # The separately generated CA is valid, but did not sign this publisher.
    untrusted, _ = api.request(PREFIX + "source-inspections", {"url": ORIGIN + "/ai-catalog.json", "networkProfile": "agentfinder-untrusted"}, expected=503)
    assert untrusted["code"] == "DEPENDENCY_UNAVAILABLE"
    report["refusals"] = {"otherTenantReads": 4, "policyStatus": 403, "untrustedTLSStatus": 503}
    report["phases"]["refusals"] = "passed"
    api.search_excluded()
    report["members"] = members


def owned_resources(project, environment):
    """Include stopped containers when independently checking owned cleanup."""
    remaining = {}
    for kind in ("container", "network", "volume"):
        arguments = ["docker", kind, "ls", *(["--all"] if kind == "container" else []), "--quiet", "--filter", "label=com.docker.compose.project=" + project]
        remaining[kind] = run(arguments, env=environment, timeout=15).stdout.strip()
    return remaining


def cleanup_owned(compose, env, project, images, private, report):
    """Remove only the invocation's recorded resources and retain cleanup status."""
    directory = private.parent
    errors = []
    for name in [project + "-generate", *report.get("probeNames", []), *([report["clientName"]] if report.get("clientName") else [])]:
        try:
            run(["docker", "rm", "-f", name], env=env, timeout=15, check=False)
        except Exception as error:
            errors.append("auxiliary container: " + str(error))
    try:
        for service in ("api", "worker", "postgres", "migrate", "provision", "federation-fixture"):
            cid = run(compose + ["ps", "--all", "--quiet", service], env=env, timeout=15, check=False).stdout.strip()
            if cid:
                result = run(["docker", "logs", cid], env=env, timeout=15, check=False, limit=4 << 20)
                (directory / (service + ".log")).write_text(result.stdout + result.stderr)
    except Exception as error:
        report["diagnosticError"] = str(error)[-2000:]
    try:
        run(compose + ["down", "--volumes", "--remove-orphans"], env=env, timeout=120)
    except Exception as error:
        errors.append("Compose teardown: " + str(error))
    for tag in images:
        try:
            if run(["docker", "image", "inspect", tag], env=env, check=False).code == 0:
                run(["docker", "image", "rm", tag], env=env, timeout=60)
        except Exception as error:
            errors.append("owned image: " + str(error))
    try:
        remaining = owned_resources(project, env)
        assert not any(remaining.values()), "owned resource residue"
        shutil.rmtree(private)
    except Exception as error:
        errors.append("owned inventory/private files: " + str(error))
    report["cleanup"] = "failed" if errors else "complete"
    if errors:
        report["cleanupError"] = "\n".join(errors)[-4000:]


def run_qualification():
    """Own one bounded disposable stack and report cleanup independently."""
    require_assertions()
    stats = tools.installed()
    shared.local_daemon()
    command_image = tools.runner_image()
    shared.docker("image", "inspect", command_image)
    before = source_fingerprint()
    OUTPUT.mkdir(parents=True, exist_ok=True)
    directory = Path(tempfile.mkdtemp(prefix="run-", dir=OUTPUT)).resolve()
    private = directory / "private"
    private.mkdir(mode=0o700)
    fixture = private / "fixture"
    fixture.mkdir(mode=0o755)
    suffix = secrets.token_hex(6)
    project = "open-agentic-catalog-agentfinder-" + suffix
    image, fixture_image = project + ":catalog", project + ":fixture"
    env = {k: os.environ[k] for k in ("PATH", "HOME", "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG", "XDG_RUNTIME_DIR", "SSH_AUTH_SOCK") if k in os.environ}
    subnet = "172.31." + str(secrets.randbelow(256)) + ".0/28"
    env.update(CATALOG_ENV_FILE="/dev/null", IMAGE=image, VERSION="agentfinder-test", COMMIT=run(["git", "rev-parse", "HEAD"]).stdout.strip(), SOURCE_STATE="unknown",
        CATALOG_FUNCTIONAL_FIXTURE_IMAGE=fixture_image, CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY=str(fixture),
        CATALOG_COMPOSE_HTTP_PORT="0", CATALOG_COMPOSE_FIXTURE_PORT="0", CATALOG_COMPOSE_POSTGRES_PORT="0", CATALOG_COMPOSE_SUBNET=subnet,
        CATALOG_COMPOSE_FEDERATION_POLICY_FILE=str(fixture / "policy.json"), CATALOG_COMPOSE_FEDERATION_TRUST_DIRECTORY=str(fixture / "trust"))
    compose = ["docker", "compose", "--env-file", "/dev/null", "-f", "compose.yaml", "-f", "test/functional/compose.yaml", "-f", "test/interoperability/agentfinder/compose.yaml", "-p", project]
    report = {"status": "incomplete", "sourceCommit": env["COMMIT"], "sourceDigest": before, "publisher": {"revision": tools.REVISION, **stats},
        "phases": dict.fromkeys(contract.PHASES, "unrun"), "cleanup": "unrun", "project": project, "images": {}, "runnerImage": {"reference": command_image, "id": shared.docker("image", "inspect", "--format={{.Id}}", command_image).stdout.strip()},
        "scope": "one-way static publication; authenticated administrative retention; no searchable transfer"}
    report_path = directory / "report.json"
    deadline = time.monotonic() + 1800

    def save():
        """Persist the current partial outcome before the next external process."""
        report_path.write_text(json.dumps(report, indent=2) + "\n")

    def checkpoint(phase):
        """Record the active phase without prematurely marking it successful."""
        report["phase"] = phase
        save()
        print("Agent Finder: " + phase, flush=True)

    def command(args, *, timeout=120, check=True, limit=16 << 20):
        """Bound each child by both its local and overall execution deadline."""
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise TimeoutError("Agent Finder campaign deadline")
        return run(args, env=env, timeout=min(timeout, remaining), check=check, limit=limit)

    def dc(*args, **options):
        """Select only this invocation's resolved Compose project."""
        return command(compose + list(args), **options).stdout

    def sql(statement):
        """Use the schema owner only for timeout-bounded, read-only evidence."""
        return json.loads(dc("exec", "-T", "-e", "PGOPTIONS=-c default_transaction_read_only=on -c statement_timeout=10000", "postgres", "psql", "-X", "-qAt", "-v", "ON_ERROR_STOP=1", "-U", "catalog", "-d", "catalog", "-c", statement))

    def interrupt(*_):
        """Enter owned cleanup on SIGINT or SIGTERM."""
        raise KeyboardInterrupt()

    previous = {sig: signal.signal(sig, interrupt) for sig in (signal.SIGINT, signal.SIGTERM)}
    try:
        checkpoint("preflight")
        raw = (tools.INPUTS / "ai-catalog.json").read_bytes()
        entries = tools.inventory(json.loads(raw))
        postgres = next(line.strip().split("image: ", 1)[1] for line in (ROOT / "compose.yaml").read_text().splitlines() if "image: pgvector/pgvector:" in line)
        command(["docker", "image", "inspect", postgres])
        report["phases"]["preflight"] = "passed"
        checkpoint("setup")
        resolved = dc("config", "--format", "json")
        (directory / "compose.json").write_text(resolved)
        report["stopCommand"] = ["docker", "compose", "--env-file", "/dev/null", "-f", str(directory / "compose.json"), "-p", project, "down", "--volumes", "--remove-orphans"]
        report["removeImages"] = ["docker", "image", "rm", image, fixture_image]
        save()
        built = command(compose + ["build", "migrate", "federation-fixture"], timeout=900, check=False)
        (directory / "build.log").write_text(built.stdout + built.stderr)
        assert built.code == 0, "owned application/fixture build failed"
        for path in (fixture, private / "untrusted"):
            path.mkdir(exist_ok=True, mode=0o755)
            command(["docker", "run", "--rm", "--pull=never", "--network=none", "--name", project + "-generate", "--label", "com.docker.compose.project=" + project,
                     "--user", f"{os.getuid()}:{os.getgid()}", "--mount", f"type=bind,src={path},dst=/fixture", fixture_image,
                     "generate", "--directory", "/fixture", "--subnet", subnet], timeout=45)
        shutil.copyfile(tools.INPUTS / "ai-catalog.json", fixture / "ai-catalog.json")
        (fixture / "ai-catalog.json").chmod(0o444)
        shutil.copyfile(private / "untrusted/trust/functional-ca.pem", fixture / "trust/untrusted.pem")
        policy = json.loads((fixture / "policy.json").read_text())
        profile = policy["privateProfiles"][0]
        profile["addressRanges"] = [str(ipaddress.ip_network(subnet))]
        policy["privateProfiles"].append({**profile, "name": "agentfinder-untrusted", "trustBundleFile": "/etc/open-agentic-catalog/federation/trust/untrusted.pem"})
        (fixture / "policy.json").chmod(0o600)
        (fixture / "policy.json").write_text(json.dumps(policy))
        (fixture / "policy.json").chmod(0o444)
        for role in ("api", "worker"):
            dc("run", "--rm", "--no-deps", "--entrypoint", "/usr/local/bin/catalogctl", role, "config", "validate", "--config", "/config/config.json", "--role", role)
        dc("up", "-d", "--wait", "--wait-timeout", "180", "api", "worker", "federation-fixture", timeout=240)
        ids = {service: dc("ps", "-q", service).strip() for service in ("api", "worker", "postgres", "federation-fixture")}
        assert all(ids.values())
        for service, cid in ids.items():
            inspected = json.loads(command(["docker", "inspect", cid]).stdout)[0]
            report["images"][service] = inspected["Image"]
            assert not inspected["HostConfig"].get("PortBindings"), "fixture published a port"
            if service != "federation-fixture":
                assert len(inspected["NetworkSettings"]["Networks"]) == 1
        report["phases"]["setup"] = "passed"

        checkpoint("confinement")
        network = json.loads(command(["docker", "network", "inspect", project + "_default"]).stdout)[0]
        assert network["Internal"] is True
        probe_names = []
        report["probeNames"] = probe_names
        probe_code = """import errno,json,socket,sys
s=socket.socket();s.settimeout(2)
try:
 code=s.connect_ex((sys.argv[1],int(sys.argv[2])))
 print(json.dumps({'code':code,'connected':code==0,'denied':code in (errno.ENETUNREACH,errno.EHOSTUNREACH,errno.ETIMEDOUT,errno.EAGAIN)}))
finally:s.close()
"""
        for service in ("api", "worker"):
            inspected = json.loads(command(["docker", "inspect", ids[service]]).stdout)[0]
            assert inspected["HostConfig"]["Dns"] == ["127.0.0.1"]
            own_ip = next(iter(inspected["NetworkSettings"]["Networks"].values()))["IPAddress"]
            fixture_ip = next(iter(json.loads(command(["docker", "inspect", ids["api"]]).stdout)[0]["NetworkSettings"]["Networks"].values()))["IPAddress"]
            assert ipaddress.ip_address(own_ip) in ipaddress.ip_network(subnet)
            for host, probe_port, denied in (("1.1.1.1", "443", True), (fixture_ip, "8443", False)):
                name = project + "-probe-" + str(len(probe_names))
                probe_names.append(name)
                save()
                observed = json.loads(command(["docker", "run", "--rm", "--pull=never", "--name", name, "--label", "com.docker.compose.project=" + project,
                    "--network", "container:" + ids[service], "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", command_image,
                    "python", "-I", "-B", "-c", probe_code, host, probe_port], timeout=10).stdout)
                assert observed["denied"] is denied and observed["connected"] is not denied, "network denial/allowed-route calibration failed"
        report["phases"]["confinement"] = "passed"
        client = project + "-client"
        report["clientName"] = client
        client_dir = private / "client"
        client_dir.mkdir(mode=0o700)
        (client_dir / "report.json").write_text(json.dumps(report))
        save()
        result = command(["docker", "run", "--rm", "--pull=never", "--name", client, "--label", "com.docker.compose.project=" + project,
            "--network", "container:" + ids["api"], "--user", f"{os.getuid()}:{os.getgid()}", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges",
            "--pids-limit", "128", "--memory", "512m", "--cpus", "1", "--env", "PYTHONDONTWRITEBYTECODE=1",
            "--mount", f"type=bind,src={tools.HERE},dst=/workspace/test/interoperability,readonly",
            "--mount", f"type=bind,src={fixture / 'trust/functional-ca.pem'},dst=/ca.pem,readonly",
            "--mount", f"type=bind,src={client_dir},dst=/result",
            command_image, "python", "-B", "/workspace/test/interoperability/agentfinder_runner.py", "client"], timeout=600, check=False)
        (directory / "client.log").write_text(result.stdout + result.stderr)
        child = json.loads((client_dir / "report.json").read_text())
        contract.merge_client(report, child)
        assert result.code == 0 and not child.get("error"), "HTTP qualification failed: " + child.get("error", result.stderr[-2000:])
        checkpoint("final-checks")
        # Check all retained phases through read-only owner SQL after HTTP execution.
        source_id, inspection_id = report["sourceId"], report["inspection"]["id"]
        for phase in ("retention", "repeat", "recovery"):
            evidence = report[phase]
            bindings, rows = persistence(sql, source_id, inspection_id, evidence["job"], evidence["snapshot"], report["recovery"]["snapshot"]["id"])
            contract.check_sql_members(rows, entries, report["members"], source_id, evidence["snapshot"])
            evidence["sql"] = bindings
        failed = report["outage"]
        failure_id, failure_job, final_id = map(contract.uuid_literal, (failed["snapshot"]["id"], failed["job"]["id"], report["recovery"]["snapshot"]["id"]))
        failure_binding = sql(f"""SELECT json_build_object(
 'snapshot_bound',n.state='failed' AND NOT n.enumeration_complete AND NOT n.withdrawal_authority AND n.member_count=0 AND n.source_revision=s.revision AND s.last_complete_snapshot_id='{final_id}',
 'job_bound',j.kind='source.sync' AND j.state='succeeded' AND j.payload->>'sourceId'=s.id::text AND (j.payload->>'sourceRevision')::bigint=s.revision AND j.attempt>{int(failed['job']['attempt'])},
 'audit_bound',(SELECT count(*)=1 FROM audit_events a WHERE a.tenant_id=s.tenant_id AND a.target_id=n.id::text AND a.action='source.sync.failed' AND a.details->'facts'->>'jobId'=j.id::text AND (a.details->'facts'->>'jobAttempt')::int={int(failed['job']['attempt'])}),
 'no_occurrences',NOT EXISTS(SELECT 1 FROM observation_occurrences c WHERE c.tenant_id=s.tenant_id AND c.source_id=s.id AND c.snapshot_id=n.id))
 FROM federation_sources s JOIN source_snapshots n ON n.tenant_id=s.tenant_id AND n.source_id=s.id AND n.id='{failure_id}'
 JOIN jobs j ON j.tenant_id=s.tenant_id AND j.id='{failure_job}' WHERE s.tenant_id='{contract.TENANT}' AND s.id='{contract.uuid_literal(source_id)}'""")
        assert set(failure_binding) == {"snapshot_bound", "job_bound", "audit_bound", "no_occurrences"} and all(v is True for v in failure_binding.values()), "failed retrieval lost PostgreSQL attribution"
        failed["sql"] = failure_binding
        report.pop("members")
        assert source_fingerprint() == before, "source changed during qualification"
        tools.installed()
        report["phases"]["final-checks"] = "passed"
    except BaseException as error:
        if "client_dir" in locals() and (client_dir / "report.json").exists():
            try:
                contract.merge_client(report, json.loads((client_dir / "report.json").read_text()))
            except (OSError, ValueError, AssertionError, KeyError, TypeError):
                report["clientReportInvalid"] = True
        report["error"] = "interrupted" if isinstance(error, KeyboardInterrupt) else (str(error)[-4000:] or type(error).__name__)
        if report.get("phase") in report["phases"]:
            report["phases"][report["phase"]] = "failed"
    finally:
        for sig in previous:
            signal.signal(sig, signal.SIG_IGN)
        # The cleanup budget is separate from the exhausted execution budget.
        cleanup_owned(compose, env, project, (image, fixture_image), private, report)
        for sig, handler in previous.items():
            signal.signal(sig, handler)
        report["status"] = "passed" if contract.finish(report) else "failed"
        save()
        print("Agent Finder report: " + str(report_path.relative_to(ROOT)), flush=True)
    if report["status"] != "passed":
        raise RuntimeError("Agent Finder qualification failed: " + report.get("error", report.get("cleanupError", "incomplete phases")))
    return report


def client_main():
    """Use standard Python inside the owned API namespace, without published ports."""
    path = Path("/result/report.json")
    report = json.loads(path.read_text())
    def checkpoint(phase):
        """Record the active phase without prematurely marking it successful."""
        report["phase"] = phase
        path.write_text(json.dumps(report))
        print("Agent Finder HTTP: " + phase, flush=True)
    try:
        api = Catalog("http://127.0.0.1:8080", "https://127.0.0.1:8443", "/ca.pem")
        raw = (tools.INPUTS / "ai-catalog.json").read_bytes()
        fetched, _ = api.http.request("fixture", "/ai-catalog.json")
        assert fetched == raw, "served publication differs from pinned bytes"
        lifecycle(api, tools.inventory(json.loads(raw)), raw, report, checkpoint)
        checkpoint("final-checks")
        traffic, _ = api.http.document("fixture", "/state")
        report["traffic"] = traffic
        assert traffic["unexpectedRequests"] == traffic["credentialedRequests"] == 0
        assert 0 < traffic["requests"] <= 128 and 0 <= traffic["discoveryProbes"] <= 16 and traffic["statuses"].get("503", 0) > 0
        report["traffic"] = traffic
    except BaseException as error:
        report["error"] = str(error)[-4000:] or type(error).__name__
        if "api" in locals():
            report["unexpectedResponses"] = api.unexpected
        raise
    finally:
        path.write_text(json.dumps(report))


def require_assertions():
    """Never qualify a campaign with Python assertions disabled."""
    if sys.flags.optimize:
        raise RuntimeError("Agent Finder qualification requires Python assertions; unset PYTHONOPTIMIZE")


def main():
    """Keep preparation, real execution and the private HTTP phase explicit."""
    require_assertions()
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["install", "check", "client"])
    args = parser.parse_args()
    if args.command == "client":
        return client_main()
    if args.command == "install":
        tools.install()
    else:
        run_qualification()


if __name__ == "__main__":
    main()
