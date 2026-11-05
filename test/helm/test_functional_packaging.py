"""Real chart-packager checks, explicitly run by make helm-package-test."""
import importlib.util
import os
from pathlib import Path
import re
import shlex
import subprocess
import tarfile
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("charts", ROOT / "test/packaging/charts.py")
charts = importlib.util.module_from_spec(spec)
spec.loader.exec_module(charts)
HELM = shlex.split(os.environ.get("HELM", "helm"))
if not HELM:
    raise ValueError("HELM must name a command")
IDENTIFIER = "sha256:" + "a" * 64


class ChartPackagingTests(unittest.TestCase):
    def test_packaging_rejects_below_minimum_without_publishing(self):
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaises(subprocess.CalledProcessError) as failed:
                charts.package(directory, "1.2.3", "dev", HELM, "1.33.0")
            output = (failed.exception.stdout or b"") + (failed.exception.stderr or b"")
            self.assertIn(b"kubeVersion", output)
            self.assertEqual(list(Path(directory).iterdir()), [])

    def test_long_versions_have_valid_truncated_labels(self):
        version = "1.2.3+" + "a" * 44 + ".b"
        app_version = "1.2.3+" + "a" * 55 + "..end"
        with tempfile.TemporaryDirectory() as directory:
            archive = charts.package(directory, version, app_version, HELM)[0]
            rendered = subprocess.run(HELM + ["template", "--kube-version", "1.34.0", "test", str(archive)], check=True, capture_output=True, text=True, timeout=30).stdout
            labels = re.findall(r'(?:helm.sh/chart|app.kubernetes.io/version): "([^"]+)"', rendered)
            self.assertTrue(labels)
            for label in labels:
                self.assertLessEqual(len(label), 63)
                self.assertRegex(label, r"^[A-Za-z0-9](?:[A-Za-z0-9_.-]*[A-Za-z0-9])?$")

    def test_actual_archives_prerelease_metadata_and_image_selection(self):
        version = "1.2.3-rc.1+build"
        original = (charts.ROOT / "deployments/helm/open-agentic-catalog/Chart.yaml").read_bytes()
        with tempfile.TemporaryDirectory() as directory:
            outputs = charts.package(directory, version, version, HELM)
            for output in outputs:
                with tarfile.open(output) as archive:
                    names = archive.getnames()
                    self.assertTrue(any(n.endswith("/LICENSE") for n in names))
                    self.assertFalse(any("/ci/" in n or "/node_modules/" in n for n in names))
                    metadata = archive.extractfile(names[0].split("/")[0] + "/Chart.yaml").read().decode()
                    self.assertIn(version, metadata)
                    self.assertNotRegex(metadata, r"(?m)^(home|sources):")
                    source_metadata = (charts.ROOT / "deployments/helm" / names[0].split("/")[0] / "Chart.yaml").read_text()
                    self.assertNotRegex(source_metadata, r"(?m)^(home|sources):")
            with tarfile.open(outputs[1]) as archive:
                self.assertEqual(archive.extractfile("open-agentic-catalog-monitoring/files/catalog.json").read(), (charts.monitoring.ASSETS / "dashboards/catalog.json").read_bytes())
                self.assertEqual(archive.extractfile("open-agentic-catalog-monitoring/files/images.json").read(), (charts.monitoring.ASSETS / "images.json").read_bytes())
            def render(*args):
                return subprocess.run(HELM + ["template", "--kube-version", "1.34.0", "test", str(outputs[0]), *args], check=True, capture_output=True, text=True, timeout=30).stdout
            rendered = render()
            self.assertIn('app.kubernetes.io/version: "1.2.3-rc.1_build"', rendered)
            self.assertIn("open-agentic-catalog:1.2.3-rc.1_build", rendered)
            self.assertIn("open-agentic-catalog:explicit", render("--set", "image.tag=explicit"))
            self.assertIn("open-agentic-catalog@" + IDENTIFIER, render("--set", "image.tag=ignored", "--set", "image.digest=" + IDENTIFIER))
            for profile in sorted((charts.ROOT / "deployments/helm/open-agentic-catalog/ci").glob("*.yaml")):
                self.assertIn("kind: Deployment", render("--values", str(profile)))
            enabled = subprocess.run(HELM + ["template", "--kube-version", "1.34.0", "monitor", str(outputs[1]), "--set", "enabled=true", "--set", "target.release=catalog", "--set", "target.deploymentLabel=test", "--set", "grafana.adminSecret.name=admin"], check=True, capture_output=True, text=True, timeout=30).stdout
            self.assertIn("kind: Deployment", enabled)
            self.assertIn("catalog.json", enabled)
            with self.assertRaisesRegex(ValueError, "already exists"):
                charts.package(directory, version, "other", HELM)
            self.assertEqual(len(list(Path(directory).iterdir())), 2)
        self.assertEqual(original, (charts.ROOT / "deployments/helm/open-agentic-catalog/Chart.yaml").read_bytes())


if __name__ == "__main__":
    unittest.main()
