"""Supervise an unchanged native crawler against disposable PostgreSQL publication."""

import argparse
import copy
import json
import os
from pathlib import Path
import secrets
import shutil
import signal
import subprocess
import sys
import tempfile
import time

from common import run
import runner as shared
from agentfinder_runner import cleanup_owned, owned_resources
import ard_registry_tools as tools
from ard_registry_contract import PHASES, SOURCE, finish, check_readback


def source_fingerprint():
    """Bind application and selected harness bytes throughout the real campaign."""
    return shared.fingerprint((*shared.CATALOG_INPUTS, shared.ROOT / "Containerfile", shared.ROOT / ".dockerignore",
        shared.ROOT / "compose.yaml", shared.ROOT / "config/postgres.json", shared.ROOT / "deployments/postgres/init.sql",
        shared.ROOT / "Makefile", shared.ROOT / "test/functional/fixture", shared.ROOT / "test/functional/Containerfile",
        shared.ROOT / "test/functional/compose.yaml", tools.INPUTS, *sorted(shared.HERE.glob("*.py"))))


def qualification(*, control=None, directory=None):
    """Require all native phases and cleanup; preserve partial failures independently."""
    if sys.flags.optimize or os.getuid() == 0:
        raise RuntimeError("native qualification requires assertions and a non-root owner")
    shared.local_daemon()
    installed = tools.installed()
    python_image = next(line.split()[1] for line in (shared.HERE / "Containerfile").read_text().splitlines() if line.startswith("FROM python:"))
    shared.docker("image", "inspect", python_image)
    before = source_fingerprint()
    tools.OUTPUT.mkdir(parents=True, exist_ok=True)
    directory = Path(tempfile.mkdtemp(prefix="run-", dir=tools.OUTPUT) if directory is None else directory).resolve()
    if directory.parent != tools.OUTPUT.resolve() or not directory.name.startswith(("run-", "control-")):
        raise ValueError("campaign output must be invocation-owned under its output directory")
    directory.mkdir(exist_ok=True)
    if any(directory.iterdir()):
        raise ValueError("campaign output must be empty; preserve existing reports")
    private = directory / "private"
    private.mkdir(mode=0o700)
    fixture, state, client = (private / name for name in ("fixture", "peer-state", "client"))
    for path in (fixture, state, client):
        path.mkdir(mode=0o755)
    project = "open-agentic-catalog-ard-registry-" + secrets.token_hex(6)
    image, fixture_image = project + ":catalog", project + ":fixture"
    subnet = "172.31." + str(secrets.randbelow(256)) + ".0/28"
    env = {k: os.environ[k] for k in ("PATH", "HOME", "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG", "XDG_RUNTIME_DIR") if k in os.environ}
    env.update(CATALOG_ENV_FILE="/dev/null", IMAGE=image, VERSION="ard-registry-test", COMMIT=run(["git", "rev-parse", "HEAD"]).stdout.strip(), SOURCE_STATE="unknown",
        CATALOG_FUNCTIONAL_FIXTURE_IMAGE=fixture_image, CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY=str(fixture),
        CATALOG_COMPOSE_HTTP_PORT="0", CATALOG_COMPOSE_FIXTURE_PORT="0", CATALOG_COMPOSE_POSTGRES_PORT="0", CATALOG_COMPOSE_SUBNET=subnet,
        CATALOG_COMPOSE_FEDERATION_POLICY_FILE=str(fixture / "policy.json"), CATALOG_COMPOSE_FEDERATION_TRUST_DIRECTORY=str(fixture / "trust"),
        CATALOG_ARD_REGISTRY_IMAGE=tools.IMAGE, CATALOG_ARD_REGISTRY_STATE=str(state), CATALOG_ARD_REGISTRY_USER=f"{os.getuid()}:{os.getgid()}")
    compose = ["docker", "compose", "--env-file", "/dev/null", "-f", "compose.yaml", "-f", "test/functional/compose.yaml", "-f", "test/interoperability/ard-registry/compose.yaml", "-p", project]
    report = {"status": "incomplete", "cleanup": "unrun", "sourceCommit": env["COMMIT"], "sourceDigest": before,
        "peer": installed, "project": project, "control": control, "phases": dict.fromkeys(PHASES, "unrun"), "observations": {}, "probeNames": [], "images": {},
        "scope": "Catalog publication to unchanged configured-source crawler; reverse List incompatible"}
    deadline = time.monotonic() + 1800

    def save():
        """Retain the current incomplete report before any new owned operation."""
        (directory / "report.json").write_text(json.dumps(report, indent=2) + "\n")

    def checkpoint(phase):
        report["phase"] = phase
        save()
        print("ARD registry: " + phase, flush=True)

    def command(arguments, *, timeout=120, check=True):
        left = deadline - time.monotonic()
        if left <= 0:
            raise TimeoutError("ARD registry campaign deadline")
        return run(arguments, env=env, timeout=min(timeout, left), check=check, limit=16 << 20)

    def dc(*arguments, **options):
        return command(compose + list(arguments), **options).stdout

    def client_phase(phase):
        """Run the bounded HTTP client in the same private namespace, with no socket access."""
        report["clientName"] = project + "-client"
        save()
        result = command(["docker", "run", "--rm", "--pull=never", "--name", report["clientName"], "--label", "com.docker.compose.project=" + project,
            "--network", "container:" + ids["api"], "--user", f"{os.getuid()}:{os.getgid()}", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges",
            "--pids-limit", "128", "--memory", "512m", "--cpus", "1", "--env", "PYTHONDONTWRITEBYTECODE=1",
            "--mount", f"type=bind,src={shared.HERE},dst=/workspace/test/interoperability,readonly",
            "--mount", f"type=bind,src={fixture / 'trust/functional-ca.pem'},dst=/ca.pem,readonly",
            "--mount", f"type=bind,src={state},dst=/peer-state,readonly", "--mount", f"type=bind,src={client},dst=/result",
            python_image, "python", "-B", "/workspace/test/interoperability/ard_registry_campaign.py", phase], timeout=180, check=False)
        (directory / (phase + ".log")).write_text(result.stdout + result.stderr)
        assert result.code == 0, "native phase failed: " + phase + "; " + result.stderr[-2500:]
        observation = json.loads((client / (phase + ".json")).read_text())
        report["observations"][phase] = observation

    def interrupted(*_):
        raise KeyboardInterrupt()

    previous = {sig: signal.signal(sig, interrupted) for sig in (signal.SIGINT, signal.SIGTERM)}
    try:
        checkpoint("setup")
        report["removeImages"] = ["docker", "image", "rm", image, fixture_image]
        save()
        built = command(compose + ["build", "migrate", "federation-fixture"], timeout=900, check=False)
        (directory / "build.log").write_text(built.stdout + built.stderr)
        assert built.code == 0, "application/fixture build failed"
        for path in (fixture, private / "untrusted"):
            path.mkdir(exist_ok=True, mode=0o755)
            command(["docker", "run", "--rm", "--pull=never", "--network=none", "--name", project + "-generate", "--label", "com.docker.compose.project=" + project,
                "--user", f"{os.getuid()}:{os.getgid()}", "--mount", f"type=bind,src={path},dst=/fixture", fixture_image,
                "generate", "--directory", "/fixture", "--subnet", subnet], timeout=45)
        shutil.copyfile(private / "untrusted/trust/functional-ca.pem", fixture / "trust/untrusted.pem")
        (directory / "compose.json").write_text(dc("config", "--format", "json"))
        report["stopCommand"] = ["docker", "compose", "--env-file", "/dev/null", "-f", str(directory / "compose.json"), "-p", project, "down", "--volumes", "--remove-orphans"]
        save()
        dc("up", "-d", "--wait", "--wait-timeout", "180", "api", "worker", "federation-fixture", timeout=240)
        ids = {service: dc("ps", "--quiet", service).strip() for service in ("api", "worker", "postgres", "federation-fixture")}
        assert all(ids.values())
        report["phases"]["setup"] = "passed"
        checkpoint("seed")
        client_phase("seed")
        report["phases"]["seed"] = "passed"
        dc("up", "-d", "--no-deps", "ard-registry")
        ids["ard-registry"] = dc("ps", "--quiet", "ard-registry").strip()
        for service, cid in ids.items():
            inspected = json.loads(command(["docker", "inspect", cid]).stdout)[0]
            assert not inspected["HostConfig"].get("PortBindings"), "owned fixture published a port"
            report["images"][service] = inspected["Image"]
            if service == "ard-registry":
                effective = dict(value.split("=", 1) for value in inspected["Config"]["Env"])
                assert json.loads(effective["ARD_SOURCES"]) == [{"id": "owned-oac", "url": SOURCE}]
                assert json.loads(effective["ARD_UPSTREAMS"]) == []
                assert effective["ARD_SNAPSHOT"] == "/state/snapshot.json" and effective["NODE_EXTRA_CA_CERTS"] == "/fixture/trust/functional-ca.pem"
                assert inspected["HostConfig"]["NetworkMode"] == "container:" + ids["api"]
        assert report["images"]["ard-registry"] == installed["imageId"]
        network = json.loads(command(["docker", "network", "inspect", project + "_default"]).stdout)[0]
        assert network["Internal"] is True
        checkpoint("initial")
        client_phase("initial")
        report["phases"]["initial"] = "passed"
        if control == "metadata-mismatch":
            wrong = copy.deepcopy(report["observations"]["seed"]["entries"])
            wrong[0]["description"] = "Intentional changed metadata expectation"
            actual = report["observations"]["initial"]
            check_readback(wrong, actual["list"], actual["search"], actual["snapshot"])
            raise AssertionError("metadata failure control did not fail")
        if control == "interrupt":
            # An external controller sends the real SIGTERM at this owned checkpoint.
            save()
            while time.monotonic() < deadline:
                time.sleep(0.1)
            raise TimeoutError("interruption controller did not signal the supervisor")
        checkpoint("confinement")
        api_info = json.loads(command(["docker", "inspect", ids["api"]]).stdout)[0]
        fixture_ip = next(iter(api_info["NetworkSettings"]["Networks"].values()))["IPAddress"]
        probe = """import errno,json,socket,sys
s=socket.socket();s.settimeout(2)
try:assert s.connect_ex((sys.argv[1],8443))==0,'owned source unreachable'
finally:s.close()
s=socket.socket();s.settimeout(2)
try:code=s.connect_ex(('1.1.1.1',443))
finally:s.close()
assert code in (errno.ENETUNREACH,errno.EHOSTUNREACH,errno.ETIMEDOUT,errno.EAGAIN),code
try:socket.getaddrinfo('example.com',443);raise AssertionError('external DNS resolved')
except socket.gaierror:pass
print(json.dumps({'ownedTCPAllowed':True,'externalTCPDenied':True,'externalDNSDenied':True}))
"""
        for role in ("api", "worker", "ard-registry"):
            name = project + "-probe-" + role
            report["probeNames"].append(name)
            save()
            result = command(["docker", "run", "--rm", "--pull=never", "--name", name, "--label", "com.docker.compose.project=" + project,
                "--network", "container:" + ids[role], "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", python_image, "python", "-I", "-B", "-c", probe, fixture_ip], timeout=25)
            report["observations"]["confinement-" + role] = json.loads(result.stdout)
        report["phases"]["confinement"] = "passed"
        for phase in ("update", "withdrawal", "malformed", "outage", "restart", "recovery", "tls-refusal"):
            checkpoint(phase)
            if phase == "restart":
                dc("stop", "--timeout", "10", "ard-registry")
                dc("start", "ard-registry")
            if phase == "tls-refusal":
                name = project + "-untrusted"
                report["probeNames"].append(name)
                save()
                dc("run", "--detach", "--no-deps", "--name", name, "-e", "NODE_EXTRA_CA_CERTS=/fixture/trust/untrusted.pem",
                    "-e", "ARD_PORT=9011", "-e", "ARD_SELF_URL=http://127.0.0.1:9011/api", "-e", "ARD_SNAPSHOT=/tmp/untrusted.json", "ard-registry")
            client_phase(phase)
            if phase == "tls-refusal":
                code = "fetch(" + json.dumps(SOURCE) + ").then(()=>process.exit(1)).catch(e=>console.log(JSON.stringify({code:e.cause?.code})))"
                refusal = json.loads(command(["docker", "exec", name, "node", "-e", code]).stdout)
                assert refusal["code"] in ("UNABLE_TO_VERIFY_LEAF_SIGNATURE", "SELF_SIGNED_CERT_IN_CHAIN", "UNABLE_TO_GET_ISSUER_CERT_LOCALLY"), "wrong TLS refusal cause"
                report["observations"][phase]["nativeTLSCause"] = refusal
            report["phases"][phase] = "passed"
        checkpoint("final-checks")
        assert source_fingerprint() == before, "source changed during qualification"
        assert tools.installed() == installed, "native tool changed during qualification"
        report["ownedBeforeCleanup"] = owned_resources(project, env)
        report["phases"]["final-checks"] = "passed"
    except BaseException as error:
        report["error"] = "interrupted" if isinstance(error, KeyboardInterrupt) else (str(error)[-4000:] or type(error).__name__)
        if report.get("phase") in report["phases"]:
            report["phases"][report["phase"]] = "failed"
    finally:
        for sig in previous:
            signal.signal(sig, signal.SIG_IGN)
        if "ids" in locals() and ids.get("ard-registry"):
            try:
                result = run(["docker", "logs", ids["ard-registry"]], env=env, check=False, timeout=15)
                (directory / "native-peer.log").write_text(result.stdout + result.stderr)
            except Exception as error:
                report["diagnosticError"] = str(error)[-2000:]
        cleanup_owned(compose, env, project, (image, fixture_image), private, report)
        report["status"] = "passed" if finish(report) else "failed"
        save()
        for sig, handler in previous.items():
            signal.signal(sig, handler)
        print("ARD registry report: " + str(directory.relative_to(shared.ROOT)), flush=True)
    if report["status"] != "passed":
        raise RuntimeError("ARD registry qualification failed: " + report.get("error", report.get("cleanupError", "incomplete")))
    return report


