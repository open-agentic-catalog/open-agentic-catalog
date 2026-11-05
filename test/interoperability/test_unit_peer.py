"""Peer report and cleanup calibrations without HF, sockets or subprocesses."""

import contextlib
import copy
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch

from common import Result
import peer_catalog as catalog
import peer_contract as c
import peer_runner as p
import runner as shared


def incomplete_cases():
    return [dict(name=name, status="fail", disposition=disposition, evidence={"error": "not executed"})
            for name, disposition in c.DISPOSITIONS.items()]


class PeerContracts(unittest.TestCase):
    def test_conformance_inputs_bind_shared_dev_manifest_and_lock(self):
        self.assertIn(shared.ROOT / "pyproject.toml", p.INPUTS)
        self.assertIn(shared.ROOT / "uv.lock", p.INPUTS)
        self.assertNotIn(p.CHECKER.with_name("requirements.txt"), p.INPUTS)

    def test_child_reports_require_the_exact_phase_and_consistent_exit_status(self):
        row = c.outcome("outage", {"preserved_members": 10})
        valid = {"state": {}, "cases": [row]}
        c.check_phase("outage", valid, 0)
        c.check_phase("outage", {"state": {}, "cases": [], "error": "outage assertion failed"}, 1)
        for payload, code in ((None, 0), ({**valid, "state": []}, 0),
                              ({**valid, "cases": [{}]}, 0), ({**valid, "cases": []}, 0),
                              ({**valid, "cases": [row, row]}, 0),
                              ({**valid, "cases": [c.outcome("recovery", c.SUMMARY["recovery"])]}, 0),
                              ({**valid, "error": "failure"}, 0), (valid, 1),
                              (valid, True), (valid, 124),
                              ({**valid, "cases": [{**row, "status": "fail"}]}, 0),
                              ({**valid, "cases": [row, row], "error": "failure"}, 1)):
            with self.subTest(payload=payload, code=code), self.assertRaises(ValueError):
                c.check_phase("outage", payload, code)

    def test_reports_reject_missing_duplicate_client_and_empty_evidence(self):
        cases = incomplete_cases()
        self.assertEqual(c.check_cases(cases), 0)
        for change in (lambda rows: rows.pop(), lambda rows: rows.__setitem__(0, rows[1]),
                       lambda rows: rows[0].update(name="all-results"),
                       lambda rows: rows[0].update(disposition="supported-but-different"),
                       lambda rows: rows[0].update(status="skip"), lambda rows: rows[0].update(evidence={})):
            rows = copy.deepcopy(cases)
            change(rows)
            with self.subTest(rows=rows[0]), self.assertRaises(ValueError):
                c.check_cases(rows)

    def test_fixed_summary_does_not_accept_missing_or_boolean_counts(self):
        for name, fields in c.SUMMARY.items():
            if name == "wire-referrals":
                continue
            for key, expected in fields.items():
                rows = incomplete_cases()
                index = next(i for i, row in enumerate(rows) if row["name"] == name)
                evidence = dict(fields)
                evidence.pop(key)
                rows[index] = c.outcome(name, evidence)
                with self.subTest(name=name, key=key), self.assertRaises(ValueError):
                    c.check_cases(rows)
                if type(expected) is int:
                    rows[index] = c.outcome(name, {**fields, key: True})
                    with self.assertRaises(ValueError):
                        c.check_cases(rows)

    def test_indexed_evidence_requires_counts_and_actual_interface(self):
        evidence = dict(count=10, supported=3, profile=c.CURRENT, enumerable=True, search="", type="hybrid")
        cases = incomplete_cases()
        index = next(i for i, row in enumerate(cases) if row["name"] == "indexed-challenge")
        cases[index] = c.outcome("indexed-challenge", evidence)
        self.assertEqual(c.check_cases(cases), 1)
        for field, value in (("count", 8), ("supported", 10), ("profile", c.LEGACY),
                             ("enumerable", False), ("search", "https://unexpected/search"), ("type", "ard_registry")):
            cases[index] = c.outcome("indexed-challenge", {**evidence, field: value})
            with self.subTest(field=field), self.assertRaises(ValueError):
                c.check_cases(cases)

    def test_route_evidence_proves_approved_only_not_just_success(self):
        root = 'INFO: 172.26.0.2:1234 - "POST /search HTTP/1.1" 200 OK\n'
        nested = 'INFO: 172.26.0.2:1234 - "POST /registries/nested/search HTTP/1.1" 200 OK\n'
        before = c.parse_routes(root)
        delta = c.route_delta(before, c.parse_routes(root + root + nested))
        c.validate_routes(delta)
        for altered in ({}, {**delta, "POST /registries/deep/search 200": 1},
                        {**delta, "POST /registries/tools/search 200": 1}, {**delta, "POST /search 200": 2}):
            with self.subTest(altered=altered), self.assertRaises(ValueError):
                c.validate_routes(altered)
        with self.assertRaisesRegex(ValueError, "truncated"):
            c.route_delta(before, {})

    def test_search_contract_and_schema_errors(self):
        validator = Mock()
        validator.iter_errors.return_value = []
        item = dict(identifier="urn:air:discover.dev:challenge:test", score=80, source="discover:challenge")
        referral = dict(identifier="urn:air:discover.dev:registry:child", displayName="Child", type=c.REGISTRY,
                        url="https://peer.example/search", tags=["extension"])
        valid = {"results": [item], "referrals": [referral]}
        c.validate_search(valid, validator)
        c.validate_search({"results": [{**item, "score": 8e1}]}, validator)
        validator.iter_errors.assert_called()
        mutations = (lambda v: v.update(unknown=True), lambda v: v.update(results={}),
                     lambda v: v["results"].append(item), lambda v: v["results"][0].update(score=True),
                     lambda v: v["results"][0].update(score=80.5), lambda v: v["results"][0].update(source="relative"),
                     lambda v: v["results"][0].pop("identifier"), lambda v: v["referrals"][0].pop("url"),
                     lambda v: v["referrals"][0].update(type=c.MCP))
        for mutation in mutations:
            value = copy.deepcopy(valid)
            mutation(value)
            with self.subTest(value=value), self.assertRaises(AssertionError):
                c.validate_search(value, validator)
        validator.iter_errors.return_value = ["invalid schema field"]
        with self.assertRaisesRegex(AssertionError, "schema"):
            c.validate_search(valid, validator)

    def test_native_failure_must_be_the_exact_search_backend_failure(self):
        output = ("✗ POST /search request failed: 500 Internal Server Error\n"
                  "CONFORMANCE STATUS: FAIL\nFound 1 critical specification errors.")
        evidence = dict(exit_code=1, output=output)
        c.check_checker("primary", "registry", evidence)
        for altered in ({**evidence, "exit_code": 0}, {**evidence, "exit_code": True},
                        {**evidence, "output": "timeout 500"},
                        {**evidence, "output": output.replace("Found 1", "Found 2")},
                        {**evidence, "output": output.replace("500", "403")}):
            with self.subTest(altered=altered), self.assertRaises(ValueError):
                c.check_checker("primary", "registry", altered)
        with self.assertRaises(ValueError):
            c.check_checker("challenge", "registry", {"exit_code": 0, "output": "no tests"})

    def test_schema_checker_requires_every_entry_and_warning_evidence(self):
        warnings = ["⚠ descriptor warning", "⚠ descriptor warning"]
        output = ("✓ Manifest validates against ArdManifest.\n✓ All 2 entries validate against ArdEntry.\n"
                  + "\n".join(warnings) + "\nValidated with 0 critical specification errors and 2 warnings.")
        evidence = dict(exit_code=0, output=output, warnings=warnings, summary_warnings=2)
        c.check_checker("primary", "manifest", evidence)
        for changed in ({**evidence, "output": output.replace("All 2", "All 1")},
                        {**evidence, "output": output + "\nskipping schema"}, {**evidence, "warnings": []},
                        {**evidence, "summary_warnings": 3}, {**evidence, "exit_code": 1}):
            with self.subTest(changed=changed), self.assertRaises(ValueError):
                c.check_checker("primary", "manifest", changed)

    def test_private_policy_is_exact_and_public_egress_disabled(self):
        origins = {"challenge": "https://172.26.0.10:8443", "primary": "https://172.26.0.11:8443"}
        policy = p.policy(origins, ["172.26.0.10", "172.26.0.11"])
        self.assertEqual(policy["public"], {"mode": "disabled"})
        for profile in policy["privateProfiles"]:
            self.assertEqual(profile["origins"], list(origins.values()))
            self.assertEqual(profile["assignedTenantIds"], [c.TENANT])
            self.assertEqual(profile["addressRanges"], ["172.26.0.10/32", "172.26.0.11/32"])
        self.assertNotEqual(*[profile["trustBundleFile"] for profile in policy["privateProfiles"]])

    def test_address_reservation_requires_new_private_internal_bridge(self):
        info = dict(Internal=True, Containers={}, IPAM={"Config": [{"Subnet": "172.26.0.0/16"}]})
        with patch.object(shared, "docker", return_value=Result(0, json.dumps([info]), "")):
            self.assertEqual(p.addresses("owned"), ["172.26.0.10", "172.26.0.11"])
        for changed in ({**info, "Internal": False}, {**info, "Containers": {"retained": {}}},
                        {**info, "IPAM": {"Config": [{"Subnet": "8.8.8.0/24"}]}},
                        {**info, "IPAM": {"Config": [{"Subnet": "172.26.0.0/30"}]}}):
            with patch.object(shared, "docker", return_value=Result(0, json.dumps([changed]), "")), self.assertRaises(RuntimeError):
                p.addresses("owned")


