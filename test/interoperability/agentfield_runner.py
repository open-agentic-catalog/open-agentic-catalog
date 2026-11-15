"""Own the native consumer, actual publication and two independent PostgreSQL stores."""

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
from agentfield_contract import PHASES, SOURCE, check_dashboard, finish
import agentfield_tools as tools


def source_fingerprint():
    """Bind the application, shared helpers and selected fixture inputs."""
    return shared.fingerprint([*shared.CATALOG_INPUTS, shared.ROOT / "Makefile", tools.INPUTS,
        shared.ROOT / "test/functional", *sorted(shared.HERE.glob("*.py")), shared.ROOT / "compose.yaml",
        shared.ROOT / "Containerfile", shared.ROOT / ".dockerignore", shared.ROOT / "config/postgres.json",
        shared.ROOT / "deployments/postgres/init.sql", shared.ROOT / "deployments/container/licenses.sh", shared.ROOT / "LICENSE"])


def qualification(*, control=None, directory=None):
    """Only exact successful phases and cleanup qualify the unchanged native client."""
    if sys.flags.optimize or os.getuid() == 0:
        raise RuntimeError("AgentField qualification requires assertions and a non-root owner")
    shared.local_daemon()
    installed = tools.installed()
    python_image = next(line.split()[1] for line in (shared.HERE / "Containerfile").read_text().splitlines() if line.startswith("FROM python:"))
    shared.docker("image", "inspect", python_image)
    before = source_fingerprint()
    tools.OUTPUT.mkdir(parents=True, exist_ok=True)
    directory = Path(tempfile.mkdtemp(prefix="run-", dir=tools.OUTPUT) if directory is None else directory).resolve()
    if directory.parent != tools.OUTPUT.resolve() or not directory.name.startswith(("run-", "control-")):
        raise ValueError("campaign output must be invocation-owned")
    directory.mkdir(exist_ok=True)
    if any(directory.iterdir()):
        raise ValueError("campaign output must be empty; preserve existing reports")
    private = directory / "private"
    private.mkdir(mode=0o700)
    fixture, state, client, config = [private / name for name in ("fixture", "native-state", "client", "config")]
    for path in (fixture, state, client, config):
        path.mkdir(mode=0o755)
    shutil.copyfile(tools.INPUTS / "config.yaml", config / "config.yaml")
    project = "open-agentic-catalog-agentfield-" + secrets.token_hex(6)
    image, fixture_image = project + ":catalog", project + ":fixture"
    env = {k: os.environ[k] for k in ("PATH", "HOME", "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG", "XDG_RUNTIME_DIR") if k in os.environ}
    env.update(CATALOG_ENV_FILE="/dev/null", IMAGE=image, VERSION="agentfield-test", SOURCE_STATE="unknown",
        COMMIT=run(["git", "rev-parse", "HEAD"]).stdout.strip(), CATALOG_FUNCTIONAL_FIXTURE_IMAGE=fixture_image,
        CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY=str(fixture), CATALOG_COMPOSE_HTTP_PORT="0", CATALOG_COMPOSE_FIXTURE_PORT="0",
        CATALOG_COMPOSE_POSTGRES_PORT="0", CATALOG_COMPOSE_SUBNET="172.31." + str(secrets.randbelow(256)) + ".0/28",
        CATALOG_COMPOSE_FEDERATION_POLICY_FILE=str(fixture / "policy.json"), CATALOG_COMPOSE_FEDERATION_TRUST_DIRECTORY=str(fixture / "trust"),
        CATALOG_AGENTFIELD_IMAGE=installed["image"], CATALOG_AGENTFIELD_STATE=str(state), CATALOG_AGENTFIELD_CONFIG=str(config),
        CATALOG_AGENTFIELD_USER=f"{os.getuid()}:{os.getgid()}", CATALOG_AGENTFIELD_PYTHON=python_image,
        CATALOG_AGENTFIELD_HARNESS=str(shared.HERE))
    compose = ["docker", "compose", "--env-file", "/dev/null", "-f", "compose.yaml", "-f", "test/functional/compose.yaml",
               "-f", "test/interoperability/agentfield/compose.yaml", "-p", project]
    report = {"status": "incomplete", "cleanup": "unrun", "project": project, "sourceCommit": env["COMMIT"],
              "sourceDigest": before, "peer": installed, "control": control, "phases": dict.fromkeys(PHASES, "unrun"),
              "observations": {}, "probeNames": [], "images": {}, "scope": "public metadata Search and passive explicit import only"}
    deadline = time.monotonic() + 1800

    def save():
        (directory / "report.json").write_text(json.dumps(report, indent=2) + "\n")

    def checkpoint(phase):
        report["phase"] = phase
        save()
        print("AgentField: " + phase, flush=True)

    def command(arguments, *, timeout=120, check=True):
        left = deadline - time.monotonic()
        if left <= 0:
            raise TimeoutError("AgentField campaign deadline")
        return run(arguments, env=env, timeout=min(timeout, left), limit=16 << 20, check=check)

    def dc(*arguments, **options):
        return command(compose + list(arguments), **options).stdout

    def client_phase(phase):
        report["clientName"] = project + "-client"
        save()
        result = command(["docker", "run", "--rm", "--pull=never", "--name", report["clientName"], "--label", "com.docker.compose.project=" + project,
            "--network", "container:" + ids["api"], "--user", f"{os.getuid()}:{os.getgid()}", "--read-only", "--cap-drop=ALL",
            "--security-opt=no-new-privileges", "--pids-limit=128", "--memory=256m", "--cpus=1",
            "--mount", f"type=bind,src={shared.HERE},dst=/workspace/test/interoperability,readonly",
            "--mount", f"type=bind,src={fixture / 'trust/functional-ca.pem'},dst=/ca.pem,readonly",
            "--mount", f"type=bind,src={client},dst=/result", python_image, "python", "-B",
            "/workspace/test/interoperability/agentfield_campaign.py", phase], timeout=120, check=False)
        (directory / (phase + ".log")).write_text(result.stdout + result.stderr)
        assert result.code == 0, "native phase failed: " + phase + "; " + result.stderr[-2500:]
        report["observations"][phase] = json.loads((client / (phase + ".json")).read_text())

    def interrupted(*_):
        raise KeyboardInterrupt()

    previous = {sig: signal.signal(sig, interrupted) for sig in (signal.SIGINT, signal.SIGTERM)}
    try:
        checkpoint("setup")
        built = command(compose + ["build", "migrate", "federation-fixture"], timeout=900, check=False)
        (directory / "build.log").write_text(built.stdout + built.stderr)
        assert built.code == 0, "application/fixture build failed"
        for path in (fixture, private / "untrusted"):
            path.mkdir(exist_ok=True, mode=0o755)
            command(["docker", "run", "--rm", "--pull=never", "--network=none", "--name", project + "-generate",
                "--label", "com.docker.compose.project=" + project, "--user", f"{os.getuid()}:{os.getgid()}",
                "--mount", f"type=bind,src={path},dst=/fixture", fixture_image, "generate", "--directory", "/fixture",
                "--subnet", env["CATALOG_COMPOSE_SUBNET"]], timeout=45)
        shutil.copyfile(private / "untrusted/trust/functional-ca.pem", fixture / "trust/untrusted.pem")
        (directory / "compose.json").write_text(dc("config", "--format", "json"))
        report["stopCommand"] = ["docker", "compose", "--env-file", "/dev/null", "-f", str(directory / "compose.json"), "-p", project,
                                 "down", "--volumes", "--remove-orphans"]
        save()
        dc("up", "-d", "--wait", "--wait-timeout", "180", "api", "worker", "federation-fixture", "agentfield-postgres", "agentfield-observer", "agentfield", timeout=240)
        ids = {service: dc("ps", "--quiet", service).strip() for service in ("api", "worker", "postgres", "agentfield", "agentfield-postgres", "agentfield-observer")}
        assert all(ids.values()), "owned service missing"
        for service, cid in ids.items():
            inspected = json.loads(command(["docker", "inspect", cid]).stdout)[0]
            assert not inspected["HostConfig"].get("PortBindings"), "owned fixture published a port"
            report["images"][service] = inspected["Image"]
            if service == "agentfield":
                assert inspected["HostConfig"]["NetworkMode"] == "container:" + ids["api"]
                effective = dict(value.split("=", 1) for value in inspected["Config"]["Env"])
                assert effective["AGENTFIELD_TELEMETRY_ENABLED"] == "false" and not any("PROXY" in name.upper() for name in effective)
        assert report["images"]["agentfield"] == installed["imageId"]
        assert json.loads(command(["docker", "network", "inspect", project + "_default"]).stdout)[0]["Internal"] is True
        report["phases"]["setup"] = "passed"
        for phase in ("native-control", "seed", "search", "import", "restart", "refusals", "tls-refusal", "tls-recovery", "draft", "withdrawal"):
            checkpoint(phase)
            if phase == "restart":
                dc("stop", "--timeout", "15", "agentfield")
                dc("start", "agentfield")
            if phase in ("tls-refusal", "tls-recovery"):
                env["CATALOG_AGENTFIELD_CA"] = "/fixture/trust/untrusted.pem" if phase == "tls-refusal" else "/fixture/trust/functional-ca.pem"
                dc("up", "-d", "--no-deps", "--force-recreate", "agentfield")
            client_phase(phase)
            report["phases"][phase] = "passed"
            if phase == "restart" and control:
                save()
                if control == "metadata-mismatch":
                    stored = report["observations"]["import"]
                    wrong = copy.deepcopy([*report["observations"]["native-control"]["importRequests"],
                                           (report["observations"]["search"]["selected"]["results"][0], SOURCE)])
                    wrong[-1][0]["description"] = "Intentional changed metadata expectation"
                    check_dashboard(stored["dashboard"], wrong)
                    raise AssertionError("metadata mismatch control did not fail")
                while time.monotonic() < deadline:
                    time.sleep(0.1)
                raise TimeoutError("interruption controller did not signal the supervisor")
        checkpoint("confinement")
        address = next(iter(json.loads(command(["docker", "inspect", ids["api"]]).stdout)[0]["NetworkSettings"]["Networks"].values()))["IPAddress"]
        probe = """import errno,json,socket,sys
with socket.socket() as s:
 s.settimeout(2);assert s.connect_ex((sys.argv[1],8444))==0,'owned source unreachable'
with socket.socket() as s:
 s.settimeout(2);code=s.connect_ex(('1.1.1.1',443))
assert code in (errno.ENETUNREACH,errno.EHOSTUNREACH,errno.ETIMEDOUT,errno.EAGAIN),code
try:socket.getaddrinfo('example.com',443);raise AssertionError('external DNS resolved')
except socket.gaierror:pass
print(json.dumps({'ownedTCPAllowed':True,'externalTCPDenied':True,'externalDNSDenied':True}))
"""
        # Native shares the API network namespace; check its recreated container ID.
        ids["agentfield"] = dc("ps", "--quiet", "agentfield").strip()
        for role in ("api", "worker", "agentfield"):
            name = project + "-probe-" + role
            report["probeNames"].append(name)
            save()
            result = command(["docker", "run", "--rm", "--pull=never", "--name", name, "--label", "com.docker.compose.project=" + project,
                "--network", "container:" + ids[role], "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges",
                python_image, "python", "-I", "-B", "-c", probe, address], timeout=25)
            report["observations"]["confinement-" + role] = json.loads(result.stdout)
        report["phases"]["confinement"] = "passed"
        checkpoint("final-checks")
        client_phase("final-checks")
        assert source_fingerprint() == before, "source changed during qualification"
        assert tools.installed() == installed, "native installation changed"
        report["phases"]["final-checks"] = "passed"
    except BaseException as error:
        report["error"] = "interrupted" if isinstance(error, KeyboardInterrupt) else (str(error)[-4000:] or type(error).__name__)
        if report.get("phase") in report["phases"]:
            report["phases"][report["phase"]] = "failed"
    finally:
        for sig in previous:
            signal.signal(sig, signal.SIG_IGN)
        for service in ("agentfield", "agentfield-observer", "agentfield-postgres"):
            try:
                result = run(compose + ["logs", "--no-color", service], env=env, check=False, timeout=15, limit=4 << 20)
                (directory / (service + ".log")).write_text(result.stdout + result.stderr)
            except Exception as error:
                report["diagnosticError"] = str(error)[-2000:]
        cleanup_owned(compose, env, project, (image, fixture_image), private, report)
        report["status"] = "passed" if finish(report) else "failed"
        save()
        for sig, handler in previous.items():
            signal.signal(sig, handler)
        print("AgentField report: " + str(directory.relative_to(shared.ROOT)), flush=True)
    if report["status"] != "passed":
        raise RuntimeError("AgentField qualification failed: " + report.get("error", report.get("cleanupError", "incomplete")))
    return report


