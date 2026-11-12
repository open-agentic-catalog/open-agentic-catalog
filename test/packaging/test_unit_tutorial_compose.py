"""Validate prepared selection without executing Compose or another process."""

import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from tutorial_test_helpers import PROJECT, SOURCE, inputs, load_example, write_inputs


compose = load_example("compose")


class TutorialComposeTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="catalog-tutorial-selection-")
        self.addCleanup(self.temporary.cleanup)
        self.directory = Path(self.temporary.name).resolve()
        self.path = write_inputs(self.directory)
        self.assertEqual(compose.prepared_inputs(self.directory)[1], inputs(self.directory))

    def test_generated_selection_controls_project_and_all_interpolation_inputs(self):
        original = self.path.read_bytes()
        values = inputs(self.directory)
        inherited = {key: "unrelated-input" for key in values}
        inherited["TUTORIAL_TEST_OTHER_SETTING"] = "preserve-this-setting"
        with patch.dict(os.environ, inherited):
            command, environment = compose.invocation(self.directory, ["ps", "-a"])
        self.assertEqual(command, ["docker", "compose", "--project-name", PROJECT,
                                   "--env-file", str(self.path), "--file", str(SOURCE / "compose.yaml"), "ps", "-a"])
        for key, value in values.items():
            self.assertEqual(environment[key], value)
        self.assertEqual(environment["TUTORIAL_TEST_OTHER_SETTING"], "preserve-this-setting")
        self.assertEqual(self.path.read_bytes(), original)

    def test_forwarded_global_selectors_and_short_flag_clusters_are_refused(self):
        for arguments in (
            ["config", "--project-name", "unrelated"], ["config", "--project-name=unrelated"],
            ["config", "-p", "unrelated"], ["config", "-punrelated"], ["up", "-dpunrelated"],
            ["config", "--env-file", "/unrelated"], ["config", "--env-file=/unrelated"],
            ["config", "--file", "/unrelated"], ["config", "--file=/unrelated"],
            ["config", "-f", "/unrelated"], ["config", "-f/unrelated"],
            ["config", "--project-directory", "/unrelated"], ["config", "--project-directory=/unrelated"],
        ):
            with self.subTest(arguments=arguments), self.assertRaisesRegex(ValueError, "selectors are fixed"):
                compose.invocation(self.directory, arguments)
        command, _ = compose.invocation(self.directory, ["exec", "-T", "postgres-a", "psql", "-X", "-v", "ON_ERROR_STOP=1"])
        self.assertEqual(command[-7:], ["exec", "-T", "postgres-a", "psql", "-X", "-v", "ON_ERROR_STOP=1"])

    def test_missing_repeated_unknown_and_malformed_metadata_are_refused(self):
        valid = self.path.read_text()
        invalid_documents = [
            "", valid.replace('COMPOSE_PROJECT_NAME="' + PROJECT + '"\n', ""),
            valid + 'COMPOSE_PROJECT_NAME="' + PROJECT + '"\n',
            valid + 'UNEXPECTED_INPUT="unrelated"\n', valid.replace('"' + PROJECT + '"', '"unrelated"'),
            valid.replace('"' + PROJECT + '"', '"catalog-docs-../../unrelated"'),
            valid.replace('"' + PROJECT + '"', 'null'), valid.replace('"' + PROJECT + '"', '["' + PROJECT + '"]'),
            valid.replace('"' + PROJECT + '"', '"line\\nline"'), valid + "not-an-assignment\n",
        ]
        for content in invalid_documents:
            with self.subTest(content=content[:80]):
                self.path.write_text(content)
                with self.assertRaises(ValueError):
                    compose.prepared_inputs(self.directory)
        self.path.unlink()
        with self.assertRaises(FileNotFoundError):
            compose.prepared_inputs(self.directory)

    def test_directory_and_source_binding_and_private_network_are_required(self):
        for key, value in (
            ("CATALOG_TUTORIAL_DIRECTORY", str(self.directory / "another")),
            ("CATALOG_TUTORIAL_SOURCE", str(self.directory)),
            ("CATALOG_TUTORIAL_SUBNET", "8.8.8.0/24"),
            ("CATALOG_TUTORIAL_SUBNET", "::/0"),
            ("CATALOG_TUTORIAL_FRONTEND_ADDRESS", "172.29.245.0"),
            ("CATALOG_TUTORIAL_FRONTEND_ADDRESS", "172.29.245.255"),
            ("CATALOG_TUTORIAL_FRONTEND_ADDRESS", "172.29.246.10"),
        ):
            with self.subTest(key=key, value=value):
                values = inputs(self.directory)
                values[key] = value
                write_inputs(self.directory, values)
                with self.assertRaises(ValueError):
                    compose.prepared_inputs(self.directory)

    def test_unprotected_symlinked_and_oversized_metadata_are_refused(self):
        self.path.chmod(0o644)
        with self.assertRaisesRegex(ValueError, "0600"):
            compose.prepared_inputs(self.directory)
        self.path.chmod(0o600)
        self.directory.chmod(0o755)
        try:
            with self.assertRaisesRegex(ValueError, "0700"):
                compose.prepared_inputs(self.directory)
        finally:
            self.directory.chmod(0o700)
        alias = self.directory / "alias"
        alias.symlink_to(self.path)
        self.path.unlink()
        self.path.symlink_to(alias)
        with self.assertRaisesRegex(ValueError, "0600"):
            compose.prepared_inputs(self.directory)
        self.path.unlink()
        self.path.write_text("x" * 65537)
        self.path.chmod(0o600)
        with self.assertRaisesRegex(ValueError, "0600"):
            compose.prepared_inputs(self.directory)


if __name__ == "__main__":
    unittest.main()