class SnapshotEvidence(unittest.TestCase):
    def test_completion_requires_exact_snapshot_source_tenant_job_and_attempt(self):
        snapshot = dict(id="snapshot", sourceId="source", sourceRevision=2, tenantId=c.TENANT, state="failed")
        event = dict(id="audit", targetId="snapshot", action="source.sync.failed", tenantId=c.TENANT,
                     details={"facts": dict(sourceId="source", sourceRevision=2, state="failed",
                                            jobId="job", jobKind="source.sync", jobAttempt=1)})
        with patch.object(catalog, "pages", return_value=[event]) as pages:
            self.assertEqual(catalog.sync_attempt(snapshot, "job"), 1)
            pages.assert_called_once_with("/api/v1/audit-events", request=None, action="source.sync.failed")
        for facts in ({"jobId": "old-job"}, {"jobKind": "other"}, {"sourceId": "other"},
                      {"sourceRevision": 1}, {"state": "complete"}, {"jobAttempt": True}, {"jobAttempt": 0}):
            changed = copy.deepcopy(event)
            changed["details"]["facts"].update(facts)
            with self.subTest(facts=facts), patch.object(catalog, "pages", return_value=[changed]), self.assertRaises(AssertionError):
                catalog.sync_attempt(snapshot, "job")
        for events in ([], [event, event], [{**event, "targetId": "old-snapshot"}],
                       [{**event, "tenantId": "other"}], [{**event, "action": "source.sync.completed"}]):
            with self.subTest(events=events), patch.object(catalog, "pages", return_value=events), self.assertRaises(AssertionError):
                catalog.sync_attempt(snapshot, "job")

    def test_outage_ignores_old_failures_and_uses_audited_not_inflight_attempt(self):
        old = dict(id="old-failure", state="failed")
        new = dict(id="new-failure", state="failed")
        state = {"challenge": dict(source_id="source", snapshot_id="complete", identities=[["key", "digest"]])}
        cases = []
        def wait(_label, callback, **_kwargs):
            self.assertIsNone(callback(), "old failed snapshot must not satisfy this outage")
            return callback()
        with patch.object(catalog, "pages", side_effect=[[old], [old], [old, new]]), \
                patch.object(catalog, "enqueue", return_value={"id": "job"}), \
                patch.object(catalog, "wait_for", side_effect=wait), \
                patch.object(catalog, "sync_attempt", return_value=1) as attempt, \
                patch.object(catalog, "source", return_value=({"lastCompleteSnapshotId": "complete"}, {})), \
                patch.object(catalog, "current", return_value=[{"sourceKey": "key", "identityDigest": "digest"}]), \
                patch.object(catalog, "api", return_value=({"attempt": 2, "state": "running"}, {})):
            catalog.outage(state, cases)
        attempt.assert_called_once_with(new, "job")
        self.assertEqual(state["recovery_job"], "job")
        self.assertEqual(state["failed_attempt"], 1)
        self.assertEqual(cases[0]["evidence"]["failed_snapshot_id"], "new-failure")


