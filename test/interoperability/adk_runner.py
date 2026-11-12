"""Qualify native ADK discovery on an owned Catalog with no external network."""

import argparse
import json
from pathlib import Path
import platform
import shutil
import signal
import sys
import tempfile
import time
import uuid

from runner import CATALOG_INPUTS, ROOT, docker, fingerprint, local_daemon
from catalog_control import CatalogControl
from mcp_runner import build, certificates
from mcp_stdio import wait_ready
from mcp_handoff import documents, representations
from adk_contract import CASES, validate_result, validate_traffic
import adk_tools as tools


def seed(catalog):
    """Publish one benign MCP release through actual author/reviewer workflows."""
    namespace, _ = catalog.request("/api/v1/namespaces", {"slug": "adk", "displayName": "ADK qualification"}, status=201)
    publisher, etag = catalog.request("/api/v1/publishers", {"displayName": "Owned ADK fixture", "domains": ["catalog.example.com"]}, status=201)
    catalog.request("/api/v1/publishers/" + publisher["id"] + "/publication-domains/catalog.example.com:approve", {"reason": "Owned metadata fixture"}, etag=etag)
    resource, _ = catalog.request("/api/v1/resources", {"namespaceId": namespace["id"], "canonicalIdentifier": "urn:air:catalog.example.com:adk:metadata-only", "kind": "mcp_server"}, actor="local-contributor", status=201)
    card, registry = documents()
    for document in (card, registry):
        document["name"] = "mcp-servers/adk-metadata"
        document["description"] = "Benign metadata for native ADK qualification."
        document["remotes"][0]["url"] = "https://127.0.0.1:9555/never-invoke"
    version, _ = catalog.request("/api/v1/resources/" + resource["id"] + "/versions", {"version": "2.0.0", "representations": representations(card, registry)}, actor="local-contributor", status=201)
    evidence, _ = catalog.request("/api/v1/version-revisions/" + version["revision"]["id"] + ":validate", {}, actor="local-contributor", status=201)
    assert evidence["evidence"]["state"] == "complete", "owned fixture validation failed"
    listing, etag = catalog.request("/api/v1/catalog-listings", {"namespaceId": namespace["id"], "resourceId": resource["id"]}, actor="local-contributor", status=201)
    _, listing_etag = catalog.request("/api/v1/catalog-listings/" + listing["id"] + "/revisions", {
        "versionRevisionId": version["revision"]["id"], "displayName": "ADK metadata control", "description": "Native deterministic adapter qualification.",
        "tags": ["adk-native"], "representativeQueries": ["Find the owned ADK metadata control", "Discover the native ADK fixture"],
        "publisherId": publisher["id"], "license": "Apache-2.0", "visibility": "public"}, actor="local-contributor", etag=etag, status=201)
    submission, etag = catalog.request("/api/v1/catalog-listings/" + listing["id"] + "/submissions", {}, actor="local-contributor", status=201)
    catalog.request("/api/v1/review-submissions/" + submission["id"] + ":approve", {"reason": "Independent fixture review"}, actor="local-reviewer", etag=etag)
    release, _ = catalog.request("/api/v1/catalog-listings/" + listing["id"] + ":publish", {"submissionId": submission["id"]}, actor="local-publisher", etag=listing_etag, status=201)
    snapshot, etag = catalog.request("/api/v1/publication/snapshots", {"host": "127.0.0.1"}, actor="local-publisher", status=201)
    active, _ = catalog.request("/api/v1/publication/snapshots/" + snapshot["id"] + ":activate", {"host": "127.0.0.1", "reason": "Native ADK qualification"}, actor="local-publisher", etag=etag)
    assert active["state"] == "active", "owned publication did not activate"
    record, _ = catalog.request("/api/v1/catalog/resources/" + release["release"]["id"], actor="local-reader")
    return record, active["id"]


def revoke_reader(catalog):
    """Revoke only the bounded direct grants of this fixture's reader identity."""
    page, _ = catalog.request("/api/v1/access/self?limit=100", actor="local-reader")
    assert len(page["items"]) == 3 and not page.get("nextCursor"), "owned reader grant population changed"
    for row in page["items"]:
        assignment = row["assignment"]
        catalog.request("/api/v1/access/assignments/" + assignment["id"] + ":revoke", {"reason": "Qualify current native ADK authority"}, etag='"rev-' + str(assignment["revision"]) + '"')
    return len(page["items"])


