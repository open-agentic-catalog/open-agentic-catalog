#!/usr/bin/env python3
"""One independent HTTPS access-token canary; not an identity product harness."""

import base64
import json
import os
from pathlib import Path
import secrets
import shlex
import time

from federation import Client, key
from http_client import ClientFailure, decode
from journeys import header
from runner import Failure, command, port, write_document, OIDC_CASES


TENANT = "00000000-0000-7000-8000-000000000001"


def realm_template(secret):
    realm = decode(Path("test/functional/oidc-realm.json").read_bytes())
    # Separate ordinary clients exercise issuer-issued claims, without an
    # Admin API, password flow, custom provider or token-signing shortcut.
    for name, audience, organization in (("catalog", "open-agentic-catalog", "owned"),
                                          ("unknown", "open-agentic-catalog", "owned"),
                                          ("wrong-audience", "not-catalog", "owned"),
                                          ("wrong-organization", "open-agentic-catalog", "unknown")):
        realm["clients"].append({
            "clientId": name, "secret": secret, "enabled": True, "protocol": "openid-connect",
            "publicClient": False, "serviceAccountsEnabled": True, "standardFlowEnabled": False,
            "directAccessGrantsEnabled": False, "implicitFlowEnabled": False,
            "defaultClientScopes": [], "optionalClientScopes": [],
            "protocolMappers": [
                {"name": "catalog-audience", "protocol": "openid-connect", "protocolMapper": "oidc-audience-mapper",
                 "config": {"included.custom.audience": audience, "access.token.claim": "true", "id.token.claim": "false"}},
                {"name": "organization", "protocol": "openid-connect", "protocolMapper": "oidc-hardcoded-claim-mapper",
                 "config": {"claim.name": "organization_id", "claim.value": organization,
                            "jsonType.label": "String", "access.token.claim": "true", "id.token.claim": "false"}},
            ]})
    return realm


