#!/usr/bin/env python3
"""Start one explicitly selected local password-based Catalog deployment."""

import argparse
import copy
import ipaddress
import hashlib
import http.client
import json
import os
from pathlib import Path
import socket
import stat
import ssl
import subprocess
import tempfile
import time
import uuid

import compose
import prepare


SERVICES = {"postgres-a", "owner-a", "api-a", "worker-a", "ui-a", "identity", "frontend"}


def run(command, environment=None):
    result = subprocess.run(command, env=environment, capture_output=True, timeout=300, check=False)
    if result.returncode:
        raise ValueError("Local authentication command failed; inspect the selected project's service logs")
    return result.stdout


def single_configuration(value):
    """Project the maintained tutorial topology without a second Catalog."""
    value = copy.deepcopy(value)
    value["services"] = {name: item for name, item in value["services"].items() if name in SERVICES}
    if set(value["services"]) != SERVICES:
        raise ValueError("Tutorial services are incomplete")
    value["volumes"] = {name: item for name, item in value["volumes"].items() if name != "postgres-b"}
    for service in value["services"].values():
        service["pull_policy"] = "never"
    for name in ("api-a", "worker-a"):
        value["services"][name]["stop_grace_period"] = "40s"
    frontend = value["services"]["frontend"]
    frontend["ports"] = [port for port in frontend["ports"] if str(port["target"]) in ("8443", "9443")]
    frontend["volumes"] = [mount for mount in frontend["volumes"] if "-b" not in mount["target"]]
    frontend["networks"]["tutorial"]["aliases"] = ["catalog-a.test", "browse-a.test"]
    frontend.setdefault("environment", {})["CATALOG_TUTORIAL_SINGLE"] = "1"
    return value


def private_json(path):
    metadata = path.lstat()
    if not stat.S_ISREG(metadata.st_mode) or metadata.st_uid != os.getuid() or stat.S_IMODE(metadata.st_mode) != 0o600 or metadata.st_size > 1048576:
        raise ValueError("Select an owned 0600 input file")
    value = json.loads(path.read_text())
    if not isinstance(value, dict):
        raise ValueError("Expected a generated JSON object")
    return value


def prepared_accounts(directory):
    accounts = private_json(directory / "accounts.json")
    if set(accounts) != {"catalog-admin", "catalog-reader"}:
        raise ValueError("The prepared account selection changed")
    accounts = {name: str(uuid.UUID(str(subject))) for name, subject in accounts.items()}
    if len(set(accounts.values())) != 2:
        raise ValueError("The prepared accounts must have different subjects")
    return accounts


def selection(directory):
    directory, values = compose.prepared_inputs(directory)
    marker = directory / "single.json"
    state = private_json(marker)
    if state.get("format") != 1 or state.get("ready") is not True:
        raise ValueError("Single-Catalog preparation is incomplete; preserve inputs and inspect preparation")
    configuration = directory / "compose.single.json"
    private_json(configuration)
    if configuration.is_symlink() or hashlib.sha256(configuration.read_bytes()).hexdigest() != state.get("sha256"):
        raise ValueError("Single-Catalog configuration changed")
    command = ["docker", "compose", "--project-name", values["COMPOSE_PROJECT_NAME"],
               "--env-file", str(directory / "compose.env"), "--file", str(configuration)]
    environment = os.environ.copy()
    environment.update(values)
    environment.pop("BUILDKIT_HOST", None)
    environment["BUILDX_BUILDER"] = "default"
    return directory, command, environment


def local_engine(environment):
    """Refuse remote Docker endpoints before starting or removing containers."""
    context = environment.get("DOCKER_CONTEXT")
    endpoint = environment.get("DOCKER_HOST")
    if context or not endpoint:
        arguments = ["docker", "context", "inspect"]
        if context:
            arguments.append(context)
        endpoint = json.loads(run(arguments, environment))[0]["Endpoints"]["docker"]["Host"]
    if not endpoint.startswith("unix:///"):
        raise ValueError("Select a local Docker Unix socket; remote engines are not supported")


def initialize(args):
    selected = Path(args.directory).expanduser()
    directory = selected.parent.resolve() / selected.name
    if directory.is_symlink():
        raise ValueError("Select the private directory directly, not a symbolic link")
    if directory.exists():
        try:
            return selection(directory)
        except FileNotFoundError:
            raise ValueError("Prepared metadata is incomplete. Preserve existing inputs and volumes. Restore the metadata; use a new directory only for a new setup") from None
    prepare.prepare(argparse.Namespace(directory=str(directory), backend_image=args.backend_image,
                                      ui_image=args.ui_image, subnet=args.subnet,
                                      frontend_address=args.frontend_address, single=True))
    command, environment = compose.invocation(directory, ["config", "--format", "json"])
    value = single_configuration(json.loads(run(command, environment)))
    raw = (json.dumps(value, indent=2) + "\n").encode()
    prepare.write(directory / "compose.single.json", raw.decode())
    (directory / "single.json").write_text(json.dumps({"format": 1, "ready": True, "sha256": hashlib.sha256(raw).hexdigest()}) + "\n")
    return selection(directory)