def cleanup_controls():
    """Exercise actual assertion failure and SIGTERM, then inspect owned residue."""
    tools.OUTPUT.mkdir(parents=True, exist_ok=True)
    directory = Path(tempfile.mkdtemp(prefix="controls-", dir=tools.OUTPUT))
    results = {}
    for mode in ("metadata-mismatch", "interrupt"):
        target = Path(tempfile.mkdtemp(prefix="control-", dir=tools.OUTPUT)).resolve()
        sent = False
        with (directory / (mode + ".log")).open("w") as log:
            child = subprocess.Popen([sys.executable, "-B", str(Path(__file__).resolve()), "--control", mode, "--directory", str(target)], stdout=log, stderr=subprocess.STDOUT)
            try:
                deadline = time.monotonic() + 1800
                while child.poll() is None:
                    if time.monotonic() >= deadline:
                        raise TimeoutError("native cleanup control deadline")
                    if mode == "interrupt" and not sent:
                        try:
                            value = json.loads((target / "report.json").read_text())
                        except (OSError, ValueError):
                            value = {}
                        if value.get("phase") == "initial" and value.get("phases", {}).get("initial") == "passed":
                            child.send_signal(signal.SIGTERM)
                            sent = True
                    time.sleep(0.1)
            finally:
                if child.poll() is None:
                    child.terminate()
                    child.wait(timeout=60)
        value = json.loads((target / "report.json").read_text())
        assert child.returncode != 0 and value["status"] == "failed", "negative control falsely passed"
        expected = "interrupted" if mode == "interrupt" else "native entry metadata changed or field lost"
        assert value.get("error") == expected, "control failed for the wrong reason: " + str(value.get("error"))
        assert sent is (mode == "interrupt"), "actual signal control incomplete"
        assert value["cleanup"] == "complete" and not (target / "private").exists()
        assert not any(owned_resources(value["project"], os.environ).values()), "independent cleanup inventory failed"
        results[mode] = {"report": str(target.relative_to(shared.ROOT)), "error": expected, "cleanup": "complete", "actualSIGTERM": sent}
        print("ARD registry cleanup control: " + mode + " passed", flush=True)
    prior = (target / "report.json").read_bytes()
    refusal = run([sys.executable, "-B", str(Path(__file__).resolve()), "--directory", str(target)], check=False, timeout=30)
    assert refusal.code != 0 and "campaign output must be empty" in refusal.stderr
    assert (target / "report.json").read_bytes() == prior and not (target / "private").exists(), "retained report was overwritten"
    results["existing-report"] = {"refused": True, "originalBytesPreserved": True}
    (directory / "report.json").write_text(json.dumps({"status": "passed", "controls": results}, indent=2) + "\n")
    print("ARD registry cleanup report: " + str(directory.relative_to(shared.ROOT)), flush=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--control", choices=("metadata-mismatch", "interrupt"))
    parser.add_argument("--directory", type=Path)
    parser.add_argument("--cleanup-controls", action="store_true")
    options = parser.parse_args()
    if options.cleanup_controls:
        signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(KeyboardInterrupt()))
        cleanup_controls()
    else:
        qualification(control=options.control, directory=options.directory)
