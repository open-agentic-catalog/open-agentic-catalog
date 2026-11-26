"""Run the SPIRE workload-read qualification on an owned Compose project."""
import argparse
import hashlib
import json
import secrets
import signal
import subprocess
import sys
import tempfile
from pathlib import Path

import tools
from fixture import Fixture, ROOT, UIDS, DOMAIN, inputs
from scenario import exercise

PHASES = (
    "startup",
    "distinct-attested-identities",
    "go-sdk-private-resource-and-published-version",
    "catalogctl-authenticated-capabilities",
    "signing-authority-rotation-provider-outage-and-recovery",
    "signed-issuer-and-deployment-claim-refusals",
    "conditional-publication-rechecks-current-access",
    "audience-membership-renewal-expiry-and-revocation",
)


def check_cleanup(project, image, run):
    for kind in ("container", "network", "volume"):
        args = ["docker", kind, "ls", "-q", "--filter", "label=com.docker.compose.project=" + project]
        if kind == "container":
            args.append("--all")
        if subprocess.check_output(args, timeout=30).strip():
            return "failed"
    tags = subprocess.check_output(["docker", "image", "ls", "-q", "--filter", "reference=" + image], timeout=30)
    return "failed" if tags.strip() or any(run.glob("private-*")) else "passed"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source-renewal", action="store_true")
    args = parser.parse_args()
    fixture_type, workflow, phases = Fixture, exercise, PHASES
    if args.source_renewal:
        from source_renewal import SourceFixture, exercise_sources, SOURCE_PHASES
        fixture_type, workflow, phases = SourceFixture, exercise_sources, SOURCE_PHASES
    root = ROOT / "output/interoperability" / ("spire-renewal" if args.source_renewal else "spire")
    root.mkdir(parents=True, exist_ok=True)
    run = Path(tempfile.mkdtemp(prefix="run-", dir=root))
    project = "oac-spire-" + secrets.token_hex(6)
    image = project + ":catalog"
    result = {"project": project, "spire": tools.VERSION, "stage": "preflight",
              "completed": [], "result": "failed", "cleanup": "pending"}
    docker_started = False
    for sig in (signal.SIGTERM, signal.SIGINT):
        signal.signal(sig, lambda *_: sys.exit(1))
    print("SPIRE qualification:", run.relative_to(ROOT), flush=True)
    try:
        arch = tools.local_daemon()
        result["platform"] = "linux/" + arch
        result["stage"] = "prepare"
        with tempfile.TemporaryDirectory(prefix="private-", dir=run) as private:
            path = Path(private)
            result["identities"] = {name: DOMAIN + name for name in UIDS}
            fixture = fixture_type(path, project, inputs(path, image, arch))
            with (path / "bin/reader").open("rb") as stream:
                result["goSdkReaderSha256"] = hashlib.file_digest(stream, "sha256").hexdigest()
            result["stage"] = "build"
            docker_started = True
            try:
                subprocess.run(["make", "container-build", "IMAGE=" + image, "CONTAINER_PLATFORM=linux/" + arch,
                                "CONTAINER_ENGINE=docker", "CATALOG_ENV_FILE=/dev/null"], cwd=ROOT, check=True, timeout=900)
                result["image"] = subprocess.check_output(["docker", "image", "inspect", "--format", "{{.Id}}", image],
                                                          text=True, timeout=30).strip()
                try:
                    result["stage"] = "startup"
                    fixture.start()
                    build, _ = fixture.client().request("GET", "/version")
                    revision = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True, timeout=15).strip()
                    if build.get("commit") != revision:
                        raise ValueError("Catalog build differs from the selected revision")
                    result["catalogBuild"] = build
                    result["completed"].append("startup")
                    result["stage"] = "workload-reads"
                    workflow(fixture, result)
                    if result["completed"] != list(phases):
                        raise ValueError("SPIRE qualification did not complete all required phases")
                finally:
                    fixture.stop()
            finally:
                if subprocess.run(["docker", "image", "inspect", image], capture_output=True, timeout=30).returncode == 0:
                    subprocess.run(["docker", "image", "rm", image], check=True, timeout=30, capture_output=True)
        result["stage"] = "complete"
        result["result"] = "passed"
    finally:
        # Record interrupted and failed runs as well; never include command output or private inputs.
        try:
            if docker_started:
                result["cleanup"] = check_cleanup(project, image, run)
            else:
                result["cleanup"] = "failed" if any(run.glob("private-*")) else "not-needed"
        except (OSError, subprocess.SubprocessError):
            result["cleanup"] = "unverified"
        if result["cleanup"] not in ("passed", "not-needed"):
            result["result"] = "failed"
        (run / "report.json").write_text(json.dumps(result, indent=2) + "\n")
    if result["cleanup"] != "passed":
        raise RuntimeError("SPIRE qualification cleanup incomplete")
    print("Passed all selected SPIRE phases; owned fixtures removed.")


if __name__ == "__main__":
    main()
