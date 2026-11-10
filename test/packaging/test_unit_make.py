import json
import unittest
import make_test_helpers as helpers
from make_test_helpers import ROOT


class MakeScopeTests(helpers.MakeScopeTestsFixture, unittest.TestCase):
    def test_ui_node_selection_matches_the_frontend_image(self):
        """Keep local Node selection aligned without executing a container."""
        selected = (ROOT / "catalog-ui/.nvmrc").read_text().strip()
        self.assertIn(
            "docker.io/library/node:" + selected + "-alpine3.24@sha256:",
            (ROOT / "catalog-ui/Containerfile").read_text(),
        )

    def test_npm_archive_and_consumers_match_the_reviewed_bootstrap(self):
        """Keep independently downloaded build tools on one reviewed artifact."""
        archive = json.loads((ROOT / "test/ci/pins.json").read_text())["archives"]["npm"]
        for directory in ("catalog-ui", "documentation", "extensions/vscode", "sdk/typescript",
                          "test/validation", "test/interoperability/mcp"):
            package = json.loads((ROOT / directory / "package.json").read_text())
            self.assertEqual(package["packageManager"], "npm@" + archive["version"], directory)
        for name in ("pi-ard", "ard-explorer"):
            peer = json.loads((ROOT / "test/interoperability" / name / "inputs.json").read_text())
            self.assertEqual(peer["npm"], archive["version"], name)
        expected = "ADD --checksum=sha256:" + archive["sha256"] + " " + archive["url"] + " /tmp/npm.tgz"
        for filename in ("catalog-ui/Containerfile", "test/interoperability/mcp/Containerfile"):
            recipe = (ROOT / filename).read_text()
            self.assertIn(expected, recipe.splitlines(), filename)
            self.assertIn('test "$(npm --version)" = "' + archive["version"] + '"', recipe, filename)
            self.assertNotIn("npm install --global", recipe, filename)
