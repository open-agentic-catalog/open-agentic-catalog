"""Run the prepared native peer and Catalog on owned, offline Compose networks."""

import json
import os
from importlib.metadata import version
from pathlib import Path
import secrets
import shutil
import signal
import sys
import tempfile

from jsonschema import Draft202012Validator, FormatChecker
from referencing import Registry, Resource
from referencing.jsonschema import DRAFT202012
import yaml

from agentfinder_contract import TENANT, uuid_literal
from agentfinder_runner import cleanup_owned
from agentfinder_tools import runner_image
import agentic_registry_tools as tools
from common import run
from runner import fingerprint, require_internal_networks

SCHEMAS = tools.ROOT / "schemas/upstream/ard/ard-0.91-aa3e598/spec/schemas"
PHASES = ("initial", "pending", "approved", "recommended", "disabled", "restored", "archived", "recovered", "withdraw-mcp")


def projection_validators():
    spec = yaml.safe_load((SCHEMAS / "ard.openapi.yaml").read_text())
    entry = json.loads((SCHEMAS / "ard-entry.schema.json").read_text())
    base = entry["$id"].rsplit("/", 1)[0] + "/ard.openapi.yaml"
    registry = Registry().with_resources([(base, Resource(spec, DRAFT202012)), (entry["$id"], Resource(entry, DRAFT202012))])
    return {key: Draft202012Validator({"$ref": base + "#/components/schemas/" + schema}, registry=registry, format_checker=FormatChecker())
            for key, schema in (("search", "SearchResponse"), ("list", "ListResponse"))}


def conformance(directory):
    validators = projection_validators()
    outcomes = {}
    for phase in PHASES:
        cmd = [sys.executable, "-B", "test/packaging/ard_conformance.py", "manifest", str(directory / (phase + "-manifest.json"))]
        result = run(cmd, timeout=90)
        (directory / (phase + "-validation.log")).write_text(result.stdout)
        projected = json.loads((directory / (phase + "-registry.json")).read_text())
        for response in (projected["skill"], projected["mcp"], *projected["controls"].values()):
            validators["search"].validate(response)
        for page in projected["list"]:
            validators["list"].validate(page)
        outcomes[phase] = {"manifest": result.stdout.strip().splitlines()[-1], "listPages": len(projected["list"]),
                           "skillMatches": len(projected["skill"]["results"]), "mcpMatches": len(projected["mcp"]["results"])}
    return outcomes


def verify_storage(dc, result, directory):
    assert set(result["history"]) == set(result["snapshots"]) == set(PHASES) - {"disabled"}, "incomplete retained history"
    assert set(result["live"]) == set(PHASES), "incomplete live Search evidence"
    source = uuid_literal(result["source"])
    for phase, proof in result["snapshots"].items():
        snapshot = uuid_literal(proof["id"])
        job = uuid_literal(proof["job"])
        entries = json.loads((directory / (phase + "-manifest.json")).read_bytes())["entries"]
        sql = f"""SELECT json_build_object('entries', (SELECT json_agg(json_build_array(c.entry_location, o.identifier, o.id,
            c.exact_entry_digest, c.evidence_complete AND c.source_revision=s.source_revision AND c.retrieval_digest=s.body_digest)
            ORDER BY c.entry_location)
        FROM observation_occurrences c JOIN observations o ON (c.tenant_id,c.source_id,c.observation_id)=(o.tenant_id,o.source_id,o.id)
        JOIN source_snapshots s ON (c.tenant_id,c.source_id,c.snapshot_id)=(s.tenant_id,s.source_id,s.id)
        WHERE s.tenant_id='{TENANT}' AND s.source_id='{source}' AND s.id='{snapshot}'),
        'policy',(SELECT details->>'policyDigest' FROM source_snapshots
            WHERE tenant_id='{TENANT}' AND source_id='{source}' AND id='{snapshot}'),
        'job',EXISTS(SELECT 1 FROM jobs WHERE id='{job}' AND tenant_id='{TENANT}' AND kind='source.sync' AND state='succeeded'
           AND payload->>'sourceId'='{source}' AND result->>'id'='{snapshot}'),
        'noAdoption',NOT EXISTS(SELECT 1 FROM resources) AND NOT EXISTS(SELECT 1 FROM listing_releases)
            AND NOT EXISTS(SELECT 1 FROM publication_snapshots))"""
        data = json.loads(dc("exec", "-T", "-e", "PGOPTIONS=-c default_transaction_read_only=on -c statement_timeout=10000",
                             "postgres", "psql", "-XqAt", "-v", "ON_ERROR_STOP=1", "-U", "catalog", "-d", "catalog", "-c", sql))
        history = result["history"][phase]
        expected = sorted([f"/entries/{i}", e["identifier"], history[e["identifier"]]["id"], history[e["identifier"]]["digest"], True]
                          for i, e in enumerate(entries))
        assert data == {"entries": expected, "policy": result["connection"]["policyDigest"], "job": True, "noAdoption": True}, (
            "stored Job, policy or occurrence mismatch: " + phase)


