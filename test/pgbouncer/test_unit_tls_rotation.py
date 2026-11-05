"""Pure TLS phase/evidence and local path checks; no tools or sockets."""
from copy import deepcopy
import base64
import hashlib
import json
from pathlib import Path
import tempfile
import unittest

import tls_rotation
import runner


class PlanTests(unittest.TestCase):
    def test_separate_renewal_overlap_switch_and_retirement(self):
        self.assertEqual(tls_rotation.plan("same-ca"), ("-renewed", ("ca",), True))
        self.assertEqual(tls_rotation.plan("overlap"), ("-renewed", ("ca", "ca-next"), False))
        self.assertEqual(tls_rotation.plan("successor"), ("-next", ("ca", "ca-next"), True))
        self.assertEqual(tls_rotation.plan("retired"), ("-next", ("ca-next",), False))
        self.assertEqual(tls_rotation.plan("restore"), ("", ("ca",), True))

    def test_file_staging_requires_no_activation_and_rejects_arbitrary_actions(self):
        self.assertEqual(tls_rotation.plan("stage-same-ca"), ("-renewed", ("ca",), False))
        self.assertEqual(tls_rotation.plan("stage-successor"), ("-next", ("ca", "ca-next"), False))
        for phase in ("", "../outside", "retire", "restart postgres", "SUCCESSOR"):
            with self.subTest(phase=phase), self.assertRaises(ValueError):
                tls_rotation.plan(phase)

    def test_scope_requires_the_exact_owned_promoted_fixture(self):
        project = "oac-pgbouncer-0123456789ab"
        with tempfile.TemporaryDirectory(prefix=project + "-") as directory:
            root = Path(directory)
            (root / "writer").write_text("standby", encoding="utf-8")
            (root / "compose.json").write_text(json.dumps({"services": {}}), encoding="utf-8")
            env = {"CATALOG_TEST_PGBOUNCER_PROJECT": project,
                   "CATALOG_TEST_PGBOUNCER_TLS_DIRECTORY": directory,
                   "CATALOG_TEST_PGBOUNCER_COMPOSE": str(root / "compose.json"),
                   "CATALOG_TEST_PGBOUNCER_WRITER_FILE": str(root / "writer")}
            actual, launcher = tls_rotation.fixture(env)
            self.assertEqual(actual, root.resolve())
            self.assertEqual(launcher[-1], project)
            for key, value in (("CATALOG_TEST_PGBOUNCER_PROJECT", "open-agentic-catalog"),
                               ("CATALOG_TEST_PGBOUNCER_COMPOSE", str(root / "other.json")),
                               ("CATALOG_TEST_PGBOUNCER_WRITER_FILE", str(root / "other"))):
                with self.subTest(key=key), self.assertRaises(ValueError):
                    tls_rotation.fixture(dict(env, **{key: value}))
            for writer in ("postgres", "../standby", "", "standby\n"):
                (root / "writer").write_text(writer, encoding="utf-8")
                with self.subTest(writer=writer), self.assertRaises(ValueError):
                    tls_rotation.fixture(env)


