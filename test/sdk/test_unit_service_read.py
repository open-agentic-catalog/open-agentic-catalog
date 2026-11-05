"""Invalid local inputs need no generated SDK import, listener or subprocess."""
import builtins
import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


class ServiceReadTests(unittest.TestCase):
    def test_rejects_invalid_inputs_before_importing_sdk(self):
        source = Path(__file__).resolve().parents[2] / "examples/tutorials/local-oidc/service-read.py"
        original = builtins.__import__
        def guarded(name, *args, **kwargs):
            if name == "catalog_sdk" or name.startswith("catalog_sdk."):
                raise AssertionError("Invalid input loaded the SDK")
            return original(name, *args, **kwargs)
        with patch("builtins.__import__", side_effect=guarded):
            spec = importlib.util.spec_from_file_location("service_read_example", source)
            module = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(module)
            with tempfile.TemporaryDirectory() as directory:
                token, claims = Path(directory) / "token", Path(directory) / "claims"
                argv = [str(source), "--origin", "https://owned.example", "--token-file", str(token), "--claims-file", str(claims), "--ca-file", str(Path(directory) / "ca.pem")]
                for document, credential in (("{}", "token"), ('{"subject":null}', "token"), ('{"subject":42}', "token"),
                                             ('{"subject":""}', "token"), ("null", "token"), ("[]", "token"),
                                             ("{", "token"), ("[" * 10000 + "0" + "]" * 10000, "token"),
                                             ('{"subject":"service"}', "")):
                    with self.subTest(document=document[:32]):
                        claims.write_text(document)
                        token.write_text(credential)
                        with patch("sys.argv", argv), self.assertRaisesRegex(SystemExit, "^Cannot read the service token or expected subject from the input files$"):
                            module.main()