def qualify(directory):
    private = directory / "private"
    fixture = private / "fixture"
    fixture.mkdir(parents=True)
    private.chmod(0o700)
    project = "oac-arr-" + secrets.token_hex(6)
    report = {"project": project, "status": "running", "cleanup": "pending", "phase": "inputs"}
    env = {k: os.environ[k] for k in ("PATH", "HOME", "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG", "XDG_RUNTIME_DIR") if k in os.environ}
    env.update(CATALOG_ENV_FILE="/dev/null", ARR_PASSWORD=secrets.token_hex(24), ARR_SECRET=secrets.token_hex(32),
               CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY=str(fixture),
               CATALOG_COMPOSE_HTTP_PORT="0", CATALOG_COMPOSE_POSTGRES_PORT="0", CATALOG_COMPOSE_FIXTURE_PORT="0",
               CATALOG_COMPOSE_SUBNET=f"172.31.{secrets.randbelow(256)}.0/28",
               CATALOG_COMPOSE_FEDERATION_POLICY_FILE=str(fixture / "policy.json"), CATALOG_COMPOSE_FEDERATION_TRUST_DIRECTORY=str(fixture / "trust"))
    compose = ["docker", "compose", "--env-file", "/dev/null", "-p", project, "-f", "compose.yaml", "-f", "test/functional/compose.yaml",
               "-f", "test/interoperability/agentfinder/compose.yaml", "-f", str(tools.OVERLAY)]

    def command(args, timeout=250):
        return run(args, env=env, timeout=timeout, limit=8 << 20).stdout

    def dc(*args):
        return command(compose + list(args))

    def phase(name):
        report["phase"] = name
        (directory / "report.json").write_text(json.dumps(report, indent=2) + "\n")
        print(name, flush=True)

    allocated = False
    try:
        report["prepared"] = tools.installed()
        prepared_images = report["prepared"]["images"]
        catalog_ref = os.environ.get("ARR_CATALOG_IMAGE", "open-agentic-catalog:dev")
        env["IMAGE"] = command(["docker", "image", "inspect", "--format", "{{.Id}}", catalog_ref]).strip()
        report["catalogReference"] = catalog_ref
        env["ARR_IMAGE"] = prepared_images[tools.PEER]
        env["CATALOG_FUNCTIONAL_FIXTURE_IMAGE"] = prepared_images[tools.FIXTURE]
        files = [tools.OVERLAY, *sorted(tools.HERE.glob("*.py")), tools.ROOT / "compose.yaml", tools.ROOT / "test/functional/compose.yaml",
                 tools.HERE / "agentfinder/compose.yaml", tools.ROOT / "test/packaging/ard_conformance.py",
                 SCHEMAS / "ard.openapi.yaml", SCHEMAS / "ard-entry.schema.json"]
        report["harnessDigest"] = fingerprint(files)
        report["validator"] = {"profile": SCHEMAS.parent.parent.name, "jsonschema": version("jsonschema")}
        report["commit"] = command(["git", "rev-parse", "HEAD"]).strip()
        phase("setup")
        services = ("api", "worker", "postgres", "arr-api", "arr-db", "arr-redis", "arr-clickhouse")
        config = json.loads(dc("config", "--format", "json"))
        require_internal_networks(config)
        for service in ("arr-init", "arr-api"):
            assert config["services"][service]["image"] == prepared_images[tools.PEER], f"{service} must use the prepared peer image ID"
        user = f"{os.getuid()}:{os.getgid()}"
        allocated = True
        command(["docker", "run", "--rm", "--pull=never", "--network=none", "--name", project + "-generate",
                 "--label", "com.docker.compose.project=" + project, "--user", user, "-v", f"{fixture}:/fixture", prepared_images[tools.FIXTURE],
                 "generate", "--directory", "/fixture", "--subnet", env["CATALOG_COMPOSE_SUBNET"]])
        password = fixture / "peer-password"
        password.write_text(env["ARR_PASSWORD"])
        password.chmod(0o600)
        up = ["up", "-d", "--wait", "--wait-timeout", "185", "--pull", "never", "--no-build"]
        dc(*up, "api", "worker", "arr-api")
        ids = {s: dc("ps", "-q", s).strip() for s in services}
        report["running"] = {}
        for name, cid in ids.items():
            data = json.loads(command(["docker", "inspect", cid]))[0]
            assert not data["HostConfig"].get("PortBindings"), f"{name} published a host port"
            expected = {config["networks"][key]["name"] for key in config["services"][name]["networks"]}
            assert set(data["NetworkSettings"]["Networks"]) == expected, f"{name} network attachments differ from Compose"
            ref = config["services"][name]["image"]
            assert data["Image"] == prepared_images.get(ref, ref), f"{name} image differs from the verified image ID"
            report["running"][name] = {"image": data["Image"], "reference": ref}
        for network in config["networks"].values():
            assert json.loads(command(["docker", "network", "inspect", network["name"]]))[0]["Internal"], f"running network {network['name']} is not internal"
        report["catalogBuild"] = json.loads(dc("exec", "-T", "api", "/usr/local/bin/catalogctl", "version"))
        evidence = directory / "evidence"
        evidence.mkdir()
        report["clientName"] = project + "-client"
        client = ["docker", "run", "--rm", "--pull=never", "--name", report["clientName"], "--label", "com.docker.compose.project=" + project,
                  "--network", "container:" + ids["api"], "--user", user, "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges",
                  "-v", f"{tools.HERE}:/workspace/test/interoperability:ro", "-v", f"{fixture}:/fixture:ro", "-v", f"{evidence}:/result",
                  runner_image(), "python", "-B", "/workspace/test/interoperability/agentic_registry_client.py"]
        phase("exercise")
        command(client + ["exercise"], timeout=700)
        phase("peer outage")
        dc("stop", "arr-api")
        command(client + ["outage"])
        phase("peer recovery")
        dc(*up, "arr-api")
        command(client + ["recovery"])
        phase("restart")
        starts = {s: command(["docker", "inspect", "-f", "{{.State.StartedAt}}", ids[s]]) for s in ("api", "worker")}
        dc("restart", "api", "worker")
        dc(*up, "api", "worker")
        for service, started in starts.items():
            assert command(["docker", "inspect", "-f", "{{.State.StartedAt}}", ids[service]]) != started, f"{service} did not restart"
        command(client + ["restart"])
        phase("supported withdrawal")
        command(client + ["withdraw-mcp"])
        phase("conformance")
        report["native"] = conformance(evidence)
        phase("storage")
        verify_storage(dc, json.loads((evidence / "checkpoint.json").read_text()), evidence)
        assert report["harnessDigest"] == fingerprint(files), "inputs changed during campaign"
        report["status"] = "passed"
    except KeyboardInterrupt:
        report["status"] = "interrupted"
    except Exception as exc:
        report["status"] = "failed"
        report["error"] = str(exc)
        print(repr(exc), file=sys.stderr)
    finally:
        if allocated:
            cleanup_owned(compose, env, project, (), private, report)
        else:
            shutil.rmtree(private)
            report["cleanup"] = "complete"
        (directory / "report.json").write_text(json.dumps(report, indent=2) + "\n")
    if report["status"] == "interrupted" and report["cleanup"] == "complete":
        raise KeyboardInterrupt
    return report["status"] == "passed" and report["cleanup"] == "complete"


if __name__ == "__main__":
    if not __debug__:
        raise RuntimeError("campaign requires Python assertions")
    def interrupted(*_):
        raise KeyboardInterrupt()
    signal.signal(signal.SIGTERM, interrupted)
    tools.OUTPUT.mkdir(parents=True, exist_ok=True)
    directory = Path(tempfile.mkdtemp(prefix="run-", dir=tools.OUTPUT))
    print(f"Agentic Resource Registry evidence: {directory.relative_to(tools.ROOT)}", flush=True)
    try:
        sys.exit(0 if qualify(directory) else 1)
    except KeyboardInterrupt:
        sys.exit(130)
