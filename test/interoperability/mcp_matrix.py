"""Owned PostgreSQL replicas and the independent four-way MCP client matrix."""

import json
import os
from pathlib import Path
import shutil
import signal
import tempfile
import time
import traceback
import uuid

from common import Result, run
from runner import CATALOG_INPUTS, ROOT, docker, fingerprint, local_daemon
from catalog_control import CatalogControl
from mcp_diagnostics import read_failure
import mcp_catalog_tools
import mcp_tools as tools
from mcp_stdio import setup

COMBINATIONS = {(transport, version) for transport in ("stdio", "http") for version in ("2025-11-25", "2026-07-28")}
CASES = {"inventory", "local-pages", "filters", "indexed", "exact-release", "explore-access", "empty-sparse",
         "closed-arguments", "live", "partial", "replica-continuation", "restart-continuation", "stale-cursor"}


def validate_result(result, secrets):
    assert result.code == 0 and not result.stderr.strip(), "independent matrix client failed"
    assert not any(secret in result.stdout + result.stderr for secret in secrets), "matrix credential exposure"
    data = json.loads(result.stdout)
    assert set(data) == {"client", "matrix", "noSessions", "peerSafe", "childrenExited"}
    assert data["client"] == "mcp@2.3.0" and data["noSessions"] is True and data["peerSafe"] is True and data["childrenExited"] is True
    rows = data["matrix"]
    assert len(rows) == 4 and {(r["transport"], r["protocol"]) for r in rows} == COMBINATIONS
    for row in rows:
        assert set(row) == {"transport", "protocol", "cases", "replicas"}
        assert set(row["cases"]) == CASES and all(value is True for value in row["cases"].values())
        assert set(row["replicas"]) == {8080, 8081}
    return data


def compose_file(path, network):
    """Reuse the shipped PostgreSQL definition; publish no host port."""
    path.write_text('services:\n  postgres:\n    extends:\n      file: ' + json.dumps(str(ROOT / 'compose.yaml')) + '\n      service: postgres\n    ports: !reset []\n    networks: [owned]\nnetworks:\n  owned:\n    external: true\n    name: ' + network + '\nvolumes:\n  catalog-postgres-18: {}\n')


def wait_ready(container, port):
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        result = docker("exec", container, "/commands/catalogctl", "probe", "--url", f"http://127.0.0.1:{port}/readyz", timeout=5, check=False)
        if result.code == 0:
            return
        assert json.loads(docker("inspect", container).stdout)[0]["State"]["Running"], "API exited before readiness"
        time.sleep(.1)
    raise RuntimeError("matrix API readiness deadline")


def run_client(name, primary):
    """Service only fixed restart checkpoints, never client-supplied commands."""
    docker("start", name, timeout=10)
    deadline, acknowledged = time.monotonic() + 180, set()
    restarts = []
    while time.monotonic() < deadline:
        state = json.loads(docker("inspect", name, timeout=5).stdout)[0]["State"]
        if not state["Running"]:
            logs = docker("logs", name, timeout=10, limit=2*1024*1024)
            return Result(state["ExitCode"], logs.stdout, logs.stderr), restarts
        stage_result = docker("exec", name, "python", "-B", "-c", "from pathlib import Path; p=Path('/tmp/restart'); print(p.read_text() if p.exists() else '')", timeout=5, check=False)
        if stage_result.code != 0:
            continue
        stage = stage_result.stdout.strip()
        if stage and stage not in acknowledged:
            assert stage in {"stdio-2025-11-25", "stdio-2026-07-28", "http-2025-11-25", "http-2026-07-28"}, "invalid restart checkpoint"
            before = json.loads(docker("inspect", primary).stdout)[0]
            docker("stop", "--time", "25", primary, timeout=30)
            stopped = json.loads(docker("inspect", primary).stdout)[0]["State"]
            assert not stopped["Running"] and stopped["ExitCode"] == 0, "API did not drain successfully"
            docker("start", primary, timeout=10)
            wait_ready(primary, 8080)
            after = json.loads(docker("inspect", primary).stdout)[0]
            assert before["Id"] == after["Id"] and before["State"]["StartedAt"] != after["State"]["StartedAt"], "API did not restart"
            docker("exec", name, "python", "-B", "-c", "from pathlib import Path; Path('/tmp/continued').write_text('" + stage + "')", timeout=5)
            acknowledged.add(stage)
            restarts.append({"case": stage, "container": before["Id"], "before": before["State"]["StartedAt"], "after": after["State"]["StartedAt"]})
        time.sleep(.1)
    raise RuntimeError("independent matrix deadline")


