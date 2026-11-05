"""Real local report/process/Docker boundaries, confined to disposable fixtures."""

from contextlib import contextmanager, redirect_stdout
import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import time
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import local_workflows as local
import test_functional_local_workflows as fixtures


SCRIPT = r'''set -euo pipefail
umask 077
mkdir -p "$ARTIFACT_OUTPUT/reports" "$ARTIFACT_OUTPUT/sbom" "$TMPDIR/context"
summary="$ARTIFACT_OUTPUT/reports/summary.txt"
printf 'run=%s\nattempt=%s\n' "$GITHUB_RUN_ID" "$GITHUB_RUN_ATTEMPT" > "$summary"
printf 'scan=%s\nrelease=%s\n' "$(test -f "$TMPDIR/scan-on" && echo on || echo off)" "$(test -f "$TMPDIR/release-on" && echo on || echo off)" >> "$summary"
printf '{"spdxVersion":"SPDX-2.3","name":"synthetic-local-report"}\n' > "$ARTIFACT_OUTPUT/sbom/control.spdx.json"
project="catalog-local-control-$GITHUB_RUN_ID"
child="catalog-local-child-$GITHUB_RUN_ID"
started=0
cleanup() {
  if test "$started" = 1; then
    if test "$CONTROL_MODE" = compose; then
      docker compose --env-file /dev/null -p "$project" -f control.compose.yaml down --volumes --remove-orphans
    else
      docker rm --force "$child"
    fi
  fi
  printf 'harness cleanup complete\n' >> "$summary"
}
trap cleanup EXIT
trap 'exit 143' TERM INT
test "$GITHUB_RUN_ID" = "$CONTEXT_RUN_ID"
test "$GITHUB_RUN_ATTEMPT" = "$CONTEXT_RUN_ATTEMPT"
test "$(git rev-parse HEAD)" = "$GITHUB_SHA"
printf 'nested-bind-payload\n' > "$TMPDIR/context/payload"
printf 'FROM scratch\nCOPY payload /payload\n' > "$TMPDIR/context/Dockerfile"
docker buildx build --builder default --load --provenance=false --network=none -t "$IMAGE" "$TMPDIR/context"
if test "$CONTROL_MODE" = compose; then
  python3.12 -B -c 'import sys; sys.path.insert(0,"test/ci"); import tools; tools.OWNED.mkdir(parents=True, exist_ok=True); tools.binary_install("compose")'
  test "$(docker compose version --short)" = 5.6.0
  export SCRATCH_DIRECTORY="$TMPDIR"
  cp server.py "$TMPDIR/server.py"
  docker compose --env-file /dev/null -p "$project" -f control.compose.yaml run --rm reader
  started=1
  docker compose --env-file /dev/null -p "$project" -f control.compose.yaml up -d server
  python3.12 -B -c 'import pathlib,time,urllib.request,os; p=pathlib.Path(os.environ["TMPDIR"])/"port"; end=time.monotonic()+15
while not p.is_file():
 if time.monotonic()>=end: raise SystemExit("loopback server readiness expired")
 time.sleep(.05)
assert urllib.request.urlopen("http://127.0.0.1:"+p.read_text(),timeout=5).read()==b"local-workflow-loopback-ok"'
  printf 'nested Compose and loopback passed\n' >> "$summary"
else
  docker run --detach --pull=never --network=none --label "io.open-agentic-catalog.workflow-local=$GITHUB_RUN_ID" --name "$child" --entrypoint /bin/sleep "$RUNNER_IMAGE" 120
  started=1
  printf ready > "$TMPDIR/ready"
  while :; do sleep 1; done
fi
mkdir -p .tools/readonly-cache
printf disposable > .tools/readonly-cache/file
chmod 0555 .tools/readonly-cache
ln -s "OUTSIDE_SENTINEL" .tools/outside-link
chmod 0700 "$ARTIFACT_OUTPUT/reports" "$ARTIFACT_OUTPUT/sbom"
chmod 0600 "$summary"
'''

COMPOSE = '''services:
  reader:
    image: RUNNER_IMAGE_VALUE
    pull_policy: never
    network_mode: none
    read_only: true
    volumes:
      - "${SCRATCH_DIRECTORY}/context/payload:/payload:ro"
    entrypoint: ["/bin/cat", "/payload"]
    labels:
      io.open-agentic-catalog.workflow-local: "${GITHUB_RUN_ID}"
  server:
    image: RUNNER_IMAGE_VALUE
    pull_policy: never
    network_mode: host
    read_only: true
    volumes:
      - "${SCRATCH_DIRECTORY}:/fixture"
    entrypoint: ["python3.12", "-B", "/fixture/server.py"]
    labels:
      io.open-agentic-catalog.workflow-local: "${GITHUB_RUN_ID}"
'''

