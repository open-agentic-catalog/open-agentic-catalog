"""In-memory checks; no tools, sockets, databases or real application processes."""
import json
import unittest

import runner
import abrupt


class ConfigurationTests(unittest.TestCase):
    def test_protected_session_contract(self):
        value = runner.configuration()
        for required in ("pool_mode = session", "auth_type = scram-sha-256",
                         "server_tls_sslmode = verify-full", "client_tls_sslmode = require",
                         "server_reset_query = DISCARD ALL", "reserve_pool_size = 0"):
            self.assertIn(required, value)
        for forbidden in ("auth_query", "auth_user", "ignore_startup_parameters", "catalog_owner", "password="):
            self.assertNotIn(forbidden, value)

    def test_capacity_and_mode_controls_are_distinct(self):
        value = runner.configuration()
        self.assertIn("capacity = host=writer port=5432 dbname=catalog_test pool_size=1", value)
        self.assertIn("pool_mode=transaction pool_size=1", value)
        self.assertIn("pool_mode=statement pool_size=1", value)
        self.assertNotIn("catalog_api = pool_size=", value)


class ReportTests(unittest.TestCase):
    def events(self, cases):
        return "\n".join(json.dumps({"Action": "pass", "Test": case}) for case in cases)

    def test_complete_cases_required(self):
        cases = {*runner.REQUIRED_CASES, "TestPgBouncer"}
        self.assertEqual(runner.completed_cases(self.events(cases)), sorted(runner.REQUIRED_CASES))
        for missing in cases:
            with self.subTest(missing=missing), self.assertRaisesRegex(RuntimeError, "missing"):
                runner.completed_cases(self.events(cases - {missing}))

    def test_failed_skipped_and_invalid_events(self):
        passing = self.events({*runner.REQUIRED_CASES, "TestPgBouncer"})
        for event in ('{"Action":"fail"}', '{"Action":"skip"}', 'not-json', 'null', '[]'):
            with self.subTest(event=event), self.assertRaises(RuntimeError):
                runner.completed_cases(passing + "\n" + event)


class StandbyTests(unittest.TestCase):
    def test_restart_requires_owned_marker_and_preserves_fresh_refusal(self):
        script = 'test ! -e "$$PGDATA/PG_VERSION"\nexec postgres -c ssl=on'
        value = runner.owned_standby_restart(script)
        self.assertIn('test "$$(cat "$$PGDATA/qualification-owner")" = "$$CATALOG_FIXTURE_OWNER"', value)
        self.assertIn('test ! -e "$$PGDATA/PG_VERSION"', value)
        self.assertIn('printf "%s" "$$CATALOG_FIXTURE_OWNER" > "$$PGDATA/qualification-owner"', value)
        self.assertNotIn('"$PGDATA', value)
        self.assertNotIn('rm ', value)

    def test_drift_is_rejected(self):
        for script in ('', 'exec postgres -c ssl=on', 'test ! -e "$$PGDATA/PG_VERSION"'):
            with self.subTest(script=script), self.assertRaisesRegex(RuntimeError, "changed"):
                runner.owned_standby_restart(script)


