from pathlib import Path
import subprocess

"""Default test selection and explicit tool gates stay independent of deployments."""

ROOT = Path(__file__).resolve().parents[2]

class MakeScopeTestsFixture:
    def run_make(self, *arguments, **kwargs):
        return subprocess.run(
            ["make", "--no-print-directory", "CATALOG_ENV_FILE=/dev/null", "PYTHON=python3", *arguments],
            cwd=ROOT, capture_output=True, text=True, timeout=30, check=True, **kwargs,
        ).stdout
