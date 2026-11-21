"""Own one retained Minikube supervisor. Disposable campaigns never consult this inventory."""

import fcntl
import hashlib
import http.client
import json
import os
from pathlib import Path
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import time
from urllib.parse import urlsplit

from install import PINS, prepare

ROOT = Path.cwd()
LAB = ROOT / "output/argocd-lab"
LOCK = LAB.parent / "argocd-lab.lock"


def private_inventory():
    if LAB.is_symlink() or not LAB.is_dir() or LAB.stat().st_uid != os.getuid() or LAB.stat().st_mode & 0o077:
        raise ValueError("lab inventory must be an owned private directory")
    allowed = {
        "owner.json",
        "ready.json",
        "ready.json.next",
        "finished.json",
        "finished.json.next",
        "run.log",
        "runner",
    }
    if any(p.name not in allowed or not p.is_file() or p.is_symlink() for p in LAB.iterdir()):
        raise ValueError("unrecognized entry in the lab inventory; refusing teardown or reuse")


def read(name):
    return json.loads((LAB / name).read_text())


def identity(pid):
    proc = Path("/proc") / str(pid)
    return {
        "executable": os.readlink(proc / "exe"),
        "start": (proc / "stat").read_text().rsplit(")", 1)[1].split()[19],
    }


def owner():
    private_inventory()
    record = read("owner.json")
    handle = os.pidfd_open(record["pid"])
    try:
        if identity(record["pid"]) != record["process"] or record["process"]["executable"] != str(LAB / "runner"):
            raise ValueError("lab process identity changed")
    except BaseException:
        os.close(handle)
        raise
    return handle


def fingerprint():
    paths = [
        Path("Makefile"),
        *Path("test/minikube").glob("*.go"),
        *Path("test/minikube/fixtures/argocd").glob("*"),
        *Path("deployments/helm/open-agentic-catalog").rglob("*"),
    ]
    # Reuse requires the same Catalog revision and locally modified build inputs.
    checksum = hashlib.sha256(json.dumps(PINS, sort_keys=True).encode())
    for key in (
        "VERSION",
        "KUBERNETES_VERSION",
        "DRIVER",
        "CONTAINER_RUNTIME",
        "BASE_IMAGE",
        "POSTGRES_IMAGE",
    ):
        checksum.update(key.encode() + b"=" + os.environ.get("CATALOG_MINIKUBE_" + key, "").encode() + b"\0")
    checksum.update(
        subprocess.check_output([sys.executable, "-B", "test/packaging/versioning.py", "metadata"], timeout=15)
    )
    paths += [
        Path(p)
        for p in (
            "Containerfile",
            "go.mod",
            "go.sum",
            "VERSION",
            "LICENSE",
            ".dockerignore",
            "deployments/container/licenses.sh",
            "test/functional/Containerfile",
            "test/packaging/versioning.py",
        )
    ]
    for root in (
        "api",
        "cmd",
        "internal",
        "migrations",
        "schemas",
        "samples",
        "examples/webhookreceiver",
        "test/functional/fixture",
    ):
        paths.extend(Path(root).rglob("*"))
    for path in sorted(p for p in paths if p.is_file()):
        checksum.update(str(path).encode() + b"\0" + path.read_bytes())
    return checksum.hexdigest()


def owned_cluster():
    handle = owner()
    os.close(handle)
    state = read("ready.json")
    work = Path(state["work"])
    if (
        not re.fullmatch(r"open-agentic-catalog-argocd-[a-f0-9]{12}", state["profile"])
        or work.parent != Path(tempfile.gettempdir())
        or not work.name.startswith("open-agentic-catalog-minikube-argocd.")
    ):
        raise ValueError("ambiguous lab inventory")
    kube = [
        os.environ.get("CATALOG_KUBECTL_BIN", "kubectl"),
        "--kubeconfig",
        str(work / "kubeconfig"),
        "--context",
        state["profile"],
    ]
    uid = subprocess.check_output(
        [*kube, "get", "ns/kube-system", "-o", "jsonpath={.metadata.uid}"],
        text=True,
        timeout=20,
    )
    if uid != state["uid"]:
        raise ValueError("lab cluster UID changed")
    return state, work, kube


