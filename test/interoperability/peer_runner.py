"""Supervise only owned HTTPS HF peers and the real Catalog API/worker."""

import ipaddress
import json
import os
from pathlib import Path
import shutil
import signal
import sys
import tempfile
import time
import uuid

from client_contract import redact
from common import run
import runner as shared
from peer_contract import DISPOSITIONS, TENANT, check_cases, check_checker, check_phase, outcome, parse_routes, route_delta, search_validator, validate_routes, validate_search
from peer_trust import configuration, selected, validate_assessment
from peer_wire import PeerHTTP, checker_evidence, qualify

PROFILE = shared.ROOT / "schemas/upstream/ard/ard-0.91-aa3e598"
CHECKER = shared.ROOT / "test/packaging/ard_conformance.py"
INPUTS = (shared.HERE, shared.ROOT / "Makefile", CHECKER, shared.ROOT / "pyproject.toml", shared.ROOT / "uv.lock", PROFILE)


def preflight(image):
    """Existing installations must provide both unchanged TLS server factories."""
    run([sys.executable, "-B", str(CHECKER), "check"], timeout=30)
    version = run(["openssl", "version"], timeout=10).stdout.strip()
    return {"openssl": version, "python": sys.version.split()[0], "uvicorn_tls": image_preflight(image)}


def image_preflight(image):
    """Qualify the existing lock's factory/TLS capabilities without host schema tools."""
    code = ("import ssl,uvicorn; from discover.challenge import create_challenge_app; "
            "from discover.server import create_app; "
            "assert callable(create_challenge_app) and callable(create_app); "
            "assert 'ssl_certfile' in __import__('inspect').signature(uvicorn.Config).parameters; "
            "print(uvicorn.__version__, ssl.OPENSSL_VERSION)")
    result = shared.installation_probe(image, "python", ["-B", "-c", code])
    assert result.stdout.strip(), "missing Uvicorn TLS capability evidence"
    return result.stdout.strip()


def addresses(network):
    """Reserve explicit private addresses on the new, empty invocation-owned bridge."""
    info = json.loads(shared.docker("network", "inspect", network).stdout)[0]
    if not info["Internal"] or info.get("Containers"):
        raise RuntimeError("peer bridge is not empty and internal")
    subnet = ipaddress.ip_network(info["IPAM"]["Config"][0]["Subnet"])
    if subnet.version != 4 or not subnet.is_private or subnet.num_addresses < 16:
        raise RuntimeError("peer bridge requires a private IPv4 subnet")
    return [str(subnet.network_address + offset) for offset in (10, 11)]


def certificates(private, ips):
    """Keep private keys owner-readable only, including partial generation failures."""
    private.mkdir(mode=0o700)
    for label in ("peer", "untrusted"):
        key, cert = private / (label + ".key"), private / (label + ".pem")
        run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", str(key),
             "-out", str(cert), "-days", "2", "-subj", "/CN=Owned HF qualification",
             "-addext", "subjectAltName=" + ",".join("IP:" + ip for ip in ips),
             "-addext", "basicConstraints=critical,CA:TRUE"], timeout=30)
        key.chmod(0o600)
        cert.chmod(0o444)


def policy(origins, ips):
    common = dict(origins=list(origins.values()), addressRanges=[ip + "/32" for ip in ips],
                  assignedTenantIds=[TENANT], transportRef="direct")
    return dict(schemaVersion=1, policyId="owned-hf", revision=1, public={"mode": "disabled"},
                privateProfiles=[dict(common, name="hf-owned", trustBundleFile="/peer.pem"),
                                 dict(common, name="hf-untrusted", trustBundleFile="/untrusted.pem")])


def calibrate_search(validator):
    """Prove real schema and envelope rejection before accepting peer evidence."""
    entry = {"identifier": "urn:air:discover.dev:challenge:probe", "score": 8e1, "source": "discover:challenge"}
    validate_search({"results": [entry]}, validator)
    negatives = [{"results": [{**entry, **changed}]} for changed in (
        {"identifier": "not-air"}, {"displayName": 3}, {"tags": "wrong"},
        {"score": True}, {"score": 1.5}, {"source": "bad URI:"})]
    negatives.extend([{"results": [], "unknown": True}, {"results": {}},
                      {"results": [entry], "referrals": [{"identifier": "missing-other-fields"}]}])
    for document in negatives:
        try:
            validate_search(document, validator)
        except AssertionError:
            continue
        raise RuntimeError("peer schema calibration accepted an invalid response")
    return {"valid": 1, "invalid": len(negatives)}


