"""Service-reader decisions with mocked SDK calls, no processes or network."""

from contextlib import redirect_stderr, redirect_stdout
import importlib.util
import io
import json
from pathlib import Path
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from catalog_sdk import SDKError


SOURCE = Path(__file__).resolve().parents[4] / "examples/tutorials/local-oidc/service-read.py"
SPEC = importlib.util.spec_from_file_location("service_read_unit", SOURCE)
SERVICE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(SERVICE)


class ServiceReadUnits(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory(prefix="service-read-unit-")
        self.addCleanup(directory.cleanup)
        root = Path(directory.name)
        self.claims = root / "claims.json"
        self.token = root / "token"
        self.claims.write_text(json.dumps({"subject": "automation"}), encoding="ascii")
        self.token.write_text("private-unit-token", encoding="ascii")
        self.enterContext(patch.object(sys, "argv", [str(SOURCE), "--origin", "https://catalog.example.test", "--token-file", str(self.token), "--claims-file", str(self.claims), "--ca-file", str(root / "ca.pem")]))
        self.output = self.enterContext(redirect_stdout(io.StringIO()))
        self.enterContext(redirect_stderr(io.StringIO()))
        self.client = self.enterContext(patch("catalog_sdk.Client"))
        self.access_factory = self.enterContext(patch("catalog_sdk.generated.api.my_access_api.MyAccessApi"))
        self.search_factory = self.enterContext(patch("catalog_sdk.generated.api.catalog_discovery_api.CatalogDiscoveryApi"))
        self.access = self.access_factory.return_value
        self.search = self.search_factory.return_value
        self.access.get_current_access_sync_with_http_info.return_value = SimpleNamespace(status_code=200, data=SimpleNamespace(actor_type="service", subject="automation", permissions=["resource.read"]))
        self.search.search_catalog_sync_with_http_info.return_value = SimpleNamespace(status_code=200)

    def test_invalid_files_stop_before_sdk_construction(self):
        for raw in ["{", "null", "[]", "{}", '{"subject":null}', '{"subject":42}', '{"subject":""}', "[" * 10000 + "0" + "]" * 10000]:
            with self.subTest(raw_type=raw[:20]):
                self.claims.write_text(raw, encoding="ascii")
                with self.assertRaises(SystemExit) as raised:
                    SERVICE.main()
                self.assertEqual(str(raised.exception), "Cannot read the service token or expected subject from the input files")
                self.client.assert_not_called()
                self.access_factory.assert_not_called()
                self.search_factory.assert_not_called()

    def test_empty_or_missing_token_stops_before_sdk_construction(self):
        for empty in (True, False):
            with self.subTest(empty=empty):
                if empty:
                    self.token.write_text(" \n", encoding="ascii")
                else:
                    self.token.unlink()
                with self.assertRaises(SystemExit) as raised:
                    SERVICE.main()
                self.assertEqual(str(raised.exception), "Cannot read the service token or expected subject from the input files")
                self.client.assert_not_called()

    def test_identity_and_tenant_permissions_precede_search(self):
        for status, actor, subject, permissions, message in [
            (403, "service", "automation", ["resource.read"], "The API did not accept the expected service identity"),
            (200, "human", "automation", ["resource.read"], "The API did not accept the expected service identity"),
            (200, "service", "other", ["resource.read"], "The API did not accept the expected service identity"),
            (200, "service", "automation", [], "The service has no current Reader permission"),
        ]:
            with self.subTest(status=status, actor=actor, subject=subject, permissions=permissions):
                self.access.get_current_access_sync_with_http_info.return_value = SimpleNamespace(status_code=status, data=SimpleNamespace(actor_type=actor, subject=subject, permissions=permissions, available_permissions=["resource.read"], namespace_grants=[{"permissions": ["resource.read"]}]))
                with self.assertRaises(SystemExit) as raised:
                    SERVICE.main()
                self.assertEqual(str(raised.exception), message)
                self.search_factory.assert_not_called()

    def test_success_requests_only_bounded_local_metadata(self):
        SERVICE.main()
        self.assertEqual(self.output.getvalue(), "Python SDK verified the service identity and read local Catalog metadata\n")
        self.access.get_current_access_sync_with_http_info.assert_called_once_with()
        self.search.search_catalog_sync_with_http_info.assert_called_once()
        request = self.search.search_catalog_sync_with_http_info.call_args.kwargs["catalog_search"]
        self.assertEqual(request.to_dict(), {"origins": ["local"], "limit": 10})

    def test_read_refusal_and_sdk_failure_have_fixed_diagnostics(self):
        self.search.search_catalog_sync_with_http_info.return_value = SimpleNamespace(status_code=403, data="private-response-sentinel")
        with self.assertRaises(SystemExit) as raised:
            SERVICE.main()
        self.assertEqual(str(raised.exception), "The local metadata read failed")
        self.access.get_current_access_sync_with_http_info.side_effect = SDKError("request_failed", request_id="private-correlation-sentinel")
        with self.assertRaises(SystemExit) as raised:
            SERVICE.main()
        self.assertEqual(str(raised.exception), "The Catalog read failed: request_failed")
        self.assertEqual(self.output.getvalue(), "")


if __name__ == "__main__":
    unittest.main()
