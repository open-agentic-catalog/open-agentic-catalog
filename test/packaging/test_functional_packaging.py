import shlex
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch
import image
from packaging_test_helpers import IDENTIFIER


class ImageTests(unittest.TestCase):
    def test_sample_notice_collector_preserves_every_source_notice(self):
        root = Path(__file__).resolve().parents[2]
        script = (root / "deployments/container/licenses.sh").read_text()
        collector = script[script.index("find samples"):]
        expected = {p.relative_to(root).as_posix(): p.read_bytes() for p in (root / "samples").rglob("*")
                    if p.is_file() and (p.name.lower().startswith(("license", "notice", "copying")) or "LICENSES" in p.parts or p == root / "samples/artwork/README.md")}
        self.assertEqual(len(expected), 8)
        with tempfile.TemporaryDirectory() as directory:
            destination = Path(directory) / "licenses"
            collector = collector.replace("/out/licenses/", str(destination) + "/")
            subprocess.run(["sh", "-eu", "-c", collector], cwd=root, check=True, timeout=10)
            actual = {p.relative_to(destination).as_posix(): p.read_bytes() for p in destination.rglob("*") if p.is_file()}
            self.assertEqual(actual, expected)

    def test_upstream_notice_selection_includes_license_directories(self):
        root = Path(__file__).resolve().parents[2]
        script = (root / "deployments/container/licenses.sh").read_text()
        # Execute only the collector's read-only selection, not its build-only
        # /out writes or dependency tooling. SPDX-named texts lack LICENSE prefixes.
        selection = script[script.index("find schemas/upstream"):].split(" -exec", 1)[0]
        result = subprocess.run(shlex.split(selection), cwd=root, capture_output=True, text=True, check=True, timeout=10)
        selected = set(result.stdout.splitlines())
        base = "schemas/upstream/agent-plugins/agent-plugins-1.0.0-1fc1b627/"
        for name in ["LICENSE.md", "LICENSES/Apache-2.0.txt", "LICENSES/CC-BY-4.0.txt"]:
            self.assertIn(base + name, selected)
    def test_actual_subprocess_failure_after_matching_output(self):
        import sys
        with tempfile.TemporaryDirectory() as directory:
            engine = Path(directory) / "engine.py"
            for failing in ["none", "catalogd", "catalogctl"]:
                engine.write_text("import sys\nctl = '--entrypoint' in sys.argv\nprint('{\"version\": \"dev\", \"commit\": \"unknown\", \"sourceState\": \"unknown\"}' if ctl else 'open-agentic-catalog dev (unknown) [unknown]')\nsys.exit(1 if ('catalogctl' if ctl else 'catalogd') == " + repr(failing) + " else 0)\n")
                if failing == "none":
                    image.verify_commands([sys.executable, str(engine)], IDENTIFIER, "dev", "unknown")
                else:
                    with self.assertRaises(subprocess.CalledProcessError):
                        image.verify_commands([sys.executable, str(engine)], IDENTIFIER, "dev", "unknown")
            with patch.object(image, "run", return_value=b"wrong version"), self.assertRaises(ValueError):
                image.verify_commands(["docker"], IDENTIFIER, "dev", "unknown")
