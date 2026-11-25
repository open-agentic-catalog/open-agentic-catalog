"""Exercise real SPIRE credentials with the Helm chart in an invocation-owned Minikube."""
import argparse
import json
import re
import shutil
import subprocess
from pathlib import Path

from fixture import API, AUDIENCE, DOMAIN, ISSUER, TENANTS, UIDS, build_binaries, certificates, until
from scenario import ME, MANIFEST, check, key, member, private_resource, read_private, refusal
from tools import HERE
from tutorial_oidc import activate_publication, claims, publish_reviewed_release


def build(args):
    args.work.mkdir(mode=0o700)
    # Only public fixture code enters the image. Generate private keys afterwards.
    directory = args.work / "bin"
    directory.mkdir()
    build_binaries(directory, "amd64")
    base = f"FROM {args.base}\n"
    hosts = [("identity", "identity.test"), ("catalog-a", "catalog-a.test")]
    if args.renewal:
        # Only the selected test image includes the interpreter and Secret writer.
        base = (f"FROM {args.base} AS catalog\nFROM python:3.13.18-bookworm@sha256:"
                "cc55e4ded9c95f6ce2133b3d096db43f214d280c2a8af65d83e246c8fa7d168c\n"
                "COPY --from=catalog /usr/local/bin/catalogctl /usr/local/bin/catalogctl\n")
        for name in ("renew.py", "kubernetes_writer.py"):
            shutil.copyfile(HERE / name, directory / name)
        hosts.append(("catalog-b", "catalog-b.test"))
    subprocess.run([args.docker, "build", "--network=none", "--pull=false", "-t", args.image, "-f", "-", directory],
                   input=(base + "COPY . /opt/\n").encode(), check=True, timeout=300)
    (args.work / "trust").mkdir(mode=0o700)
    certificates(args.work, hosts)


