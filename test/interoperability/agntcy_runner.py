"""Qualify one A2A artifact and ARD limits against an explicitly selected local lab."""

import contextlib
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.parse

from agntcy_contract import DISPOSITIONS, PROFILE, check_cases, check_checker, check_search_errors, document_digest, raw_entry_evidence
from agntcy_lab import ASSETS, Lab, LocalHTTP, UnexpectedStatus, environment, file_digest, read_json
from client_contract import redact
from common import run
from peer_contract import A2A, CURRENT, TENANT
from peer_wire import checker_evidence
import runner as shared

CHECKER = shared.ROOT / "test/packaging/ard_conformance.py"
ARD = shared.ROOT / "schemas/upstream/ard/ard-0.91-aa3e598"
INPUTS = (shared.HERE, shared.ROOT / "Makefile", CHECKER, shared.ROOT / "pyproject.toml", shared.ROOT / "uv.lock", ARD)


def wait_for(callback, label, timeout=30):
    deadline = time.monotonic() + timeout
    while True:
        result = callback()
        if result is not None:
            return result
        if time.monotonic() >= deadline:
            raise RuntimeError(label + " deadline exceeded")
        time.sleep(0.1)


def configuration(private, lab, port):
    """The fresh memory Catalog can fetch only the lab TLS origin for this tenant."""
    origin = lab.lock["tls_origin"]
    address = urllib.parse.urlsplit(origin).hostname
    policy = dict(schemaVersion=1, policyId="owned-agntcy", revision=1, public={"mode": "disabled"},
                  privateProfiles=[dict(name="agntcy-owned", origins=[origin], addressRanges=[address + "/32"],
                      assignedTenantIds=[TENANT], transportRef="direct", trustBundleFile=str(lab.certificate))])
    (private / "policy.json").write_text(json.dumps(policy))
    config = read_json(shared.ROOT / "config/local.json")
    config["http"].update(address=f"127.0.0.1:{port}", publicBaseUrl=f"http://127.0.0.1:{port}")
    config["worker"]["operationsAddress"] = "127.0.0.1:0"
    config["federation"]["policyFile"] = str(private / "policy.json")
    path = private / "config.json"
    path.write_text(json.dumps(config))
    path.chmod(0o600)
    return path


def native_artifact(lab):
    """Reuse only the exact content-addressed seed; never delete or overwrite lab data."""
    seed = read_json(ASSETS / "record.json")
    card = read_json(ASSETS / "card.json")
    assert seed["modules"][0]["data"]["card_data"] == card, "fixture card/wrapper drift"
    cid = lab.lock["cid"]
    before = lab.native("pull", cid, "--output", "json", check=False)
    if before.code == 0:
        assert json.loads(before.stdout) == seed, "existing CID contains unexpected record content"
    elif "NotFound" not in before.stderr:
        raise RuntimeError("native seed read failed for a reason other than NotFound")
    pushed = lab.native("push", str(ASSETS / "record.json"), "--output", "raw")
    assert pushed.stdout.strip() == cid, "native push changed deterministic CID"
    retained = json.loads(lab.native("pull", cid, "--output", "json").stdout)
    assert retained == seed, "native storage changed record content"
    exported = json.loads(lab.native("export", cid, "--format", "a2a").stdout)
    assert exported == card, "native export changed A2A content"
    return exported, before.code == 0


def artifact(api, lab, card):
    namespace, _ = api.document("catalog", "/api/v1/namespaces", {"slug": "default", "displayName": "Interoperability"}, expected=201)
    resource, _ = api.document("catalog", "/api/v1/resources", dict(namespaceId=namespace["id"], kind="agent",
        canonicalIdentifier="urn:air:catalog.example.com:default:agntcy-a2a"), expected=201)
    version, _ = api.document("catalog", "/api/v1/resources/" + resource["id"] + "/versions",
        dict(version="1.0.0", representations=[dict(role="primary", mediaType=A2A, data=card)]), expected=201)
    assert version["evidence"]["state"] == "complete", "import validation incomplete"
    revision = version["revision"]
    path = "/api/v1/version-revisions/" + revision["id"]
    validated, _ = api.document("catalog", path + ":validate", {}, expected=201)
    assert validated["evidence"]["state"] == "complete", "revalidation incomplete"
    assert validated["revision"]["contentSetDigest"] == revision["contentSetDigest"], "validation changed content digest"
    envelope, _ = api.document("catalog", path)
    retained = envelope["revision"]
    assert envelope["evidence"]["id"] == validated["evidence"]["id"] and envelope["evidence"]["state"] == "complete", "readback lost validation evidence"
    assert len(retained["representations"]) == 1, "unexpected representation inventory"
    representation = retained["representations"][0]
    assert representation["profileId"] == PROFILE and representation["validationState"] == "valid", "incorrect A2A validation profile/state"
    assert representation["data"] == card and representation["role"] == "primary" and representation["mediaType"] == A2A, "retained card differs"
    assert retained["contentSetDigest"] == revision["contentSetDigest"], "readback changed content digest"
    return dict(cid=lab.lock["cid"], profile=PROFILE, validation="complete", retained=True, native_preserved=True,
                input_sha256=document_digest(read_json(ASSETS / "card.json")), export_sha256=document_digest(card),
                retained_sha256=document_digest(representation["data"]), resource_id=resource["id"], revision_id=revision["id"])