class OIDCJourney:
    def __init__(self):
        self.env = dict(os.environ)
        self.fixture = Path(self.env["CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY"])
        self.state = Path(self.env["CATALOG_FUNCTIONAL_STATE_DIRECTORY"])
        self.project = "open-agentic-catalog-functional-" + self.env["CATALOG_FUNCTIONAL_SUFFIX"]
        self.base = self.env["CATALOG_FUNCTIONAL_BASE_URL"]
        self.port = str(port())
        self.origin = "https://catalog-oidc:" + self.port
        self.issuer = self.origin + "/realms/catalog"
        self.completed = []
        self.phase = "setup"
        self.env.update(IMAGE="open-agentic-catalog:functional-" + self.env["CATALOG_FUNCTIONAL_SUFFIX"],
                        VERSION="functional", COMPOSE_PROGRESS="quiet", COMMIT=self.env["CATALOG_FUNCTIONAL_COMMIT"],
                        CATALOG_COMPOSE_HTTP_PORT=self.env["CATALOG_FUNCTIONAL_HTTP_PORT"],
                        CATALOG_COMPOSE_POSTGRES_PORT="0", CATALOG_COMPOSE_SUBNET=self.env["CATALOG_FUNCTIONAL_SUBNET"],
                        CATALOG_COMPOSE_FEDERATION_POLICY_FILE=str(self.fixture / "policy.json"),
                        CATALOG_COMPOSE_FEDERATION_TRUST_DIRECTORY=str(self.fixture / "trust"),
                        CATALOG_FUNCTIONAL_OIDC_PORT=self.port,
                        CATALOG_FUNCTIONAL_OIDC_HOSTS=json.dumps([
                            {"host": "127.0.0.1", "tenantId": TENANT, "mode": "anonymous", "publisherDomain": "catalog.example.com"},
                            {"host": "private.catalog.test", "tenantId": TENANT, "mode": "authenticated", "publisherDomain": "catalog.example.com"},
                            {"host": "disabled.catalog.test", "tenantId": TENANT, "mode": "disabled", "publisherDomain": "catalog.example.com"}]))

    def record(self, name):
        if name != OIDC_CASES[len(self.completed)]:
            raise Failure("oidc-case-order")
        self.completed.append(name)
        write_document(self.state / "oidc-cases.json", {"completed": self.completed})

    def configure_catalog(self, mode, scopes=False):
        auth = {"mode": "oidc", "issuer": self.issuer, "audience": "open-agentic-catalog", "tenancyMode": mode,
                "enforceScopes": scopes, "trustBundleFile": "/fixture/trust/functional-ca.pem"}
        auth["dedicatedTenantId" if mode == "dedicated" else "organizationClaim"] = TENANT if mode == "dedicated" else "organization_id"
        path = self.fixture / "oidc-catalog.json"
        write_document(path, {"environment": "production", "profile": "functional-oidc", "auth": auth})
        path.chmod(0o444)

    def compose(self, *args, capture=False):
        argv = shlex.split(self.env.get("CATALOG_FUNCTIONAL_COMPOSE_COMMAND", "docker compose"))
        argv += ["--env-file", "/dev/null", "-f", "compose.yaml", "-f", "test/functional/compose.yaml"]
        argv += ["-f", "test/functional/compose.oidc.yaml"]
        return command(argv + ["-p", self.project, *args], self.env, 120, capture=capture, limit=1 << 20)

    def realm_command(self, action, *args):
        return decode(self.compose("run", "--rm", "--no-deps", "--entrypoint", "/usr/local/bin/catalogctl", "migrate",
                                   "identity", "realms", action, "--config", "/config/config.json", "--tenant", TENANT, *args, capture=True))

    def provider(self, path, client=None):
        argv = ["curl", "--silent", "--show-error", "--fail", "--max-time", "10", "--max-filesize", str(1 << 20),
                "--noproxy", "*", "--cacert", str(self.fixture / "trust/functional-ca.pem"),
                "--resolve", "catalog-oidc:" + self.port + ":127.0.0.1", self.issuer + path]
        if client:
            argv += ["--data-urlencode", "grant_type=client_credentials", "--data-urlencode", "client_id=" + client,
                     "--data-urlencode", "client_secret@" + str(self.fixture / "oidc-client-secret")]
        return decode(command(argv, self.env, 15, limit=1 << 20, capture=True))

    def token(self, client="catalog"):
        token = self.provider("/protocol/openid-connect/token", client)["access_token"]
        if not isinstance(token, str) or not 0 < len(token) <= 32768 or len(token.split(".")) != 3:
            raise Failure("oidc-token-shape")
        return token

    def provision(self, token, mode, organization=""):
        # This reads only our newly issued subject for the explicit operator
        # bootstrap. The Catalog independently verifies the token on requests.
        encoded = token.split(".")[1]
        claims = decode(base64.urlsafe_b64decode(encoded + "=" * (-len(encoded) % 4)))
        args = ["run", "--rm", "--no-deps", "--entrypoint", "/usr/local/bin/catalogctl", "migrate",
                "identity", "provision", "--config", "/config/config.json", "--tenant", TENANT,
                "--tenant-name", "Tenant " + TENANT, "--issuer", self.issuer, "--tenancy-mode", mode,
                "--subject", claims["sub"], "--actor-type", "service", "--roles", "tenant_admin,publisher,reader",
                "--operator", "owned-functional-oidc", "--reason", "Independent provider qualification"]
        if organization:
            args += ["--external-organization", organization]
        return decode(self.compose(*args, capture=True))

    def ready(self):
        deadline = time.monotonic() + 60
        api = Client(self.base)
        while time.monotonic() < deadline:
            try:
                api.request("GET", "/readyz", retry_unavailable=True)
                version, _ = api.request("GET", "/version")
                if version.get("commit") != self.env["CATALOG_FUNCTIONAL_COMMIT"] or version.get("version") != "functional":
                    raise Failure("oidc-catalog-build-drift")
                return
            except (ClientFailure, AssertionError):
                time.sleep(0.2)
        raise Failure("oidc-catalog-startup")

    def run(self):
        self.configure_catalog("dedicated")
        secret = secrets.token_urlsafe(32)
        write_document(self.fixture / "catalog-realm.json", realm_template(secret))
        # The private invocation parent is 0700; exact container-mounted leaves
        # must be readable by the upstream non-root UID, not host-owned 0600.
        (self.fixture / "catalog-realm.json").chmod(0o444)
        for name, value in (("oidc-client-secret", secret), ("oidc-cursor-key", base64.b64encode(secrets.token_bytes(32)).decode())):
            path = self.fixture / name
            path.write_text(value, encoding="ascii")
            path.chmod(0o444)
        configuration = decode(self.compose("config", "--format", "json", capture=True))
        reference = configuration["services"]["oidc"]["image"]
        engine = shlex.split(self.env.get("CATALOG_FUNCTIONAL_ENGINE_COMMAND", "docker"))
        if not command(engine + ["image", "ls", "--quiet", "--no-trunc", reference], self.env, 10, capture=True).strip():
            self.compose("pull", "oidc")
        self.compose("up", "--detach", "--no-deps", "oidc")
        deadline = time.monotonic() + 120
        while True:
            try:
                discovery = self.provider("/.well-known/openid-configuration")
                break
            except Failure:
                if time.monotonic() >= deadline:
                    raise Failure("oidc-provider-startup") from None
                time.sleep(0.5)
        if discovery.get("issuer") != self.issuer or discovery.get("jwks_uri") != self.issuer + "/protocol/openid-connect/certs":
            raise Failure("oidc-discovery-identity")
        keys = self.provider("/protocol/openid-connect/certs")["keys"]
        token = self.token()
        encoded = token.split(".")[0]
        token_header = decode(base64.urlsafe_b64decode(encoded + "=" * (-len(encoded) % 4)))
        if token_header.get("kid") not in {value.get("kid") for value in keys}:
            raise Failure("oidc-key-binding")
        self.record("provider-startup")
        self.phase = "provision"
        provisioned = self.provision(token, "dedicated")
        self.phase = "provision-replay"
        if self.provision(token, "dedicated") != provisioned:
            raise Failure("oidc-provision-replay")
        self.phase = "dedicated-startup"
        self.compose("up", "--detach", "--no-deps", "--no-build", "api")
        self.ready()
        self.phase = "capabilities"
        api = Client(self.base, token)
        api.request("GET", "/api/v1/capabilities")
        access, _ = api.request("GET", "/api/v1/me")
        if access["membershipId"] != provisioned["membershipId"] or access["actorType"] != "service" or access["scopeEnforcement"]:
            raise Failure("oidc-self-access")
        self.phase = "membership"
        membership, _ = api.request("GET", "/api/v1/memberships/" + provisioned["membershipId"])
        if membership["actorType"] != "service":
            raise Failure("oidc-service-membership")
        self.phase = "token-rejection"
        # Provision this subject so wrong-audience refusal cannot accidentally
        # pass merely because the subject has no Catalog membership.
        self.provision(self.token("wrong-audience"), "dedicated")
        for client in ("unknown", "wrong-audience"):
            Client(self.base, self.token(client)).request("GET", "/api/v1/capabilities", expected=(401,))
        self.record("dedicated-mapping")
        self.phase = "discovery"
        # The production profile changes the advertised scheme from the
        # development seed. An immutable snapshot must match that configuration;
        # publish a fresh one through the ordinary reviewed control boundary.
        snapshot, _ = api.request("POST", "/api/v1/publication/snapshots", {"host": "127.0.0.1"}, (201,), {"Idempotency-Key": key("public-snapshot")})
        api.request("POST", "/api/v1/publication/snapshots/" + snapshot["id"] + ":activate",
                    {"host": "127.0.0.1", "reason": "Owned production discovery configuration"},
                    headers={"Idempotency-Key": key("public-activate"), "If-Match": snapshot["etag"]})
        # Real-token issuer/audience changes must not be accepted by signature
        # bypass; malformed and time/cache matrices stay in verifier unit tests.
        public, public_headers = Client(self.base).request("GET", "/.well-known/ard.json")
        with_token, token_headers = api.request("GET", "/.well-known/ard.json")
        if public != with_token or header(public_headers, "ETag") != header(token_headers, "ETag"):
            raise Failure("oidc-anonymous-widened")
        for host in ("disabled.catalog.test", "unknown.catalog.test"):
            api.request("GET", "/.well-known/ard.json", expected=(404,), headers={"Host": host})
        Client(self.base).request("GET", "/.well-known/ard.json", expected=(401,), headers={"Host": "private.catalog.test"})
        snapshot, _ = api.request("POST", "/api/v1/publication/snapshots", {"host": "private.catalog.test"}, (201,), {"Idempotency-Key": key("private-snapshot")})
        api.request("POST", "/api/v1/publication/snapshots/" + snapshot["id"] + ":activate",
                    {"host": "private.catalog.test", "reason": "Owned authenticated discovery"},
                    headers={"Idempotency-Key": key("activate"), "If-Match": snapshot["etag"]})
        _, private_headers = api.request("GET", "/.well-known/ard.json", headers={"Host": "private.catalog.test"})
        if "private" not in (header(private_headers, "Cache-Control") or ""):
            raise Failure("oidc-private-cache")
        self.record("discovery-isolation")
        self.phase = "membership-lifecycle"
        # A second service account is administered by the first, so suspension
        # and offboarding cannot accidentally depend on disabling our operator.
        second = self.token("unknown")
        target = self.provision(second, "dedicated")
        member_path = "/api/v1/memberships/" + target["membershipId"]
        self.phase = "membership-lifecycle-read"
        current, h = api.request("GET", member_path)
        for action, expected_state in (("suspend", "suspended"), ("reactivate", "active"), ("offboard", "offboarded")):
            self.phase = "membership-lifecycle-" + action
            current, h = api.request("POST", member_path + ":" + action, {"reason": "Owned identity lifecycle"},
                                    headers={"Idempotency-Key": key(action), "If-Match": header(h, "ETag")})
            if current["membership"]["state"] != expected_state:
                raise Failure("oidc-membership-state")
            self.phase = "membership-lifecycle-" + action + "-readback"
            retained, h = api.request("GET", member_path)
            if retained["state"] != expected_state or retained["revision"] != current["membership"]["revision"]:
                raise Failure("oidc-membership-readback")
            self.phase = "membership-lifecycle-" + action + "-access"
            Client(self.base, second).request("GET", "/api/v1/capabilities", expected=(200,) if action == "reactivate" else (401,))
            Client(self.base, second).request("GET", "/api/v1/me", expected=(200,) if action == "reactivate" else (401,))
        self.record("membership-lifecycle")
        self.phase = "shared-mapping"
        shared = self.provision(token, "shared", "owned")
        self.configure_catalog("shared")
        self.compose("up", "--detach", "--no-deps", "--no-build", "--force-recreate", "api")
        self.ready()
        api.request("GET", "/api/v1/capabilities")
        self.provision(self.token("wrong-organization"), "shared", "owned")
        Client(self.base, self.token("wrong-organization")).request("GET", "/api/v1/capabilities", expected=(401,))
        self.record("shared-mapping")
        self.phase = "realm-operations"
        page = self.realm_command("list", "--limit", "100")
        realm = next(item for item in page["items"] if item["id"] == shared["realmId"])
        for action, state in (("suspend", "suspended"), ("reactivate", "active")):
            arguments = ("--realm", realm["id"], "--revision", str(realm["revision"]),
                         "--operator", "owned-functional-oidc", "--reason", "Owned realm lifecycle")
            result = self.realm_command(action, *arguments)
            realm = result["realm"]
            if realm["state"] != state or result["tenantAccessSuspended"]:
                raise Failure("oidc-realm-transition")
            # A stale command must fail, not mutate or append another transition.
            try:
                self.realm_command(action, *arguments)
            except Failure as error:
                if str(error) != "child-failed":
                    raise
            else:
                raise Failure("oidc-realm-stale-accepted")
            retained = next(item for item in self.realm_command("list", "--limit", "100")["items"] if item["id"] == realm["id"])
            if retained != realm:
                raise Failure("oidc-realm-readback")
            api.request("GET", "/api/v1/capabilities", expected=(401,) if action == "suspend" else (200,))
        self.record("realm-operations")
        self.phase = "provider-outage"
        token = self.token()
        api = Client(self.base, token)
        api.request("GET", "/api/v1/capabilities")
        self.compose("stop", "--timeout", "10", "oidc")
        api.request("GET", "/api/v1/capabilities")
        invalid_header = {**token_header, "kid": "owned-unknown-key"}
        invalid_token = base64.urlsafe_b64encode(json.dumps(invalid_header).encode()).decode().rstrip("=") + "." + token.split(".", 1)[1]
        # An unknown key requires a refresh. While the issuer is stopped, this
        # is unavailable verification, not a completed signature rejection.
        problem, _ = Client(self.base, invalid_token).request("GET", "/api/v1/capabilities", expected=(503,))
        if problem["code"] != "DEPENDENCY_UNAVAILABLE":
            raise Failure("oidc-unknown-key-outage-classification")
        Client(self.base).request("GET", "/readyz")
        self.compose("start", "oidc")
        deadline = time.monotonic() + 60
        while True:
            try:
                token = self.token()
                break
            except Failure:
                if time.monotonic() >= deadline:
                    raise Failure("oidc-provider-recovery") from None
                time.sleep(0.5)
        api = Client(self.base, token)
        api.request("GET", "/api/v1/capabilities")
        # With key retrieval available, the same forged token is invalid.
        problem, _ = Client(self.base, invalid_token).request("GET", "/api/v1/capabilities", expected=(401,))
        if problem["code"] != "UNAUTHENTICATED":
            raise Failure("oidc-unknown-key-recovery-classification")
        self.record("provider-outage")
        self.phase = "scope-ceiling"
        self.configure_catalog("shared", scopes=True)
        self.compose("up", "--detach", "--no-deps", "--no-build", "--force-recreate", "api")
        self.ready()
        api.request("POST", "/api/v1/namespaces", {"slug": "scope-denied", "displayName": "Denied"}, (403,), {"Idempotency-Key": key("scope")})
        access, _ = api.request("GET", "/api/v1/me")
        if not access["scopeEnforcement"] or "tenant.admin" in access["scopes"] or access["membershipId"] != shared["membershipId"]:
            raise Failure("oidc-self-scope-ceiling")
        self.record("scope-ceiling")
        self.phase = "identity-report"
        print("Independent HTTPS provider, tenancy, membership/realm, outage, discovery and scope cases passed.")
        identifier = self.compose("ps", "-q", "oidc", capture=True).decode().strip()
        engine = shlex.split(self.env.get("CATALOG_FUNCTIONAL_ENGINE_COMMAND", "docker"))
        digest = command(engine + ["container", "inspect", "--format", "{{.Image}}", identifier], self.env, 10, capture=True).decode().strip()
        # Read the pin from the resolved overlay, never maintain a second pin.
        configuration = decode(self.compose("config", "--format", "json", capture=True))
        reference = configuration["services"]["oidc"]["image"]
        pinned = reference.split("@", 1)[1]
        image_info = decode(command(engine + ["image", "inspect", reference], self.env, 10, capture=True))[0]
        if image_info["Id"] != digest or not any(item.endswith("@" + pinned) for item in image_info["RepoDigests"]):
            raise Failure("oidc-provider-image-drift")
        write_document(self.state / "oidc.json", {"configurationDigest": digest, "indexDigest": pinned})


if __name__ == "__main__":
    journey = OIDCJourney()
    try:
        journey.run()
    except Exception:
        write_document(journey.state / "oidc-failure.json", {"phase": journey.phase})
        raise
