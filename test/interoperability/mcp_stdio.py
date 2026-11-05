"""Preserve the legacy Catalog stdio assertions with the locked independent SDK."""

import json
from pathlib import Path
import platform
import shutil
import signal
import tempfile
import time
import traceback
import uuid

from common import run
from runner import CATALOG_INPUTS, ROOT, docker, fingerprint, local_daemon
from catalog_control import CatalogControl
import mcp_tools as tools
import mcp_catalog_tools

ORIGIN = "https://127.0.0.1:9443"
CASES = frozenset(("initialize-list", "four-kind-pages", "release-projection", "indexed-selection",
                   "closed-arguments", "denied-access", "tenant-isolation", "revoked-access",
                   "malformed-response", "api-byte-limit", "tool-byte-limit", "credential-reflection",
                   "cancellation", "concurrency", "eof", "termination", "tls"))


def validate_result(result, secrets):
    """Accept only a complete independent-client report, never tool exit alone."""
    assert result.code == 0, "independent MCP client failed"
    assert not any(secret in result.stdout + result.stderr for secret in secrets), "client leaked a fixture credential"
    assert not result.stderr.strip(), "independent MCP client emitted diagnostics"
    data = json.loads(result.stdout)
    assert set(data) == {"protocol", "client", "server", "cases", "childrenExited", "peerUnchanged"}
    assert data["client"] == "mcp@2.3.0"
    assert data["protocol"] == "2025-11-25", "unexpected negotiated revision; review the pinned client"
    assert data["server"]["name"] == "open-agentic-catalog-control" and data["server"]["version"]
    assert set(data["cases"]) == CASES and all(value is True for value in data["cases"].values())
    assert data["childrenExited"] is True and data["peerUnchanged"] is True
    return data


def wait_ready(name):
    deadline = time.monotonic() + 25
    while time.monotonic() < deadline:
        result = docker("exec", name, "/commands/catalogctl", "probe", "--url", "http://127.0.0.1:8080/readyz", timeout=5, check=False)
        if result.code == 0:
            return
        state = json.loads(docker("inspect", name, timeout=5).stdout)[0]["State"]
        assert state["Running"], "owned Catalog exited before readiness"
        time.sleep(.1)
    raise RuntimeError("owned Catalog readiness deadline")


def setup(catalog, peer_origin):
    """Publish native demo data and synchronize an owned source through normal APIs."""
    seeded = docker("exec", catalog.container, "/commands/catalogctl", "seed-demo", "--sample-set", "expanded",
                    "--base-url", "http://127.0.0.1:8080", "--state-file", "/tmp/seed.json",
                    "--archive-origin", ORIGIN, "--timeout", "2m", timeout=130, limit=2*1024*1024)
    seed = json.loads(seeded.stdout)
    assert seed["published"] == 12 and len(seed["items"]) == 14
    inspection, _ = catalog.request("/api/v1/federation/source-inspections", {"url": peer_origin + "/.well-known/ard.json", "networkProfile": "stdio-owned"}, status=201)
    assert inspection["entryCount"] == 4
    source, etag = catalog.request("/api/v1/federation/sources", dict(inspectionId=inspection["id"], displayName="Owned stdio peer",
                                  retrievalMode="indexed", updateMode="manual_review", networkProfile="stdio-owned",
                                  scope={"publishers": ["catalog.example.com"]}, reason="Independent MCP client qualification"), status=201)
    receipt, _ = catalog.request("/api/v1/federation/sources/" + source["id"] + ":sync", {}, etag=etag, status=202)
    deadline = time.monotonic() + 40
    while True:
        job, _ = catalog.request("/api/v1/jobs/" + receipt["job"]["id"])
        assert job["state"] not in ("failed", "cancelled"), "owned source sync failed"
        if job["state"] == "succeeded":
            break
        assert time.monotonic() < deadline, "owned source sync deadline"
        time.sleep(.1)
    snapshot, _ = catalog.request("/api/v1/federation/snapshots/" + job["result"]["snapshotId"])
    assert snapshot["state"] == "complete" and snapshot["memberCount"] == 4 and snapshot["enumerationComplete"]
    search, _ = catalog.request("/api/v1/catalog/search", {"origins": ["indexed"], "limit": 100}, actor="local-reader")
    assert len(search["items"]) == 3, "owned indexed valid controls missing"
    native = [{key: item.get(key) for key in ("id", "identifier", "version", "versionPresent", "type")} for item in search["items"]]
    catalog.write("expected.json", json.dumps({"releases": [i for i in seed["items"] if i.get("releaseId")], "sourceId": source["id"], "indexedControl": native}))
    return {"localReleases": 12, "indexedEntries": 3, "syncJob": job["id"], "snapshot": snapshot["id"], "indexedControl": native}