class Kubernetes:
    def __init__(self, args):
        self.args = args

    def kubectl(self, *args, namespace="test", data=None):
        cmd = [self.args.kubectl, "--kubeconfig", self.args.kubeconfig, "--context", self.args.context,
               "-n", namespace, *args]
        result = subprocess.run(cmd, input=data, capture_output=True, timeout=130)
        if result.returncode:
            # kubectl output can contain an SVID or Secret. Only expose the operation.
            raise ValueError("Kubernetes fixture command failed: " + " ".join(args[:3]))
        return result.stdout

    def exec(self, pod, container, *args):
        return self.kubectl("exec", pod, "-c", container, "--", *args)

    def server(self, *args):
        return self.exec("server", "server", "/opt/spire-server", *args)

    def issue(self, name, audience=AUDIENCE):
        raw = self.exec("workloads", name, "/opt/spire-agent", "api", "fetch", "jwt", "-socketPath",
                        "/workload/api.sock", "-spiffeID", DOMAIN + name, "-audience", audience, "-output", "json")
        svids = json.loads(raw)[0]["svids"]
        check(len(svids) == 1 and svids[0]["spiffe_id"] == DOMAIN + name, "Unexpected workload identity")
        token = svids[0]["svid"]
        body = claims(token)
        check(body["iss"] == ISSUER and body["sub"] == DOMAIN + name and body["aud"] in (audience, [audience]),
              "Unexpected signed workload claims")
        return token

    def start(self):
        args, trust = self.args, self.args.work / "trust"
        self.kubectl("create", "namespace", "test")
        self.kubectl("create", "configmap", "spire-config",
                     *["--from-file=" + str(HERE / f) for f in ("server.conf", "agent.conf", "provider.conf")])
        for namespace in ("test", args.namespace):
            self.kubectl("create", "configmap", "spire-issuer-ca", "--from-file=" + str(trust / "ca.pem"), namespace=namespace)
        for secret, filenames in (("issuer-tls", ["identity"]), ("frontend-tls", ["catalog-a", "catalog-b"] if args.renewal else ["catalog-a"])):
            self.kubectl("create", "secret", "generic", secret,
                         *["--from-file=" + str(trust / (filename + suffix)) for filename in filenames for suffix in (".pem", "-key.pem")])
        manifest = (HERE / "kubernetes.yaml").read_text()
        if args.renewal:
            from kubernetes_renewal import renewal_manifest
            manifest = renewal_manifest(self, manifest)
        self.kubectl("create", "-f", "-", data=manifest.replace("SPIRE_FIXTURE_IMAGE", args.image)
                     .replace("CATALOG_NAMESPACE", args.namespace).encode())
        until(lambda: self.server("healthcheck"))
        bundle = self.server("bundle", "show", "-format", "pem").decode()
        join = json.loads(self.server("token", "generate", "-spiffeID", DOMAIN + "agent", "-output", "json"))["value"]
        for name in ("reporter", "admin", "reviewer", "issuer"):
            self.server("entry", "create", "-parentID", DOMAIN + "agent", "-spiffeID", DOMAIN + name,
                        "-selector", "unix:uid:" + str(UIDS[name]), "-jwtSVIDTTL", "900")
        secret = {"apiVersion": "v1", "kind": "Secret", "metadata": {"name": "agent-bootstrap"},
                  "stringData": {"bundle.pem": bundle, "join": join}}
        self.kubectl("create", "-f", "-", data=json.dumps(secret).encode())
        token = until(lambda: self.issue("reporter"), timeout=120)
        until(lambda: self.exec("workloads", "reporter", "/opt/reader", "--issuer-key", key(token)) == b"")
        pods = json.loads(self.kubectl("get", "pods", "server", "frontend", "workloads", "-o", "json"))
        for pod in pods["items"]:
            for container in pod["spec"]["containers"]:
                resources = container["resources"]
                check({"cpu", "memory", "ephemeral-storage"} <= resources.get("requests", {}).keys()
                      and {"memory", "ephemeral-storage"} <= resources.get("limits", {}).keys(),
                      "Missing SPIRE fixture resource budget for " + container["name"])

    def client(self, token):
        return API(self.args.work, "a", token, loopback_port=self.args.port)

    def unselected(self):
        until(lambda: self.client("").request("GET", "/readyz"))
        refusal(self.client(self.issue("admin")), ME, 401, "UNAUTHENTICATED")

    def reads(self):
        admin = self.client(self.issue("admin"))
        me, _ = until(lambda: admin.request("GET", ME))
        check((me["subject"], me["actorType"], me["tenantId"]) == (DOMAIN + "admin", "service", TENANTS["a"]),
              "Catalog resolved the wrong administrator")
        reporter = self.client(self.issue("reporter"))
        refusal(reporter, ME, 401, "UNAUTHENTICATED")
        for role in ("reader", "reviewer"):
            member(admin, "reviewer", role)
        published, _ = publish_reviewed_release(admin, self.client(self.issue("reviewer")))
        activate_publication(admin, published)
        refusal(self.client(""), MANIFEST, 401, "UNAUTHENTICATED")
        private = private_resource(admin, "private-reporter")
        other = private_resource(admin, "private-other")
        membership = member(admin, "reporter", "reader", namespaceId=private["namespaceId"])
        read_private(reporter, private)
        refusal(reporter, "/api/v1/resources/" + other["id"], 403, "FORBIDDEN")
        refusal(reporter, MANIFEST, 403, "FORBIDDEN")
        refusal(self.client(self.issue("reporter", "other-service")), ME, 401, "UNAUTHENTICATED")
        role, _ = admin.request("POST", "/api/v1/access/roles", {"name": "Publication reader",
                                "permissions": ["discovery.read"], "reason": "Read authenticated publication"}, 201)
        assignment, etag = admin.request("POST", "/api/v1/access/assignments", {"roleDefinitionId": role["id"],
                                         "membershipId": membership["membershipId"], "reason": "Read publication"}, 201)
        self.exec("workloads", "reporter", "/opt/reader", private["id"], private["canonicalIdentifier"], published)
        admin.request("POST", "/api/v1/access/assignments/" + assignment["id"] + ":revoke",
                      {"reason": "Withdraw discovery"}, etag=etag)
        refusal(reporter, MANIFEST, 403, "FORBIDDEN")
        read_private(reporter, private)
        # Keep only object references for an optional retained lab, never credentials.
        references = {"consumer": [private["id"], private["canonicalIdentifier"], published],
                      "roleDefinitionId": role["id"], "membershipId": membership["membershipId"]}
        (self.args.work / "records.json").write_text(json.dumps(references))

    def resume(self):
        references = json.loads((self.args.work / "records.json").read_text())
        admin = self.client(self.issue("admin"))
        until(lambda: admin.request("GET", ME))
        admin.request("POST", "/api/v1/access/assignments", {"roleDefinitionId": references["roleDefinitionId"],
                      "membershipId": references["membershipId"], "reason": "Restore the lab reader"}, 201)
        self.exec("workloads", "reporter", "/opt/reader", *references["consumer"])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("phase", choices=("build", "start", "unselected", "reads", "resume", "source-setup", "source-renewal", "source-disabled"))
    parser.add_argument("--work", type=Path, required=True)
    for option in ("image", "base", "docker", "kubectl", "kubeconfig", "context", "namespace"):
        parser.add_argument("--" + option, required=True)
    parser.add_argument("--port", type=int)
    parser.add_argument("--peer-port", type=int)
    parser.add_argument("--renewal", action="store_true")
    args = parser.parse_args()
    if (not re.fullmatch(r"open-agentic-catalog-spire-[0-9a-f]{12}", args.context)
            or args.namespace != "open-agentic-catalog-e2e"
            or Path(args.kubeconfig) != args.work.parent / "kubeconfig"):
        parser.error("use the private SPIRE Minikube harness")
    if args.phase == "build":
        build(args)
    else:
        fixture = Kubernetes(args)
        if args.phase.startswith("source-") and args.renewal:
            from kubernetes_renewal import qualify, setup, disabled
            {"source-setup": setup, "source-renewal": qualify, "source-disabled": disabled}[args.phase](fixture)
            return
        {"unselected": fixture.unselected, "reads": fixture.reads, "start": fixture.start, "resume": fixture.resume}[args.phase]()


if __name__ == "__main__":
    main()
