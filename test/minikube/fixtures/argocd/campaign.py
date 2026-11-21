"""Native Argo CD qualification against a single invocation-owned cluster."""

import base64
import copy
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import ssl
import subprocess
import sys
import time
import urllib.request

import yaml

from install import CACHE, PINS

from admission import (
    APP,
    ARGO,
    CLUSTER,
    GIT,
    HOOKS,
    PROJECT,
    RESOURCE,
    ROLES,
    TRACK,
    application,
    require,
    source,
    validate_app,
    validate_installed,
    validate_sync,
)


def digest(data):
    return hashlib.sha256(data).hexdigest()


def run(args, data=None, env=None, timeout=180):
    result = subprocess.run(
        args,
        input=data,
        text=True,
        capture_output=True,
        timeout=timeout,
        env=env,
        check=False,
    )
    if result.returncode:
        raise RuntimeError(f"{Path(args[0]).name} failed: " + result.stderr[:1200])
    return result.stdout


def wait(description, read, seconds=120):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        result = read()
        if result:
            return result
        time.sleep(1)
    raise TimeoutError(description)


class Campaign:
    def __init__(self, profile, ns, directory, kubectl, values, images, addr):
        self.work = Path(directory)
        require(
            re.fullmatch("open-agentic-catalog-argocd-[a-f0-9]{12}", profile)
            and ns == "open-agentic-catalog-e2e"
            and os.environ.get("MINIKUBE_PROFILE") == profile
            and Path(os.environ["KUBECONFIG"]) == self.work / "kubeconfig",
            "requires private Argo CD Minikube context",
        )
        self.kube = [
            kubectl,
            "--kubeconfig",
            os.environ["KUBECONFIG"],
            "--context",
            profile,
        ]
        self.ns, self.values, self.images, self.addr = (
            ns,
            Path(values),
            json.loads(images),
            addr,
        )
        self.config = self.work / "argo-config"
        self.receipt = self.work / "argo-evidence.json"
        self.evidence = (
            json.loads(self.receipt.read_text())
            if self.receipt.exists()
            else {
                "phases": dict.fromkeys(
                    (
                        "bootstrap",
                        "install",
                        "upgrade",
                        "faults",
                        "restore",
                        "restore-hpa",
                    ),
                    "unrun",
                )
            }
        )

    def k(self, *args, ns=None, data=None):
        return run([*self.kube, "-n", ns or self.ns, *args], data)

    def get(self, resource, *args, ns=None):
        return json.loads(self.k("get", resource, *args, "-o", "json", ns=ns))

    def apply(self, kind, name, body, api="v1", ns=ARGO):
        self.k(
            "apply",
            "-f",
            "-",
            ns=ns,
            data=json.dumps({"apiVersion": api, "kind": kind, "metadata": {"name": name}, **body}),
        )

    def record(self):
        pending = self.receipt.with_suffix(".next")
        pending.write_text(json.dumps(self.evidence, indent=2))
        pending.replace(self.receipt)

    def repo(self):
        checkout = self.work / "git"
        checkout.mkdir()
        chart = checkout / "chart"
        shutil.copytree("deployments/helm/open-agentic-catalog", chart)
        environment = {
            "PATH": os.environ["PATH"],
            "HOME": str(self.work),
            "GIT_CONFIG_NOSYSTEM": "1",
            "GIT_CONFIG_GLOBAL": os.devnull,
            "GIT_TERMINAL_PROMPT": "0",
        }

        def git(*args):
            return run(
                [
                    "git",
                    "-C",
                    str(checkout),
                    "-c",
                    "user.name=Catalog fixture",
                    "-c",
                    "user.email=fixture@localhost",
                    "-c",
                    "core.hooksPath=/dev/null",
                    *args,
                ],
                env=environment,
            ).strip()

        git("init", "-b", "fixture")
        initial = json.loads(self.values.read_text())
        # Keep failed Job evidence beyond even the one-hour maximum harness timeout.
        initial["migration"]["ttlSecondsAfterFinished"] = 7200
        self.evidence["namespaceUID"] = self.get("namespace/" + self.ns)["metadata"]["uid"]
        commits = {}
        for phase in ("initial", "target", "bad-role", "bad-start", "invalid"):
            values = copy.deepcopy(initial)
            if phase != "initial":
                values["image"]["tag"] = self.images["aliases"]["target"].split(":")[1]
            values["podAnnotations"]["open-agentic-catalog.github.io/test-rollout"] = phase
            if phase == "bad-role":
                values["databaseRoles"]["api"] = "catalog_api_missing"
            if phase == "bad-start":
                values["secrets"]["apiDatabase"]["name"] = "deliberately-absent-database"
            if phase == "invalid":
                values["api"]["replicas"] = 0
            path = chart / "fixture.json"
            path.write_text(json.dumps(values, sort_keys=True))
            git("add", "chart")
            git("commit", "-m", phase)
            git("branch", phase)
            commits[phase] = {
                "sha": git("rev-parse", "HEAD"),
                "values": digest(path.read_bytes()),
                "tree": git("rev-parse", "HEAD:chart"),
            }
        self.evidence["inputs"] = commits
        metadata = yaml.safe_load(git("show", "initial:chart/Chart.yaml"))
        self.evidence["chart"] = {
            "name": metadata["name"],
            "version": metadata["version"],
            "appVersion": metadata["appVersion"],
            "templatesTree": git("rev-parse", "initial:chart/templates"),
        }
        git("bundle", "create", str(self.work / "catalog.bundle"), "--branches")
        self.record()

    def bootstrap(self):
        self.repo()
        self.k("create", "namespace", ARGO)
        bootstrap = list(yaml.safe_load_all((CACHE / "install.yaml").read_text()))
        native = {ref.split("@")[0]: self.images["aliases"][key] for key, ref in PINS["images"].items()}
        for obj in bootstrap:
            kind, name = obj["kind"], obj["metadata"]["name"]
            if kind in ("Deployment", "StatefulSet"):
                pod = obj["spec"]["template"]["spec"]
                for container in pod["containers"] + pod.get("initContainers", []):
                    container.update(image=native[container["image"]], imagePullPolicy="Never")
            if kind == "ConfigMap" and name == "argocd-cm":
                obj["data"] = {
                    "application.resourceTrackingMethod": "annotation",
                    "accounts.maintenance": "apiKey",
                    "accounts.observer": "apiKey",
                }
            if kind == "ConfigMap" and name == "argocd-rbac-cm":
                obj["data"] = {
                    "policy.csv": "\n".join(
                        "p, maintenance, applications, " + action + ", " + PROJECT + "/" + APP + ", allow"
                        for action in ("get", "sync")
                    )
                    + "\np, maintenance, projects, get, "
                    + PROJECT
                    + ", allow\np, observer, applications, get, "
                    + PROJECT
                    + "/"
                    + APP
                    + ", allow"
                }
            if kind == "ConfigMap" and name == "argocd-tls-certs-cm":
                obj["data"] = {"catalog-git.argocd.svc": (self.work / "ca.pem").read_text()}
        for host, secret in (
            ("argocd-server", "argocd-server-tls"),
            ("catalog-git", "catalog-git-tls"),
        ):
            self.k(
                "create",
                "secret",
                "tls",
                secret,
                "--cert",
                str(self.work / (host + ".crt")),
                "--key",
                str(self.work / (host + ".key")),
                ns=ARGO,
            )
        self.network()
        self.k(
            "apply",
            "--server-side",
            "-f",
            "-",
            data=json.dumps({"apiVersion": "v1", "kind": "List", "items": bootstrap}),
            ns=ARGO,
        )
        bundle = json.loads(
            self.k(
                "create",
                "configmap",
                "fixture-source",
                "--from-file=" + str(self.work / "catalog.bundle"),
                "--from-file=" + str(Path(__file__).with_name("git_server.py")),
                "--dry-run=client",
                "-o",
                "json",
                ns=ARGO,
            )
        )
        bundle["immutable"] = True
        self.k("create", "-f", "-", data=json.dumps(bundle), ns=ARGO)
        for obj in yaml.safe_load_all(Path(__file__).with_name("git.yaml").read_text()):
            if obj["kind"] == "Deployment":
                pod = obj["spec"]["template"]["spec"]
                for container in (*pod["initContainers"], *pod["containers"]):
                    container["image"] = self.images["aliases"]["git"]
            self.k("create", "-f", "-", data=json.dumps(obj), ns=ARGO)
        for obj in (
            "deployment/catalog-git",
            "deployment/argocd-repo-server",
            "deployment/argocd-server",
            "deployment/argocd-redis",
            "deployment/argocd-dex-server",
            "deployment/argocd-applicationset-controller",
            "deployment/argocd-notifications-controller",
            "statefulset/argocd-application-controller",
        ):
            self.k("rollout", "status", obj, "--timeout=180s", ns=ARGO)

        running = {}
        expected_ids = {
            ref: self.images["ids"][name + "-config"]
            for name, ref in self.images["aliases"].items()
            if name != "target"
        }
        for pod in self.get("pods", ns=ARGO)["items"]:
            for section, statuses in (
                ("containers", "containerStatuses"),
                ("initContainers", "initContainerStatuses"),
            ):
                images = {c["name"]: c["image"] for c in pod["spec"].get(section, [])}
                for container in pod["status"].get(statuses, []):
                    ref = images[container["name"]]
                    require(
                        container["imageID"].endswith(expected_ids[ref]),
                        "running Argo or Git fixture image does not match its pin",
                    )
                    running[ref] = container["imageID"]
        require(set(running) == set(expected_ids), "incomplete native image inventory")
        self.evidence["runningImages"] = running
        self.record()

    def network(self):
        dns = {
            "namespaceSelector": {"matchLabels": {"kubernetes.io/metadata.name": "kube-system"}},
            "podSelector": {"matchLabels": {"k8s-app": "kube-dns"}},
        }
        ips = [self.get("svc/kubernetes", ns="default")["spec"]["clusterIP"]]
        endpoints = self.get(
            "endpointslices",
            "-l",
            "kubernetes.io/service-name=kubernetes",
            ns="default",
        )["items"]
        for item in endpoints:
            ips.extend(ip for endpoint in item["endpoints"] for ip in endpoint["addresses"])
        rules = [
            {
                "to": [{"podSelector": {"matchLabels": {"app.kubernetes.io/name": "argocd-" + component}}}],
                "ports": [{"port": port} for port in ports],
            }
            for component, ports in (
                ("redis", [6379]),
                ("repo-server", [8081]),
                ("dex-server", [5556, 5557]),
                ("server", [8080]),
            )
        ]
        rules += [
            {
                "to": [{"podSelector": {"matchLabels": {"app": "catalog-git"}}}],
                "ports": [{"port": 8443}],
            },
            {
                "to": [dns],
                "ports": [{"port": 53, "protocol": protocol} for protocol in ("TCP", "UDP")],
            },
            {
                "to": [{"ipBlock": {"cidr": ip + "/32"}} for ip in ips],
                "ports": [{"port": 443}, {"port": 8443}],
            },
        ]
        self.apply(
            "NetworkPolicy",
            "native-egress",
            {
                "spec": {
                    "podSelector": {"matchExpressions": [{"key": "app.kubernetes.io/name", "operator": "Exists"}]},
                    "policyTypes": ["Egress"],
                    "egress": rules,
                }
            },
            api="networking.k8s.io/v1",
        )
        self.apply(
            "NetworkPolicy",
            "git-reader",
            {
                "spec": {
                    "podSelector": {"matchLabels": {"app": "catalog-git"}},
                    "policyTypes": ["Ingress", "Egress"],
                    "egress": [],
                    "ingress": [
                        {
                            "from": [
                                {"podSelector": {"matchLabels": {"app.kubernetes.io/name": "argocd-repo-server"}}}
                            ],
                            "ports": [{"port": 8443}],
                        }
                    ],
                }
            },
            api="networking.k8s.io/v1",
        )

    def verify_network(self):
        # A real endpoint in the Catalog namespace is ready but unreachable from Git.
        self.k(
            "wait",
            "--for=condition=Available",
            "deployment/decoy-fixture",
            "--timeout=30s",
        )
        ip = self.get("svc/decoy-fixture")["spec"]["clusterIP"]
        probe = "import socket,sys\ntry: socket.create_connection((sys.argv[1],8443),3)\nexcept TimeoutError: sys.exit(0)\nsys.exit(1)"
        self.k("exec", "deployment/catalog-git", "--", "python3", "-c", probe, ip, ns=ARGO)
        refs = self.k(
            "exec",
            "deployment/argocd-repo-server",
            "-c",
            "argocd-repo-server",
            "--",
            "git",
            "-c",
            "http.sslCAInfo=/app/config/tls/catalog-git.argocd.svc",
            "ls-remote",
            GIT,
            "refs/heads/initial",
            ns=ARGO,
        )
        require(
            refs.split()[0] == self.sha("initial"),
            "repository server did not read the trusted Git ref",
        )
        # Both destinations are live: PostgreSQL already bootstrapped the Catalog roles,
        # and repo-server just fetched this Git service using its real TLS configuration.
        for caller, destination, port in (
            ("argocd-repo-server", self.get("service/postgres")["spec"]["clusterIP"], "5432"),
            ("argocd-server", self.get("service/catalog-git", ns=ARGO)["spec"]["clusterIP"], "8443"),
        ):
            probe = subprocess.run(
                [
                    *self.kube,
                    "-n",
                    ARGO,
                    "exec",
                    "deployment/" + caller,
                    "-c",
                    caller,
                    "--",
                    "timeout",
                    "3",
                    "bash",
                    "-c",
                    'exec 3<>"/dev/tcp/$1/$2"',
                    "probe",
                    destination,
                    port,
                ],
                capture_output=True,
                timeout=15,
                check=False,
            )
            require(probe.returncode == 124, f"expected a blocked TCP connection from {caller} to port {port}")

    def login(self):
        password = base64.b64decode(
            self.get("secret/argocd-initial-admin-secret", ns=ARGO)["data"]["password"]
        ).decode()
        ca = self.work / "ca.pem"
        client = urllib.request.build_opener(
            urllib.request.ProxyHandler({}),
            urllib.request.HTTPSHandler(context=ssl.create_default_context(cafile=ca)),
        )
        request = urllib.request.Request(
            "https://" + self.addr + "/api/v1/session",
            data=json.dumps({"username": "admin", "password": password}).encode(),
            headers={"Content-Type": "application/json"},
        )
        with client.open(request, timeout=15) as result:
            token = json.load(result)["token"]
        config = {
            "current-context": "fixture",
            "contexts": [{"name": "fixture", "server": self.addr, "user": "fixture"}],
            "servers": [
                {
                    "server": self.addr,
                    "certificate-authority-data": base64.b64encode(ca.read_bytes()).decode(),
                }
            ],
            "users": [{"name": "fixture", "auth-token": token}],
        }
        self.config.touch(mode=0o600)
        self.config.write_text(yaml.safe_dump(config))

        (self.work / "admin-password").touch(mode=0o600)
        (self.work / "admin-password").write_text(password)
        for account in ("observer", "maintenance"):
            access = self.cli("account", "generate-token", "--account", account, "--expires-in", "24h").strip()
            restricted = copy.deepcopy(config)
            restricted["users"][0]["auth-token"] = access
            target = self.work / ("argo-" + account)
            target.touch(mode=0o600)
            target.write_text(yaml.safe_dump(restricted))
        (self.work / "argo-maintenance").replace(self.config)

    def cli(self, *args, timeout=120, config=None):
        return run(
            [
                str(CACHE / "argocd"),
                "--config",
                str(config or self.config),
                "--server-crt",
                str(self.work / "ca.pem"),
                *args,
            ],
            env={"PATH": os.environ["PATH"], "HOME": str(self.work)},
            timeout=timeout,
        )

    def install(self):
        self.verify_network()
        self.login()
        versions = json.loads(self.cli("version", "-o", "json"))
        for component in ("client", "server"):
            actual = versions[component]
            require(
                actual["Version"].split("+")[0] == PINS["version"] and actual["GitCommit"] == PINS["commit"],
                "unexpected native Argo CD build: " + component,
            )
        self.evidence["argoVersion"] = {k: versions[k]["Version"] for k in ("client", "server")}
        self.apply(
            "AppProject",
            PROJECT,
            {
                "spec": {
                    "sourceRepos": [GIT],
                    "destinations": [{"server": CLUSTER, "namespace": self.ns}],
                }
            },
            api="argoproj.io/v1alpha1",
        )
        self.apply(
            "Application",
            APP,
            {"spec": application(self.ns, self.sha("initial"))},
            api="argoproj.io/v1alpha1",
        )
        rendered = self.preflight("initial")
        self.require_empty_destination(rendered)
        self.k("create", "configmap", "catalog-migration", "--from-literal=marker=unowned")
        try:
            self.require_empty_destination(rendered)
        except ValueError as error:
            require("configmap/catalog-migration" in str(error), "unexpected first-install conflict")
        else:
            raise ValueError("first install admitted a pre-existing migration hook")
        require(not self.app().get("operation") and self.drained(), "first-install refusal started Catalog")
        self.k("delete", "configmap/catalog-migration")
        self.sync("initial", initial=True)

    def sha(self, phase):
        return self.evidence["inputs"][phase]["sha"]

    def app(self):
        return self.get(RESOURCE, ns=ARGO)

    def authority(self, phase):
        project = self.get("appproject/" + PROJECT, ns=ARGO)["spec"]
        require(
            project["sourceRepos"] == [GIT]
            and project["destinations"] == [{"namespace": self.ns, "server": CLUSTER}]
            and not project.get("syncWindows")
            and not project.get("roles"),
            "unapproved AppProject or sync window",
        )
        require(
            self.cli("account", "can-i", "sync", "applications", PROJECT + "/" + APP).strip().lower() == "yes",
            "maintenance identity cannot sync this Application",
        )
        require(
            len(self.get("applications", ns=ARGO)["items"]) == 1 and not self.get("applicationsets", ns=ARGO)["items"],
            "another Application or ApplicationSet can control this install",
        )
        self.guard_hpa()
        obj = self.app()
        validate_app(obj, self.ns, self.sha(phase))
        return obj

    def guard_hpa(self):
        for item in self.get("hpa")["items"]:
            ref = item["spec"]["scaleTargetRef"]
            require(
                not (ref["kind"] == "Deployment" and ref["name"] in ("catalog-api", "catalog-worker")),
                "targeting HPA must be suspended",
            )

    def preflight(self, phase):
        self.authority(phase)
        self.cli("app", "get", APP, "--hard-refresh", "-o", "json")
        objects = list(yaml.safe_load_all(self.cli("app", "manifests", APP, "--revision", self.sha(phase))))
        indexed = {(o["kind"], o["metadata"]["name"]): o for o in objects if o}
        require(len(indexed) == len(objects), "duplicate or empty rendered resources")
        approved = run(
            [
                "git",
                "-C",
                str(self.work / "git"),
                "show",
                self.sha(phase) + ":chart/fixture.json",
            ],
            env={
                "HOME": str(self.work),
                "PATH": os.environ["PATH"],
                "GIT_CONFIG_NOSYSTEM": "1",
                "GIT_CONFIG_GLOBAL": os.devnull,
            },
        )
        require(
            digest(approved.encode()) == self.evidence["inputs"][phase]["values"],
            "fixture values digest changed",
        )
        values = json.loads(approved)
        image = values["image"]
        expected_image = image["repository"] + ":" + image["tag"]
        for kind, name, wave in HOOKS:
            annotations = indexed[kind, name]["metadata"]["annotations"]
            require(
                annotations.get("helm.sh/hook") == "pre-install,pre-upgrade"
                and annotations.get("helm.sh/hook-weight") == wave
                and annotations.get("helm.sh/hook-delete-policy") == "before-hook-creation,hook-succeeded",
                "unapproved migration hooks",
            )
        job = indexed["Job", "catalog-migrate"]["spec"]
        container = job["template"]["spec"]["containers"][0]
        require(
            job["activeDeadlineSeconds"] == values["migration"]["activeDeadlineSeconds"]
            and job["backoffLimit"] == 0
            and job["ttlSecondsAfterFinished"] > 3600
            and container["command"] == ["/usr/local/bin/catalogctl", "migrate", "apply"]
            and container["args"]
            == ["--api-role", values["databaseRoles"]["api"], "--worker-role", values["databaseRoles"]["worker"]]
            and container["env"]
            == [{"name": "CATALOG_DATABASE_DSN", "valueFrom": {"secretKeyRef": values["secrets"]["migrationDatabase"]}}]
            and container["image"] == expected_image
            and job["template"]["spec"]["serviceAccountName"] == "catalog-migration"
            and container["envFrom"] == [{"configMapRef": {"name": "catalog-migration"}}],
            "migration target changed",
        )
        for role in ROLES:
            spec = indexed["Deployment", "catalog-" + role]["spec"]
            require(
                spec["replicas"] == 2
                and spec["selector"] == {"matchLabels": self.selector(role)}
                and spec["template"]["spec"]["containers"][0]["image"] == expected_image,
                "changed replica, selector or image",
            )
        native_hash = digest(
            json.dumps(
                sorted(objects, key=lambda o: (o["kind"], o["metadata"]["name"])),
                sort_keys=True,
            ).encode()
        )
        require(
            self.evidence.setdefault("nativeRender", {}).setdefault(phase, native_hash) == native_hash,
            "native render changed for approved commit",
        )
        namespace_uid = self.get("namespace/" + self.ns)["metadata"]["uid"]
        require(
            namespace_uid == self.evidence.setdefault("namespaceUID", namespace_uid),
            "destination namespace changed",
        )
        self.evidence.setdefault("renders", {})[phase] = digest(
            json.dumps(indexed["ConfigMap", "catalog-migration"]["data"], sort_keys=True).encode()
        )
        self.record()
        return objects

    def require_empty_destination(self, objects):
        existing = self.k(
            "get",
            "--ignore-not-found",
            "-o",
            "name",
            "-f",
            "-",
            data=json.dumps({"apiVersion": "v1", "kind": "List", "items": objects}),
        ).strip()
        require(not existing, f"first install conflicts with existing objects: {existing}")

    @staticmethod
    def selector(role):
        return {
            "app.kubernetes.io/instance": "catalog",
            "app.kubernetes.io/component": role,
            "app.kubernetes.io/name": "open-agentic-catalog",
        }

    def pods(self, role):
        return self.get(
            "pods",
            "-l",
            ",".join(f"{key}={value}" for key, value in self.selector(role).items()),
        )["items"]

    def drained(self):
        return all(not self.pods(role) for role in ROLES)

    def drain(self, phase):
        self.authority(phase)
        deployments = {role: self.get("deployment/catalog-" + role) for role in ROLES}
        for role, deployment in deployments.items():
            require(
                deployment["metadata"]["annotations"].get(TRACK)
                == APP + ":apps/Deployment:" + self.ns + "/catalog-" + role
                and deployment["spec"]["selector"] == {"matchLabels": self.selector(role)},
                "refusing to drain an unowned workload or unexpected selector",
            )
        for role, deployment in deployments.items():
            # The worker's status can advance while the API is draining. Fence the
            # latest version only if it still identifies the objects we inspected.
            live = self.get("deployment/catalog-" + role)
            require(
                live["metadata"]["uid"] == deployment["metadata"]["uid"]
                and live["metadata"].get("annotations", {}).get(TRACK) == deployment["metadata"]["annotations"][TRACK]
                and live["spec"] == deployment["spec"],
                "deployment ownership or desired state changed during drain",
            )
            self.k(
                "scale",
                "deployment/catalog-" + role,
                "--resource-version=" + live["metadata"]["resourceVersion"],
                "--current-replicas=" + str(deployment["spec"]["replicas"]),
                "--replicas=0",
            )
            wait(
                "all " + role + " Pods must disappear",
                lambda: not self.pods(role),
                seconds=90,
            )
        current = json.loads(self.cli("app", "get", APP, "--hard-refresh", "-o", "json"))
        require(
            current["status"]["sync"]["status"] == "OutOfSync"
            and current["status"]["sync"]["revision"] == current["spec"]["source"]["targetRevision"]
            and self.drained(),
            "controller must observe drift without undoing the drain",
        )

    def select(self, old, new):
        current = self.authority(old)
        patch = [
            {
                "op": "test",
                "path": "/metadata/resourceVersion",
                "value": current["metadata"]["resourceVersion"],
            },
            {"op": "replace", "path": "/spec/source", "value": source(self.sha(new))},
        ]
        self.k("patch", RESOURCE, "--type=json", "-p", json.dumps(patch), ns=ARGO)
        self.preflight(new)

    def sql_args(self, query):
        return [
            *self.kube,
            "-n",
            self.ns,
            "exec",
            "postgres-0",
            "--",
            "psql",
            "--no-psqlrc",
            "-v",
            "ON_ERROR_STOP=1",
            "-U",
            "catalog_owner",
            "-d",
            "catalog",
            "-Atc",
            query,
        ]

    def sql(self, query):
        return run(self.sql_args(query)).strip()

    def ledger(self):
        value = self.sql(
            "SELECT count(*) || ':' || encode(sha256(convert_to(string_agg(version::text || ':' || checksum, ',' ORDER BY version), 'UTF8')), 'hex') FROM catalog_schema_migrations"
        )
        files = sorted(Path("migrations").glob("[0-9]*.sql"))
        expected = ",".join(f"{int(p.name.split('_')[0])}:{digest(p.read_bytes())}" for p in files)
        require(
            files and value == f"{len(files)}:{digest(expected.encode())}", "migration ledger differs from shipped SQL"
        )
        require(
            self.sql(
                "SELECT count(*)=2 AND bool_and(rolcanlogin AND NOT rolsuper AND NOT rolinherit AND rolbypassrls=(rolname='catalog_worker'))"
                " FROM pg_roles WHERE rolname IN ('catalog_worker','catalog_api')"
            )
            == "t",
            "runtime database identities changed",
        )
        require(
            self.sql(
                "SELECT has_column_privilege('catalog_api','resources','id','SELECT') "
                "AND NOT has_table_privilege('catalog_api','resources','DELETE') "
                "AND has_table_privilege('catalog_worker','jobs','DELETE') "
                "AND NOT has_table_privilege('catalog_api','jobs','DELETE')"
            )
            == "t",
            "runtime grant readback failed",
        )
        return value

    def observed_operation(self, phase, states):
        state = self.app().get("status", {}).get("operationState", {})
        if state.get("syncResult", {}).get("revision") != self.sha(phase):
            return None
        validate_sync(state["operation"], self.sha(phase))
        require(state["syncResult"].get("source") == source(self.sha(phase)), "native result applied another source")
        require(
            state["phase"] in states or state["phase"] in ("Running", "Terminating"),
            "unexpected native operation " + state["phase"] + ": " + state.get("message", "")[:300],
        )
        return state if state["phase"] in states else None

    def sync(self, phase, initial=False, fault=None):
        rendered = self.preflight(phase)
        if initial:
            self.require_empty_destination(rendered)
        require(self.drained(), "old runtime Pods still exist")
        name = "oac-argo-barrier"
        lock = (
            "SELECT pg_advisory_lock(880185172399);"
            if initial or fault in ("bad-role", "terminate")
            else "BEGIN; LOCK TABLE catalog_schema_migrations IN ACCESS EXCLUSIVE MODE;"
        )
        waiter = None
        with subprocess.Popen(
            self.sql_args(f"SET application_name='{name}'; {lock} SELECT pg_sleep(150)"),
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        ) as holder:
            try:
                wait(
                    "owner barrier acquired",
                    lambda: (
                        self.sql(
                            f"SELECT count(*) FROM pg_stat_activity WHERE application_name='{name}' AND wait_event='PgSleep'"
                        )
                        == "1"
                    ),
                    15,
                )
                if fault == "terminate":
                    # Simulate a client losing its connection before receiving a terminal sync receipt.
                    waiter = subprocess.Popen(
                        [
                            str(CACHE / "argocd"),
                            "--config",
                            str(self.config),
                            "--server-crt",
                            str(self.work / "ca.pem"),
                            "app",
                            "sync",
                            APP,
                        ],
                        env={"HOME": str(self.work), "PATH": os.environ["PATH"]},
                        stdout=subprocess.DEVNULL,
                        stderr=subprocess.DEVNULL,
                    )
                else:
                    self.cli("app", "sync", APP, "--async")
                blocked = (
                    " FROM pg_stat_activity a WHERE a.usename='catalog_owner' AND a.datname=current_database() "
                    "AND EXISTS (SELECT 1 FROM pg_stat_activity b WHERE b.application_name='oac-argo-barrier' "
                    "AND b.pid=ANY(pg_blocking_pids(a.pid)))"
                )
                owner = wait(
                    "owner SQL reached controlled barrier",
                    lambda: self.sql("SELECT a.pid || ':' || extract(epoch FROM a.backend_start)" + blocked),
                    90,
                )
                require(
                    re.fullmatch(r"[1-9]\d*:\d+\.\d+", owner),
                    "expected exactly one blocked owner session",
                )
                require(self.drained(), "runtime Pods appeared before owner SQL")
                hooks = {}
                created = []
                for kind, objname, _ in HOOKS:
                    obj = self.get(kind + "/" + objname)
                    hooks[kind] = obj["metadata"]["uid"]
                    created.append(obj["metadata"]["creationTimestamp"])
                    require(
                        all(prior["hooks"][kind] != hooks[kind] for prior in self.evidence.get("barriers", [])),
                        "migration reused a previous hook",
                    )

                    if kind == "ConfigMap":
                        require(
                            digest(json.dumps(obj["data"], sort_keys=True).encode()) == self.evidence["renders"][phase],
                            "live owner configuration changed",
                        )
                require(created == sorted(created), "migration hooks were created out of order")
                migrating = [
                    p for p in self.pods("migration") if p["metadata"]["ownerReferences"][0]["uid"] == hooks["Job"]
                ]
                require(
                    len(migrating) == 1
                    and migrating[0]["status"]["phase"] == "Running"
                    and migrating[0]["status"]["containerStatuses"][0]["imageID"].endswith(
                        self.images["ids"]["application" if initial else "target-config"]
                    ),
                    "owner Pod did not consume hook prerequisites",
                )
                self.evidence.setdefault("barriers", []).append(
                    {
                        "phase": phase,
                        "hooks": hooks,
                        "pod": migrating[0]["metadata"]["uid"],
                    }
                )
                self.record()
                if fault == "terminate":
                    require(
                        waiter.poll() is None,
                        "sync client already exited before disconnection",
                    )
                    waiter.kill()
                    waiter.wait(timeout=10)
                    active = self.observed_operation(phase, ("Running",))
                    require(active, "client exit lost the native operation")
                    # The independent CLI wait is bounded while the same operation still holds the Job.
                    try:
                        self.cli("app", "wait", APP, "--operation", "--timeout", "1")
                    except RuntimeError as error:
                        require(
                            any(
                                word in str(error).lower()
                                for word in (
                                    "timeout",
                                    "timed out",
                                    "deadline",
                                    "time out",
                                )
                            ),
                            "unexpected native wait error",
                        )
                    else:
                        raise ValueError("native wait returned success while owner SQL was blocked")
                    require(
                        self.observed_operation(phase, ("Running",))["startedAt"] == active["startedAt"],
                        "operation replaced after client disconnect",
                    )
                    self.cli("app", "terminate-op", APP)
                    wait(
                        "termination acknowledged",
                        lambda: self.observed_operation(phase, ("Failed", "Error")),
                    )
                    wait(
                        "terminated migration Job and Pods removed",
                        lambda: (
                            not self.k("get", "job/catalog-migrate", "--ignore-not-found", "-o", "name").strip()
                            and not self.pods("migration")
                        ),
                        seconds=45,
                    )
                    # A client disappearing does not interrupt PostgreSQL's wait on an advisory lock.
                    # The fixture owns this backend only while its identity and its blocker still match.
                    pid, started = owner.split(":")
                    backend = f" AND a.pid={int(pid)} AND extract(epoch FROM a.backend_start)={started}"
                    result = self.sql("SELECT pg_terminate_backend(a.pid, 5000)" + blocked + backend)
                    require(
                        result in ("", "t"),
                        "could not stop the identified migration session",
                    )
                    self.evidence["terminatedOwnerSQL"] = "terminated blocked session" if result else "already stopped"
                    self.record()
                    wait(
                        "identified migration session stopped",
                        lambda: self.sql("SELECT count(*) FROM pg_stat_activity a WHERE true" + backend) == "0",
                        seconds=15,
                    )
            finally:
                if waiter is not None and waiter.poll() is None:
                    waiter.kill()
                    waiter.wait(timeout=5)
                self.sql(f"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name='{name}'")
                holder.wait(timeout=12)
        if fault == "terminate":
            wait(
                "terminated native operation",
                lambda: self.observed_operation(phase, ("Failed", "Error")),
            )
            wait(
                "owner Pod stopped",
                lambda: not any(p["status"]["phase"] in ("Pending", "Running") for p in self.pods("migration")),
            )
            require(self.drained(), "terminated operation started runtimes")
            require(
                self.sql(
                    "SELECT count(*) FROM pg_stat_activity WHERE usename='catalog_owner' AND datname=current_database() "
                    "AND backend_type='client backend' AND client_addr IS NOT NULL AND state IS DISTINCT FROM 'idle'"
                )
                == "0"
                and self.ledger() == self.evidence["ledger"],
                "owner SQL or retained ledger uncertain after termination",
            )
            return
        if fault == "bad-role":
            state = wait(
                "native migration refusal",
                lambda: self.observed_operation(phase, ("Failed", "Error")),
            )
            require(
                any(
                    r.get("name") == "catalog-migrate" and r.get("hookPhase") == "Failed"
                    for r in state["syncResult"]["resources"]
                )
                and self.drained(),
                "missing failed migration or runtimes restarted",
            )
            failed = self.get("job/catalog-migrate")
            require(
                failed["metadata"]["uid"] == hooks["Job"] and failed["status"].get("failed") == 1,
                "failed Job evidence changed",
            )
            require(
                self.k("logs", "job/catalog-migrate").strip()
                == "catalogctl: migrate: INTERNAL: operation failed; check configuration and dependency availability"
                and self.sql("SELECT count(*) FROM pg_roles WHERE rolname='catalog_api_missing'") == "0"
                and self.ledger() == self.evidence["ledger"],
                "expected exact role-validation refusal",
            )
            self.evidence["failedJob"] = failed["metadata"]["uid"]
            self.record()
            return
        state = wait(
            "native operation completion",
            lambda: self.observed_operation(phase, ("Succeeded",)),
            180,
        )
        require(
            any(
                r.get("name") == "catalog-migrate" and r.get("hookPhase") == "Succeeded"
                for r in state["syncResult"]["resources"]
            ),
            "missing successful owner Job result",
        )
        wait(
            "native successful hook cleanup",
            lambda: all(
                not self.k("get", kind + "/" + name, "--ignore-not-found", "-o", "name").strip()
                for kind, name, _ in HOOKS
            ),
        )
        ledger = self.ledger()
        require(
            self.evidence.setdefault("ledger", ledger) == ledger,
            "same-format migration ledger changed",
        )
        if fault == "bad-start":
            wait(
                "target cannot start without required database Secret",
                lambda: any(
                    "CreateContainerConfigError" == status.get("state", {}).get("waiting", {}).get("reason")
                    and 'secret "deliberately-absent-database" not found'
                    in status["state"]["waiting"].get("message", "")
                    for p in self.pods("api")
                    for status in p["status"].get("containerStatuses", [])
                ),
            )
            require(
                self.app()["status"]["health"]["status"] != "Healthy",
                "startup failure was treated as healthy",
            )
            return
        for role in ROLES:
            self.k("rollout", "status", "deployment/catalog-" + role, "--timeout=150s")
            pods = self.pods(role)
            image_id = self.images["ids"]["application" if initial else "target-config"]
            require(
                len(pods) == 2
                and all(
                    p["status"]["containerStatuses"][0]["ready"]
                    and p["status"]["containerStatuses"][0]["imageID"].endswith(image_id)
                    for p in pods
                ),
                "target image or ready replica mismatch",
            )
        self.cli("app", "wait", APP, "--health", "--sync", "--timeout", "90")
        current = self.app()
        validate_installed(current, self.ns, self.sha(phase))
        self.evidence.setdefault("deployments", []).append(
            {
                "desired": self.sha(phase),
                "operationRevision": state["syncResult"]["revision"],
                "compared": current["status"]["sync"]["revision"],
                "operation": state["phase"],
                "health": current["status"]["health"]["status"],
                "imageID": image_id,
                "api": 2,
                "worker": 2,
                "namespaceUID": self.get("namespace/" + self.ns)["metadata"]["uid"],
            }
        )
        self.record()

    def hpa(self, name, target, replicas=2):
        self.apply(
            "HorizontalPodAutoscaler",
            name,
            {
                "spec": {
                    "minReplicas": replicas,
                    "maxReplicas": replicas,
                    "scaleTargetRef": {
                        "apiVersion": "apps/v1",
                        "kind": "Deployment",
                        "name": target,
                    },
                }
            },
            api="autoscaling/v2",
            ns=self.ns,
        )

    def refusals(self):
        def unchanged():
            return sorted(p["metadata"]["uid"] for role in ROLES for p in self.pods(role))

        before = unchanged()
        operation = self.app()["status"]["operationState"]["startedAt"]
        worker = "deployment/catalog-worker"
        tracking = self.get(worker)["metadata"]["annotations"][TRACK]
        self.k(
            "annotate",
            worker,
            "--overwrite",
            TRACK + "=other-application:apps/Deployment:" + self.ns + "/catalog-worker",
        )
        try:
            self.drain("initial")
        except ValueError as error:
            require("unowned workload" in str(error), "unexpected ownership refusal")
        else:
            raise ValueError("drain admitted a worker tracked by another Application")
        require(
            all(self.get("deployment/catalog-" + role)["spec"]["replicas"] == 2 for role in ROLES)
            and unchanged() == before,
            "refusing the worker changed the API or worker replicas",
        )
        self.k("annotate", worker, "--overwrite", TRACK + "=" + tracking)
        override = [{"name": "api.replicas", "value": "2"}]

        def patch_parameters(operations):
            self.k("patch", RESOURCE, "--type=json", "-p", json.dumps(operations), ns=ARGO)

        patch_parameters(
            [
                {
                    "op": "test",
                    "path": "/spec/source",
                    "value": source(self.sha("initial")),
                },
                {
                    "op": "add",
                    "path": "/spec/source/helm/parameters",
                    "value": override,
                },
            ]
        )
        try:
            self.authority("initial")
        except ValueError as error:
            require("desired revision" in str(error), "unexpected override refusal")
        else:
            raise ValueError("unreviewed Helm parameter accepted")
        patch_parameters(
            [
                {
                    "op": "test",
                    "path": "/spec/source/helm/parameters",
                    "value": override,
                },
                {"op": "remove", "path": "/spec/source/helm/parameters"},
            ]
        )
        self.hpa("catalog-neighbor", "decoy-fixture", replicas=1)
        self.evidence["neighborDeployment"] = self.get("deployment/decoy-fixture")["metadata"]["uid"]
        self.evidence["neighborHPA"] = self.get("hpa/catalog-neighbor")["metadata"]["uid"]
        self.hpa("catalog-maintenance", "catalog-api")
        try:
            self.authority("initial")
        except ValueError as error:
            require("targeting HPA" in str(error), "unrelated HPA refusal")
        else:
            raise ValueError("targeting HPA accepted")
        self.evidence["approvedHPA"] = self.get("hpa/catalog-maintenance")["spec"]
        self.k("delete", "hpa/catalog-maintenance")
        try:
            self.authority("target")
        except ValueError as error:
            require("desired revision" in str(error), "unrelated revision refusal")
        else:
            raise ValueError("wrong desired revision accepted")
        observer = self.work / "argo-observer"
        require(
            self.cli(
                "account",
                "can-i",
                "sync",
                "applications",
                PROJECT + "/" + APP,
                config=observer,
            )
            .strip()
            .lower()
            == "no",
            "observer can start migration",
        )
        try:
            self.cli("app", "sync", APP, "--async", config=observer)
        except RuntimeError as error:
            require(
                "permissiondenied" in str(error).lower() or "permission denied" in str(error).lower(),
                "unexpected observer refusal",
            )
        else:
            raise ValueError("observer sync admitted")
        try:
            self.select("initial", "invalid")
        except RuntimeError as error:
            require(
                "replicas" in str(error) or "schema" in str(error),
                "unexpected invalid render error",
            )
        else:
            raise ValueError("invalid target rendered")
        self.select("invalid", "initial")
        require(
            self.app()["status"]["operationState"]["startedAt"] == operation
            and unchanged() == before
            and self.sql(
                "SELECT count(*) FROM pg_stat_activity WHERE usename='catalog_owner' AND wait_event_type='Lock'"
            )
            == "0",
            "negative preflight changed running Pods or started owner work",
        )
        self.record()

    def restore_hpa(self):
        neighbor = self.get("hpa/catalog-neighbor")
        require(
            neighbor["metadata"]["uid"] == self.evidence["neighborHPA"]
            and neighbor["spec"]["scaleTargetRef"]["name"] == "decoy-fixture"
            and self.get("deployment/decoy-fixture")["metadata"]["uid"] == self.evidence["neighborDeployment"],
            "unrelated HPA was replaced or retargeted",
        )
        self.apply(
            "HorizontalPodAutoscaler",
            "catalog-maintenance",
            {"spec": self.evidence["approvedHPA"]},
            api="autoscaling/v2",
            ns=self.ns,
        )
        require(
            self.get("hpa/catalog-maintenance")["spec"] == self.evidence["approvedHPA"],
            "restored HPA differs from the approved configuration",
        )

    def upgrade(self):
        self.refusals()
        self.select("initial", "target")
        self.drain("target")
        self.sync("target")

    def faults(self):
        self.select("target", "bad-role")
        self.drain("bad-role")
        self.sync("bad-role", fault="bad-role")
        self.select("bad-role", "bad-start")
        self.sync("bad-start", fault="bad-start")
        self.select("bad-start", "target")
        self.drain("target")
        self.sync("target")

    def restore(self):
        self.select("target", "bad-start")
        self.drain("bad-start")
        self.sync("bad-start", fault="terminate")
        self.select("bad-start", "target")
        self.sync("target")


if __name__ == "__main__":
    phase, *arguments = sys.argv[1:]
    campaign = Campaign(*arguments)
    campaign.evidence["phases"][phase] = "started"
    campaign.record()
    try:
        {
            "bootstrap": campaign.bootstrap,
            "install": campaign.install,
            "upgrade": campaign.upgrade,
            "faults": campaign.faults,
            "restore": campaign.restore,
            "restore-hpa": campaign.restore_hpa,
        }[phase]()
    except Exception:
        campaign.evidence["phases"][phase] = "failed"
        campaign.record()
        raise
    campaign.evidence["phases"][phase] = "passed"
    campaign.record()
