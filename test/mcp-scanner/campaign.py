"""One owned Catalog/worker/MCP Compose campaign, with a host-side receiver."""

import argparse
import base64
import hashlib
import hmac
import http.client
import importlib.util
import ipaddress
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import signal
import socket
import sys
import tempfile
import time

ROOT = Path(__file__).resolve().parents[2]
HERE = Path(__file__).resolve().parent
OUTPUT = ROOT / "output/review/mcp-scanner"
# Reuse the maintained bounded HTTP, response-contract and child-process helpers.
sys.path.insert(0, str(ROOT / "test/functional"))
from core import mutate  # noqa: E402
from federation import Client  # noqa: E402
from http_client import fixture_tls_context  # noqa: E402
from journeys import header  # noqa: E402
from runner import command  # noqa: E402

spec = importlib.util.spec_from_file_location("mcp_scanner_runner", HERE / "runner.py")
scanner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(scanner)
relay_spec = importlib.util.spec_from_file_location("mcp_scanner_relay", HERE / "relay.py")
relay = importlib.util.module_from_spec(relay_spec)
relay_spec.loader.exec_module(relay)
TENANT = "00000000-0000-7000-8000-000000000001"
EVENT_SOURCE = "https://github.com/open-agentic-catalog/open-agentic-catalog/tenants/" + TENANT
CASES = ("network-isolation", "least-privilege-reader", "worker-delivery-and-restart", "duplicate-receipt",
         "out-of-order-releases", "finding-control", "unpublished-draft", "wrong-runtime-credential",
         "wrong-runtime-trust", "revoked-catalog-access", "scanner-process-failure", "missed-event-reconciliation",
         "unrecognized-endpoint", "immutable-catalog-evidence", "metadata-only")


def environment():
    keep = ("PATH", "HOME", "GOCACHE", "GOMODCACHE")
    return {k: os.environ[k] for k in keep if k in os.environ} | {
        "CATALOG_ENV_FILE": "/dev/null", "DOCKER_HOST": "unix:///var/run/docker.sock",
        "DOCKER_CLI_HINTS": "false", "COMPOSE_PROGRESS": "quiet", "GOTELEMETRY": "off", "PYTHONDONTWRITEBYTECODE": "1"}


def write(path, value, mode=0o600):
    raw = value if isinstance(value, bytes) else (json.dumps(value, sort_keys=True, indent=2) + "\n").encode()
    descriptor, temporary = tempfile.mkstemp(prefix=".mcp-scanner-", dir=path.parent)
    try:
        with os.fdopen(descriptor, "wb") as stream:
            os.fchmod(stream.fileno(), mode)
            stream.write(raw)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
    finally:
        Path(temporary).unlink(missing_ok=True)


def owner(directory):
    directory = Path(directory)
    if directory.is_symlink() or directory.parent.resolve() != OUTPUT.resolve() or not re.fullmatch(r"catalog-mcp-scan-[0-9a-f]{12}", directory.name):
        raise ValueError("select an owned MCP Scanner run directory")
    value = json.loads((directory / "report.json").read_bytes())
    if value["project"] != directory.name or value["directory"] != str(directory.resolve()):
        raise ValueError("MCP Scanner owner marker does not match")
    return value


def call(argv, *, seconds=90, env=None):
    return command(list(map(str, argv)), environment() if env is None else env, seconds, capture=True)


def compose(directory, *args, seconds=90):
    state = owner(directory)
    return call(["docker", "compose", "--env-file", directory / "compose.env", "--project-directory", ROOT,
                 "--project-name", state["project"], "--file", HERE / "compose.yaml", *args], seconds=seconds)


def sql(directory, statement, database="catalog"):
    return compose(directory, "exec", "-T", "postgres", "psql", "-X", "-q", "-U", "catalog", "-d", database,
                   "-v", "ON_ERROR_STOP=1", "-c", "SET statement_timeout='5s'", "-Atc", statement).decode().strip()


def wait(label, callback, seconds=60):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        value = callback()
        left = deadline - time.monotonic()
        if value and left > 0:
            return value
        if left <= 0:
            break
        time.sleep(min(0.1, left))
    raise ValueError("MCP Scanner wait failed: " + label)


def service_targets(directory):
    state = owner(directory)
    name = state["project"] + "_owned"
    network = json.loads(call(["docker", "network", "inspect", name]))[0]
    ipam = network["IPAM"]["Config"]
    if (not network["Internal"] or network["Driver"] != "bridge"
            or network["Labels"].get("com.docker.compose.project") != state["project"]
            or len(ipam) != 1 or ipam[0].get("Subnet") != state["subnet"] or ipam[0].get("Gateway") != state["gateway"]):
        raise ValueError("fixture internal network identity changed")
    values = {}
    for service, target in relay.PORTS.items():
        cid = compose(directory, "ps", "-q", service).decode().strip()
        if not re.fullmatch(r"[0-9a-f]{64}", cid):
            raise ValueError("fixture service identity is unavailable")
        container = json.loads(call(["docker", "inspect", cid]))[0]
        labels = container["Config"]["Labels"]
        networks = container["NetworkSettings"]["Networks"]
        if (container["Id"] != cid or not container["State"]["Running"]
                or labels.get("com.docker.compose.project") != state["project"] or labels.get("com.docker.compose.service") != service
                or set(networks) != {name} or networks[name]["NetworkID"] != network["Id"]
                or container["HostConfig"]["PortBindings"] or any(container["NetworkSettings"]["Ports"].values())):
            raise ValueError("fixture service network or publication changed")
        address = networks[name]["IPAddress"]
        if network["Containers"].get(cid, {}).get("IPv4Address") != address + "/28":
            raise ValueError("fixture service address is not retained by its network")
        values[service] = {"address": address, "port": target}
    relay.targets(state["subnet"], values)
    return values


