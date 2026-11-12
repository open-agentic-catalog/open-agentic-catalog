"""Exercise tutorial launchers and actual Compose rendering without a daemon."""

import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

from tutorial_test_helpers import PROJECT, SOURCE, inputs, write_inputs


def fixture_snapshot(directory):
    """Compare private fixture hashes and modes without exposing their contents."""
    return {str(path.relative_to(directory)): (path.stat().st_mode & 0o777, hashlib.sha256(path.read_bytes()).hexdigest() if path.is_file() else None)
            for path in [directory, *sorted(directory.rglob("*"))]}


class TutorialLauncherTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="catalog-tutorial-launcher-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.directory = self.root / "prepared"
        self.directory.mkdir(mode=0o700)
        self.metadata = write_inputs(self.directory)
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.marker = self.root / "docker-invoked.json"
        self.docker = self.bin / "docker"
        self.docker.write_text("#!/usr/bin/env python3\nimport json, os, sys\nfrom pathlib import Path\n"
                               + "Path(" + repr(str(self.marker)) + ").write_text(json.dumps({'argv': sys.argv[1:], 'project': os.environ.get('COMPOSE_PROJECT_NAME')}))\n"
                               + "sys.exit(17)\n")
        self.docker.chmod(0o700)
        self.environment = os.environ.copy()
        self.environment.update(PATH=str(self.bin) + os.pathsep + self.environment.get("PATH", ""),
                                COMPOSE_PROJECT_NAME="unrelated-project")

    def launch(self, *arguments, environment=None):
        return subprocess.run([sys.executable, "-B", str(SOURCE / "compose.py"), "--directory", str(self.directory), *arguments],
                              capture_output=True, text=True, env=environment or self.environment, timeout=15, check=False)

    def test_selected_command_preserves_child_exit_status(self):
        before = fixture_snapshot(self.directory)
        result = self.launch("ps", "-a")
        self.assertEqual(result.returncode, 17, result.stderr)
        invoked = json.loads(self.marker.read_text())
        self.assertEqual(invoked["argv"], ["compose", "--project-name", PROJECT, "--env-file", str(self.metadata),
                                           "--file", str(SOURCE / "compose.yaml"), "ps", "-a"])
        self.assertEqual(invoked["project"], PROJECT)
        self.assertEqual(fixture_snapshot(self.directory), before)

    def test_show_project_never_invokes_docker(self):
        result = self.launch("--show-project")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, PROJECT + "\n")
        self.assertFalse(self.marker.exists())

    def test_invalid_selection_and_override_flags_fail_before_docker(self):
        valid = self.metadata.read_text()
        cases = [
            (valid.replace('"' + PROJECT + '"', '"untrusted-selection-sentinel"'), ["down", "--volumes", "--remove-orphans"]),
            (valid, ["config", "--project-name=untrusted-selection-sentinel"]),
            (valid, ["up", "-dpuntrusted-selection-sentinel"]),
            (valid, ["config", "--env-file=/untrusted-selection-sentinel"]),
            (valid, ["config", "-f/untrusted-selection-sentinel"]),
            (valid, ["config", "--project-directory=/untrusted-selection-sentinel"]),
        ]
        for content, arguments in cases:
            with self.subTest(arguments=arguments):
                self.metadata.write_text(content)
                before = fixture_snapshot(self.directory)
                result = self.launch(*arguments)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("Tutorial Compose selection refused", result.stderr)
                self.assertNotIn("untrusted-selection-sentinel", result.stdout + result.stderr)
                self.assertFalse(self.marker.exists())
                self.assertEqual(fixture_snapshot(self.directory), before)
        self.metadata.unlink()
        result = self.launch("down", "--volumes")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.marker.exists())

    @unittest.skipUnless(shutil.which("docker"), "Docker Compose CLI is required for daemon-free configuration rendering")
    def test_actual_compose_config_pins_project_and_interpolation_under_conflicting_environment(self):
        environment = os.environ.copy()
        # Keep configuration rendering independent of any live or remote engine.
        environment.update(DOCKER_HOST="unix:///nonexistent-catalog-tutorial-test.sock", DOCKER_CONTEXT="")
        for key in inputs(self.directory):
            environment.pop(key, None)
        version = subprocess.run(["docker", "compose", "version", "--short"], capture_output=True, text=True,
                                 env=environment, timeout=10, check=False)
        if version.returncode:
            self.skipTest("Installed Docker has no usable Compose CLI")
        base = ["docker", "compose", "--env-file", str(self.metadata), "--file", str(SOURCE / "compose.yaml")]
        before = fixture_snapshot(self.directory)

        def render(command, selected_environment):
            result = subprocess.run(command + ["config", "--format", "json"], capture_output=True, text=True,
                                    env=selected_environment, timeout=15, check=False)
            self.assertEqual(result.returncode, 0, result.stderr)
            return json.loads(result.stdout)

        clean = render(base, environment)
        self.assertEqual(clean["name"], PROJECT)
        environment["COMPOSE_PROJECT_NAME"] = "unrelated-existing-project"
        redirected = render(base, environment)
        self.assertEqual(redirected["name"], "unrelated-existing-project")
        pinned = render(base + ["--project-name", PROJECT], environment)
        self.assertEqual(pinned["name"], PROJECT)
        environment.update({key: "unrelated-input" for key in inputs(self.directory) if key != "COMPOSE_PROJECT_NAME"})
        selected = render([sys.executable, "-B", str(SOURCE / "compose.py"), "--directory", str(self.directory)], environment)
        self.assertEqual(selected["name"], PROJECT)
        self.assertEqual(len(selected["services"]), 12)
        values = inputs(self.directory)
        self.assertEqual(selected["services"]["api-a"]["image"], values["CATALOG_TUTORIAL_BACKEND_IMAGE"])
        self.assertEqual(selected["services"]["ui-a"]["image"], values["CATALOG_TUTORIAL_UI_IMAGE"])
        self.assertEqual(selected["services"]["frontend"]["image"], values["CATALOG_TUTORIAL_FRONTEND_IMAGE"])
        self.assertEqual(selected["services"]["frontend"]["build"]["context"], str(SOURCE))
        self.assertEqual(selected["networks"]["tutorial"]["ipam"]["config"], [{"subnet": values["CATALOG_TUTORIAL_SUBNET"]}])
        self.assertEqual(selected["services"]["frontend"]["networks"]["tutorial"]["ipv4_address"], values["CATALOG_TUTORIAL_FRONTEND_ADDRESS"])
        password_mounts = [mount for mount in selected["services"]["postgres-a"]["volumes"] if mount["target"] == "/inputs/postgres-password"]
        self.assertEqual(len(password_mounts), 1)
        self.assertEqual(password_mounts[0]["source"], str(self.directory / "a/postgres-password"))
        self.assertEqual(fixture_snapshot(self.directory), before)


