"""Isolated native-event and exact corpus admission controls."""

import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import receiver_fuzz as f


class ReceiverFuzzTests(unittest.TestCase):
    def events(self, *values):
        return b"\n".join(json.dumps(e).encode() for e in values)

    def test_native_seed_and_campaign_completion(self):
        valid = [{"Action": "run", "Test": f.TARGET}, {"Action": "pass", "Test": f.TARGET}, {"Action": "pass"}]
        f.baseline_complete(self.events(*valid))
        for bad in ([], valid[:-1], valid[1:], valid + [valid[-1]],
                    [{**valid[0], "Test": "Other"}, valid[1], valid[2]],
                    [valid[0], {**valid[1], "Action": "skip"}, valid[2]],
                    valid + [{"Action": "fail", "Test": f.TARGET + "/seed"}]):
            with self.subTest(bad=bad), self.assertRaises(ValueError):
                f.baseline_complete(self.events(*bad))
        with self.assertRaises(ValueError):
            f.baseline_complete(b"not native JSON")

    def test_budget_is_bounded(self):
        for value in ("1s", "10s", "60s"):
            self.assertEqual(f.budget(value), value)
        for value in ("0s", "61s", "1m", "9999x", "10s;false"):
            with self.subTest(value=value), self.assertRaises(ValueError):
                f.budget(value)

    def test_exact_receiver_corpus_reuses_bounded_collector(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            name = f.MODULE / "testdata/fuzz" / f.TARGET / "0123456789abcdef"
            data = b'go test fuzz v1\n[]byte("synthetic")\n'
            (root / name).parent.mkdir(parents=True)
            (root / name).write_bytes(data)
            target = root / "output/corpus"
            with patch.object(f.corpus.subprocess, "run", side_effect=AssertionError("unit spawned process")):
                self.assertEqual(f.corpus.collect(root, [name.as_posix()], target, packages=(f.MODULE.as_posix(),)), [name.as_posix()])
                self.assertEqual((target / name).read_bytes(), data)
                self.assertEqual((root / name).read_bytes(), data)
                self.assertEqual(f.corpus.collect(root, [name.as_posix()], root / "output/root-corpus"), [])