def relay_args(directory):
    state = owner(directory)
    return [state.get("relayPython", sys.executable), "-B", str(HERE / "campaign.py"), "relay", "--directory", str(directory)]


def serve_relays(directory):
    state = owner(directory)
    selected = relay.targets(state["subnet"], state["relayTargets"])
    listeners = None
    try:
        with relay.defer_stop():
            listeners = relay.Relays(selected)
        write(directory / "private/relay-ready.json", {"schemaVersion": 1, "pid": os.getpid(), "listeners": listeners.addresses()})
        while True:
            signal.pause()
    finally:
        if listeners is not None:
            listeners.close()


def relay_addresses(directory):
    state = owner(directory)
    value = json.loads((directory / "private/relay-ready.json").read_bytes())
    if (set(value) != {"schemaVersion", "pid", "listeners"} or type(value.get("schemaVersion")) is not int
            or value["schemaVersion"] != 1 or type(value.get("pid")) is not int or value["pid"] != state["relayPID"]
            or process_identity(state["relayPID"]) != state["relayStart"]
            or not isinstance(value.get("listeners"), dict) or set(value["listeners"]) != set(relay.PORTS)):
        raise ValueError("fixture relay readiness identity changed")
    for address in value["listeners"].values():
        if not isinstance(address, str) or not re.fullmatch(r"127\.0\.0\.1:[0-9]{1,5}", address) or not 1 <= int(address.rsplit(":", 1)[1]) <= 65535:
            raise ValueError("fixture relay listener is not loopback-only")
    if len(set(value["listeners"].values())) != len(relay.PORTS):
        raise ValueError("fixture relay listeners overlap")
    return value["listeners"]


def start_relays(directory):
    state = owner(directory)
    if state.get("relayPID") and process_identity(state["relayPID"]) == state.get("relayStart"):
        raise ValueError("owned relay is already running")
    state.update(relayTargets=service_targets(directory), relayPython=sys.executable)
    write(directory / "report.json", state)
    (directory / "private/relay-ready.json").unlink(missing_ok=True)
    child = start_process(directory, "relay", relay_args(directory))
    def ready():
        if child.poll() is not None:
            raise ValueError("fixture relay startup failed")
        try:
            return relay_addresses(directory)
        except FileNotFoundError:
            return None
    return wait("loopback relay readiness", ready, 15)


def start_process(directory, role, arguments):
    import subprocess
    if role not in ("relay", "receiver"):
        raise ValueError("unexpected owned process role")
    if relay.threading.active_count() != 1:
        raise ValueError("start owned processes from the single-threaded coordinator")
    state = owner(directory)
    child = None
    try:
        with relay.defer_stop() as previous_mask:
            with (directory / "private" / (role + ".log")).open("ab") as stream:
                os.fchmod(stream.fileno(), 0o600)
                # This coordinator has one thread. Restore the child's original
                # mask before exec; only the parent defers stop delivery.
                child = subprocess.Popen(arguments, env=environment(), stdout=stream, stderr=stream, start_new_session=True,
                                         preexec_fn=lambda: signal.pthread_sigmask(signal.SIG_SETMASK, previous_mask))
            state.update({role + "PID": child.pid, role + "Start": process_identity(child.pid)})
            write(directory / "report.json", state)
        return child
    except BaseException:
        if child is not None:
            with relay.defer_stop():
                if child.poll() is None:
                    child.terminate()
                try:
                    child.wait(timeout=80 if role == "receiver" else 8)
                except subprocess.TimeoutExpired:
                    child.kill()
                    child.wait(timeout=8)
        raise


def source_digest():
    names = call(["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"]).decode().rstrip("\0").split("\0")
    return hashlib.sha256(json.dumps({name: hashlib.sha256((ROOT / name).read_bytes()).hexdigest()
        for name in sorted(set(names)) if (ROOT / name).is_file()}, sort_keys=True).encode()).hexdigest()


def go_executable():
    root = Path(call(["go", "env", "GOROOT"]).decode().strip())
    executable = root / "bin/go"
    if not executable.is_file():
        raise ValueError("the repository Go toolchain is unavailable")
    return executable