def run_client(name, progress, catalog, *, interrupt=False):
    """Service one fixed revocation checkpoint while the native toolset stays alive."""
    docker("start", name, timeout=10)
    deadline, revoked, paused = time.monotonic() + 180, False, False
    while time.monotonic() < deadline:
        state = json.loads(docker("inspect", name, timeout=5).stdout)[0]["State"]
        if not state["Running"]:
            from common import Result
            logs = docker("logs", name, timeout=10, limit=2*1024*1024)
            return Result(state["ExitCode"], logs.stdout, logs.stderr), revoked
        checkpoint = progress / "revoke"
        if checkpoint.exists() and not (revoked or paused):
            assert checkpoint.read_bytes() == b"revoke-reader\n", "invalid ADK checkpoint"
            if interrupt:
                # A fixed cleanup calibration holds this existing checkpoint;
                # its controller sends SIGTERM to this supervisor, not ADK.
                (progress / "interrupt-ready").write_text("interrupt-ready\n")
                paused = True
            else:
                revoke_reader(catalog)
                (progress / "continue").write_text("revoked\n")
                revoked = True
        time.sleep(.05)
    raise RuntimeError("native ADK execution deadline")


def isolation(name, network, mounts):
    """Verify actual namespace, readonly inputs, limits and sole writable progress."""
    value = json.loads(docker("inspect", name, timeout=10).stdout)[0]
    host = value["HostConfig"]
    assert host["NetworkMode"] == network and host["ReadonlyRootfs"] and not host["Privileged"]
    assert "ALL" in host["CapDrop"] and "no-new-privileges" in host["SecurityOpt"]
    assert not host["PortBindings"] and not value["NetworkSettings"]["Ports"] and not host.get("Tmpfs")
    assert host["Memory"] == 512*1024*1024 and host["PidsLimit"] == 128
    actual = {m["Destination"]: (m["Source"], m["RW"]) for m in value["Mounts"]}
    assert actual == {target: (str(source), writable) for source, target, writable in mounts}, "ADK mount scope changed"
    assert all(m["Type"] == "bind" for m in value["Mounts"]), "ADK unexpected storage"
    return {"network": "none" if network == "none" else "owned-loopback", "externalEgress": False, "publishedPorts": [], "readOnlyRoot": True,
            "mounts": {target: {"writable": writable} for _, target, writable in mounts}}


def cleanup(names, private, report):
    """Independently verify absence after scoped removal, including interruption."""
    mask = signal.pthread_sigmask(signal.SIG_BLOCK, {signal.SIGINT, signal.SIGTERM})
    try:
        try:
            tools.remove(names)
            for name in names:
                found = docker("container", "ls", "--all", "--quiet", "--filter", "name=^/" + name + "$", timeout=10).stdout.strip()
                assert not found, "owned ADK container residue"
            # Read after removal, while the owned progress directory still exists.
            progress = private / "progress/cases.json"
            if progress.exists():
                with progress.open() as stream:
                    raw = stream.read(8193)
                assert len(raw) <= 8192, "ADK progress exceeds its bound"
                value = json.loads(raw)
                assert set(value) == {"phase", "cases"} and value["phase"] in CASES | {"setup", "complete"}, "invalid ADK progress phase"
                assert set(value["cases"]) == CASES and all(v in {"passed", "failed", "incomplete", "unrun"} for v in value["cases"].values()), "invalid ADK case outcomes"
                report.update(nativePhase=value["phase"], caseOutcomes=value["cases"])
        finally:
            shutil.rmtree(private)
        report["cleanup"] = not private.exists() and not report.get("creationUnconfirmed", False)
    finally:
        signal.pthread_sigmask(signal.SIG_SETMASK, mask)


def prerequisites():
    """Select the existing native host and installed tools without acquisition."""
    arch = local_daemon()
    if platform.system() != "Linux" or {"x86_64": "amd64", "aarch64": "arm64"}.get(platform.machine()) != arch:
        raise RuntimeError("ADK qualification requires a native matching Linux host/engine")
    return arch, tools.installed()


