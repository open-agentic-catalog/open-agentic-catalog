"""Run unchanged Inspector and Everything on owned loopback, through a governed Catalog release without egress."""

import argparse
import copy
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import signal
import sys
import tempfile
import time
import traceback
import uuid

from common import run
from runner import CATALOG_INPUTS, ROOT, build_binaries, cached_go, docker, fingerprint, local_daemon
import mcp_tools as tools
from mcp_contract import CASES, MARKER, client_config, validate_case, validate_observations
from mcp_catalog import Catalog
import mcp_handoff as handoff


def certificates(binary, directory):
    run([str(binary), "certificates", str(directory)], timeout=10)


def build(directory, arch):
    go = cached_go()
    env = {key: os.environ[key] for key in ("PATH", "HOME", "GOCACHE", "GOMODCACHE") if key in os.environ}
    env.update(CGO_ENABLED="0", GOOS="linux", GOARCH=arch, GOENV="off", GOWORK="off", GOFLAGS="-mod=readonly",
               GOPROXY="off", GOSUMDB="off", GOTOOLCHAIN="local", GOTELEMETRY="off",
               GOPRIVATE="", GONOPROXY="", GONOSUMDB="")
    run([go, "build", "-trimpath", "-o", str(directory / "fixture"), "./test/interoperability/mcpfixture"], env=env, timeout=120)
    build_binaries(directory, go, arch)
    return {name: hashlib.sha256((directory / name).read_bytes()).hexdigest() for name in ("fixture", "catalogd", "catalogctl")}


def isolation(container, network, private, mounts=()):
    info = json.loads(docker("inspect", container, timeout=10).stdout)[0]
    host = info["HostConfig"]
    assert host["NetworkMode"] == network
    assert host["ReadonlyRootfs"] and not host["Privileged"]
    assert "ALL" in host["CapDrop"] and "no-new-privileges" in host["SecurityOpt"]
    assert not host["PortBindings"] and not info["NetworkSettings"]["Ports"]
    assert info["Config"]["User"] == f"{os.getuid()}:{os.getgid()}"
    assert host["Memory"] == 512*1024*1024 and host["PidsLimit"] == 128
    assert host["LogConfig"]["Config"]["max-size"] == "1m"
    bound = {}
    for mount in info["Mounts"]:
        assert mount["Type"] in ("bind", "tmpfs"), "unexpected fixture mount"
        if mount["Type"] == "bind":
            assert Path(mount["Source"]).is_relative_to(private) and not mount["RW"]
            bound[mount["Source"]] = mount["Destination"]
    assert bound == {str(private / source): target for source, target in mounts}, "fixture mount scope changed"
    return {"network": "none" if network == "none" else "owned-server-loopback", "non_root": True,
            "read_only": True, "published_ports": [], "external_egress": False, "bind_destinations": sorted(bound.values())}