def prepare():
    scanner.require_host()
    with scanner.private_directory() as (_private, env):
        installed = scanner.require_installed(env)
    source = source_digest()
    commit = call(["git", "rev-parse", "HEAD"]).decode().strip()
    retained = call(["docker", "ps", "-aq"]).decode().split()
    OUTPUT.mkdir(mode=0o700, parents=True, exist_ok=True)
    directory = OUTPUT / ("catalog-mcp-scan-" + secrets.token_hex(6))
    directory.mkdir(mode=0o700)
    private = directory / "private"
    private.mkdir(mode=0o700)
    state = {"project": directory.name, "directory": str(directory.resolve()), "source": source,
             "commit": commit, "phase": "preparing", "image": directory.name + ":backend",
             "scanner": {k: installed[k] for k in ("version", "imageId", "rulesSha256")},
             "completed": [], "success": False, "cleanup": False,
             "retained": retained}
    write(directory / "report.json", state)
    try:
        for name in ("build", "backend", "mcp", "receiver", "reports"):
            (private / name).mkdir(mode=0o700)
        go = go_executable()
        goenv = environment() | {"GOWORK": "off", "GOTOOLCHAIN": "local", "CGO_ENABLED": "0", "GOPROXY": "off", "GOSUMDB": "off"}
        for path, package in (("trust-fixture", "./test/functional/fixture"), ("response-contract", "./test/functional/contracts")):
            call([go, "build", "-mod=readonly", "-trimpath", "-o", private / "build" / path, package], env=goenv, seconds=180)
        for name, package in (("receiver", "."), ("mcp-fixture", "./cmd/fixture")):
            call([go, "-C", ROOT / "examples/mcp-scanner/receiver", "build", "-mod=readonly", "-trimpath", "-o", private / "build" / name, package], env=goenv, seconds=180)
        index = secrets.randbelow(4096)
        subnet = ipaddress.ip_network(f"172.29.{index // 16}.{index % 16 * 16}/28")
        gateway = str(subnet.network_address + 1)
        trust = private / "trust-inputs"
        call([private / "build/trust-fixture", "generate", "--directory", trust, "--subnet", str(subnet)], seconds=20)
        for target in (private / "backend", private / "mcp", private / "receiver"):
            for name, source in (("server.pem", "server.pem"), ("server-key.pem", "server-key.pem"), ("ca.pem", "trust/functional-ca.pem")):
                write(target / name, (trust / source).read_bytes())
        call([private / "build/trust-fixture", "generate", "--directory", private / "untrusted", "--subnet", str(subnet)], seconds=20)
        mcp_key, admin_key, db_key = (secrets.token_hex(24) for _ in range(3))
        write(private / "mcp/mcp-token", mcp_key.encode())
        write(private / "mcp/admin-token", admin_key.encode())
        write(private / "receiver/mcp-token", mcp_key.encode())
        write(private / "receiver/catalog-token", b"local-template-service-client")
        write(private / "backend/encryption.key", base64.b64encode(os.urandom(32)))
        config = json.loads((ROOT / "config/postgres.json").read_bytes())
        config["http"]["publicBaseUrl"] = "http://127.0.0.1:8080"
        config["webhooks"].update(enabled=True, policyFile="/config/webhook-policy.json",
                                  pollInterval="100ms", timeout="3s", maxAttempts=3, retryCeiling="5s")
        config["worker"]["operationsAddress"] = "0.0.0.0:9090"
        config["federation"]["policyFile"] = "/config/federation-policy.json"
        write(private / "backend/config.json", config)
        write(private / "backend/federation-policy.json", {"schemaVersion": 1, "policyId": "mcp-scanner-owned", "revision": 1, "public": {"mode": "disabled"}, "privateProfiles": []})
        # The private policy is replaced with the exact receiver port after startup.
        write(private / "backend/webhook-policy.json", {"schemaVersion": 1, "policyId": "mcp-scanner-owned", "revision": 1,
              "public": {"mode": "disabled"}, "privateProfiles": []})
        for p in (private / "backend").iterdir():
            p.chmod(0o640)
        (private / "backend").chmod(0o750)
        image = directory.name + ":backend"
        call(["make", "container-build", "IMAGE=" + image, "CATALOG_ENV_FILE=/dev/null"], seconds=900)
        state["image"] = image
        state["imageId"] = json.loads(call(["docker", "image", "inspect", image]))[0]["Id"]
        state.update(subnet=str(subnet), gateway=gateway, phase="prepared")
        write(directory / "report.json", state)
        values = {"CATALOG_IMAGE": image, "PRIVATE": str(private), "REPOSITORY": str(ROOT), "RUN_UID": os.getuid(),
                  "RUN_GID": os.getgid(), "PG_PASSWORD": db_key, "GATEWAY": gateway, "SUBNET": str(subnet)}
        # Docker's env-file parser receives private generated values, never shell code.
        write(directory / "compose.env", "".join(k + "=" + str(v) + "\n" for k, v in values.items()).encode())
        compose(directory, "config", "--quiet", seconds=15)
        return directory
    except BaseException as error:
        try:
            state = owner(directory)
            state.update(success=False, phase="interrupted" if isinstance(error, KeyboardInterrupt) else "failed")
            write(directory / "report.json", state)
        finally:
            stop(directory)
        raise


def client(directory, token="local-admin"):
    state = owner(directory)
    os.environ["CATALOG_FUNCTIONAL_CONTRACT_DRIVER"] = str(directory / "private/build/response-contract")
    return Client(state["api"], token)


def receiver_args(directory, action, *, runner=None, poll="1s"):
    state = owner(directory)
    private = directory / "private"
    args = [private / "build/receiver", action, "--origin", state["api"], "--allow-loopback-http", "--tenant", TENANT,
            "--config-file", private / "receiver/mapping.json", "--database-file", private / "receiver/receiver.dsn",
            "--token-file", private / "receiver/catalog-token", "--reports-dir", private / "reports",
            "--scanner-python", Path(sys.executable).resolve(), "--scanner-runner", runner or HERE / "runner.py"]
    if action == "serve":
        args += ["--signer-file", private / "receiver/signer.json", "--tls-cert", private / "receiver/server.pem",
                 "--tls-key", private / "receiver/server-key.pem", "--listen", state["receiverAddress"],
                 "--poll-interval", poll, "--reconcile-interval", "24h"]
    return list(map(str, args))


def receiver_request(directory, method="GET", body=None, headers=None):
    state = owner(directory)
    context = fixture_tls_context(str(directory / "private/receiver/ca.pem"))
    # Verify the fixture certificate's exact DNS name without changing host DNS.
    with socket.create_connection((state["gateway"], state["receiverPort"]), timeout=3) as raw:
        with context.wrap_socket(raw, server_hostname="peer-a.test") as tls:
            payload = b"" if body is None else body
            fields = {"Host": "peer-a.test", "Connection": "close", "Content-Length": str(len(payload)), **(headers or {})}
            path = "/healthz" if method == "GET" else "/webhook"
            message = method + " " + path + " HTTP/1.1\r\n" + "".join(k + ": " + v + "\r\n" for k, v in fields.items()) + "\r\n"
            tls.sendall(message.encode("ascii") + payload)
            response = http.client.HTTPResponse(tls)
            response.begin()
            if len(response.read(4097)) > 4096:
                raise ValueError("receiver response exceeds its bound")
            return response.status


