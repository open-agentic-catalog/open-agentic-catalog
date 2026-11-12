"""Native Go seed/completion/failure calibrations for the selected receiver owner."""

import json
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import receiver_fuzz as f


class ReceiverFuzzNativeTests(unittest.TestCase):
    def fixture(self, body):
        owned = tempfile.TemporaryDirectory(prefix="catalog-receiver-fuzz-control-")
        self.addCleanup(owned.cleanup)
        root = Path(owned.name).resolve()
        module = root / f.MODULE
        module.mkdir(parents=True)
        version = next(line.split()[1] for line in (f.ROOT / "go.mod").read_text().splitlines() if line.startswith("go "))
        (module / "go.mod").write_text("module receiver-fuzz-control\n\ngo " + version + "\n")
        for name in ("go.sum", "schema.sql"):
            (module / name).write_text("")
        (module / "event_test.go").write_text('package control\nimport "testing"\n' + body)
        sdk = root / "sdk/go"
        sdk.mkdir(parents=True)
        for name in ("go.mod", "go.sum"):
            (sdk / name).write_text("")
        for name in ("test/mcp-scanner/receiver_fuzz.py", "test/ci/fuzz_corpus.py"):
            selected = root / name
            selected.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(f.ROOT / name, selected)
        subprocess.run(["git", "init", "--quiet", str(root)], check=True, capture_output=True, timeout=15)
        return root

    def receipt(self, root):
        paths = list((root / "output/mcp-scanner-receiver-fuzz").glob("run-*/result.json"))
        self.assertEqual(len(paths), 1)
        return json.loads(paths[0].read_text())

    def test_native_healthy_missing_skipped_and_assertion_failure(self):
        go = shutil.which("go")
        self.assertIsNotNone(go, "Select the maintained Go environment")
        cases = [
            ('func FuzzEventAdmission(f *testing.F){ f.Add([]byte("owned")); f.Fuzz(func(t *testing.T,b []byte){}) }', True),
            ('func TestOther(t *testing.T){}', False),
            ('func FuzzEventAdmission(f *testing.F){ f.Skip("control");f.Fuzz(func(t *testing.T,b []byte){}) }', False),
            ('func FuzzEventAdmission(f *testing.F){f.Add([]byte("owned"));f.Fuzz(func(t *testing.T,b []byte){t.Fatal("synthetic assertion")})}', False),
        ]
        for body, passed in cases:
            with self.subTest(passed=passed, body=body):
                root = self.fixture(body)
                result = f.run(go, "1s", root=root)
                receipt = self.receipt(root)
                self.assertEqual(result == 0, passed)
                self.assertEqual(receipt["status"], "passed" if passed else "failed")
                self.assertIn("go1.", receipt["go"])
                self.assertEqual(len(receipt["goSha256"]), 64)
                self.assertIn("test/mcp-scanner/receiver_fuzz.py", receipt["source"])
                self.assertIn("test/ci/fuzz_corpus.py", receipt["source"])

    def test_native_counterexample_and_collection_failure_preserve_fuzz_failure(self):
        go = shutil.which("go")
        self.assertIsNotNone(go)
        body = 'func FuzzEventAdmission(f *testing.F){ f.Add([]byte{});f.Fuzz(func(t *testing.T,b []byte){if len(b)>0 {t.Fatal("synthetic finding")}}) }'
        for collection_fails in (False, True):
            with self.subTest(collection_fails=collection_fails):
                root = self.fixture(body)
                if collection_fails:
                    with patch.object(f.corpus, "collect", side_effect=ValueError("synthetic collection failure")):
                        result = f.run(go, "2s", root=root)
                else:
                    result = f.run(go, "2s", root=root)
                receipt = self.receipt(root)
                self.assertNotEqual(result, 0)
                self.assertEqual(receipt["status"], "failed")
                self.assertEqual(result, receipt["exitCode"])
                if collection_fails:
                    self.assertEqual(receipt["collectionError"], "ValueError")
                else:
                    self.assertTrue(receipt["corpus"], "Native synthetic counterexample was not retained")
                    for name in receipt["corpus"]:
                        self.assertTrue(name.startswith(str(f.MODULE / "testdata/fuzz" / f.TARGET) + "/"))
                        self.assertTrue((root / name).read_bytes().startswith(b"go test fuzz v1\n"))
                self.assertEqual(receipt["cleanup"], "passed")


if __name__ == "__main__":
    unittest.main()
