import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch

import oidc
import runner


class OIDCTests(unittest.TestCase):
    def test_revoked_assignment_is_retained_in_impact_but_only_original_grant_is_effective(self):
        admin, reader = Mock(), Mock()
        grant = {"id": "sibling", "roleDefinitionId": "custom", "membershipId": "member",
                 "namespaceId": "other", "revision": 2, "state": "revoked"}
        admin.request.return_value = ({"items": [grant]}, {})
        original = {"assignment": {"id": "original"}}
        reader.request.return_value = ({"items": [original]}, {})
        oidc.verify_assignment_views(admin, reader, {"id": "custom"}, grant, "original")
        for extra in ("unrelated", "sibling", "original"):
            with self.subTest(extra=extra), self.assertRaisesRegex(runner.Failure, "effective-assignments"):
                reader.request.return_value = ({"items": [original, {"assignment": {"id": extra}}]}, {})
                oidc.verify_assignment_views(admin, reader, {"id": "custom"}, grant, "original")

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
        self.assertCountEqual([client["clientId"] for client in value["clients"]],
                              ["catalog", "namespace-reader", "unknown", "wrong-audience", "wrong-organization"])
        scopes = {scope["name"] for scope in value["clientScopes"]}
        self.assertEqual(scopes, {"catalog.read", "tenant.admin"})
        for client in value["clients"]:
            self.assertTrue(client["serviceAccountsEnabled"])
            self.assertFalse(client["standardFlowEnabled"])
            self.assertFalse(client["directAccessGrantsEnabled"])
            self.assertFalse(client["publicClient"])
            self.assertEqual(client["secret"], "private-secret")
            self.assertEqual(client["defaultClientScopes"], [])
            self.assertEqual(set(client["optionalClientScopes"]), scopes if client["clientId"] in ("catalog", "namespace-reader") else set())

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