def process_identity(pid):
    try:
        raw = Path(f"/proc/{pid}/stat").read_text()
        return raw[raw.rfind(")") + 2:].split()[19]
    except (OSError, IndexError):
        return None


def start_receiver(directory, *, poll="1s", runner=None):
    state = owner(directory)
    if state.get("receiverPID") and process_identity(state["receiverPID"]) == state.get("receiverStart"):
        raise ValueError("owned receiver is already running")
    index = directory / "private/reports/index.json"
    previous = index.stat().st_mtime_ns if index.exists() else 0
    child = start_process(directory, "receiver", receiver_args(directory, "serve", runner=runner, poll=poll))
    def ready():
        if child.poll() is not None:
            raise ValueError("receiver startup failed")
        try:
            return receiver_request(directory) == 200 and index.is_file() and index.stat().st_mtime_ns > previous
        except OSError:
            return False
    wait("receiver startup", ready, 180)


def stop_process(directory, role, expected, seconds):
    state = owner(directory)
    pid = state.get(role + "PID")
    if pid and process_identity(pid) == state.get(role + "Start"):
        command_line = Path(f"/proc/{pid}/cmdline").read_bytes().split(b"\0")
        if command_line[:len(expected)] != [str(part).encode() for part in expected]:
            raise ValueError(role + " process ownership changed")
        os.kill(pid, signal.SIGTERM)
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline and process_identity(pid) == state[role + "Start"]:
            try:
                finished, _ = os.waitpid(pid, os.WNOHANG)
                if finished:
                    break
            except ChildProcessError:
                if Path(f"/proc/{pid}/stat").read_text().split(")", 1)[1].strip().startswith("Z"):
                    break
            time.sleep(0.1)
        else:
            if process_identity(pid) == state[role + "Start"]:
                os.killpg(pid, signal.SIGKILL)
                raise ValueError("owned " + role + " did not stop cleanly")
    state.pop(role + "PID", None)
    state.pop(role + "Start", None)
    write(directory / "report.json", state)


def stop_receiver(directory):
    stop_process(directory, "receiver", [directory / "private/build/receiver"], 80)


def stop_relays(directory):
    stop_process(directory, "relay", relay_args(directory), 8)


def start(directory):
    state = owner(directory)
    if state["phase"] != "prepared":
        raise ValueError("start only a prepared fixture")
    compose(directory, "up", "-d", "--no-build", "--pull", "never", "--wait", "--wait-timeout", "60", "postgres", "mcp")
    # Select the host receiver port only after its private bridge exists.
    with socket.socket() as listener:
        listener.bind((state["gateway"], 0))
        state["receiverPort"] = listener.getsockname()[1]
    state["receiverAddress"] = state["gateway"] + ":" + str(state["receiverPort"])
    state["receiverURL"] = "https://peer-a.test:" + str(state["receiverPort"]) + "/webhook"
    policy = {"schemaVersion": 1, "policyId": "mcp-scanner-owned", "revision": 1, "public": {"mode": "disabled"},
              "privateProfiles": [{"name": "mcp-scanner-owned", "origins": [state["receiverURL"].rsplit("/", 1)[0]],
                 "addressRanges": [state["gateway"] + "/32"], "assignedTenantIds": [TENANT], "transportRef": "direct", "trustBundleFile": "/config/ca.pem"}]}
    write(directory / "private/backend/webhook-policy.json", policy, 0o640)
    compose(directory, "run", "--rm", "--no-deps", "owner", "migrate", "apply", "--config", "/config/config.json", "--api-role", "catalog_api", "--worker-role", "catalog_worker")
    compose(directory, "run", "--rm", "--no-deps", "owner", "identity", "provision-development", "--config", "/config/config.json")
    compose(directory, "up", "-d", "--no-build", "--pull", "never", "--wait", "--wait-timeout", "60", "api", "worker")
    write(directory / "report.json", state)
    addresses = start_relays(directory)
    state = owner(directory)
    state["api"] = "http://" + addresses["api"]
    state["mcp"] = "https://" + addresses["mcp"]
    state["pg"] = addresses["postgres"]
    state["phase"] = "started"
    write(directory / "report.json", state)
    private = directory / "private"
    password = secrets.token_hex(24)
    sql(directory, "CREATE ROLE mcp_scan_receiver LOGIN PASSWORD '" + password + "' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS")
    sql(directory, "CREATE DATABASE mcp_scan_receiver OWNER mcp_scan_receiver")
    sql(directory, "GRANT CONNECT ON DATABASE catalog TO catalog_api,catalog_worker; REVOKE CONNECT ON DATABASE catalog FROM PUBLIC; REVOKE CONNECT ON DATABASE mcp_scan_receiver FROM PUBLIC")
    assert sql(directory, "SELECT has_database_privilege('mcp_scan_receiver','catalog','CONNECT')::int + has_database_privilege('catalog_api','mcp_scan_receiver','CONNECT')::int + has_database_privilege('catalog_worker','mcp_scan_receiver','CONNECT')::int") == "0"
    write(private / "receiver/receiver.dsn", ("postgres://mcp_scan_receiver:" + password + "@" + state["pg"] + "/mcp_scan_receiver?sslmode=disable").encode())
    api = client(directory)
    namespace, _ = mutate(api, "POST", "/api/v1/namespaces", {"slug": "mcp-scanner-" + state["project"][-12:], "displayName": "MCP Scanner fixture"}, 201)
    resource, _ = mutate(api, "POST", "/api/v1/resources", {"namespaceId": namespace["id"], "kind": "mcp_server", "canonicalIdentifier": "urn:air:catalog.example.com:server:mcp-scanner-" + state["project"][-12:]}, 201)
    publisher, headers = mutate(api, "POST", "/api/v1/publishers", {"displayName": "MCP Scanner fixture", "domains": ["catalog.example.com"]}, 201)
    mutate(api, "POST", "/api/v1/publishers/" + publisher["id"] + "/publication-domains/catalog.example.com:approve", {"reason": "Owned example publication"}, etag=header(headers, "ETag"))
    listing, _ = mutate(api, "POST", "/api/v1/catalog-listings", {"namespaceId": namespace["id"], "resourceId": resource["id"]}, 201)
    state.update(namespace=namespace["id"], resource=resource["id"], publisher=publisher["id"], listing=listing["id"])
    write(directory / "report.json", state)
    mapping = {"resources": [{"resourceId": state["resource"], "namespaceId": state["namespace"], "endpoint": state["mcp"] + "/mcp",
                              "tokenFile": str(private / "receiver/mcp-token"), "caFile": str(private / "receiver/ca.pem")}]}
    write(private / "receiver/mapping.json", mapping)
    call(receiver_args(directory, "init"))
    endpoint, _ = mutate(api, "POST", "/api/v1/webhooks", {"name": "MCP Scanner receiver", "url": state["receiverURL"], "eventTypes": ["listing.release.published"]}, 201)
    write(private / "receiver/signer.json", {"secret": endpoint["secret"], "version": endpoint["endpoint"]["secretVersion"]})
    state["endpoint"] = endpoint["endpoint"]["id"]
    write(directory / "report.json", state)
    print("Owned MCP Scanner fixture started: " + str(directory), flush=True)


