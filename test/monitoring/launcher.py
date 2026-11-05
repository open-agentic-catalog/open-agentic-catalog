#!/usr/bin/env python3
"""Catalog-owned local monitoring helpers; no external monitoring discovery."""

import importlib.util
import ipaddress
import json
import os
from pathlib import Path
import re
import secrets
import shlex
import sys

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("catalog_functional_runner", ROOT / "test/functional/runner.py")
safety = importlib.util.module_from_spec(spec)
spec.loader.exec_module(safety)
query_spec = importlib.util.spec_from_file_location("catalog_monitoring_queries", ROOT / "test/monitoring/queries.py")
queries = importlib.util.module_from_spec(query_spec)
query_spec.loader.exec_module(queries)
runtime_spec = importlib.util.spec_from_file_location("catalog_demo_runtimes", ROOT / "test/demo/runtimes.py")
runtimes = importlib.util.module_from_spec(runtime_spec)
runtime_spec.loader.exec_module(runtimes)
Failure = safety.Failure
PROJECT = re.compile(r"[a-z0-9][a-z0-9_-]{0,63}\Z")


def validate_project(project):
    if not isinstance(project, str) or not PROJECT.fullmatch(project):
        raise Failure("invalid-project")
    return project


def image_pins():
    pins = safety.read_document(ROOT / "deployments/monitoring/images.json")
    if set(pins) != {"prometheus", "grafana"}:
        raise Failure("invalid-image-pins")
    for name, image in pins.items():
        prefix = "prom/prometheus:" if name == "prometheus" else "grafana/grafana:"
        if not isinstance(image, str) or not image.startswith(prefix) or not re.fullmatch(r"[a-z0-9/.:_-]+@sha256:[0-9a-f]{64}", image):
            raise Failure("invalid-image-pin")
    return pins


def private_directory(path):
    # Do not follow a pre-existing link at any component, including output/.
    path = Path(path).absolute()
    for parent in [*reversed(path.parents), path]:
        if parent.is_symlink():
            raise Failure("unsafe-state-path")
    path.mkdir(parents=True, exist_ok=True, mode=0o700)
    if path.stat().st_uid != os.getuid():
        raise Failure("foreign-state-owner")
    path.chmod(0o700)
    return path


def secret_file(directory):
    path = private_directory(directory) / "grafana-password"
    if path.is_symlink():
        raise Failure("unsafe-secret-path")
    try:
        descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    except FileExistsError:
        if not path.is_file() or path.stat().st_uid != os.getuid() or path.stat().st_mode & 0o022:
            raise Failure("unsafe-secret-file") from None
        with path.open("rb") as source:
            if not re.fullmatch(rb"[a-f0-9]{64}\n", source.read(128)):
                raise Failure("invalid-secret-file") from None
    else:
        with os.fdopen(descriptor, "w") as output:
            output.write(secrets.token_hex(32) + "\n")
        # The 0700 parent protects this file on the host. Compose file-backed
        # secrets retain host modes, so the mounted file must be readable by the
        # non-root Grafana UID; only that service receives this mount.
        path.chmod(0o444)
    return path


def bounded_integer(environment, name, default, minimum, maximum):
    value = environment.get(name, str(default))
    if not isinstance(value, str) or not re.fullmatch(r"[0-9]{1,6}", value) or not minimum <= int(value) <= maximum:
        raise Failure("invalid-" + name.lower().replace("_", "-"))
    return int(value)


def monitoring_environment(environment, secret, prometheus_port="19090", grafana_port="13000"):
    result = dict(environment)
    for value in [prometheus_port, grafana_port]:
        if not re.fullmatch(r"[0-9]{1,5}", str(value)) or int(value) > 65535:
            raise Failure("invalid-monitoring-port")
    if int(prometheus_port) and int(prometheus_port) == int(grafana_port):
        raise Failure("conflicting-monitoring-ports")
    retention_hours = bounded_integer(environment, "CATALOG_MONITORING_RETENTION_HOURS", 6, 1, 24)
    retention_mb = bounded_integer(environment, "CATALOG_MONITORING_RETENTION_MB", 512, 128, 2048)
    pins = image_pins()
    result.update(CATALOG_MONITORING_PROMETHEUS_IMAGE=pins["prometheus"],
                  CATALOG_MONITORING_GRAFANA_IMAGE=pins["grafana"],
                  CATALOG_MONITORING_SECRET_FILE=str(secret),
                  CATALOG_MONITORING_RETENTION_HOURS=str(retention_hours),
                  CATALOG_MONITORING_RETENTION_MB=str(retention_mb),
                  CATALOG_MONITORING_PROMETHEUS_PORT=str(prometheus_port),
                  CATALOG_MONITORING_GRAFANA_PORT=str(grafana_port))
    return result