def check(control=None):
    """Run one native HTTP adapter campaign with an isolated real Catalog."""
    assert control in (None, "metadata-mismatch", "interrupt"), "unknown ADK cleanup control"
    arch, installed = prerequisites()
    tools.OUTPUT.mkdir(parents=True, exist_ok=True)
    directory = Path(tempfile.mkdtemp(prefix="run-", dir=tools.OUTPUT)).resolve()
    private = directory / "private"
    private.mkdir(mode=0o700)
    names, secrets = [], []
    source_paths = [*CATALOG_INPUTS, ROOT / "Makefile", tools.HERE]
    source = fingerprint(source_paths)
    report = {"scope": "adk-native-mcp-without-model", "source": source, "installation": installed, "containers": names,
              "phase": "setup", "control": control or "none", "success": False, "cleanup": False, "caseOutcomes": dict.fromkeys(sorted(CASES), "unrun")}
    print("ADK report: " + str(directory.relative_to(ROOT)), flush=True)

    def save():
        """Retain partial outcomes even after setup, execution or cleanup failure."""
        (directory / "report.json").write_text(json.dumps(report, indent=2) + "\n")

    def timeout(*_):
        raise TimeoutError("ADK qualification deadline")

    previous = signal.signal(signal.SIGALRM, timeout)
    signal.alarm(600)
    save()
    try:
        commands, tls, client, progress, files = (private / name for name in ("commands", "tls", "client", "progress", "catalog"))
        for path in (commands, tls, client, progress, files):
            path.mkdir(mode=0o700)
        report["binaries"] = build(commands, arch)
        certificates(commands / "fixture", tls)
        shutil.copyfile(tls / "ca.pem", client / "ca.pem")
        shutil.copyfile(tools.INPUTS / "client.py", client / "client.py")
        shutil.copyfile(tools.HERE / "adk_contract.py", client / "adk_contract.py")
        prefix = "catalog-adk-" + uuid.uuid4().hex[:12]
        primary, proxy, native = (prefix + suffix for suffix in ("-catalog", "-tls", "-client"))
        catalog = CatalogControl(files, primary)
        catalog.configuration()
        secrets.extend(catalog.tokens.values())
        config = json.loads((files / "config.json").read_text())
        config["mcp"]["enabled"] = True
        config["http"]["publicBaseUrl"] = "https://127.0.0.1:9555"
        assert config["semanticSearch"]["enabled"] is False, "ADK fixture enables embeddings"
        catalog.write("config.json", json.dumps(config))

        def create(name, network, mounts, *args):
            """Register ownership before creation and record effective confinement."""
            names.append(name)
            arguments = ["--network=" + network]
            for path, target, writable in mounts:
                arguments += ["--mount", f"type=bind,src={path},dst={target}" + ("" if writable else ",readonly")]
            result = tools.create(name, *arguments, *args)
            report.setdefault("isolation", {})[name.rsplit("-", 1)[-1]] = isolation(name, network, mounts)
            return result.stdout.strip()

        image = installed["image"]
        primary_id = create(primary, "none", [(commands, "/commands", False), (files, "/catalog", False)], "--entrypoint=/commands/catalogd", image, "all", "--config", "/catalog/config.json")
        docker("start", primary, timeout=10)
        wait_ready(primary)
        report["phase"] = "governance"
        record, snapshot = seed(catalog)
        report["selection"] = {"resource": record["resource"]["id"], "release": record["release"]["id"], "versionRevision": record["versionRevision"]["id"], "snapshot": snapshot}
        create(proxy, "container:" + primary_id, [(commands, "/commands", False), (tls, "/private", False)], "--entrypoint=/commands/fixture", image, "serve-control")
        docker("start", proxy, timeout=10)
        client_input = {"reader": catalog.tokens["local-reader"], "invalid": catalog.tokens["invalid"], "foreign": catalog.tokens["other-tenant-reader"], "record": record}
        if control == "metadata-mismatch":
            # Change only the disposable expected evidence, never Catalog state,
            # the native adapter, tool arguments/schemas or published document.
            client_input["record"] = {**record, "listingRevision": {**record["listingRevision"], "displayName": "Intentional metadata mismatch"}}
        (client / "input.json").write_text(json.dumps(client_input))
        (client / "input.json").chmod(0o600)
        create(native, "container:" + primary_id, [(client, "/client", False), (progress, "/progress", True)], image, "/client/client.py")
        report["phase"] = "native-adapter"
        save()
        result, revoked = run_client(native, progress, catalog, interrupt=control == "interrupt")
        report["revoked"] = revoked
        assert not any(secret in result.stdout + result.stderr for secret in secrets), "native ADK credential exposure"
        (directory / "native.json").write_text(json.dumps({"exit": result.code, "stdout": result.stdout, "stderr": result.stderr}, indent=2) + "\n")
        # Read through the still-running TLS fixture, never through a client report.
        observation = docker("exec", proxy, "python", "-B", "-c",
                             "import urllib.request,ssl; print(urllib.request.urlopen('https://127.0.0.1:9555/adk-observations',context=ssl.create_default_context(cafile='/private/ca.pem'),timeout=5).read().decode())", timeout=10)
        report["traffic"] = json.loads(observation.stdout)
        report["native"] = validate_result(result, secrets, {key: installed["sources"][key]["version"] for key in ("adk", "mcp")})
        assert revoked, "ADK revocation was not performed"
        validate_traffic(report["traffic"])
        assert source == fingerprint(source_paths), "ADK/Catalog inputs changed during execution"
        for name in (proxy, primary):
            docker("stop", "--time", "25", name, timeout=30)
            state = json.loads(docker("inspect", name, timeout=5).stdout)[0]["State"]
            assert not state["Running"] and state["ExitCode"] == 0, "owned ADK fixture did not drain"
        report["phase"] = "validated"
    except BaseException as error:
        report["error"] = type(error).__name__
        detail = str(error)
        for secret in secrets:
            detail = detail.replace(secret, "[fixture-credential]")
        report["detail"] = detail[-2000:]
        if isinstance(error, tools.CreationUnconfirmed):
            report["creationUnconfirmed"] = True
        raise
    finally:
        signal.alarm(0)
        signal.signal(signal.SIGALRM, previous)
        try:
            cleanup(names, private, report)
        finally:
            save()
    assert report["cleanup"] and report["phase"] == "validated" and all(v == "passed" for v in report["caseOutcomes"].values()), "ADK execution or case evidence incomplete"
    report.update(success=True, phase="complete")
    save()
    print("Passed 13 native ADK MCP groups without a model; owned cleanup passed.")


