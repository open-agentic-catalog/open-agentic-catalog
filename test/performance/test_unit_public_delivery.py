import base64
import copy
import hashlib
import json
import unittest
from unittest.mock import Mock

import runner
from evidence_test_helpers import profile, report


class PublicDeliveryTests(unittest.TestCase):
    def test_manifest_identity_and_independent_population_are_required(self):
        journey = Mock(http=runner.http, failure=runner.Failure)
        entries = [{"identifier": "registry", "type": runner.public_delivery.REGISTRY}] + [
            {"identifier": f"urn:air:scale.example:scale:local-{i*4+1:06d}", "type": "application/mcp+json"} for i in range(997)]
        entries += [{"identifier": name, "type": "native"} for name in ("agent", "mcp", "skill")]
        expected = {entry["identifier"] for entry in entries}

        def describe(rows, identifiers=expected, header_changes=None):
            raw = json.dumps({"entries": rows}).encode()
            headers = {"etag": '"fixed"', "content-digest": "sha-256=:" + base64.b64encode(hashlib.sha256(raw).digest()).decode() + ":"}
            headers.update(header_changes or {})
            return runner.public_delivery.describe(journey, headers, raw, identifiers)

        descriptor, identifiers = describe(entries)
        self.assertEqual((1001, expected), (descriptor["entries"], identifiers))
        for rows in (entries[:-1], entries + [entries[1]], entries[1:], [dict(entry, type="native") for entry in entries]):
            with self.subTest(rows=len(rows)), self.assertRaises(runner.Failure):
                describe(rows)
        for changed in ({"content-digest": "private-invalid"}, {"etag": 'W/"weak"'}, {"etag": '"bad\nvalue"'}):
            with self.subTest(changed=changed), self.assertRaises(runner.Failure):
                describe(entries, header_changes=changed)
        with self.assertRaises(runner.Failure):
            describe(entries, expected - {"skill"} | {"unrelated"})

    def test_full_and_conditional_reports_reconcile_exact_body_bytes(self):
        manifest = {"entries": 1001, "bytes": 123456, "etag": '"fixed"', "digest": "sha256:" + "a" * 64}
        for conditional in (False, True):
            selected = profile() | {"scenario": "public", "public": manifest | {"conditional": conditional}}
            data = report() | {"profile": selected, "baselineResults": 1001}
            data["measured"]["successfulResponseBytes"] = 0 if conditional else 3 * manifest["bytes"]
            runner.validate_workload(data, selected)
            for change in (1, 3 * manifest["bytes"] + 1):
                wrong = copy.deepcopy(data)
                wrong["measured"]["successfulResponseBytes"] = change
                with self.assertRaises(runner.Failure):
                    runner.validate_workload(wrong, selected)
            for key, value in (("baselineResults", 4), ("responseDigest", "sha256:" + "b" * 64)):
                with self.assertRaises(runner.Failure):
                    runner.validate_workload(data | {key: value}, selected)

    def test_public_delivery_settings_are_fixed(self):
        self.assertEqual({"warmupSeconds": 5, "seconds": 60, "workers": 2, "maxRequests": 50000}, runner.workload_settings("public-delivery", {}))
        for name, value in (("PERF_DURATION_SECONDS", "30"), ("PERF_WARMUP_SECONDS", "0"), ("PERF_WORKERS", "3")):
            with self.subTest(name=name), self.assertRaises(runner.Failure):
                runner.workload_settings("public-delivery", {name: value})

    def test_conditional_read_requires_status_and_no_body(self):
        journey = Mock(api="http://127.0.0.1:1", failure=runner.Failure)
        for status, body in ((304, b""), (200, b""), (304, b"private-response")):
            journey.http.request.return_value = status, {"ETag": '"fixed"'}, body
            if status == 304 and not body:
                runner.public_delivery.read(journey, etag='"fixed"', expected=304)
                request = journey.http.request.call_args.args[0]
                self.assertEqual('"fixed"', request.get_header("If-none-match"))
                self.assertIsNone(request.get_header("Authorization"))
            else:
                with self.assertRaises(runner.Failure):
                    runner.public_delivery.read(journey, etag='"fixed"', expected=304)

    def test_denied_read_cannot_return_manifest_bytes_with_an_error_status(self):
        transport = Mock()
        transport.decode = runner.http.decode
        journey = Mock(api="http://127.0.0.1:1", http=transport, failure=runner.Failure)
        for status, code in ((401, "UNAUTHENTICATED"), (403, "FORBIDDEN"), (404, "NOT_FOUND")):
            problem = {"errorCode": code, "message": "Denied"}
            for document in (problem, {"entries": []}, problem | {"entries": []}, problem | {"errorCode": "INTERNAL"}):
                transport.request.return_value = status, {}, json.dumps(document).encode()
                if document == problem:
                    runner.public_delivery.read(journey, expected=status)
                else:
                    with self.assertRaises(runner.Failure):
                        runner.public_delivery.read(journey, expected=status)
