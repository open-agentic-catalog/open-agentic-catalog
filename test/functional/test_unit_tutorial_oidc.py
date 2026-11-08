"""Isolated exact source-test/snapshot and complete credential-generation controls."""

from datetime import datetime, timedelta, timezone
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import tutorial_oidc as f


class TutorialOIDCTests(unittest.TestCase):
    def setUp(self):
        # Every helper exercised here is pure/file-only. Guard against new
        # process/listener/HTTP work leaking into the isolated owner.
        for target in ("subprocess.run", "socket.create_connection", "socket.socket"):
            guard = patch(target, side_effect=AssertionError("unit crossed a real boundary"))
            guard.start()
            self.addCleanup(guard.stop)

    def test_source_test_requires_body_success_and_exact_new_generation(self):
        f.assert_test({"status": "succeeded", "credentialRevision": "new"}, "new")
        for value in ({}, {"status": "failed", "credentialRevision": "new"},
                      {"status": "succeeded", "credentialRevision": "old"}):
            with self.subTest(value=value), self.assertRaisesRegex(ValueError, "generation"):
                f.assert_test(value, "new")

    def test_native_failure_projection_preserves_only_fixed_bootstrap_reason(self):
        known = b"catalogctl: identity: INTERNAL: initial identity provision requires tenant, tenant-name, issuer, subject, operator, reason, and tenant_admin in roles"
        failure = f.NativeFailure(1, known + b"\n")
        self.assertEqual(failure.returncode, 1)
        self.assertEqual(failure.diagnostic, known.decode())
        for raw in (b"private-token-sentinel", known + b"\nprivate-token-sentinel", b""):
            observed = f.NativeFailure(2, raw)
            self.assertIsNone(observed.diagnostic)
            self.assertNotIn("private-token-sentinel", str(observed))

    def test_exact_published_and_indexed_identity_oracles(self):
        identifier = "urn:air:owned.example:agents:fixture"
        snapshot = {"host": "catalog-a.test", "audience": "authenticated", "state": "candidate", "ard": {"entries": [{"identifier": identifier, "version": "1.0.0"}]}}
        f.assert_publication(snapshot, identifier)
        for invalid in ({**snapshot, "host": "other.test"}, {**snapshot, "audience": "anonymous"},
                        {**snapshot, "ard": {"entries": []}}):
            with self.subTest(invalid=invalid), self.assertRaises(ValueError):
                f.assert_publication(invalid, identifier)
        with self.assertRaisesRegex(ValueError, "REPRESENTATIVE_QUERIES_REQUIRED"):
            f.assert_publication({**snapshot, "ard": {"entries": []}, "exclusions": [{"reasons": ["REPRESENTATIVE_QUERIES_REQUIRED"]}]}, identifier)

        class InertAPI:
            def request(self, method, path, body):
                self.outer.assertEqual((method, path), ("POST", "/api/v1/catalog/search"))
                self.outer.assertEqual(body, {"origins": ["indexed"], "kinds": ["agent"], "text": "federation tutorial", "limit": 10})
                return self.page, None

        api = InertAPI()
        api.outer = self
        item = {"identifier": identifier, "type": "indexed", "kind": "agent", "versionPresent": True, "version": "1.0.0", "indexed": {"sourceIds": ["source"]}}
        api.page = {"partial": False, "items": [item]}
        f.check_index(api, "source", identifier)
        changes = (("identifier", "different"), ("type", "local"), ("kind", "skill"), ("versionPresent", False),
                   ("version", "other"), ("indexed", {"sourceIds": []}), ("local", {}))
        for key, value in changes:
            api.page = {"partial": False, "items": [{**item, key: value, "description": "private-token-sentinel"}]}
            with self.subTest(key=key), self.assertRaises(ValueError) as error:
                f.check_index(api, "source", identifier)
            self.assertNotIn("private-token-sentinel", str(error.exception))
        for items, partial in (([], False), ([item, item], False), ([item], True)):
            api.page = {"partial": partial, "items": items}
            with self.subTest(items=items, partial=partial), self.assertRaises(ValueError):
                f.check_index(api, "source", identifier)

    def test_snapshot_requires_complete_current_exact_binding(self):
        expected = {"id": "snapshot", "state": "complete", "enumerationComplete": True, "sourceId": "source", "sourceRevision": 4, "credentialRevision": "new"}
        source = {"lastCompleteSnapshotId": "snapshot"}
        f.assert_snapshot(expected, source, 4, "new", "source")
        for key, value in (("state", "partial"), ("enumerationComplete", False), ("sourceId", "other"),
                           ("sourceRevision", 3), ("credentialRevision", "old"), ("id", "not-current")):
            with self.subTest(key=key), self.assertRaisesRegex(ValueError, "complete enumeration"):
                f.assert_snapshot({**expected, key: value}, source, 4, "new", "source")

    def test_refusals_require_exact_fresh_source_audit_and_guard_evidence(self):
        problem = {"code": "DEPENDENCY_UNAVAILABLE"}
        facts = {"beforeRevision": 4, "afterRevision": 4, "stateChanged": False, "outcome": "transport_error"}
        record = {"action": "source.tested", "targetId": "source", "tenantId": f.TENANTS["b"], "details": {"facts": facts}}
        f.assert_source_refusal(problem, [record], "source", 4)
        http_facts = {**facts, "outcome": "http_error", "httpClass": "4xx", "bodyDigest": "sha256:" + "a" * 64, "credentialRevision": "bad-audience"}
        http_record = {**record, "details": {"facts": http_facts}}
        f.assert_source_refusal(problem, [http_record], "source", 4, "bad-audience")
        for invalid in ([], [record, record], [{**record, "targetId": "other"}],
                        [{**record, "details": {"facts": {**facts, "beforeRevision": 3}}}],
                        [{**record, "details": {"facts": {**facts, "httpClass": "4xx"}}}],
                        [{**record, "details": {"facts": {**facts, "credentialRevision": "leaked"}}}]):
            with self.subTest(invalid=invalid), self.assertRaises(ValueError):
                f.assert_source_refusal(problem, invalid, "source", 4)
        for key, value in (("httpClass", "5xx"), ("credentialRevision", "other"), ("bodyDigest", ""), ("outcome", "transport_error")):
            with self.subTest(key=key), self.assertRaises(ValueError):
                f.assert_source_refusal(problem, [{**record, "details": {"facts": {**http_facts, key: value}}}], "source", 4, "bad-audience")
        with self.assertRaises(ValueError):
            f.assert_source_refusal({"code": "INTERNAL"}, [record], "source", 4)

    def test_full_generation_replacement_retains_immutable_old_token(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            target = root / "b/source-credentials"
            target.mkdir(parents=True)
            expiry = (datetime.now(timezone.utc) + timedelta(minutes=5)).isoformat()
            (root / "b/upstream-a.claims.json").write_text(json.dumps({"expiresAt": expiry}))
            (root / "b/upstream-a.token").write_text("synthetic-one\n")
            first = f.generation(root)
            old = json.loads((target / "catalog-a-reader.json").read_text())
            (root / "b/upstream-a.token").write_text("synthetic-two\n")
            second = f.generation(root)
            new = json.loads((target / "catalog-a-reader.json").read_text())
            self.assertNotEqual(first, second)
            self.assertEqual(new["tenantId"], f.TENANTS["b"])
            self.assertEqual(new["origin"], f.A)
            self.assertEqual(new["type"], "bearer")
            self.assertEqual((target / old["secretFile"]).read_text(), "synthetic-one\n")
            self.assertEqual((target / new["secretFile"]).read_text(), "synthetic-two\n")
            self.assertEqual((target / new["secretFile"]).stat().st_mode & 0o777, 0o444)
            self.assertEqual((target / "catalog-a-reader.json").stat().st_mode & 0o777, 0o444)
            self.assertEqual(sorted(p.name for p in target.iterdir()), sorted(["catalog-a-reader.json", old["secretFile"], new["secretFile"]]))
            (root / "b/upstream-a.claims.json").write_text(json.dumps({"expiresAt": (datetime.now(timezone.utc) - timedelta(minutes=1)).isoformat()}))
            with self.assertRaisesRegex(ValueError, "expired"):
                f.generation(root)
            self.assertEqual(json.loads((target / "catalog-a-reader.json").read_text()), new)

    def test_qualification_realm_preserves_readonly_prepared_input(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "a").mkdir(mode=0o700)
            original = root / "catalog-realm.json"
            before = b'{"realm":"catalog","clients":[{"clientId":"existing"}]}\n'
            original.write_bytes(before)
            original.chmod(0o444)
            original_mount = {"type": "bind", "source": str(original), "target": "/opt/keycloak/data/import/catalog-realm.json", "read_only": True}
            untouched = {"type": "bind", "source": "/owned/certificate", "target": "/tls/identity.pem", "read_only": True}
            config = {"services": {"identity": {"volumes": [original_mount, untouched.copy()]}}}
            f.qualification_reviewer(root, config)
            self.assertEqual(original.read_bytes(), before)
            self.assertEqual(original.stat().st_mode & 0o777, 0o444)
            selected = root / "qualification-realm.json"
            realm = json.loads(selected.read_text())
            self.assertEqual(realm["clients"][0], {"clientId": "existing"})
            self.assertEqual(len(realm["clients"]), 2)
            self.assertEqual(realm["clients"][1]["clientId"], "catalog-reviewer-qualification")
            self.assertEqual(realm["clients"][1]["secret"], (root / "a/reviewer-client-secret").read_text())
            self.assertEqual(selected.stat().st_mode & 0o777, 0o444)
            self.assertEqual((root / "a/reviewer-client-secret").stat().st_mode & 0o777, 0o600)
            self.assertEqual(config["services"]["identity"]["volumes"][0], {"type": "bind", "source": str(selected), "target": "/opt/keycloak/data/import/catalog-realm.json", "read_only": True})
            self.assertEqual(config["services"]["identity"]["volumes"][1], untouched)
            for mounts in ([], [untouched], [{**original_mount, "source": "/unrelated/realm"}]):
                with self.subTest(mounts=mounts), self.assertRaisesRegex(ValueError, "realm mount changed"):
                    f.qualification_reviewer(root, {"services": {"identity": {"volumes": mounts}}})