class TutorialPreparationTests(unittest.TestCase):
    @unittest.skipUnless(shutil.which("openssl"), "OpenSSL is required for actual partial/fresh certificate preparation")
    def test_partial_certificate_failure_preserves_inputs_and_fresh_destination_recovers(self):
        openssl = shutil.which("openssl")
        with tempfile.TemporaryDirectory(prefix="catalog-tutorial-preparation-") as temporary:
            root = Path(temporary).resolve()
            bin_directory = root / "bin"
            bin_directory.mkdir()
            failing = bin_directory / "openssl"
            failing.write_text("#!/usr/bin/env python3\nimport os, sys\n"
                               + "if '-x509' not in sys.argv:\n    sys.exit(19)\n"
                               + "os.execv(" + repr(openssl) + ", [" + repr(openssl) + ", *sys.argv[1:]])\n")
            failing.chmod(0o700)
            environment = os.environ.copy()
            environment["PATH"] = str(bin_directory) + os.pathsep + environment.get("PATH", "")
            partial = root / "partial"
            fresh = root / "fresh"

            def prepare(directory, selected_environment):
                return subprocess.run([sys.executable, "-B", str(SOURCE / "prepare.py"), "--directory", str(directory),
                                       "--backend-image", "example-backend:unrun", "--ui-image", "example-ui:unrun"],
                                      capture_output=True, text=True, env=selected_environment, timeout=45, check=False)

            failed = prepare(partial, environment)
            self.assertNotEqual(failed.returncode, 0)
            self.assertIn("Preparation failed", failed.stderr)
            self.assertEqual(partial.stat().st_mode & 0o777, 0o700)
            self.assertTrue((partial / "trust/ca.pem").is_file())
            self.assertTrue((partial / "trust/ca-key.pem").is_file())
            self.assertFalse((partial / "compose.env").exists())
            before = fixture_snapshot(partial)
            repeated = prepare(partial, os.environ.copy())
            self.assertNotEqual(repeated.returncode, 0)
            self.assertEqual(fixture_snapshot(partial), before)
            recovered = prepare(fresh, os.environ.copy())
            self.assertEqual(recovered.returncode, 0, recovered.stderr)
            self.assertTrue((fresh / "compose.env").is_file())
            self.assertTrue((fresh / "trust/browse-b.pem").is_file())
            self.assertNotEqual((fresh / "trust/ca.pem").read_bytes(), (partial / "trust/ca.pem").read_bytes())
            self.assertEqual((fresh / "trust/ca-key.pem").stat().st_mode & 0o777, 0o600)
            self.assertEqual((fresh / "compose.env").stat().st_mode & 0o777, 0o600)
            self.assertEqual(fixture_snapshot(partial), before)
            selected = subprocess.run([sys.executable, "-B", str(SOURCE / "compose.py"), "--directory", str(fresh), "--show-project"],
                                      capture_output=True, text=True, timeout=10, check=False)
            self.assertEqual(selected.returncode, 0, selected.stderr)
            self.assertRegex(selected.stdout, r"^catalog-docs-[0-9a-f]{8}\n$")


if __name__ == "__main__":
    unittest.main()
