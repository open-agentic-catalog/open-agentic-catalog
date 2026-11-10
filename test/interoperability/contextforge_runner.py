"""Own one isolated Compose fixture for both ContextForge integration flows."""

import base64
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import re
import shutil
import signal
import subprocess
import sys
import traceback
import uuid

from common import run
from runner import ROOT, CATALOG_INPUTS, cached_go, docker, fingerprint, local_daemon
import contextforge_tools as tools
import mcp_catalog_tools
import mcp_tools
from catalog_control import validate_search_record
from contextforge.handoff_checks import TAG, classify_prompt_failure, endpoint, select_record, validate_record, validate_runtime

GUIDE = ROOT / "documentation/src/content/docs/guides/contextforge.mdx"
SOURCE = (tools.INPUTS, Path(__file__), ROOT / "Makefile", ROOT / "VERSION", ROOT / "config/postgres.json",
          ROOT / "test/packaging/versioning.py", ROOT / "test/functional/fixture", ROOT / "test/functional/Containerfile",
          ROOT / "Containerfile", ROOT / "deployments/postgres/init.sql",
          ROOT / "catalog-ui/scripts/contextforge-journeys.mjs", ROOT / "catalog-ui/.nvmrc", ROOT / "catalog-ui/package-lock.json", GUIDE,
          *(Path(__file__).parent / name for name in ("common.py", "runner.py", "mcp_tools.py", "mcp_catalog_tools.py", "catalog_control.py")), *CATALOG_INPUTS)
TENANTS = {"a": "00000000-0000-7000-8000-000000000001", "b": "00000000-0000-7000-8000-000000000002"}
JWT_PATTERN = re.compile(r"eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+")


def environment():
    """Do not inherit workflow secrets, Compose selectors or injected proxies."""
    keep = ("PATH", "HOME", "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG", "SSH_AUTH_SOCK", "GOCACHE", "GOMODCACHE")
    return {k: os.environ[k] for k in keep if k in os.environ} | {"CATALOG_ENV_FILE": "/dev/null", "DOCKER_CLI_HINTS": "false"}


def owner(directory):
    directory = Path(directory)
    if directory.is_symlink() or directory.parent.resolve() != tools.OUTPUT.resolve():
        raise ValueError("select an owned ContextForge run directory")
    if not re.fullmatch(r"catalog-contextforge-[0-9a-f]{12}", directory.name):
        raise ValueError("invalid ContextForge project name")
    state = json.loads((directory / "owner.json").read_text())
    if state.get("project") != directory.name or state.get("directory") != str(directory.resolve()):
        raise ValueError("ContextForge ownership marker does not match")
    return state


def compose_args(directory, *arguments):
    state = owner(directory)
    return ["docker", "compose", "--project-directory", str(directory / "private"), "--env-file", str(directory / "compose.env"),
            "--project-name", state["project"], "--file", str(tools.INPUTS / "compose.yaml"), *arguments]


def compose(directory, *arguments, timeout=120, check=True):
    return run(compose_args(directory, *arguments), env=environment(), timeout=timeout, limit=4*1024*1024, check=check)


def json_file(path, value):
    path.write_text(json.dumps(value, indent=2) + "\n")
    path.chmod(0o600)


def update_env(directory, **changes):
    values = json.loads((directory / "environment.json").read_text())
    values.update(changes)
    json_file(directory / "environment.json", values)
    text = "".join(k + "=" + str(v) + "\n" for k, v in values.items())
    (directory / "compose.env").write_text(text)
    (directory / "compose.env").chmod(0o600)


def private_permissions(private):
    for path in private.rglob("*"):
        if path.is_dir():
            path.chmod(0o750)
        else:
            path.chmod(0o640)
    (private / "secrets.json").chmod(0o600)
    (private / "login.json").chmod(0o600)
    for name in ("state", "scratch"):
        (private / name).chmod(0o2770)