def https_get(directory, host, port, path):
    context = ssl.create_default_context(cafile=str(directory / "trust/ca.pem"))
    connection = http.client.HTTPSConnection(host, port, timeout=5, context=context)
    connection.connect = lambda: setattr(connection, "sock", context.wrap_socket(
        socket.create_connection(("127.0.0.1", port), timeout=5), server_hostname=host))
    try:
        connection.request("GET", path)
        response = connection.getresponse()
        data = response.read(1048577)
        if response.status != 200 or len(data) > 1048576:
            raise ValueError("Local HTTPS response unavailable")
        return data
    finally:
        connection.close()


def wait_for(action):
    deadline = time.monotonic() + 180
    while True:
        try:
            return action()
        except (OSError, ValueError, http.client.HTTPException):
            if time.monotonic() >= deadline:
                raise ValueError("Local service did not become ready within 180 seconds") from None
            time.sleep(1)


def certificates_current(directory):
    for name in ("ca", "identity", "catalog-a", "browse-a", "postgres-a"):
        path = directory / "trust" / (name + ".pem")
        try:
            run(["openssl", "x509", "-in", str(path), "-checkend", "60", "-noout"])
        except ValueError:
            raise ValueError("An active TLS certificate is expired or invalid. Run local_auth.py renew for this directory") from None


def available_port(port):
    try:
        with socket.socket() as listener:
            listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            listener.bind(("127.0.0.1", port))
    except OSError:
        raise ValueError("Local port " + str(port) + " is unavailable. Stop its current listener before starting this setup") from None


def preflight(directory, command, environment):
    """Inspect prerequisites and conflicts before creating project containers."""
    configuration = private_json(directory / "compose.single.json")
    prepared_accounts(directory)
    for service in configuration["services"].values():
        for mount in service.get("volumes", []):
            if mount["type"] == "bind":
                path = Path(mount["source"])
                if path.is_symlink() or not path.is_file() or directory not in path.resolve().parents:
                    raise ValueError("A prepared bind input is missing or changed. Restore the selected private inputs")
    run(["docker", "info", "--format", "{{.ServerVersion}}"], environment)
    for name in ("api-a", "ui-a", "identity", "postgres-a"):
        try:
            run(["docker", "image", "inspect", configuration["services"][name]["image"]], environment)
        except ValueError:
            raise ValueError("The local image for " + name + " is missing or unreadable. Prepare the documented images, then retry") from None
    running = run(command + ["ps", "--services", "--status", "running"], environment).decode().splitlines()
    for service, ports in (("identity", (8447,)), ("frontend", (8443, 9443))):
        if service not in running:
            for port in ports:
                available_port(port)
    networks = run(["docker", "network", "ls", "-q"], environment).decode().split()
    desired = ipaddress.ip_network(environment["CATALOG_TUTORIAL_SUBNET"])
    if networks:
        for network in json.loads(run(["docker", "network", "inspect", *networks], environment)):
            if (network.get("Labels") or {}).get("com.docker.compose.project") == environment["COMPOSE_PROJECT_NAME"]:
                continue
            for entry in network.get("IPAM", {}).get("Config") or []:
                if entry.get("Subnet"):
                    existing = ipaddress.ip_network(entry["Subnet"])
                    if existing.version == desired.version and existing.overlaps(desired):
                        raise ValueError("Choose an unused private subnet with --subnet and --frontend-address")
    certificates_current(directory)


