import json
import subprocess
import sys
import tempfile
from pathlib import Path
import unittest
import runner


class ScaleTests(unittest.TestCase):
    def test_public_delivery_make_target_is_isolated_and_selects_only_its_profile(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            probe = root / "probe.py"
            probe.write_text('import json,sys; print(json.dumps(sys.argv[1:]))\n')
            env_file = root / "ignored.env"
            env_file.write_text("$(error DO_NOT_READ_ME)\n")
            arguments = ["make", "--no-print-directory", "-s", "test-public-delivery", "PYTHON=python3 " + str(probe), "CATALOG_ENV_FILE=" + str(env_file)]
            result = subprocess.run(arguments, cwd=runner.ROOT, env=runner.local.isolated_environment(), capture_output=True, timeout=15, check=False)
            self.assertEqual(0, result.returncode, result.stderr)
            self.assertEqual(["test/performance/runner.py", "public-delivery"], json.loads(result.stdout))
            refused = subprocess.run([*arguments, "check"], cwd=runner.ROOT, env=runner.local.isolated_environment(), capture_output=True, timeout=15, check=False)
            self.assertNotEqual(0, refused.returncode)
            self.assertIn(b"Isolated measurement targets must run alone", refused.stderr)

    def test_real_failed_helper_retains_only_its_allowlisted_stage(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            binary = directory / "scale-plans"
            for stage, code in [("report-output-limit", "helper-report-output-limit"),
                                ("private secret", "helper-failed")]:
                binary.write_text(f'#!{sys.executable}\nimport sys\nprint("private SQL or DSN")\nprint("scale-plan-failure: {stage}")\nprint("private stderr", file=sys.stderr)\nsys.exit(1)\n')
                binary.chmod(0o700)
                with self.assertRaisesRegex(runner.Failure, "^scale-plan-" + code + "$"):
                    runner.scale.collect_plans(runner.local, runner.local.isolated_environment(), directory, 1234)

    def test_make_size_is_isolated_and_not_loaded_from_env_file(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            probe = root / "probe.py"
            probe.write_text('import os,json; print(json.dumps({k:v for k,v in os.environ.items() if k.startswith("PERF_")}))\n')
            env_file = root / "ignored.env"
            env_file.write_text("$(error DO_NOT_READ_ME)\n")
            for target, controls, expected in [("test-scale", [], {"PERF_ITEMS": "1000", "PERF_SQL_PLANS": "off", "PERF_SCALE_WORKLOAD": "standard"}), ("test-scale", ["PERF_ITEMS=10000", "PERF_SQL_PLANS=on"], {"PERF_ITEMS": "10000", "PERF_SQL_PLANS": "on", "PERF_SCALE_WORKLOAD": "standard"}), ("test-scale", ["PERF_SCALE_WORKLOAD=publisher-sort"], {"PERF_ITEMS": "1000", "PERF_SQL_PLANS": "off", "PERF_SCALE_WORKLOAD": "publisher-sort"}), ("test-load", ["PERF_ITEMS=10000", "PERF_SQL_PLANS=on", "PERF_SCALE_WORKLOAD=publisher-sort"], {})]:
                result = subprocess.run(["make", "--no-print-directory", "-s", target, "PYTHON=python3 " + str(probe), "CATALOG_ENV_FILE=" + str(env_file), *controls], cwd=runner.ROOT, env=runner.local.isolated_environment(), capture_output=True, timeout=15, check=False)
                self.assertEqual(0, result.returncode, result.stderr)
                self.assertEqual({"PERF_DURATION_SECONDS": "", "PERF_WARMUP_SECONDS": "5", "PERF_WORKERS": "2"} | expected, json.loads(result.stdout))