def discovery(api, lab, network_profile="agntcy-owned"):
    cid = lab.lock["cid"]

    def indexed():
        try:
            return api.document("peer", "/v1/agents/" + cid)[0]
        except UnexpectedStatus as error:
            if error.status == 404:
                return None
            raise

    entry = wait_for(indexed, "native projection")
    assert entry["identifier"] == "urn:ai:org.agntcy:cid:" + cid, "selected native record absent"
    manifest, _ = api.request("peer", "/.well-known/ai-catalog.json")
    direct, _ = api.request("native", "/.well-known/ai-catalog.json")
    assert manifest == direct and json.loads(manifest)["entries"] == [], "gateway changed bytes or manifest is no longer empty"
    api.request("peer", "/.well-known/ard.json", expected=404)
    origin = lab.lock["tls_origin"]
    inspection, _ = api.document("catalog", "/api/v1/federation/source-inspections",
        dict(url=origin + "/", networkProfile=network_profile), expected=201)
    # Generic JSON at the predecessor alias selects ARD; the path alone does
    # not explicitly declare the predecessor AI Catalog media type.
    expected = dict(sourceType="ard_manifest", profileId=CURRENT, entryCount=0)
    actual = {key: inspection.get(key) for key in expected}
    assert actual == expected, "unexpected resolved interface: " + json.dumps(actual)
    assert inspection["resolvedUrl"] == origin + "/.well-known/ai-catalog.json", "unexpected discovery endpoint"
    source, headers = api.document("catalog", "/api/v1/federation/sources",
        dict(inspectionId=inspection["id"], displayName="Owned AGNTCY qualification", retrievalMode="indexed",
             updateMode="manual_review", networkProfile=network_profile, scope={"publishers": ["org.agntcy"]},
             reason="Qualify empty predecessor discovery"), expected=201)
    source_path = "/api/v1/federation/sources/" + source["id"]
    receipt, _ = api.document("catalog", source_path + ":sync", {}, expected=202, etag=headers["etag"])
    job_id = receipt["job"]["id"]

    def complete():
        job, _ = api.document("catalog", "/api/v1/jobs/" + job_id)
        assert job["state"] not in ("failed", "cancelled"), "synchronization Job failed"
        return job if job["state"] == "succeeded" else None

    job = wait_for(complete, "worker synchronization")
    snapshot, _ = api.document("catalog", "/api/v1/federation/snapshots/" + job["result"]["snapshotId"])
    retained, _ = api.document("catalog", source_path)
    observations, _ = api.document("catalog", "/api/v1/observations?" + urllib.parse.urlencode(dict(sourceId=source["id"], freshness="current")))
    assert snapshot["state"] == "complete" and snapshot["memberCount"] == 0 and observations["items"] == [], "unexpected nonempty/incomplete synchronization"
    assert retained["lastCompleteSnapshotId"] == snapshot["id"] and snapshot["sourceId"] == source["id"], "snapshot/source binding differs"
    return dict(native_record=cid, manifest_entries=0, profile=inspection["profileId"], source_type=inspection["sourceType"],
                state=snapshot["state"], members=snapshot["memberCount"], observations=len(observations["items"]),
                current_pointer=True, bytes_preserved=True, source_id=source["id"], job_id=job_id, snapshot_id=snapshot["id"],
                inspection_id=inspection["id"], manifest_sha256="sha256:" + hashlib.sha256(manifest).hexdigest())


