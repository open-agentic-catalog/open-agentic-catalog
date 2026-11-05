"""Package and validate the two independent charts without rewriting source files."""
import importlib.util
import os
from pathlib import Path
import re
import shlex
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("monitoring_helm", ROOT / "test/monitoring/helm.py")
monitoring = importlib.util.module_from_spec(spec)
spec.loader.exec_module(monitoring)


def package(output, version, app_version, helm, kube_version="1.34.0"):
    """Publish complete, validated archives; existing caller outputs are preserved."""
    if len(version) > 128 or not monitoring.SEMVER.fullmatch(version):
        raise ValueError("CHART_VERSION must be SemVer (without a leading v)")
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._+-]{0,127}", app_version):
        raise ValueError("invalid APP_VERSION")
    output = Path(output)
    output.mkdir(parents=True, exist_ok=True)
    names = ["open-agentic-catalog", monitoring.CHART_NAME]
    destinations = [output / (name + "-" + version + ".tgz") for name in names]
    if any(path.exists() for path in destinations):
        raise ValueError("chart output already exists; choose another version or ARTIFACT_OUTPUT")
    published = []
    with tempfile.TemporaryDirectory(prefix=".charts-", dir=output) as temporary:
        stage = Path(temporary)
        source = stage / names[0]
        shutil.copytree(ROOT / "deployments/helm" / names[0], source)
        shutil.copyfile(ROOT / "LICENSE", source / "LICENSE")
        subprocess.run(helm + ["package", str(source), "--version", version, "--app-version", app_version,
                              "--destination", str(stage)], check=True, timeout=60, capture_output=True)
        monitoring.package(stage, version, app_version)
        for destination in destinations:
            archive = stage / destination.name
            subprocess.run(helm + ["lint", "--strict", str(archive), "--kube-version", kube_version], check=True, timeout=60, capture_output=True)
            subprocess.run(helm + ["template", "package-check", str(archive), "--kube-version", kube_version], check=True, timeout=60, capture_output=True)
        try:
            for destination in destinations:
                os.link(stage / destination.name, destination)
                published.append(destination)
        except OSError:
            for destination in published:
                destination.unlink()
            raise
    return destinations


if __name__ == "__main__":
    try:
        for result in package(os.environ.get("PACKAGING_OUTPUT", "output/artifacts"),
                              os.environ.get("PACKAGING_CHART_VERSION", "0.1.0"),
                              os.environ.get("PACKAGING_APP_VERSION", "dev"),
                              shlex.split(os.environ.get("PACKAGING_HELM", "helm")),
                              os.environ.get("PACKAGING_KUBE_VERSION", "1.34.0")):
            print(result)
    except (ValueError, OSError, subprocess.SubprocessError) as error:
        raise SystemExit("Chart packaging failed: " + str(error)) from error
