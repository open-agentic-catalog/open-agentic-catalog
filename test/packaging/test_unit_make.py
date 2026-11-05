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
