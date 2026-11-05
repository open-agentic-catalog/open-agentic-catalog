"""Read-only pin/locality checks and bounded clients for the selected Directory lab."""

import hashlib
import ipaddress
import json
import os
from pathlib import Path
import platform
import re
import ssl
import urllib.error
import urllib.parse
import urllib.request
import uuid

from common import run
from agntcy_contract import document_digest
from peer_wire import NoRedirect
import runner as shared

ASSETS = shared.HERE / "agntcy"
LIMIT = 2 * 1024 * 1024


class UnexpectedStatus(AssertionError):
    """Retain HTTP status for narrowly bounded readiness/index polling."""

    def __init__(self, peer, path, expected, actual, raw=b"", headers=None):
        super().__init__(f"{peer} {path}: expected HTTP {expected}, got {actual}")
        self.status, self.raw, self.headers = actual, raw, headers or {}


def read_json(path):
    """Bound local metadata before decoding; never read a private credential as evidence."""
    with path.open("rb") as stream:
        raw = stream.read(LIMIT + 1)
    if len(raw) > LIMIT:
        raise ValueError("lab metadata exceeds size bound")
    return json.loads(raw)


def file_digest(path):
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(65536), b""):
            digest.update(chunk)
    return digest.hexdigest()


def environment(private):
    """No inherited credentials, proxies, Directory contexts or Catalog overrides."""
    return dict(PATH=os.environ.get("PATH", ""), LANG="C.UTF-8",
                XDG_CONFIG_HOME=str(private / "client-config"), XDG_CACHE_HOME=str(private / "client-cache"),
                XDG_DATA_HOME=str(private / "client-data"), TMPDIR=str(private))


def selected_lab(value):
    if not value:
        raise ValueError("AGNTCY_LAB_DIR is required; see test/interoperability/agntcy/README.md")
    lab = Path(value).resolve(strict=True)
    state = read_json(lab / "private/state.json")
    for key, name in (("kubeconfig", "kubeconfig"), ("minikubeHome", "minikube")):
        if Path(state[key]).resolve() != lab / "private" / name:
            raise ValueError("lab state must use its own private Kubernetes inputs")
    for key in ("profile", "namespace", "network", "release"):
        if not isinstance(state.get(key), str) or not re.fullmatch(r"[a-z0-9][a-z0-9-]{0,62}", state[key]):
            raise ValueError("invalid lab identity: " + key)
    if not state["profile"].startswith("agntcy-ard-") or state["namespace"] != "agntcy-research":
        raise ValueError("select the isolated Directory research lab")
    return lab, state


def validate_kubeconfig(document, state):
    """Never let an ambient context or exec authentication select a remote cluster."""
    context = next(item["context"] for item in document["contexts"] if item["name"] == state["profile"])
    cluster = next(item["cluster"] for item in document["clusters"] if item["name"] == context["cluster"])
    user = next(item["user"] for item in document["users"] if item["name"] == context["user"])
    parsed = urllib.parse.urlsplit(cluster["server"])
    if (parsed.scheme != "https" or parsed.hostname != "127.0.0.1" or not parsed.port
            or parsed.username or parsed.password or parsed.path or parsed.query or parsed.fragment
            or cluster.get("insecure-skip-tls-verify") or cluster.get("proxy-url")
            or "exec" in user or "auth-provider" in user):
        raise ValueError("lab Kubernetes API must use verified loopback TLS without auth plugins")


def pod_identities(document, lock):
    """Require every selected workload to be ready at its exact recorded image digest."""
    found = {}
    wanted = {value: key for key, value in lock["images"].items()}
    for pod in document["items"]:
        if pod["metadata"].get("deletionTimestamp"):
            raise ValueError("lab has a terminating workload")
        statuses = {item["name"]: item for item in pod.get("status", {}).get("containerStatuses", [])}
        for container in pod["spec"]["containers"]:
            status = statuses.get(container["name"], {})
            label = wanted.get(container["image"])
            if container["image"] == "agntcy-ard-schema-fixture:local":
                label, digest = "router", lock["router_image_id"]
            elif label:
                digest = container["image"].split("@", 1)[1]
            else:
                raise ValueError("lab workload has an unreviewed image")
            if label in found or status.get("ready") is not True or status.get("imageID", "").split("@")[-1] != digest:
                raise ValueError("lab workload is unready, duplicated or differs from its image pin: " + label)
            found[label] = dict(uid=pod["metadata"]["uid"], container_id=status["containerID"], image_id=status["imageID"])
    if set(found) != set(lock["images"]) | {"router"}:
        raise ValueError("missing pinned lab workload")
    return found


