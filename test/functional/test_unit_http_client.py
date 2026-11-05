"""Isolated fixture HTTP/TLS policy; real exchanges use the packaged gates."""

from pathlib import Path
from datetime import datetime, timezone
import ssl
from email.message import Message
import unittest
from unittest.mock import Mock, patch

import http_client


class ResponseDateTests(unittest.TestCase):
    def test_only_one_bounded_utc_date_becomes_a_transient_timestamp(self):
        value = "Thu, 22 Oct 2026 12:00:00 GMT"
        expected = int(datetime(2026, 10, 22, 12, tzinfo=timezone.utc).timestamp())
        for name in ("Date", "date", "DATE"):
            self.assertEqual(http_client.response_date({name: value, "private": "private-secret"}), expected)
        for headers in ({}, {"Date": None}, {"Date": 1}, {"Date": "private-secret"}, {"Date": "x" * 65},
                        {"Date": value, "date": value}, {"Date": "Thu, 22 Oct 2026 12:00:00 +0100"},
                        {"Date": "Thu, 22 Oct 2026 12:00:00"}):
            with self.subTest(headers=headers):
                self.assertIsNone(http_client.response_date(headers))


class FixtureTLSContextTests(unittest.TestCase):
    def test_private_ca_and_explicit_floor_preserve_identity_verification(self):
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
        context.minimum_version = ssl.TLSVersion.MINIMUM_SUPPORTED
        cafile = Path("fixture/trust/functional-ca.pem")
        with patch.object(http_client.ssl, "create_default_context", return_value=context) as create:
            result = http_client.fixture_tls_context(cafile)
        create.assert_called_once_with(cafile=cafile)
        self.assertIs(result, context)
        self.assertEqual(result.minimum_version, ssl.TLSVersion.TLSv1_2)
        self.assertEqual(result.verify_mode, ssl.CERT_REQUIRED)
        self.assertTrue(result.check_hostname)

    def test_trust_load_failure_propagates_without_fallback(self):
        for error in (FileNotFoundError("missing fixture CA"), ssl.SSLError("invalid fixture CA")):
            with self.subTest(error=type(error).__name__), \
                 patch.object(http_client.ssl, "create_default_context", side_effect=error) as create:
                with self.assertRaises(type(error)) as caught:
                    http_client.fixture_tls_context("fixture-ca.pem")
                self.assertIs(caught.exception, error)
                create.assert_called_once_with(cafile="fixture-ca.pem")


class RequestDeadlineTests(unittest.TestCase):
    def test_continuous_bytes_cannot_extend_the_elapsed_budget(self):
        response = Mock(headers=Message())
        response.__enter__ = Mock(return_value=response)
        response.__exit__ = Mock(return_value=False)
        response.read1.side_effect = [b"x", b"x", b""]
        opener = Mock()
        opener.open.return_value = response
        # One read succeeds inside the budget. The next loop reaches the
        # deadline even though the response can supply bytes without blocking.
        with patch.object(http_client.urllib.request, "build_opener", return_value=opener), \
                patch.object(http_client, "REQUEST_SECONDS", 1), \
                patch.object(http_client.time, "monotonic", side_effect=[100, 100.5, 101]):
            with self.assertRaisesRegex(http_client.ClientFailure, "^functional HTTP deadline$"):
                http_client.request(http_client.urllib.request.Request("http://fixture.invalid/slow"))
        opener.open.assert_called_once()
        response.read1.assert_called_once()
        response.__exit__.assert_called_once()


if __name__ == "__main__":
    unittest.main()
