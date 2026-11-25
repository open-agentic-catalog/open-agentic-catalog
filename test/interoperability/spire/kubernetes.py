"""Exercise real SPIRE credentials with the Helm chart in an invocation-owned Minikube."""
import argparse
import json
import re
import subprocess
from pathlib import Path

from fixture import API, AUDIENCE, DOMAIN, ISSUER, TENANTS, UIDS, build_binaries, certificates, until
from scenario import ME, MANIFEST, check, key, member, private_resource, read_private, refusal
from tools import HERE
from tutorial_oidc import activate_publication, claims, publish_reviewed_release


def build(args):
    args.work.mkdir(mode=0o700)
    # The image build context contains binaries only. Generate private keys afterwards.
    directory = args.work / "bin"
    directory.mkdir()
    build_binaries(directory, "amd64")
    subprocess.run([args.docker, "build", "--network=none", "--pull=false", "-t", args.image, "-f", "-", directory],
                   input=f"FROM {args.base}\nCOPY . /opt/\n".encode(), check=True, timeout=300)
    (args.work / "trust").mkdir(mode=0o700)
    certificates(args.work, (("identity", "identity.test"), ("catalog-a", "catalog-a.test")))


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
        for secret, filename in (("issuer-tls", "identity"), ("frontend-tls", "catalog-a")):
            self.kubectl("create", "secret", "generic", secret,
                         *["--from-file=" + str(trust / (filename + suffix)) for suffix in (".pem", "-key.pem")])
        manifest = (HERE / "kubernetes.yaml").read_text()
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
    parser.add_argument("phase", choices=("build", "start", "unselected", "reads", "resume"))
    parser.add_argument("--work", type=Path, required=True)
    for option in ("image", "base", "docker", "kubectl", "kubeconfig", "context", "namespace"):
        parser.add_argument("--" + option, required=True)
    parser.add_argument("--port", type=int)
    args = parser.parse_args()
    if (not re.fullmatch(r"open-agentic-catalog-spire-[0-9a-f]{12}", args.context)
            or args.namespace != "open-agentic-catalog-e2e"
            or Path(args.kubeconfig) != args.work.parent / "kubeconfig"):
        parser.error("use the private SPIRE Minikube harness")
    if args.phase == "build":
        build(args)
    else:
        fixture = Kubernetes(args)
        {"unselected": fixture.unselected, "reads": fixture.reads, "start": fixture.start, "resume": fixture.resume}[args.phase]()


if __name__ == "__main__":
    main()
