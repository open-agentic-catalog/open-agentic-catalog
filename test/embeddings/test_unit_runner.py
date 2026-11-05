"""Service-free assertions for fixture configuration and lifecycle ordering."""

import contextlib
import importlib.util
import io
import json
from pathlib import Path
import signal
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("embedding_fixture", Path(__file__).with_name("runner.py"))
fixture = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(fixture)


class EmbeddingFixtureTests(unittest.TestCase):
    """Mock shared acquisition/process calls; never use Docker or inference."""

    def setUp(self):
        self.profile = {"model": "fixture", "revision": "pinned", "dimensions": 384, "query_prefix": "query: ", "tei_image": "image@digest"}
        self.report = {"dimensions": 384, "batchOrdering": True, "repeatStable": True, "overlengthRejected": True, "elapsedMillis": 0}

    def test_settings_pin_one_owned_address(self):
        result = fixture.settings(self.profile, "http://172.22.0.2:80")
        self.assertEqual(result["allowedAddressPrefixes"], ["172.22.0.2/32"])
        self.assertEqual(result["endpoint"], "http://172.22.0.2:80/v1/embeddings")
        self.assertEqual(result["model"], "fixture")
        self.assertEqual(result["queryPrefix"], "query: ")
        self.assertNotIn("tokenFile", result)
        self.assertNotIn("requestedDimensions", result)

    def test_invalid_origins(self):
        for origin in ("https://example.com", "http://8.8.8.8:80", "http://127.0.0.1:80", "http://169.254.169.254:80", "http://0.0.0.0:80",
                       "http://172.22.0.2:81", "http://user:password@172.22.0.2:80", "http://172.22.0.2:80/path", "http://172.22.0.2:80?a=b", "http://172.22.0.2:80/#fragment"):
            with self.subTest(origin=origin), self.assertRaises(ValueError):
                fixture.settings(self.profile, origin)

    def test_build_and_cleanup_precede_success_report(self):
        events = []

        @contextlib.contextmanager
        def resources():
            events.append("start")
            try:
                yield {"tei": "owned"}
            finally:
                events.append("cleanup")

        def command(args, **kwargs):
            if args[0] == "go":
                events.append("build")
                return ""
            events.append("qualify")
            self.assertEqual(json.loads(kwargs["data"])["dimensions"], 384)
            return json.dumps(self.report)

        output = io.StringIO()
        with patch.object(fixture.runner, "verify_model"), patch.object(fixture.runner, "command", side_effect=command), \
                patch.object(fixture.runner, "owned_resources", resources), \
                patch.object(fixture.runner, "start_tei", return_value=("http://172.22.0.2:80", {})), contextlib.redirect_stdout(output):
            fixture.qualify(self.profile, Path("unused-model"))
        self.assertEqual(events, ["build", "start", "qualify", "cleanup"])
        self.assertEqual(json.loads(output.getvalue()), {**self.report, "cleanup": "complete", "model": "fixture", "revision": "pinned", "teiImage": "image@digest"})

    def test_incomplete_or_failed_report_cleans_up_without_success(self):
        cases = [None, [], {"repeatStable": True}]
        for field in ("batchOrdering", "repeatStable", "overlengthRejected"):
            missing = self.report.copy()
            del missing[field]
            cases.append(missing)
            cases.extend({**self.report, field: value} for value in (False, 1, "true", None))
        cases.extend({**self.report, "dimensions": value} for value in (None, 2, "384", True, 384.0))
        cases.extend({**self.report, "elapsedMillis": value} for value in (None, -1, "0", True, 1.5))
        for report in cases:
            with self.subTest(report=report):
                events = []

                @contextlib.contextmanager
                def resources():
                    events.append("start")
                    try:
                        yield {"tei": "owned"}
                    finally:
                        events.append("cleanup")

                output = io.StringIO()
                with patch.object(fixture.runner, "verify_model"), \
                        patch.object(fixture.runner, "command", side_effect=["", json.dumps(report)]), \
                        patch.object(fixture.runner, "owned_resources", resources), \
                        patch.object(fixture.runner, "start_tei", return_value=("http://172.22.0.2:80", {})), \
                        contextlib.redirect_stdout(output), self.assertRaisesRegex(ValueError, "invalid embedding qualification report"):
                    fixture.qualify(self.profile, Path("unused-model"))
                self.assertEqual(events, ["start", "cleanup"])
                self.assertEqual(output.getvalue(), "")

    def test_cleanup_failure_does_not_report_success(self):
        @contextlib.contextmanager
        def resources():
            yield {"tei": "owned"}
            raise RuntimeError("cleanup failed")

        output = io.StringIO()
        with patch.object(fixture.runner, "verify_model"), \
                patch.object(fixture.runner, "command", side_effect=["", json.dumps(self.report)]), \
                patch.object(fixture.runner, "owned_resources", resources), \
                patch.object(fixture.runner, "start_tei", return_value=("http://172.22.0.2:80", {})), \
                contextlib.redirect_stdout(output), self.assertRaisesRegex(RuntimeError, "cleanup failed"):
            fixture.qualify(self.profile, Path("unused-model"))
        self.assertEqual(output.getvalue(), "")

    def test_failure_does_not_report_success(self):
        output = io.StringIO()
        with patch.object(fixture.runner, "verify_model"), patch.object(fixture.runner, "command", side_effect=RuntimeError("build failed")), \
                patch.object(fixture.runner, "owned_resources") as resources, contextlib.redirect_stdout(output), self.assertRaisesRegex(RuntimeError, "build failed"):
            fixture.qualify(self.profile, Path("unused-model"))
        resources.assert_not_called()
        self.assertEqual(output.getvalue(), "")

    def test_sigterm_unwinds_and_restores_handler(self):
        previous = signal.getsignal(signal.SIGTERM)
        def interrupted(*args):
            signal.getsignal(signal.SIGTERM)(signal.SIGTERM, None)
        with patch.object(fixture.sys, "argv", ["runner.py", "test"]), patch.object(fixture, "qualify", side_effect=interrupted), self.assertRaises(KeyboardInterrupt):
            fixture.main()
        self.assertEqual(signal.getsignal(signal.SIGTERM), previous)


if __name__ == "__main__":
    unittest.main()