def prepare():
    assert local_daemon() == "amd64", "ContextForge native execution requires Linux AMD64"
    cf, client, everything = tools.installed(), mcp_catalog_tools.installed(), mcp_tools.installed()
    tools.OUTPUT.mkdir(parents=True, exist_ok=True)
    project = "catalog-contextforge-" + uuid.uuid4().hex[:12]
    directory = tools.OUTPUT / project
    directory.mkdir(mode=0o700)
    private = directory / "private"
    private.mkdir(mode=0o700)
    retained = docker("ps", "-q").stdout.split()
    state = dict(project=project, directory=str(directory.resolve()), source=fingerprint(SOURCE), retained=retained,
                 scope="contextforge-native-catalog-proxy-and-governed-handoff", phase="prepared", success=False, cleanup=False,
                 installation=cf, clients={"pythonMCP": client, "everything": everything}, cases={})
    json_file(directory / "owner.json", state)
    build = directory / "build"
    build.mkdir(mode=0o700)
    try:
        go = cached_go()
        run([go, "build", "-trimpath", "-o", str(build / "fixture"), "./test/functional/fixture"],
                     env=environment() | {"GOPROXY": "off", "GOSUMDB": "off", "GOTOOLCHAIN": "local", "GOTELEMETRY": "off"}, timeout=180)
        run([str(build / "fixture"), "contextforge-prepare", "--directory", str(private)], timeout=20)
        config = json.loads((ROOT / "config/postgres.json").read_text())
        config["auth"] = dict(mode="oidc", issuer="https://federation-fixture:8443", audience="open-agentic-catalog", tenancyMode="shared", organizationClaim="organization_id", enforceScopes=True, trustBundleFile="/private/ca.pem")
        config["http"]["address"] = "0.0.0.0:8080"
        config["http"]["publicBaseUrl"] = "https://federation-fixture:8443"
        config["worker"]["operationsAddress"] = "0.0.0.0:9090"
        config["mcp"]["enabled"] = True
        config["webhooks"]["enabled"] = False
        config["federation"]["policyFile"] = "/private/policy.json"
        config["database"]["dsn"] = "postgres://catalog_api@postgres:5432/catalog?sslmode=disable"
        json_file(private / "backend/config.json", config)
        json_file(private / "backend/policy.json", dict(schemaVersion=1, policyId="contextforge-owned", revision=1, public={"mode": "disabled"},
                  privateProfiles=[dict(name="contextforge-owned", origins=["https://federation-fixture:8443"], addressRanges=["10.0.0.1/32"],
                                        assignedTenantIds=list(TENANTS.values()), transportRef="direct", trustBundleFile="/private/ca.pem")]))
        (private / "backend/cursor-key").write_text(base64.b64encode(os.urandom(32)).decode())
        secrets = json.loads((private / "secrets.json").read_text())
        json_file(private / "login.json", {"email": "admin@example.com", "password": secrets["cfPassword"]})
        (private / "catalog-client").mkdir(mode=0o700)
        shutil.copyfile(private / "frontend/ca.pem", private / "catalog-client/ca.pem")
        for name, token in (("reader", secrets["tokens"]["a-reader"]), ("other-tenant", secrets["tokens"]["b-reader"]), ("invalid", secrets["tokens"]["a-audience"]), ("missing", "")):
            (private / "catalog-client" / (name + "-token")).write_text(token)
        cfenv = dict(JWT_SECRET_KEY=secrets["cfJWT"], AUTH_ENCRYPTION_SECRET=secrets["cfEncryption"], PLATFORM_ADMIN_EMAIL="admin@example.com",
                     PLATFORM_ADMIN_PASSWORD=secrets["cfPassword"], DEFAULT_USER_PASSWORD=secrets["cfDefaultPassword"])
        (private / "contextforge.env").write_text("".join(k + "=" + v + "\n" for k, v in cfenv.items()))
        private_permissions(private)
        (private / "client-state").mkdir(mode=0o700)
        # Current packaged images, without changing the retained development tags.
        catalog_image, fixture_image = project + ":backend", project + ":fixture"
        result = run(["make", "container-build", "IMAGE=" + catalog_image, "CATALOG_ENV_FILE=/dev/null"], env=environment(), timeout=900, limit=16*1024*1024, check=False)
        (directory / "catalog-build.log").write_text(result.stdout + result.stderr)
        assert result.code == 0, "Catalog image build failed; inspect its build log"
        result = docker("build", "--network=none", "--progress=plain", "-f", str(ROOT / "test/functional/Containerfile"), "-t", fixture_image, str(ROOT), timeout=300, limit=8*1024*1024, check=False)
        (directory / "fixture-build.log").write_text(result.stdout + result.stderr)
        assert result.code == 0, "fixture image build failed; inspect its build log"
        state["builds"] = {}
        for name, reference in (("catalog", catalog_image), ("fixture", fixture_image)):
            image = json.loads(docker("image", "inspect", reference).stdout)[0]
            state["builds"][name] = {"id": image["Id"], "reference": reference, "labels": image["Config"].get("Labels") or {}}
        pg = "pgvector/pgvector:pg18@sha256:2358fcba361ed2233a5ed81b5fe4ca779ccb304120ce531a3bf51c0ed7e2bc11"
        docker("image", "inspect", pg, timeout=10)
        values = dict(PRIVATE=str(private), REPOSITORY=str(ROOT), FIXTURE_UID=os.getuid(), FIXTURE_GID=os.getgid(), CATALOG_IMAGE=catalog_image,
                      FIXTURE_IMAGE=fixture_image, POSTGRES_IMAGE=pg, CONTEXTFORGE_IMAGE=cf["reference"], CLIENT_IMAGE=client["image"],
                      EVERYTHING_IMAGE=everything["image"], UPSTREAM_CIDRS='["10.0.0.1/32"]')
        json_file(directory / "environment.json", values)
        update_env(directory)
        compose(directory, "config", "--quiet", timeout=15)
        json_file(directory / "owner.json", state)
        json_file(directory / "report.json", state)
        return directory
    except BaseException as error:
        shutil.rmtree(private)
        state.update(phase="prepare-failed", errorType=type(error).__name__, privateRemoved=True, cleanup=True)
        json_file(directory / "owner.json", state)
        json_file(directory / "report.json", state)
        print("ContextForge preparation report: " + str(directory / "report.json"), file=sys.stderr)
        raise


