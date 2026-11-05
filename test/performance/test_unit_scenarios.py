import json
import unittest
from unittest.mock import Mock, patch

import runner


class MixedJourneyTests(unittest.TestCase):
    def journey(self):
        return runner.scenarios.Journey("http://127.0.0.1:1", "a" * 24, runner.http, runner.Failure)

    def test_activation_failure_retains_only_numeric_clock_evidence(self):
        journey = self.journey()
        journey.command = Mock(side_effect=runner.Failure("journey-unexpected-status-503"))
        for created, age in [("1970-01-01T00:00:10Z", 1000), (None, None)]:
            with patch.object(runner.scenarios.time, "time_ns", side_effect=[11_000_000_000, 6_100_000_000]), patch.object(runner.scenarios.time, "monotonic_ns", side_effect=[1_000_000_000, 1_100_000_000]), self.assertRaises(runner.Failure):
                journey.activate({"id": "owned", "createdAt": created}, {}, "activate")
            self.assertEqual(-5000, journey.failure_clock["clockAdjustmentMillis"])
            self.assertEqual(age, journey.failure_clock["candidateAgeAtRequestMillis"])
            self.assertEqual(None if age is None else -3900, journey.failure_clock["candidateAgeAtFailureMillis"])

    def test_same_key_pair_requires_one_effect_and_replay_evidence(self):
        journey = self.journey()
        self.assertEqual("one", journey.pair(lambda: ({"id": "one"}, {"Idempotency-Replayed": "true"}))[0]["id"])
        with self.assertRaises(runner.Failure):
            journey.pair(lambda: ({"id": "one"}, {}))
        with self.assertRaises(runner.Failure):
            journey.pair(Mock(side_effect=[({"id": "one"}, {}), ({"id": "two"}, {"Idempotency-Replayed": "true"})]))

    def test_mixed_interrupt_failure_and_finite_plan(self):
        journey = self.journey()
        journey.iteration = Mock(side_effect=RuntimeError("private cause"))
        for seconds in (60, 3600):
            output = {}
            stop = Mock()
            stop.wait.return_value = False
            journey.mixed(stop, seconds, 5, output)
            self.assertEqual("failed", output["status"])
            self.assertEqual(60 if seconds == 3600 else 2, output["plannedPublications"])
            self.assertNotIn("private", str(output))
        output = {}
        stop.wait.return_value = True
        journey.iteration.reset_mock()
        journey.mixed(stop, 60, 5, output)
        journey.iteration.assert_not_called()
        self.assertEqual("failed", output["status"])

    def test_source_sync_acceptance_envelope_is_not_the_job(self):
        journey = self.journey()
        journey.request = Mock(return_value=({"job": {"id": "owned"}, "existing": False}, {}))
        self.assertEqual({"id": "owned"}, journey.command("/federation/sources/owned:sync", {}, "sync", status=202)[0])
        journey.request = Mock(return_value=({"id": "owned"}, {}))
        with self.assertRaisesRegex(runner.Failure, "invalid-sync-acceptance"):
            journey.command("/federation/sources/owned:sync", {}, "sync", status=202)

    def test_public_reconciliation_counts_the_registry_as_well_as_releases(self):
        for entries, succeeds in [(5, True), (4, False)]:
            journey = self.journey()
            journey.namespace, journey.publisher, journey.domain = "namespace", "publisher", "owned.example"
            journey.source = {"id": "source"}
            identifier = "urn:air:owned.example:performance:skill-0"
            journey.command = Mock(return_value=({"id": "one", "revision": {"id": "revision"}}, {"ETag": '"rev-1"', "Idempotency-Replayed": "true"}))
            journey.wait_job = Mock()
            journey.request = Mock(side_effect=[
                ({"nextCursor": "old"}, {}), (None, {}), (None, {}), (None, {}),
                ({"id": "source"}, {"ETag": '"rev-1"'}),
                ({"durableTotal": 1, "items": [{"identifier": identifier}]}, {}),
            ])
            journey.http = Mock()
            journey.http.request.return_value = (200, {}, json.dumps({"entries": [{"identifier": identifier}] + [{"identifier": str(i)} for i in range(entries - 1)]}).encode())
            journey.http.decode = json.loads
            if succeeds:
                journey.iteration(0)
                self.assertEqual(1, journey.stats["committedPublications"])
                self.assertEqual(1, journey.stats["terminalSyncJobs"])
                self.assertEqual(2, journey.stats["idempotentPairs"])
            else:
                with self.assertRaisesRegex(runner.Failure, "snapshot-readback-failed"):
                    journey.iteration(0)
                self.assertEqual(0, journey.stats["committedPublications"])


