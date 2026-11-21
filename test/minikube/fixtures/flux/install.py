"""Prepare the external tools used by the standalone Flux Minikube test."""
import hashlib
import json
import os
from pathlib import Path
import platform
import subprocess
import tarfile
from tempfile import TemporaryDirectory


def prepare_manifest(cache, pins):
    manifest = cache / "controllers.yaml"
    if manifest.is_file() and hashlib.sha256(manifest.read_bytes()).hexdigest() == pins["controllerManifestSha256"]:
        return
    version = pins["version"]
    archive = f"flux_{version.removeprefix('v')}_linux_amd64.tar.gz"
    with TemporaryDirectory(dir=cache) as tmp:
        work = Path(tmp)
        subprocess.run(["gh", "release", "download", version, "--repo", "fluxcd/flux2", "--pattern", archive,
                        "--dir", str(work)], check=True, timeout=300)
        downloaded = work / archive
        if hashlib.sha256(downloaded.read_bytes()).hexdigest() != pins["cliArchiveSha256"]:
            raise ValueError("Flux CLI archive checksum mismatch")
        cli = work / "flux"
        with tarfile.open(downloaded, "r:gz") as package:
            member = package.getmember("flux")
            if not member.isfile():
                raise ValueError("expected the Flux CLI executable in the verified archive")
            with package.extractfile(member) as executable:
                cli.write_bytes(executable.read())
        cli.chmod(0o700)
        result = subprocess.run([str(cli), "install", "--export", "--components=source-controller,helm-controller",
                                 "--namespace=flux-system"], env={**os.environ, "KUBECONFIG": os.devnull},
                                check=True, stdout=subprocess.PIPE, timeout=120)
        if hashlib.sha256(result.stdout).hexdigest() != pins["controllerManifestSha256"]:
            raise ValueError("Flux controller manifest checksum mismatch")
        staged = work / "controllers.yaml"
        staged.write_bytes(result.stdout)
        staged.replace(manifest)


def main():
    if platform.system() != "Linux" or platform.machine() not in ("amd64", "x86_64"):
        raise SystemExit("Flux Minikube qualification requires Linux AMD64")
    pins = json.loads(Path(__file__).with_name("pins.json").read_text())
    cache = Path(".tools/minikube-flux").resolve()
    cache.mkdir(parents=True, exist_ok=True)
    prepare_manifest(cache, pins)
    for image in pins["images"].values():
        inspected = subprocess.run(["docker", "image", "inspect", "--format", "{{.Os}}/{{.Architecture}}", image],
                                   capture_output=True, text=True, check=False, timeout=30)
        if inspected.returncode or inspected.stdout.strip() != "linux/amd64":
            subprocess.run(["docker", "pull", "--platform=linux/amd64", image], check=True, timeout=600)
    print("Flux controller manifest and Linux AMD64 images ready for make test-minikube-flux")


if __name__ == "__main__":
    main()