def check_client_output(directory, result):
    secrets = (directory / "private/secrets.json").read_text()
    raw_secrets = json.loads(secrets)
    values = list(raw_secrets["tokens"].values()) + [v for v in raw_secrets.values() if isinstance(v, str)]
    assert not any(v and v in result.stdout + result.stderr for v in values), "client output contains a credential"
    assert not JWT_PATTERN.search(result.stdout + result.stderr), "client output contains a peer credential"


def client_command(directory, action, timeout=90):
    result = compose(directory, "exec", "--no-TTY", "client", "python", "-B", "/code/client.py", action, timeout=timeout, check=False)
    check_client_output(directory, result)
    if result.code:
        (directory / "client-error.log").write_text(result.stderr[-3000:])
        raise RuntimeError("ContextForge client stage failed: " + action + "; inspect client-error.log")
    return json.loads(result.stdout)


def guide_source(text):
    blocks = re.findall(r"^```python\n(.*?)^```$", text, re.MULTILINE | re.DOTALL)
    assert len(blocks) == 1, "the setup guide must contain one explicit Python client example"
    compile(blocks[0], str(GUIDE), "exec")
    return blocks[0]


def guide_echo(directory):
    """Execute the actual manual example through the already selected runtime."""
    path = directory / "private/client-state/guide-example.py"
    path.write_text(guide_source(GUIDE.read_text()))
    path.chmod(0o600)
    result = compose(directory, "exec", "--no-TTY", "client", "python", "-B", "/state/guide-example.py", timeout=30, check=False)
    check_client_output(directory, result)
    assert result.code == 0 and result.stdout.strip() == "Echo: Hello", "the documented SDK example did not return its exact echo"
    return {"echo": True}


def start(directory):
    state = owner(directory)
    assert state["phase"] == "prepared", "start only a newly prepared fixture"
    compose(directory, "up", "-d", "--pull", "never", "--no-build", "--wait", "--wait-timeout", "60", "postgres", "federation-fixture", "everything", "client", timeout=90)
    client_command(directory, "frontend")
    frontend = compose(directory, "ps", "-q", "federation-fixture").stdout.strip()
    info = json.loads(docker("inspect", frontend).stdout)[0]
    assert set(info["NetworkSettings"]["Networks"]) == {state["project"] + "_owned", state["project"] + "_admin"}, "frontend has unexpected network attachments"
    ip = info["NetworkSettings"]["Networks"][state["project"] + "_owned"]["IPAddress"]
    policy_path = directory / "private/backend/policy.json"
    policy = json.loads(policy_path.read_text())
    policy["privateProfiles"][0]["addressRanges"] = [ip + "/32"]
    json_file(policy_path, policy)
    policy_path.chmod(0o640)
    update_env(directory, UPSTREAM_CIDRS=json.dumps([ip + "/32"]))
    compose(directory, "run", "--rm", "--no-deps", "owner", "migrate", "apply", "--config", "/private/config.json", "--api-role", "catalog_api", "--worker-role", "catalog_worker", timeout=90)
    for org, tenant in TENANTS.items():
        compose(directory, "run", "--rm", "--no-deps", "owner", "identity", "provision", "--config", "/private/config.json",
                "--tenant", tenant, "--tenant-name", "ContextForge tenant " + org, "--issuer", "https://federation-fixture:8443",
                "--tenancy-mode", "shared", "--external-organization", "org-" + org, "--subject", org + "-admin",
                "--operator", "contextforge-fixture", "--reason", "Owned ContextForge native control", timeout=45)
    compose(directory, "up", "-d", "--pull", "never", "--no-build", "--wait", "--wait-timeout", "60", "api", "worker", timeout=90)
    state["identities"] = client_command(directory, "identities")
    compose(directory, "up", "-d", "--pull", "never", "--no-build", "--wait", "--wait-timeout", "120", "contextforge", timeout=150)
    for service in ("api", "worker", "contextforge", "federation-fixture", "client", "everything", "postgres"):
        cid = compose(directory, "ps", "-q", service).stdout.strip()
        data = json.loads(docker("inspect", cid).stdout)[0]
        networks = data["NetworkSettings"]["Networks"]
        expected_networks = {state["project"] + "_owned"}
        if service == "federation-fixture":
            expected_networks.add(state["project"] + "_admin")
        assert set(networks) == expected_networks, "fixture has an unexpected network attachment"
        assert data["HostConfig"]["NetworkMode"] != "host" and not data["HostConfig"]["Privileged"], "service runs privileged or with host networking: " + service
        bindings = data["HostConfig"]["PortBindings"] or {}
        assert all(binding["HostIp"] == "127.0.0.1" for values in bindings.values() for binding in values or []), "non-loopback port: " + service
        assert service == "federation-fixture" or not any(bindings.values()), "unexpected published port: " + service
        net = json.loads(docker("network", "inspect", state["project"] + "_owned").stdout)[0]
        assert net["Internal"], "fixture permits external egress"
        if service == "contextforge":
            assert data["Config"]["User"] == "10001:10001", "ContextForge native runtime user changed"
    state["phase"] = "started"
    json_file(directory / "owner.json", state)
    connection_info(directory)
    json_file(directory / "report.json", state)