def compose_arguments(project, enabled, executable=None, ui=False, embeddings=False, demo=False, peers=False):
    result = [*(executable or ["docker", "compose"]), "--env-file", "/dev/null", "-f", str(ROOT / "compose.yaml")]
    if enabled:
        result += ["-f", str(ROOT / "compose.monitoring.yaml")]
    if ui:
        result += ["-f", str(ROOT / "compose.ui.yaml")]
    if embeddings:
        result += ["-f", str(ROOT / "compose.embeddings.yaml")]
    if demo:
        result += ["-f", str(ROOT / "compose.demo.yaml")]
    if peers:
        result += ["-f", str(ROOT / "compose.demo-runtimes.yaml")]
    return result + ["-p", validate_project(project)]


def ui_inputs(project, environment, create):
    """Prepare local-only UI inputs; retain session keys across Compose restarts."""
    parent = ROOT / "output/ui-compose" / validate_project(project)
    port = bounded_integer(environment, "CATALOG_UI_PORT", 4180, 1024, 65535)
    directory = parent / "inputs"
    if create:
        if environment.get("CATALOG_AUTH_MODE", "development") != "development":
            raise Failure("local-ui-requires-development-api")
        private_directory(parent)
        private_directory(directory)
        # The private parent protects host access; the mounted child must be
        # traversable by the non-root image UID and admission-assigned UIDs.
        directory.chmod(0o755)
        profile = safety.read_document(ROOT / "config/postgres.json")
        users = [{"alias": token, "label": (user.get("label") or user["subject"]), "token": token,
                  **({"description": user["description"]} if user.get("description") else {}),
                  **{key: user[key] for key in ("profileGroup", "elevated", "separateTenant") if key in user}}
                 for token, user in profile["auth"]["developmentTokens"].items()]
        config = {"listen": "0.0.0.0:8080", "environment": "development",
                  "origin": "http://127.0.0.1:" + str(port), "basePath": "",
                  "assets": "/app/assets", "upstream": "http://api:8080",
                  "cookieName": "catalog-ui-compose-" + str(port), "keysFile": "/config/keys.json",
                  "sessionSeconds": 3600, "requestBytes": 2097152, "responseBytes": 16777216,
                  "logLevel": environment.get("CATALOG_UI_LOG_LEVEL", "info"),
                  "logFormat": environment.get("CATALOG_UI_LOG_FORMAT", "text"),
                  "documentationUrl": environment.get("CATALOG_UI_DOCUMENTATION_URL", ""),
                  "timeoutSeconds": 60, "authMode": "development", "developmentUsersFile": "/config/users.json"}
        for name, value in [("keys.json", [{"hash": secrets.token_hex(64), "block": secrets.token_hex(32)}]),
                            ("users.json", users), ("config.json", config)]:
            path = directory / name
            if path.is_symlink():
                raise Failure("unsafe-ui-input")
            if path.exists():
                if not path.is_file() or path.stat().st_uid != os.getuid() or path.stat().st_mode & 0o022:
                    raise Failure("unsafe-ui-input")
                if name == "keys.json":
                    # The host validates key syntax at startup; never silently rotate it.
                    continue
                path.chmod(0o600)
            flags = os.O_WRONLY | os.O_CREAT | os.O_NOFOLLOW
            flags |= os.O_EXCL if name == "keys.json" else os.O_TRUNC
            descriptor = os.open(path, flags, 0o600)
            with os.fdopen(descriptor, "w") as output:
                json.dump(value, output)
            path.chmod(0o444)
    # Stop/down/ps use the retained origin even when the port flag is omitted.
    elif (directory / "config.json").is_file():
        config = safety.read_document(directory / "config.json")
        port = int(config["origin"].rsplit(":", 1)[1])
    environment.update(CATALOG_UI_PORT=str(port), CATALOG_UI_INPUT_DIRECTORY=str(directory))
    return environment