def deadline_exceeded(_signum, _frame):
    """Interrupt bounded children while preserving the supervisor cleanup path."""
    raise RuntimeError("peer qualification overall deadline exceeded")


def check():
    """Run the fixed peer inventory; clean every owned resource on all exits."""
    trust_enabled = selected()
    if os.getuid() == 0:
        raise RuntimeError("peer qualification requires a non-root host user for private-key ownership")
    arch = shared.local_daemon()
    stamp = shared.installed_image()
    tools = preflight(stamp["image"])
    schema = json.loads((PROFILE / "spec/schemas/ard-entry.schema.json").read_text())
    validator = search_validator(schema)
    tools["schema_calibrations"] = calibrate_search(validator)
    catalog_before = shared.fingerprint(shared.CATALOG_INPUTS)
    harness_before = shared.fingerprint(INPUTS)
    shared.OUTPUT.mkdir(parents=True, exist_ok=True)
    directory = Path(tempfile.mkdtemp(prefix="peer-", dir=shared.OUTPUT))
    directory.chmod(0o755)
    build, private = directory / "build", directory / "private"
    build.mkdir()
    name = "open-agentic-catalog-hf-peer-" + uuid.uuid4().hex[:12]
    network, server, image = name + "-net", name + "-catalog", name + ":fixture"
    peers = {label: name + "-" + label for label in ("challenge", "primary")}
    clients = []
    cases = []
    report = dict(status="setup-failed", started_utc=time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                  catalog_commit=run(["git", "rev-parse", "HEAD"]).stdout.strip(),
                  catalog_source_sha256=catalog_before, harness_sha256=harness_before, client=stamp,
                  tools=tools, cases=cases, trust_verification=trust_enabled, owned=dict(network=network, catalog=server, peers=peers, image=image, clients=clients))

    def record(case_name, callback):
        try:
            evidence = callback()
            cases.append(outcome(case_name, evidence))
        except (AssertionError, RuntimeError, OSError, ValueError, KeyError, TypeError) as error:
            cases.append(dict(name=case_name, status="fail", disposition=DISPOSITIONS[case_name], evidence={"error": redact(str(error))[:6000]}))

    def checkpoint(label):
        print("HF peer phase: " + label, flush=True)
        (directory / "phase.json").write_text(json.dumps({"phase": label, "owned": report["owned"]}))

    def phase(label, state):
        client = name + "-" + label
        clients.append(client)
        checkpoint(label)
        result = shared.docker("run", *shared.safety_args(client), "--network", "container:" + server,
                               "--mount", "type=bind,source=" + str(shared.HERE) + ",target=/harness,readonly",
                               "--entrypoint=python", stamp["image"], "-B", "/harness/peer_catalog.py", label,
                               json.dumps(state), timeout=180, check=False)
        if result.stdout != redact(result.stdout):
            raise RuntimeError("peer phase emitted a synthetic credential")
        if not result.stdout.strip():
            raise RuntimeError(label + ": missing phase report: " + redact(result.stderr)[:4000])
        payload = json.loads(result.stdout)
        check_phase(label, payload, result.code)
        cases.extend(payload["cases"])
        if result.code or "error" in payload:
            raise RuntimeError(label + ": " + str(payload.get("error", "phase process failed")))
        return payload["state"]

    def routes():
        logs = shared.docker("logs", peers["challenge"], limit=2 * 1024 * 1024)
        return parse_routes(logs.stdout)

    previous_alarm = signal.signal(signal.SIGALRM, deadline_exceeded)
    signal.setitimer(signal.ITIMER_REAL, 1200)
    try:
        checkpoint("setup")
        shared.docker("network", "create", "--internal", network)
        ips = addresses(network)
        origins = dict(zip(peers, ("https://" + ip + ":8443" for ip in ips), strict=True))
        report["origins"] = origins
        certificates(private, ips)
        for (peer, container), ip, factory in zip(peers.items(), ips, ("discover.challenge:create_challenge_app", "discover.server:create_app"), strict=True):
            flags = [flag for flag in shared.safety_args(container) if not flag.startswith(("--user=", "--log-driver="))]
            shared.docker("run", "-d", *flags, f"--user={os.getuid()}:{os.getgid()}",
                          "--log-driver=json-file", "--log-opt=max-size=1m", "--log-opt=max-file=2",
                          "--network", network, "--ip", ip,
                          "--mount", f"type=bind,source={private / 'peer.key'},target=/key.pem,readonly",
                          "--mount", f"type=bind,source={private / 'peer.pem'},target=/cert.pem,readonly",
                          "--entrypoint=python", stamp["image"], "-B", "-m", "uvicorn", factory, "--factory",
                          "--host", "0.0.0.0", "--port", "8443", "--ssl-keyfile", "/key.pem", "--ssl-certfile", "/cert.pem")
            actual = shared.docker("inspect", "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", container).stdout.strip()
            assert actual == ip, "peer address reservation changed"
        http = PeerHTTP(origins, str(private / "peer.pem"))
        http.ready()
        manifests = qualify(http, validator, record)
        for peer, origin in origins.items():
            for mode in ("manifest", "publisher", "registry"):
                def checker(peer=peer, origin=origin, mode=mode):
                    if mode == "manifest":
                        path = directory / (peer + "-manifest.json")
                        path.write_text(json.dumps(manifests[peer]))
                        target = str(path)
                    else:
                        target = origin
                    environment = dict(os.environ, SSL_CERT_FILE=str(private / "peer.pem"))
                    for key in ("HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy"):
                        environment[key] = ""
                    result = run([sys.executable, "-B", str(CHECKER), mode, target], env=environment,
                                 timeout=75, limit=2 * 1024 * 1024, check=False)
                    evidence = checker_evidence(result)
                    (directory / f"checker-{peer}-{mode}.json").write_text(json.dumps(evidence, indent=2))
                    check_checker(peer, mode, evidence)
                    return evidence
                record(f"checker-{peer}-{mode}", checker)
        for cap, label, count in ((1, "one", 6), (5, "five", 13)):
            def navigation(cap=cap, label=label, count=count):
                client = name + "-navigate-" + label
                clients.append(client)
                checkpoint("navigation-" + label)
                result = shared.docker("run", *shared.safety_args(client), "--network", network,
                                       "--mount", f"type=bind,source={private / 'peer.pem'},target=/cert.pem,readonly",
                                       "--env=SSL_CERT_FILE=/cert.pem", stamp["image"], "navigate", origins["challenge"],
                                       "challenge", "--follow-referrals", "--max-registries", str(cap), "--max-depth", "2",
                                       "--limit", "20", "--timeout", "2", "--json", timeout=40)
                payload = json.loads(result.stdout)
                assert all(v["status"] == "ok" for v in payload["discovered"]), "HF navigation failed discovery"
                visits = [v for v in payload["discovered"] if v["kind"] == "registry"]
                assert len(visits) == cap and len(payload["results"]) == count
                assert len({v["url"] for v in visits}) == cap
                assert all(v["url"].startswith(origins["challenge"] + "/") for v in payload["discovered"])
                assert len({e["identifier"] for e in payload["results"]}) == count
                return dict(registries=cap, results=count, discovered=payload["discovered"])
            record("navigation-" + label, navigation)
        config = shared.configuration()
        config["federation"]["policyFile"] = "/policy.json"
        policy_path = directory / "policy.json"
        policy_path.write_text(json.dumps(policy(origins, ips)))
        if trust_enabled:
            config["trustVerification"] = configuration(origins["challenge"], "discover.dev", "hf-owned", "/trust")
            config["worker"]["leaseDuration"] = "30s"
        shared.build_catalog(build, stamp, arch, config, image)
        report["catalog_image_id"] = shared.docker("image", "inspect", "--format", "{{.Id}}", image).stdout.strip()
        mounts = []
        for path, target in ((policy_path, "/policy.json"), (private / "peer.pem", "/peer.pem"), (private / "untrusted.pem", "/untrusted.pem")):
            mounts.extend(["--mount", f"type=bind,source={path},target={target},readonly"])
        if trust_enabled:
            public_keys = build / "trust"
            public_keys.mkdir(mode=0o755)
            for key in ("ra.pem", "tl.pem"):
                (public_keys / key).write_bytes((shared.ROOT / "internal/standards/anstrust/testdata/native" / key).read_bytes())
                (public_keys / key).chmod(0o444)
            mounts.extend(["--mount", f"type=bind,source={public_keys},target=/trust,readonly"])
            probe = name + "-config"
            clients.append(probe)
            shared.docker("run", "--rm", *shared.safety_args(probe), "--network=none", *mounts,
                          "--entrypoint=/catalogctl", image, "config", "validate", "--config", "/config.json", "--role", "all")
        shared.docker("run", "-d", *shared.safety_args(server), "--network", network, *mounts, image, "all", "--config", "/config.json")
        for _ in range(60):
            if shared.docker("exec", server, "/catalogctl", "probe", "--url", "http://127.0.0.1:8080/readyz", check=False).code == 0:
                break
            time.sleep(0.25)
        else:
            raise RuntimeError("owned Catalog readiness failed")
        if trust_enabled:
            loaded = json.loads(shared.docker("exec", server, "/catalogctl", "config", "validate", "--config", "/config.json", "--role", "all").stdout)
            report["loaded_trust_configuration"] = loaded["configuration"]["trustVerification"]
            assert report["loaded_trust_configuration"]["enabled"] is True and report["loaded_trust_configuration"]["policyCount"] == 1
        smoke = shared.docker("exec", server, "/catalogctl", "smoke", "--run-id", "hf-interop", timeout=180)
        (directory / "smoke.json").write_text(smoke.stdout)
        state = phase("bootstrap", {"origins": origins})
        for label in ("live", "public"):
            before = routes()
            state = phase(label, state)
            delta = route_delta(before, routes())
            def contacts(delta=delta):
                validate_routes(delta)
                return delta
            record(label + "-routes", contacts)
        shared.docker("stop", "--time", "5", peers["challenge"], timeout=15)
        state = phase("outage", state)
        shared.docker("start", peers["challenge"])
        http.ready()
        state = phase("recovery", state)
        report["recovery"] = state
        if trust_enabled:
            # Existing Uvicorn access logs cover both owned origins, including
            # rejected paths. Known manifest traffic calibrates log collection.
            for peer in peers.values():
                logs = shared.docker("logs", peer, limit=2 * 1024 * 1024)
                wire = logs.stdout + logs.stderr
                assert 'GET /.well-known/ai-catalog.json ' in wire
                assert not any(route in wire for route in (" /checkpoint ", "/receipt ", "/status-token ", "/v1/log/checkpoint/history"))
            state = phase("trust", state)
            report["trust"] = state["trust"]
            validate_assessment(report["trust"]["assessment"], report["trust"]["source_id"], report["trust"]["entry_digest"])
            for peer in peers.values():
                logs = shared.docker("logs", peer, limit=2 * 1024 * 1024)
                assert not any(route in logs.stdout + logs.stderr for route in (" /checkpoint ", "/receipt ", "/status-token ", "/v1/log/checkpoint/history"))
            report["trust"]["evidenceRequests"] = 0
        report["status"] = "passed"
    except KeyboardInterrupt:
        report.update(status="interrupted", error="peer qualification interrupted")
    except (AssertionError, RuntimeError, OSError, ValueError, KeyError, TypeError) as error:
        report.update(status="qualification-failed", error=redact(str(error))[:6000])
    finally:
        signal.setitimer(signal.ITIMER_REAL, 0)
        signal.signal(signal.SIGALRM, previous_alarm)
        cleanup_errors = []
        for args in [*(("rm", "-f", container) for container in [*clients, server, *peers.values()]),
                     ("network", "rm", network), ("image", "rm", image)]:
            try:
                result = shared.docker(*args, check=False)
                if result.code and not any(term in result.stderr.lower() for term in ("no such", "not found")):
                    cleanup_errors.append(" ".join(args))
            except (RuntimeError, OSError) as error:
                cleanup_errors.append(str(error))
        for path in (private, build):
            try:
                if path.exists():
                    shutil.rmtree(path)
            except OSError as error:
                cleanup_errors.append(str(error))
        report["cleanup_errors"] = cleanup_errors
        try:
            # Report validation must never precede mandatory resource cleanup.
            for case_name in DISPOSITIONS.keys() - {case["name"] for case in cases}:
                cases.append(dict(name=case_name, status="fail", disposition=DISPOSITIONS[case_name], evidence={"error": "phase not completed"}))
            report["passed"] = check_cases(cases)
            report["total"] = len(DISPOSITIONS)
            assert catalog_before == shared.fingerprint(shared.CATALOG_INPUTS) and harness_before == shared.fingerprint(INPUTS), "qualification inputs changed during run"
            if report["status"] == "passed" and report["passed"] != report["total"]:
                report["status"] = "qualification-failed"
        except (AssertionError, ValueError, OSError, KeyError, TypeError) as error:
            report.update(status="invalid-evidence", error=str(error))
        if cleanup_errors:
            report["status"] = "cleanup-failed"
        (directory / "result.json").write_text(redact(json.dumps(report, indent=2)) + "\n")
    print("Report: " + str(directory / "result.json"))
    print("Status: " + report["status"])
    return int(report["status"] != "passed")