class PeerCleanup(unittest.TestCase):
    def test_partial_certificate_setup_timeout_interruption_and_cleanup_failure(self):
        for failure in ("certificate", "timeout", "interrupt", "cleanup"):
            calls = []
            def certificates(private, _ips):
                private.mkdir(mode=0o700)
                (private / "peer.key").write_text("private synthetic key")
                if failure == "interrupt":
                    raise KeyboardInterrupt()
                if failure == "timeout":
                    p.deadline_exceeded(None, None)
                raise RuntimeError("injected certificate generation failure")
            def docker(*args, **_):
                calls.append(args)
                return Result(1, "", "denied") if failure == "cleanup" and args[0] == "rm" else Result(0, "", "")
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as name, \
                    patch.object(shared, "OUTPUT", Path(name)), patch.object(shared, "local_daemon", return_value="amd64"), \
                    patch.object(shared, "installed_image", return_value={"image": "sha256:test"}), \
                    patch.object(shared, "fingerprint", return_value="source"), patch.object(p, "preflight", return_value={}), \
                    patch.object(p, "search_validator"), patch.object(p, "calibrate_search", return_value={}), \
                    patch.object(p, "addresses", return_value=["172.26.0.10", "172.26.0.11"]), \
                    patch.object(p, "certificates", side_effect=certificates), patch.object(p.os, "getuid", return_value=1000), \
                    patch.object(shared, "docker", side_effect=docker), patch.object(p, "run", return_value=Result(0, "commit", "")), \
                    contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(p.check(), 1)
                directory = next(Path(name).iterdir())
                self.assertFalse((directory / "private").exists())
                self.assertFalse((directory / "build").exists())
                report = json.loads((directory / "result.json").read_text())
                self.assertEqual(report["total"], 26)
                self.assertEqual(report["passed"], 0)
                self.assertEqual(report["status"], "cleanup-failed" if failure == "cleanup" else "interrupted" if failure == "interrupt" else "qualification-failed")
                cleanup = [args for args in calls if args[0] in ("rm", "image") or args[:2] == ("network", "rm")]
                self.assertEqual(len(cleanup), 5)
                self.assertTrue(all(args[-1].startswith("open-agentic-catalog-hf-peer-") for args in cleanup))

    def test_malformed_phase_report_cannot_bypass_owned_cleanup(self):
        calls = []
        def certificates(private, _ips):
            private.mkdir(mode=0o700)
            (private / "peer.key").write_text("private synthetic key")
        def docker(*args, **_):
            calls.append(args)
            if args[0] == "inspect":
                return Result(0, "172.26.0.10" if args[-1].endswith("challenge") else "172.26.0.11", "")
            if args[0] == "run" and "/harness/peer_catalog.py" in args:
                return Result(0, json.dumps({"cases": [{}], "state": {}}), "")
            return Result(0, "{}", "")
        with tempfile.TemporaryDirectory() as name, contextlib.ExitStack() as stack:
            for target, attribute, value in (
                (shared, "OUTPUT", Path(name)), (shared, "local_daemon", Mock(return_value="amd64")),
                (shared, "installed_image", Mock(return_value={"image": "sha256:test"})),
                (shared, "fingerprint", Mock(return_value="source")), (shared, "build_catalog", Mock()),
                (shared, "docker", Mock(side_effect=docker)),
                (p, "preflight", Mock(return_value={})), (p, "search_validator", Mock()),
                (p, "calibrate_search", Mock(return_value={})), (p, "PeerHTTP", Mock()),
                (p, "qualify", Mock(return_value={})), (p, "certificates", Mock(side_effect=certificates)),
                (p, "addresses", Mock(return_value=["172.26.0.10", "172.26.0.11"])),
                (p.os, "getuid", Mock(return_value=1000)), (p, "run", Mock(return_value=Result(0, "commit", ""))),
            ):
                stack.enter_context(patch.object(target, attribute, value))
            stack.enter_context(contextlib.redirect_stdout(io.StringIO()))
            self.assertEqual(p.check(), 1)
            directory = next(Path(name).iterdir())
            self.assertFalse((directory / "private").exists())
            self.assertFalse((directory / "build").exists())
            report = json.loads((directory / "result.json").read_text())
            self.assertNotEqual(report["status"], "passed")
            self.assertEqual(report["cleanup_errors"], [])
            owned = report["owned"]
            for container in [owned["catalog"], *owned["peers"].values(), *owned["clients"]]:
                self.assertIn(("rm", "-f", container), calls)
            self.assertIn(("network", "rm", owned["network"]), calls)
            self.assertIn(("image", "rm", owned["image"]), calls)