SERVER = '''from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path
class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b"local-workflow-loopback-ok")
    def log_message(self, *_args):
        pass
server = HTTPServer(("127.0.0.1", 0), Handler)
Path("/fixture/port").write_text(str(server.server_port))
server.serve_forever()
'''


class RuntimeFixtures(unittest.TestCase):
    def setUp(self):
        self.fixture = fixtures.LocalWorkflowFixtures()
        self.fixture.setUp()
        self.addCleanup(self.fixture.doCleanups)
        self.root, self.owned = self.fixture.root, self.fixture.owned

    def simulate(self, execution, *, summary=True, cleanup_error=False, oversized_profile=False):
        @contextmanager
        def resources(*_args, **_kwargs):
            state = SimpleNamespace(images=[], errors=[])
            yield state
            if cleanup_error:
                state.errors.append("docker-resource-cleanup")
                raise ValueError("controlled cleanup failure")
        def execute(args, *_args):
            source = Path(args[args.index("--directory") + 1])
            run_id = next(value.split("=", 1)[1] for value in args if value.startswith("GITHUB_RUN_ID="))
            if summary:
                job = "isolated-unit" if oversized_profile else "documentation"
                path = source / f"output/ci/{run_id}-1/quality/{job}/summary.md"
                path.parent.mkdir(parents=True)
                path.write_text("safe summary\n")
            if oversized_profile:
                (source / "coverage").mkdir()
                (source / "coverage/unit.out").write_bytes(b"too large")
            return execution
        with (
            patch.object(local, "ROOT", self.root), patch.object(local, "OWNED", self.owned),
            patch.object(local, "check_runner", return_value="sha256:fixture"),
            patch.object(local, "docker", return_value=SimpleNamespace(stdout="")),
            patch.object(local, "docker_resources", side_effect=resources),
            patch.object(local, "execute", side_effect=execute), redirect_stdout(io.StringIO()),
        ):
            code = local.run("unit" if oversized_profile else "docs", "backend")
        path = next((self.root / "output/artifacts/workflows").glob("*/run.json"))
        result = json.loads(path.read_text())
        if result.get("remainingDirectory"):
            self.addCleanup(shutil.rmtree, result["remainingDirectory"])
        return code, result, path.parent

    def test_success_requires_reports_and_preserves_the_original_checkout(self):
        original = self.fixture.git("status", "--porcelain")
        code, result, destination = self.simulate({"status": "passed", "exitCode": 0})
        self.assertEqual(code, 0)
        self.assertEqual(result["status"], "passed")
        self.assertEqual(result["cleanup"], "passed")
        self.assertEqual(result["reportStatus"], "complete")
        self.assertEqual(len(result["files"]), 1)
        self.assertEqual((destination / result["files"][0]["path"]).read_text(), "safe summary\n")
        self.assertEqual(self.fixture.git("status", "--porcelain"), original)

    def test_zero_exit_without_expected_reports_is_a_failure(self):
        code, result, _ = self.simulate({"status": "passed", "exitCode": 0}, summary=False)
        self.assertEqual(code, 1)
        self.assertEqual(result["execution"]["status"], "passed")
        self.assertEqual(result["status"], "failed")
        self.assertEqual(result["reportStatus"], "partial")
        self.assertTrue(result["missingReports"])

    def test_failed_execution_keeps_available_reports_and_the_original_exit(self):
        code, result, _ = self.simulate({"status": "failed", "exitCode": 19})
        self.assertEqual(code, 19)
        self.assertEqual(result["execution"]["status"], "failed")
        self.assertEqual(result["cleanup"], "passed")
        self.assertEqual(result["reportStatus"], "complete")

    def assert_partial_report_inventory(self, execution, expected_exit):
        with patch.object(local.local_reports, "COVERAGE_LIMIT", 4):
            code, result, destination = self.simulate(execution, oversized_profile=True)
        self.assertEqual(code, expected_exit)
        self.assertEqual(result["execution"], execution)
        self.assertEqual(result["status"], "failed")
        self.assertEqual(result["reportStatus"], "failed")
        self.assertEqual(result["cleanup"], "passed")
        self.assertEqual(len(result["files"]), 1)
        actual = {str(p.relative_to(destination)) for p in (destination / "reports").rglob("*") if p.is_file()}
        self.assertEqual(actual, {item["path"] for item in result["files"]})
        summary = destination / result["files"][0]["path"]
        self.assertEqual(summary.read_text(), "safe summary\n")
        self.assertEqual(result["files"][0]["sha256"], hashlib.sha256(summary.read_bytes()).hexdigest())

    def test_report_rejection_preserves_the_original_failure_and_complete_summary(self):
        self.assert_partial_report_inventory({"status": "failed", "exitCode": 19}, 19)

    def test_report_rejection_refuses_success_and_keeps_the_complete_summary(self):
        self.assert_partial_report_inventory({"status": "passed", "exitCode": 0}, 1)

    def test_cleanup_failure_is_separate_and_does_not_claim_success(self):
        code, result, _ = self.simulate({"status": "passed", "exitCode": 0}, cleanup_error=True)
        self.assertEqual(code, 1)
        self.assertEqual(result["execution"]["status"], "passed")
        self.assertEqual(result["cleanup"], "failed")
        self.assertIn("docker-resource-cleanup", result["cleanupErrors"])
        self.assertTrue(Path(result["remainingDirectory"]).exists())

    def test_failed_execution_and_cleanup_keep_both_results(self):
        code, result, _ = self.simulate({"status": "failed", "exitCode": 19}, cleanup_error=True)
        self.assertEqual(code, 19)
        self.assertEqual(result["execution"], {"status": "failed", "exitCode": 19})
        self.assertEqual(result["cleanup"], "failed")
        self.assertEqual(result["status"], "failed")

    def test_preparation_failure_retains_cleanup_failure_and_owned_directory(self):
        def prepare(state):
            state.prepared = True
            raise ValueError("controlled setup failure after resource creation")

        def cleanup(state):
            state.errors.append("docker-resource-cleanup")
            raise ValueError("controlled early cleanup failure")

        with (
            patch.object(local, "ROOT", self.root), patch.object(local, "OWNED", self.owned),
            patch.object(local, "check_runner", return_value="sha256:fixture"),
            patch.object(local, "docker", return_value=SimpleNamespace(stdout="")),
            patch.object(local.DockerResources, "prepare", prepare),
            patch.object(local.DockerResources, "cleanup", cleanup),
            patch.object(local, "execute") as execute, redirect_stdout(io.StringIO()),
        ):
            code = local.run("docs", "backend")
        path = next((self.root / "output/artifacts/workflows").glob("*/run.json"))
        result = json.loads(path.read_text())
        if result.get("remainingDirectory"):
            self.addCleanup(shutil.rmtree, result["remainingDirectory"])
        self.assertEqual(code, 1)
        self.assertEqual(result["execution"]["status"], "setup_failed")
        self.assertEqual(result["cleanup"], "failed")
        self.assertIn("docker-resource-cleanup", result["cleanupErrors"])
        self.assertTrue(Path(result["remainingDirectory"]).exists())
        execute.assert_not_called()

    def actual_source(self, mode):
        if os.environ.get("CATALOG_TEST_ACT_RUNTIME") != "1" or not os.environ.get("CATALOG_TEST_ACT"):
            self.skipTest("CATALOG_TEST_ACT_RUNTIME=1 selects actual local Docker qualification")
        selected = Path(os.environ["CATALOG_TEST_ACT"])
        shutil.copyfile(selected, self.owned / "act")
        receipt = json.loads((selected.parent / "installation.json").read_text())
        receipt.update(version=local.PIN["version"], archiveSha256=local.PIN["sha256"],
                       binarySha256=hashlib.sha256(selected.read_bytes()).hexdigest())
        (self.owned / "installation.json").write_text(json.dumps(receipt))
        self.sentinel = self.fixture.base / "outside-sentinel"
        self.sentinel.write_text("outside data")
        self.sentinel.chmod(0o640)
        target = self.root / "test/ci"
        target.mkdir(parents=True)
        for name in ("tools.py", "pins.json"):
            shutil.copyfile(local.ROOT / "test/ci" / name, target / name)
        workflow = '''name: Local runtime control
on:
  workflow_dispatch:
    inputs:
      upload_artifacts: {type: boolean, default: false}
      scan: {type: boolean, default: false}
      release_candidate: {type: boolean, default: false}
jobs:
  images:
    runs-on: ubuntu-24.04
    strategy:
      matrix:
        product: [backend]
        arch: [amd64]
    env:
      CONTROL_MODE: CONTROL_MODE_VALUE
      RUNNER_IMAGE: RUNNER_IMAGE_VALUE
      IMAGE: open-agentic-catalog-ci:${{ github.run_id }}-${{ github.run_attempt }}-amd64
      ARTIFACT_OUTPUT: output/ci/${{ github.run_id }}-${{ github.run_attempt }}/images/backend/amd64
      CONTEXT_RUN_ID: ${{ github.run_id }}
      CONTEXT_RUN_ATTEMPT: ${{ github.run_attempt }}
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1
        with:
          persist-credentials: false
          fetch-depth: 0
      - name: Scan input control
        if: inputs.scan
        run: touch "$TMPDIR/scan-on"
      - name: Candidate input control
        if: inputs.release_candidate
        run: touch "$TMPDIR/release-on"
      - name: Forbidden upload
        if: ${{ always() && inputs.upload_artifacts && !env.ACT }}
        run: exit 93
      - name: Qualify local runtime
        run: bash control.sh
'''.replace("CONTROL_MODE_VALUE", mode).replace("RUNNER_IMAGE_VALUE", local.PIN["runner"])
        (self.root / ".github/workflows/images.yml").write_text(workflow)
        (self.root / "control.sh").write_text(SCRIPT.replace("OUTSIDE_SENTINEL", str(self.sentinel)))
        (self.root / "control.compose.yaml").write_text(COMPOSE.replace("RUNNER_IMAGE_VALUE", local.PIN["runner"]))
        (self.root / "server.py").write_text(SERVER)
        self.fixture.git("add", ".")
        self.fixture.git("commit", "--quiet", "-m", "test: prepare owned local Docker runtime control")

    def test_actual_compose_binds_loopback_buildx_boolean_inputs_reports_and_root_owned_cleanup(self):
        self.actual_source("compose")
        initial = self.fixture.git("status", "--porcelain")
        for value in (False, True):
            with self.subTest(value=value), patch.object(local, "ROOT", self.root), patch.object(local, "OWNED", self.owned):
                self.assertEqual(local.run("image", "backend", scan=value, release_candidate=value), 0)
        results = [json.loads(path.read_text()) for path in (self.root / "output/artifacts/workflows").glob("*/run.json")]
        self.assertEqual(len(results), 2)
        for result in results:
            self.assertEqual(result["status"], "passed")
            self.assertEqual(result["cleanup"], "passed")
            self.assertEqual(len(result["images"]), 1)
            destination = self.root / "output/artifacts/workflows" / result["runId"]
            summaries = [item for item in result["files"] if item["path"].endswith("summary.txt")]
            text = (destination / summaries[0]["path"]).read_text()
            expected = "on" if result["inputs"]["scan"] == "true" else "off"
            self.assertIn("scan=" + expected, text)
            self.assertIn("release=" + expected, text)
            self.assertIn("nested Compose and loopback passed", text)
            self.assertIn("harness cleanup complete", text)
            self.assertIn("run=" + result["runId"], text)
        self.assertEqual(self.sentinel.read_text(), "outside data")
        self.assertEqual(self.sentinel.stat().st_mode & 0o777, 0o640)
        self.assertEqual(self.fixture.git("status", "--porcelain"), initial)

    def test_actual_wrapper_deadline_allows_harness_cleanup_and_records_timeout(self):
        self.actual_source("wait")
        workflow, job, suite, docker, _minutes = local.SELECTIONS["image"]
        with (
            patch.object(local, "ROOT", self.root), patch.object(local, "OWNED", self.owned),
            patch.dict(local.SELECTIONS, {"image": (workflow, job, suite, docker, 0.12)}),
        ):
            self.assertEqual(local.run("image", "backend"), 1)
        result = json.loads(next((self.root / "output/artifacts/workflows").glob("*/run.json")).read_text())
        self.assertEqual(result["execution"]["status"], "timed_out")
        self.assertEqual(result["cleanup"], "passed")
        summary = self.root / "output/artifacts/workflows" / result["runId"] / next(item["path"] for item in result["files"] if item["path"].endswith("summary.txt"))
        self.assertIn("harness cleanup complete", summary.read_text())

    def test_actual_preexisting_containers_and_volumes_are_preserved(self):
        self.actual_source("wait")
        marker = self.fixture.base.name.removeprefix("catalog-act-fixture-")
        container_name = "act-preexisting-control-" + marker
        volume_name = "act-preexisting-control-" + marker
        with local.private_directory() as (directory, env):
            container = local.docker(["create", "--network=none", "--name", container_name,
                                     local.PIN["runner"], "/bin/true"], env, directory).stdout.strip()
            try:
                with patch.object(local, "ROOT", self.root), patch.object(local, "OWNED", self.owned):
                    with self.assertRaisesRegex(ValueError, "pre-existing act containers"):
                        local.run("image", "backend")
                self.assertEqual(json.loads(local.docker(["inspect", container], env, directory).stdout)[0]["Id"], container)
            finally:
                local.docker(["rm", container], env, directory)
            local.docker(["volume", "create", "--label", "control-owner=" + marker, volume_name], env, directory)
            try:
                before = json.loads(local.docker(["volume", "inspect", volume_name], env, directory).stdout)
                with patch.object(local, "ROOT", self.root), patch.object(local, "OWNED", self.owned):
                    self.assertNotEqual(local.run("image", "backend"), 0)
                after = json.loads(local.docker(["volume", "inspect", volume_name], env, directory).stdout)
                self.assertEqual(after, before)
            finally:
                local.docker(["volume", "rm", volume_name], env, directory)
            names = local.docker(["volume", "ls", "--format", "{{.Name}}"], env, directory).stdout.splitlines()
            created = "act-toolcache" not in names
            if created:
                local.docker(["volume", "create", "--label", "control-owner=" + marker, "act-toolcache"], env, directory)
            try:
                before = json.loads(local.docker(["volume", "inspect", "act-toolcache"], env, directory).stdout)
                with local.docker_resources(env, directory, "123456789012345678"):
                    pass
                after = json.loads(local.docker(["volume", "inspect", "act-toolcache"], env, directory).stdout)
                self.assertEqual(after, before)
            finally:
                if created:
                    local.docker(["volume", "rm", "act-toolcache"], env, directory)

    def test_actual_sigterm_allows_harness_cleanup_and_records_interruption(self):
        self.actual_source("wait")
        script = "import sys; sys.path.insert(0," + repr(str(local.ROOT / "test/ci")) + "); import local_workflows as l; from pathlib import Path; l.ROOT=Path(" + repr(str(self.root)) + "); l.OWNED=Path(" + repr(str(self.owned)) + "); sys.exit(l.run('image','backend'))"
        process = subprocess.Popen([sys.executable, "-B", "-c", script], stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, start_new_session=True)
        try:
            deadline = time.monotonic() + 25
            while time.monotonic() < deadline:
                paths = list((self.root / "output/artifacts/workflows").glob("*"))
                if paths:
                    run_id = paths[0].name
                    with local.private_directory() as (directory, env):
                        ids = local.docker(["ps", "--filter", "label=" + local.LABEL + "=" + run_id, "--format", "{{.ID}}"], env, directory).stdout.splitlines()
                        ready = False
                        for identifier in ids:
                            data = json.loads(local.docker(["inspect", identifier], env, directory).stdout)[0]
                            ready = any(item.get("Type") == "bind" and Path(item["Source"]).name == "tmp" and (Path(item["Source"]) / "ready").is_file() for item in data.get("Mounts", []))
                            if ready:
                                break
                        if ready:
                            break
                if process.poll() is not None:
                    self.fail("runtime exited before the interruption control was ready")
                time.sleep(0.1)
            else:
                self.fail("interruption control did not become ready")
            process.send_signal(signal.SIGTERM)
            stdout, stderr = process.communicate(timeout=60)
            self.assertNotEqual(process.returncode, 0, stdout + stderr)
        finally:
            if process.poll() is None:
                os.killpg(process.pid, signal.SIGKILL)
                process.communicate(timeout=10)
        result = json.loads(next((self.root / "output/artifacts/workflows").glob("*/run.json")).read_text())
        self.assertEqual(result["execution"]["status"], "interrupted")
        self.assertEqual(result["cleanup"], "passed")
        summary = self.root / "output/artifacts/workflows" / result["runId"] / next(item["path"] for item in result["files"] if item["path"].endswith("summary.txt"))
        self.assertIn("harness cleanup complete", summary.read_text())


class ProcessDeadlineFixtures(unittest.TestCase):
    def test_actual_process_timeout_and_exit_status(self):
        with local.private_directory() as (directory, env):
            self.assertEqual(local.execute([sys.executable, "-c", "raise SystemExit(17)"], env, directory, 5),
                             {"status": "failed", "exitCode": 17})
            started = time.monotonic()
            result = local.execute([sys.executable, "-c", "import signal,sys,time; signal.signal(signal.SIGINT, lambda *_: sys.exit(130)); time.sleep(30)"], env, directory, .1)
            self.assertEqual(result["status"], "timed_out")
            self.assertLess(time.monotonic() - started, 35)
