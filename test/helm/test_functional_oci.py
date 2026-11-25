"""Consumer checks run by the disposable registry in make helm-oci-test."""

import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import urllib.parse
import urllib.request
from unittest.mock import patch

import release_delivery as release
from test_functional_packaging import HELM, charts, independent_chart_sources

ROOT = Path(__file__).resolve().parents[2]


def roundtrip(server, repository, archive, name, version, app_version, checksum):
    target = "oci://" + server.netloc + "/" + repository
    # Each pull starts with empty Helm and Docker settings. The fixture also rejects credentials.
    with tempfile.TemporaryDirectory(prefix="catalog-helm-client-") as temporary:
        client = Path(temporary)
        env = {**os.environ, "HOME": temporary, "DOCKER_CONFIG": str(client / "docker"),
               "HELM_CONFIG_HOME": str(client / "config"), "HELM_CACHE_HOME": str(client / "cache"),
               "HELM_DATA_HOME": str(client / "data"), "HELM_REGISTRY_CONFIG": str(client / "registry.json"),
               "NO_PROXY": server.hostname, "no_proxy": server.hostname}
        sender = {**env, "HELM_REGISTRY_CONFIG": str(client / "sender.json"), "HELM_CACHE_HOME": str(client / "sender-cache")}
        subprocess.run(HELM + ["push", str(archive), target, "--plain-http"], check=True, env=sender, timeout=60)
        tag = release.chart_tag(version)
        url = f"{server.geturl()}/v2/{repository}/{name}/manifests/{tag}"
        request = urllib.request.Request(url, headers={"Accept": "application/vnd.oci.image.manifest.v1+json"})
        with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(request, timeout=10) as response:
            raw = response.read()
            manifest_digest = response.headers["Docker-Content-Digest"]
        manifest = json.loads(raw)
        if (manifest_digest != "sha256:" + hashlib.sha256(raw).hexdigest()
                or manifest["annotations"]["org.opencontainers.image.source"] != "https://github.com/open-agentic-catalog/open-agentic-catalog"
                or [layer["digest"] for layer in manifest["layers"]] != ["sha256:" + checksum]):
            raise ValueError(f"{name}:{tag} manifest differs from the qualified chart or its source")

        subprocess.run(HELM + ["pull", target + "/" + name, "--version", version, "--plain-http", "--destination", temporary],
                       check=True, env=env, timeout=60)
        pulled = client / f"{name}-{version}.tgz"
        if pulled.read_bytes() != archive.read_bytes():
            raise ValueError(f"Anonymous pull changed {archive.name}")
        metadata = subprocess.check_output(HELM + ["show", "chart", str(pulled)], text=True, env=env, timeout=30).splitlines()
        if not {f"name: {name}", f"version: {version}", f"appVersion: {app_version}"}.issubset(metadata):
            raise ValueError(f"Unexpected chart identity in anonymous pull of {archive.name}")
        print(f"{repository}/{name}:{tag}  chart={version} appVersion={app_version} archiveSha256={checksum} manifestDigest={manifest_digest}")


def main():
    server = urllib.parse.urlsplit(sys.argv[1])
    if server.scheme != "http" or server.hostname not in {"127.0.0.1", "::1"} or not server.port or server.path:
        raise ValueError("Expected the disposable registry loopback URL")
    app_version = (ROOT / "VERSION").read_text().strip()
    with tempfile.TemporaryDirectory(prefix="catalog-helm-oci-") as temporary:
        work = Path(temporary)
        selected = os.environ.get("HELM_OCI_QUALIFIED")
        if selected:
            directory = Path(selected).resolve(strict=True)
        else:
            directory = work / "qualified"
            charts.package(directory, None, app_version, HELM)
            release.chart_result(directory)
        receipt = json.loads(release.admission.regular(directory, "qualification.json").read_text())
        expected = release.release_charts()
        if (receipt["result"] != "success" or receipt["source"] != release.admission.source_identity(ROOT)
                or receipt["files"].keys() != expected.keys() or {p.name for p in directory.glob("*.tgz")} != expected.keys()):
            raise ValueError("Chart qualification does not match this checkout and inventory")
        for filename, (name, version) in expected.items():
            archive = release.admission.regular(directory, filename)
            checksum = receipt["files"][filename]
            if release.candidate.digest(archive) != checksum:
                raise ValueError(f"Qualified archive changed: {filename}")
            roundtrip(server, "charts", archive, name, version, app_version, checksum)

        versions = {"open-agentic-catalog": "1.2.3+build.7", "open-agentic-catalog-monitoring": "2.4.0-rc.1"}
        source = work / "version-fixtures"
        directory = work / "packages"
        with independent_chart_sources(source, versions), patch.object(release, "ROOT", source):
            charts.package(directory, None, app_version, HELM)
            inventory = release.release_charts()
            # A new checkout can have different mtimes for exactly the same Git files.
            os.utime(source / "deployments/helm/open-agentic-catalog/Chart.yaml", (1, 1))
            for rebuilt in charts.package(work / "retry", None, app_version, HELM):
                if rebuilt.read_bytes() != (directory / rebuilt.name).read_bytes():
                    raise ValueError(f"Repackaging unchanged chart inputs changed {rebuilt.name}")
        if inventory != {f"{name}-{version}.tgz": (name, version) for name, version in versions.items()}:
            raise ValueError("Release selected the wrong chart versions")
        for filename, (name, version) in inventory.items():
            archive = directory / filename
            roundtrip(server, "version-fixtures", archive, name, version, app_version, release.candidate.digest(archive))


if __name__ == "__main__":
    main()
