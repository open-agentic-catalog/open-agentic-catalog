"""Package shared assets and run explicit, independently owned monitoring releases."""
import argparse
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import re
import subprocess
import tarfile
import tempfile

ROOT = Path(__file__).resolve().parents[2]
CHART_NAME = "open-agentic-catalog-monitoring"
SOURCE = ROOT / "deployments/helm" / CHART_NAME
ASSETS = ROOT / "deployments/monitoring"


SEMVER = re.compile(r"(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?\Z")


def package(destination, chart_version=None, app_version=None):
    """Create a deterministic self-contained archive without generated source copies."""
    destination = Path(destination)
    version = re.search(r"^version: ([0-9]+\.[0-9]+\.[0-9]+)$", (SOURCE / "Chart.yaml").read_text(), re.M)
    if not version:
        raise ValueError("invalid monitoring chart version")
    selected = chart_version if chart_version is not None else version[1]
    if len(selected) > 128 or not SEMVER.fullmatch(selected):
        raise ValueError("invalid chart SemVer")
    if app_version is not None and not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._+-]{0,127}", app_version):
        raise ValueError("invalid application version")
    entries = {str(path.relative_to(SOURCE)): path.read_bytes() for path in SOURCE.rglob("*") if path.is_file()}
    metadata = entries["Chart.yaml"].decode()
    metadata = re.sub(r"^version: .+$", "version: " + selected, metadata, flags=re.M)
    if app_version is not None:
        metadata = re.sub(r"^appVersion: .+$", "appVersion: " + json.dumps(app_version), metadata, flags=re.M)
    entries["Chart.yaml"] = metadata.encode()
    for name, source in {
        "images.json": "images.json", "catalog.json": "dashboards/catalog.json", "grafana.ini": "grafana.ini",
        "dashboards.yaml": "provisioning/dashboards/catalog.yaml",
    }.items():
        entries["files/" + name] = (ASSETS / source).read_bytes()
    entries["LICENSE"] = (ROOT / "LICENSE").read_bytes()
    destination.mkdir(parents=True, exist_ok=True)
    output = destination / (CHART_NAME + "-" + selected + ".tgz")
    # Replace only this build's exact output, never recursively reset a directory.
    with tempfile.NamedTemporaryFile(dir=destination, prefix=".monitoring-", delete=False) as temporary:
        temporary_path = Path(temporary.name)
        try:
            with gzip.GzipFile(fileobj=temporary, mode="wb", filename="", mtime=0) as compressed:
                with tarfile.open(fileobj=compressed, mode="w") as archive:
                    for name, raw in sorted(entries.items()):
                        info = tarfile.TarInfo(CHART_NAME + "/" + name)
                        info.size, info.mode, info.mtime = len(raw), 0o644, 0
                        archive.addfile(info, io.BytesIO(raw))
            temporary.flush()
            os.replace(temporary_path, output)
        finally:
            temporary_path.unlink(missing_ok=True)
    return output


def resource_name(namespace, release):
    suffix = hashlib.sha256((namespace + "/" + release).encode()).hexdigest()[:8]
    return "cm-" + release[:28].rstrip("-") + "-" + suffix


def deployment_arguments(arguments):
    """Require the caller to select the context; never infer it or manage a cluster."""
    for name in ("context", "namespace", "release"):
        value = getattr(arguments, name)
        if not value or value.startswith("-") or any(character.isspace() for character in value):
            raise ValueError("explicit safe context, namespace and release are required")
    for name, maximum in (("namespace", 63), ("release", 53)):
        value = getattr(arguments, name)
        if len(value) > maximum or not re.fullmatch(r"[a-z0-9](?:[-a-z0-9]*[a-z0-9])?", value):
            raise ValueError(name + " must be a DNS label up to " + str(maximum) + " characters")
    return ["--kube-context", arguments.context, "--namespace", arguments.namespace]


def run(arguments):
    if arguments.action == "package":
        print(package(arguments.output))
        return
    selection = deployment_arguments(arguments)
    # Refuse to upgrade or remove a release belonging to another chart.
    current = subprocess.run([arguments.helm, "list", *selection, "--all", "--filter", "^" + arguments.release + "$", "--output", "json"],
                             check=True, capture_output=True, text=True, timeout=30)
    releases = json.loads(current.stdout)
    if not isinstance(releases, list) or len(releases) > 1 or any(not isinstance(item, dict) for item in releases):
        raise ValueError("unexpected Helm release inventory")
    if any(not re.fullmatch(re.escape(CHART_NAME) + r"-[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?", str(item.get("chart", ""))) for item in releases):
        raise ValueError("selected release belongs to another chart")
    if arguments.action == "install":
        if not arguments.values or not Path(arguments.values).is_file():
            raise ValueError("install requires an explicit values file")
        archive = package(arguments.output)
        subprocess.run([arguments.helm, "upgrade", "--install", arguments.release, str(archive), *selection,
                        "--values", arguments.values, "--set", "enabled=true", "--wait", "--timeout", "5m"], check=True, timeout=330)
    elif arguments.action == "uninstall":
        subprocess.run([arguments.helm, "uninstall", arguments.release, *selection, "--wait", "--timeout", "3m"], check=True, timeout=210)
    elif arguments.action == "status":
        subprocess.run([arguments.helm, "status", arguments.release, *selection], check=True, timeout=30)
    else:
        if not releases:
            raise ValueError("selected monitoring release does not exist")
        port = 3000 if arguments.component == "grafana" else 9090
        subprocess.run([arguments.kubectl, "--context", arguments.context, "--namespace", arguments.namespace,
                        "port-forward", "--address", "127.0.0.1", "service/" + resource_name(arguments.namespace, arguments.release) + "-" + arguments.component,
                        str(arguments.port) + ":" + str(port)], check=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["package", "install", "status", "access", "uninstall"])
    parser.add_argument("--output", default=str(ROOT / "output/helm-monitoring"))
    parser.add_argument("--helm", default="helm")
    parser.add_argument("--kubectl", default="kubectl")
    parser.add_argument("--context", default="")
    parser.add_argument("--namespace", default="")
    parser.add_argument("--release", default="catalog-monitoring")
    parser.add_argument("--values", default="")
    parser.add_argument("--component", choices=["grafana", "prometheus"], default="grafana")
    parser.add_argument("--port", type=int, default=13000)
    arguments = parser.parse_args()
    if not 1024 <= arguments.port <= 65535:
        parser.error("local port must be between 1024 and 65535")
    try:
        run(arguments)
    except (ValueError, OSError, subprocess.SubprocessError) as error:
        parser.exit(1, "monitoring command failed: " + str(error) + "\n")


if __name__ == "__main__":
    main()