class AbruptTests(unittest.TestCase):
    def held(self):
        identifiers = ["00000000-0000-7000-8000-%012d" % i for i in range(1, 5)]
        return dict(zip(("tenant", "reservation", "job", "outbox"), identifiers, strict=True),
                    outboxDocument={"id": identifiers[3], "tenant_id": identifiers[0]},
                    listeners=["http://127.0.0.1:%d" % (19000+i) for i in range(4)],
                    processes=[{"instanceId": "process-%d" % i, "startedAt": "2026-10-31T12:00:00Z"} for i in range(4)])

    def test_held_input_and_invalid_identifiers(self):
        value = self.held()
        self.assertEqual(abrupt.validate_held_input(json.dumps(value)), value)
        for key in ("tenant", "reservation", "job", "outbox"):
            bad = dict(value, **{key: "' OR true --"})
            with self.subTest(key=key), self.assertRaises(ValueError):
                abrupt.validate_held_input(json.dumps(bad))

    def test_missing_oversized_and_mismatched_evidence(self):
        for raw in ("x" * 65537, "null", "{}", "[]"):
            with self.subTest(raw=raw[:20]), self.assertRaises(ValueError):
                abrupt.validate_held_input(raw)
        value = self.held()
        value["processes"][1] = value["processes"][0]
        with self.assertRaises(ValueError):
            abrupt.validate_held_input(json.dumps(value))
        value = self.held()
        value["outboxDocument"]["id"] = value["tenant"]
        with self.assertRaises(ValueError):
            abrupt.validate_held_input(json.dumps(value))

    def test_report_requires_each_positive_proof_and_replayed_wal(self):
        value = {"selection": "abrupt", "heldOnPrimary": True, "heldOnStandby": True,
                 "writerFenced": True, "promoted": True, "barrier": "1/FF", "replayedBarrier": "2/0",
                 "processes": self.held()["processes"]}
        value.update(phase="recovery", oldWriterExitCode=137, jobAttempt=1, jobOwner="worker-owned",
                     recoverySeconds=3.0, outboxSHA256="sha256:"+"a"*64,
                     **{key: self.held()[key] for key in ("reservation", "job", "outbox")})
        value.update(degradedListeners=4,recoveredListeners=4,
                     outcomes=dict.fromkeys(abrupt.RECOVERY_PROOFS,True)|{"jobAttempt":2,"jobOwner":"worker-recovered"})
        runner.validate_abrupt_evidence(value)
        for key in value:
            bad = dict(value)
            del bad[key]
            with self.subTest(key=key), self.assertRaises(RuntimeError):
                runner.validate_abrupt_evidence(bad)
        for key in ("heldOnPrimary", "heldOnStandby", "writerFenced", "promoted"):
            with self.subTest(key=key), self.assertRaises(RuntimeError):
                runner.validate_abrupt_evidence(dict(value, **{key: False}))
        with self.assertRaisesRegex(RuntimeError,"other worker"):
            runner.validate_abrupt_evidence(dict(value,outcomes=dict(value["outcomes"],jobOwner=value["jobOwner"])))
        for overrides in ({"replayedBarrier":"1/FE"}, {"oldWriterExitCode":0}, {"jobAttempt":2,"jobOwner":"worker-recovered"},
                          {"recoverySeconds":181}, {"outboxSHA256":"invalid"}, {"job":"invalid"},
                          {"processes":[value["processes"][0]]*4}):
            with self.subTest(overrides=overrides), self.assertRaises(RuntimeError):
                runner.validate_abrupt_evidence(dict(value, **overrides))


    def test_listener_scope_refuses_unowned_or_ambiguous_origins(self):
        for bad in ("https://127.0.0.1:19000", "http://example.com:19000", "http://user@127.0.0.1:19000",
                    "http://127.0.0.1:19000/path", "http://127.0.0.1:19000?q=x", "http://127.0.0.1"):
            value=self.held()
            value["listeners"][1]=bad
            with self.subTest(bad=bad),self.assertRaises(ValueError):
                abrupt.validate_held_input(json.dumps(value))

    def test_every_recovery_proof_is_required(self):
        value=dict.fromkeys(abrupt.RECOVERY_PROOFS,True)|{"jobAttempt":2,"jobOwner":"worker-recovered"}
        abrupt.validate_recovery_outcomes(value)
        for key in value:
            missing=dict(value)
            del missing[key]
            with self.subTest(key=key),self.assertRaises(ValueError):
                abrupt.validate_recovery_outcomes(missing)
        for key in abrupt.RECOVERY_PROOFS:
            with self.subTest(key=key),self.assertRaises(ValueError):
                abrupt.validate_recovery_outcomes(dict(value,**{key:False}))
        for owner in ("",True,"x"*129):
            with self.subTest(owner=owner),self.assertRaises(ValueError):
                abrupt.validate_recovery_outcomes(dict(value,jobOwner=owner))
        for attempt in (0,1,True,"2"):
            with self.subTest(attempt=attempt),self.assertRaises(ValueError):
                abrupt.validate_recovery_outcomes(dict(value,jobAttempt=attempt))


if __name__ == "__main__":
    unittest.main()
