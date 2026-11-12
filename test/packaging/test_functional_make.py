import ast
import importlib.util
from pathlib import Path
import json
import os
import re
import shlex
import subprocess
import shutil
import sys
import tempfile
import unittest
import make_test_helpers as helpers
from make_test_helpers import ROOT


class MakeScopeTests(helpers.MakeScopeTestsFixture, unittest.TestCase):
    def test_container_fixture_builds_override_the_host_operating_system(self):
        """Execute the real recipes with a compiler double for both container CPUs."""
        source = (ROOT / "Makefile").read_text().splitlines()
        recipes = []
        for output in ('"$$fixture_directory/demo-archives"', '"$$fixture_directory/publication-report/receiver"'):
            selected = [line.strip().removesuffix("\\").rstrip() for line in source if "$(GO)" in line and " -o " + output in line]
            self.assertEqual(len(selected), 1)
            recipes.append(selected[0])
        with tempfile.TemporaryDirectory(prefix="catalog-linux-fixtures-") as directory:
            root = Path(directory)
            (root / "go.py").write_text('''import json, os, sys
with open("builds.jsonl", "a") as output:
 output.write(json.dumps({"args": sys.argv[1:], "os": os.getenv("GOOS"), "arch": os.getenv("GOARCH"), "cgo": os.getenv("CGO_ENABLED")}) + "\\n")
''')
            makefile = "GO := " + shlex.quote(sys.executable) + " go.py\nprobe:\n"
            for recipe in recipes:
                makefile += "\t@set -eu; fixture_directory='fixture inputs'; peer_overlay=selected; backup_overlay=; " + recipe + "\n"
            (root / "Makefile").write_text(makefile)
            environment = {key: value for key, value in os.environ.items() if key not in {"MAKEFLAGS", "MFLAGS"}}
            environment.update(GOOS="darwin", GOARCH="host-value", CGO_ENABLED="1")
            for architecture in ("amd64", "arm64"):
                with self.subTest(architecture=architecture):
                    (root / "builds.jsonl").unlink(missing_ok=True)
                    result = subprocess.run(["make", "--no-print-directory", "probe", "HOST_GOARCH=" + architecture],
                                            cwd=root, env=environment, capture_output=True, text=True, timeout=10, check=False)
                    self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                    builds = [json.loads(line) for line in (root / "builds.jsonl").read_text().splitlines()]
                    self.assertEqual(len(builds), 2)
                    for build in builds:
                        self.assertEqual((build["os"], build["arch"], build["cgo"]), ("linux", architecture, "0"))
                    for build, output in zip(builds, ("fixture inputs/demo-archives", "fixture inputs/publication-report/receiver"), strict=True):
                        self.assertEqual(build["args"][build["args"].index("-o") + 1], output)

    def test_functional_curl_gate_checks_the_active_backend_only(self):
        source = (ROOT / "Makefile").read_text()
        gate = next(line.strip().removesuffix("\\").rstrip() for line in source.splitlines()
                    if line.strip().startswith("if $(CURL) --version"))
        with tempfile.TemporaryDirectory(prefix="catalog-curl-gate-") as directory:
            root = Path(directory)
            curl = root / "curl"
            curl.write_text('#!/bin/sh\nprintf "%s\\n" "$TEST_CURL_VERSION"\n')
            curl.chmod(0o700)
            (root / "Makefile").write_text("CURL := " + shlex.quote(str(curl)) + "\nprobe:\n\t@set -eu; " + gate + "\n\t@printf accepted\n")
            for backend, accepted in (("OpenSSL/3.0.0", True), ("OpenSSL/3.0.0 (SecureTransport)", True),
                                      ("SecureTransport (OpenSSL/3.0.0)", False)):
                with self.subTest(backend=backend):
                    environment = dict(os.environ, TEST_CURL_VERSION="curl 8.0.0 (aarch64-apple-darwin) libcurl/8.0.0 " + backend)
                    environment.pop("MAKEFLAGS", None)
                    environment.pop("MFLAGS", None)
                    result = subprocess.run(["make", "--no-print-directory", "probe"], cwd=root, env=environment,
                                            capture_output=True, text=True, timeout=10, check=False)
                    self.assertEqual(result.returncode == 0, accepted, result.stderr)
                    self.assertEqual(result.stdout, "accepted" if accepted else "")
                    if not accepted:
                        self.assertIn("Functional curl cannot verify the fixture certificate.", result.stderr)

    def test_integration_projects_include_the_ci_run_and_cleanup_the_same_project(self):
        source = (ROOT / "Makefile").read_text().split("define run_postgres_integration\n", 1)[1].split("\nendef", 1)[0]
        with tempfile.TemporaryDirectory(prefix="catalog-integration-project-") as directory:
            root = Path(directory)
            (root / "Makefile").write_text("COMPOSE := " + shlex.quote(sys.executable) + " compose.py\nprobe:\n" + source.replace("$(1)", "nested") + "\nnested:\n\t@printf done > nested-ran\n")
            (root / "compose.py").write_text('''import json, pathlib, sys
with pathlib.Path("commands.jsonl").open("a") as output:
 output.write(json.dumps(sys.argv[1:])+"\\n")
if "port" in sys.argv:
 print("127.0.0.1:54321")
''')
            environment = {key: value for key, value in os.environ.items() if key not in {"MAKEFLAGS", "MFLAGS", "GITHUB_RUN_ID"}}
            for run_id in (None, "123456789012345678", "223456789012345678"):
                with self.subTest(run_id=run_id):
                    (root / "commands.jsonl").unlink(missing_ok=True)
                    (root / "nested-ran").unlink(missing_ok=True)
                    env = dict(environment, **({"GITHUB_RUN_ID": run_id} if run_id else {}))
                    result = subprocess.run(["make", "--no-print-directory", "probe"], cwd=root, env=env,
                                            capture_output=True, text=True, timeout=15, check=False)
                    self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                    commands = [json.loads(line) for line in (root / "commands.jsonl").read_text().splitlines()]
                    self.assertEqual(len(commands), 3)
                    projects = [command[command.index("-p") + 1] for command in commands]
                    self.assertEqual(len(set(projects)), 1)
                    prefix = "open-agentic-catalog-integration-" + (run_id + "-" if run_id else "")
                    self.assertRegex(projects[0], "^" + prefix + r"[0-9]+$")
                    self.assertIn("up", commands[0])
                    self.assertIn("port", commands[1])
                    self.assertIn("down", commands[2])
                    self.assertTrue((root / "nested-ran").is_file())

    def test_help_groups_every_public_target_without_starting_workflows(self):
        """Default and explicit help stay complete, readable and service-free."""
        source = (ROOT / "Makefile").read_text()
        groups, expected = [], []
        group = None
        for line in source.splitlines():
            if line.startswith("##@ "):
                group = line[4:]
                groups.append(group)
            match = re.match(r"^([a-zA-Z0-9_-]+):.*?##\s*(.+)$", line)
            if match:
                self.assertIsNotNone(group, "public target has no help section")
                expected.append((group, match[1], match[2]))
        self.assertTrue(groups)
        self.assertEqual(len(groups), len(set(groups)))
        self.assertEqual(set(groups), {item[0] for item in expected})
        names = [item[1] for item in expected]
        self.assertEqual(len(names), len(set(names)))
        self.assertNotIn("test-functional-workflow", names)

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            # Guard every workflow before its real recipe could run. This also
            # catches an accidental prerequisite added to help in the future.
            guarded = [name for name in names if name != "help"] + ["test-functional-workflow"]
            guard = "\n" + " ".join(guarded) + ": help-workflow-guard\n"
            guard += ".PHONY: help-workflow-guard\nhelp-workflow-guard:\n\t@touch workflow-started\n\t@exit 91\n"
            (root / "Makefile").write_text(source + guard + "\n##@ Empty fixture section\n")
            (root / "go.mod").write_bytes((ROOT / "go.mod").read_bytes())
            # Includes and early target-specific exports must not steal the
            # default goal when the source is rearranged.
            (root / "workflow.env").write_text(".DEFAULT_GOAL := help-workflow-guard\n")
            # Help has no timestamp dependency. Fixed input mtimes prevent wall
            # clock adjustments from adding unrelated Make skew warnings.
            for name in ("Makefile", "workflow.env"):
                os.utime(root / name, (1, 1))
            environment = {key: value for key, value in os.environ.items() if key not in ("MAKEFLAGS", "MFLAGS")}
            outputs = []
            for goals in ([], ["help"]):
                result = subprocess.run(
                    ["make", "--no-print-directory", "CATALOG_ENV_FILE=workflow.env",
                     "GO=/unavailable/go", "PYTHON=/unavailable/python", "COMPOSE=/unavailable/compose",
                     "CONTAINER_ENGINE=/unavailable/engine", *goals],
                    cwd=root, env=environment, capture_output=True, text=True, timeout=10, check=False,
                )
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stderr, "")
                self.assertFalse((root / "workflow-started").exists())
                self.assertNotIn("\x1b", result.stdout)
                self.assertNotIn("Empty fixture section", result.stdout)
                self.assertNotIn("test-functional-workflow", result.stdout)
                self.assertIn("Usage: make <target>", result.stdout)
                actual, actual_groups = [], []
                group = None
                for line in result.stdout.splitlines():
                    if line in groups:
                        group = line
                        actual_groups.append(group)
                    match = re.match(r"^\s+([a-zA-Z0-9_-]+)\s+(.+)$", line)
                    if match:
                        actual.append((group, match[1], match[2]))
                self.assertEqual(actual_groups, groups)
                self.assertEqual(actual, expected)
                outputs.append(result.stdout)
            self.assertEqual(outputs[0], outputs[1])

    def test_demo_inputs_are_scoped_and_resolve_defaults(self):
        names = ("DEMO_DATA_FILE", "DEMO_SAMPLE_SET", "DEMO_BASE_URL", "DEMO_ARCHIVE_ORIGIN", "DEMO_ARCHIVE_TRUST_FILE")
        environment = {key: value for key, value in os.environ.items() if key not in names}
        probe = 'demo-seed:\n\t@printf "%s|%s|%s|%s|%s\\n" "$$DEMO_DATA_FILE" "$$DEMO_SAMPLE_SET" "$$DEMO_BASE_URL" "$$DEMO_ARCHIVE_ORIGIN" "$$DEMO_ARCHIVE_TRUST_FILE"\n'
        result = self.run_make("-s", "-f", "Makefile", "-f", "-", "demo-seed", env=environment, input=probe)
        self.assertEqual(result.strip(), "samples/catalog.yaml|curated|http://127.0.0.1:8080|https://172.30.255.41:8443|output/demo/archives/cert.pem")
        args = ["DEMO_DATA_FILE=custom/catalog.yaml", "DEMO_SAMPLE_SET=additional", "CATALOG_COMPOSE_HTTP_PORT=18089", "DEMO_ARCHIVE_IP=172.30.253.41", "DEMO_ARCHIVE_DIR=output/custom"]
        result = self.run_make("-s", "-f", "Makefile", "-f", "-", "demo-seed", *args, env=environment, input=probe)
        self.assertEqual(result.strip(), "custom/catalog.yaml|additional|http://127.0.0.1:18089|https://172.30.253.41:8443|output/custom/cert.pem")
        unrelated = self.run_make("-s", "-f", "Makefile", "-f", "-", "demo-unrelated", *args, env=environment, input=probe.replace("demo-seed:", "demo-unrelated:"))
        self.assertEqual(unrelated.strip(), "||||")

    def test_demo_maintenance_retains_explicit_subnet_without_runtime_env(self):
        environment = {key: value for key, value in os.environ.items() if key != "CATALOG_COMPOSE_SUBNET"}
        for target in ("compose-stop", "compose-down", "compose-reset", "compose-ps", "compose-logs"):
            probe = target + ':\n\t@printf "%s|%s|%s\\n" "$$CATALOG_COMPOSE_SUBNET" "$$CATALOG_COMPOSE_FEDERATION_CREDENTIALS_DIRECTORY" "$$CATALOG_DATABASE_DSN"\n'
            result = self.run_make("-s", "-f", "Makefile", "-f", "-", target,
                                   "CATALOG_COMPOSE_SUBNET=172.30.253.0/24",
                                   "CATALOG_COMPOSE_FEDERATION_CREDENTIALS_DIRECTORY=/private/bindings",
                                   "CATALOG_DATABASE_DSN=private-runtime-sentinel",
                                   env=environment, input=probe)
            self.assertEqual(result.strip(), "172.30.253.0/24||")
        result = self.run_make("-s", "-f", "Makefile", "-f", "-", "scope-probe",
                               "CATALOG_COMPOSE_SUBNET=172.30.253.0/24", env=environment,
                               input='scope-probe:\n\t@printf "%s\\n" "$$CATALOG_COMPOSE_SUBNET"\n')
        self.assertEqual(result.strip(), "")

    def test_verification_environment_is_runtime_scoped(self):
        names = ("CATALOG_TRUST_VERIFICATION", "CATALOG_COMPOSE_TRUST_VERIFICATION_DIRECTORY")
        environment = {key: value for key, value in os.environ.items() if key not in names}
        for target in ("run", "config-check", "diagnostics", "compose-up", "verification-unrelated"):
            probe = target + ':\n\t@printf "%s|%s\\n" "$$CATALOG_TRUST_VERIFICATION" "$$CATALOG_COMPOSE_TRUST_VERIFICATION_DIRECTORY"\n'
            result = self.run_make("-s", "-f", "Makefile", "-f", "-", target,
                                   'CATALOG_TRUST_VERIFICATION={"enabled":false}',
                                   "CATALOG_COMPOSE_TRUST_VERIFICATION_DIRECTORY=/public/keys",
                                   env=environment, input=probe)
            expected = '{"enabled":false}' if target != "verification-unrelated" else ""
            directory = "/public/keys" if target == "compose-up" else ""
            self.assertEqual(result.strip(), expected + "|" + directory)

    def test_catalog_demo_gate_is_explicit_alone_and_env_free(self):
        with tempfile.TemporaryDirectory() as directory:
            env_file = Path(directory) / "workflow.env"
            env_file.write_text("$(error Demo evaluated inherited workflow settings)\n")
            result = self.run_make("-n", "test-demo-catalog", "CATALOG_ENV_FILE=" + str(env_file))
            self.assertIn("test/demo/qualify_catalog.py", result)
            self.assertNotIn("tools-install", result)
            mixed = subprocess.run(["make", "-n", "test-demo-catalog", "check", "CATALOG_ENV_FILE=" + str(env_file)],
                                   cwd=ROOT, capture_output=True, text=True, timeout=30, check=False)
            self.assertNotEqual(mixed.returncode, 0)
            self.assertIn("Interoperability targets must run alone", mixed.stderr)
            self.assertNotIn("Demo evaluated", mixed.stderr)

    def test_demo_up_orders_stages_and_stops_before_next_on_failure(self):
        # Actual recursive Make with inert fixture recipes: no Docker/API calls.
        source = (ROOT / "Makefile").read_text()
        stages = ("demo-check", "demo-archives-prepare", "compose-up", "demo-seed")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "go.mod").write_bytes((ROOT / "go.mod").read_bytes())
            (root / "VERSION").write_bytes((ROOT / "VERSION").read_bytes())
            helper = root / "test/packaging"
            helper.mkdir(parents=True)
            (helper / "versioning.py").write_bytes((ROOT / "test/packaging/versioning.py").read_bytes())
            tool = root / "ready"
            tool.write_text("#!/bin/sh\nexit 0\n")
            tool.chmod(0o700)
            for failed in (None, *stages):
                marker = root / "stages"
                marker.unlink(missing_ok=True)
                overrides = ""
                for stage in stages:
                    overrides += f"\n{stage}:\n\t@echo {stage} >> stages\n"
                    if stage == failed:
                        overrides += "\t@exit 91\n"
                (root / "Makefile").write_text(source + overrides)
                result = subprocess.run(["make", "-s", "CATALOG_ENV_FILE=/dev/null", "demo-up", "CURL=" + str(tool)], cwd=root, capture_output=True, text=True, timeout=30, check=False)
                expected = stages if failed is None else stages[:stages.index(failed) + 1]
                self.assertEqual(marker.read_text().splitlines(), list(expected))
                self.assertEqual(result.returncode == 0, failed is None, result.stderr)
            marker.unlink()
            result = subprocess.run(["make", "-s", "CATALOG_ENV_FILE=/dev/null", "demo-up", "DEMO_BASE_URL=http://127.0.0.1:12345"], cwd=root, capture_output=True, text=True, timeout=10, check=False)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(marker.exists(), "mismatched API target started workflow")

    @staticmethod
    def demo_up_inert_fixture(root):
        """Replace child recipes, never their real recursive Make boundary."""
        for name in ("go.mod", "VERSION"):
            (root / name).write_bytes((ROOT / name).read_bytes())
        helper = root / "test/packaging"
        helper.mkdir(parents=True)
        (helper / "versioning.py").write_bytes((ROOT / "test/packaging/versioning.py").read_bytes())
        # These literal names are independent of the implementation allowlist.
        names = ("CATALOG_ENV_FILE", "COMPOSE_PROJECT", "CATALOG_COMPOSE_HTTP_PORT", "CATALOG_COMPOSE_POSTGRES_PORT",
                 "CATALOG_COMPOSE_SUBNET", "CATALOG_UI_PORT", "IMAGE", "UI_IMAGE", "DEMO_ARCHIVE_DIR", "DEMO_ARCHIVE_ADDRESS",
                 "DEMO_ARCHIVE_IP", "DEMO_ARCHIVE_PORT", "DEMO_ARCHIVE_TRUST_FILE", "DEMO_ARCHIVE_ORIGIN", "DEMO_DATA_FILE",
                 "DEMO_SAMPLE_SET", "DEMO_STATE_FILE", "DEMO_BASE_URL", "DEMO_EXTEND", "DEMO_TIMEOUT", "LOCAL_UI",
                 "LOCAL_DEMO_ARCHIVES", "LOCAL_DEMO_RUNTIMES", "DEMO_RUNTIME_DIR", "DEMO_A2A_PORT", "DEMO_MCP_PORT",
                 "LOCAL_MONITORING", "LOCAL_EMBEDDINGS", "LOCAL_WEBHOOKS", "EMBEDDING_PROFILE", "CATALOG_UI_LOG_LEVEL",
                 "CATALOG_UI_LOG_FORMAT", "CATALOG_UI_DOCUMENTATION_URL", "CATALOG_MONITORING_PROMETHEUS_PORT",
                 "CATALOG_MONITORING_GRAFANA_PORT", "CATALOG_MONITORING_RETENTION_HOURS", "CATALOG_MONITORING_RETENTION_MB")
        stages = ("demo-check", "demo-archives-prepare", "compose-up", "demo-seed")
        source = (ROOT / "Makefile").read_text()
        for name in names:
            source += "\n" + " ".join(stages) + ": private export FIXTURE_" + name + " := $(" + name + ")\n"
        for stage in stages:
            source += "\n" + stage + ":\n\t@python3 record.py " + stage + "\n"
        source += "\ndemo-unrelated:\n\t@python3 record.py demo-unrelated\n"
        (root / "Makefile").write_text(source)
        (root / ".env").write_text("$(error The default workflow file must not be loaded)\n")
        recorder = '''#!/usr/bin/env python3
import json
import os
from pathlib import Path
import sys
stage = "readiness" if Path(sys.argv[0]).name in ("ready", "curl") else sys.argv[1]
runtime = ("CATALOG_DATABASE_DSN", "CATALOG_WEBHOOK_ENCRYPTION_KEY", "CATALOG_SEMANTIC_SEARCH",
           "CATALOG_COMPOSE_FEDERATION_CREDENTIALS_DIRECTORY", "CATALOG_COMPOSE_FEDERATION_TRUST_DIRECTORY",
           "CATALOG_COMPOSE_FEDERATION_POLICY_FILE", "CATALOG_COMPOSE_WEBHOOK_DIRECTORY",
           "CATALOG_COMPOSE_TRUST_VERIFICATION_DIRECTORY", "CATALOG_TRUST_VERIFICATION",
           "CATALOG_ENVIRONMENT", "CATALOG_AUTH_MODE", "CATALOG_WEBHOOKS_ENABLED", "CATALOG_DATABASE_DSN_FILE",
           "CATALOG_CURSOR_SIGNING_KEY", "CATALOG_CURSOR_SIGNING_KEY_FILE", "CATALOG_TRACING", "CATALOG_LOG_LEVEL", "CATALOG_PROFILE")
row = {"stage": stage, "effective": {name[8:]: value for name, value in os.environ.items() if name.startswith("FIXTURE_")},
       "runtime": {name: os.environ.get(name) for name in runtime},
       "composeInputs": {name: os.environ.get(name) for name in ("CATALOG_COMPOSE_HTTP_PORT", "CATALOG_COMPOSE_POSTGRES_PORT", "CATALOG_COMPOSE_SUBNET")},
       "buildMetadata": {name: os.environ.get(name) for name in ("VERSION", "COMMIT", "SOURCE_STATE")},
       "workflowSecret": os.environ.get("PRIVATE_WORKFLOW_SENTINEL"),
       "flagsContainRuntime": any(os.environ.get(name) and os.environ[name] in os.environ.get("MAKEFLAGS", "") for name in runtime)}
if stage == "readiness":
    row["url"] = sys.argv[-1]
    row["probeArguments"] = sys.argv[1:-1]
with open("stages.jsonl", "a", encoding="utf-8") as output:
    output.write(json.dumps(row) + "\\n")
if Path("fail-" + stage).exists():
    raise SystemExit(91)
'''
        (root / "record.py").write_text(recorder)
        (root / "ready").write_text(recorder)
        (root / "ready").chmod(0o700)
        (root / "curl").write_text(recorder)
        (root / "curl").chmod(0o700)
        return names

    @staticmethod
    def demo_up_environment(root):
        environment = {name: value for name, value in os.environ.items()
                       if not name.startswith(("CATALOG_", "DEMO_", "LOCAL_", "FIXTURE_"))
                       and name not in ("MAKEFLAGS", "MFLAGS", "MAKEOVERRIDES", "IMAGE", "UI_IMAGE", "EMBEDDING_PROFILE", "VERSION", "COMMIT", "SOURCE_STATE")}
        environment.update(CATALOG_ENV_FILE="/dev/null", COMPOSE_PROJECT="owned-environment", IMAGE="backend:owned-env",
                           UI_IMAGE="ui:owned-env", CATALOG_COMPOSE_HTTP_PORT="18321", CATALOG_COMPOSE_POSTGRES_PORT="0",
                           CATALOG_COMPOSE_SUBNET="172.30.230.0/24", CATALOG_UI_PORT="18322", DEMO_ARCHIVE_IP="172.30.230.41",
                           DEMO_ARCHIVE_PORT="18521", DEMO_ARCHIVE_ADDRESS="127.0.0.1:18521",
                           DEMO_ARCHIVE_ORIGIN="https://172.30.230.41:8443", DEMO_BASE_URL="http://127.0.0.1:18321",
                           DEMO_SAMPLE_SET="minimal", DEMO_EXTEND="true", DEMO_TIMEOUT="17m", LOCAL_UI="off",
                           LOCAL_DEMO_ARCHIVES="off", LOCAL_DEMO_RUNTIMES="on", DEMO_A2A_PORT="19543", DEMO_MCP_PORT="19544",
                           LOCAL_MONITORING="on", LOCAL_EMBEDDINGS="on", LOCAL_WEBHOOKS="off", EMBEDDING_PROFILE="bge-small-v1",
                           CATALOG_UI_LOG_LEVEL="debug", CATALOG_UI_LOG_FORMAT="json", CATALOG_UI_DOCUMENTATION_URL="https://manual.example.test/",
                           CATALOG_MONITORING_PROMETHEUS_PORT="19094", CATALOG_MONITORING_GRAFANA_PORT="13004",
                           CATALOG_MONITORING_RETENTION_HOURS="8", CATALOG_MONITORING_RETENTION_MB="256", PATH=str(root) + os.pathsep + os.environ["PATH"],
                           VERSION="2.3.4-env", COMMIT="a" * 40, SOURCE_STATE="forged-state-must-not-win")
        literal = "paths with spaces/$cash#'\";`touch shell-expanded`$(shell touch make-expanded)"
        for name in ("DEMO_ARCHIVE_DIR", "DEMO_ARCHIVE_TRUST_FILE", "DEMO_DATA_FILE", "DEMO_STATE_FILE", "DEMO_RUNTIME_DIR"):
            environment[name] = str(root / literal / name.lower())
        environment.update(CATALOG_DATABASE_DSN="private:$cash#'\";`touch shell-expanded`$(shell touch make-expanded)\nsecond line",
                           CATALOG_WEBHOOK_ENCRYPTION_KEY="owned-key-$cash#$(shell touch make-expanded)",
                           CATALOG_SEMANTIC_SEARCH='{"enabled":false,"model":"literal-$cash#$(shell touch make-expanded)"}',
                           CATALOG_COMPOSE_FEDERATION_CREDENTIALS_DIRECTORY=str(root / literal / "credentials"),
                           CATALOG_COMPOSE_FEDERATION_TRUST_DIRECTORY=str(root / literal / "trust"),
                           CATALOG_COMPOSE_FEDERATION_POLICY_FILE=str(root / literal / "policy.json"),
                           CATALOG_COMPOSE_WEBHOOK_DIRECTORY=str(root / literal / "webhooks"),
                           CATALOG_COMPOSE_TRUST_VERIFICATION_DIRECTORY=str(root / literal / "keys"),
                           CATALOG_TRUST_VERIFICATION='{"enabled":false}')
        return environment

    def check_demo_up_selection(self, root, names, environment, expected, *arguments):
        marker = root / "stages.jsonl"
        marker.unlink(missing_ok=True)
        result = subprocess.run(["make", "--no-print-directory", "-s", "demo-up", *arguments], cwd=root, env=environment,
                                capture_output=True, text=True, timeout=30, check=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        rows = [json.loads(line) for line in marker.read_text().splitlines()]
        self.assertEqual([row["stage"] for row in rows], ["demo-check", "demo-archives-prepare", "compose-up", "readiness", "demo-seed"])
        for row in rows:
            stage = row["stage"]
            self.assertFalse(row["flagsContainRuntime"], "environment credentials reached recursive Make arguments")
            self.assertIsNone(row["workflowSecret"], "unrelated workflow input was exported")
            if stage == "readiness":
                self.assertEqual(row["url"], expected["DEMO_BASE_URL"] + "/readyz")
                self.assertEqual(row["probeArguments"], ["--fail", "--silent", "--show-error", "--retry", "30", "--retry-delay", "1",
                                                         "--retry-max-time", "60", "--max-time", "2"])
            else:
                effective = {name: expected[name] for name in names}
                if stage == "compose-up":
                    effective.update(LOCAL_UI="on", LOCAL_DEMO_ARCHIVES="on")
                self.assertEqual(row["effective"], effective, stage)
            for name, value in row["runtime"].items():
                self.assertEqual(value, expected.get(name) if stage == "compose-up" else None, stage + ": " + name)
            self.assertEqual(row["buildMetadata"], {"VERSION": expected["VERSION"], "COMMIT": expected["COMMIT"], "SOURCE_STATE": "unknown"}
                             if stage == "compose-up" else {"VERSION": None, "COMMIT": None, "SOURCE_STATE": None}, stage)
        for name in ("make-expanded", "shell-expanded"):
            self.assertFalse((root / name).exists(), "literal launcher input was executed")
        self.assertNotIn(expected["CATALOG_DATABASE_DSN"], result.stdout + result.stderr)

    def test_demo_up_preserves_environment_literals_and_optional_service_inputs(self):
        with tempfile.TemporaryDirectory(prefix="demo-up environment ") as directory:
            root = Path(directory)
            names = self.demo_up_inert_fixture(root)
            environment = self.demo_up_environment(root)
            self.check_demo_up_selection(root, names, environment, environment)
            marker = root / "stages.jsonl"
            marker.unlink()
            result = subprocess.run(["make", "--no-print-directory", "-s", "demo-unrelated"], cwd=root, env=environment,
                                    capture_output=True, text=True, timeout=10, check=False)
            self.assertEqual(result.returncode, 0, result.stderr)
            unrelated = json.loads(marker.read_text())
            self.assertTrue(all(value is None for value in unrelated["runtime"].values()))

    def test_demo_up_keeps_absent_runtime_and_compose_inputs_absent(self):
        with tempfile.TemporaryDirectory(prefix="demo-up absent inputs ") as directory:
            root = Path(directory)
            self.demo_up_inert_fixture(root)
            environment = self.demo_up_environment(root)
            for name in tuple(environment):
                if name.startswith("CATALOG_") and name not in ("CATALOG_ENV_FILE", "CATALOG_UI_PORT"):
                    environment.pop(name)
            environment["DEMO_BASE_URL"] = "http://127.0.0.1:8080"
            marker = root / "stages.jsonl"
            for explicit_empty in ({}, {"CATALOG_ENVIRONMENT": "", "CATALOG_AUTH_MODE": "", "CATALOG_COMPOSE_SUBNET": ""}):
                with self.subTest(explicitEmpty=bool(explicit_empty)):
                    marker.unlink(missing_ok=True)
                    result = subprocess.run(["make", "--no-print-directory", "-s", "demo-up"], cwd=root,
                                            env={**environment, **explicit_empty}, capture_output=True, text=True, timeout=30, check=False)
                    self.assertEqual(result.returncode, 0, result.stderr)
                    rows = [json.loads(line) for line in marker.read_text().splitlines()]
                    self.assertEqual([row["stage"] for row in rows], ["demo-check", "demo-archives-prepare", "compose-up", "readiness", "demo-seed"])
                    for row in rows:
                        for name, value in row["runtime"].items():
                            self.assertEqual(value, explicit_empty.get(name) if row["stage"] == "compose-up" else None, name)
                        if row["stage"] == "compose-up":
                            self.assertEqual(row["composeInputs"], {"CATALOG_COMPOSE_HTTP_PORT": None, "CATALOG_COMPOSE_POSTGRES_PORT": None,
                                                                   "CATALOG_COMPOSE_SUBNET": explicit_empty.get("CATALOG_COMPOSE_SUBNET")})

    def test_demo_up_preserves_selected_workflow_with_spaces_and_input_precedence(self):
        with tempfile.TemporaryDirectory(prefix="demo-up workflow ") as directory:
            root = Path(directory)
            names = self.demo_up_inert_fixture(root)
            environment = self.demo_up_environment(root)
            file_values = {"COMPOSE_PROJECT": "owned-workflow", "CATALOG_COMPOSE_HTTP_PORT": "18331",
                           "DEMO_BASE_URL": "http://127.0.0.1:18331", "DEMO_DATA_FILE": str(root / "file selected/catalog.yaml"),
                           "DEMO_EXTEND": "false", "CATALOG_DATABASE_DSN": "workflow-secret-$cash#literal", "VERSION": "2.3.5-file", "COMMIT": "b" * 40}
            for filename in ("workflow with spaces $cash # selected.env", "workflow\tselected.env", "workflow\nselected.env",
                             "workflow\\selected.env", "workflow]selected.env"):
                with self.subTest(filename=filename):
                    workflow = root / filename
                    workflow.write_text("".join(name + "=" + value.replace("$", "$$").replace("#", "\\#") + "\n" for name, value in file_values.items()))
                    with workflow.open("a") as output:
                        output.write("PRIVATE_WORKFLOW_SENTINEL=must-not-escape-workflow\n")
                    environment["CATALOG_ENV_FILE"] = str(workflow)
                    expected = {**environment, **file_values}
                    self.check_demo_up_selection(root, names, environment, expected)
            overrides = {"COMPOSE_PROJECT": "owned-command-line", "CATALOG_COMPOSE_HTTP_PORT": "18341",
                         "DEMO_BASE_URL": "http://127.0.0.1:18341", "DEMO_DATA_FILE": str(root / "CLI selected/catalog.yaml"), "DEMO_EXTEND": "true",
                         "VERSION": "2.3.6-cli", "COMMIT": "c" * 40}
            self.check_demo_up_selection(root, names, environment, {**expected, **overrides}, *(name + "=" + value for name, value in overrides.items()))

    def test_demo_up_refuses_ambiguous_workflow_before_any_child(self):
        with tempfile.TemporaryDirectory(prefix="demo-up workflow refusals ") as directory:
            root = Path(directory)
            self.demo_up_inert_fixture(root)
            environment = self.demo_up_environment(root)
            marker = root / "stages.jsonl"
            for filename in ("workflow\\ with space.env", "workflow\\\tselected.env"):
                workflow = root / filename
                workflow.write_text("COMPOSE_PROJECT=exact-selected-workflow\n")
                with self.subTest(filename=filename):
                    result = subprocess.run(["make", "--no-print-directory", "-s", "demo-up"], cwd=root,
                                            env={**environment, "CATALOG_ENV_FILE": str(workflow)},
                                            capture_output=True, text=True, timeout=10, check=False)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn("backslash before whitespace", result.stderr)
                    self.assertFalse(marker.exists(), "ambiguous workflow selection started a child")

    def test_demo_up_refuses_wildcard_workflow_before_include_or_child_work(self):
        """An existing literal file must not silently select another matching file."""
        cases = (("workflow*.env", "workflowz.env"), ("workflow?.env", "workflowz.env"),
                 ("workflow[1].env", "workflow1.env"), ("selected*/workflow.env", "selectedz/workflow.env"),
                 ("selected?/workflow.env", "selectedz/workflow.env"), ("selected[1]/workflow.env", "selected1/workflow.env"))
        for selected_name, matching_name in cases:
            for goal in ("demo-up", "compose-ps"):
                with self.subTest(selected=selected_name, goal=goal), tempfile.TemporaryDirectory(prefix="demo-up wildcard workflow ") as directory:
                    root = Path(directory)
                    self.demo_up_inert_fixture(root)
                    with (root / "Makefile").open("a") as output:
                        output.write("\ncompose-ps:\n\t@python3 record.py compose-ps\n")
                    environment = self.demo_up_environment(root)
                    selected, matching = root / selected_name, root / matching_name
                    selected.parent.mkdir(parents=True, exist_ok=True)
                    matching.parent.mkdir(parents=True, exist_ok=True)
                    selected.write_text("COMPOSE_PROJECT=owned-selected-literal\n")
                    matching.write_text("$(file >unselected-workflow-expanded,wrong workflow was included)\n"
                                        "COMPOSE_PROJECT=owned-unselected-match\n")
                    self.assertTrue(selected.is_file())
                    result = subprocess.run(["make", "--no-print-directory", "-s", goal], cwd=root,
                                            env={**environment, "CATALOG_ENV_FILE": str(selected)},
                                            capture_output=True, text=True, timeout=10, check=False)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn("A workflow path cannot contain wildcard characters: * ? [", result.stderr)
                    self.assertFalse((root / "unselected-workflow-expanded").exists(), "refusal happened after another workflow was evaluated")
                    self.assertFalse((root / "stages.jsonl").exists(), "wildcard workflow selection started a child")
                    self.assertNotIn(environment["CATALOG_DATABASE_DSN"], result.stdout + result.stderr)

    def test_non_demo_goal_keeps_an_exact_regular_workflow_file(self):
        """Environment-selected filenames remain literal outside the demo goals."""
        cases = (("exact workflow]name.env", None), ("exact workflow\\selected.env", None),
                 ("exact workflow $cash # selected.env", "exact workflow ash # selected.env"),
                 ("workflow$(cash).env", "workflow.env"), ("selected$cash/workflow.env", "selectedash/workflow.env"))
        for selected_name, matching_name in cases:
            with self.subTest(selected=selected_name), tempfile.TemporaryDirectory(prefix="exact non-demo workflow ") as directory:
                root = Path(directory)
                self.demo_up_inert_fixture(root)
                workflow = root / selected_name
                workflow.parent.mkdir(parents=True, exist_ok=True)
                workflow.write_text("COMPOSE_PROJECT=owned-exact-regular-workflow\n")
                if matching_name:
                    matching = root / matching_name
                    matching.parent.mkdir(parents=True, exist_ok=True)
                    matching.write_text("$(file >unselected-workflow-expanded,wrong workflow was included)\n"
                                        "COMPOSE_PROJECT=owned-unselected-match\n")
                with (root / "Makefile").open("a") as output:
                    output.write("\ncompose-ps:\n\t@printf '%s' \"$$CATALOG_LOCAL_PROJECT\" > selected-project\n")
                environment = {key: value for key, value in os.environ.items() if key not in ("MAKEFLAGS", "MFLAGS", "MAKEOVERRIDES")}
                environment.update(CATALOG_ENV_FILE=str(workflow), COMPOSE_PROJECT="owned-environment")
                result = subprocess.run(["make", "--no-print-directory", "-s", "compose-ps"],
                                        cwd=root, env=environment, capture_output=True, text=True, timeout=10, check=False)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual((root / "selected-project").read_text(), "owned-exact-regular-workflow")
                self.assertFalse((root / "unselected-workflow-expanded").exists(), "literal workflow filename selected another file")

    @unittest.skipIf(os.getuid() == 0, "Root bypasses workflow read permission bits")
    def test_demo_up_refuses_unreadable_selected_and_default_workflow_before_any_child(self):
        with tempfile.TemporaryDirectory(prefix="demo-up unreadable workflow ") as directory:
            root = Path(directory)
            names = self.demo_up_inert_fixture(root)
            environment = self.demo_up_environment(root)
            marker = root / "stages.jsonl"
            for filename in ("unreadable selected.env", ".env"):
                workflow = root / filename
                workflow.write_text("COMPOSE_PROJECT=exact-selected-workflow\n")
                workflow.chmod(0)
                try:
                    with self.subTest(filename=filename):
                        result = subprocess.run(["make", "--no-print-directory", "-s", "demo-up"], cwd=root,
                                                env={**environment, "CATALOG_ENV_FILE": filename},
                                                capture_output=True, text=True, timeout=10, check=False)
                        self.assertNotEqual(result.returncode, 0)
                        self.assertIn("unreadable", result.stderr)
                        self.assertFalse(marker.exists(), "unreadable workflow selection started a child")
                finally:
                    workflow.chmod(0o600)
            (root / ".env").unlink()
            environment["CATALOG_ENV_FILE"] = ".env"
            self.check_demo_up_selection(root, names, environment, environment)

    def test_demo_up_refuses_mismatches_and_stops_after_failed_readiness(self):
        with tempfile.TemporaryDirectory(prefix="demo-up refusals ") as directory:
            root = Path(directory)
            self.demo_up_inert_fixture(root)
            environment = self.demo_up_environment(root)
            marker = root / "stages.jsonl"
            for changed in ({"DEMO_BASE_URL": "http://127.0.0.1:18399"}, {"DEMO_ARCHIVE_ORIGIN": "https://172.30.230.99:8443"},
                            {"CATALOG_COMPOSE_HTTP_PORT": "0", "DEMO_BASE_URL": "http://127.0.0.1:0"},
                            {"CATALOG_ENV_FILE": str(root / "missing selected workflow.env")}):
                result = subprocess.run(["make", "--no-print-directory", "-s", "demo-up"], cwd=root, env={**environment, **changed},
                                        capture_output=True, text=True, timeout=10, check=False)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(marker.exists(), "invalid selection started a child workflow")
            (root / "fail-readiness").touch()
            result = subprocess.run(["make", "--no-print-directory", "-s", "demo-up"], cwd=root, env=environment,
                                    capture_output=True, text=True, timeout=30, check=False)
            self.assertNotEqual(result.returncode, 0)
            rows = [json.loads(line) for line in marker.read_text().splitlines()]
            self.assertEqual([row["stage"] for row in rows], ["demo-check", "demo-archives-prepare", "compose-up", "readiness"])

    def test_compose_captures_default_and_selected_embedding_profile(self):
        """Immediate scoped exports must see defaults before any recipe runs."""
        environment = {key: value for key, value in os.environ.items() if key != "EMBEDDING_PROFILE"}
        for target in ("compose-check", "compose-up"):
            for arguments, expected in (([], "minilm-l6-v1"), (["EMBEDDING_PROFILE=bge-small-v1"], "bge-small-v1")):
                with self.subTest(target=target, expected=expected):
                    output = self.run_make(
                        "-s", "-f", "Makefile", "-f", "-", target, *arguments, env=environment,
                        input=target + ':\n\t@printf "%s\\n" "$$EMBEDDING_PROFILE"\n',
                    )
                    self.assertEqual(output.strip(), expected)

    def test_database_pool_launcher_exports(self):
        names = ("CATALOG_DATABASE_MAX_CONNECTIONS", "CATALOG_DATABASE_MAX_CONN_LIFETIME", "CATALOG_DATABASE_MAX_CONN_IDLE_TIME",
                 "CATALOG_DATABASE_HEALTH_CHECK_PERIOD")
        environment = {key: value for key, value in os.environ.items() if key not in names}
        arguments = [f"{name}={value}" for name, value in zip(names, ("23", "45m", "3m", "15s"), strict=True)]
        for target in ("run", "config-check", "diagnostics", "migrate-plan", "migrate-apply", "migrate-semantic-plan",
                       "migrate-semantic-apply", "repair-idempotency-list", "repair-idempotency-plan",
                       "repair-idempotency-apply", "repair-search-projections", "restore-verify", "compose-up"):
            output = self.run_make("-s", "-f", "Makefile", "-f", "-", "-o", "build", "-o", "ui-build",
                target, *arguments, env=environment,
                input=target + ':\n\t@printf "%s %s %s %s\\n" "$$CATALOG_DATABASE_MAX_CONNECTIONS" "$$CATALOG_DATABASE_MAX_CONN_LIFETIME" "$$CATALOG_DATABASE_MAX_CONN_IDLE_TIME" "$$CATALOG_DATABASE_HEALTH_CHECK_PERIOD"\n')
            self.assertEqual("23 45m 3m 15s", output.strip(), target)
        unrelated = self.run_make("-s", "-f", "Makefile", "-f", "-", "pool-export-probe", *arguments,
            env=environment, input='pool-export-probe:\n\t@printf "%s|%s|%s|%s\\n" "$$CATALOG_DATABASE_MAX_CONNECTIONS" "$$CATALOG_DATABASE_MAX_CONN_LIFETIME" "$$CATALOG_DATABASE_MAX_CONN_IDLE_TIME" "$$CATALOG_DATABASE_HEALTH_CHECK_PERIOD"\n')
        self.assertEqual("|||", unrelated.strip())

    def test_logging_launcher_exports_and_ui_build_identity(self):
        environment = {key: value for key, value in os.environ.items()
                       if key not in ("CATALOG_LOG_LEVEL", "CATALOG_LOG_FORMAT", "CATALOG_UI_LOG_LEVEL", "CATALOG_UI_LOG_FORMAT")}
        for target in ("run", "config-check", "diagnostics", "compose-up"):
            output = self.run_make("-s", "-f", "Makefile", "-f", "-", "-o", "build", "-o", "ui-build", target,
                "CATALOG_LOG_LEVEL=warn", "CATALOG_LOG_FORMAT=json",
                env=environment, input=target + ':\n\t@printf "%s %s\\n" "$$CATALOG_LOG_LEVEL" "$$CATALOG_LOG_FORMAT"\n')
            self.assertEqual("warn json", output.strip())
        for target in ("ui-dev", "compose-up"):
            for arguments, expected in (([], "info text"), (["CATALOG_UI_LOG_LEVEL=error", "CATALOG_UI_LOG_FORMAT=json"], "error json")):
                output = self.run_make("-s", "-f", "Makefile", "-f", "-", "-o", "build", "-o", "ui-build", target, *arguments,
                    env=environment, input=target + ':\n\t@printf "%s %s\\n" "$$CATALOG_UI_LOG_LEVEL" "$$CATALOG_UI_LOG_FORMAT"\n')
                self.assertEqual(expected, output.strip())
        output = self.run_make("-n", "ui-build", "VERSION=1.2.3-fixture", "COMMIT=abc123")
        self.assertIn("-X main.version=1.2.3-fixture", output)
        self.assertIn("-X main.commit=abc123", output)

    def test_ui_documentation_launcher_exports(self):
        environment = {key: value for key, value in os.environ.items() if key not in ("CATALOG_UI_DOCUMENTATION_URL", "UI_DOCUMENTATION_DIRECTORY")}
        for target in ("ui-dev", "compose-up"):
            arguments = ["UI_DOCUMENTATION_DIRECTORY=/owned/manual"] if target == "ui-dev" else []
            output = self.run_make("-s", "-f", "Makefile", "-f", "-", "-o", "ui-build", target,
                "CATALOG_UI_DOCUMENTATION_URL=https://docs.example.test/manual/", *arguments,
                env=environment, input=target + ':\n\t@printf "%s|%s\\n" "$$CATALOG_UI_DOCUMENTATION_URL" "$$UI_DOCUMENTATION_DIRECTORY"\n')
            self.assertEqual("https://docs.example.test/manual/|" + ("/owned/manual" if target == "ui-dev" else ""), output.strip())

    def test_ui_packaging_targets_select_ui_image_without_building(self):
        for target, action in [("ui-container-inspect", "inspect"), ("ui-sbom", "sbom"), ("ui-container-scan", "scan")]:
            output = self.run_make("-n", target, "UI_IMAGE=ui:selected", "IMAGE=backend:unrelated")
            self.assertIn("image.py " + action + " --profile ui", output)
            self.assertNotIn("docker build", output)
            # Inspect target-scoped environment using an additional Make recipe;
            # never execute the real engine or scanner in this fixture.
            output = self.run_make("-s", "-f", "Makefile", "-f", "-", target,
                "UI_IMAGE=ui:selected", "IMAGE=backend:unrelated",
                input=target + ":\n\t@printf '%s\\n' \"$$PACKAGING_IMAGE\" \"$$PACKAGING_PLATFORM\"\n")
            self.assertEqual(output.splitlines(), ["ui:selected", "linux/amd64"])
    def test_backend_scan_and_coverage_install_preserve_explicit_inputs(self):
        output = self.run_make("-n", "container-scan", "IMAGE=backend:selected")
        self.assertIn("image.py scan", output)
        self.assertNotIn("docker build", output)
        output = self.run_make("-s", "-f", "Makefile", "-f", "-", "container-scan", "IMAGE=backend:selected", "UI_IMAGE=ui:other", "CONTAINER_PLATFORM=linux/arm64", "TRIVY=/tools/pinned",
            input='container-scan:\n\t@printf "%s %s %s\\n" "$$PACKAGING_IMAGE" "$$PACKAGING_PLATFORM" "$$PACKAGING_TRIVY"\n')
        self.assertEqual(output.strip(), "backend:selected linux/arm64 /tools/pinned")
        output = self.run_make("-n", "coverage-tools-install")
        self.assertNotIn("@latest", output)
        self.assertIn("-C test/packaging/coverage-tools install -mod=readonly", output)
        self.assertIn("coverage_tools.py", output)

    def test_go_patterns_cover_owned_roots_without_npm_or_artifact_trees(self):
        output = self.run_make(
            "-s", "-f", "Makefile", "-f", "-", "print-source-scope",
            input="print-source-scope:\n\t@printf '%s\\n' $(GO_SOURCE_PACKAGES)\n",
        )
        self.assertEqual(set(output.split()), {
            "./api/...", "./cmd/...", "./examples/...", "./internal/...",
            "./migrations/...", "./schemas/...", "./samples/...", "./test/...",
            "./catalog-ui/cmd/...", "./catalog-ui/internal/...",
        })
        commands = self.run_make("-n", "test", "test-race", "coverage-profile", "lint", "ui-test")
        commands += self.run_make("-n", "test-unit")
        self.assertNotIn(" ./...", commands)
        self.assertNotIn(" ./catalog-ui/...", commands)
        self.assertIn("./examples/...", commands)
        self.assertIn("./catalog-ui/internal/...", commands)
    def test_source_checks_stay_in_their_owning_gates(self):
        api_lint = "npm --prefix test/validation run lint"
        self.assertEqual(self.run_make("-n", "check").count(api_lint), 1)
        self.assertEqual(self.run_make("-n", "api-check", "api-lint").count(api_lint), 1)
        self.assertIn("npm run check", self.run_make("-n", "docs-site-check"))
        for target in ("verify", "test-unit", "coverage-unit", "test-tooling-unit", "docs-check"):
            commands = self.run_make("-n", target)
            self.assertNotIn(api_lint, commands)
            self.assertNotIn("npm run check", commands)
            self.assertNotIn("check:astro", commands)
        for project in ("documentation", "test/validation"):
            script = json.loads((ROOT / project / "package.json").read_text())["scripts"]["test:unit"]
            expected = {
                "documentation": "node ../catalog-ui/scripts/native-test-completion.mjs scripts/*.unit.test.mjs",
                "test/validation": "node --test *.unit.test.mjs",
            }
            self.assertEqual(script, expected[project])

    def test_python_lint_roots_match_the_staged_hook_and_cover_first_party_python(self):
        command = next(line for line in self.run_make("-n", "python-lint").splitlines() if " check " in line)
        roots = shlex.split(command)[shlex.split(command).index("--no-fix") + 1:]
        spec = importlib.util.spec_from_file_location("hook_runner", ROOT / "test/hooks/runner.py")
        runner = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(runner)
        self.assertEqual({root + "/" for root in roots}, set(runner.PYTHON_ROOTS), "hook and python-lint roots differ")
        # Python outside these roots must have another named owner: the SDK gate lints sdk/python.
        owned_elsewhere = ("sdk/python/", "schemas/upstream/", "samples/upstream/", "internal/adapter/testdata/upstream/")
        environment = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
        listed = subprocess.run(["git", "-C", str(ROOT), "ls-files", "--cached", "--others", "--exclude-standard", "-z"],
                                env=environment, check=True, capture_output=True, timeout=30).stdout.split(b"\0")
        unowned = sorted(name for name in map(os.fsdecode, listed)
                         if name.endswith(".py") and not name.startswith(tuple(r + "/" for r in roots) + owned_elsewhere)
                         and (ROOT / name).is_file())
        self.assertEqual(unowned, [], "Python source outside python-lint and named owners")

    def test_container_sources_have_a_container_check_owner_in_make_and_the_hook(self):
        command = next(line for line in self.run_make("-n", "container-check").splitlines() if line.startswith("hadolint "))
        selected = set(shlex.split(command)[1:])
        spec = importlib.util.spec_from_file_location("hook_runner", ROOT / "test/hooks/runner.py")
        runner = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(runner)
        environment = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
        listed = subprocess.run(["git", "-C", str(ROOT), "ls-files", "--cached", "--others", "--exclude-standard", "-z"],
                                env=environment, check=True, capture_output=True, timeout=30).stdout.split(b"\0")
        found = {name for name in map(os.fsdecode, listed) if (ROOT / name).is_file()
                 and re.fullmatch(r"(?:Containerfile|Dockerfile)(?:\.[^/]+)?", Path(name).name)
                 and not name.startswith(("schemas/upstream/", "samples/upstream/", "internal/adapter/testdata/upstream/"))}
        # The compatibility Dockerfile is a byte copy checked by container-sync-check.
        copy = "deployments/container/Dockerfile"
        self.assertIn(copy, found)
        self.assertEqual(found - {copy} - selected, set(), "Containerfile outside make container-check")
        self.assertEqual(selected - found, set(), "container-check names a missing or non-Containerfile path")
        self.assertEqual(set(runner.CONTAINER_FILES), selected, "staged-hook Containerfile list differs from container-check")

    # Shell scripts have one explicit owner list: the Makefile shell-check recipe.
    # Immutable or copied upstream trees are not first-party shell source.
    SHELL_EXCLUDED = ("schemas/upstream/", "samples/upstream/", "internal/adapter/testdata/upstream/")
    SHELL_SHEBANG = re.compile(rb"#!\s*(?:/usr/bin/env\s+(?:-S\s+)?)?(?:\S*/)?(?:ba|da|z|k)?sh(?:\s|$)")

    @classmethod
    def shell_sources(cls, root):
        """List current first-party shell files by extension or shebang, without printing contents."""
        environment = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
        listed = subprocess.run(
            ["git", "-C", str(root), "ls-files", "--cached", "--others", "--exclude-standard", "-z"],
            env=environment, check=True, capture_output=True, timeout=30,
        ).stdout.split(b"\0")
        found = set()
        for raw in listed:
            name = os.fsdecode(raw)
            path = root / name
            if not name or name.startswith(cls.SHELL_EXCLUDED) or path.is_symlink() or not path.is_file():
                continue
            if name.endswith((".sh", ".bash")):
                found.add(name)
                continue
            with path.open("rb") as handle:
                if cls.SHELL_SHEBANG.match(handle.readline(256)):
                    found.add(name)
        return found

    def test_shell_sources_have_a_shell_check_owner_in_make_and_the_hook(self):
        selected = shlex.split(next(line for line in self.run_make("-n", "shell-check").splitlines()
                                    if line.startswith("shellcheck ")))[1:]
        self.assertEqual(len(selected), len(set(selected)))
        sources = self.shell_sources(ROOT)
        self.assertEqual(sources - set(selected), set(), "shell source outside make shell-check")
        self.assertEqual(set(selected) - sources, set(), "shell-check names a non-shell or missing file")
        spec = importlib.util.spec_from_file_location("hook_runner", ROOT / "test/hooks/runner.py")
        runner = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(runner)
        self.assertEqual(set(runner.SHELL_FILES), set(selected), "staged-hook shell list differs from make shell-check")

    def test_shell_inventory_detects_unowned_scripts_and_ignores_excluded_inputs(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            subprocess.run(["git", "init", "-q", directory], check=True)
            (root / ".gitignore").write_text("output/\n")
            files = {
                "scripts/by-extension.sh": "echo ok\n", "scripts/by-shebang": "#!/usr/bin/env bash\n",
                "scripts/posix": "#!/bin/sh\n", "scripts/env-split": "#!/usr/bin/env -S sh -eu\n",
                "scripts/python-tool": "#!/usr/bin/env python3\n", "scripts/shell-named.py": "#!/usr/bin/env python3\n",
                "scripts/shellish": "#!/usr/bin/env shellcheck-wrapper\n",
                "schemas/upstream/vendor.sh": "echo vendored\n", "output/ignored.sh": "echo ignored\n",
            }
            for name, content in files.items():
                (root / name).parent.mkdir(parents=True, exist_ok=True)
                (root / name).write_text(content)
            (root / "scripts/linked.sh").symlink_to(root / "scripts/by-extension.sh")
            (root / "scripts/directory.sh").mkdir()
            self.assertEqual(self.shell_sources(root), {
                "scripts/by-extension.sh", "scripts/by-shebang", "scripts/posix", "scripts/env-split",
            })

    def test_source_gate_failures_stop_later_checks_without_installing_tools(self):
        """Execute maintained Make/shell sequences with owned tool doubles."""
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name in ("Makefile", "go.mod"):
                (root / name).write_bytes((ROOT / name).read_bytes())
            (root / ".env").write_text("$(error API-only goals must not read this)\n")
            tools = root / "bin"
            tools.mkdir()
            # This uses no Node/npm installation; actual analyzer semantics are
            # qualified separately in the owning npm functional fixtures.
            stub = '''#!/usr/bin/env python3
import json
import os
from pathlib import Path
import sys
call = [Path(sys.argv[0]).name, *sys.argv[1:]]
with open(os.environ["CALL_LOG"], "a", encoding="utf-8") as output:
    output.write(json.dumps(call) + "\\n")
sys.exit(41 if json.dumps(call) == os.environ.get("FAIL_CALL") else 0)
'''
            for name in ("npm", "node", "yamllint"):
                tool = tools / name
                tool.write_text(stub)
                tool.chmod(0o700)
            calls = root / "calls.jsonl"
            environment = {key: value for key, value in os.environ.items() if key not in ("MAKEFLAGS", "MFLAGS")}
            environment.update(PATH=str(tools) + os.pathsep + os.environ["PATH"], CALL_LOG=str(calls))
            docs = json.loads((ROOT / "documentation/package.json").read_text())["scripts"]["check"]
            scenarios = (
                (["make", "--no-print-directory", "YAMLLINT=yamllint", "api-lint"], [
                    ["yamllint", "-c", ".yamllint.yml", "api/openapi/control-v1.yaml"],
                    ["npm", "--prefix", "test/validation", "run", "lint"],
                    ["node", "test/validation/runner.mjs", "lint"],
                ]),
                (["make", "--no-print-directory", "CATALOG_ENV_FILE=/dev/null", "docs-site-check"], [["npm", "run", "check"]]),
                (["sh", "-c", docs], [
                    ["npm", "run", "lint"], ["npm", "run", "check:astro"],
                    ["node", "--test", "scripts/*.test.mjs"], ["node", "scripts/check.mjs"],
                ]),
            )
            (root / "documentation").mkdir()
            for command, expected in scenarios:
                for failure in [None, *range(len(expected))]:
                    with self.subTest(command=command, failure=failure):
                        calls.unlink(missing_ok=True)
                        environment["FAIL_CALL"] = json.dumps(expected[failure]) if failure is not None else ""
                        result = subprocess.run(command, cwd=root, env=environment, capture_output=True, text=True, timeout=10, check=False)
                        self.assertEqual(result.returncode == 0, failure is None, result.stderr)
                        actual = [json.loads(line) for line in calls.read_text().splitlines()]
                        self.assertEqual(actual, expected if failure is None else expected[:failure + 1])

    def test_documentation_audit_keeps_mcp_sources_and_excludes_installed_packages(self):
        commands = self.run_make("-n", "docs-check")
        lint = next(shlex.split(line) for line in commands.splitlines() if "--dot --ignore" in line)
        ignored = [lint[i + 1] for i, word in enumerate(lint) if word == "--ignore"]
        self.assertIn(".venv", ignored)
        self.assertIn("test/interoperability/mcp/node_modules", ignored)
        self.assertNotIn("test/interoperability/mcp", ignored)
        self.assertNotIn("test/interoperability/mcp/README.md", ignored)
        for directory in ("openai", "huggingface", "context7/plugin"):
            self.assertIn("internal/adapter/testdata/upstream/" + directory, ignored)
        self.assertNotIn("internal/adapter/testdata/upstream", ignored)
        self.assertNotIn("internal/adapter/testdata/upstream/README.md", ignored)
        self.assertEqual(lint[-2:], ["**/*.md", "**/*.mdx"])
        self.assertIn("TestDocumentationContracts", commands)

    def test_security_keeps_scanners_and_limits_vendor_recipe_exclusion(self):
        commands = self.run_make("-n", "security")
        arguments_by_line = [shlex.split(line) for line in commands.splitlines()
                             if line.strip() and not line.lstrip().startswith("#")]
        scan = next(arguments for arguments in arguments_by_line
                    if "fs" in arguments and "--scanners" in arguments)
        self.assertIn("--disable-telemetry", scan)
        self.assertIn("--skip-version-check", scan)
        self.assertEqual(scan[scan.index("--scanners") + 1], "vuln,secret,misconfig")
        self.assertEqual(scan[scan.index("--exit-code") + 1], "1")
        self.assertEqual(scan[-1], ".")
        files = [scan[i + 1] for i, word in enumerate(scan) if word == "--skip-files"]
        self.assertEqual(files, ["documentation/node_modules/yaml-language-server/Dockerfile"])
        directories = [scan[i + 1] for i, word in enumerate(scan) if word == "--skip-dirs"]
        self.assertIn(".tools", directories)
        self.assertNotIn("uv.lock", scan)
        self.assertNotIn("pyproject.toml", scan)
        self.assertEqual(set(directories), {"bin", "coverage", ".tools", "**/.tools", "**/.venv", "**/node_modules", "documentation/dist", "catalog-ui/dist", "output", "specs", "objects", "objects-*"})
        self.assertNotIn("schemas", directories)
        self.assertNotIn("schemas/upstream", directories)
        # Maintain each component's first-party locks outside disposable installations.
        for maintained in ("sdk/rust", "test/sdk/rust", "sdk/python", "sdk/typescript",
                           "catalog-ui", "documentation", "extensions/vscode", "test/validation",
                           "test/interoperability/mcp", "examples/langgraph"):
            self.assertNotIn(maintained, directories)
        self.assertIn("govulncheck", commands)

    def test_ui_lint_target_is_read_only_and_full_check_includes_it(self):
        commands = self.run_make("-n", "ui-lint")
        self.assertIn("cd catalog-ui && npm run lint", commands)
        self.assertNotIn("--fix", commands)
        self.assertNotIn("npm install", commands)
        self.assertIn("npm run check", self.run_make("-n", "ui-check"))
        scripts = json.loads((ROOT / "catalog-ui/package.json").read_text())["scripts"]
        self.assertIn("npm run lint", scripts["check"].split(" && "))
        self.assertIn("--max-warnings 0", scripts["lint"])
        self.assertNotIn("--fix", scripts["lint"])
    def test_ui_dead_code_is_in_ui_check_once_and_outside_units_and_backend(self):
        scripts = json.loads((ROOT / "catalog-ui/package.json").read_text())["scripts"]
        self.assertEqual(scripts["check"].split(" && ").count("npm run dead-code:check"), 1)
        self.assertNotIn("dead-code", scripts["test:unit"])
        self.assertIn("knip.json", scripts["format-check"])
        for target, mode in [("ui-dead-code-report", "report"), ("ui-dead-code-check", "check")]:
            command = self.run_make("-n", target)
            self.assertIn("cd catalog-ui && npm run dead-code:" + mode, command)
            self.assertNotIn("install", command)
            self.assertNotIn("--fix", command)
        for goals in [("check", "verify"), ("test-unit",), ("ui-test-unit",)]:
            commands = self.run_make("-n", *goals)
            self.assertNotIn("dead-code:check", commands)
            self.assertNotIn("knip", commands)
    def test_real_helm_packaging_is_explicit_and_not_duplicated(self):
        expected = {
            "test_packaging_rejects_below_minimum_without_publishing",
            "test_long_versions_have_valid_truncated_labels",
            "test_actual_archives_prerelease_metadata_and_image_selection",
        }
        def methods(path):
            tree = ast.parse(path.read_text())
            return {node.name for node in ast.walk(tree) if isinstance(node, ast.FunctionDef)}

        self.assertTrue(expected <= methods(ROOT / "test/helm/test_functional_packaging.py"))
        self.assertFalse(expected & methods(ROOT / "test/packaging/test_unit_packaging.py"))
        for target in ["helm-package-test", "helm-test", "helm-check", "deploy-check"]:
            with self.subTest(target=target):
                commands = self.run_make("-n", target, "HELM=custom-helm")
                self.assertEqual(commands.count("python3 -B test/helm/test_functional_packaging.py"), 1)
                self.assertIn('HELM="custom-helm"', commands)
        commands = self.run_make("-n", "verify")
        self.assertNotIn("python3 -B test/helm/test_functional_packaging.py", commands)
        self.assertNotIn("-run TestEndToEnd", commands)
        self.assertIn("-run TestEndToEnd", self.run_make("-n", "test-e2e"))
    def test_ui_dependency_checks_are_explicit_and_preserve_scanner_selection(self):
        """Check Make wiring without an advisory service or installed UI packages."""
        for target, mode in [("ui-deps-check", "trivy"), ("ui-deps-audit", "audit")]:
            commands = self.run_make("-n", target)
            self.assertEqual(commands.strip(), "node catalog-ui/scripts/dependencies.mjs " + mode)
        output = self.run_make(
            "-s", "-f", "Makefile", "-f", "-", "ui-deps-check", "TRIVY=/fixture/trivy",
            input="ui-deps-check:\n\t@printf '%s\\n' \"$$UI_DEPS_TRIVY\"\n",
        )
        self.assertEqual(output.strip(), "/fixture/trivy")
        for target in ["ui-check", "ui-test", "test-ui", "verify"]:
            self.assertNotIn("dependencies.mjs", self.run_make("-n", target))
    def test_python_lint_is_in_check_not_service_free_go_verification(self):
        """Inspect suite wiring without requiring or running a real Ruff binary."""
        command = '"fixture-ruff" check --config ruff.toml --no-cache --no-fix test extensions/vscode/tooling examples/langgraph examples/tutorials/local-oidc examples/mcp-scanner'
        for target in ("python-lint", "check"):
            with self.subTest(target=target):
                output = self.run_make("-n", target, "RUFF=fixture-ruff")
                self.assertEqual(output.count(command), 1)
                self.assertNotIn("pip install", output)
                self.assertNotIn("uv tool", output)
        for target in ("test-unit", "test", "verify", "build"):
            with self.subTest(target=target):
                self.assertNotIn("fixture-ruff", self.run_make("-n", target, "RUFF=fixture-ruff"))
    def test_langgraph_tools_and_tests_are_explicit_and_do_not_export_questions_to_other_gates(self):
        for target in ("langgraph-tools-install", "test-langgraph-unit", "test-langgraph", "test-langgraph-model"):
            output = self.run_make("-n", target)
            self.assertIn("test/langgraph/runner.py", output)
            self.assertNotIn("compose-up", output)
            self.assertNotIn("ollama pull", output)
        for target, expected in (("langgraph-demo", "PRIVATE_QUESTION"), ("test-unit", "")):
            output = self.run_make("-s", "-f", "Makefile", "-f", "-", target,
                                   "LANGGRAPH_QUESTION=PRIVATE_QUESTION",
                                   input=target + ':\n\t@printf "%s" "$$LANGGRAPH_QUESTION"\n')
            self.assertEqual(output, expected)

    def test_mcp_scanner_targets_are_explicit_env_isolated_and_preserve_literal_inputs(self):
        for target, action in (("mcp-scanner-tools-install", "install"), ("mcp-scanner-scan", "scan"), ("mcp-scanner-check", "check"), ("test-mcp-scanner-compose", "run")):
            output = self.run_make("-n", target, "CATALOG_ENV_FILE=/unavailable/selected-workflow")
            self.assertIn(("test/mcp-scanner/campaign.py " if target == "test-mcp-scanner-compose" else "test/mcp-scanner/runner.py ") + action, output)
            self.assertNotIn("compose-up", output)
            self.assertNotIn("pip install", output)
        names = ("MCP_SCANNER_INPUT", "MCP_SCANNER_OUTPUT", "MCP_SCANNER_SOURCE", "MCP_SCANNER_CAPTURED_AT", "MCP_SCANNER_CONTEXT")
        values = ["literal $(error must-not-expand) `touch must-not-execute` " + name for name in names]
        probe = "mcp-scanner-scan:\n\t@python3 -c 'import json,os; print(json.dumps([os.environ.get(n) for n in " + repr(list(names)) + "]))'\n"
        # Use double quotes in the Python expression inside the single shell quotes.
        probe = probe.replace(repr(list(names)), json.dumps(list(names)))
        result = self.run_make("-s", "-f", "Makefile", "-f", "-", "mcp-scanner-scan", *[name + "=" + value for name, value in zip(names, values, strict=True)], input=probe)
        self.assertEqual(json.loads(result), values)
        mixed = subprocess.run(["make", "--no-print-directory", "mcp-scanner-check", "build"], cwd=ROOT, capture_output=True, text=True, timeout=10, check=False)
        self.assertNotEqual(mixed.returncode, 0)
        self.assertIn("MCP Scanner targets must run alone", mixed.stderr)

    def test_mcp_scanner_receiver_targets_are_explicit_and_keep_sdk_checks_separate(self):
        goals = ("mcp-scanner-receiver-build", "mcp-scanner-receiver-check", "test-mcp-scanner-receiver", "test-mcp-scanner-receiver-dsn")
        for target in goals:
            with self.subTest(target=target):
                extra = ["CATALOG_TEST_DATABASE_DSN=postgres://owned-fixture/receiver"] if target.endswith("-dsn") else []
                output = self.run_make("-n", target, "CATALOG_ENV_FILE=/unavailable/selected-workflow", *extra)
                self.assertIn("examples/mcp-scanner/receiver", output)
                self.assertNotIn("compose-up", output)
                self.assertNotIn("mcp-scanner/runner.py install", output)
                if target == "mcp-scanner-receiver-check":
                    self.assertIn('test/sdk/runner.py check --language "go"', output)
                    self.assertIn("go test -race -shuffle=on", output)
                    self.assertIn("-tags=functional", output)
                    self.assertIn("go-licenses report", output)
                if target.endswith("-dsn"):
                    self.assertIn("-tags=functional,integration", output)
                    self.assertIn("MCP_SCANNER_RECEIVER_BINARY=", output)
                    self.assertIn("MCP_SCANNER_TEST_PYTHON=", output)

    def test_langgraph_launcher_preserves_literal_environment_and_command_line_inputs(self):
        names = ["LANGGRAPH_QUESTION", "LANGGRAPH_MODEL_ORIGIN", "LANGGRAPH_MODEL",
                 "LANGGRAPH_NAMESPACE_ID", "LANGGRAPH_ALLOW_LOOPBACK_HTTP"]
        probe = "import json, os; print(json.dumps({k: os.environ[k] for k in " + repr(names) + " if k in os.environ}))"
        for target in ("langgraph-demo", "test-langgraph-model"):
            for source in ("environment", "command-line"):
                with self.subTest(target=target, source=source), tempfile.TemporaryDirectory() as directory:
                    marker = Path(directory) / "unexpected-make-expansion"
                    value = ("literal café $(shell printf UNEXPECTED) $(file >" + str(marker)
                             + ",unexpected) $$ $HOME ${HOME} `unused` 'quoted'\nsecond line")
                    inputs = dict.fromkeys(names, value)
                    env = dict(os.environ)
                    for name in names:
                        env.pop(name, None)
                    arguments = []
                    if source == "environment":
                        env.update(inputs)
                    else:
                        arguments = [name + "=" + text for name, text in inputs.items()]
                    output = self.run_make("-s", "-f", "Makefile", "-f", "-", target,
                                           *arguments, env=env,
                                           input=target + ":\n\t@python3 -c " + shlex.quote(probe) + "\n")
                    self.assertFalse(marker.exists(), "Make evaluated launcher input as an expression")
                    expected = inputs if target == "langgraph-demo" else {
                        name: inputs[name] for name in ("LANGGRAPH_MODEL_ORIGIN", "LANGGRAPH_MODEL")
                    }
                    self.assertEqual(json.loads(output), expected)

        env = dict(os.environ)
        for name in names:
            env.pop(name, None)
        for target in ("langgraph-demo", "test-langgraph-model"):
            with self.subTest(target=target, source="defaults"):
                expected = {"LANGGRAPH_MODEL_ORIGIN": "", "LANGGRAPH_MODEL": "qwen2.5:7b"}
                if target == "langgraph-demo":
                    expected.update(LANGGRAPH_QUESTION="", LANGGRAPH_NAMESPACE_ID="", LANGGRAPH_ALLOW_LOOPBACK_HTTP="0")
                output = self.run_make("-s", "-f", "Makefile", "-f", "-", target, env=env,
                                       input=target + ":\n\t@python3 -c " + shlex.quote(probe) + "\n")
                self.assertEqual(json.loads(output), expected)

    def test_data_and_conformance_checks_use_their_existing_gate_boundaries(self):
        for target in ('json-check', 'check'):
            self.assertIn('test/packaging/data_checks.py json', self.run_make('-n', target))
        self.assertIn('--yamllint "lint with spaces"', self.run_make('-n', 'yaml-check', 'YAMLLINT=lint with spaces'))
        for target in ('conformance-fixture', 'conformance-live', 'test-functional-workflow'):
            output = self.run_make('-n', target)
            self.assertIn('python3 test/packaging/ard_conformance.py check', output)
            self.assertNotIn('python3 schemas/upstream/', output)
        for target in ('test-unit', 'test-tooling-unit', 'build'):
            self.assertNotIn('ard_conformance.py', self.run_make('-n', target))

    def test_default_targets_do_not_export_database_credentials(self):
        # Replace only the selected recipe with an environment probe: no Go tests
        # or database connection can run, even if export isolation regresses.
        names = ["CATALOG_TEST_DATABASE_DSN", "CATALOG_DATABASE_DSN", "CATALOG_MIGRATION_DATABASE_DSN", "CATALOG_CLIENT_SKILL_SESSION_DIR"]
        probe = "import json, os; print(json.dumps({k: os.environ[k] for k in " + repr(names) + " if k in os.environ}))"
        for target in ["test-functional-local", "test-unit", "coverage-unit", "test-race", "coverage-profile"]:
            for source in ["environment", "command-line"]:
                with self.subTest(target=target, source=source):
                    env = dict(os.environ)
                    arguments = []
                    if source == "environment":
                        env.update(dict.fromkeys(names, "unused-test-sentinel"))
                    else:
                        arguments = [name + "=unused-test-sentinel" for name in names]
                    output = self.run_make(
                        "-s", "-f", "Makefile", "-f", "-", target, *arguments, env=env,
                        input=target + ':\n\t@python3 -c "' + probe.replace('"', '\\"') + '"\n',
                    )
                    self.assertEqual(json.loads(output), {})
    def test_unit_selection_bypasses_env_files_and_rejects_mixed_goals(self):
        """An env file must not execute even when Make receives an override."""
        with tempfile.TemporaryDirectory() as directory:
            for filename in ("workflow.env", "workflow[1].env"):
                env_file = Path(directory) / filename
                env_file.write_text("$(error Unit selection evaluated a workflow file)\n")
                for target in ("test-unit", "coverage-unit", "ui-test-unit", "test-tooling-unit"):
                    self.run_make("-n", target, "CATALOG_ENV_FILE=" + str(env_file))
                    for other in ("test", "test-functional-local", "run", "coverage-profile", "ui-test"):
                        with self.subTest(target=target, other=other, workflow=filename):
                            result = subprocess.run(
                                ["make", "-n", target, other, "CATALOG_ENV_FILE=" + str(env_file)],
                                cwd=ROOT, capture_output=True, text=True, timeout=30, check=False,
                            )
                            self.assertNotEqual(result.returncode, 0)
                            self.assertIn("Unit test targets must run alone", result.stderr)
                            self.assertNotIn("Unit selection evaluated", result.stderr)

    def test_native_unit_selection_is_isolated_nonempty_and_propagates_failure(self):
        """Use two identically named modules to protect owner-local discovery."""
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "Makefile").write_bytes((ROOT / "Makefile").read_bytes())
            (root / "go.mod").write_text("module example.invalid/native\n\ngo 1.27.0\n")
            (root / "test/packaging").mkdir(parents=True)
            shutil.copyfile(ROOT / "test/packaging/completion.py", root / "test/packaging/completion.py")
            for owner in ("first", "second"):
                folder = root / "test" / owner
                folder.mkdir(parents=True)
                (folder / "runner.py").write_text("OWNER = " + repr(owner) + "\n")
                (folder / "test_unit_runner.py").write_text(
                    "import sys, unittest, runner\nclass Units(unittest.TestCase):\n"
                    "    def test_owner(self):\n        self.assertEqual(sys.flags.optimize, 0)\n"
                    "        self.assertEqual(runner.OWNER, " + repr(owner) + ")\n")
                (folder / "test_functional_runner.py").write_text('raise RuntimeError("functional selected")\n')
            def run(owners):
                return subprocess.run(
                    ["make", "--no-print-directory", "test-tooling-unit", "PYTHON_UNIT_DIRS=" + owners,
                     "PYTHONOPTIMIZE=2", "CATALOG_ENV_FILE=/missing/unused.env"],
                    cwd=root, capture_output=True, text=True, timeout=30, check=False,
                    env=dict(os.environ, PYTHONOPTIMIZE="2"))
            result = run("first second")
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stderr.count("Ran 1 test"), 2)
            self.assertNotIn("functional selected", result.stderr)
            self.assertNotEqual(run("").returncode, 0)
            (root / "test/empty").mkdir()
            self.assertNotEqual(run("empty").returncode, 0)
            (root / "test/first/test_unit_skipped.py").write_text("import unittest\nclass Units(unittest.TestCase):\n    @unittest.skip('synthetic')\n    def test_required(self): pass\n")
            result = run("first second")
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("failed, skipped or did not complete", result.stderr)
            (root / "test/first/test_unit_skipped.py").unlink()
            (root / "test/first/test_unit_empty.py").write_text("import unittest\n")
            result = run("first second")
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("required test file has no cases", result.stderr)
            (root / "test/first/test_unit_empty.py").unlink()
            (root / "test/first/test_unit_runner.py").write_text('raise RuntimeError("unit failed")\n')
            result = run("first second")
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("unit failed", result.stderr)
            self.assertNotIn("test_owner", result.stderr)

    def test_go_selection_ignores_ambient_flags_and_preserves_failures(self):
        """Exercise discovery, execution and atomic coverage through real Make."""
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "Makefile").write_bytes((ROOT / "Makefile").read_bytes())
            (root / "go.mod").write_text("module example.invalid/selection\n\ngo 1.27.0\n")
            (root / "test/packaging").mkdir(parents=True)
            for name in ("application_coverage.py", "unit_coverage.py"):
                (root / "test/packaging" / name).write_bytes((ROOT / "test/packaging" / name).read_bytes())
            log = root / "calls.jsonl"
            stub = root / "go-probe"
            stub.write_text("#!" + sys.executable + "\n" + '''
import json, os, pathlib, sys
args = sys.argv[1:]
with open(os.environ["SELECTION_LOG"], "a") as log:
    log.write(json.dumps({"tool": pathlib.Path(sys.argv[0]).name, "args": args, "flags": os.getenv("GOFLAGS"), "workspace": os.getenv("GOWORK"), "optimize": sys.flags.optimize}) + "\\n")
if pathlib.Path(sys.argv[0]).name == "npm":
    sys.exit(int(os.environ.get("SELECTION_NPM_EXIT", "0")))
if args[0] == "list":
    print(os.environ.get("SELECTION_PACKAGES", "example.invalid/selection/api\\nexample.invalid/selection/cmd/catalogctl"))
    sys.exit(int(os.environ.get("SELECTION_LIST_EXIT", "0")))
if args[0] == "test":
    for arg in args:
        if arg.startswith("-coverprofile="):
            pathlib.Path(arg.split("=", 1)[1]).write_text("mode: atomic\\nexample.invalid/selection/api/code.go:1.1,2.1 1 0\\n")
    if os.getenv("CATALOG_PROCESS_COVERAGE_DIR"):
        directory = pathlib.Path(os.environ["CATALOG_PROCESS_COVERAGE_DIR"])
        (directory / "covmeta.fixture").touch()
        (directory / "covcounters.fixture").touch()
    sys.exit(int(os.environ.get("SELECTION_TEST_EXIT", "0")))
if args[:3] == ["tool", "covdata", "textfmt"]:
    target = next(arg[3:] for arg in args if arg.startswith("-o="))
    pathlib.Path(target).write_text("mode: atomic\\nexample.invalid/selection/api/code.go:1.1,2.1 1 1\\n")
    sys.exit(0)
sys.exit("unexpected Go invocation")
''')
            stub.chmod(0o700)
            (root / "catalog-ui").mkdir()
            npm = root / "npm"
            npm.write_bytes(stub.read_bytes())
            npm.chmod(0o700)
            env = {key: value for key, value in os.environ.items()
                   if not key.startswith("CATALOG_") and key not in ("MAKEFLAGS", "MFLAGS", "GOFLAGS", "GOWORK")}
            env["PATH"] = str(root) + os.pathsep + env.get("PATH", "")
            env["SELECTION_LOG"] = str(log)

            def run(target, settings, arguments=()):
                log.write_text("")
                result = subprocess.run(
                    ["make", "--no-print-directory", target, "CATALOG_ENV_FILE=/dev/null",
                     "GO=" + str(stub), *arguments],
                    cwd=root, env=env | settings, capture_output=True, text=True, timeout=30, check=False,
                )
                calls = [json.loads(line) for line in log.read_text().splitlines()]
                return result, calls

            poison = {"GOFLAGS": "-tags=integration,helm,minikube -run=Nothing -skip=. -list=.", "GOWORK": "/missing/workspace", "PYTHONOPTIMIZE": "2"}
            for target in ("test-unit", "ui-test-unit", "ui-test", "test-functional-local", "test", "test-race", "test-shuffle", "coverage-profile"):
                for source in ("environment", "command-line"):
                    with self.subTest(target=target, source=source):
                        result, calls = run(target, poison if source == "environment" else {},
                            [key + "=" + value for key, value in poison.items()] if source == "command-line" else [])
                        self.assertEqual(result.returncode, 0, result.stderr)
                        self.assertTrue(calls)
                        for call in calls:
                            self.assertEqual(call["flags"], "-mod=readonly")
                            self.assertEqual(call["workspace"], "off")
                            self.assertEqual(call["optimize"], 0)
                        tests = [call["args"] for call in calls if call["tool"] == "go-probe" and call["args"][0] == "test"]
                        self.assertEqual(len(tests), 2 if target == "test-shuffle" else 1)
                        if target == "test-shuffle":
                            self.assertIn("example.invalid/selection/api", tests[0])
                            self.assertNotIn("example.invalid/selection/cmd/catalogctl", tests[0])
                            self.assertEqual(tests[1], ["test", "-tags=functional", "./cmd/catalogctl", "-shuffle=on", "-count=10", "-timeout=25m"])
                        self.assertEqual("-tags=functional" in tests[0], target not in ("test-unit", "ui-test-unit"))
                        self.assertEqual("-tags=minikube" in tests[0], target == "test-unit")
                        self.assertIn("-shuffle=on", tests[0])
                        self.assertIn("-count=10" if target == "test-shuffle" else "-count=1", tests[0])
                        self.assertEqual("-timeout=20m" in tests[0], target == "test-shuffle")
                        self.assertEqual("-race" in tests[0], target == "test-race")
                        if target in ("ui-test", "ui-test-unit"):
                            npm_calls = [call["args"] for call in calls if call["tool"] == "npm"]
                            self.assertEqual(npm_calls, [["test"] if target == "ui-test" else ["run", "test:unit"]])
                        if target == "coverage-profile":
                            self.assertIn("-tags=functional", calls[0]["args"])

            for target in ("test-unit", "coverage-profile", "test-shuffle"):
                for settings in ({"SELECTION_PACKAGES": ""}, {"SELECTION_LIST_EXIT": "7"}):
                    with self.subTest(target=target, settings=settings):
                        result, calls = run(target, settings)
                        self.assertNotEqual(result.returncode, 0)
                        self.assertFalse(any(call["args"][0] == "test" for call in calls))

            for target in ("ui-test-unit", "ui-test"):
                result, calls = run(target, {"SELECTION_NPM_EXIT": "9"})
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(any(call["tool"] == "go-probe" for call in calls))

            profile = root / "coverage/default.out"
            profile.write_text("retained successful profile\n")
            for target in ("test-unit", "ui-test-unit", "ui-test", "test-functional-local", "test", "test-race", "test-shuffle", "coverage-profile"):
                with self.subTest(failed_target=target):
                    result, _ = run(target, {"SELECTION_TEST_EXIT": "9"})
                    self.assertNotEqual(result.returncode, 0)
            self.assertEqual(profile.read_text(), "retained successful profile\n")
            self.assertEqual(list(profile.parent.glob("default.out.*")), [])
            self.assertTrue((profile.parent / ".staging").is_dir())
            self.assertEqual(list((profile.parent / ".staging").iterdir()), [])

    def test_unit_coverage_retains_embed_only_sample_inventory(self):
        command = self.run_make("-n", "coverage-unit")
        self.assertIn('--zero-statements "github.com/open-agentic-catalog/open-agentic-catalog/migrations"', command)
        self.assertIn('--zero-statements "github.com/open-agentic-catalog/open-agentic-catalog/samples"', command)
        self.assertIn('$(GO_APPLICATION_PACKAGES)', (ROOT / "Makefile").read_text())

    def test_process_coverage_is_explicit_and_full_coverage_runs_fresh_inputs(self):
        local = self.run_make("-n", "coverage-profile")
        self.assertIn("CATALOG_PROCESS_COVERAGE_DIR=", local)
        self.assertIn("application_coverage.py collect", local)
        self.assertIn("application_coverage.py merge", local)
        for target in ("test-unit", "coverage-unit", "test-functional-local", "verify"):
            commands = self.run_make("-n", target)
            self.assertNotIn("TestPostgresProcesses", commands)
            self.assertNotIn("compose.integration.yaml -p", commands)
        for target in ("test-process-postgres-dsn", "coverage-process-postgres-dsn-profile"):
            commands = self.run_make("-n", target)
            self.assertIn("CATALOG_TEST_DATABASE_DSN is required", commands)
            self.assertIn("-race -tags=integration ./test/functional -run '^TestPostgresProcesses$'", commands)
        # Never dry-run a recursive recipe: Make may execute its entire shell.
        commands = re.search(r"^coverage-full:[^\n]*\n((?:\t[^\n]*\n)+)", (ROOT / "Makefile").read_text(), re.MULTILINE)[1]
        self.assertIn("-j1 coverage-unit", commands)
        self.assertIn("-j1 coverage-check coverage-core-check coverage-postgres-check coverage-process-postgres-profile", commands)
        self.assertEqual(commands.count("application_coverage.py snapshot"), 2)
        self.assertIn("Source changed during full coverage", commands)
    def test_real_go_tags_ignore_persisted_selection_and_keep_functional_failures(self):
        """Use a disposable module to prove the boundary with the actual Go tool."""
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "Makefile").write_bytes((ROOT / "Makefile").read_bytes())
            (root / "go.mod").write_text("module example.invalid/selection\n\ngo 1.27.0\n")
            validation = root / "test" / "validation"
            installed = validation / "node_modules" / "tool"
            installed.mkdir(parents=True)
            (validation / "go.mod").write_bytes((ROOT / "test/validation/go.mod").read_bytes())
            (installed / "invalid.go").write_text("not valid Go source\n")
            owned = root / "test" / "owned"
            owned.mkdir()
            (owned / "owned_test.go").write_text('package owned\nimport "testing"\nfunc TestOwned(t *testing.T) {}\n')
            package = root / "api"
            package.mkdir()
            (package / "unit_test.go").write_text('''package selection
import "testing"
func TestUnit(t *testing.T) {}
''')
            (package / "local_test.go").write_text('''//go:build functional

package selection
import "testing"
func TestFunctional(t *testing.T) { t.Fatal("functional failure was selected") }
''')
            (package / "external_test.go").write_text('''//go:build integration || helm

package selection
import "testing"
func TestExternal(t *testing.T) { t.Fatal("external selection leaked") }
''')
            env_file = root / "go.env"
            env_file.write_text("GOFLAGS=-tags=integration -run=Nothing\n")
            env = {key: value for key, value in os.environ.items()
                   if not key.startswith("CATALOG_") and key not in ("MAKEFLAGS", "MFLAGS", "GOFLAGS", "GOWORK")}
            env.update(GOENV=str(env_file), GOWORK="/missing/workspace")
            for target in ("test-unit", "test-functional-local", "test"):
                with self.subTest(target=target):
                    result = subprocess.run(
                        ["make", "--no-print-directory", target, "GO_SOURCE_DIRS=api test", "CATALOG_ENV_FILE=/dev/null"],
                        cwd=root, env=env, capture_output=True, text=True, timeout=30, check=False,
                    )
                    output = result.stdout + result.stderr
                    self.assertNotIn("external selection leaked", output)
                    self.assertIn("example.invalid/selection/test/owned", output)
                    self.assertNotIn("node_modules", output)
                    if target == "test-unit":
                        self.assertEqual(result.returncode, 0, output)
                        self.assertNotIn("no tests to run", output)
                    else:
                        self.assertNotEqual(result.returncode, 0)
                        self.assertIn("functional failure was selected", output)
    def test_functional_selection_reaches_broad_and_focused_gates(self):
        """A moved regression must remain selected by its existing owner."""
        for target in ("test", "test-functional-local", "test-race", "test-shuffle",
                       "coverage-profile", "test-e2e", "embeddings-check", "api-contract-check",
                       "client-skill-check", "conformance-fixture", "ui-test"):
            with self.subTest(target=target):
                commands = self.run_make("-n", target)
                tests = [line for line in commands.splitlines() if "go test " in line]
                self.assertTrue(tests)
                self.assertTrue(all("-tags=functional" in line for line in tests), tests)
        commands = self.run_make("-n", "test", "test-functional-local")
        self.assertEqual(commands.count("go test "), 1)
        commands = self.run_make("-n", "lint")
        self.assertIn("test/packaging/go_licenses.py", commands)
        self.assertIn('TAGS = "functional,integration,helm,minikube"',
                      (ROOT / "test/packaging/go_licenses.py").read_text())
        self.assertIn("go vet -tags=functional,integration,helm,minikube", commands)
        self.assertIn("--build-tags=functional,integration,helm,minikube", commands)
        for selection in ("-tags=integration", "-tags=helm", "-tags=minikube", "-tags=minikube,functional"):
            self.assertIn("go test " + selection + " ", commands)
        self.assertIn("-tags=minikube,functional", self.run_make("-n", "test-minikube-contract"))
    def test_coverage_consumers_use_the_default_suite_profile(self):
        commands = self.run_make("-n", "coverage-check", "coverage-core-check", "coverage-summary", "coverage-html")
        self.assertIn("coverage/default.out", commands)
        self.assertNotIn("coverage/unit.out", commands)
        self.assertEqual(commands.count("-covermode=atomic"), 1)
    def test_clean_targets_are_isolated_and_ignore_env_files(self):
        """Cleanup must not run builds concurrently or evaluate local workflow files."""
        for target in ("clean", "clean-dry-run"):
            output = self.run_make("-n", target, "CATALOG_ENV_FILE=/not/a/workflow")
            self.assertIn("python3 -B test/packaging/clean.py", output)
            self.assertEqual("--dry-run" in output, target == "clean-dry-run")
            for other in ("build", "clean-dry-run" if target == "clean" else "clean"):
                result = subprocess.run(
                    ["make", "-n", target, other], cwd=ROOT,
                    capture_output=True, text=True, timeout=30, check=False,
                )
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("Cleanup targets must run alone", result.stderr)


    def test_unit_coverage_preserves_selection_denominator_and_atomic_output(self):
        """Exercise real Go coverage, including untested code and hostile flags."""
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            subprocess.run(["git", "init", "-q", str(root)], check=True, capture_output=True, timeout=10)
            (root / "Makefile").write_bytes((ROOT / "Makefile").read_bytes())
            (root / "go.mod").write_text("module example.invalid/coverage\n\ngo 1.27.0\n")
            reporter = root / "test/packaging/unit_coverage.py"
            reporter.parent.mkdir(parents=True)
            reporter.write_bytes((ROOT / "test/packaging/unit_coverage.py").read_bytes())
            sources = {
                "api/code.go": "package api\nfunc Used() int { return 1 }\n",
                "api/uncovered.go": "package api\nfunc Unused() int { return 2 }\n",
                "api/code_test.go": 'package api\nimport "testing"\nfunc TestUnit(t *testing.T) { if Used()!=1 { t.Fatal("wrong result") } }\n',
                "api/local_test.go": '//go:build functional\n\npackage api\nimport "testing"\nfunc TestFunctional(t *testing.T) { t.Fatal("functional selected") }\n',
                "api/external_test.go": '//go:build integration || helm\n\npackage api\nimport "testing"\nfunc TestExternal(t *testing.T) { t.Fatal("external selected") }\n',
                "api/minikube_test.go": '//go:build minikube\n\npackage api\nimport "testing"\nfunc TestMinikubeUnit(t *testing.T) { Used() }\n',
                "cmd/unimported/main.go": "package main\nfunc main() { println(1) }\n",
                "internal/a2apb/generated.go": "package a2apb\nfunc Excluded() int { return 3 }\n",
                "migrations/embed.go": "package migrations\nconst Version=1\n",
                "examples/example/example.go": "package example\nfunc Excluded() int { return 4 }\n",
                "test/helper/helper.go": "package helper\nfunc Excluded() int { return 5 }\n",
                "test/helper/helper_test.go": 'package helper\nimport "testing"\nfunc TestHarness(t *testing.T) { Excluded() }\n',
            }
            for name, content in sources.items():
                filename = root / name
                filename.parent.mkdir(parents=True, exist_ok=True)
                filename.write_text(content)
            go = shutil.which("go")
            self.assertIsNotNone(go)
            wrapper = root / "go-probe"
            wrapper.write_text("#!" + sys.executable + "\n" + '''
import json, os, pathlib, subprocess, sys
args = sys.argv[1:]
log = pathlib.Path("calls.jsonl")
previous = log.read_text().splitlines() if log.exists() else []
with log.open("a") as output:
    output.write(json.dumps({"args": args, "flags": os.getenv("GOFLAGS"), "workspace": os.getenv("GOWORK"), "optimize": sys.flags.optimize}) + "\\n")
if args[0] == "list" and os.getenv("FAIL_LIST") == str(len(previous) + 1):
    sys.exit(9)
if args[0] == "list" and os.getenv("EMPTY_LIST") == str(len(previous) + 1):
    sys.exit(0)
if args[0] == "test" and os.getenv("FAIL_TEST"):
    sys.exit(9)
sys.exit(subprocess.call([os.environ["REAL_GO"], *args]))
''')
            wrapper.chmod(0o700)
            goenv = root / "go.env"
            goenv.write_text("GOFLAGS=-tags=integration -run=Nothing\n")
            env = {key: value for key, value in os.environ.items()
                   if not key.startswith("CATALOG_") and key not in ("MAKEFLAGS", "MFLAGS", "GOFLAGS", "GOWORK")}
            env.update(REAL_GO=go, GOENV=str(goenv))
            def run(target="coverage-unit", settings=None, arguments=()):
                (root / "calls.jsonl").unlink(missing_ok=True)
                result = subprocess.run(
                    ["make", "--no-print-directory", target, "GO=" + str(wrapper),
                     "GO_SOURCE_DIRS=api cmd internal migrations examples test", "UNIT_COVERAGE_MIN=33.3",
                     "CRITICAL_COVERAGE_PACKAGES=example.invalid/coverage/api", "UNIT_CRITICAL_PACKAGE_COVERAGE_MIN=50",
                     "CATALOG_ENV_FILE=/missing/unused.env", *arguments],
                    cwd=root, env=dict(env, **(settings or {})), capture_output=True, text=True, timeout=30, check=False)
                calls = [json.loads(line) for line in (root / "calls.jsonl").read_text().splitlines()]
                return result, calls
            unit, unit_calls = run("test-unit")
            self.assertEqual(unit.returncode, 0, unit.stdout + unit.stderr + repr(unit_calls))
            # Cover inherited, persisted and Make command-line selection overrides.
            variants = [({}, ()),
                        ({"GOFLAGS": "-tags=integration -run=Nothing -coverpkg=./api", "GOWORK": "/missing/workspace", "PYTHONOPTIMIZE": "2"}, ()),
                        ({}, ("GOFLAGS=-skip=.* -list=. -exec=false -covermode=set", "GOWORK=/missing/workspace", "PYTHONOPTIMIZE=2"))]
            for settings, arguments in variants:
                result, calls = run(settings=settings, arguments=arguments)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertEqual(len(calls), 3)  # two discoveries, one test execution
                self.assertTrue(all(call["flags"] == "-mod=readonly" and call["workspace"] == "off" for call in calls))
                self.assertTrue(all(call["optimize"] == 0 for call in calls))
                self.assertTrue(all("-tags=minikube" in call["args"] for call in calls))
                def select(call):
                    return [arg for arg in call["args"] if arg.startswith("example.invalid/")]
                self.assertEqual(select(calls[-1]), select(unit_calls[-1]))
                self.assertIn("1/3 statements (33.33%)", result.stdout)
                self.assertIn("n/a (no executable statements)", result.stdout)
                raw = (root / "coverage/unit.out").read_text()
                for name in ("api/code.go", "api/uncovered.go", "cmd/unimported/main.go"):
                    self.assertIn("example.invalid/coverage/" + name, raw)
                for name in ("a2apb", "examples/", "test/helper"):
                    self.assertNotIn(name, raw)
            profile = root / "coverage/unit.out"
            previous = profile.read_bytes()
            for settings, arguments in [({"FAIL_LIST": "1"}, ()), ({"FAIL_LIST": "2"}, ()),
                                        ({"EMPTY_LIST": "1"}, ()), ({"EMPTY_LIST": "2"}, ()),
                                        ({"FAIL_TEST": "1"}, ()), ({}, ("UNIT_COVERAGE_MIN=34",)),
                                        ({}, ("UNIT_COVERAGE_MIN=nan",)),
                                        ({}, ("UNIT_CRITICAL_PACKAGE_COVERAGE_MIN=50.001",)),
                                        ({}, ("CRITICAL_COVERAGE_PACKAGES=",)),
                                        ({}, ("CRITICAL_COVERAGE_PACKAGES=example.invalid/coverage/missing",))]:
                result, _ = run(settings=settings, arguments=arguments)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(profile.read_bytes(), previous)
                self.assertTrue((profile.parent / ".staging").is_dir())
                self.assertEqual(list((profile.parent / ".staging").iterdir()), [])
            # Successful execution cannot publish an incomplete profile either.
            reporter.write_text('raise SystemExit("malformed profile rejected")\n')
            result, _ = run()
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(profile.read_bytes(), previous)
