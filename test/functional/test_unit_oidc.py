import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import oidc
import runner


class OIDCTests(unittest.TestCase):
    def test_configuration_has_only_the_selected_mapping(self):
        with tempfile.TemporaryDirectory() as directory:
            journey = object.__new__(oidc.OIDCJourney)
            journey.fixture = Path(directory)
            journey.issuer = "https://catalog-oidc:12345/realms/catalog"
            for mode in ("dedicated", "shared"):
                journey.configure_catalog(mode, scopes=True)
                value = runner.read_document(journey.fixture / "oidc-catalog.json")
                self.assertEqual(value["environment"], "production")
                auth = value["auth"]
                self.assertEqual(auth["issuer"], journey.issuer)
                self.assertTrue(auth["enforceScopes"])
                self.assertEqual("dedicatedTenantId" in auth, mode == "dedicated")
                self.assertEqual("organizationClaim" in auth, mode == "shared")
                self.assertEqual(auth["trustBundleFile"], "/fixture/trust/functional-ca.pem")

    def test_provider_identity_requires_both_digests(self):
        valid = {"configurationDigest": "sha256:" + "a" * 64, "indexDigest": "sha256:" + "b" * 64}
        self.assertTrue(runner.valid_oidc_identity(valid))
        for value in (None, [], {}, {"configurationDigest": valid["configurationDigest"]}, {**valid, "extra": "secret"}, {**valid, "indexDigest": 1}, {**valid, "indexDigest": "latest"}):
            self.assertFalse(runner.valid_oidc_identity(value))

    def test_realm_is_ephemeral_service_only(self):
        with patch.object(Path, "read_bytes", return_value=b'{"realm":"catalog","clients":[]}'):
            value = oidc.realm_template("private-secret")
        self.assertEqual(len(value["clients"]), 4)
        for client in value["clients"]:
            self.assertTrue(client["serviceAccountsEnabled"])
            self.assertFalse(client["standardFlowEnabled"])
            self.assertFalse(client["directAccessGrantsEnabled"])
            self.assertFalse(client["publicClient"])
            self.assertEqual(client["secret"], "private-secret")

    def test_progress_and_identity_cannot_be_skipped(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)
            self.assertEqual(runner.oidc_progress(path), [])
            for invalid in ({"completed": ["shared-mapping"]}, {"completed": [], "extra": True}):
                runner.write_document(path / "oidc-cases.json", invalid)
                with self.assertRaises(runner.Failure):
                    runner.oidc_progress(path)
        base = {"profile": "oidc", "images": {"api": {}}, "servedVersion": {"commit": "x"}, "smoke": {"checkCount": 1},
                "oidc": {"configurationDigest": "sha256:" + "a" * 64, "indexDigest": "sha256:" + "b" * 64}, "oidcCases": runner.OIDC_CASES}
        for field in ("oidc", "oidcCases"):
            report = json.loads(json.dumps(base))
            del report[field]
            self.assertFalse(runner.finalize(report, list(runner.OIDC_STEPS), "", True))
        self.assertTrue(runner.finalize(base, list(runner.OIDC_STEPS), "", True))
        self.assertEqual(base["qualification"], "independent-oidc-profile")