def mark(directory, case):
    state = owner(directory)
    if case not in CASES or case in state["completed"]:
        raise ValueError("unexpected or repeated campaign case")
    state["completed"].append(case)
    write(directory / "report.json", state)
    print("MCP Scanner case passed: " + case, flush=True)


def receiver_sql(directory, statement):
    return sql(directory, statement, "mcp_scan_receiver")


def observation(directory):
    state = owner(directory)
    path = directory / "private/reports" / (state["resource"] + ".json")
    return json.loads(path.read_bytes()) if path.is_file() else None


def native_report(directory):
    saved = observation(directory)
    raw = (directory / "private/reports" / saved["scanFile"]).read_bytes()
    assert hashlib.sha256(raw).hexdigest() == saved["scanSha256"]
    return json.loads(raw)


def retain_report(directory, label):
    if label not in ("benign", "finding", "reconciled", "last"):
        raise ValueError("invalid report label")
    state = owner(directory)
    source = directory / "private/reports"
    metadata = source / (state["resource"] + ".json")
    if not metadata.is_file():
        return
    saved = observation(directory)
    target = directory / "evidence" / label
    target.mkdir(mode=0o700, parents=True, exist_ok=True)
    target.parent.chmod(0o700)
    write(target / metadata.name, metadata.read_bytes())
    if saved.get("scanFile"):
        native_report(directory)
        write(target / saved["scanFile"], (source / saved["scanFile"]).read_bytes())


def stats(directory):
    state = owner(directory)
    token = (directory / "private/mcp/admin-token").read_text()
    return Client(state["mcp"], token, fixture_tls_context(str(directory / "private/mcp/ca.pem")), catalog=False).request("GET", "/fixture/state")[0]


def mode(directory, value):
    state = owner(directory)
    token = (directory / "private/mcp/admin-token").read_text()
    Client(state["mcp"], token, fixture_tls_context(str(directory / "private/mcp/ca.pem")), catalog=False).request("POST", "/fixture/control", {"mode": value}, (204,))


def grant(directory, *, scoped=False):
    state = owner(directory)
    api, reader = client(directory), client(directory, "local-template-service-client")
    membership = reader.request("GET", "/api/v1/me")[0]
    permissions = ["resource.read"] if scoped else ["webhook.read", "webhook.read_evidence"]
    role, _ = mutate(api, "POST", "/api/v1/access/roles", {"name": "MCP Scanner " + secrets.token_hex(4), "permissions": permissions, "reason": "Read example evidence only"}, 201)
    body = {"roleDefinitionId": role["id"], "membershipId": membership["membershipId"], "reason": "Owned receiver qualification"}
    if scoped:
        body["namespaceId"] = state["namespace"]
    assigned, _ = mutate(api, "POST", "/api/v1/access/assignments", body, 201)
    return assigned


def publish(directory, number, *, endpoint=None, release=True):
    state = owner(directory)
    api = client(directory, "local-contributor")
    card = json.loads((ROOT / "samples/cards/order-mcp.json").read_bytes())
    card.update(name="org.example/mcp-scanner", version="1." + str(number) + ".0", remotes=[{"type": "streamable-http", "url": endpoint or state["mcp"] + "/mcp", "supportedProtocolVersions": ["2025-11-25"]}])
    version, _ = mutate(api, "POST", "/api/v1/resources/" + state["resource"] + "/versions", {"version": card["version"], "representations": [{"role": "primary", "mediaType": "application/mcp-server-card+json", "data": card}]}, 201)
    evidence, _ = mutate(api, "POST", "/api/v1/version-revisions/" + version["revision"]["id"] + ":validate", {}, 201)
    assert evidence["evidence"]["state"] == "complete"
    if not release:
        return version
    path = "/api/v1/catalog-listings/" + state["listing"]
    _, headers = api.request("GET", path)
    _, headers = mutate(api, "POST", path + "/revisions", {"versionRevisionId": version["revision"]["id"], "publisherId": state["publisher"],
        "displayName": "MCP definition example", "description": "An inert server for scanner qualification.", "visibility": "tenant", "license": "Apache-2.0"}, 201, header(headers, "ETag"))
    listing_etag = header(headers, "ETag")
    submission, headers = mutate(api, "POST", path + "/submissions", {}, 201)
    mutate(client(directory, "local-reviewer"), "POST", "/api/v1/review-submissions/" + submission["id"] + ":approve", {"reason": "Independent example review"}, etag=header(headers, "ETag"))
    result, _ = mutate(client(directory, "local-publisher"), "POST", path + ":publish", {"submissionId": submission["id"]}, 201, listing_etag)
    return result["release"]