def validate_native(result):
    assert set(result) == {"catalog", "everything", "contextforge", "refusals"}, "incomplete native case groups"
    assert len(result["catalog"]["tools"]) == 6 and set(result["catalog"]["tools"]) == {"catalog_control_" + x for x in ("capabilities", "search", "release", "indexed_record", "explore", "access")}
    assert result["catalog"]["protocol"] == "2026-07-28" and result["catalog"]["capabilitiesAndAccess"] is True
    everything = result["everything"]
    assert everything["protocol"] == "2025-11-25" and everything["version"] == "2.0.0"
    assert all(everything[x] is True for x in ("echo", "resource", "prompt")), "missing direct Everything control"
    assert everything["resourceSha256"] == "36593c6d475378b29c6c43a3256fbfd2cad7b087dcbd3e940d53fa0876a70cd7", "native resource bytes differ"
    peer = result["contextforge"]
    assert peer["version"] == tools.pins()["version"]
    assert all(peer[x] is True for x in ("adminUI", "nativeTeamRolesAndServerScopedTokens", "scopedRuntimeReadAndWriteDenial"))
    assert peer["nativeRegistration"] == {"a": {"tools": 6, "resources": 0, "prompts": 0}, "b": {"tools": 6, "resources": 0, "prompts": 0},
                                          "everything": {"tools": 1, "resources": 1, "prompts": 1}}
    assert result["refusals"] == dict.fromkeys(("missingCatalogToken", "missingEverythingKey", "missingContextForgeToken", "untrustedCA", "invalidContextForgeToken", "httpFailureDiagnostic"), True)


def native(directory):
    state = owner(directory)
    assert state["phase"] == "started", "start the owned fixture first"
    result = client_command(directory, "native", timeout=180)
    validate_native(result)
    state["cases"] = result
    state["phase"] = "native-controls-complete"
    state["success"] = False
    assert fingerprint(SOURCE) == state["source"], "source inputs changed during native qualification"
    for image in state["builds"].values():
        assert docker("image", "inspect", image["reference"], "--format", "{{.Id}}").stdout.strip() == image["id"], "test image changed during qualification"
    json_file(directory / "owner.json", state)
    json_file(directory / "report.json", state)


def connection_info(directory):
    """Return URLs and private file paths, never credential values."""
    state = owner(directory)
    published = compose(directory, "port", "federation-fixture", "8443", timeout=10).stdout.strip()
    assert re.fullmatch(r"127\.0\.0\.1:[0-9]{1,5}", published), "ContextForge must publish only a loopback port"
    assert 1 <= int(published.rsplit(":", 1)[1]) <= 65535, "invalid ContextForge loopback port"
    result = {"project": state["project"], "directory": str(directory), "phase": state["phase"],
              "adminUrl": "https://" + published + "/v1/admin/", "caFile": str(directory / "private/frontend/ca.pem"),
              "loginFile": str(directory / "private/login.json")}
    path = directory / "private/client-state/contextforge-connections.json"
    if path.exists():
        connections = json.loads(path.read_text())["connections"]
        result["servers"] = {label: {"id": entry["server"], "url": endpoint(entry["server"]),
                                     "headerFile": str(directory / "private/headers" / (label + ".header"))} for label, entry in connections.items()}
    return result