class JobPollingTests(unittest.TestCase):
    def setUp(self):
        self.journey = runner.scenarios.Journey("http://127.0.0.1:1", "a" * 24, runner.http, runner.Failure)
        self.journey.request = Mock()
        self.now = 100.0
        self.clock = self.enterContext(patch.object(runner.scenarios.time, "monotonic", side_effect=lambda: self.now))
        self.sleep = self.enterContext(patch.object(runner.scenarios.time, "sleep", side_effect=self.advance))

    def advance(self, seconds):
        self.now += seconds

    def reset_polling(self):
        self.now = 100.0
        self.journey.request.reset_mock(return_value=True, side_effect=True)
        self.journey.stats["pollRequests"] = 0
        self.sleep.reset_mock()

    def test_accepted_job_is_not_terminal_success(self):
        for state in ("failed", "cancelled", "dead_letter"):
            with self.subTest(state=state):
                self.reset_polling()
                self.journey.request.return_value = ({"state": state}, {})
                with self.assertRaisesRegex(runner.Failure, "^journey-job-failed$"):
                    self.journey.wait_job({"id": "owned"})
                self.journey.request.assert_called_once_with("GET", "/jobs/owned")
                self.assertEqual(1, self.journey.stats["pollRequests"])
                self.sleep.assert_not_called()

    def test_known_nonterminal_states_continue_until_success(self):
        for state in ("queued", "running", "waiting"):
            with self.subTest(state=state):
                self.reset_polling()
                completed = {"id": "owned", "state": "succeeded"}
                self.journey.request.side_effect = [({"state": state}, {}), (completed, {})]
                self.assertIs(completed, self.journey.wait_job({"id": "owned"}))
                self.assertEqual(2, self.journey.request.call_count)
                self.assertTrue(all(call.args == ("GET", "/jobs/owned") for call in self.journey.request.call_args_list))
                self.assertEqual(2, self.journey.stats["pollRequests"])
                self.sleep.assert_called_once_with(.25)

    def test_success_requires_response_before_monotonic_deadline(self):
        for elapsed in (0, 29.999, 30, 31):
            with self.subTest(elapsed=elapsed):
                self.reset_polling()
                completed = {"id": "owned", "state": "succeeded"}
                def request(*_args):
                    self.now = 100 + elapsed
                    return completed, {}
                self.journey.request.side_effect = request
                if elapsed < 30:
                    self.assertIs(completed, self.journey.wait_job({"id": "owned"}))
                else:
                    with self.assertRaisesRegex(runner.Failure, "^journey-job-deadline$"):
                        self.journey.wait_job({"id": "owned"})
                self.journey.request.assert_called_once_with("GET", "/jobs/owned")
                self.assertEqual(1, self.journey.stats["pollRequests"])
                self.sleep.assert_not_called()

    def test_exhausted_allowance_stops_before_request(self):
        for elapsed in (30, 31):
            with self.subTest(elapsed=elapsed):
                self.clock.side_effect = [100.0, 100 + elapsed]
                with self.assertRaisesRegex(runner.Failure, "^journey-job-deadline$"):
                    self.journey.wait_job({"id": "owned"})
                self.journey.request.assert_not_called()
                self.assertEqual(0, self.journey.stats["pollRequests"])
                self.sleep.assert_not_called()

    def test_sleep_uses_only_remaining_allowance(self):
        def request(*_args):
            self.now = 129.9
            return {"state": "waiting"}, {}
        self.journey.request.side_effect = request
        with self.assertRaisesRegex(runner.Failure, "^journey-job-deadline$"):
            self.journey.wait_job({"id": "owned"})
        self.journey.request.assert_called_once_with("GET", "/jobs/owned")
        self.sleep.assert_called_once()
        self.assertAlmostEqual(.1, self.sleep.call_args.args[0])
        self.assertEqual(130, self.now)
        self.assertEqual(1, self.journey.stats["pollRequests"])

    def test_late_nonterminal_response_does_not_sleep_or_poll_again(self):
        for elapsed in (30, 31):
            with self.subTest(elapsed=elapsed):
                self.reset_polling()
                def request(*_args):
                    self.now = 100 + elapsed
                    return {"state": "running"}, {}
                self.journey.request.side_effect = request
                with self.assertRaisesRegex(runner.Failure, "^journey-job-deadline$"):
                    self.journey.wait_job({"id": "owned"})
                self.journey.request.assert_called_once_with("GET", "/jobs/owned")
                self.sleep.assert_not_called()

    def test_multiple_polls_share_one_deadline(self):
        observations = iter([(112.0, "queued"), (129.9, "waiting")])
        def request(*_args):
            self.assertLessEqual(self.journey.request.call_count, 2, "Polled after the shared deadline")
            self.now, state = next(observations)
            return {"state": state}, {}
        self.journey.request.side_effect = request
        with self.assertRaisesRegex(runner.Failure, "^journey-job-deadline$"):
            self.journey.wait_job({"id": "owned"})
        self.assertEqual(2, self.journey.request.call_count)
        self.assertEqual(2, self.journey.stats["pollRequests"])
        self.assertEqual(2, self.sleep.call_count)
        self.assertEqual(.25, self.sleep.call_args_list[0].args[0])
        self.assertAlmostEqual(.1, self.sleep.call_args_list[1].args[0])
        self.assertEqual(130.0, self.now)

    def test_malformed_or_unknown_state_is_refused_without_more_work(self):
        values = [None, [], True, 1, "succeeded", {}, *({"state": state} for state in (None, [], {}, 1, True, "", "unknown", "canceled"))]
        for value in values:
            with self.subTest(value=value):
                self.reset_polling()
                self.journey.request.return_value = (value, {})
                with self.assertRaisesRegex(runner.Failure, "^journey-invalid-job-state$"):
                    self.journey.wait_job({"id": "owned"})
                self.journey.request.assert_called_once_with("GET", "/jobs/owned")
                self.assertEqual(1, self.journey.stats["pollRequests"])
                self.sleep.assert_not_called()

    def test_request_errors_are_preserved_even_after_deadline(self):
        for failure in (runner.Failure("journey-unexpected-status-503"), runner.http.ClientFailure("unavailable")):
            for elapsed in (0, 31):
                with self.subTest(failure=type(failure), elapsed=elapsed):
                    self.reset_polling()
                    def request(*_args):
                        self.now = 100 + elapsed
                        raise failure
                    self.journey.request.side_effect = request
                    with self.assertRaises(type(failure)) as caught:
                        self.journey.wait_job({"id": "owned"})
                    self.assertIs(failure, caught.exception)
                    self.journey.request.assert_called_once_with("GET", "/jobs/owned")
                    self.assertEqual(1, self.journey.stats["pollRequests"])
                    self.sleep.assert_not_called()


if __name__ == "__main__":
    unittest.main()