def event(directory, release_id):
    raw = sql(directory, "SELECT json_build_object('id',id,'type',event_type,'subject',subject,'time',created_at,'data',data) FROM outbox_events WHERE tenant_id='" + TENANT + "' AND subject='" + release_id + "' AND data->>'action'='listing.release.published' ORDER BY created_at DESC LIMIT 1")
    value = json.loads(raw)
    value.update(specversion="1.0", source=EVENT_SOURCE, datacontenttype="application/json", dataschema="https://github.com/open-agentic-catalog/open-agentic-catalog/schemas/event/v1")
    return value


def completed(directory, event_id):
    return receiver_sql(directory, "SELECT EXISTS(SELECT 1 FROM mcp_receiver_inbox WHERE source='" + EVENT_SOURCE + "' AND id='" + event_id + "' AND completed_at IS NOT NULL)") == "t"


def refresh(directory):
    call(receiver_args(directory, "reconcile"))
    call(receiver_args(directory, "process"), seconds=240)
    return observation(directory)


def qualify(directory):
    state = owner(directory)
    if state["phase"] != "started" or state["source"] != source_digest():
        raise ValueError("fixture source or phase changed")
    for name in ("api", "worker", "postgres", "mcp"):
        cid = compose(directory, "ps", "-q", name).decode().strip()
        value = json.loads(call(["docker", "inspect", cid]))[0]
        assert set(value["NetworkSettings"]["Networks"]) == {state["project"] + "_owned"}
        assert not value["HostConfig"]["Privileged"] and value["HostConfig"]["NetworkMode"] != "host"
        assert not value["HostConfig"]["PortBindings"] and not any(value["NetworkSettings"]["Ports"].values())
        if name != "postgres":
            assert value["Image"] == state["imageId"]
    network = json.loads(call(["docker", "network", "inspect", state["project"] + "_owned"]))[0]
    assert network["Internal"] and len(network["Containers"]) == 4
    assert service_targets(directory) == state["relayTargets"]
    addresses = relay_addresses(directory)
    assert (state["api"], state["mcp"], state["pg"]) == ("http://" + addresses["api"], "https://" + addresses["mcp"], addresses["postgres"])
    mark(directory, "network-isolation")
    reader, api = client(directory, "local-template-service-client"), client(directory)
    before = reader.request("GET", "/api/v1/me")[0]
    assert before["permissions"] == [] and before["availablePermissions"] == []
    grant(directory)
    scoped = grant(directory, scoped=True)
    selected = reader.request("GET", "/api/v1/me?namespaceId=" + state["namespace"])[0]
    assert set(selected["availablePermissions"]) == {"resource.read", "webhook.read", "webhook.read_evidence"}
    reader.request("POST", "/api/v1/webhooks", {"name": "Denied", "url": state["receiverURL"]}, (403,), {"Idempotency-Key": "scanner-denied"})
    mark(directory, "least-privilege-reader")
    # Startup sees no publication. A slow polling interval leaves the later
    # worker-accepted receipt pending so restart is tested before collection.
    start_receiver(directory, poll="1m")
    first = publish(directory, 0)
    first_event = event(directory, first["id"])
    def accepted():
        return receiver_sql(directory, "SELECT EXISTS(SELECT 1 FROM mcp_receiver_inbox WHERE source='" + EVENT_SOURCE + "' AND id='" + first_event["id"] + "' AND completed_at IS NULL)") == "t"
    wait("durable worker receipt", accepted, 30)
    assert stats(directory)["methods"] == {}
    stop_receiver(directory)
    assert accepted()
    # A fresh processor consumes the retained worker receipt. Serve startup
    # also queues reconciliation, which can replace the latest observation.
    call(receiver_args(directory, "process"), seconds=240)
    assert completed(directory, first_event["id"])
    saved = observation(directory)
    raw = native_report(directory)
    assert saved["state"] == "eligible_at_check" and saved["catalogEvidence"]["releaseId"] == first["id"] and saved["trigger"]["id"] == first_event["id"]
    assert raw["status"] == "completed" and raw["plannedTools"] == raw["completedTools"] == 1 and raw["cleanup"] == "passed" and raw["results"][0]["findings"] == []
    retain_report(directory, "benign")
    mark(directory, "worker-delivery-and-restart")
    start_receiver(directory)
    baseline = stats(directory)
    signer = json.loads((directory / "private/receiver/signer.json").read_bytes())
    body = json.dumps(first_event, separators=(",", ":")).encode()
    timestamp = str(int(time.time()))
    signature = hmac.new(signer["secret"].encode(), timestamp.encode() + b"." + body, hashlib.sha256).hexdigest()
    assert receiver_request(directory, "POST", body, {"Content-Type": "application/cloudevents+json", "X-Catalog-Timestamp": timestamp,
        "X-Catalog-Signature": "v1,kid=" + str(signer["version"]) + ",sha256=" + signature}) == 204
    stop_receiver(directory)
    assert stats(directory) == baseline
    assert receiver_sql(directory, "SELECT count(*) FROM mcp_receiver_inbox WHERE source='" + EVENT_SOURCE + "' AND id='" + first_event["id"] + "'") == "1"
    mark(directory, "duplicate-receipt")
    compose(directory, "stop", "--timeout", "20", "worker")
    second = publish(directory, 1)
    mode(directory, "finding")
    third = publish(directory, 2)
    second_event, third_event = event(directory, second["id"]), event(directory, third["id"])
    start_receiver(directory)
    compose(directory, "up", "-d", "--no-deps", "--wait", "worker")
    wait("new release analysis", lambda: completed(directory, second_event["id"]) and completed(directory, third_event["id"]), 180)
    stop_receiver(directory)
    assert receiver_sql(directory, "SELECT outcome FROM mcp_receiver_inbox WHERE source='" + EVENT_SOURCE + "' AND id='" + second_event["id"] + "'") == "catalog_record_unavailable"
    assert stats(directory)["methods"].get("tools/list") == baseline["methods"].get("tools/list", 0) + 2  # startup reconciliation plus one notification can reuse analysis
    mark(directory, "out-of-order-releases")
    saved, raw = observation(directory), native_report(directory)
    assert saved["catalogEvidence"]["releaseId"] == third["id"] and raw["status"] == "completed" and raw["completedTools"] == 1
    assert {f["details"]["content_type"] for f in raw["results"][0]["findings"]} == {"description"}
    retain_report(directory, "finding")
    mark(directory, "finding-control")
    immutable, _ = reader.request("GET", "/api/v1/catalog/resources/" + third["id"])
    publish(directory, 3, release=False)
    saved = refresh(directory)
    assert saved["catalogEvidence"]["releaseId"] == third["id"] and saved["analysisReused"]
    mark(directory, "unpublished-draft")
    token_path = directory / "private/receiver/mcp-token"
    original_token = token_path.read_bytes()
    counts = stats(directory)
    write(token_path, b"incorrect-runtime-credential")
    try:
        failed = refresh(directory)
        assert failed["state"] == "failed" and failed["failure"] == "mcp_http_failed"
        assert stats(directory)["denied"] == counts["denied"] + 1 and stats(directory)["methods"] == counts["methods"]
    finally:
        write(token_path, original_token)
    mark(directory, "wrong-runtime-credential")
    ca_path = directory / "private/receiver/ca.pem"
    original_ca = ca_path.read_bytes()
    write(ca_path, (directory / "private/untrusted/trust/functional-ca.pem").read_bytes())
    counts = stats(directory)
    try:
        failed = refresh(directory)
        assert failed["state"] == "failed" and failed["failure"] == "mcp_tls_failed" and stats(directory) == counts
    finally:
        write(ca_path, original_ca)
    mark(directory, "wrong-runtime-trust")
    call(receiver_args(directory, "reconcile"))
    mutate(api, "POST", "/api/v1/access/assignments/" + scoped["id"] + ":revoke", {"reason": "Qualify revoked receiver read"}, etag='"rev-' + str(scoped["revision"]) + '"')
    counts = stats(directory)
    reader.request("GET", "/api/v1/catalog/resources/" + third["id"], expected=(403,))
    # Use a pending reference already captured by reconciliation, then revoke it.
    # No control API or database substitution creates a publication.
    call(receiver_args(directory, "process"))
    assert stats(directory) == counts
    grant(directory, scoped=True)
    mark(directory, "revoked-catalog-access")
    fault = directory / "private/fault-runner.py"
    write(fault, ("import runpy,sys\nif sys.argv[1] == 'scan': raise SystemExit(2)\nrunpy.run_path(" + repr(str(HERE / "runner.py")) + ",run_name='__main__')\n").encode())
    call(receiver_args(directory, "reconcile"))
    # A different definition prevents the successful cache from hiding the fault.
    mode(directory, "benign")
    call(receiver_args(directory, "process", runner=fault), seconds=240)
    failed = observation(directory)
    assert failed["state"] == "failed" and failed["failure"] == "scanner_report_missing"
    mark(directory, "scanner-process-failure")
    # The receiver first recovers the failed scan without another notification.
    recovered = refresh(directory)
    assert recovered["state"] == "eligible_at_check" and recovered["catalogEvidence"]["releaseId"] == third["id"]
    endpoint_path = "/api/v1/webhooks/" + state["endpoint"]
    _, headers = api.request("GET", endpoint_path)
    mutate(api, "DELETE", endpoint_path, None, 204, header(headers, "ETag"))
    missed = publish(directory, 4)
    missed_event = event(directory, missed["id"])
    wait("disabled endpoint event drained", lambda: sql(directory, "SELECT published_at IS NOT NULL FROM outbox_events WHERE id='" + missed_event["id"] + "'") == "t")
    assert receiver_sql(directory, "SELECT count(*) FROM mcp_receiver_inbox WHERE id='" + missed_event["id"] + "'") == "0"
    recovered = refresh(directory)
    assert recovered["state"] == "eligible_at_check" and recovered["catalogEvidence"]["releaseId"] == missed["id"] and recovered["trigger"]["source"] == "urn:catalog-mcp-scanner:reconciliation"
    retain_report(directory, "reconciled")
    endpoint, _ = mutate(api, "POST", "/api/v1/webhooks", {"name": "Restored MCP Scanner receiver", "url": state["receiverURL"], "eventTypes": ["listing.release.published"]}, 201)
    write(directory / "private/receiver/signer.json", {"secret": endpoint["secret"], "version": endpoint["endpoint"]["secretVersion"]})
    updated = owner(directory)
    updated["endpoint"] = endpoint["endpoint"]["id"]
    write(directory / "report.json", updated)
    mark(directory, "missed-event-reconciliation")
    counts = stats(directory)
    fourth = publish(directory, 5, endpoint=state["mcp"] + "/unmapped")
    fourth_event = event(directory, fourth["id"])
    start_receiver(directory)
    wait("unrecognized endpoint refusal", lambda: completed(directory, fourth_event["id"]), 45)
    stop_receiver(directory)
    assert stats(directory) == counts
    assert receiver_sql(directory, "SELECT outcome FROM mcp_receiver_inbox WHERE source='" + EVENT_SOURCE + "' AND id='" + fourth_event["id"] + "'") == "endpoint_mapping_mismatch"
    mark(directory, "unrecognized-endpoint")
    after, _ = reader.request("GET", "/api/v1/catalog/resources/" + fourth["id"])
    old_revision, _ = api.request("GET", "/api/v1/version-revisions/" + immutable["versionRevision"]["id"])
    assert old_revision["revision"] == immutable["versionRevision"] and after["release"]["state"] == "published"
    mark(directory, "immutable-catalog-evidence")
    final = stats(directory)
    assert final["invoked"] == 0 and set(final["methods"]) <= {"initialize", "notifications/initialized", "tools/list", "DELETE"}
    mark(directory, "metadata-only")
    state = owner(directory)
    state.update(phase="passed", success=True, observations={"runtime": final, "resource": state["resource"], "firstRelease": first["id"], "findingRelease": third["id"]})
    if state["completed"] != list(CASES) or state["source"] != source_digest():
        raise ValueError("incomplete or changed-source campaign")
    write(directory / "report.json", state)