def check():
    from mcp_runner import build, isolation
    from peer_runner import addresses
    arch = local_daemon()
    stamp = mcp_catalog_tools.installed()
    inputs = [*CATALOG_INPUTS, ROOT / "Makefile", tools.HERE, ROOT / "compose.yaml", ROOT / "deployments/postgres/init.sql"]
    source = fingerprint(inputs)
    tools.OUTPUT.mkdir(parents=True, exist_ok=True)
    directory = Path(tempfile.mkdtemp(prefix="matrix-", dir=tools.OUTPUT))
    private = directory / "private"
    private.mkdir(mode=0o700)
    names, secrets = [], []
    prefix = "catalog-mcp-matrix-" + uuid.uuid4().hex[:12]
    network, compose = prefix + "-network", None
    report = dict(scope="catalog-control-mcp-matrix", source=source, installation=stamp, containers=names,
                  network={"name": network}, composeProject=prefix, phase="setup", cleanup=False, success=False)
    print("Catalog MCP matrix report: " + str(directory.relative_to(ROOT)), flush=True)
    previous = signal.signal(signal.SIGALRM, lambda *_: (_ for _ in ()).throw(TimeoutError("MCP matrix deadline")))
    signal.alarm(900)
    try:
        commands, peer, client = (private / name for name in ("commands", "peer", "client"))
        for path in (commands, peer, client):
            path.mkdir(mode=0o700)
        report["binaries"] = build(commands, arch)
        for file in ("catalog_client.py", "catalog_matrix.py"):
            shutil.copyfile(tools.INPUTS / file, client / file)
        shutil.copyfile(tools.HERE / "mcp_diagnostics.py", client / "mcp_diagnostics.py")
        image = stamp["image"]
        # Register resources before creation and delay signals until each bounded
        # creation returns. Cleanup is required even for interruption/failure.
        mask = signal.pthread_sigmask(signal.SIG_BLOCK, {signal.SIGINT, signal.SIGTERM, signal.SIGALRM})
        try:
            docker("network", "create", "--internal", network, timeout=30)
        except (OSError, RuntimeError):
            report["creation_unconfirmed"] = True
            raise
        finally:
            signal.pthread_sigmask(signal.SIG_SETMASK, mask)
        network_info = json.loads(docker("network", "inspect", network, timeout=10).stdout)[0]
        assert network_info["Internal"], "matrix network allows external egress"
        report["network"]["internal"] = True
        ip = addresses(network)[0]
        origin = "https://" + ip + ":9443"
        run([str(commands / "fixture"), "prepare-stdio", str(peer), ip], timeout=15)
        server = prefix + "-peer"
        names.append(server)
        tools.create(server, "--network=" + network, "--ip=" + ip, "--mount", f"type=bind,src={peer},dst=/private,readonly", "--mount", f"type=bind,src={commands},dst=/commands,readonly", "--entrypoint=/commands/fixture", image, "serve-matrix")
        docker("start", server, timeout=10)
        model = private / "compose.yaml"
        compose_file(model, network)
        compose = ("compose", "--project-name", prefix, "-f", str(model))
        docker(*compose, "up", "-d", "--pull", "never", "--no-build", "--wait", "--wait-timeout", "60", "postgres", timeout=90)
        postgres = docker(*compose, "ps", "-q", "postgres").stdout.strip()
        db = json.loads(docker("inspect", postgres).stdout)[0]
        assert not db["HostConfig"]["PortBindings"], "PostgreSQL published a host port"
        report["postgres"] = {"container": postgres, "image": db["Image"]}
        configs = []
        controls = []
        primary = prefix + "-api-0"
        base = private / "catalog"
        base.mkdir(mode=0o700)
        catalog = CatalogControl(base, primary)
        catalog.configuration()
        secrets.extend(catalog.tokens.values())
        settings = json.loads((base / "config.json").read_text())
        settings["auth"]["developmentTokens"].update(json.loads((ROOT / "config/local.json").read_text())["auth"]["developmentTokens"])
        settings.update(profile="postgres", database=json.loads((ROOT / "config/postgres.json").read_text())["database"])
        settings["http"]["publicBaseUrl"] = "https://127.0.0.1:9555"
        settings["mcp"]["enabled"] = True
        settings["discovery"]["hosts"].append(dict(settings["discovery"]["hosts"][0], host="localhost", mode="authenticated"))
        settings["demoArchives"] = dict(origin="https://127.0.0.1:9443", trustBundleFile="/private/ca.pem", allowlistFile="/private/allowlist.json")
        settings["federation"]["policyFile"] = "/catalog/policy.json"
        catalog.write("policy.json", json.dumps(dict(schemaVersion=1, policyId="stdio-owned", revision=1, public={"mode": "disabled"}, privateProfiles=[dict(name="stdio-owned", origins=[origin], addressRanges=[ip + "/32"], assignedTenantIds=["00000000-0000-7000-8000-000000000001"], transportRef="direct", trustBundleFile="/private/ca.pem")])))
        for role in ("local-admin", "invalid", "other-tenant-reader"):
            catalog.write(role + "-token", catalog.tokens[role])
        catalog.write("config.json", json.dumps(settings))
        # Shared explicit cursor key is scoped only to this disposable invocation.
        import base64
        catalog.write("cursor-key", base64.b64encode(os.urandom(32)).decode())
        def create(name, directory, *args):
            names.append(name)
            return tools.create(name, "--network=container:" + server,
                                "--env", "CATALOG_CURSOR_SIGNING_KEY_FILE=/catalog/cursor-key",
                                "--mount", f"type=bind,src={commands},dst=/commands,readonly",
                                "--mount", f"type=bind,src={peer},dst=/private,readonly",
                                "--mount", f"type=bind,src={directory},dst=/catalog,readonly", *args)
        owner = "postgres://catalog:catalog@postgres:5432/catalog?sslmode=disable"
        for action, arguments in (("migrate", ("migrate", "apply", "--config", "/catalog/config.json", "--api-role", "catalog_api", "--worker-role", "catalog_worker")), ("provision", ("identity", "provision-development", "--config", "/catalog/config.json"))):
            name = prefix + "-" + action
            create(name, base, "--env", "CATALOG_DATABASE_DSN=" + owner, "--entrypoint=/commands/catalogctl", image, *arguments)
            docker("start", "-a", name, timeout=90, limit=2*1024*1024)
            assert json.loads(docker("inspect", name).stdout)[0]["State"]["ExitCode"] == 0, "database preparation failed"
        for index in range(2):
            path = base if index == 0 else private / "replica"
            if index:
                shutil.copytree(base, path)
            config = dict(settings, http=dict(settings["http"], address=f"0.0.0.0:{8080 + index}"))
            (path / "config.json").write_text(json.dumps(config))
            name = prefix + "-api-" + str(index)
            create(name, path, "--entrypoint=/commands/catalogd", image, "api", "--config", "/catalog/config.json")
            docker("start", name, timeout=10)
            wait_ready(name, 8080 + index)
            configs.append(path)
            controls.append(name)
        worker = prefix + "-worker"
        create(worker, base, "--env", "CATALOG_DATABASE_DSN=postgres://catalog_worker:catalog-worker@postgres:5432/catalog?sslmode=disable", "--entrypoint=/commands/catalogd", image, "worker", "--config", "/catalog/config.json")
        docker("start", worker, timeout=10)
        report["phase"] = "governance-and-indexing"
        report["fixtures"] = setup(catalog, origin)
        me, _ = catalog.request("/api/v1/me", actor="local-reader")
        roles, _ = catalog.request("/api/v1/access/roles?limit=100")
        reader_role = next(r["id"] for r in roles["items"] if r.get("key") == "reader")
        local, _ = catalog.request("/api/v1/catalog/search", {"origins": ["local"], "limit": 100}, actor="local-reader")
        namespaces = sorted({r["local"]["resource"]["namespaceId"] for r in local["items"]})
        assert len(namespaces) >= 2, "self-access paging needs two real namespaces"
        for namespace in namespaces[:2]:
            catalog.request("/api/v1/access/assignments", dict(membershipId=me["membershipId"], roleDefinitionId=reader_role, namespaceId=namespace, reason="Qualify bounded MCP self-access"), status=201)
        live = []
        for path in ("live", "partial"):
            inspection, _ = catalog.request("/api/v1/federation/source-inspections", {"url": origin + "/" + path + "/search", "networkProfile": "stdio-owned"}, status=201)
            connected, _ = catalog.request("/api/v1/federation/sources", dict(inspectionId=inspection["id"], displayName="Owned " + path, retrievalMode="live", updateMode="manual_review", networkProfile="stdio-owned", scope={"publishers": ["catalog.example.com"]}, live=dict(enabled=True, queryPrivacyAcknowledged=True, federationMode="none"), discoveryAssignments=[dict(host="localhost", audience="authenticated")], reason="Independent MCP live qualification"), status=201)
            live.append(connected["id"])
        (peer / "outage").touch(mode=0o600)
        expected = json.loads((base / "expected.json").read_text())
        catalog.write("expected.json", json.dumps(dict(expected, liveSources=live)))
        # No test-only replica has a divergent token, policy, or cursor key.
        shutil.copyfile(base / "expected.json", configs[1] / "expected.json")
        name = prefix + "-client"
        create(name, base, "--mount", f"type=bind,src={client},dst=/client,readonly", image, "/client/catalog_matrix.py")
        server_id = json.loads(docker("inspect", server).stdout)[0]["Id"]
        report["isolation"] = {"client": isolation(name, "container:" + server_id, private, (("commands", "/commands"), ("peer", "/private"), ("catalog", "/catalog"), ("client", "/client")))}
        report["phase"] = "independent-matrix"
        (directory / "report.json").write_text(json.dumps(report, indent=2) + "\n")
        result, report["restarts"] = run_client(name, primary)
        if result.code != 0:
            report["clientFailure"] = read_failure(result.stdout)
        report["client"] = validate_result(result, secrets)
        assert len(report["restarts"]) == 4, "matrix restart checkpoints incomplete"
        for name in (*controls, worker, server):
            docker("stop", "--time", "25", name, timeout=30)
            state = json.loads(docker("inspect", name).stdout)[0]["State"]
            assert not state["Running"] and state["ExitCode"] == 0, "matrix shutdown failed"
            logs = docker("logs", name, timeout=10, limit=2*1024*1024)
            assert not any(secret in logs.stdout + logs.stderr for secret in secrets), "service credential exposure"
        assert source == fingerprint(inputs), "MCP matrix source changed during execution"
        report["phase"] = "validated"
    except (AssertionError, RuntimeError, OSError, ValueError, KeyError, TypeError, KeyboardInterrupt) as error:
        if isinstance(error, tools.CreationUnconfirmed):
            report["creation_unconfirmed"] = True
        report["error"] = traceback.format_exc(limit=4)[-6000:]
        report["failure_logs"] = {}
        for name in names:
            try:
                logs = docker("logs", name, timeout=5, limit=256*1024, check=False)
                report["failure_logs"][name] = (logs.stdout + logs.stderr)[-4000:]
            except (RuntimeError, OSError):
                report["failure_logs"][name] = "unavailable"
        for secret in secrets:
            report = json.loads(json.dumps(report).replace(secret, "[fixture-credential]"))
        raise
    finally:
        signal.alarm(0)
        signal.signal(signal.SIGALRM, previous)
        removed = database_removed = network_removed = False
        try:
            try:
                tools.remove(names)
                removed = True
            finally:
                try:
                    if compose:
                        docker(*compose, "down", "--volumes", "--remove-orphans", "--timeout", "20", timeout=60)
                    database_removed = True
                finally:
                    result = docker("network", "rm", network, timeout=15, check=False)
                    network_removed = result.code == 0 or "not found" in result.stderr
                    assert network_removed, "owned matrix network cleanup failed"
        finally:
            shutil.rmtree(private)
            report["cleanup"] = removed and database_removed and network_removed and not private.exists() and not report.get("creation_unconfirmed", False)
            (directory / "report.json").write_text(json.dumps(report, indent=2) + "\n")
    assert report["cleanup"] and report["phase"] == "validated"
    report.update(success=True, phase="complete")
    (directory / "report.json").write_text(json.dumps(report, indent=2) + "\n")
    print("Passed all four independent MCP transport/protocol combinations with PostgreSQL replicas, restart and cleanup.")
