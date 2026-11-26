"""Real chart-packager checks, explicitly run by make helm-package-test."""
from contextlib import contextmanager
import importlib.util
import os
from pathlib import Path
import re
import shlex
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("charts", ROOT / "test/packaging/charts.py")
charts = importlib.util.module_from_spec(spec)
spec.loader.exec_module(charts)
HELM = shlex.split(os.environ.get("HELM", "helm"))
if not HELM:
    raise ValueError("HELM must name a command")
IDENTIFIER = "sha256:" + "a" * 64


@contextmanager
def independent_chart_sources(root, versions):
    chart_dir = root / "deployments/helm"
    for name, version in versions.items():
        shutil.copytree(ROOT / "deployments/helm" / name, chart_dir / name)
        metadata = chart_dir / name / "Chart.yaml"
        metadata.write_text(re.sub(r"^version: .+$", "version: " + version, metadata.read_text(), flags=re.M))
    shutil.copyfile(ROOT / "LICENSE", root / "LICENSE")
    with patch.object(charts, "ROOT", root), patch.object(charts.monitoring, "SOURCE", chart_dir / charts.monitoring.CHART_NAME):
        yield


class ChartPackagingTests(unittest.TestCase):
    def test_release_qualification_checks_the_app_version_inside_each_archive(self):
        app_version = (ROOT / "VERSION").read_text().strip()
        command = [sys.executable, "-B", str(ROOT / "test/ci/release_delivery.py"), "charts", "--directory"]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            correct = charts.package(root / "correct", None, app_version, HELM)
            stale = charts.package(root / "stale", None, app_version + "-other", HELM)
            for obsolete in stale:
                with self.subTest(chart=obsolete.name), tempfile.TemporaryDirectory(dir=root) as selection:
                    for current in correct:
                        shutil.copyfile(obsolete if current.name == obsolete.name else current, Path(selection) / current.name)
                    result = subprocess.run(command + [selection], capture_output=True, text=True, timeout=30)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn(f"Packaged chart {obsolete.name}", result.stderr)
                    self.assertFalse((Path(selection) / "qualification.json").exists())
            subprocess.run(command + [str(root / "correct")], check=True, capture_output=True, timeout=30)
            self.assertTrue((root / "correct/qualification.json").is_file())

    def test_default_packages_keep_each_chart_version_and_use_the_supplied_app_version(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            versions = {"open-agentic-catalog": "1.2.3", "open-agentic-catalog-monitoring": "2.0.0-rc.1"}
            with independent_chart_sources(root, versions):
                archives = charts.package(root / "packages", None, "3.0.0", HELM)
            self.assertEqual({path.name for path in archives}, {f"{name}-{version}.tgz" for name, version in versions.items()})
            for name, version in versions.items():
                archive = root / "packages" / f"{name}-{version}.tgz"
                metadata = subprocess.check_output(HELM + ["show", "chart", str(archive)], text=True, timeout=30).splitlines()
                self.assertIn(f"version: {version}", metadata)
                self.assertIn("appVersion: 3.0.0", metadata)

    def test_ignored_project_license_is_not_replaced_by_a_nested_license(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "source"
            chart = root / "deployments/helm/open-agentic-catalog"
            shutil.copytree(ROOT / "deployments/helm/open-agentic-catalog", chart)
            shutil.copyfile(ROOT / "LICENSE", root / "LICENSE")
            (chart / "vendor").mkdir()
            (chart / "vendor/LICENSE").write_text("Other component's license\n")
            with (chart / ".helmignore").open("a") as ignored:
                ignored.write("\n/LICENSE\n")
            output = Path(directory) / "packages"
            with patch.object(charts, "ROOT", root), self.assertRaisesRegex(ValueError, "open-agentic-catalog/LICENSE"):
                charts.package(output, "1.2.3", "dev", HELM)
            self.assertEqual(list(output.iterdir()), [])

    def test_incorrect_monitoring_license_leaves_neither_archive(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            license_text = (ROOT / "LICENSE").read_bytes()
            (root / "LICENSE").write_bytes(license_text.replace(b"Apache", b"AcmeCo", 1))
            output = root / "packages"
            with patch.object(charts.monitoring, "ROOT", root), self.assertRaisesRegex(ValueError, "open-agentic-catalog-monitoring/LICENSE"):
                charts.package(output, "1.2.3", "dev", HELM)
            self.assertEqual(list(output.iterdir()), [])

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
        app_version = "2.0.0-rc.2+build"
        original = (charts.ROOT / "deployments/helm/open-agentic-catalog/Chart.yaml").read_bytes()
        with tempfile.TemporaryDirectory() as directory:
            outputs = charts.package(directory, version, app_version, HELM)
            for chart, output in zip(("open-agentic-catalog", "open-agentic-catalog-monitoring"), outputs, strict=True):
                self.assertEqual(output.name, f"{chart}-{version}.tgz")
                info = subprocess.run(HELM + ["show", "chart", str(output)], capture_output=True, text=True, check=True, timeout=30).stdout.splitlines()
                self.assertIn(f"name: {chart}", info)
                self.assertIn(f"version: {version}", info)
                self.assertIn(f"appVersion: {app_version}", info)
                with tarfile.open(output) as archive:
                    names = archive.getnames()
                    member = archive.getmember(f"{chart}/LICENSE")
                    self.assertTrue(member.isreg())
                    self.assertEqual(archive.extractfile(member).read(), (ROOT / "LICENSE").read_bytes())
                    self.assertFalse(any("/ci/" in n or "/node_modules/" in n for n in names))
                    metadata = archive.extractfile(f"{chart}/Chart.yaml").read().decode()
                    self.assertNotRegex(metadata, r"(?m)^(home|sources):")
                    source_metadata = (ROOT / "deployments/helm" / chart / "Chart.yaml").read_text()
                    self.assertNotRegex(source_metadata, r"(?m)^(home|sources):")
            with tarfile.open(outputs[1]) as archive:
                for filename, source in (("catalog.json", "dashboards/catalog.json"), ("images.json", "images.json"),
                                         ("grafana.ini", "grafana.ini"), ("dashboards.yaml", "provisioning/dashboards/catalog.yaml")):
                    with self.subTest(file=filename):
                        self.assertEqual(archive.extractfile("open-agentic-catalog-monitoring/files/" + filename).read(),
                                         (charts.monitoring.ASSETS / source).read_bytes())
            def render(*args, values=None):
                return subprocess.run(HELM + ["template", "--kube-version", "1.34.0", "test", str(outputs[0]), *args], input=values, check=True, capture_output=True, text=True, timeout=30).stdout
            with tarfile.open(outputs[0]) as archive:
                for filename, reference in (("spire-values.yaml", "spire-issuer-ca"), ("spire-source-values.yaml", "catalog-a-reader")):
                    with self.subTest(example=filename):
                        member = "open-agentic-catalog/examples/" + filename
                        contents = archive.extractfile(member).read()
                        self.assertEqual(contents, (charts.ROOT / "deployments/helm" / member).read_bytes())
                        self.assertIn(reference, render("--values", "-", values=contents.decode()))
            rendered = render()
            self.assertIn('app.kubernetes.io/version: "2.0.0-rc.2_build"', rendered)
            self.assertIn("open-agentic-catalog:2.0.0-rc.2_build", rendered)
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
