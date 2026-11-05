"""Real local OpenSSL controls; no independent ANS process or external traffic."""

import importlib.util
from pathlib import Path
import tempfile
import unittest

from common import run

spec = importlib.util.spec_from_file_location("ans_native_functional", Path(__file__).parent / "ans/native.py")
native = importlib.util.module_from_spec(spec)
spec.loader.exec_module(native)


class CSRControls(unittest.TestCase):
    def test_actual_signed_requests_keep_identity_uri_separate_from_server_dns(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            for kind, san in (("identity", "URI:ans://v1.0.0.alpha.ans.test"), ("server", "DNS:alpha.ans.test")):
                with self.subTest(kind=kind):
                    raw = native.csr("alpha.ans.test", directory, kind)
                    self.assertTrue(raw.startswith("-----BEGIN CERTIFICATE REQUEST-----"))
                    request = directory / (kind + ".csr")
                    result = run(["openssl", "req", "-in", str(request), "-noout", "-text", "-verify"], timeout=10)
                    self.assertIn(san, result.stdout)
                    self.assertIn("verify OK", result.stderr)
                    self.assertEqual((directory / (kind + ".key")).stat().st_mode & 0o777, 0o600)
        self.assertFalse(directory.exists())
