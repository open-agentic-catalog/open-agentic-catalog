"""Pure configuration and evidence tests for Rapid execution."""

import json
from pathlib import Path
import tempfile
import unittest

import property as p


PACKAGE = "example.com/property"
NAME = "TestPropertyExample"


def events(count=100, terminal="pass", message=None):
    message = f"[rapid] OK, passed {count} tests (1ms)\n" if message is None else message
    return b"\n".join(json.dumps(dict(Package=PACKAGE, **row)).encode() for row in [
        {"Action": "start"}, {"Action": "run", "Test": NAME},
        {"Action": "output", "Test": NAME, "Output": message},
        {"Action": terminal, "Test": NAME}, {"Action": "fail" if terminal == "fail" else "pass"}]) + b"\n"


class PropertyTests(unittest.TestCase):
    def test_bounds_environment_and_discovery(self):
        self.assertEqual(p.settings()["checks"], 1000)
        self.assertEqual(p.settings("10000", str(2**64-1))["seed"], 2**64-1)
        for checks, seed in (("0", "1"), ("-1", "1"), ("a", "1"), ("10001", "1"),
                             ("100", "0"), ("100", str(2**64)), (" 1", "1"), ("1", "$(touch x)")):
            with self.subTest(checks=checks, seed=seed), self.assertRaises(ValueError):
                p.settings(checks, seed)
        env = p.environment({"RAPID_FAILFILE": "missing", "RAPID_CHECKS": "0", "RAPID_DEBUG": "true",
                             "RAPID_FUTURE_FLAG": "invalid", "GOFLAGS": "-run=none", "GOWORK": "bad"}, p.settings())
        self.assertEqual(env["RAPID_CHECKS"], "1000")
        self.assertEqual(env["RAPID_SEED"], "1")
        self.assertEqual(env["RAPID_NOFAILFILE"], "1")
        self.assertEqual(env["GOFLAGS"], "-mod=readonly")
        self.assertEqual(env["GOWORK"], "off")
        for key in ("RAPID_FAILFILE", "RAPID_DEBUG", "RAPID_FUTURE_FLAG"):
            self.assertNotIn(key, env)
        self.assertEqual(p.names(b"TestPropertyOne\nTestExample\nTestPropertyTwo\nok package\n"),
                         ["TestPropertyOne", "TestPropertyTwo"])
        self.assertEqual(p.names(b"TestOther\n", required=False), [])
        for raw in (b"", b"TestOther\n", b"TestPropertyOne\nTestPropertyOne\n"):
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                p.names(raw)

    def test_completion_and_assertion_failure_differ_from_incomplete_or_invalid_runs(self):
        p.validate_events(events(), PACKAGE, [NAME], 100)
        p.validate_events(events(terminal="fail", message="[rapid] failed after 2 tests: assertion"),
                          PACKAGE, [NAME, "TestPropertyLater"], 100, failed_run=True)
        for raw in (events(20), events(terminal="skip"), events(message=""),
                    events(message="[rapid] OK, passed 100 tests (1ms)\n" * 2),
                    events(message="[rapid] only generated 0 valid tests from 1000 total"),
                    events(message="[rapid] ignoring fail file: no such file"),
                    events(message='[rapid] fail file "old" is no longer valid'),
                    events(message="[rapid] flaky test, can not reproduce a failure"),
                    events(message="(cached)"), events().replace(b'"start"', b'"unknown"'),
                    events().replace(PACKAGE.encode(), b"wrong"), b"not JSON"):
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                p.validate_events(raw, PACKAGE, [NAME], 100)
        with self.assertRaisesRegex(ValueError, "missing property"):
            p.validate_events(events(), PACKAGE, [NAME, "TestPropertyMissing"], 100)
        with self.assertRaisesRegex(ValueError, "undiscovered"):
            p.validate_events(events(), PACKAGE, ["TestPropertyOther"], 100)
        with self.assertRaisesRegex(ValueError, "invalid or cached"):
            p.validate_events(events(terminal="fail", message="[rapid] only generated 0 valid tests from 1000 total"),
                              PACKAGE, [NAME], 100, failed_run=True)

    def test_corpus_identity_includes_changes_and_refuses_symlinks(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            self.assertEqual(p.corpus(root, "."), {})
            path = root / "testdata/rapid/TestPropertyExample"
            path.mkdir(parents=True)
            failfile = path / "example.fail"
            failfile.write_text("first")
            before = p.corpus(root, ".")
            failfile.write_text("second")
            self.assertNotEqual(before, p.corpus(root, "."))
            (path / "linked.fail").symlink_to(failfile)
            with self.assertRaisesRegex(ValueError, "symlinked"):
                p.corpus(root, ".")


if __name__ == "__main__":
    unittest.main()
