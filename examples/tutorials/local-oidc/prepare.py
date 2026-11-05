#!/usr/bin/env python3
"""Prepare private inputs for the manual's local password/federation tutorial."""

import argparse
import base64
import json
import ipaddress
from pathlib import Path
import re
import secrets
import subprocess
import uuid


ROOT = Path(__file__).resolve().parents[3]
ISSUER = "https://identity.test:8447/realms/catalog"
SCOPES = [
    "audit.export", "audit.read", "catalog.publish", "catalog.read",
    "catalog.review", "catalog.write", "feedback.write", "personal.write", "publishers.manage",
    "publishers.read", "ratings.write", "sources.manage", "sources.read",
    "tenant.admin", "webhooks.manage",
]


def write(path, value, mode=0o600):
    """Create inputs exclusively; a prepared environment is never overwritten."""
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    with path.open("x", encoding="ascii") as stream:
        stream.write(value)
    path.chmod(mode)


def document(path, value, mode=0o444):
    write(path, json.dumps(value, indent=2) + "\n", mode)


def openssl(*arguments):
    subprocess.run(["openssl", *map(str, arguments)], check=True, capture_output=True, timeout=30)


def certificate(directory, name, dns):
    key = directory / (name + "-key.pem")
    request = directory / (name + ".csr")
    extensions = directory / (name + ".extensions")
    write(extensions, "subjectAltName=" + ",".join("DNS:" + host for host in dns)
          + "\nextendedKeyUsage=serverAuth\nbasicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\n")
    openssl("req", "-new", "-newkey", "rsa:2048", "-noenc", "-subj", "/CN=" + dns[0], "-keyout", key, "-out", request)
    openssl("x509", "-req", "-in", request, "-CA", directory / "ca.pem", "-CAkey", directory / "ca-key.pem",
            "-set_serial", "0x" + secrets.token_hex(16), "-days", "7", "-sha256", "-extfile", extensions,
            "-out", directory / (name + ".pem"))
    key.chmod(0o444)
    (directory / (name + ".pem")).chmod(0o444)
    request.unlink()
    extensions.unlink()


def audience_mapper(audience):
    return {"name": "catalog-audience", "protocol": "openid-connect", "protocolMapper": "oidc-audience-mapper",
            "config": {"included.custom.audience": audience, "access.token.claim": "true", "id.token.claim": "false"}}


def client(identifier, audience, *, secret=None, redirect=None, service=False):
    value = {
        "clientId": identifier, "enabled": True, "protocol": "openid-connect",
        "publicClient": secret is None, "standardFlowEnabled": not service,
        "directAccessGrantsEnabled": False, "implicitFlowEnabled": False,
        "serviceAccountsEnabled": service,
        "defaultClientScopes": ["basic", *(["catalog.read"] if service else [])],
        "optionalClientScopes": [] if service else SCOPES,
        "protocolMappers": [audience_mapper(audience)],
        "attributes": {"access.token.lifespan": "300" if identifier.startswith("catalog-automation-") else "1800"},
    }
    if secret is not None:
        value["secret"] = secret
    if redirect:
        value["redirectUris"] = [redirect]
        value["attributes"]["pkce.code.challenge.method"] = "S256"
    return value


def bootstrap(database, passwords):
    roles = "\n".join(
        "CREATE ROLE " + role + " LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION "
        + ("NOBYPASSRLS" if role == "catalog_api" else "BYPASSRLS")
        + " PASSWORD '" + password + "';"
        for role, password in passwords.items()
    )
    return roles + "\n" + f"""
ALTER DATABASE {database} OWNER TO catalog_owner;
ALTER SCHEMA public OWNER TO catalog_owner;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO catalog_api, catalog_worker;
SET ROLE catalog_owner;
CREATE EXTENSION pgcrypto WITH SCHEMA public;
RESET ROLE;
DO $$
DECLARE owned_function record;
BEGIN
  FOR owned_function IN
    SELECT n.nspname, p.proname, pg_get_function_identity_arguments(p.oid) AS arguments
    FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
    JOIN pg_depend d ON d.classid='pg_proc'::regclass AND d.objid=p.oid AND d.deptype='e'
    JOIN pg_extension e ON e.oid=d.refobjid
    WHERE d.refclassid='pg_extension'::regclass AND e.extname='pgcrypto' AND n.nspname='public'
  LOOP
    EXECUTE format('ALTER FUNCTION %I.%I(%s) OWNER TO catalog_owner',
                   owned_function.nspname, owned_function.proname, owned_function.arguments);
  END LOOP;
END $$;
"""