class EvidenceTests(unittest.TestCase):
    def evidence(self):
        expected = {role + suffix: "sha256:" + (str(index) * 64)
                    for index, (role, suffix) in enumerate(
                        ((role, suffix) for role in ("pooler", "postgres") for suffix in ("", "-renewed", "-next")), 1)}
        phases = []
        for index, name in enumerate(tls_rotation.PHASES):
            suffix = "" if index == 0 else "-renewed" if index < 3 else "-next"
            postmaster = "server-" + str(0 if index == 0 else 1 if index < 3 else 2)
            phase = {"name": name, "poolerContainer": str(index) * 64,
                     "poolerLeaf": expected["pooler" + suffix], "postgresLeaf": expected["postgres" + suffix],
                     "postmasterStart": postmaster, "jobState": "succeeded"}
            for kind, offset in (("apiInstance", 1), ("workerInstance", 11), ("jobID", 21)):
                phase[kind] = "00000000-0000-7000-8000-%012d" % (index + offset)
            for role in ("api", "worker"):
                phase[role + "Backend"] = {"pid": 101 + index, "startedAt": "backend-" + str(index),
                                          "postmasterStart": postmaster, "role": "catalog_" + role, "tls": True}
            phases.append(phase)
        return {"phases": phases, **dict.fromkeys(tls_rotation.PROOFS, True)}, expected

    def test_complete_native_evidence(self):
        value, expected = self.evidence()
        tls_rotation.validate_evidence(value, expected)

    def test_every_phase_and_observed_proof_is_required(self):
        value, expected = self.evidence()
        for proof in tls_rotation.PROOFS:
            for replacement in (False, None, 1, "true"):
                invalid = deepcopy(value)
                invalid[proof] = replacement
                with self.subTest(proof=proof, replacement=replacement), self.assertRaises(ValueError):
                    tls_rotation.validate_evidence(invalid, expected)
        for index in range(5):
            invalid = deepcopy(value)
            del invalid["phases"][index]
            with self.subTest(index=index), self.assertRaises(ValueError):
                tls_rotation.validate_evidence(invalid, expected)

    def test_wrong_peer_warm_backend_or_missing_work_cannot_qualify(self):
        value, expected = self.evidence()
        for field, replacement in (("poolerLeaf", expected["pooler"]), ("postgresLeaf", expected["postgres"]),
                                   ("poolerContainer", value["phases"][0]["poolerContainer"]),
                                   ("apiInstance", value["phases"][0]["apiInstance"]),
                                   ("workerInstance", value["phases"][0]["workerInstance"]),
                                   ("jobID", value["phases"][0]["jobID"]), ("jobState", "running"),
                                   ("apiBackend", value["phases"][0]["apiBackend"])):
            invalid = deepcopy(value)
            invalid["phases"][3][field] = replacement
            with self.subTest(field=field), self.assertRaises(ValueError):
                tls_rotation.validate_evidence(invalid, expected)
        for field, replacement in (("pid", True), ("tls", False), ("role", "catalog"),
                                   ("startedAt", ""), ("postmasterStart", "other-server")):
            invalid = deepcopy(value)
            invalid["phases"][2]["workerBackend"][field] = replacement
            with self.subTest(field=field), self.assertRaises(ValueError):
                tls_rotation.validate_evidence(invalid, expected)

    def test_server_restart_order_is_not_a_file_update(self):
        value, expected = self.evidence()
        for index in (1, 2, 3, 4):
            invalid = deepcopy(value)
            original = invalid["phases"][index - 1]["postmasterStart"]
            changed = original if index in (1, 3) else "unexpected-restart"
            invalid["phases"][index]["postmasterStart"] = changed
            for role in ("api", "worker"):
                invalid["phases"][index][role + "Backend"]["postmasterStart"] = changed
            with self.subTest(index=index), self.assertRaises(ValueError):
                tls_rotation.validate_evidence(invalid, expected)

    def test_checkpoint_uses_public_der_digests_and_preserves_complete_evidence(self):
        value, _ = self.evidence()
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            expected = {}
            # Opaque public bytes isolate the digest/read contract. Native
            # PostgreSQL/TLS tests own certificate parsing and acceptance.
            for index, name in enumerate(role + suffix for role in ("pooler", "postgres") for suffix in ("", "-renewed", "-next")):
                raw = bytes([index])
                expected[name] = "sha256:" + hashlib.sha256(raw).hexdigest()
                encoded = base64.b64encode(raw).decode("ascii")
                (root / (name + ".pem")).write_text("-----BEGIN CERTIFICATE-----\n" + encoded + "\n-----END CERTIFICATE-----\n", encoding="ascii")
            for phase in value["phases"]:
                suffix = "" if phase["name"] == "baseline" else "-renewed" if phase["name"] in ("same-ca", "overlap") else "-next"
                for role in ("pooler", "postgres"):
                    phase[role + "Leaf"] = expected[role + suffix]
            (root / "tls-evidence.json").write_text(json.dumps(value), encoding="ascii")
            self.assertEqual(runner.tls_evidence(root), value)
            value["retiredUpstreamRefused"] = False
            (root / "tls-evidence.json").write_text(json.dumps(value), encoding="ascii")
            with self.assertRaises(ValueError):
                runner.tls_evidence(root)
            (root / "tls-evidence.json").write_text("x" * 65537, encoding="ascii")
            with self.assertRaisesRegex(ValueError, "oversized"):
                runner.tls_evidence(root)


if __name__ == "__main__":
    unittest.main()