def register(directory):
    native(directory)
    private = directory / "private"
    connections = json.loads((private / "client-state/contextforge-connections.json").read_text())
    headers = private / "headers"
    headers.mkdir(mode=0o700)
    for label, token in {"admin": connections["admin"], **{k: v["token"] for k, v in connections["connections"].items()}}.items():
        assert token and token.isascii() and not any(c.isspace() for c in token), "single private bearer value"
        path = headers / (label + ".header")
        path.write_text("Authorization: Bearer " + token + "\n")
        path.chmod(0o600)


def browser(directory):
    """Explicit browser selection uses the already installed UI tools."""
    version = run(["node", "--version"], env=environment(), timeout=5).stdout.strip()
    assert version == "v" + (ROOT / "catalog-ui/.nvmrc").read_text().strip(), "select the UI's pinned Node version first"
    info = connection_info(directory)
    result = run(["node", str(ROOT / "catalog-ui/scripts/contextforge-journeys.mjs"), str(directory), info["adminUrl"]], env=environment(), timeout=120, check=False)
    assert result.code == 0, "ContextForge browser check failed; inspect browser.json"
    outcome = json.loads(result.stdout)
    assert outcome == dict.fromkeys(("login", "servers", "gateways", "logout", "localRequestsOnly"), True), "incomplete browser checks"
    state = owner(directory)
    state["browser"] = outcome
    json_file(directory / "owner.json", state)
    json_file(directory / "report.json", state)


def proxy(directory):
    state = owner(directory)
    state.update(success=False, phase="catalog-proxy")
    json_file(directory / "owner.json", state)
    state["dataset"] = client_command(directory, "dataset", timeout=120)
    assert state["dataset"] == {"aLocal": 2, "bLocal": 1, "indexed": 1, "independentReview": True}, "incomplete governed proxy dataset"
    json_file(directory / "owner.json", state)
    state["proxy"] = client_command(directory, "proxy", timeout=240)
    validate_proxy(state["proxy"])
    json_file(directory / "owner.json", state)
    state["beforeOutage"] = client_command(directory, "availability-ready")
    assert state["beforeOutage"] == {"forwardedRead": True}, "missing forwarded availability control"
    compose(directory, "stop", "--timeout", "20", "api", timeout=30)
    state["outage"] = client_command(directory, "availability-down", timeout=45)
    assert state["outage"].get("translation") in ("tool_error", "rpc_error") and state["outage"].get("boundedFailure") is True and state["outage"].get("upstream502") is True, "incomplete upstream outage control"
    json_file(directory / "owner.json", state)
    compose(directory, "up", "-d", "--pull", "never", "--no-build", "--wait", "--wait-timeout", "60", "api", timeout=90)
    state["recovery"] = client_command(directory, "availability-ready")
    assert state["recovery"] == {"forwardedRead": True}, "missing forwarded recovery control"
    assert fingerprint(SOURCE) == state["source"], "source inputs changed during proxy qualification"
    state.update(success=False, phase="catalog-proxy-complete")
    json_file(directory / "owner.json", state)
    json_file(directory / "report.json", state)


def validate_proxy(result):
    """Reject incomplete client summaries independently of client exit status."""
    assert set(result) == {"protocols", "isolation", "credentials", "lifecycle", "metadataOnly"}, "incomplete proxy groups"
    assert result["metadataOnly"] is True, "unverified metadata-only boundary"
    protocols = result["protocols"]
    assert len(protocols) == 2 and [p["protocol"] for p in protocols] == ["2025-11-25", "2026-07-28"], "incomplete protocol selection"
    for protocol in protocols:
        assert protocol["upstreamProtocols"] and set(protocol["upstreamProtocols"]) <= {"2025-11-25", "2026-07-28"}, "missing upstream negotiation"
        assert all(protocol[x] is True for x in ("inventory", "schemas", "sixTools", "pagesAndFilters", "emptyResults", "exactReferences", "indexed", "facetsAndAccess")), "missing metadata control"
        assert protocol["closedArguments"] in ("tool_error", "rpc_error"), "missing closed-argument refusal"
        assert protocol["argumentValidation"] in ("catalog", "gateway_or_client"), "missing argument-validation location"
    isolation = result["isolation"]
    assert all(isolation[x] is True for x in ("twoTenants", "wrongTeam", "upstreamContact", "revokedCollections", "sameClientRestored")), "missing current-access control"
    assert isolation["revokedExactRead"] in ("tool_error", "rpc_error"), "missing exact-read refusal"
    credentials = result["credentials"]
    assert all(credentials[x] is True for x in ("missingAndInvalidInbound", "host", "origin", "privateTLS", "expiredAudienceAndScope", "managedCredentialOverrideBlocked", "invalidCredentialUpdatesRefused", "configuredIdentity")), "missing credential control"
    assert credentials["upstreamDenials"] == {"expired": 401, "audience": 401, "scope": 403}, "incorrect upstream admission result"
    assert result["lifecycle"] == dict.fromkeys(("sameUserSubset", "excludedWithoutUpstreamCall", "acceptedCredentialReplacement", "replacementData", "restored"), True), "incomplete gateway lifecycle"


