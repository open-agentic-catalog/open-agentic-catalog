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
        for chart in ["other", helm.CHART_NAME + "-1.2.3", ""]:
            for action in ["install", "uninstall", "access", "status"]:
                with self.subTest(chart=chart, action=action), patch.object(helm.subprocess, "run") as command:
                    metadata = argparse.Namespace(stdout=json.dumps({"name": "monitor", "namespace": "test", "chart": chart, "version": "4.5.6"}))
                    command.side_effect = [argparse.Namespace(stdout="monitor\n"), metadata] if action == "install" else [metadata]
                    with self.assertRaisesRegex(ValueError, "another chart"):
                        helm.run(self.options(action=action, values=str(helm.SOURCE / "values.yaml")))
                    self.assertEqual(command.call_args.args[0], ["helm", "get", "metadata", "monitor", "--kube-context", "owned", "--namespace", "test", "--output", "json"])

    def test_malformed_inventory_never_changes_resources(self):
        for names in ["other\n", "monitor\nother\n"]:
            with self.subTest(names=names), patch.object(helm.subprocess, "run") as command:
                command.return_value.stdout = names
                with self.assertRaisesRegex(ValueError, "inventory"):
                    helm.run(self.options(action="install", values=str(helm.SOURCE / "values.yaml")))
                self.assertEqual(command.call_count, 1)

    def test_wrong_or_missing_release_metadata_stops_before_action(self):
        for item in [[], {}, {"name": "monitor", "namespace": "elsewhere", "chart": helm.CHART_NAME},
                     {"name": "other", "namespace": "test", "chart": helm.CHART_NAME}]:
            with self.subTest(item=item), patch.object(helm.subprocess, "run") as command:
                command.return_value.stdout = json.dumps(item)
                with self.assertRaisesRegex(ValueError, "metadata"):
                    helm.run(self.options(action="uninstall"))
                self.assertEqual(command.call_count, 1)

    def test_uninstall_only_removes_selected_release(self):
        with patch.object(helm.subprocess, "run") as command:
            command.return_value.stdout = json.dumps({"name": "monitor", "namespace": "test", "chart": helm.CHART_NAME, "version": "1.2.3-rc.1+build"})
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
                command.side_effect = [argparse.Namespace(stdout=""), helm.subprocess.CalledProcessError(1, "helm")]
                with self.assertRaises(helm.subprocess.CalledProcessError):
                    helm.run(self.options(action="install", values=str(values)))
                self.assertEqual(command.call_count, 2)
                arguments = command.call_args.args[0]
                self.assertEqual(arguments[1:3], ["install", "monitor"])
                self.assertIn("enabled=true", arguments)
                self.assertGreater(arguments.index("enabled=true"), arguments.index("--values"))

    def test_existing_release_requires_metadata_before_upgrade(self):
        values = str(helm.SOURCE / "values.yaml")
        inventory = argparse.Namespace(stdout="monitor\n")
        metadata = argparse.Namespace(stdout=json.dumps({"name": "monitor", "namespace": "test", "chart": helm.CHART_NAME, "version": "1.2.3-rc.1+build"}))
        with patch.object(helm.subprocess, "run") as command, patch.object(helm, "package", return_value=Path("packaged.tgz")):
            command.side_effect = [inventory, metadata, None]
            helm.run(self.options(action="install", values=values))
            listing, lookup, upgrade = [item.args[0] for item in command.call_args_list]
            self.assertEqual(listing[listing.index("--filter") + 1], "^monitor$")
            for state in ["--deployed", "--failed", "--pending", "--uninstalling", "--uninstalled", "--superseded"]:
                self.assertIn(state, listing)
            self.assertEqual(lookup[1:4], ["get", "metadata", "monitor"])
            self.assertEqual(upgrade[1:4], ["upgrade", "monitor", "packaged.tgz"])
            self.assertNotIn("--install", upgrade)

    def test_lookup_failure_never_falls_back_to_install(self):
        failure = helm.subprocess.CalledProcessError(1, "helm")
        for responses in [[failure], [argparse.Namespace(stdout="monitor\n"), failure]]:
            with self.subTest(lookups=len(responses)), patch.object(helm.subprocess, "run", side_effect=responses) as command:
                with self.assertRaises(helm.subprocess.CalledProcessError):
                    helm.run(self.options(action="install", values=str(helm.SOURCE / "values.yaml")))
                self.assertEqual(command.call_count, len(responses))

    def test_reinstall_reuses_only_matching_retained_history(self):
        for chart in [helm.CHART_NAME, helm.CHART_NAME + "-1.2.3"]:
            with self.subTest(chart=chart), patch.object(helm.subprocess, "run") as command, patch.object(helm, "package", return_value=Path("packaged.tgz")):
                metadata = {"name": "monitor", "namespace": "test", "status": "uninstalled", "chart": chart}
                command.side_effect = [argparse.Namespace(stdout="monitor\n"), argparse.Namespace(stdout=json.dumps(metadata)), None]
                options = self.options(action="install", values=str(helm.SOURCE / "values.yaml"))
                if chart == helm.CHART_NAME:
                    helm.run(options)
                    self.assertEqual(command.call_args.args[0][1:5], ["install", "--replace", "monitor", "packaged.tgz"])
                else:
                    with self.assertRaisesRegex(ValueError, "another chart"):
                        helm.run(options)
                    self.assertEqual(command.call_count, 2)

    def test_access_is_loopback_and_names_are_namespace_unique(self):
        with patch.object(helm.subprocess, "run") as command:
            command.return_value.stdout = json.dumps({"name": "monitor", "namespace": "test", "chart": helm.CHART_NAME})
            helm.run(self.options(action="access"))
            arguments = command.call_args.args[0]
            self.assertIn("127.0.0.1", arguments)
            self.assertIn("13000:3000", arguments)
        self.assertNotEqual(helm.resource_name("first", "monitor"), helm.resource_name("second", "monitor"))
        self.assertLess(len(helm.resource_name("test", "a" * 53) + "-prometheus"), 64)
