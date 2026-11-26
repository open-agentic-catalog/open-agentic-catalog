"""Supervise two independently reported publication replays on private Compose networks."""

import argparse
from importlib.metadata import version
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import signal
import sys
import tempfile

from agentfinder_contract import TENANT, uuid_literal
from agentfinder_runner import cleanup_owned
from agentfinder_tools import runner_image
from common import run
from publication_replay import CapturePrerequisiteError, EXERCISE_PHASES, FEEDS, PATH, PROFILE, inventory, read_capture
from runner import fingerprint, local_daemon

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
OUTPUT = ROOT / "output/interoperability/publication-replay"


def save(directory, value):
    (directory / "report.json").write_text(json.dumps(value, indent=2) + "\n")


def harness_digest():
    return fingerprint((*sorted(HERE.glob("*.py")), HERE / "agentfinder/compose.yaml", HERE / "Containerfile",
                        ROOT / "compose.yaml", ROOT / "test/functional/compose.yaml", ROOT / "test/packaging/ard_conformance.py"))


def validate_input(provider, fixture, directory, report):
    feed = FEEDS[provider]
    for index, pin in enumerate(feed["captures"]):
        raw = read_capture(os.environ.get(pin[0]), pin)
        path = fixture / f"capture-{index}.json"
        path.write_bytes(raw)
        entries = inventory(raw, feed)
        result = run([sys.executable, "-B", "test/packaging/ard_conformance.py", "manifest", str(path)], timeout=90, check=False)
        diagnostics = directory / f"capture-{index}-validation.log"
        diagnostics.write_text(result.stdout + result.stderr)
        if result.code:
            raise RuntimeError(f"Pinned ARD manifest validation failed; see {diagnostics.name}")
        warnings = re.search(r"Validated with 0 critical specification errors and (\d+) warnings", result.stdout)
        assert warnings and int(warnings[1]) == feed["warnings"], "unexpected pinned ARD CLI disposition"
        report["captures"].append({"sha256": pin[2], "bytes": len(raw), "entries": len(entries), "schema": "passed",
                                   "warnings": int(warnings[1]), "validatorLog": diagnostics.name})


def database_checks(dc, evidence, entries):
    source = uuid_literal(evidence["sourceId"])
    snapshot = uuid_literal(evidence["lastSnapshotId"])
    count = evidence["rawVerified"]
    sql = f"""SELECT json_build_object(
    'locations',(SELECT json_object_agg(c.entry_location,o.identifier)
      FROM observations o JOIN observation_occurrences c ON c.tenant_id=o.tenant_id AND c.source_id=o.source_id AND c.observation_id=o.id
      WHERE c.snapshot_id='{snapshot}' AND c.source_id='{source}' AND c.tenant_id='{TENANT}'),
    'membership',(SELECT count(*)={count} AND count(DISTINCT o.id)={count} AND count(DISTINCT c.entry_location)={count}
      AND bool_and(c.evidence_complete AND c.retrieval_digest=n.body_digest AND c.source_revision=n.source_revision
        AND c.entry_location LIKE '/entries/%' AND o.raw_entry_bytes IS NOT NULL)
      FROM observations o JOIN observation_occurrences c ON c.tenant_id=o.tenant_id AND c.source_id=o.source_id AND c.observation_id=o.id
      JOIN source_snapshots n ON n.id=c.snapshot_id AND n.tenant_id=c.tenant_id AND n.source_id=c.source_id
      WHERE n.id='{snapshot}' AND n.source_id='{source}' AND n.tenant_id='{TENANT}'),
    'audits',(SELECT count(DISTINCT o.id)={count} FROM observations o JOIN audit_events a
      ON a.tenant_id=o.tenant_id AND a.target_id=o.id::text AND a.action='evidence.raw_observation.read'
      WHERE o.tenant_id='{TENANT}' AND o.source_id='{source}'),
    'noAdoption',NOT EXISTS(SELECT 1 FROM resources) AND NOT EXISTS(SELECT 1 FROM listing_releases)
       AND NOT EXISTS(SELECT 1 FROM publication_snapshots))"""
    checks = json.loads(dc("exec", "-T", "-e", "PGOPTIONS=-c default_transaction_read_only=on -c statement_timeout=10000",
                           "postgres", "psql", "-XqAt", "-v", "ON_ERROR_STOP=1", "-U", "catalog", "-d", "catalog", "-c", sql))
    locations = checks.pop("locations")
    assert locations == {f"/entries/{i}": entry["identifier"] for i, entry in enumerate(entries)}, "wrong persisted entry location"
    assert set(checks) == {"membership", "audits", "noAdoption"} and all(v is True for v in checks.values()), checks
    return checks