def stop(directory):
    state = owner(directory)
    if state.get("cleanup") and not (directory / "private").exists() and not (directory / "compose.env").exists():
        return True
    errors = []
    try:
        stop_receiver(directory)
    except (OSError, ValueError):
        errors.append("receiver")
    # Stop the writer before checking the metadata/native-report pair.
    if "receiver" not in errors and state.get("resource") and (directory / "private/reports").is_dir():
        try:
            retain_report(directory, "last")
        except (OSError, ValueError, KeyError, AssertionError):
            errors.append("report-evidence")
    try:
        stop_relays(directory)
    except (OSError, ValueError):
        errors.append("relay")
    if (directory / "compose.env").is_file():
        try:
            compose(directory, "--profile", "operator", "down", "--volumes", "--remove-orphans", "--timeout", "20")
            for kind in ("container", "volume", "network"):
                args = ["docker", kind, "ls", "--quiet", "--filter", "label=com.docker.compose.project=" + state["project"]]
                if kind == "container":
                    args.append("--all")
                if call(args).strip():
                    errors.append(kind)
        except Exception:
            errors.append("compose")
    state = owner(directory)
    if state.get("image"):
        try:
            if state["image"] != state["project"] + ":backend":
                raise ValueError("owned image reference changed")
            found = call(["docker", "image", "ls", "--quiet", "--no-trunc", state["image"]])
            if found.strip():
                call(["docker", "image", "rm", state["image"]])
        except Exception:
            errors.append("image")
    try:
        present = set(call(["docker", "ps", "-aq"]).decode().split())
        if not set(state["retained"]) <= present:
            errors.append("retained-containers")
    except Exception:
        errors.append("retained-containers-inspection")
    if not errors:
        try:
            if (directory / "private").exists():
                shutil.rmtree(directory / "private", ignore_errors=False)
            (directory / "compose.env").unlink(missing_ok=True)
        except OSError:
            errors.append("private-inputs")
    state["cleanup"] = not errors
    if errors:
        state.update(success=False, cleanupFailures=errors)
    else:
        state.pop("cleanupFailures", None)
    write(directory / "report.json", state)
    return not errors