class Lab:
    """Only fixed loopback CLI and read-only operations on the explicitly selected lab."""

    def __init__(self, value, private):
        self.path, self.state = selected_lab(value)
        self.lock = read_json(ASSETS / "lock.json")
        self.env = environment(private)
        self.cache = private / "kube-cache"
        self.cli = self.path / "artifacts/dirctl-linux-amd64"
        self.certificate = self.path / "private/tls.crt"

    def kubectl(self, *arguments):
        return run(["kubectl", "--cache-dir", str(self.cache), "--kubeconfig", self.state["kubeconfig"], "--context", self.state["profile"],
                    "--namespace", self.state["namespace"], *arguments], env=self.env, timeout=20, limit=LIMIT)

    def native(self, *arguments, check=True):
        return run([str(self.cli), "--server-addr", self.lock["grpc_address"], "--auth-mode", "insecure", *arguments],
                   env=self.env, timeout=30, limit=LIMIT, check=check)

    def workloads(self):
        return pod_identities(json.loads(self.kubectl("get", "pods", "-o", "json").stdout), self.lock)

    def preflight(self):
        if platform.system() != "Linux" or platform.machine() not in ("x86_64", "amd64"):
            raise ValueError("selected lab pins qualify Linux AMD64 only")
        shared.local_daemon()
        network = json.loads(shared.docker("network", "inspect", self.state["network"]).stdout)[0]
        gateway = urllib.parse.urlsplit(self.lock["tls_origin"]).hostname
        if (gateway not in [item.get("Gateway") for item in network["IPAM"]["Config"]]
                or self.state["profile"] not in [item["Name"] for item in network["Containers"].values()]):
            raise ValueError("TLS fixture must be on the selected local Minikube bridge")
        source = self.path / "dir"
        if (run(["git", "-C", str(source), "rev-parse", "HEAD"], timeout=10).stdout.strip() != self.lock["revision"]
                or run(["git", "-C", str(source), "status", "--porcelain", "--untracked-files=no"], timeout=10).stdout.strip()):
            raise ValueError("Directory source is not the unchanged reviewed revision")
        for path, digest in ((self.cli, self.lock["cli_sha256"]),
                             (self.path / "artifacts/dir-v1.7.1.tgz", self.lock["chart_sha256"])):
            if file_digest(path) != digest:
                raise ValueError("lab artifact checksum mismatch: " + path.name)
        version = self.native("version").stdout.strip()
        if version != "Application Version: v1.7.1 (7bf5795c)":
            raise ValueError("Directory CLI version differs from its pin")
        validate_kubeconfig(json.loads(self.kubectl("config", "view", "--raw", "-o", "json").stdout), self.state)
        releases = json.loads(run(["helm", "--kubeconfig", self.state["kubeconfig"], "--kube-context", self.state["profile"],
            "--namespace", self.state["namespace"], "list", "--output", "json"], env=self.env, timeout=20, limit=LIMIT).stdout)
        release = next((item for item in releases if item["name"] == self.state["release"]), {})
        if release.get("chart") != "dir-v1.7.1" or release.get("status") != "deployed":
            raise ValueError("lab Helm release differs from the deployed chart pin")
        policies = json.loads(self.kubectl("get", "networkpolicies", "-o", "json").stdout)
        if {item["metadata"]["name"]: document_digest(item["spec"]) for item in policies["items"]} != self.lock["network_policy_sha256"]:
            raise ValueError("lab isolation policy differs from the reviewed local-only setup")
        return dict(revision=self.lock["revision"], cli_version=version, cli_sha256=self.lock["cli_sha256"],
                    chart_sha256=self.lock["chart_sha256"], certificate_sha256=file_digest(self.certificate),
                    workloads=self.workloads())


class LocalHTTP:
    """No redirects, proxies or arbitrary destinations; Catalog credentials stay local."""

    timeout = 5

    def __init__(self, origins, certificate, token="local-admin"):
        self.token = token
        for origin in origins.values():
            parsed = urllib.parse.urlsplit(origin)
            address = ipaddress.ip_address(parsed.hostname)
            if (parsed.scheme not in ("http", "https") or not parsed.port or parsed.path
                    or parsed.username or parsed.password or parsed.query or parsed.fragment
                    or not (address.is_loopback or address.is_private)
                    or address.is_unspecified or address.is_multicast or address.is_link_local or address.is_reserved
                    or (parsed.scheme == "http" and not address.is_loopback)):
                raise ValueError("local fixture origin rejected")
        self.origins = dict(origins)
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect(),
            urllib.request.HTTPSHandler(context=ssl.create_default_context(cafile=str(certificate))))

    def request(self, peer, path, body=None, *, expected=200, etag=None, method=None):
        if peer not in self.origins or not path.startswith("/") or path.startswith("//") or "\\" in path:
            raise ValueError("fixture origin/path rejected")
        headers = {"Accept": "application/json"}
        if peer == "catalog":
            headers["Authorization"] = "Bearer " + self.token
            if body is not None:
                headers["Idempotency-Key"] = uuid.uuid4().hex
        if etag:
            headers["If-Match"] = etag
        if body is not None:
            headers["Content-Type"] = "application/json"
        request = urllib.request.Request(self.origins[peer] + path, None if body is None else json.dumps(body).encode(), headers, method=method)
        try:
            response = self.opener.open(request, timeout=self.timeout)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            raw = response.read(LIMIT + 1)
            if len(raw) > LIMIT:
                raise ValueError("fixture response exceeds size bound")
            if response.status != expected:
                raise UnexpectedStatus(peer, path, expected, response.status, raw,
                                       {key.lower(): value for key, value in response.headers.items()})
            return raw, {key.lower(): value for key, value in response.headers.items()}

    def document(self, peer, path, body=None, **options):
        raw, headers = self.request(peer, path, body, **options)
        return json.loads(raw), headers