def check():
    # Lazy import preserves the existing runner's default and avoids import cycles.
    from mcp_runner import build, isolation
    arch = local_daemon()
    if platform.system() != "Linux" or {"x86_64": "amd64", "aarch64": "arm64"}.get(platform.machine()) != arch:
        raise RuntimeError("Catalog stdio qualification requires a native matching Linux host/engine")
    stamp = mcp_catalog_tools.installed()
    inputs = [*CATALOG_INPUTS, ROOT / "Makefile", tools.HERE]
    source = fingerprint(inputs)
    tools.OUTPUT.mkdir(parents=True, exist_ok=True)
    directory = Path(tempfile.mkdtemp(prefix="stdio-", dir=tools.OUTPUT))
    private = directory / "private"
    private.mkdir(mode=0o700)
    names, secrets = [], []
    network = None
    report = {"scope": "catalog-control-mcp-stdio", "source": source, "installation": stamp,
              "containers": names, "phase": "setup", "cleanup": False, "success": False}
    print("Catalog stdio report: " + str(directory.relative_to(ROOT)), flush=True)
    previous = signal.signal(signal.SIGALRM, lambda *_: (_ for _ in ()).throw(TimeoutError("Catalog stdio scenario deadline")))
    signal.alarm(600)
    try:
        commands, peer, catalog_files, client = (private / name for name in ("commands", "peer", "catalog", "client"))
        for path in (commands, peer, catalog_files, client):
            path.mkdir(mode=0o700)
        report["binaries"] = build(commands, arch)
        shutil.copyfile(tools.INPUTS / "catalog_client.py", client / "catalog_client.py")
        shutil.copyfile(tools.HERE / "mcp_diagnostics.py", client / "mcp_diagnostics.py")
        image = stamp["image"]
        prefix = "catalog-stdio-" + uuid.uuid4().hex[:12]
        network = prefix + "-network"
        report["network"] = {"name": network}
        previous_mask = signal.pthread_sigmask(signal.SIG_BLOCK, {signal.SIGINT, signal.SIGTERM, signal.SIGALRM})
        try:
            docker("network", "create", "--internal", network, timeout=30)
        except (RuntimeError, OSError) as error:
            report["creation_unconfirmed"] = True
            raise RuntimeError("owned network creation unconfirmed") from error
        finally:
            signal.pthread_sigmask(signal.SIG_SETMASK, previous_mask)
        from peer_runner import addresses
        private_ip = addresses(network)[0]
        peer_origin = "https://" + private_ip + ":9443"
        report["network"] = {"name": network, "internal": True, "peer": private_ip}
        run([str(commands / "fixture"), "prepare-stdio", str(peer), private_ip], timeout=15)
        server = prefix + "-peer"
        names.append(server)
        server_id = tools.create(server, "--network=" + network, "--ip=" + private_ip, "--mount", f"type=bind,src={peer},dst=/private,readonly",
                                 "--mount", f"type=bind,src={commands},dst=/commands,readonly", "--entrypoint=/commands/fixture", image, "serve-stdio").stdout.strip()
        docker("start", server, timeout=10)
        catalog_name = prefix + "-catalog"
        catalog = CatalogControl(catalog_files, catalog_name)
        catalog.configuration()
        secrets.extend(catalog.tokens.values())
        config = json.loads((catalog_files / "config.json").read_text())
        # The demo loader uses its explicit, public local-development identities.
        # Random aliases are used for MCP, disclosure checks and current revocation.
        config["auth"]["developmentTokens"].update(json.loads((ROOT / "config/local.json").read_text())["auth"]["developmentTokens"])
        config["demoArchives"] = dict(origin=ORIGIN, trustBundleFile="/private/ca.pem", allowlistFile="/private/allowlist.json")
        config["federation"]["policyFile"] = "/catalog/policy.json"
        catalog.write("config.json", json.dumps(config))
        catalog.write("policy.json", json.dumps(dict(schemaVersion=1, policyId="stdio-owned", revision=1, public={"mode": "disabled"},
                      privateProfiles=[dict(name="stdio-owned", origins=[peer_origin], addressRanges=[private_ip + "/32"],
                                            assignedTenantIds=["00000000-0000-7000-8000-000000000001"], transportRef="direct", trustBundleFile="/private/ca.pem")])))
        for role in ("local-admin", "invalid", "other-tenant-reader"):
            catalog.write(role + "-token", catalog.tokens[role])
        names.append(catalog_name)
        tools.create(catalog_name, "--network=container:" + server,
                     "--mount", f"type=bind,src={commands},dst=/commands,readonly",
                     "--mount", f"type=bind,src={peer},dst=/private,readonly",
                     "--mount", f"type=bind,src={catalog_files},dst=/catalog,readonly",
                     "--entrypoint=/commands/catalogd", image, "all", "--config", "/catalog/config.json")
        docker("start", catalog_name, timeout=10)
        report["isolation"] = {"peer": isolation(server, network, private, (("peer", "/private"), ("commands", "/commands"))),
                               "catalog": isolation(catalog_name, "container:" + server_id, private, (("commands", "/commands"), ("peer", "/private"), ("catalog", "/catalog")))}
        report["isolation"]["peer"]["network"] = "owned-internal-bridge"
        wait_ready(catalog_name)
        report["phase"] = "governance-and-indexing"
        report["fixtures"] = setup(catalog, peer_origin)
        report["phase"] = "independent-client"
        (directory / "report.json").write_text(json.dumps(report, indent=2) + "\n")
        name = prefix + "-client"
        names.append(name)
        tools.create(name, "--network=container:" + server,
                     "--mount", f"type=bind,src={commands},dst=/commands,readonly",
                     "--mount", f"type=bind,src={peer},dst=/private,readonly",
                     "--mount", f"type=bind,src={catalog_files},dst=/catalog,readonly",
                     "--mount", f"type=bind,src={client},dst=/client,readonly", image, "/client/catalog_client.py")
        report["isolation"]["client"] = isolation(name, "container:" + server_id, private, (("commands", "/commands"), ("peer", "/private"), ("catalog", "/catalog"), ("client", "/client")))
        result = docker("start", "-a", name, timeout=180, limit=2*1024*1024, check=False)
        state = json.loads(docker("inspect", name, timeout=10).stdout)[0]["State"]
        assert not state["Running"] and state["ExitCode"] == 0, "independent client container failed"
        report["client"] = validate_result(result, secrets)
        for stopped in (catalog_name, server):
            docker("stop", "--time", "25", stopped, timeout=30)
            state = json.loads(docker("inspect", stopped, timeout=10).stdout)[0]["State"]
            assert not state["Running"] and state["ExitCode"] == 0, "fixture shutdown failed"
            logs = docker("logs", stopped, timeout=10, limit=2*1024*1024)
            assert not any(secret in logs.stdout + logs.stderr for secret in secrets), "service log credential exposure"
        assert source == fingerprint(inputs), "Catalog stdio harness inputs changed during execution"
        report["phase"] = "validated"
    except tools.CreationUnconfirmed:
        report["creation_unconfirmed"] = True
        raise
    except (AssertionError, RuntimeError, OSError, ValueError, KeyError, TypeError, KeyboardInterrupt):
        detail = traceback.format_exc(limit=4)
        for secret in secrets:
            detail = detail.replace(secret, "[fixture-credential]")
        report["error"] = detail[-6000:]
        report["failure_logs"] = {}
        for name in names:
            try:
                logs = docker("logs", name, timeout=5, limit=32*1024, check=False)
                safe = logs.stdout + logs.stderr
                for secret in secrets:
                    safe = safe.replace(secret, "[fixture-credential]")
                report["failure_logs"][name] = safe[-6000:]
            except (RuntimeError, OSError):
                report["failure_logs"][name] = "unavailable"
        raise
    finally:
        signal.alarm(0)
        signal.signal(signal.SIGALRM, previous)
        containers_removed = False
        network_removed = network is None
        try:
            try:
                tools.remove(names)
                containers_removed = True
            finally:
                if network:
                    result = docker("network", "rm", network, timeout=15, check=False)
                    network_removed = result.code == 0 or "not found" in result.stderr
                    if not network_removed:
                        raise RuntimeError("owned stdio network cleanup failed")
        finally:
            shutil.rmtree(private)
            report["cleanup"] = containers_removed and network_removed and not private.exists() and not report.get("creation_unconfirmed", False)
            (directory / "report.json").write_text(json.dumps(report, indent=2) + "\n")
    assert report["cleanup"] and report["phase"] == "validated"
    report.update(success=True, phase="complete")
    (directory / "report.json").write_text(json.dumps(report, indent=2) + "\n")
    print("Passed 17 Catalog stdio qualification groups with the locked independent client; cleanup passed.")
