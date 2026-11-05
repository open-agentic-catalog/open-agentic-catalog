"""Packaging and explicit-release wrapper safety without a Kubernetes connection."""
import argparse
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

import helm


class HelmMonitoringTests(unittest.TestCase):
    def options(self, **changes):
        values = dict(action="status", context="owned", namespace="test", release="monitor",
                      helm="helm", kubectl="kubectl", values="", component="grafana", port=13000, output="unused")
        values.update(changes)
        return argparse.Namespace(**values)

    def test_package_is_deterministic_and_contains_shared_assets(self):
        with tempfile.TemporaryDirectory() as directory:
            archive = helm.package(directory)
            first = archive.read_bytes()
            self.assertEqual(first, helm.package(directory).read_bytes())
            with tarfile.open(archive) as package:
                for output, source in [("catalog.json", "dashboards/catalog.json"), ("images.json", "images.json"), ("grafana.ini", "grafana.ini")]:
                    self.assertEqual(package.extractfile(helm.CHART_NAME + "/files/" + output).read(), (helm.ASSETS / source).read_bytes())
                self.assertFalse(any("/charts/" in name or name.endswith(".py") for name in package.getnames()))
                self.assertIn(helm.CHART_NAME + "/LICENSE", package.getnames())

    def test_missing_selection_never_calls_cluster(self):
        with patch.object(helm.subprocess, "run") as command:
            for changes in [dict(context=""), dict(namespace=""), dict(release="--all"), dict(context="bad\ncontext")]:
                with self.assertRaises(ValueError):
                    helm.run(self.options(**changes))
            command.assert_not_called()

    def test_unrelated_release_cannot_be_changed(self):
        with patch.object(helm.subprocess, "run") as command:
            for chart in ["other-1.0.0", helm.CHART_NAME + "-unrelated-1.0.0", ""]:
                command.return_value.stdout = json.dumps([{"chart": chart}])
                for action in ["install", "uninstall", "access", "status"]:
                    with self.assertRaisesRegex(ValueError, "another chart"):
                        helm.run(self.options(action=action))
            self.assertTrue(all(call.args[0][1] == "list" for call in command.call_args_list))

    def test_malformed_inventory_never_changes_resources(self):
        with patch.object(helm.subprocess, "run") as command:
            for inventory in [{}, ["unexpected"], [{}, {}]]:
                command.return_value.stdout = json.dumps(inventory)
                with self.assertRaisesRegex(ValueError, "inventory"):
                    helm.run(self.options(action="uninstall"))
            self.assertTrue(all(call.args[0][1] == "list" for call in command.call_args_list))

    def test_uninstall_only_removes_selected_release(self):
        with patch.object(helm.subprocess, "run") as command:
            command.return_value.stdout = json.dumps([{"chart": helm.CHART_NAME + "-0.1.0"}])
            helm.run(self.options(action="uninstall"))
            arguments = command.call_args.args[0]
            self.assertEqual(arguments[:3], ["helm", "uninstall", "monitor"])
            self.assertIn("--kube-context", arguments)
            self.assertEqual(command.call_count, 2)
            self.assertNotIn("delete", arguments)

    def test_failed_install_does_not_reset_or_uninstall(self):
        with tempfile.TemporaryDirectory() as directory:
            values = Path(directory) / "values.json"
            values.write_text("{}")
            with patch.object(helm.subprocess, "run") as command, patch.object(helm, "package", return_value=Path(directory) / "chart.tgz"):
                command.side_effect = [argparse.Namespace(stdout="[]"), helm.subprocess.CalledProcessError(1, "helm")]
                with self.assertRaises(helm.subprocess.CalledProcessError):
                    helm.run(self.options(action="install", values=str(values)))
                self.assertEqual(command.call_count, 2)
                arguments = command.call_args.args[0]
                self.assertIn("enabled=true", arguments)
                self.assertGreater(arguments.index("enabled=true"), arguments.index("--values"))

    def test_access_is_loopback_and_names_are_namespace_unique(self):
        with patch.object(helm.subprocess, "run") as command:
            command.return_value.stdout = json.dumps([{"chart": helm.CHART_NAME + "-0.1.0"}])
            helm.run(self.options(action="access"))
            arguments = command.call_args.args[0]
            self.assertIn("127.0.0.1", arguments)
            self.assertIn("13000:3000", arguments)
        self.assertNotEqual(helm.resource_name("first", "monitor"), helm.resource_name("second", "monitor"))
        self.assertLess(len(helm.resource_name("test", "a" * 53) + "-prometheus"), 64)