def interrupted(_number, _frame):
    raise KeyboardInterrupt


def main():
    signal.signal(signal.SIGTERM, interrupted)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("prepare", "start", "test", "stop", "run", "relay"))
    parser.add_argument("--directory", type=Path)
    args = parser.parse_args()
    directory = args.directory
    if args.action == "relay":
        try:
            if directory is None:
                raise ValueError("select the owned run directory")
            serve_relays(directory)
        except KeyboardInterrupt:
            return 0
        except Exception:
            print("MCP Scanner fixture relay failed.", file=sys.stderr)
            return 1
    stage = "prepare" if args.action in ("prepare", "run") else args.action
    try:
        if args.action in ("prepare", "run"):
            directory = prepare()
            print("MCP Scanner run directory: " + str(directory), flush=True)
        if args.action == "prepare":
            return 0
        if directory is None:
            raise ValueError("select the owned run directory")
        if args.action in ("start", "run"):
            stage = "start"
            start(directory)
        if args.action in ("test", "run"):
            stage = "test"
            try:
                qualify(directory)
            finally:
                if not stop(directory):
                    raise ValueError("MCP Scanner cleanup failed")
        elif args.action == "stop" and not stop(directory):
            return 1
        return 0
    except scanner.SetupError as error:
        print(str(error), file=sys.stderr)
        return 2
    except KeyboardInterrupt:
        if directory is not None:
            state = owner(directory)
            state.update(success=False, phase="interrupted")
            write(directory / "report.json", state)
            stop(directory)
        print("MCP Scanner campaign interrupted.", file=sys.stderr)
        return 1
    except Exception as error:
        if directory is not None and directory.is_dir():
            state = owner(directory)
            state.update(success=False, phase="failed", failureStage=stage, failureType=type(error).__name__)
            locations = []
            trace = error.__traceback__
            while trace is not None:
                if Path(trace.tb_frame.f_code.co_filename) == HERE / "campaign.py":
                    locations.append({"function": trace.tb_frame.f_code.co_name, "line": trace.tb_lineno})
                trace = trace.tb_next
            state["failureLocation"] = locations
            if stage == "test" and len(state["completed"]) < len(CASES):
                state["failedCase"] = CASES[len(state["completed"])]
            write(directory / "report.json", state)
            if not state.get("cleanup"):
                stop(directory)
        print("MCP Scanner campaign failed; inspect its private local report.", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