def demo_inputs(environment, verify):
    """Select already prepared local fixtures; never rotate TLS or seed records."""
    result = dict(environment)
    if os.getuid() == 0:
        raise Failure("demo-archives-require-non-root")
    ip = result.get("DEMO_ARCHIVE_IP", "172.30.255.41")
    subnet = result.get("CATALOG_COMPOSE_SUBNET", "172.30.255.0/24")
    try:
        address = ipaddress.ip_address(ip)
        network = ipaddress.ip_network(subnet)
    except ValueError:
        raise Failure("invalid-demo-network") from None
    if address.version != 4 or str(address) != ip or not any(address in ipaddress.ip_network(cidr) for cidr in ("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16")) or address.is_loopback or address not in network or address in {network.network_address, network.broadcast_address}:
        raise Failure("invalid-demo-network")
    port = bounded_integer(result, "DEMO_ARCHIVE_PORT", 18443, 1024, 65535)
    directory = Path(result.get("DEMO_ARCHIVE_DIR", str(ROOT / "output/demo/archives"))).absolute()
    if verify:
        if result.get("CATALOG_ENVIRONMENT", "development") != "development" or result.get("CATALOG_AUTH_MODE", "development") != "development":
            raise Failure("demo-archives-require-development")
        if any(parent.is_symlink() for parent in [*directory.parents, directory]) or not directory.is_dir() or directory.stat().st_uid != os.getuid() or directory.stat().st_mode & 0o077:
            raise Failure("demo-archives-prepare-private-directory-first")
        for name in ("cert.pem", "key.pem", "demo-archives", "allowlist.json"):
            path = directory / name
            if path.is_symlink() or not path.is_file() or path.stat().st_uid != os.getuid() or path.stat().st_mode & 0o022:
                raise Failure("demo-archives-prepare-files-first")
        if (directory / "key.pem").stat().st_mode & 0o077 or not os.access(directory / "demo-archives", os.X_OK):
            raise Failure("unsafe-demo-archive-files")
    result.update(DEMO_ARCHIVE_DIR=str(directory), DEMO_ARCHIVE_IP=ip, DEMO_ARCHIVE_PORT=str(port),
                  DEMO_ARCHIVE_UID=str(os.getuid()), DEMO_ARCHIVE_GID=str(os.getgid()))
    return result


def command(arguments, environment, seconds=120, capture=False):
    return safety.command(arguments, environment, seconds, capture=capture)


def isolated_environment():
    result = {key: os.environ[key] for key in ("PATH", "HOME", "DOCKER_CONFIG", "XDG_RUNTIME_DIR") if key in os.environ}
    result.update(DOCKER_CONTEXT="default", CATALOG_ENV_FILE="/dev/null", PYTHONDONTWRITEBYTECODE="1")
    return result


def embedding_inputs(environment, verify):
    """Share model pins; only starting a model requires the verified cache."""
    embedding_spec = importlib.util.spec_from_file_location("catalog_local_embeddings", ROOT / "test/embeddings/local.py")
    module = importlib.util.module_from_spec(embedding_spec)
    embedding_spec.loader.exec_module(module)
    return module.inputs(environment, verify=verify)


def monitoring_volumes(project, engine, environment):
    volumes = []
    for volume in ("monitoring-prometheus", "monitoring-grafana"):
        raw = command([*engine, "volume", "ls", "--filter", "label=com.docker.compose.project=" + project,
                       "--filter", "label=com.docker.compose.volume=" + volume, "--format", "{{.Name}}"], environment, capture=True)
        names = raw.decode().splitlines()
        if len(names) > 1 or any(name != project + "_" + volume for name in names):
            raise Failure("foreign-monitoring-volume")
        volumes.extend(names)
    return volumes