def start(directory, command, environment):
    print("Checking local images, ports, network and TLS inputs", flush=True)
    preflight(directory, command, environment)
    print("Building the local TLS frontend", flush=True)
    run(command + ["build", "frontend"], environment)
    print("Starting PostgreSQL and waiting for local Keycloak HTTPS", flush=True)
    run(command + ["up", "-d", "--wait", "--wait-timeout", "180", "postgres-a"], environment)
    run(command + ["up", "-d", "identity", "frontend"], environment)
    def issuer_ready():
        value = json.loads(https_get(directory, "identity.test", 8447, "/realms/catalog/.well-known/openid-configuration"))
        if value.get("issuer") != prepare.ISSUER:
            raise ValueError("Local issuer disagrees with prepared inputs")
    wait_for(issuer_ready)
    print("Stopping the selected API, then worker, before migrations", flush=True)
    run(command + ["stop", "api-a"], environment)
    run(command + ["stop", "worker-a"], environment)
    print("Applying Catalog migrations and checking administrator membership", flush=True)
    run(command + ["run", "--rm", "owner-a", "migrate", "apply", "--config", "/inputs/a/owner.json",
                   "--api-role", "catalog_api", "--worker-role", "catalog_worker"], environment)
    accounts = prepared_accounts(directory)
    subject = accounts["catalog-admin"]
    count = run(command + ["exec", "-T", "postgres-a", "psql", "-XAt", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "catalog_a",
                           "-c", "SELECT count(*) FROM tenant_memberships m JOIN tenant_identity_realms r ON r.tenant_id=m.tenant_id AND r.id=m.realm_id WHERE r.issuer='" + prepare.ISSUER + "' AND m.actor_type='human' AND m.tenant_id='00000000-0000-7000-8000-000000000001' AND subject='" + subject + "'"], environment).strip()
    if count == b"0":
        run(command + ["run", "--rm", "owner-a", "identity", "provision", "--config", "/inputs/a/owner.json",
                       "--tenant", "00000000-0000-7000-8000-000000000001", "--tenant-name", "Local password Catalog",
                       "--issuer", prepare.ISSUER, "--subject", subject, "--roles", "tenant_admin",
                       "--operator", "local-auth", "--reason", "Initial local password administrator"], environment)
    elif count != b"1":
        raise ValueError("Administrator membership is ambiguous")
    print("Starting the Catalog API, worker and UI", flush=True)
    run(command + ["up", "-d", "--wait", "--wait-timeout", "180", "api-a", "worker-a", "ui-a"], environment)
    wait_for(lambda: https_get(directory, "catalog-a.test", 8443, "/readyz"))
    wait_for(lambda: https_get(directory, "browse-a.test", 9443, "/"))
    print("Authentication: OIDC through local Keycloak")
    print("OIDC Catalog: https://browse-a.test:9443/")
    print("Keycloak: https://identity.test:8447/admin/")
    print("Issuer: " + prepare.ISSUER)
    print("Initial passwords: selected private directory, *-initial-password files; provider administrator: bootstrap-password")
    print("Account subjects: accounts.json. Grant Reader access in Members after administrator login.")


def renew(directory, command, environment):
    """Replace TLS files only after stopping every consumer in this project."""
    run(command + ["stop"], environment)
    with tempfile.TemporaryDirectory(prefix="renew-", dir=directory) as temp:
        trust = Path(temp)
        prepare.openssl("req", "-x509", "-newkey", "rsa:2048", "-noenc", "-days", "7", "-sha256",
                        "-subj", "/CN=Local Catalog tutorial CA", "-addext", "basicConstraints=critical,CA:TRUE",
                        "-addext", "keyUsage=critical,keyCertSign,cRLSign", "-keyout", trust / "ca-key.pem", "-out", trust / "ca.pem")
        (trust / "ca-key.pem").chmod(0o600)
        (trust / "ca.pem").chmod(0o444)
        for name, dns in (("identity", "identity.test"), ("catalog-a", "catalog-a.test"),
                          ("browse-a", "browse-a.test"), ("postgres-a", "postgres-a")):
            prepare.certificate(trust, name, [dns])
        for path in trust.iterdir():
            os.replace(path, directory / "trust" / path.name)
    run(command + ["rm", "-f"], environment)
    print("TLS inputs replaced. Import trust/ca.pem in your browser, then run local-auth-up with the same directory.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("up", "down", "renew"))
    parser.add_argument("--directory", required=True)
    parser.add_argument("--backend-image", default="open-agentic-catalog:dev")
    parser.add_argument("--ui-image", default="open-agentic-catalog-ui:dev")
    parser.add_argument("--subnet", default="172.29.245.0/24")
    parser.add_argument("--frontend-address", default="172.29.245.10")
    args = parser.parse_args()
    try:
        local_engine(os.environ.copy())
        directory, command, environment = initialize(args) if args.action == "up" else selection(args.directory)
        if args.action == "up":
            start(directory, command, environment)
        elif args.action == "renew":
            renew(directory, command, environment)
        else:
            run(command + ["down"], environment)
    except (OSError, ValueError, KeyError, subprocess.SubprocessError, http.client.HTTPException) as error:
        detail = str(error) if isinstance(error, ValueError) else type(error).__name__
        raise SystemExit("Local authentication setup failed (" + detail + "). Check the selected private directory, prerequisites, ports and scoped service logs. For expired certificates, use local_auth.py renew.") from None


if __name__ == "__main__":
    main()
