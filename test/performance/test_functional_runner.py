import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
import runner


class PerformanceOwnership(unittest.TestCase):
    def test_make_exports_only_workload_controls_to_isolated_target(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            probe = root / "probe.py"
            probe.write_text('import os,json; print(json.dumps({k:v for k,v in os.environ.items() if k.startswith(("PERF_", "CATALOG_MONITORING_"))}))\n')
            env_file = root / "ignored.env"
            env_file.write_text("$(error DO_NOT_READ_ME)\n")
            process = subprocess.run(["make", "--no-print-directory", "-s", "test-load-smoke", "PYTHON=python3 " + str(probe),
                                      "CATALOG_ENV_FILE=" + str(env_file), "PERF_DURATION_SECONDS=45", "PERF_WORKERS=3", "PERF_WARMUP_SECONDS=0"],
                                     cwd=runner.ROOT, env=runner.local.isolated_environment(), capture_output=True, timeout=15, check=False)
            self.assertEqual(0, process.returncode, process.stderr)
            values = json.loads(process.stdout)
            self.assertEqual({"PERF_DURATION_SECONDS": "45", "PERF_WORKERS": "3", "PERF_WARMUP_SECONDS": "0"}, values)
    def test_make_skips_env_before_parsing(self):
        with tempfile.TemporaryDirectory() as temporary:
            malicious = Path(temporary) / "sentinel.env"
            malicious.write_text("$(error SENTINEL_ENV_WAS_READ)\n")
            env = dict(os.environ, CATALOG_ENV_FILE=str(malicious))
            process = subprocess.run(["make", "-n", "test-load-smoke"], cwd=runner.ROOT, env=env, capture_output=True, timeout=15, check=False)
            self.assertEqual(0, process.returncode, process.stderr)
            self.assertNotIn(b"SENTINEL_ENV_WAS_READ", process.stderr)
            mixed = subprocess.run(["make", "-n", "test-load-smoke", "compose-reset"], cwd=runner.ROOT, env=env, capture_output=True, timeout=15, check=False)
            self.assertNotEqual(0, mixed.returncode)
            self.assertIn(b"must run alone", mixed.stderr)
    def test_search_benchmark_make_isolation_and_cleanup(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            malicious = root / "sentinel.env"
            malicious.write_text("$(error SENTINEL_ENV_WAS_READ)\n")
            calls = root / "calls.jsonl"
            probe = root / "probe.py"
            probe.write_text(
                "import json, os, sys\n"
                "from pathlib import Path\n"
                "kind, log, status, *args = sys.argv[1:]\n"
                "with Path(log).open('a') as output:\n"
                "    output.write(json.dumps({'kind': kind, 'args': args, 'dsn': os.environ.get('CATALOG_TEST_DATABASE_DSN'), 'runtime': os.environ.get('CATALOG_DATABASE_DSN')}) + '\\n')\n"
                "if kind == 'compose' and 'port' in args:\n"
                "    print('127.0.0.1:54321')\n"
                "sys.exit(int(status) if kind == 'go' else 0)\n"
            )
            owned_dsn = "postgres://catalog:catalog@127.0.0.1:54321/catalog_test?sslmode=disable"
            explicit_dsn = "postgres://catalog:catalog@127.0.0.1:54322/catalog_test?sslmode=disable"
            for target in ["bench-search", "bench-search-plans", "bench-search-dsn", "bench-search-plans-dsn"]:
                for selection in ["environment", "command-line"]:
                    for failure in [False, True]:
                        with self.subTest(target=target, selection=selection, failure=failure):
                            calls.write_text("")
                            arguments = ["make", "--no-print-directory", "-s", target,
                                         "COMPOSE=python3 " + str(probe) + " compose " + str(calls) + " 0",
                                         "GO=python3 " + str(probe) + " go " + str(calls) + (" 7" if failure else " 0")]
                            env = runner.local.isolated_environment() | {"CATALOG_TEST_DATABASE_DSN": explicit_dsn, "CATALOG_DATABASE_DSN": "DO_NOT_EXPORT"}
                            if selection == "environment":
                                env["CATALOG_ENV_FILE"] = str(malicious)
                            else:
                                arguments.append("CATALOG_ENV_FILE=" + str(malicious))
                            process = subprocess.run(arguments, cwd=runner.ROOT, env=env, capture_output=True, timeout=15, check=False)
                            self.assertEqual(failure, process.returncode != 0, process.stderr)
                            self.assertNotIn(b"SENTINEL_ENV_WAS_READ", process.stderr)
                            records = [json.loads(line) for line in calls.read_text().splitlines()]
                            go_calls = [record for record in records if record["kind"] == "go"]
                            self.assertEqual(1, len(go_calls))
                            self.assertEqual(explicit_dsn if target.endswith("-dsn") else owned_dsn, go_calls[0]["dsn"])
                            self.assertIsNone(go_calls[0]["runtime"])
                            compose_calls = [record for record in records if record["kind"] == "compose"]
                            if target.endswith("-dsn"):
                                self.assertEqual([], compose_calls)
                            else:
                                self.assertEqual(["up", "port"] + (["logs"] if failure else []) + ["down"], [record["args"][6] for record in compose_calls])
                                projects = set()
                                for record in compose_calls:
                                    self.assertEqual(["--env-file", "/dev/null", "-f", "compose.integration.yaml", "-p"], record["args"][:5])
                                    projects.add(record["args"][5])
                                self.assertEqual(1, len(projects))
                                self.assertTrue(projects.pop().startswith("open-agentic-catalog-integration-"))
                                self.assertEqual(["down", "--volumes", "--remove-orphans"], compose_calls[-1]["args"][6:])
    def test_search_benchmarks_reject_mixed_make_goals(self):
        for target in ["bench-search", "bench-search-plans", "bench-search-dsn", "bench-search-plans-dsn"]:
            with self.subTest(target=target):
                process = subprocess.run(["make", "-n", target, "help", "COMPOSE=false", "GO=false"],
                                         cwd=runner.ROOT, env=runner.local.isolated_environment(), capture_output=True, timeout=15, check=False)
                self.assertNotEqual(0, process.returncode)
                self.assertIn(b"must run alone", process.stderr)