def main():
    """Keep acquisition separate and refuse optimized assertion-free execution."""
    if not __debug__:
        raise RuntimeError("ADK qualification requires Python assertions")
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("install", "check"))
    parser.add_argument("--control", choices=("metadata-mismatch", "interrupt"), help="Owned cleanup calibration; use make test-interoperability-adk-cleanup")
    args = parser.parse_args()
    if args.action != "check" and args.control:
        parser.error("--control is only supported with check")
    signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(KeyboardInterrupt()))
    # The cleanup controller masks signals while registering its child PID.
    signal.pthread_sigmask(signal.SIG_UNBLOCK, {signal.SIGINT, signal.SIGTERM})
    if args.action == "install":
        tools.install()
    else:
        check(args.control)


if __name__ == "__main__":
    try:
        main()
    except (AssertionError, RuntimeError, OSError, ValueError, KeyError, TypeError, KeyboardInterrupt) as error:
        # Only fixed prerequisite guidance belongs in terminal errors. Native or
        # transport diagnostics remain in the redacted invocation-owned report.
        hints = {"Run make adk-tools-install first (missing or stale tools)",
                 "ADK qualification requires Python assertions",
                 "Run ADK qualification as a non-root developer",
                 "ADK qualification requires a native matching Linux host/engine"}
        hint = error.args[0] if len(error.args) == 1 and isinstance(error.args[0], str) else ""
        print("ADK qualification failed: " + (hint if hint in hints else type(error).__name__), file=sys.stderr)
        sys.exit(2)
