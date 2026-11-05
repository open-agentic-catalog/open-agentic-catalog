import base64
import json
import subprocess
import unittest
from unittest.mock import patch

from http_client import ClientFailure, MAX_HEADERS, MAX_RESPONSE
from response_contract import validate

from federation import Client


class ResponseContractTests(unittest.TestCase):
    def test_limits_are_enforced_before_starting_validator(self):
        with patch.dict("os.environ", {"CATALOG_FUNCTIONAL_CONTRACT_DRIVER": "/owned/contract"}):
            for headers, body in (({}, b"x" * (MAX_RESPONSE + 1)),
                                  ({"X": "x" * MAX_HEADERS}, b"{}")):
                with self.subTest(body_bytes=len(body), header_count=len(headers)):
                    with patch("subprocess.run") as run:
                        with self.assertRaisesRegex(ClientFailure, "contract limit"):
                            validate("GET", "/api/v1/namespaces", 200, headers, body)
                        run.assert_not_called()
            # Limits are inclusive, not an accidental one-byte reduction.
            with patch("subprocess.run") as run:
                run.return_value.returncode = 0
                validate("GET", "/metrics", 200, {"X": "x" * (MAX_HEADERS - 1)}, b"x" * MAX_RESPONSE)
                run.assert_called_once()

    def test_launch_error_does_not_expose_private_details(self):
        with patch.dict("os.environ", {"CATALOG_FUNCTIONAL_CONTRACT_DRIVER": "/owned/contract"}):
            with patch("subprocess.run", side_effect=OSError("private-secret")):
                with self.assertRaises(ClientFailure) as failure:
                    validate("GET", "/api/v1/namespaces", 200, {}, b"private-secret")
                self.assertEqual(str(failure.exception), "functional response validator failed")
                self.assertTrue(failure.exception.__suppress_context__)

    def test_unexpected_response_diagnostics_are_closed(self):
        for body, expected in ((b'{"code":"REQUEST_DEADLINE_EXCEEDED","detail":"private-secret"}', "REQUEST_DEADLINE_EXCEEDED"),
                               (b'{"code":"DEPENDENCY_UNAVAILABLE","detail":"private-secret"}', "DEPENDENCY_UNAVAILABLE"),
                               (b'{"code":"private-secret"}', "unclassified"), (b'private-secret', "unclassified")):
            with patch("federation.bounded_request", return_value=(503, {}, body)):
                with self.assertRaises(AssertionError) as failure:
                    Client("http://127.0.0.1:18080").request("GET", "/api/v1/capabilities")
                self.assertIn(expected, str(failure.exception))
                self.assertNotIn("private-secret", str(failure.exception))

    def test_requires_driver(self):
        with patch.dict("os.environ", {}, clear=True):
            with self.assertRaises(ClientFailure):
                validate("GET", "/api/v1/namespaces", 200, {}, b"{}")

    def test_private_stdin_and_failures(self):
        with patch.dict("os.environ", {"CATALOG_FUNCTIONAL_CONTRACT_DRIVER": "/owned/contract"}):
            with patch("subprocess.run") as run:
                run.return_value.returncode = 0
                validate("POST", "/api/v1/namespaces", 201, {"Content-Type": "application/json"}, b"private-secret")
                args, kwargs = run.call_args
                self.assertEqual(args[0], ["/owned/contract", "--validate-stdin"])
                self.assertEqual(kwargs["stderr"], subprocess.DEVNULL)
                self.assertEqual(kwargs["stdout"], subprocess.DEVNULL)
                self.assertEqual(kwargs["timeout"], 10)
                self.assertFalse(kwargs["check"])
                value = json.loads(kwargs["input"])
                self.assertEqual(value["method"], "POST")
                self.assertEqual(value["path"], "/api/v1/namespaces")
                self.assertEqual(value["status"], 201)
                self.assertEqual(value["headers"], {"Content-Type": ["application/json"]})
                self.assertEqual(base64.b64decode(value["body"]), b"private-secret")
                run.return_value.returncode = 1
                with self.assertRaisesRegex(ClientFailure, "contract mismatch"):
                    validate("POST", "/api/v1/namespaces", 201, {}, b"private-secret")
                run.side_effect = subprocess.TimeoutExpired("private-secret", 10)
                with self.assertRaisesRegex(ClientFailure, "validator failed"):
                    validate("POST", "/api/v1/namespaces", 201, {}, b"private-secret")