def prepare(args):
    directory = Path(args.directory).expanduser().resolve()
    if not re.fullmatch(r"/[-a-zA-Z0-9_./]+", str(directory)) or ".." in directory.parts:
        raise ValueError("Use an absolute directory without spaces or shell metacharacters")
    if directory == ROOT or ROOT in directory.parents:
        raise ValueError("Private tutorial inputs must be outside the checkout")
    single = getattr(args, "single", False)
    network = ipaddress.ip_network(args.subnet)
    frontend = ipaddress.ip_address(args.frontend_address)
    private_ranges = [ipaddress.ip_network(value) for value in ("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16")]
    if network.version != 4 or not any(network.subnet_of(value) for value in private_ranges) or frontend not in network or frontend in (network.network_address, network.broadcast_address):
        raise ValueError("Select one private IPv4 subnet and a usable frontend address within it")
    directory.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    directory.mkdir(mode=0o700)
    trust = directory / "trust"
    trust.mkdir(mode=0o700)
    openssl("req", "-x509", "-newkey", "rsa:2048", "-noenc", "-days", "7", "-sha256",
            "-subj", "/CN=Local Catalog tutorial CA", "-addext", "basicConstraints=critical,CA:TRUE",
            "-addext", "keyUsage=critical,keyCertSign,cRLSign", "-keyout", trust / "ca-key.pem", "-out", trust / "ca.pem")
    (trust / "ca-key.pem").chmod(0o600)
    (trust / "ca.pem").chmod(0o444)
    for name, dns in (
        ("identity", ["identity.test"]), ("catalog-a", ["catalog-a.test"]),
        ("catalog-b", ["catalog-b.test"]), ("browse-a", ["browse-a.test"]),
        ("browse-b", ["browse-b.test"]), ("postgres-a", ["postgres-a"]), ("postgres-b", ["postgres-b"]),
    ):
        if not single or not name.endswith("-b"):
            certificate(trust, name, dns)
    project = "catalog-docs-" + secrets.token_hex(4)
    environment = {
        "COMPOSE_PROJECT_NAME": project,
        "CATALOG_TUTORIAL_DIRECTORY": str(directory),
        "CATALOG_TUTORIAL_SOURCE": str(Path(__file__).resolve().parent),
        "CATALOG_TUTORIAL_BACKEND_IMAGE": args.backend_image,
        "CATALOG_TUTORIAL_UI_IMAGE": args.ui_image,
        "CATALOG_TUTORIAL_FRONTEND_IMAGE": "open-agentic-catalog-tutorial-tls:" + project,
        "CATALOG_TUTORIAL_SUBNET": args.subnet,
        "CATALOG_TUTORIAL_FRONTEND_ADDRESS": args.frontend_address,
    }
    write(directory / "compose.env", "".join(name + "=" + json.dumps(value) + "\n" for name, value in environment.items()))
    write(directory / "bootstrap-password", secrets.token_urlsafe(32), 0o444)
    clients = []
    for name, port, ui_port, audience, tenant in (
        ("a", 8443, 9443, "open-agentic-catalog", "00000000-0000-7000-8000-000000000001"),
        ("b", 8445, 9445, "open-agentic-catalog-b", "00000000-0000-7000-8000-000000000002"),
    ):
        if single and name == "b":
            continue
        target = directory / name
        target.mkdir(mode=0o700)
        database = "catalog_" + name
        passwords = {role: secrets.token_hex(32) for role in ("catalog_owner", "catalog_api", "catalog_worker")}
        write(target / "postgres-password", secrets.token_hex(32), 0o444)
        write(target / "bootstrap.sql", bootstrap(database, passwords), 0o444)
        write(target / "pg_hba.conf", "local all all trust\nhostssl all all all scram-sha-256\nhostnossl all all all reject\n", 0o444)
        write(target / "cursor.key", base64.b64encode(secrets.token_bytes(32)).decode("ascii"), 0o444)
        policy = {"schemaVersion": 1, "policyId": "tutorial-" + name, "revision": 1, "public": {"mode": "disabled"}}
        if name == "b":
            policy["privateProfiles"] = [{
                "name": "tutorial-a", "origins": ["https://catalog-a.test:8443"],
                "addressRanges": [args.frontend_address + "/32"], "assignedTenantIds": [tenant],
                "transportRef": "direct", "trustBundleFile": "/trust/ca.pem",
            }]
        document(target / "federation-policy.json", policy, 0o644)
        if not single:
            credential_directory = target / "source-credentials"
            credential_directory.mkdir(mode=0o755)
        for role, filename in (("catalog_owner", "owner"), ("catalog_api", "api"), ("catalog_worker", "worker")):
            config = json.loads((ROOT / "config/production.example.json").read_text())
            config["profile"] = "tutorial-" + name
            config["serviceName"] = "catalog-" + name
            config["http"]["publicBaseUrl"] = f"https://catalog-{name}.test:{port}"
            config["http"]["corsOrigins"] = [f"https://browse-{name}.test:{ui_port}"]
            config["database"]["dsn"] = f"postgres://{role}@postgres-{name}:5432/{database}?sslmode=verify-full"
            config["auth"].update(issuer=ISSUER, audience=audience, dedicatedTenantId=tenant, trustBundleFile="/trust/ca.pem")
            config["discovery"]["hosts"] = [{"host": f"catalog-{name}.test", "tenantId": tenant,
                                               "mode": "authenticated", "publisherDomain": "tutorial.catalog.example" if name == "a" else "tutorial-b.catalog.example"}]
            config["federation"]["policyFile"] = f"/inputs/{name}/federation-policy.json"
            if name == "b":
                config["credentialResolution"] = {"enabled": True, "directory": "/credentials"}
            document(target / (filename + ".json"), config, 0o644)
            write(target / (filename + ".dsn"),
                  f"postgres://{role}:{passwords[role]}@postgres-{name}:5432/{database}?sslmode=verify-full&sslrootcert=/trust/ca.pem", 0o444)
        ui_secret = secrets.token_urlsafe(32)
        write(target / "ui-client-secret", ui_secret, 0o444)
        document(target / "ui-keys.json", [{"hash": secrets.token_hex(64), "block": secrets.token_hex(32)}])
        suffix = "" if name == "a" else "-b"
        document(target / "ui.json", {
            "listen": "0.0.0.0:8080", "environment": "production", "logLevel": "info", "logFormat": "json",
            "origin": f"https://browse-{name}.test:{ui_port}", "basePath": "",
            "upstream": f"https://catalog-{name}.test:{port}", "authMode": "oidc", "issuer": ISSUER,
            "clientId": "catalog-ui" + suffix, "clientSecretFile": f"/inputs/{name}/ui-client-secret",
            "issuerCAFile": "/trust/ca.pem", "apiCAFile": "/trust/ca.pem", "scopes": [],
            "cookieName": "catalog-" + name + "-session", "keysFile": f"/inputs/{name}/ui-keys.json", "assets": "/app/assets",
            "sessionSeconds": 3600, "requestBytes": 2097152, "responseBytes": 8388608, "timeoutSeconds": 30,
        }, 0o644)
        clients.extend([
            client("catalog-ui" + suffix, audience, secret=ui_secret, redirect=f"https://browse-{name}.test:{ui_port}/auth/callback"),
            client("catalog-cli" + suffix, audience, redirect="http://127.0.0.1/oauth/callback"),
            client("catalog-vscode" + suffix, audience, redirect="vscode://open-agentic-catalog.open-agentic-catalog/oidc/*"),
        ])
        if single:
            continue
        automation_secret = secrets.token_urlsafe(32)
        write(target / "automation-client-secret", automation_secret)
        clients.append(client("catalog-automation-" + name, audience, secret=automation_secret, service=True))
    if not single:
        source_secret = secrets.token_urlsafe(32)
        write(directory / "b" / "source-client-secret", source_secret)
        clients.append(client("catalog-source-b-to-a", "open-agentic-catalog", secret=source_secret, service=True))
    else:
        clients = [value for value in clients if value["clientId"] in ("catalog-ui", "catalog-cli")]
    users = []
    if single:
        for username in ("catalog-admin", "catalog-reader"):
            password = secrets.token_urlsafe(32)
            users.append({"id": str(uuid.uuid4()), "username": username, "enabled": True,
                          "firstName": "Catalog", "lastName": username, "email": username + "@example.invalid",
                          "emailVerified": True, "realmRoles": ["default-roles-catalog"], "credentials": [{"type": "password", "value": password, "temporary": False}]})
            write(directory / (username + "-initial-password"), password)
        document(directory / "accounts.json", {value["username"]: value["id"] for value in users}, 0o600)
    basic = {"name": "basic", "protocol": "openid-connect", "attributes": {"include.in.token.scope": "false"},
             "protocolMappers": [{"name": "subject", "protocol": "openid-connect", "protocolMapper": "oidc-sub-mapper",
                                  "config": {"access.token.claim": "true"}}]}
    scope_documents = [{"name": scope, "protocol": "openid-connect", "attributes": {
        "include.in.token.scope": "true", "display.on.consent.screen": "false"}} for scope in SCOPES]
    realm = {
        "realm": "catalog", "enabled": True, "sslRequired": "all", "registrationAllowed": False,
        "resetPasswordAllowed": False, "accessTokenLifespan": 1800, "clients": clients,
        "clientScopes": scope_documents if single else [basic, *scope_documents], "users": users,
    }
    if single:
        # Custom scope imports otherwise omit Keycloak's built-in account-console scopes.
        realm["attributes"] = {"CreateDefaultClientScopes": "true"}
    document(directory / "catalog-realm.json", realm)
    if single:
        document(directory / "single.json", {"format": 1, "ready": False}, 0o600)
    print("Prepared " + project + " in " + str(directory))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--directory", required=True)
    parser.add_argument("--backend-image", required=True)
    parser.add_argument("--ui-image", required=True)
    parser.add_argument("--subnet", default="172.29.245.0/24")
    parser.add_argument("--frontend-address", default="172.29.245.10")
    args = parser.parse_args()
    try:
        prepare(args)
    except (OSError, ValueError, subprocess.SubprocessError):
        raise SystemExit("Preparation failed; inspect only the selected private directory and prerequisites.") from None


if __name__ == "__main__":
    main()