def cli_result(directory, action, *arguments, actor="reader"):
    assert action in ("search", "record", "capabilities") and actor in ("reader", "other-tenant", "invalid", "missing"), "fixed CLI read selection"
    result = compose(directory, "--progress", "quiet", "run", "--rm", "--no-deps", "cli", "remote", action, "--base-url", "https://federation-fixture:8443",
                     "--ca-file", "/client/ca.pem", "--token-file", "/client/" + actor + "-token", "--timeout", "10s", *arguments, timeout=30, check=False)
    secrets = json.loads((directory / "private/secrets.json").read_text())
    assert not any(token in result.stdout + result.stderr for token in secrets["tokens"].values()), "Catalog CLI reflected a credential"
    assert not JWT_PATTERN.search(result.stdout + result.stderr), "Catalog CLI reflected a peer credential"
    return result


def cli(directory, action, *arguments):
    result = cli_result(directory, action, *arguments)
    assert result.code == 0, "Catalog CLI read failed"
    value = json.loads(result.stdout)
    assert value["meta"]["requestId"] and value["data"] is not None, "Catalog CLI envelope"
    return value


def handoff(directory):
    state = owner(directory)
    state.update(success=False, phase="governed-handoff")
    json_file(directory / "owner.json", state)
    expected = client_command(directory, "handoff-publish", timeout=90)
    state["selection"] = {k: v for k, v in expected.items() if k not in ("listingBody", "metadataBaseline")}
    query = cli(directory, "search", "--kind", "mcp_server", "--tag", TAG, "--limit", "2")
    item, selected = select_record(query, lambda release: cli(directory, "record", "--id", release), expected)
    validate_search_record(item, selected)
    later = client_command(directory, "handoff-draft")
    state["draft"] = later
    item, retained = select_record(cli(directory, "search", "--kind", "mcp_server", "--tag", TAG, "--limit", "2"), lambda release: cli(directory, "record", "--id", release), expected)
    validate_search_record(item, retained)
    for section in ("versionRevision", "listingRevision", "release", "evidence"):
        assert retained[section] == selected[section], "later draft changed sealed handoff evidence"
    assert retained["listing"]["candidateRevisionId"] == later["listingRevision"], "later candidate remains unpublished"
    private = directory / "private/client-state"
    assert not (private / "handoff-client.json").exists(), "client configuration must follow authorized selection"
    healthy = cli_result(directory, "capabilities", actor="other-tenant")
    assert healthy.code == 0 and "mcp_server" in json.loads(healthy.stdout)["data"]["resourceKinds"], "valid other-tenant credential control"
    for actor, status in (("missing", None), ("invalid", 401), ("other-tenant", 404)):
        refused = cli_result(directory, "record", "--id", expected["release"], actor=actor)
        assert refused.code == 1 and not refused.stdout.strip(), "denied CLI read produced no successful record"
        if status:
            assert json.loads(refused.stderr)["status"] == status, "actual CLI metadata admission refusal"
        assert not (private / "handoff-client.json").exists(), "denied CLI read created a runtime configuration"
    json_file(private / "handoff-selected.json", retained)
    state["handoffConnection"] = client_command(directory, "handoff-map")
    assert state["handoffConnection"] == {"url": endpoint(expected["server"]), "server": expected["server"], "metadataOnly": True,
                                           "deniedReads": ["missing", "invalid", "wrong-tenant"], "separateCredential": True}, "complete governed mapping"
    json_file(directory / "owner.json", state)
    started = datetime.now(timezone.utc).isoformat()
    runtime = client_command(directory, "handoff-runtime", timeout=120)
    logs = compose(directory, "logs", "--no-color", "--since", started, "--tail", "200", "contextforge", "federation-fixture", check=False, timeout=20)
    text = logs.stdout + logs.stderr
    secrets = json.loads((directory / "private/secrets.json").read_text())
    for token in list(secrets["tokens"].values()) + [v for v in secrets.values() if isinstance(v, str)]:
        text = text.replace(token, "[redacted]")
    text = JWT_PATTERN.sub("[redacted token]", text)
    (directory / "handoff-peer.log").write_text(text)
    (directory / "handoff-peer.log").chmod(0o600)
    runtime["prompt"] = classify_prompt_failure(runtime["prompt"], runtime["nativeNames"]["prompt"], text)
    validate_runtime(runtime, tools.pins()["version"])
    state["handoffRuntime"] = runtime
    state["handoffRefusals"] = client_command(directory, "handoff-refusals")
    assert state["handoffRefusals"] == {"missing": 401, "wrongTeam": 403, "noInvocation": True}, "runtime refusal controls"
    state["guideExample"] = guide_echo(directory)
    json_file(directory / "owner.json", state)
    state["handoffLifecycle"] = {}
    for service in ("everything", "contextforge"):
        before = client_command(directory, "handoff-ready")
        assert before["echo"] and before["forwarded"] and before["sameServer"], "healthy forwarded echo before outage"
        compose(directory, "stop", "--timeout", "20", service, timeout=30)
        down = client_command(directory, "handoff-down-" + service, timeout=45)
        assert down == {"boundedFailure": True, "service": service, "upstream502": service == "everything"}, "actual runtime outage"
        compose(directory, "exec", "--no-TTY", "api", "/usr/local/bin/catalogctl", "probe", "--url", "http://127.0.0.1:8080/readyz", timeout=15)
        during = cli(directory, "record", "--id", expected["release"])["data"]
        validate_record(during, expected)
        assert during == retained, "runtime outage changed the Catalog release"
        compose(directory, "up", "-d", "--pull", "never", "--no-build", "--wait", "--wait-timeout", "120", service, timeout=150)
        recovered = client_command(directory, "handoff-ready")
        assert recovered["echo"] and recovered["forwarded"] and recovered["sameServer"], "same endpoint recovers after restart"
        assert cli(directory, "record", "--id", expected["release"])["data"] == retained, "recovery changed sealed metadata"
        state["handoffLifecycle"][service] = {"boundedFailure": True, "recovered": True, "sameServer": True, "catalogReady": True, "sealedUnchanged": True}
        json_file(directory / "owner.json", state)
    assert fingerprint(SOURCE) == state["source"], "source inputs changed during governed handoff"
    state.update(success=True, phase="governed-handoff-complete")
    json_file(directory / "owner.json", state)
    json_file(directory / "report.json", state)


