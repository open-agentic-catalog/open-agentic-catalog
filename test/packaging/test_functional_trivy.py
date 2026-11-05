"""Native process/Make boundaries, with explicitly selected real-scanner controls."""
import json
import os
from pathlib import Path
import secrets
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import trivy
import image

ROOT = Path(__file__).resolve().parents[2]


class TrivyProcessTests(unittest.TestCase):
    @unittest.skipUnless(os.environ.get("CATALOG_TEST_IMAGE_SECRETS") and os.environ.get("CATALOG_TEST_TRIVY"),
                         "select a local backend image and pinned scanner for metadata-secret qualification")
    def test_actual_image_metadata_secret_and_clean_control(self):
        # Explicit opt-in only: no acquisition, application execution or shared tags.
        selected = os.environ["CATALOG_TEST_IMAGE_SECRETS"]
        base = json.loads(image.run(["docker", "image", "inspect", selected]))[0]
        identifier = base["Id"]
        self.assertRegex(identifier, image.DIGEST)
        labels = base["Config"]["Labels"]
        tool = str(Path(os.environ["CATALOG_TEST_TRIVY"]).resolve())
        trivy.qualify([tool], env=trivy.environment())
        tag = "localhost/catalog-secret-control:" + secrets.token_hex(12)
        base_tag = tag + "-base"
        # Intentionally invalid, never submitted to a provider or used as authority.
        sentinel = "ghp_" + "N70InvalidQualificationOnlyAb12Cd34E"
        with tempfile.TemporaryDirectory(prefix="catalog image secret.") as directory:
            root = Path(directory)
            env = trivy.environment() | {
                "PACKAGING_ENGINE": "docker", "PACKAGING_TRIVY": tool,
                "PACKAGING_PLATFORM": base["Os"] + "/" + base["Architecture"],
                "PACKAGING_VERSION": labels["org.opencontainers.image.version"],
                "PACKAGING_COMMIT": labels["org.opencontainers.image.revision"],
                "PACKAGING_SOURCE_STATE": labels["io.open-agentic-catalog.source-state"],
            }
            built = False
            image.run(["docker", "tag", identifier, base_tag])
            try:
                for reference, expected in ((identifier, 0), (tag, 1)):
                    if expected:
                        context = root / "build"
                        context.mkdir()
                        (context / "Dockerfile").write_text("FROM " + base_tag + "\nENV N70_SYNTHETIC=" + sentinel + "\n")
                        image.run(["docker", "build", "--pull=false", "--network=none", "--tag", tag, str(context)], seconds=120)
                        built = True
                        derived = json.loads(image.run(["docker", "image", "inspect", tag]))[0]
                        self.assertEqual(derived["RootFS"]["Layers"], base["RootFS"]["Layers"], "control must change metadata only")
                        self.assertIn("N70_SYNTHETIC=" + sentinel, derived["Config"]["Env"])
                    output = root / ("secret" if expected else "clean")
                    result = subprocess.run([sys.executable, "-B", str(ROOT / "test/packaging/image.py"), "scan"],
                                            cwd=ROOT, env=env | {"PACKAGING_IMAGE": reference, "PACKAGING_OUTPUT": str(output)},
                                            capture_output=True, timeout=360, check=False)
                    self.assertNotIn(sentinel.encode(), result.stdout + result.stderr)
                    self.assertEqual(result.returncode, expected, "image scan returned an unexpected outcome")
                    reports = list(output.glob("scan-*/run.json"))
                    self.assertEqual(len(reports), 1)
                    report = json.loads(reports[0].read_text())
                    self.assertEqual(report["outcome"], "findings" if expected else "passed")
                    self.assertEqual(report["configuration"]["imageConfigScanners"], ["secret"])
                    self.assertEqual(report["findings"]["image"]["vulnerabilities"], 0)
                    self.assertEqual(report["image"]["imageId"], derived["Id"] if expected else identifier)
                    native = json.loads(reports[0].with_name("image.json").read_text())
                    detected = [finding for item in native["Results"] for finding in item.get("Secrets", [])]
                    if expected:
                        self.assertGreater(report["findings"]["image"]["secrets"], 0)
                        self.assertTrue(any(finding["RuleID"] == "github-pat" for finding in detected))
                    else:
                        self.assertEqual(detected, [])
            finally:
                image.run(["docker", "image", "rm", base_tag])
                cleanup = subprocess.run(["docker", "image", "rm", tag], capture_output=True, timeout=60, check=False)
                if built:
                    self.assertEqual(cleanup.returncode, 0, "owned metadata-control image cleanup failed")

    @unittest.skipUnless(os.environ.get("CATALOG_TEST_TRIVY"), "select pinned CATALOG_TEST_TRIVY for actual development-lock scanning")
    def test_actual_development_lock_and_vulnerable_positive_control(self):
        selected = [os.environ["CATALOG_TEST_TRIVY"]]
        trivy.qualify(selected, env=trivy.environment())
        self.assertEqual(trivy.scan_development_dependencies(selected, ROOT), len(trivy.development_dependencies(ROOT)))
        local = {"catalog-python-sdk-tools": {"virtual": "."}, "open-agentic-catalog": {"directory": "../../../sdk/python"}}
        sdk = ROOT / "test/sdk/python"
        self.assertEqual(trivy.scan_development_dependencies(selected, sdk, local), len(trivy.development_dependencies(sdk, local)))
        # This only scans advisory metadata; it never installs the old package.
        with patch.object(trivy, "development_dependencies", return_value={("requests", "2.19.1")}), self.assertRaisesRegex(ValueError, "High/Critical"):
            trivy.scan_development_dependencies(selected, ROOT)

    def test_success_failure_timeout_and_output_bound(self):
        self.assertEqual(trivy.execute([sys.executable, "-c", "print('native-result')"]), b"native-result\n")
        for source, seconds, error in [
            ("import sys; print('secret sentinel', file=sys.stderr); sys.exit(1)", 5, ValueError),
            ("import time; time.sleep(5)", 0.05, subprocess.TimeoutExpired),
            ("import sys; sys.stdout.write('x' * (9 << 20))", 5, ValueError),
        ]:
            with self.subTest(source=source), self.assertRaises(error) as failure:
                trivy.execute([sys.executable, "-c", source], seconds=seconds)
            if error is ValueError:
                self.assertNotIn("secret sentinel", str(failure.exception))

    def test_cli_rejects_an_old_actual_tool_without_echoing_stderr(self):
        with tempfile.TemporaryDirectory() as directory:
            tool = Path(directory) / "scanner.py"
            tool.write_text("import sys\nprint('private sentinel', file=sys.stderr)\nprint('{\"Version\":\"0.66.0\"}')\n")
            result = subprocess.run([sys.executable, "-B", str(ROOT / "test/packaging/trivy.py")],
                                    env=os.environ | {"PACKAGING_TRIVY": sys.executable + " " + str(tool)},
                                    capture_output=True, text=True, timeout=10, check=False)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("qualification failed", result.stderr)
            self.assertNotIn("private sentinel", result.stdout + result.stderr)

    def test_image_missing_in_selected_engine_cannot_trigger_scanner_or_pull(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            engine = root / "engine.py"
            log = root / "commands"
            engine.write_text("import pathlib,sys\npathlib.Path(" + repr(str(log)) + ").write_text(' '.join(sys.argv[1:]))\nsys.exit(1)\n")
            result = subprocess.run([sys.executable, "-B", str(ROOT / "test/packaging/image.py"), "scan"],
                                    env=os.environ | {"PACKAGING_ENGINE": sys.executable + " " + str(engine), "PACKAGING_IMAGE": "absent:fixture", "PACKAGING_TRIVY": "/must/not/run"},
                                    capture_output=True, text=True, timeout=10, check=False)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(log.read_text(), "image inspect absent:fixture")

    def test_html_failure_preserves_previous_report_and_removes_staging(self):
        # Exercise the actual Make recipe; substitute only tools and profile
        # generation, so converter failure cannot be hidden by the next command.
        with tempfile.TemporaryDirectory(prefix="catalog coverage.") as directory:
            root = Path(directory)
            (root / "go.mod").write_text((ROOT / "go.mod").read_text())
            (root / "coverage").mkdir()
            (root / "coverage/default.out").write_text("mode: atomic\n")
            output = root / "coverage/index.html"
            output.write_text("previous")
            render = root / "render"
            for converter, renderer_status, expected, content in [("false", 0, 2, "previous"), ("true", 9, 2, "previous"), ("true", 0, 0, "rendered")]:
                render.write_text("#!/bin/sh\ncat >/dev/null\nprintf 'rendered'\nexit " + str(renderer_status) + "\n")
                render.chmod(0o700)
                result = subprocess.run(["make", "--no-print-directory", "-f", str(ROOT / "Makefile"), "-o", "coverage-profile", "coverage-html",
                                         "PYTHON=true", "GOCOV=" + converter, "GOCOV_HTML=" + str(render)],
                                        cwd=root, capture_output=True, text=True, timeout=15, check=False)
                self.assertEqual(result.returncode, expected, result.stdout + result.stderr)
                self.assertEqual(output.read_text(), content)
                self.assertTrue((root / "coverage/.staging").is_dir())
                self.assertEqual(list((root / "coverage/.staging").iterdir()), [])
