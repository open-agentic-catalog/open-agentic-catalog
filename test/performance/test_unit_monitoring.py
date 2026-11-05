import copy
import json
import unittest
from unittest.mock import patch

import runner


class MonitoringEvidenceTests(unittest.TestCase):
    # Expected shipped scrape identities must not come from the validator.
    expected_targets = [
        {"catalog_deployment": "local", "catalog_role": "api", "job": "catalog-api", "instance": "api:8080"},
        {"catalog_deployment": "local", "catalog_role": "worker", "job": "catalog-worker", "instance": "worker:9090"},
    ]

    def targets(self):
        return [{"health": "up", "labels": copy.deepcopy(labels)} for labels in self.expected_targets]

    def samples(self, timestamp=100, evaluated=200):
        return [{"metric": copy.deepcopy(labels), "value": [evaluated, str(timestamp)]} for labels in self.expected_targets]

    def test_target_identity_rejects_duplicate_foreign_and_wrong_role(self):
        runner.monitoring_targets(self.targets())
        controls = [[], self.targets()[:1], self.targets() + self.targets()[:1], [self.targets()[0]] * 2]
        for field, value in [("catalog_deployment", "foreign"), ("catalog_role", "worker"), ("job", "other"), ("instance", "api2:8080")]:
            targets = self.targets()
            targets[0]["labels"][field] = value
            controls.append(targets)
        for health in ["down", None]:
            targets = self.targets()
            targets[0]["health"] = health
            controls.append(targets)
        for targets in controls:
            with self.subTest(targets=targets), self.assertRaises(runner.Failure):
                runner.monitoring_targets(targets)

    def test_sample_time_uses_underlying_value_and_exact_identity(self):
        self.assertEqual({"api": 100, "worker": 100}, runner.monitoring_sample_times(self.samples(evaluated=999999)))
        controls = [[], self.samples()[:1], [self.samples()[0]] * 2, [{}], None]
        for value in ["NaN", "Inf", "-1", "0", "invalid", None]:
            samples = self.samples()
            samples[0]["value"][1] = value
            controls.append(samples)
        for field, value in [("instance", "foreign"), ("catalog_role", "worker"), ("catalog_deployment", "other"), ("private", "secret")]:
            samples = self.samples()
            samples[0]["metric"][field] = value
            controls.append(samples)
        for samples in controls:
            with self.assertRaises(runner.Failure):
                runner.monitoring_sample_times(samples)

    def test_recovery_requires_both_scrape_times_to_advance(self):
        stale = self.samples(timestamp=100, evaluated=999999)
        partial = self.samples(timestamp=101)
        partial[1]["value"][1] = "100"
        with patch.object(runner.time, "monotonic", return_value=0), patch.object(runner.time, "sleep"), \
                patch.object(runner, "monitoring_targets") as targets:
            query = unittest.mock.Mock(side_effect=[stale, partial, self.samples(timestamp=102)])
            result = runner.recovered_monitoring_samples(lambda: self.targets(), query)
            self.assertEqual({"baseline": {"api": 100, "worker": 100}, "fresh": {"api": 102, "worker": 102}}, result)
            self.assertEqual(3, targets.call_count)
            self.assertTrue(query.call_args.args[0].startswith("timestamp("))
        # A request completing after the deadline cannot become a success.
        with patch.object(runner.time, "monotonic", side_effect=[0, 1, 2, 61, 62]), patch.object(runner.time, "sleep"):
            with self.assertRaises(runner.Failure):
                runner.recovered_monitoring_samples(lambda: self.targets(), unittest.mock.Mock(side_effect=[self.samples(100), self.samples(102)]))

    def test_newer_stored_samples_after_restart_still_require_a_further_scrape(self):
        # A scrape during collector stop may be newer than the pre-outage
        # value (100). Repeated stored 101s after restart remain insufficient.
        with patch.object(runner.time, "monotonic", side_effect=[0, 1, 2, 3, 61]), patch.object(runner.time, "sleep"):
            with self.assertRaisesRegex(runner.Failure, "recovery-sample-deadline"):
                runner.recovered_monitoring_samples(lambda: self.targets(), lambda _: self.samples(101, 999999))

    def test_outage_read_is_authorized_and_checks_known_retained_content(self):
        seeded = {"resourceId": "owned", "identifier": "urn:example:owned", "kind": "agent"}
        resource = {"id": "owned", "canonicalIdentifier": "urn:example:owned", "kind": "agent"}
        with patch.object(runner.http, "request", return_value=(200, {}, json.dumps(resource).encode())) as request:
            self.assertEqual({"authorizedRead": True, "retainedContentMatched": True}, runner.monitoring_retained_read("http://127.0.0.1:1", seeded))
            self.assertEqual("Bearer local-reader", request.call_args.args[0].get_header("Authorization"))
            self.assertEqual("http://127.0.0.1:1/api/v1/resources/owned", request.call_args.args[0].full_url)
        for status, body in [(503, resource), (200, resource | {"id": "other"}), (200, resource | {"canonicalIdentifier": "other"}), (200, resource | {"kind": "skill"}), (200, {})]:
            with patch.object(runner.http, "request", return_value=(status, {}, json.dumps(body).encode())), self.assertRaises(runner.Failure):
                runner.monitoring_retained_read("http://127.0.0.1:1", seeded)
