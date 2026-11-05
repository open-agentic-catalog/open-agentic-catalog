"""Isolated license/environment policies; no package installs or subprocesses."""

import importlib.util
from pathlib import Path
from types import SimpleNamespace
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location(
    "example_inventory", HERE / "inventory.py"
)
inventory = importlib.util.module_from_spec(spec)
spec.loader.exec_module(inventory)
spec = importlib.util.spec_from_file_location("example_runner", HERE / "runner.py")
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)


def package(name, version, license):
    return SimpleNamespace(
        metadata={"Name": name, "License-Expression": license}, version=version
    )


class ToolUnits(unittest.TestCase):
    def test_environment_excludes_ambient_credentials_and_tracing(self):
        with patch.dict(
            "os.environ",
            {
                "CATALOG_SDK_TOKEN": "PRIVATE",
                "PYTHONPATH": "bad",
                "LANGSMITH_API_KEY": "PRIVATE",
                "LANGCHAIN_TRACING_V2": "true",
                "VIRTUAL_ENV": "bad",
                "PATH": "/bin",
            },
            clear=True,
        ):
            env = runner.environment()
        self.assertNotIn("CATALOG_SDK_TOKEN", env)
        self.assertNotIn("LANGSMITH_API_KEY", env)
        self.assertEqual(env["LANGCHAIN_TRACING_V2"], "false")
        self.assertEqual(env["PYTHONPATH"], str(runner.EXAMPLE))
        self.assertEqual(env["UV_PYTHON_DOWNLOADS"], "never")

    def test_license_control_rejects_changed_versions_and_unreviewed_licenses(self):
        for dependency, code in [
            (package("langgraph", "0.0.0", "MIT"), "differs from its lock"),
            (package("langgraph", "1.2.14", "GPL-3.0"), "Unreviewed"),
            (package("certifi", "2026.10.19", "GPL-3.0"), "Unreviewed"),
        ]:
            with (
                patch.object(inventory, "distributions", return_value=[dependency]),
                self.assertRaisesRegex(RuntimeError, code),
            ):
                inventory.inventory()

    def test_missing_environment_cannot_pass_inventory(self):
        with (
            patch.object(inventory, "distributions", return_value=[]),
            self.assertRaisesRegex(RuntimeError, "Incomplete"),
        ):
            inventory.inventory()

    def test_installed_locked_license_closure_is_accepted(self):
        values = inventory.inventory()
        self.assertEqual(values["langgraph"]["license"], "MIT")
        self.assertEqual(values["certifi"]["license"], "MPL-2.0")
        self.assertEqual(values["orjson"]["license"], "MPL-2.0 AND (Apache-2.0 OR MIT)")