def stop(directory):
    signal.alarm(0)
    state = owner(directory)
    state.update(cleanup=False, resourcesRemoved=False, privateRemoved=False)
    try:
        if (directory / "private").exists():
            result = compose(directory, "down", "--volumes", "--remove-orphans", "--timeout", "20", timeout=90, check=False)
            assert result.code == 0, "owned Compose teardown failed"
        for kind, args in (("container", ("ps", "-aq")), ("network", ("network", "ls", "-q")), ("volume", ("volume", "ls", "-q"))):
            remaining = docker(*args, "--filter", "label=com.docker.compose.project=" + state["project"]).stdout.strip()
            assert not remaining, "owned " + kind + " cleanup incomplete"
        state["resourcesRemoved"] = True
        running = set(docker("ps", "-q").stdout.split())
        assert set(state["retained"]) <= running, "a retained container stopped during qualification"
        state["cleanup"] = True
    except BaseException as error:
        state["success"] = False
        state["cleanupFailure"] = type(error).__name__
        raise
    finally:
        try:
            if state.get("resourcesRemoved") and (directory / "private").exists():
                shutil.rmtree(directory / "private")
        finally:
            state["privateRemoved"] = not (directory / "private").exists()
            state["cleanup"] = state["cleanup"] and state["privateRemoved"]
            state["success"] = state["success"] and state["cleanup"]
            json_file(directory / "owner.json", state)
            json_file(directory / "report.json", state)


