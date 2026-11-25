"""One disposable PostgreSQL-backed Catalog and native SPIRE trust domain."""
import json
import os
from pathlib import Path
import subprocess
import sys
import time

import tools

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / "test/functional"))
from tutorial_oidc import API, TENANTS, prepare  # noqa: E402

ISSUER = "https://identity.test:8447"
AUDIENCE = "open-agentic-catalog"
DOMAIN = "spiffe://workloads.test/"
UIDS = {"reporter": 11001, "control": 11002, "admin": 11003, "reviewer": 11004, "unassigned": 11005, "issuer": 11006}


def until(action, timeout=75):
    deadline = time.monotonic() + timeout
    while True:
        try:
            value = action()
            if value:
                return value
        except (ValueError, OSError, subprocess.CalledProcessError) as error:
            if time.monotonic() >= deadline:
                raise TimeoutError("Fixture did not become ready") from error
        if time.monotonic() >= deadline:
            raise TimeoutError("Fixture condition did not become true")
        time.sleep(1)


def certificates(path, hosts):
    trust = path / "trust"
    prepare.openssl("req", "-x509", "-newkey", "rsa:2048", "-noenc", "-days", "2", "-sha256",
                    "-subj", "/CN=Disposable SPIRE qualification", "-addext", "basicConstraints=critical,CA:TRUE",
                    "-addext", "keyUsage=critical,keyCertSign,cRLSign", "-keyout", trust / "ca-key.pem", "-out", trust / "ca.pem")
    (trust / "ca.pem").chmod(0o444)
    for name, host in hosts:
        prepare.certificate(trust, name, [host])


def build_binaries(directory, arch):
    tools.archives(arch, destination=directory)
    env = dict(os.environ, CGO_ENABLED="0", GOOS="linux", GOARCH=arch)
    for binary, source, module in (("frontend", ROOT / "examples/tutorials/local-oidc/tls-frontend.go", ROOT),
                                   ("reader", tools.HERE / "reader.go", ROOT / "sdk/go")):
        subprocess.run(["go", "build", "-trimpath", "-o", directory / binary, source], cwd=module,
                       env=env, check=True, timeout=240)
        # These files are mounted read-only and run by different container UIDs.
        (directory / binary).chmod(0o555)


def inputs(path, image, arch):
    for name in ("trust", "a", "bin", "server", "bootstrap"):
        (path / name).mkdir(mode=0o700)
    certificates(path, (("identity", "identity.test"), ("catalog-a", "catalog-a.test"),
                        ("catalog-b", "catalog-b.test"), ("postgres-a", "postgres-a")))
    target = path / "a"
    db, passwords = prepare.database_inputs(target, "a")
    prepare.document(target / "federation-policy.json", {"schemaVersion": 1, "policyId": "spire-test",
                     "revision": 1, "public": {"mode": "disabled"}})
    for role in ("api", "worker", "owner"):
        config = json.loads((ROOT / "config/production.example.json").read_text())
        config["http"]["publicBaseUrl"] = "https://catalog-a.test:8443"
        config["auth"].update(issuer=ISSUER, trustBundleFile="/trust/ca.pem", audience=AUDIENCE,
                              tenancyMode="dedicated", dedicatedTenantId=TENANTS["a"], enforceScopes=False)
        config["discovery"]["hosts"] = [{"host": f"catalog-{side}.test", "tenantId": tenant,
                 "mode": "authenticated", "publisherDomain": "tutorial.catalog.example" if side == "a" else "other.example"}
                 for side, tenant in TENANTS.items()]
        config["federation"]["policyFile"] = "/inputs/a/federation-policy.json"
        login = "catalog_" + role
        config["database"]["dsn"] = f"postgres://{login}@postgres-a/{db}?sslmode=verify-full"
        prepare.document(target / (role + ".json"), config, 0o644)
        prepare.write(target / (role + ".dsn"), f"postgres://{login}:{passwords[login]}@postgres-a/{db}?sslmode=verify-full&sslrootcert=/trust/ca.pem", 0o444)
    build_binaries(path / "bin", arch)
    env = {key: os.environ[key] for key in ("PATH", "HOME", "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG",
                                          "XDG_RUNTIME_DIR", "SSH_AUTH_SOCK") if key in os.environ}
    return dict(env, FIXTURE=str(path), CATALOG_TUTORIAL_BACKEND_IMAGE=image, CATALOG_TUTORIAL_DIRECTORY=str(path),
                CATALOG_TUTORIAL_UI_IMAGE=image, CATALOG_TUTORIAL_FRONTEND_IMAGE=image, CATALOG_TUTORIAL_SOURCE=str(tools.HERE))