def search(api, lab, network_profile="agntcy-owned"):
    nested, _ = api.document("peer", "/v1/search", {"query": {"text": "test"}, "pageSize": 1, "federation": "none"}, expected=400)
    scalar, _ = api.document("peer", "/v1/search", {"query": "test", "pageSize": 1}, expected=503)
    check_search_errors(nested, scalar)
    problem, _ = api.document("catalog", "/api/v1/federation/source-inspections",
        dict(url=lab.lock["tls_origin"] + "/v1/search", networkProfile=network_profile), expected=400)
    assert problem["code"] == "INVALID_ARGUMENT", "explicit Search unexpectedly fell back or failed a different guard"
    return dict(nested_status=400, scalar_status=503, catalog_status=400, catalog_code=problem["code"],
                nested_error=nested, scalar_error=scalar)


def conformance(api, lab, directory, checker_origin=None):
    raw, _ = api.request("peer", "/.well-known/ai-catalog.json")
    manifest = directory / "manifest.json"
    manifest.write_bytes(raw)
    env = dict(lab.env, SSL_CERT_FILE=str(lab.certificate))
    modes = {}
    origin = checker_origin or lab.lock["tls_origin"]
    for mode, target in (("manifest", str(manifest)), ("publisher", origin), ("registry", origin + "/v1")):
        result = run([sys.executable, "-B", str(CHECKER), mode, target], env=env, timeout=75, check=False, limit=256 * 1024)
        evidence = checker_evidence(result)
        (directory / ("checker-" + mode + ".json")).write_text(json.dumps(evidence, indent=2))
        modes[mode] = evidence
    entry, _ = api.document("peer", "/v1/agents/" + lab.lock["cid"])
    (directory / "raw-entry.json").write_text(json.dumps(entry, indent=2))
    schema = read_json(ARD / "spec/schemas/ard-entry.schema.json")
    expected = lab.lock.get("expected", {})
    raw_evidence = raw_entry_evidence(entry, schema, lab.lock["cid"], expected_errors=expected.get("entry_errors"))
    for mode, evidence in modes.items():
        check_checker(mode, evidence, expected.get("checker_errors"))
    return dict(modes=modes, raw_entry=raw_evidence)


def stop_catalog(process):
    if process is None:
        return
    if process.poll() is not None:
        raise RuntimeError("owned Catalog exited unexpectedly before cleanup")
    os.killpg(process.pid, signal.SIGTERM)
    try:
        process.wait(timeout=25)
    except subprocess.TimeoutExpired:
        os.killpg(process.pid, signal.SIGKILL)
        process.wait(timeout=5)
        raise RuntimeError("owned Catalog required forced termination") from None
    if process.returncode != 0:
        raise RuntimeError("owned Catalog shutdown failed")


@contextlib.contextmanager
def register_process():
    """Defer catchable signals until the child reference and ownership record exist."""
    pending = []
    previous = {number: signal.signal(number, lambda number, _frame: pending.append(number))
                for number in (signal.SIGINT, signal.SIGTERM, signal.SIGALRM)}
    try:
        yield
    finally:
        for number, handler in previous.items():
            signal.signal(number, handler)
        if pending and sys.exc_info()[0] is None:
            raise KeyboardInterrupt("interrupted while registering owned Catalog")