def check():
    arch = local_daemon()
    if platform.system() != "Linux" or {"x86_64": "amd64", "aarch64": "arm64"}.get(platform.machine()) != arch:
        raise RuntimeError("MCP control requires a native matching Linux host/engine")
    stamp = tools.installed()
    inputs = [*CATALOG_INPUTS, ROOT / "Makefile", tools.HERE]
    # Include every shared helper and Catalog input, not merely the wrapper source.
    source = fingerprint(inputs)
    tools.OUTPUT.mkdir(parents=True, exist_ok=True)
    report_dir = Path(tempfile.mkdtemp(prefix="run-", dir=tools.OUTPUT))
    report = {"scope": "mcp-governed-handoff", "source": source, "installation": stamp,
              "cases": {}, "governed_cases": {}, "catalog_denials": {}, "post_failure_integrity": False,
              "cleanup": False, "success": False, "phase": "setup"}
    print("MCP report: " + str(report_dir.relative_to(ROOT)), flush=True)
    names = []
    report["containers"] = names
    prefix = "catalog-mcp-" + uuid.uuid4().hex[:12]
    image = stamp["image"]
    private = Path(tempfile.mkdtemp(prefix="private-", dir=report_dir))
    # The complete scenario has a 10 minute deadline, including setup. Cleanup has separate bounds.
    previous = signal.signal(signal.SIGALRM, lambda *_: (_ for _ in ()).throw(TimeoutError("MCP scenario deadline")))
    signal.alarm(600)
    secrets = []
    try:
        commands, wrapper, clients, catalog_files = (private / part for part in ("commands", "wrapper", "clients", "catalog"))
        for directory in (commands, wrapper, clients, catalog_files):
            directory.mkdir(mode=0o700)
        report["binaries"] = build(commands, arch)
        certificates(commands / "fixture", wrapper)
        key = os.urandom(32).hex()
        secrets.append(key)
        (wrapper / "key").write_text(key)
        (wrapper / "key").chmod(0o600)
        # Only the wrapper sees the TLS private key; Inspector receives the admission key in config.
        shutil.copyfile(wrapper / "ca.pem", clients / "ca.pem")
        for name, config in (("client.json", client_config(key)), ("missing.json", client_config())):
            (clients / name).write_text(json.dumps(config))
            (clients / name).chmod(0o600)
        server = prefix + "-server"
        names.append(server)
        server_id = tools.create(server, "--network=none", "--env=PORT=3001", image, tools.EVERYTHING, "streamableHttp").stdout.strip()
        docker("start", server, timeout=10)
        proxy = prefix + "-proxy"
        names.append(proxy)
        tools.create(proxy, "--network=container:" + server,
               "--mount", f"type=bind,src={wrapper},dst=/private,readonly",
               "--mount", f"type=bind,src={commands},dst=/commands,readonly", "--entrypoint=/commands/fixture", image, "serve")
        docker("start", proxy, timeout=10)
        report["isolation"] = {"server": isolation(server, "none", private),
                               "proxy": isolation(proxy, "container:" + server_id, private, (("wrapper", "/private"), ("commands", "/commands")))}
        deadline = time.monotonic() + 20
        while True:
            status = docker("exec", proxy, "/commands/fixture", "probe", check=False, timeout=5)
            if status.code == 0:
                break
            if time.monotonic() >= deadline:
                raise RuntimeError("local MCP fixture did not become ready")
            time.sleep(0.1)
        before = json.loads(status.stdout)
        assert before["requests"] == 0
        def execute(case, *, derived=False):
            nonlocal before
            failure = case in handoff.MCP_FAILURES
            label = "handoff-" + case if derived and not failure else case
            report["phase"] = label
            (report_dir / "report.json").write_text(json.dumps(report, indent=2) + "\n")
            print("MCP case: " + label, flush=True)
            name = prefix + "-" + label
            names.append(name)
            method = "tools/list" if failure else {"initialize": "initialize", "tools-list": "tools/list", "echo": "tools/call", "missing-key": "tools/list", "tool-error": "tools/call"}[case]
            config_file = case + ".json" if case in handoff.MCP_FAILURES[:3] else "handoff.json" if derived else "missing.json" if case == "missing-key" else "client.json"
            args = ["--cli", "--config", "/client/" + config_file, "--server", "handoff", "--method", method, "--format", "json", "--stored-auth-only"]
            if case in ("echo", "tool-error"):
                args += ["--tool-name", "echo", "--tool-args-json", json.dumps({"message": MARKER} if case == "echo" else {"message": 42})]
            trust = [] if case == "untrusted-ca" else ["--env=NODE_EXTRA_CA_CERTS=/client/ca.pem"]
            tools.create(name, "--network=container:" + server,
                            "--mount", f"type=bind,src={clients},dst=/client,readonly",
                            *trust, "--env=MCP_STORAGE_DIR=/tmp/mcp",
                            "--env=MCP_CLIENT_CONFIG_PATH=/tmp/settings.json", "--env=MCP_INSPECTOR_SECRET_STORE=memory",
                            image, tools.INSPECTOR, *args)
            result = docker("start", "--attach", name, timeout=30, limit=2*1024*1024, check=False)
            state = json.loads(docker("inspect", name, timeout=10).stdout)[0]["State"]
            assert not state["Running"] and state["ExitCode"] == result.code, "client did not execute to native completion"
            report["isolation"][label] = isolation(name, "container:" + server_id, private, (("clients", "/client"),))
            # Synthetic credentials and private paths never enter retained output.
            assert not any(secret in result.stdout + result.stderr for secret in secrets), "client leaked a fixture credential"
            (report_dir / (label + ".json")).write_text(json.dumps({"exit": result.code, "stdout": result.stdout, "stderr": result.stderr}, indent=2) + "\n")
            try:
                report["cases"][label] = handoff.failure_result(case, result) if failure else validate_case(case, result)
            except (AssertionError, ValueError, KeyError):
                report["failed_command"] = {"case": label, "exit": result.code, "stdout": result.stdout[:8192], "stderr": result.stderr[:8192]}
                raise
            if case == "unavailable":
                report["cases"][label]["observations"] = None
                return
            after = json.loads(docker("exec", proxy, "/commands/fixture", "probe", timeout=5).stdout)
            report["cases"][label]["observations"] = after
            if failure:
                handoff.failure_traffic(case, before, after)
            elif case == "missing-key":
                assert after["forwarded"] == before["forwarded"]
                assert after["denied"] > before["denied"]
            else:
                assert len(after["initialize"]) == len(before["initialize"]) + 1
                assert after["http"].get("GET", 0) == before["http"].get("GET", 0) + 1
                calls = 1 if case in ("echo", "tool-error") else 0
                assert after["methods"].get("tools/call", 0) == before["methods"].get("tools/call", 0) + calls
            before = after
        for case in CASES:
            execute(case)
        report["direct_observations"] = before
        validate_observations(before)
        report["phase"] = "catalog-governance"
        (report_dir / "report.json").write_text(json.dumps(report, indent=2) + "\n")
        catalog_name = prefix + "-catalog"
        catalog = Catalog(catalog_files, catalog_name)
        catalog.configuration()
        secrets.extend(catalog.tokens.values())
        names.append(catalog_name)
        tools.create(catalog_name, "--network=container:" + server,
                     "--mount", f"type=bind,src={commands},dst=/commands,readonly",
                     "--mount", f"type=bind,src={catalog_files},dst=/catalog,readonly",
                     "--entrypoint=/commands/catalogd", image, "all", "--config", "/catalog/config.json")
        docker("start", catalog_name, timeout=10)
        report["isolation"]["catalog"] = isolation(catalog_name, "container:" + server_id, private, (("commands", "/commands"), ("catalog", "/catalog")))
        deadline = time.monotonic() + 20
        while True:
            status = docker("exec", catalog_name, "/commands/catalogctl", "probe", "--url", "http://127.0.0.1:8080/readyz", timeout=5, check=False)
            if status.code == 0:
                break
            if time.monotonic() >= deadline:
                raise RuntimeError("owned Catalog did not become ready")
            time.sleep(0.1)
        selected, report["selection"], report["governed_cases"] = catalog.qualify()
        after = json.loads(docker("exec", proxy, "/commands/fixture", "probe", timeout=5).stdout)
        assert after == before, "Catalog metadata operations contacted MCP"
        report["metadata_observations"] = after
        for case in handoff.CATALOG_DENIALS:
            report["phase"] = "catalog-" + case
            (report_dir / "report.json").write_text(json.dumps(report, indent=2) + "\n")
            assert not (clients / "handoff.json").exists()
            owned = list(names)
            result = catalog.denial(case, report["selection"]["release"])
            after = json.loads(docker("exec", proxy, "/commands/fixture", "probe", timeout=5).stdout)
            assert after == before and names == owned and not (clients / "handoff.json").exists(), "denied read created a handoff"
            report["catalog_denials"][case] = dict(result, observations=after)
        config = handoff.map_client(selected, key)
        path = clients / "handoff.json"
        path.write_text(json.dumps(config))
        path.chmod(0o600)
        report["governed_cases"]["metadata-mapping"] = True
        report["connection"] = {k: v for k, v in config["mcpServers"]["handoff"].items() if k != "headers"}
        for case in ("initialize", "tools-list", "echo"):
            execute(case, derived=True)
        report["handoff_observations"] = before
        handoff.validate_traffic(report)
        wrong_key = os.urandom(32).hex()
        secrets.append(wrong_key)
        for case in handoff.MCP_FAILURES:
            if case in handoff.MCP_FAILURES[:3]:
                failed = copy.deepcopy(config)
                headers = failed["mcpServers"]["handoff"]["headers"]
                if case == "bearer-forwarding":
                    headers["Authorization"] = "Bearer " + catalog.tokens["local-reader"]
                else:
                    headers["X-MCP-Test-Key"] = wrong_key if case == "wrong-key" else catalog.tokens["local-reader"]
                path = clients / (case + ".json")
                path.write_text(json.dumps(failed))
                path.chmod(0o600)
            if case == "unavailable":
                docker("stop", "--time", "5", proxy, timeout=10)
                state = json.loads(docker("inspect", proxy, timeout=10).stdout)[0]["State"]
                assert not state["Running"] and state["ExitCode"] == 0, "wrapper did not stop normally"
                report["wrapper_stopped"] = True
            execute(case, derived=True)
        report["observations"] = before  # Last completed probe; the stopped wrapper cannot be observed.
        catalog.verify_retained(report["selection"], selected)
        report["post_failure_integrity"] = True
        # A successful fixture shutdown is part of qualification, not just forced removal.
        docker("stop", "--time", "25", catalog_name, timeout=30)
        stopped = json.loads(docker("inspect", catalog_name, timeout=10).stdout)[0]["State"]
        assert not stopped["Running"] and stopped["ExitCode"] == 0, "Catalog shutdown failed"
        logs = docker("logs", server, timeout=10, limit=2*1024*1024).stdout
        assert not any(secret in logs for secret in secrets)
        assert logs.count("Received MCP POST request") == report["observations"]["http"]["POST"]
        assert logs.count("Session initialized with ID:") == 7
        report["upstream_post_requests"] = logs.count("Received MCP POST request")
        report["upstream_log_sha256"] = hashlib.sha256(logs.encode()).hexdigest()
        if source != fingerprint(inputs):
            raise RuntimeError("MCP harness inputs changed during execution")
        report["phase"] = "validated"
    except tools.CreationUnconfirmed:
        # A missing name immediately after a CLI timeout does not prove the daemon cancelled creation.
        report["creation_unconfirmed"] = True
        raise
    except (AssertionError, RuntimeError, OSError, ValueError, KeyError, TypeError, KeyboardInterrupt):
        detail = traceback.format_exc(limit=4)
        for secret in secrets:
            detail = detail.replace(secret, "[fixture-credential]")
        report["error"] = detail[-6000:]
        raise
    finally:
        signal.alarm(0)
        signal.signal(signal.SIGALRM, previous)
        containers_removed = False
        try:
            tools.remove(names)
            containers_removed = True
        finally:
            shutil.rmtree(private)
            report["cleanup"] = containers_removed and not private.exists() and not report.get("creation_unconfirmed", False)
            (report_dir / "report.json").write_text(json.dumps(report, indent=2) + "\n")
    handoff.complete(report)
    report.update(success=True, phase="complete")
    (report_dir / "report.json").write_text(json.dumps(report, indent=2) + "\n")
    print("Passed 13 MCP exchanges, five governed assertions, three Catalog denials and post-failure integrity (22 outcomes).")


def main():
    if not __debug__:
        raise RuntimeError("MCP qualification requires Python assertions")
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("install", "check", "catalog", "catalog-install"))
    args = parser.parse_args()
    signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(KeyboardInterrupt()))
    if args.action == "catalog-install":
        from mcp_catalog_tools import install as install_catalog
        install_catalog()
    elif args.action == "catalog":
        from mcp_stdio import check as check_catalog
        from mcp_matrix import check as check_matrix
        check_catalog()
        check_matrix()
    else:
        (tools.install if args.action == "install" else check)()


if __name__ == "__main__":
    try:
        main()
    except (AssertionError, RuntimeError, OSError, ValueError, KeyError, TypeError, KeyboardInterrupt) as error:
        print("MCP qualification failed: " + (str(error) or type(error).__name__), file=sys.stderr)
        sys.exit(2)
