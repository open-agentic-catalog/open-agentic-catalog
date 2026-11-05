import json
from pathlib import Path
import subprocess
import tempfile
import unittest
import launcher


class MonitoringSafety(unittest.TestCase):
    def test_make_exports_env_file_settings_and_command_line_precedence(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            env_file = root / "selected.env"
            env_file.write_text("CATALOG_MONITORING_GRAFANA_PORT=23000\nCATALOG_MONITORING_RETENTION_HOURS=12\n")
            probe = root / "probe.py"
            probe.write_text('import os,json; print(json.dumps({k:v for k,v in os.environ.items() if k.startswith("CATALOG_MONITORING_")}))\n')
            env = launcher.isolated_environment()
            process = subprocess.run(["make", "--no-print-directory", "-s", "compose-ps", "CATALOG_ENV_FILE=" + str(env_file),
                                      "PYTHON=python3 " + str(probe), "CATALOG_MONITORING_GRAFANA_PORT=24000"],
                                     cwd=launcher.ROOT, env=env, capture_output=True, timeout=15, check=False)
            self.assertEqual(0, process.returncode, process.stderr)
            values = json.loads(process.stdout)
            self.assertEqual("24000", values["CATALOG_MONITORING_GRAFANA_PORT"])
            self.assertEqual("12", values["CATALOG_MONITORING_RETENTION_HOURS"])
            self.assertEqual("512", values["CATALOG_MONITORING_RETENTION_MB"])

    def test_make_scopes_demo_inputs_and_preserves_command_selection(self):
        with tempfile.TemporaryDirectory() as temporary:
            probe = Path(temporary) / "probe.py"
            probe.write_text('import os,json; print(json.dumps({k:os.environ.get(k) for k in ("LOCAL_DEMO_ARCHIVES","DEMO_ARCHIVE_DIR","DEMO_ARCHIVE_IP","DEMO_ARCHIVE_PORT")}))\n')
            process = subprocess.run(["make", "--no-print-directory", "-s", "compose-ps", "CATALOG_ENV_FILE=/dev/null",
                                      "PYTHON=python3 " + str(probe), "LOCAL_DEMO_ARCHIVES=on", "DEMO_ARCHIVE_DIR=output/selected-demo",
                                      "DEMO_ARCHIVE_IP=172.30.255.42", "DEMO_ARCHIVE_PORT=18449"],
                                     cwd=launcher.ROOT, env=launcher.isolated_environment(), capture_output=True, timeout=15, check=False)
            self.assertEqual(0, process.returncode, process.stderr)
            self.assertEqual({"LOCAL_DEMO_ARCHIVES":"on", "DEMO_ARCHIVE_DIR":"output/selected-demo", "DEMO_ARCHIVE_IP":"172.30.255.42", "DEMO_ARCHIVE_PORT":"18449"}, json.loads(process.stdout))


class SampleMakeSafety(unittest.TestCase):
    def test_sample_file_is_scoped_to_check_and_prepare(self):
        with tempfile.TemporaryDirectory() as temporary:
            probe = Path(temporary) / "probe.py"
            probe.write_text('import os,json; print(json.dumps({k:os.environ.get(k) for k in ("DEMO_DATA_FILE","DEMO_SAMPLE_SET","DEMO_STATE_FILE")}))\n')
            selectors = ["CATALOG_ENV_FILE=/dev/null", "DEMO_DATA_FILE=/selected/catalog.yaml", "DEMO_SAMPLE_SET=minimal", "DEMO_STATE_FILE=/selected/receipt.json"]
            checked = subprocess.run(["make", "--no-print-directory", "-s", "demo-check", "GO=python3 " + str(probe), *selectors], cwd=launcher.ROOT, env=launcher.isolated_environment(), capture_output=True, timeout=15, check=False)
            self.assertEqual(0, checked.returncode, checked.stderr)
            self.assertEqual({"DEMO_DATA_FILE":"/selected/catalog.yaml", "DEMO_SAMPLE_SET":"minimal", "DEMO_STATE_FILE":"/selected/receipt.json"}, json.loads(checked.stdout))
            unrelated = subprocess.run(["make", "--no-print-directory", "-s", "compose-ps", "PYTHON=python3 " + str(probe), *selectors], cwd=launcher.ROOT, env=launcher.isolated_environment(), capture_output=True, timeout=15, check=False)
            self.assertEqual(0, unrelated.returncode, unrelated.stderr)
            self.assertEqual({"DEMO_DATA_FILE":None, "DEMO_SAMPLE_SET":None, "DEMO_STATE_FILE":None}, json.loads(unrelated.stdout))


class DemoPeerMakeSafety(unittest.TestCase):
    def test_scoped_peer_settings(self):
        with tempfile.TemporaryDirectory() as temporary:
            probe = Path(temporary) / 'probe.py'
            probe.write_text('import os,json; print(json.dumps({k:os.environ.get(k) for k in ("LOCAL_DEMO_RUNTIMES","DEMO_RUNTIME_DIR","DEMO_A2A_PORT","DEMO_MCP_PORT")}))\n')
            process = subprocess.run(['make', '--no-print-directory', '-s', 'compose-ps', 'CATALOG_ENV_FILE=/dev/null',
                                      'PYTHON=python3 ' + str(probe), 'LOCAL_DEMO_RUNTIMES=on', 'DEMO_RUNTIME_DIR=output/selected-runtimes',
                                      'DEMO_A2A_PORT=21443', 'DEMO_MCP_PORT=21444'], cwd=launcher.ROOT,
                                     env=launcher.isolated_environment(), capture_output=True, timeout=15, check=False)
            self.assertEqual(0, process.returncode, process.stderr)
            self.assertEqual({'LOCAL_DEMO_RUNTIMES': 'on', 'DEMO_RUNTIME_DIR': 'output/selected-runtimes', 'DEMO_A2A_PORT': '21443', 'DEMO_MCP_PORT': '21444'}, json.loads(process.stdout))
