"""Supervise a locked real A2A client/server control on owned Docker loopback."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import sys
import tempfile
import time
import traceback
import uuid

from common import run
from runner import CATALOG_INPUTS, ROOT, build_binaries, cached_go, docker, fingerprint, local_daemon
import a2a_tools as tools
from a2a_contract import CASES, check_result, check_traffic
from a2a_catalog import Catalog
import a2a_handoff as handoff


def certificates(private):
    """Generate an invocation-owned CA and localhost leaf; mount no key in the client."""
    server, client = private / "server", private / "client"
    server.mkdir(mode=0o700)
    client.mkdir(mode=0o700)
    run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "2",
         "-keyout", str(private / "ca.key"), "-out", str(client / "ca.pem"),
         "-subj", "/CN=Owned A2A control CA", "-addext", "basicConstraints=critical,CA:TRUE", "-addext", "keyUsage=critical,keyCertSign,cRLSign"], timeout=15)
    run(["openssl", "req", "-newkey", "rsa:2048", "-nodes", "-keyout", str(server / "key.pem"),
         "-out", str(private / "leaf.csr"), "-subj", "/CN=Owned A2A control"], timeout=15)
    (private / "extensions").write_text("subjectAltName=IP:127.0.0.1\nbasicConstraints=critical,CA:FALSE\nextendedKeyUsage=serverAuth\nkeyUsage=critical,digitalSignature,keyEncipherment\nauthorityKeyIdentifier=keyid,issuer\nsubjectKeyIdentifier=hash\n")
    run(["openssl", "x509", "-req", "-in", str(private / "leaf.csr"), "-CA", str(client / "ca.pem"),
         "-CAkey", str(private / "ca.key"), "-set_serial", "1", "-days", "2",
         "-extfile", str(private / "extensions"), "-out", str(server / "cert.pem")], timeout=15)
    # The readiness probe needs CA trust too, without a client mount of the leaf key.
    shutil.copyfile(client / "ca.pem", server / "ca.pem")
    for key in (private / "ca.key", server / "key.pem"):
        key.chmod(0o600)


def isolation(name, network, private, mounts):
    """Verify effective Docker settings, including exact readonly mount scope."""
    info = json.loads(docker("inspect", name, timeout=10).stdout)[0]
    host = info["HostConfig"]
    assert host["NetworkMode"] == network and host["ReadonlyRootfs"] and not host["Privileged"]
    assert "ALL" in host["CapDrop"] and "no-new-privileges" in host["SecurityOpt"]
    assert not host["PortBindings"] and not info["NetworkSettings"]["Ports"]
    assert info["Config"]["User"] == f"{os.getuid()}:{os.getgid()}"
    assert host["Memory"] == 512*1024*1024 and host["PidsLimit"] == 128
    bound = {}
    for mount in info["Mounts"]:
        assert mount["Type"] in ("bind", "tmpfs")
        if mount["Type"] == "bind":
            assert not mount["RW"] and Path(mount["Source"]).is_relative_to(private)
            bound[mount["Source"]] = mount["Destination"]
    assert bound == {str(private / source): target for source, target in mounts}
    return {"network": "none" if network == "none" else "owned-server-loopback", "external_egress": False,
            "non_root": True, "read_only": True, "published_ports": [], "bind_destinations": sorted(bound.values())}


def cleanup(names, private, report):
    """Finish bounded cleanup even if another signal arrives; never report uncertainty as success."""
    old = signal.pthread_sigmask(signal.SIG_BLOCK, {signal.SIGINT, signal.SIGTERM})
    try:
        try:
            tools.remove(names)
        finally:
            shutil.rmtree(private)
        report["cleanup"] = not private.exists() and not report.get("creation_unconfirmed", False)
    finally:
        signal.pthread_sigmask(signal.SIG_SETMASK, old)


def check():
    arch = local_daemon()
    stamp = tools.installed()
    inputs = [*CATALOG_INPUTS, ROOT / "Makefile", tools.HERE]
    source = fingerprint(inputs)
    run_id = uuid.uuid4().hex[:12]
    directory = tools.OUTPUT / run_id
    directory.mkdir(parents=True, mode=0o700)
    private = Path(tempfile.mkdtemp(prefix="catalog-a2a-"))
    report = {"run_id": run_id, "scope": "a2a-governed-handoff", "source": source, "tools": stamp,
              "success": False, "cleanup": False, "cases": {}, "governed_cases": {}, "catalog_denials": {}, "failures": {}, "phase": "setup"}
    names = []
    report["containers"] = names
    secrets = []

    def save():
        (directory / "report.json").write_text(json.dumps(report, indent=2, ensure_ascii=False) + "\n")

    def deadline(*_):
        raise TimeoutError("A2A control exceeded 600 seconds")

    old = signal.signal(signal.SIGALRM, deadline)
    signal.alarm(600)
    save()
    print("A2A report: " + str(directory / "report.json"), flush=True)
    try:
        certificates(private)
        fixture, commands, catalog_files = (private / part for part in ("fixture", "commands", "catalog"))
        for path in (fixture, commands, catalog_files):
            path.mkdir(mode=0o700)
        for name in ("a2a_fixture.py", "a2a_contract.py"):
            shutil.copyfile(tools.HERE / name, fixture / name)
        go = cached_go()
        build_binaries(commands, go, arch)
        report["binaries"] = {name: hashlib.sha256((commands / name).read_bytes()).hexdigest() for name in ("catalogd", "catalogctl")}
        report["go"] = run([go, "version"], env={"GOTOOLCHAIN": "local", "GOENV": "off"}, timeout=10).stdout.strip()
        prefix = "catalog-a2a-" + run_id
        catalog_name, server = prefix + "-catalog", prefix + "-server"
        catalog = Catalog(catalog_files, catalog_name, {})
        catalog.configuration()
        secrets.extend(catalog.tokens.values())
        common_mount = ("--mount", f"type=bind,src={fixture},dst=/fixture,readonly")
        names.append(catalog_name)
        tools.create(catalog_name, "--network=none", *common_mount,
                     "--mount", f"type=bind,src={commands},dst=/commands,readonly",
                     "--mount", f"type=bind,src={catalog_files},dst=/catalog,readonly",
                     "--entrypoint=/commands/catalogd", stamp["image"], "all", "--config", "/catalog/config.json")
        docker("start", catalog_name, timeout=10)
        catalog_id = docker("inspect", catalog_name, "--format", "{{.Id}}", timeout=10).stdout.strip()
        report["isolation"] = {"catalog": isolation(catalog_name, "none", private, (("fixture", "/fixture"), ("commands", "/commands"), ("catalog", "/catalog")))}
        expires = time.monotonic() + 20
        while docker("exec", catalog_name, "/commands/catalogctl", "probe", "--url", "http://127.0.0.1:8080/readyz", timeout=5, check=False).code:
            if time.monotonic() >= expires:
                raise RuntimeError("owned Catalog did not become ready")
            time.sleep(0.1)
        names.append(server)
        tools.create(server, "--network=container:" + catalog_name, *common_mount,
                     "--mount", f"type=bind,src={private / 'server'},dst=/server,readonly",
                     stamp["image"], "/fixture/a2a_fixture.py", "serve")
        docker("start", server, timeout=10)
        report["isolation"]["server"] = isolation(server, "container:" + catalog_id, private, (("fixture", "/fixture"), ("server", "/server")))
        report["phase"] = "server-readiness"
        save()
        expires = time.monotonic() + 20
        while True:
            probe = docker("exec", server, "python", "-B", "/fixture/a2a_fixture.py", "probe", timeout=5, check=False)
            if probe.code == 0:
                assert json.loads(probe.stdout) == [], "runtime received unexpected traffic before control"
                break
            if time.monotonic() >= expires:
                raise RuntimeError("A2A fixture did not become ready")
            time.sleep(0.1)

        def observations():
            return json.loads(docker("exec", server, "python", "-B", "/fixture/a2a_fixture.py", "probe", timeout=5).stdout)

        def execute(mode):
            name = prefix + "-" + mode
            names.append(name)
            tools.create(name, "--network=container:" + catalog_name, *common_mount,
                         "--mount", f"type=bind,src={private / 'client'},dst=/client,readonly",
                         stamp["image"], "/fixture/a2a_fixture.py", "client", mode)
            report["isolation"][mode] = isolation(name, "container:" + catalog_id, private, (("fixture", "/fixture"), ("client", "/client")))
            report["phase"] = "native-" + mode
            save()
            result = docker("start", "--attach", name, timeout=30, limit=1024*1024, check=False)
            state = json.loads(docker("inspect", name, timeout=10).stdout)[0]["State"]
            assert not state["Running"] and state["ExitCode"] == result.code
            assert not any(secret in result.stdout + result.stderr for secret in secrets), "client leaked a Catalog credential"
            (directory / (mode + ".json")).write_text(json.dumps({"exit": result.code, "stdout": result.stdout, "stderr": result.stderr}, indent=2) + "\n")
            return result

        result = execute("direct")
        report["native_exit"] = result.code
        assert result.code == 0, "native A2A control failed; see direct.json"
        report["result"] = json.loads(result.stdout)
        check_result(report["result"])
        report["direct_observations"] = observations()
        check_traffic(report["direct_observations"], report["result"])
        report["cases"] = dict.fromkeys(CASES, True)
        catalog.card = report["result"]["card"]
        report["phase"] = "governance"
        save()
        selected, report["selection"], report["governed_cases"] = catalog.qualify()
        report["metadata_observations"] = observations()
        assert report["metadata_observations"] == report["direct_observations"], "Catalog metadata operations contacted A2A"
        card_path = private / "client/card.json"
        for case in handoff.CATALOG_DENIALS:
            report["phase"] = "catalog-" + case
            save()
            assert not card_path.exists()
            owned = list(names)
            denied = catalog.denial(case, report["selection"]["release"])
            after = observations()
            assert after == report["metadata_observations"] and names == owned and not card_path.exists(), "denial created a handoff"
            report["catalog_denials"][case] = dict(denied, observations=after)
        card = handoff.validate_record(selected, report["selection"], catalog.card)
        card_path.write_bytes(handoff.canonical(card))
        card_path.chmod(0o600)
        result = execute("governed")
        report["handoff_exit"] = result.code
        assert result.code == 0, "native A2A handoff failed; see governed.json"
        report["handoff_result"] = json.loads(result.stdout)
        check_result(report["handoff_result"], governed=True)
        report["handoff_observations"] = observations()
        check_traffic(report["handoff_observations"][3:], report["handoff_result"], governed=True)
        result = execute("untrusted-ca")
        report["failures"]["untrusted-ca"] = handoff.failure_result("untrusted-ca", result)
        report["observations"] = observations()
        assert report["observations"] == report["handoff_observations"], "TLS rejection reached HTTP"
        catalog.verify_retained(report["selection"], selected)
        docker("stop", "--time", "5", server, timeout=10)
        state = json.loads(docker("inspect", server, timeout=10).stdout)[0]["State"]
        assert not state["Running"] and state["ExitCode"] == 0, "A2A listener did not stop normally"
        report["server_stopped"] = True
        result = execute("unavailable")
        report["failures"]["unavailable"] = handoff.failure_result("unavailable", result)
        catalog.verify_retained(report["selection"], selected)
        assert card_path.read_bytes() == handoff.canonical(card)
        report["post_failure_integrity"] = True
        docker("stop", "--time", "25", catalog_name, timeout=30)
        state = json.loads(docker("inspect", catalog_name, timeout=10).stdout)[0]["State"]
        assert not state["Running"] and state["ExitCode"] == 0, "Catalog shutdown failed"
        assert source == fingerprint(inputs), "A2A inputs changed during execution"
    except BaseException as error:
        report["error"] = traceback.format_exc(limit=4)[-6000:]
        if isinstance(error, tools.CreationUnconfirmed):
            report["creation_unconfirmed"] = True
        # Redact synthetic credentials even from failure diagnostics.
        for name in names:
            try:
                logs = docker("logs", name, timeout=5, limit=1024*1024, check=False)
                raw = logs.stdout + logs.stderr
                for secret in secrets:
                    raw = raw.replace(secret, "[redacted]")
                (directory / (name + ".log")).write_text(raw)
            except (RuntimeError, OSError):
                pass
        raise
    finally:
        signal.alarm(0)
        signal.signal(signal.SIGALRM, old)
        try:
            cleanup(names, private, report)
        finally:
            save()
    try:
        handoff.complete(report)
    except (AssertionError, ValueError, KeyError, TypeError):
        report["error"] = traceback.format_exc(limit=4)[-6000:]
        save()
        raise
    report.update(success=True, phase="complete")
    save()
    print("Passed A2A direct control, governed handoff, three Catalog denials and two runtime failures; owned cleanup complete.")


def main():
    if not __debug__:
        raise RuntimeError("A2A qualification requires Python assertions")
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("install", "check"))
    args = parser.parse_args()
    signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(KeyboardInterrupt()))
    (tools.install if args.action == "install" else check)()


if __name__ == "__main__":
    try:
        main()
    except (AssertionError, RuntimeError, OSError, ValueError, KeyError, TypeError, KeyboardInterrupt) as error:
        print("A2A qualification failed: " + (str(error) or type(error).__name__), file=sys.stderr)
        sys.exit(2)