def check(value):
    """Retain bounded outcomes and clean owned state before final report validation."""
    shared.OUTPUT.mkdir(parents=True, exist_ok=True)
    directory = Path(tempfile.mkdtemp(prefix="agntcy-", dir=shared.OUTPUT))
    private = directory / "private"
    process = None
    report = dict(status="setup-failed", started_utc=time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                  cases=[dict(name=name, status="unrun", disposition=disposition, evidence={}) for name, disposition in DISPOSITIONS.items()],
                  owned={"private_directory": str(private), "catalog_pid": None}, cleanup={"status": "pending"})

    def checkpoint(phase):
        (directory / "phase.json").write_text(json.dumps(dict(phase=phase, owned=report["owned"])))
        print("AGNTCY phase: " + phase, flush=True)

    def record(name, callback):
        case = next(item for item in report["cases"] if item["name"] == name)
        checkpoint(name)
        try:
            case.update(status="pass", evidence=callback())
        except (AssertionError, RuntimeError, OSError, ValueError, KeyError, TypeError) as error:
            case.update(status="fail", evidence={"error": redact(str(error))[:2000]})

    try:
        checkpoint("setup")
        private.mkdir(mode=0o700)
        lab = Lab(value, private)
        report["lab"] = lab.preflight()
        run([sys.executable, "-B", str(CHECKER), "check"], env=lab.env, timeout=30)
        go = shutil.which(os.environ.get("AGNTCY_GO") or "go")
        if not go:
            raise ValueError("install the repository Go toolchain/modules before running this target")
        required = next(line.split()[1] for line in (shared.ROOT / "go.mod").read_text().splitlines() if line.startswith("go "))
        version = run([go, "version"], env=dict(lab.env, GOTOOLCHAIN="local", GOENV="off"), timeout=10).stdout.strip()
        if version != "go version go" + required + " linux/amd64":
            raise ValueError("select the installed Go " + required + " toolchain with GO=/path/to/go")
        report.update(catalog_commit=run(["git", "rev-parse", "HEAD"]).stdout.strip(),
                      catalog_source_sha256=shared.fingerprint(shared.CATALOG_INPUTS), harness_sha256=shared.fingerprint(INPUTS), go=version)
        checkpoint("build")
        shared.build_binaries(private, go, "amd64")
        report["catalog_binary_sha256"] = file_digest(private / "catalogd")
        checkpoint("native-artifact")
        card, reused = native_artifact(lab)
        report["seed"] = dict(cid=lab.lock["cid"], reused=reused, retained_in_selected_lab=True)
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            port = listener.getsockname()[1]
        config = configuration(private, lab, port)
        checkpoint("start-catalog")
        with register_process():
            process = subprocess.Popen([str(private / "catalogd"), "all", "--config", str(config)], env=environment(private),
                                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True)
            report["owned"]["catalog_pid"] = process.pid
            checkpoint("catalog-readiness")
        api = LocalHTTP(dict(catalog=f"http://127.0.0.1:{port}", peer=lab.lock["tls_origin"], native=lab.lock["native_origin"]), lab.certificate)

        def ready():
            if process.poll() is not None:
                raise RuntimeError("owned Catalog failed to start")
            try:
                api.request("catalog", "/readyz")
                return True
            except (OSError, AssertionError):
                return None

        wait_for(ready, "Catalog readiness")
        record("artifact", lambda: artifact(api, lab, card))
        record("discovery", lambda: discovery(api, lab))
        record("search", lambda: search(api, lab))
        record("conformance", lambda: conformance(api, lab, directory))
        if lab.workloads() != report["lab"]["workloads"]:
            raise RuntimeError("retained lab workload identities changed during qualification")
        if (shared.fingerprint(shared.CATALOG_INPUTS) != report["catalog_source_sha256"]
                or shared.fingerprint(INPUTS) != report["harness_sha256"]):
            raise RuntimeError("source inputs changed during qualification")
        report["status"] = "completed"
    except (Exception, KeyboardInterrupt) as error:
        report["error"] = redact(str(error) or "interrupted")[:2000]
    finally:
        signal.setitimer(signal.ITIMER_REAL, 0)
        errors = []
        try:
            checkpoint("cleanup")
        except OSError:
            errors.append("cleanup checkpoint failed")
        try:
            stop_catalog(process)
        except (OSError, RuntimeError) as error:
            errors.append(str(error))
        try:
            if private.exists():
                shutil.rmtree(private)
        except OSError:
            errors.append("owned private directory removal failed")
        report["cleanup"] = dict(status="fail" if errors else "pass", errors=errors, private_removed=not private.exists())
    try:
        report["passed"] = check_cases(report["cases"], read_json(ASSETS / "lock.json")["cid"])
        if report["status"] != "completed" or report["cleanup"]["status"] != "pass":
            raise ValueError("setup, execution or cleanup incomplete")
        report["status"] = "pass"
    except (ValueError, KeyError, TypeError) as error:
        report["status"] = "fail"
        report.setdefault("error", str(error))
    (directory / "result.json").write_text(json.dumps(report, indent=2))
    print("AGNTCY report: " + str(directory / "result.json"), flush=True)
    return 0 if report["status"] == "pass" else 1


def interrupted(_signum, _frame):
    raise KeyboardInterrupt("qualification interrupted or overall deadline exceeded")


def main():
    if not __debug__:
        print("AGNTCY qualification requires Python assertions", file=sys.stderr)
        return 2
    for signum in (signal.SIGINT, signal.SIGTERM, signal.SIGALRM):
        signal.signal(signum, interrupted)
    signal.setitimer(signal.ITIMER_REAL, 900)
    return check(os.environ.get("AGNTCY_LAB_DIR"))


if __name__ == "__main__":
    raise SystemExit(main())