def development(action, project, mode):
    validate_project(project)
    if mode not in {"off", "on"}:
        raise Failure("invalid-monitoring-selection")
    if action not in {"up", "stop", "down", "reset", "ps", "logs", "reset-monitoring", "check", "prepare-runtimes"}:
        raise Failure("invalid-action")
    environment = dict(os.environ)
    webhooks = environment.get("LOCAL_WEBHOOKS", "on")
    if webhooks not in {"off", "on"}:
        raise Failure("invalid-webhooks-selection")
    # Only the local launcher opts in. An explicit runtime setting takes
    # precedence, and raw Compose, test profiles and Helm keep their defaults.
    # Development already has a stable key fallback; do not rotate existing
    # encrypted endpoint secrets by manufacturing a new key on each launch.
    if "CATALOG_WEBHOOKS_ENABLED" not in environment:
        if webhooks == "on" and environment.get("CATALOG_ENVIRONMENT", "development") != "development":
            raise Failure("local-webhooks-default-requires-development")
        environment["CATALOG_WEBHOOKS_ENABLED"] = "true" if webhooks == "on" else "false"
    demo_mode = environment.get("LOCAL_DEMO_ARCHIVES", "off")
    if demo_mode not in {"off", "on"}:
        raise Failure("invalid-demo-archives-selection")
    if demo_mode == "on":
        environment = demo_inputs(environment, verify=action == "up")
    peer_mode = environment.get("LOCAL_DEMO_RUNTIMES", "off")
    if peer_mode not in {"off", "on"}:
        raise Failure("invalid-demo-runtimes-selection")
    ui_mode = environment.get("LOCAL_UI", "off")
    embedding_mode = environment.get("LOCAL_EMBEDDINGS", "off")
    if embedding_mode not in {"off", "on"}:
        raise Failure("invalid-embeddings-selection")
    if ui_mode not in {"off", "on"}:
        raise Failure("invalid-ui-selection")
    if ui_mode == "on":
        bounded_integer(environment, "CATALOG_UI_PORT", 4180, 1024, 65535)
        if action == "up" and environment.get("CATALOG_AUTH_MODE", "development") != "development":
            raise Failure("local-ui-requires-development-api")
    executable = shlex.split(environment.get("CATALOG_LOCAL_COMPOSE", "docker compose"))
    engine = shlex.split(environment.get("CATALOG_LOCAL_ENGINE", "docker"))
    if not executable or not engine:
        raise Failure("missing-container-command")
    directory = ROOT / "output/monitoring" / project
    enabled = mode == "on"
    # Validate enabled settings before any tool invocation or credential write.
    if enabled or action == "check":
        environment = monitoring_environment(environment, "/dev/null",
                    environment.get("CATALOG_MONITORING_PROMETHEUS_PORT", "19090"),
                    environment.get("CATALOG_MONITORING_GRAFANA_PORT", "13000"))
    if action == "check":
        command(compose_arguments(project, True, executable) + ["config", "--quiet"], environment)
        environment.update(CATALOG_UI_INPUT_DIRECTORY=str(ROOT / "config"))
        command(compose_arguments(project, True, executable, ui=True) + ["config", "--quiet"], environment)
        environment = embedding_inputs(environment, verify=False)
        command(compose_arguments(project, True, executable, ui=True, embeddings=True) + ["config", "--quiet"], environment)
        peer_environment = runtimes.inputs(environment, verify=False, command=command)
        command(compose_arguments(project, True, executable, peers=True) + ["config", "--quiet"], peer_environment)
        return
    if action == "prepare-runtimes":
        try:
            prepared = runtimes.inputs(environment, verify=True, command=command)
        except RuntimeError as error:
            raise Failure("install-locked-a2a-and-mcp-tools-first") from error
        print("Prepared private demo peer inputs: " + prepared["DEMO_RUNTIME_DIR"])
        return
    # Detect existing services using Docker labels, not a remembered filename.
    raw = command([*engine, "ps", "-a", "--filter", "label=com.docker.compose.project=" + project, "--format", '{{.Label "com.docker.compose.service"}}'], environment, capture=True)
    present = set(raw.decode().splitlines())
    peers_present = bool(present & set(runtimes.SERVICES))
    if peers_present and peer_mode == "off" and action == "up":
        raise Failure("demo-runtimes-already-enabled-use-on-or-compose-down")
    include_peers = peer_mode == "on" or peers_present
    if include_peers:
        try:
            environment = runtimes.inputs(environment, verify=action == "up", command=command)
        except RuntimeError as error:
            raise Failure("install-locked-a2a-and-mcp-tools-first") from error
    monitoring_present = bool(present & {"prometheus", "grafana"})
    ui_present = "ui" in present
    if ui_present and ui_mode == "off" and action == "up":
        raise Failure("ui-already-enabled-use-on-or-compose-down")
    demo_present = "demo-archives" in present
    if demo_present and demo_mode == "off" and action == "up":
        raise Failure("demo-archives-already-enabled-use-on-or-compose-down")
    include_demo = demo_mode == "on" or demo_present
    if include_demo and demo_mode == "off":
        environment = demo_inputs(environment, verify=False)
    include_ui = ui_mode == "on" or ui_present
    include_embeddings = embedding_mode == "on" or "embeddings" in present
    if "embeddings" in present and embedding_mode == "off" and action == "up":
        raise Failure("embeddings-already-enabled-use-on-or-compose-down")
    if include_embeddings:
        environment = embedding_inputs(environment, verify=action == "up")
    if include_ui:
        environment = ui_inputs(project, environment, create=action == "up")
    if monitoring_present and not enabled and action == "up":
        raise Failure("monitoring-already-enabled-use-on-or-explicit-reset")
    # Retained volumes outlive `down`. Validate all reset targets before any
    # container removal, including when no monitoring containers remain.
    volumes = monitoring_volumes(project, engine, environment) if action in {"reset", "reset-monitoring"} else []
    # Inspection/stop/down include existing monitoring even if the caller omitted
    # the selector. No orphan deletion decides whether monitoring should survive.
    include = enabled or monitoring_present or bool(volumes) or action == "reset-monitoring"
    if include:
        environment = monitoring_environment(environment, "/dev/null",
                    environment.get("CATALOG_MONITORING_PROMETHEUS_PORT", "19090"),
                    environment.get("CATALOG_MONITORING_GRAFANA_PORT", "13000"))
        environment["CATALOG_MONITORING_SECRET_FILE"] = str(secret_file(directory))
    compose = compose_arguments(project, include, executable, ui=include_ui, embeddings=include_embeddings, demo=include_demo, peers=include_peers)
    if action == "reset-monitoring":
        command(compose + ["rm", "--stop", "--force", "prometheus", "grafana"], environment)
        for name in volumes:
            command([*engine, "volume", "rm", name], environment)
        # Keep the private password for a repeatable local setup. It is not data
        # authority, and never delete arbitrary caller files during a reset.
        return
    if action == "logs":
        os.execvpe(compose[0], compose + ["logs", "--follow", "--tail", "100"], environment)
        return
    arguments = {"up": ["up", "--no-build", "--force-recreate", "--wait", "--wait-timeout", "180"], "stop": ["stop"], "down": ["down"], "reset": ["down", "--volumes"],
                 "ps": ["ps"]}[action]
    if action == "up":
        # A failed build must not stop the currently running Catalog. Finish
        # building first, then replace roles using those already-built images.
        command(compose + ["build"], environment, seconds=600)
        # Validate both target role configurations before disturbing the live
        # processes. These commands do not connect to or mutate PostgreSQL.
        for role in ("api", "worker"):
            command(compose + ["run", "--rm", "--no-deps", "--entrypoint", "/usr/local/bin/catalogctl",
                               role, "config", "validate", "--config", "/config/config.json", "--role", role], environment)
    if action == "up" and present & {"api", "worker"}:
        # Force-recreation is already disruptive. Drain API first, then worker,
        # so switching the local webhook flag cannot overlap opposite modes.
        command(compose + ["stop", "api"], environment)
        command(compose + ["stop", "worker"], environment)
    output = command(compose + arguments, environment, seconds=600, capture=action == "ps")
    if action == "ps":
        sys.stdout.buffer.write(output)
    if action == "up" and include:
        print("Catalog monitoring enabled; username: catalog; password file: " + str(directory / "grafana-password"))
        if int(environment["CATALOG_MONITORING_GRAFANA_PORT"]):
            print("Grafana: http://127.0.0.1:" + environment["CATALOG_MONITORING_GRAFANA_PORT"])
        else:
            print("Grafana: use compose-ps to discover the assigned loopback port.")
    if action == "up" and include_ui:
        print("Catalog UI: http://127.0.0.1:" + environment["CATALOG_UI_PORT"])
    if action == "up" and include_peers:
        runtimes.ready(environment, compose, command, failure=Failure)
        print("Local A2A: https://127.0.0.1:" + environment["DEMO_A2A_PORT"] + "/; local MCP: https://127.0.0.1:" + environment["DEMO_MCP_PORT"] + "/mcp")
        print("Separate private client inputs: " + environment["DEMO_RUNTIME_DIR"] + "; run make demo-runtimes-test explicitly. Startup does not invoke or seed resources.")
    if action == "up" and include_demo:
        print("Demo archives enabled; explicitly seed using DEMO_ARCHIVE_ORIGIN=https://" + environment["DEMO_ARCHIVE_IP"] + ":8443. Startup does not populate records.")
    if action == "up" and include_embeddings:
        print("Local embeddings: http://embeddings:80/v1/embeddings on the Compose network only; configure semanticSearch and apply the optional database schema explicitly.")
    if action == "reset":
        print("Removed the selected project's Compose data volumes; recovery requires a prior backup.")


def main():
    if len(sys.argv) == 2:
        development(sys.argv[1], os.environ.get("CATALOG_LOCAL_PROJECT", "open-agentic-catalog"), os.environ.get("LOCAL_MONITORING", "off"))
    elif len(sys.argv) == 4:
        development(*sys.argv[1:])
    else:
        raise Failure("expected-action-project-mode")


if __name__ == "__main__":
    try:
        main()
    except (Failure, OSError, ValueError, UnicodeError) as error:
        print("Catalog monitoring failed: " + (str(error) if isinstance(error, Failure) else "local-input-or-tool-error"), file=sys.stderr)
        sys.exit(1)