def qualify(provider, directory):
    directory.mkdir()
    private = directory / "private"
    private.mkdir(mode=0o700)
    fixture = private / "fixture"
    fixture.mkdir()
    project = "oac-publication-" + provider + "-" + secrets.token_hex(5)
    catalog = os.environ.get("PUBLICATION_REPLAY_IMAGE", "open-agentic-catalog:dev")
    generator = os.environ.get("PUBLICATION_REPLAY_FIXTURE_IMAGE", "open-agentic-catalog:publication-replay-fixture")
    env = {k: os.environ[k] for k in ("PATH", "HOME", "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG", "XDG_RUNTIME_DIR") if k in os.environ}
    env.update(CATALOG_ENV_FILE="/dev/null", IMAGE=catalog, CATALOG_FUNCTIONAL_FIXTURE_IMAGE=generator,
               CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY=str(fixture), CATALOG_COMPOSE_HTTP_PORT="0",
               CATALOG_COMPOSE_POSTGRES_PORT="0", CATALOG_COMPOSE_FIXTURE_PORT="0",
               CATALOG_COMPOSE_SUBNET=f"172.31.{secrets.randbelow(256)}.0/28",
               CATALOG_COMPOSE_FEDERATION_POLICY_FILE=str(fixture / "policy.json"),
               CATALOG_COMPOSE_FEDERATION_TRUST_DIRECTORY=str(fixture / "trust"))
    compose = ["docker", "compose", "--env-file", "/dev/null", "-p", project, "-f", "compose.yaml",
               "-f", "test/functional/compose.yaml", "-f", "test/interoperability/agentfinder/compose.yaml"]

    def command(args, timeout=180):
        return run(args, env=env, timeout=timeout, limit=8 << 20).stdout

    def dc(*args):
        return command(compose + list(args))

    report = {"provider": provider, "project": project, "status": "incomplete", "cleanup": "unrun", "captures": [],
              "phases": dict.fromkeys(("inputs", "setup", "exercise", "restart", "database"), "unrun")}
    allocated = False
    try:
        def start(phase):
            report["phase"] = phase
            report["phases"][phase] = "running"
            save(directory, report)
            print(f"{provider}: {phase}", flush=True)

        start("inputs")
        report["harnessDigest"] = harness_digest()
        report["publisherUrl"] = "https://" + FEEDS[provider]["publisher"] + PATH
        report["validator"] = {"profile": PROFILE, "jsonschema": version("jsonschema"),
                               "upstreamCLI": "aa3e598bb7752a9175897823234311216acfa864"}
        validate_input(provider, fixture, directory, report)
        report["phases"]["inputs"] = "passed"
        start("setup")
        local_daemon()
        config = json.loads(dc("config", "--format", "json"))
        images = {config["services"][s]["image"] for s in ("api", "worker", "postgres", "migrate", "provision")}
        images.update((generator, runner_image()))
        report["images"] = {image: command(["docker", "image", "inspect", "--format", "{{.Id}}", image]).strip() for image in sorted(images)}
        report["sourceCommit"] = command(["git", "rev-parse", "HEAD"]).strip()
        user = f"{os.getuid()}:{os.getgid()}"
        allocated = True
        command(["docker", "run", "--rm", "--pull=never", "--network=none", "--name", project + "-generate",
                 "--label", "com.docker.compose.project=" + project, "--user", user, "-v", f"{fixture}:/fixture", generator,
                 "generate", "--directory", "/fixture", "--subnet", env["CATALOG_COMPOSE_SUBNET"]])
        up = ["up", "-d", "--wait", "--wait-timeout", "130", "--pull", "never", "--no-build", "api", "worker"]
        dc(*up)
        network = project + "_default"
        assert json.loads(command(["docker", "network", "inspect", network]))[0]["Internal"]
        report["catalogBuild"] = json.loads(dc("exec", "-T", "api", "/usr/local/bin/catalogctl", "version"))
        ids = {s: dc("ps", "-q", s).strip() for s in ("api", "worker", "postgres")}

        def inspect(s):
            return json.loads(command(["docker", "inspect", ids[s]]))[0]

        for service in ids:
            inspect_service = inspect(service)
            assert inspect_service["Image"] == report["images"][config["services"][service]["image"]], "running image differs from preflight"
            assert set(inspect_service["NetworkSettings"]["Networks"]) == {network}
            assert not inspect_service["HostConfig"].get("PortBindings")
            if service != "postgres":
                assert inspect_service["HostConfig"]["Dns"] == ["127.0.0.1"]
        report["phases"]["setup"] = "passed"
        result = private / "result"
        result.mkdir()
        report["clientName"] = project + "-client"
        client = ["docker", "run", "--rm", "--pull=never", "--name", report["clientName"], "--label", "com.docker.compose.project=" + project,
                  "--network", "container:" + ids["api"], "--user", user, "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges",
                  "-v", f"{HERE}:/workspace/test/interoperability:ro", "-v", f"{fixture}:/fixture:ro", "-v", f"{result}:/result",
                  runner_image(), "python", "-B", "/workspace/test/interoperability/publication_replay_client.py", provider]
        start("exercise")
        command(client + ["exercise"], timeout=650)
        assert json.loads((result / "exercise.json").read_text()) == dict.fromkeys(EXERCISE_PHASES, "passed"), "incomplete publication exercise"
        report["evidence"] = json.loads((result / "checkpoint.json").read_text())
        report["phases"]["exercise"] = "passed"
        start("restart")
        starts = {s: inspect(s)["State"]["StartedAt"] for s in ("api", "worker")}
        dc("restart", "api", "worker")
        dc(*up)
        assert all(inspect(s)["State"]["StartedAt"] != value for s, value in starts.items())
        command(client + ["restart"])
        report["phases"]["restart"] = "passed"
        start("database")
        entries = inventory(read_capture(fixture / "capture-0.json", FEEDS[provider]["captures"][0]), FEEDS[provider])
        report["database"] = database_checks(dc, report["evidence"], entries)
        assert report["harnessDigest"] == harness_digest(), "harness changed during qualification"
        report["phases"]["database"] = "passed"
        report["status"] = "passed"
    except KeyboardInterrupt:
        report["status"] = "interrupted"
        raise
    except Exception as error:
        report["status"] = "blocked" if report.get("phase") == "inputs" and isinstance(error, CapturePrerequisiteError) else "failed"
        report["error"] = str(error)
        print(f"{provider}: {error}", file=sys.stderr, flush=True)
    finally:
        phase = report.get("phase")
        if report["phases"].get(phase) == "running":
            report["phases"][phase] = report["status"]
        partial = private / "result/exercise.json"
        try:
            if partial.is_file():
                report["exercisePhases"] = json.loads(partial.read_text())
        except (OSError, ValueError) as error:
            # A killed child can leave a partial progress file; still tear down its project.
            report["progressError"] = str(error)
            if report["status"] == "passed":
                report["status"] = "failed"
        if allocated:
            cleanup_owned(compose, env, project, (), private, report)
        else:
            shutil.rmtree(private)
            report["cleanup"] = "complete"
        save(directory, report)
    return report["status"] == "passed" and report["cleanup"] == "complete"


def main():
    if not __debug__:
        raise RuntimeError("qualification requires Python assertions")
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--provider", choices=tuple(FEEDS))
    args = parser.parse_args()
    OUTPUT.mkdir(parents=True, exist_ok=True)
    directory = Path(tempfile.mkdtemp(prefix="run-", dir=OUTPUT))
    print(f"Publication replay reports: {directory.relative_to(ROOT)}", flush=True)

    def interrupt(*_):
        raise KeyboardInterrupt()

    signal.signal(signal.SIGTERM, interrupt)
    try:
        results = [qualify(name, directory / name) for name in ([args.provider] if args.provider else FEEDS)]
    except KeyboardInterrupt:
        print("Publication replay interrupted", file=sys.stderr)
        return 130
    return 0 if all(results) else 1


if __name__ == "__main__":
    sys.exit(main())