def main():
    if not __debug__:
        raise RuntimeError("ContextForge qualification requires Python assertions")
    arguments = sys.argv[1:]
    action = arguments[0] if arguments else "check"
    directory = None
    if action == "prepare" and len(arguments) == 1:
        print(prepare())
        return
    stages = {"start": ("prepared", start), "register": ("started", register), "proxy": ("native-controls-complete", proxy),
              "handoff": ("catalog-proxy-complete", handoff)}
    if action in (*stages, "info", "browser", "stop") and len(arguments) == 2:
        directory = Path(arguments[1]).absolute()
        state = owner(directory)
        if action == "stop":
            stop(directory)
            return
        assert not state.get("cleanup"), "the fixture has already stopped; prepare a new one"
        if action == "info":
            print(json.dumps(connection_info(directory), indent=2))
            return
        assert fingerprint(SOURCE) == state["source"], "source changed; stop this fixture and prepare a new one"
        if action in stages:
            assert state["phase"] == stages[action][0], "run each setup stage once, in documented order"
        else:
            assert state["phase"] in ("native-controls-complete", "catalog-proxy-complete", "governed-handoff-complete"), "register the native servers first"
    elif action != "check" or len(arguments) > 1:
        raise ValueError("select check, prepare, or start/register/proxy/handoff/info/browser/stop <owned-directory>")
    completed = False
    try:
        if action == "check":
            directory = prepare()
            print("ContextForge integration report: " + str(directory / "report.json"), flush=True)
            start(directory)
            register(directory)
            proxy(directory)
            handoff(directory)
        elif action == "browser":
            browser(directory)
        else:
            stages[action][1](directory)
        completed = True
    except BaseException as error:
        if directory is not None:
            state = owner(directory)
            state["errorType"] = type(error).__name__
            state["errorLocation"] = [{"file": Path(frame.filename).name, "line": frame.lineno} for frame in traceback.extract_tb(error.__traceback__)]
            state["success"] = False
            json_file(directory / "owner.json", state)
            try:
                failure = directory / "private/client-state/client-failure.json"
                if failure.exists():
                    state["clientFailure"] = json.loads(failure.read_text())
                failed_token = directory / "private/client-state/native-failure-token"
                if failed_token.exists():
                    script = '''import sys,json,time,jwt,sqlite3
from mcpgateway.config import settings
from mcpgateway.utils.jwt_config_helper import get_jwt_public_key_or_secret
token=sys.stdin.read()
result={"wall":time.time()}
try:
 claims=jwt.decode(token,get_jwt_public_key_or_secret(),algorithms=[settings.jwt_algorithm],audience=settings.jwt_audience,issuer=settings.jwt_issuer)
 result["verification"]="passed"
except Exception as e:
 result["verification"]=type(e).__name__
 claims=jwt.decode(token,options={"verify_signature":False})
result["times"]={k:v for k,v in claims.items() if k in ("iat","exp","nbf") and type(v) in (int,float)}
with sqlite3.connect("file:/data/mcp.db?mode=ro",uri=True) as db:
 result["subjectExists"]=db.execute("SELECT count(*) FROM email_users WHERE id=? AND email=?",(claims.get("sub"),"admin@example.com")).fetchone()[0]==1
print(json.dumps(result))'''
                    probe = subprocess.run(compose_args(directory, "exec", "--no-TTY", "contextforge", "python3", "-B", "-c", script), input=failed_token.read_text(),
                                           capture_output=True, text=True, timeout=15, env=environment(), check=False)
                    if probe.returncode == 0:
                        state["nativeAuthenticationProbe"] = json.loads(probe.stdout)
                json_file(directory / "owner.json", state)
                # Native service logs can contain credentials. Keep redacted diagnostics only.
                result = compose(directory, "logs", "--no-color", "--tail", "60", check=False, timeout=20)
                diagnostic = result.stdout + result.stderr + "\n" + str(error)
                secrets = json.loads((directory / "private/secrets.json").read_text())
                for value in list(secrets["tokens"].values()) + [v for v in secrets.values() if isinstance(v, str)]:
                    diagnostic = diagnostic.replace(value, "[redacted]")
                diagnostic = JWT_PATTERN.sub("[redacted token]", diagnostic)
                (directory / "failure.log").write_text(diagnostic)
                (directory / "failure.log").chmod(0o600)
            except Exception as diagnostic:
                state["diagnosticFailure"] = type(diagnostic).__name__
            json_file(directory / "owner.json", state)
        raise
    finally:
        if directory is not None and (action == "check" or not completed):
            stop(directory)
    if action == "check":
        print("ContextForge native controls, Catalog proxy, governed handoff and owned cleanup passed.")
    else:
        print(json.dumps({"stage": action, "report": str(directory / "report.json")}))


if __name__ == "__main__":
    def interrupted(_signum, _frame):
        raise KeyboardInterrupt("ContextForge qualification interrupted")
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    signal.signal(signal.SIGALRM, interrupted)
    signal.alarm(1800)
    try:
        main()
    except (Exception, KeyboardInterrupt) as error:
        # Dependency exceptions and raw process diagnostics stay private.
        print("ContextForge qualification failed: " + type(error).__name__, file=sys.stderr)
        raise SystemExit(1) from None
    finally:
        signal.alarm(0)