class Fixture:
    def __init__(self, path, project, env):
        self.path, self.env, self.project = path, env, project
        self.ports = {}
        self.compose_file = tools.HERE / "compose.yaml"

    def dc(self, *args, check=True):
        return subprocess.run(["docker", "compose", "--env-file", "/dev/null", "-f", self.compose_file,
                               "-p", self.project, *args], env=self.env, check=check, capture_output=True, timeout=180)

    def server(self, *args):
        return self.dc("exec", "-T", "server", "/opt/spire-server", *args).stdout

    def server_issuer(self, issuer):
        config = (tools.HERE / "server.conf").read_text().replace('    jwt_issuer = "' + ISSUER + '"',
                 '    jwt_issuer = "' + issuer + '"' if issuer else "")
        (self.path / "server/server.conf").write_text(config)

    def start(self):
        resolved = self.dc("config", "--format", "json").stdout
        self.compose_file = self.path.parent / "compose.json"
        self.compose_file.write_bytes(resolved)
        self.server_issuer(ISSUER)
        self.dc("up", "-d", "postgres-a", "server", "frontend")
        until(lambda: self.server("healthcheck"))
        bundle = self.server("bundle", "show", "-format", "pem")
        (self.path / "bootstrap/bundle.pem").write_bytes(bundle)
        join = json.loads(self.server("token", "generate", "-spiffeID", DOMAIN + "agent", "-output", "json"))
        prepare.write(self.path / "bootstrap/join", join["value"])
        for name, uid in UIDS.items():
            self.server("entry", "create", "-parentID", DOMAIN + "agent", "-spiffeID", DOMAIN + name,
                        "-selector", "unix:uid:" + str(uid), "-jwtSVIDTTL", "40" if name == "reporter" else "900")
        self.dc("up", "-d", "agent", "issuer")
        self.dc("up", "-d", "--wait", "postgres-a")
        self.dc("run", "--rm", "-T", "owner-a", "migrate", "apply", "--config", "/inputs/a/owner.json")
        self.dc("run", "--rm", "-T", "owner-a", "identity", "provision", "--config", "/inputs/a/owner.json",
                "--tenant", TENANTS["a"], "--tenant-name", "SPIRE qualification", "--issuer", ISSUER,
                "--subject", DOMAIN + "admin", "--actor-type", "service", "--roles", "tenant_admin,contributor,publisher,reader",
                "--operator", "spire-qualification", "--reason", "Owned publication fixture")
        self.dc("up", "-d", "--wait", "api-a", "worker-a")
        for side, port in (("a", 8443), ("b", 8445)):
            self.ports[side] = int(self.dc("port", "frontend", str(port)).stdout.strip().rsplit(b":", 1)[1])

    def client(self, token="", side="a"):
        return API(self.path, side, token, loopback_port=self.ports[side])

    def issue(self, name, *, audience=AUDIENCE, selected=None, denied=False):
        identity = DOMAIN + (selected or name)
        uid = str(UIDS[name])
        proc = self.dc("run", "--rm", "--no-deps", "-T", "--user", uid + ":" + uid, "--entrypoint", "/opt/spire-agent",
                       "reader", "api", "fetch", "jwt", "-socketPath", "/workload/api.sock", "-spiffeID", identity,
                       "-audience", audience, "-output", "json", check=False)
        if denied:
            if not proc.returncode or b"PermissionDenied" not in proc.stderr + proc.stdout or b'"svid"' in proc.stdout:
                raise ValueError("Expected workload identity selection refusal")
            return None
        if proc.returncode:
            raise ValueError("Native workload credential unavailable for " + name)
        result = json.loads(proc.stdout)[0]["svids"]
        if len(result) != 1 or result[0]["spiffe_id"] != identity or not result[0]["svid"]:
            raise ValueError("Unexpected attested identity")
        return result[0]["svid"]

    def stop(self):
        self.dc("down", "--volumes", "--remove-orphans", "--timeout", "5")
