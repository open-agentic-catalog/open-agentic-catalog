"""Reject event/report mismatches without processes, sockets or PostgreSQL."""

import copy
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import publication_report as report
from publication_report_test_helpers import evidence
import runner
from test_unit_webhook_crash import evidence as crash_evidence


class PublicationReportTests(unittest.TestCase):
    def test_receiver_image_must_match_the_catalog_build(self):
        digest = "sha256:" + "a" * 64
        services = ("api", "worker", "postgres", "federation-fixture", "publication-report")
        identifiers = {name: str(index) * 64 for index, name in enumerate(services, 1)}
        for mismatch in (False, True):
            with self.subTest(mismatch=mismatch), tempfile.TemporaryDirectory() as directory:
                instance = object.__new__(runner.Runner)
                instance.state = Path(directory)
                runner.write_document(instance.state / "version.json", {"version": "owned"})
                instance.environment, instance.base = {"IMAGE": "owned-image"}, {}
                instance.compose, instance.engine = ["compose"], ["docker"]
                instance.report = {"profile": "webhooks", "source": {"worktreeDigest": "source-bound"}}
                def command(args, *_args, **_kwargs):
                    if args[0] == "git":
                        return b"tracked-file\0"
                    if args[0] == "compose":
                        return identifiers[args[-1]].encode()
                    if mismatch and args[1:3] == ["container", "inspect"] and args[-1] == identifiers["publication-report"]:
                        return ("sha256:" + "f" * 64).encode()
                    return digest.encode()
                with mock.patch.object(runner, "command", side_effect=command), mock.patch.object(runner, "digest_files", return_value="source-bound"):
                    if mismatch:
                        with self.assertRaisesRegex(runner.Failure, "publication-report-image-mismatch"):
                            instance.capture_identity(runner.time.monotonic() + 60)
                        self.assertNotIn("images", instance.report)
                    else:
                        instance.capture_identity(runner.time.monotonic() + 60)
                        self.assertEqual(set(instance.report["images"]), set(services))
                        self.assertEqual(instance.report["images"]["publication-report"]["configurationDigest"], digest)

    def test_only_the_exact_event_and_current_state_can_qualify(self):
        trigger = {"id": "receipt", "source": report.SOURCE, "action": "publication.activated", "targetId": "snapshot"}
        active = {"id": "snapshot", "tenantId": report.TENANT, "host": "127.0.0.1", "state": "active",
                  "generation": 2, "audience": "anonymous", "etag": '"' + "a" * 64 + '"', "createdAt": "2026-10-27T12:00:00Z"}
        for selected in (active, None):
            expected = {"lastSuccessfulObservation": {"label": "Last observed publication state", "tenant": report.TENANT,
                "host": "127.0.0.1", "observedAt": "2026-10-27T12:00:01Z", "trigger": trigger, "active": selected}}
            self.assertTrue(report.check_observation(expected, trigger, selected))
            for field in expected["lastSuccessfulObservation"]:
                bad = copy.deepcopy(expected)
                del bad["lastSuccessfulObservation"][field]
                with self.subTest(active=selected is not None, missing=field):
                    self.assertFalse(report.check_observation(bad, trigger, selected))
            for field, value in (("trigger", None), ("trigger", dict(trigger, id="another-receipt")),
                                 ("trigger", dict(trigger, source="another-source")),
                                 ("trigger", dict(trigger, targetId="another-snapshot")),
                                 ("tenant", "another-tenant"), ("host", "another.host"),
                                 ("observedAt", "not-a-time"), ("observedAt", "2026-10-27T12:00:01"),
                                 ("observedAt", True), ("active", {}), ("secret", "private")):
                bad = copy.deepcopy(expected)
                bad["lastSuccessfulObservation"][field] = value
                with self.subTest(field=field, value=value):
                    self.assertFalse(report.check_observation(bad, trigger, selected))
            self.assertFalse(report.check_observation(dict(expected, refreshFailure="catalog_read_failed"), trigger, selected))
        for value in (None, {}, {"lastSuccessfulObservation": None}):
            self.assertFalse(report.check_observation(value, trigger, active))
        for field, value in (("id", "old-snapshot"), ("generation", 1), ("state", "retired")):
            saved = {"lastSuccessfulObservation": {"label": "Last observed publication state", "tenant": report.TENANT,
                "host": "127.0.0.1", "observedAt": "2026-10-27T12:00:01Z", "trigger": trigger,
                "active": dict(active, **{field: value})}}
            self.assertFalse(report.check_observation(saved, trigger, active))

    def test_completion_requires_closed_true_evidence(self):
        control = evidence()
        self.assertTrue(report.valid_evidence(control))
        for field in control:
            missing = dict(control)
            del missing[field]
            self.assertFalse(report.valid_evidence(missing), field)
        for field, value in (("programDigest", "private"), ("completedReceipts", True), ("completedReceipts", 1),
                             ("activationReport", False), ("unpublishReport", 1), ("readerRestricted", False),
                             ("databaseIsolated", None), ("fileMatchesDatabase", False), ("body", "private")):
            self.assertFalse(report.valid_evidence(dict(control, **{field: value})), field)
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)
            value = {"completed": runner.WEBHOOK_CASES, "crash": crash_evidence(), "publicationReport": control}
            runner.write_document(path / "webhooks.json", value)
            self.assertEqual(runner.webhook_evidence(path), value)
            for bad in (None, {}, dict(control, completedReceipts=1)):
                runner.write_document(path / "webhooks.json", dict(value, publicationReport=bad))
                with self.assertRaisesRegex(runner.Failure, "publication-report-evidence"):
                    runner.webhook_evidence(path)
            del value["publicationReport"]
            runner.write_document(path / "webhooks.json", value)
            with self.assertRaisesRegex(runner.Failure, "publication-report-evidence"):
                runner.webhook_evidence(path)

    def test_preparation_keeps_private_inputs_out_of_catalog_mounts(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = Path(directory)
            (fixture / "webhooks").mkdir()
            (fixture / "server.pem").write_bytes(b"certificate")
            (fixture / "server-key.pem").write_bytes(b"private TLS key")
            original = {"privateProfiles": [{"name": "original", "origins": ["https://fixture:8443"]}]}
            runner.write_document(fixture / "webhooks/policy.json", original)
            (fixture / "webhooks/policy.json").chmod(0o444)
            env = {"CATALOG_FUNCTIONAL_PROFILE": "webhooks", "CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY": directory,
                   "CATALOG_FUNCTIONAL_SUBNET": "172.31.1.0/28"}
            report.prepare(env)
            policy = runner.read_document(fixture / "webhooks/policy.json")
            self.assertEqual(policy["privateProfiles"][0], original["privateProfiles"][0])
            self.assertEqual(policy["privateProfiles"][1], {"name": "publication-report", "origins": ["https://api:9443"],
                "addressRanges": [env["CATALOG_FUNCTIONAL_SUBNET"]], "assignedTenantIds": [report.TENANT],
                "trustBundleFile": "/config/webhooks/ca.pem", "transportRef": "direct"})
            for name in ("publication-report", "publication-report-output"):
                self.assertEqual((fixture / name).stat().st_mode & 0o777, 0o700)
            for name in ("server.pem", "server-key.pem", "catalog-token"):
                self.assertEqual((fixture / "publication-report" / name).stat().st_mode & 0o777, 0o600)
                self.assertFalse((fixture / "webhooks" / name).exists())
            self.assertEqual((fixture / "publication-report/server-key.pem").read_bytes(), b"private TLS key")
            self.assertFalse((fixture / "publication-report/receiver.dsn").exists())
            with self.assertRaisesRegex(runner.Failure, "profile"):
                report.prepare(dict(env, CATALOG_FUNCTIONAL_PROFILE="backup-restore"))