def status(verify_ready=False):
    state, work, kube = owned_cluster()
    subprocess.run(
        [
            *kube,
            "-n",
            "argocd",
            "get",
            "application/catalog-publication",
            "-o",
            "custom-columns=APPLICATION:.metadata.name,REVISION:.status.sync.revision,SYNC:.status.sync.status,HEALTH:.status.health.status,OPERATION:.status.operationState.phase",
        ],
        check=True,
        timeout=20,
    )
    subprocess.run(
        [
            *kube,
            "-n",
            "open-agentic-catalog-e2e",
            "get",
            "deploy/catalog-api",
            "deploy/catalog-worker",
        ],
        check=True,
        timeout=20,
    )
    if verify_ready:
        app = json.loads(
            subprocess.check_output(
                [
                    *kube,
                    "-n",
                    "argocd",
                    "get",
                    "application/catalog-publication",
                    "-o",
                    "json",
                ],
                timeout=20,
            )
        )
        evidence = json.loads((work / "argo-evidence.json").read_text())
        desired = evidence["inputs"]["initial"]["sha"]
        from admission import validate_installed

        validate_installed(app, "open-agentic-catalog-e2e", desired)
        subprocess.run(
            [
                ".tools/minikube-argocd/argocd",
                "--config",
                str(work / "argo-config"),
                "app",
                "get",
                "catalog-publication",
            ],
            env={"HOME": str(work), "PATH": os.environ["PATH"]},
            stdout=subprocess.DEVNULL,
            timeout=30,
            check=True,
        )
        url = urlsplit(state["catalog"])
        if url.scheme != "http" or url.hostname != "127.0.0.1" or not url.port:
            raise ValueError("unexpected Catalog lab address")
        connection = http.client.HTTPConnection(url.hostname, url.port, timeout=5)
        try:
            connection.request("GET", "/readyz")
            if connection.getresponse().status != 200:
                raise ValueError("the retained Catalog API is not ready")
        except (OSError, http.client.HTTPException) as error:
            raise ValueError(
                "cannot reach the retained Catalog port-forward; inspect the lab with make argocd-status"
            ) from error
        finally:
            connection.close()
    print(
        f"Profile {state['profile']}\nArgo CD {state['url']}\nCatalog {state['catalog']}\nKubeconfig {work / 'kubeconfig'}\nCLI config {work / 'argo-config'}\nAdmin password file {work / 'admin-password'}\nCA {work / 'ca.pem'}"
    )


def down():
    if not LAB.exists() and not LAB.is_symlink():
        print("No retained Argo CD lab.")
        return
    private_inventory()
    if not (LAB / "finished.json").exists():
        if (LAB / "ready.json").exists():
            owned_cluster()
        handle = owner()
        try:
            signal.pidfd_send_signal(handle, signal.SIGTERM)
        finally:
            os.close(handle)
        deadline = time.monotonic() + 300
        while not (LAB / "finished.json").exists() and time.monotonic() < deadline:
            time.sleep(0.5)
    finished = read("finished.json")
    if (
        not finished["cleanup"]
        or finished["pid"] != read("owner.json")["pid"]
        or not re.fullmatch("open-agentic-catalog-argocd-[a-f0-9]{12}", finished["profile"])
        or ((LAB / "ready.json").exists() and read("ready.json")["profile"] != finished["profile"])
    ):
        raise ValueError("scoped cleanup failed; inspect the retained lab log")
    shutil.rmtree(LAB)
    print("Retained Argo CD profile, database and private inputs removed.")


def up():
    prepare(check=True)
    baseline = fingerprint()
    if LAB.exists() or LAB.is_symlink():
        private_inventory()
        if read("owner.json")["baseline"] != baseline:
            raise ValueError("lab baseline changed; inspect and run make argocd-down first")
        status(verify_ready=True)
        return
    LAB.mkdir(mode=0o700)
    cmd = [str(LAB / "runner"), "--scenario", "argocd", "--argocd-lab"]
    try:
        subprocess.run(
            ["go", "build", "-tags=minikube", "-o", cmd[0], "./test/minikube"],
            check=True,
            timeout=120,
        )
        with (LAB / "run.log").open("x") as log:
            proc = subprocess.Popen(
                cmd,
                stdin=subprocess.DEVNULL,
                stdout=log,
                stderr=subprocess.STDOUT,
                start_new_session=True,
            )
    except BaseException:
        shutil.rmtree(LAB)
        raise
    (LAB / "owner.json").write_text(json.dumps({"pid": proc.pid, "process": identity(proc.pid), "baseline": baseline}))
    end = time.monotonic() + 3600
    while time.monotonic() < end:
        if proc.poll() is not None:
            raise RuntimeError("lab startup failed; inspect " + str(LAB / "run.log"))
        if (LAB / "ready.json").exists():
            status(verify_ready=True)
            return
        time.sleep(1)
    raise TimeoutError("lab startup timed out; inspect run.log and use make argocd-down")


def main():
    os.umask(0o077)
    LAB.parent.mkdir(exist_ok=True)
    with LOCK.open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        action = sys.argv[1]
        if action == "status" and not LAB.exists() and not LAB.is_symlink():
            print("No retained Argo CD lab.")
            return
        {"up": up, "down": down, "status": status}[action]()


if __name__ == "__main__":
    main()