def cleanup_controls():
    """Real failed assertions and SIGTERM must exit unsuccessfully with no owned residue."""
    tools.OUTPUT.mkdir(parents=True, exist_ok=True)
    directory = Path(tempfile.mkdtemp(prefix="controls-", dir=tools.OUTPUT))
    results = {}
    for mode in ("metadata-mismatch", "interrupt"):
        target = Path(tempfile.mkdtemp(prefix="control-", dir=tools.OUTPUT))
        sent = False
        with (directory / (mode + ".log")).open("w") as log:
            child = subprocess.Popen([sys.executable, "-B", str(Path(__file__).resolve()), "--control", mode, "--directory", str(target)], stdout=log, stderr=subprocess.STDOUT)
            try:
                deadline = time.monotonic() + 1800
                while child.poll() is None:
                    if time.monotonic() >= deadline:
                        raise TimeoutError("native cleanup control deadline")
                    try:
                        value = json.loads((target / "report.json").read_text())
                    except (OSError, ValueError):
                        value = {}
                    if mode == "interrupt" and not sent and value.get("phases", {}).get("restart") == "passed":
                        child.send_signal(signal.SIGTERM)
                        sent = True
                    time.sleep(0.1)
            finally:
                if child.poll() is None:
                    child.terminate()
                    child.wait(timeout=150)
        value = json.loads((target / "report.json").read_text())
        assert child.returncode != 0 and value["status"] == "failed", "negative control falsely passed"
        expected = "interrupted" if mode == "interrupt" else "native imported metadata mismatch"
        assert value.get("error") == expected, "control failed for wrong reason: " + str(value.get("error"))
        assert sent is (mode == "interrupt") and value["cleanup"] == "complete" and not (target / "private").exists()
        assert not any(owned_resources(value["project"], os.environ).values()), "independent cleanup inventory failed"
        results[mode] = {"report": str(target.relative_to(shared.ROOT)), "error": expected, "cleanup": "complete", "actualSIGTERM": sent}
    original = (target / "report.json").read_bytes()
    refusal = run([sys.executable, "-B", str(Path(__file__).resolve()), "--directory", str(target)], timeout=30, check=False)
    assert refusal.code != 0 and "campaign output must be empty" in refusal.stderr and (target / "report.json").read_bytes() == original
    results["existing-report"] = {"refused": True, "originalBytesPreserved": True}
    (directory / "report.json").write_text(json.dumps({"status": "passed", "controls": results}, indent=2) + "\n")
    print("AgentField cleanup report: " + str(directory.relative_to(shared.ROOT)), flush=True)


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
