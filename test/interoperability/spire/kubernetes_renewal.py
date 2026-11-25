"""Qualify the application chart's Secret projection with an independently renewing SPIRE writer."""
import base64
from datetime import datetime
import ipaddress
import json
import sys
import time

from fixture import API, DOMAIN, TENANTS, until
from scenario import ME, check, member, refusal
from renew import LEAF
from source_renewal import create_source, finish, indexed_snapshot, inspect_source, preserve, queue, tested_revision
from tools import HERE
from tutorial_oidc import A, check_source_refusal

PEER = "open-agentic-catalog-e2e-peer"
BINDING = "catalog-a-reader"


def renewal_manifest(k, base):
    directory = k.args.work / "manifests"
    directory.mkdir()
    k.kubectl("create", "serviceaccount", "source-writer")
    (directory / "base.yaml").write_text(base)
    (directory / "writer.yaml").write_bytes((HERE / "kubernetes-writer.yaml").read_bytes())
    frontend = {"apiVersion": "v1", "kind": "Pod", "metadata": {"name": "frontend"},
                "spec": {"containers": [{"name": "frontend", "env": [{"name": "CATALOG_TUTORIAL_SINGLE", "value": "0"}]}]}}
    (directory / "kustomization.yaml").write_text(json.dumps({"resources": ["base.yaml"],
        "patches": [{"path": "writer.yaml"}, {"patch": json.dumps(frontend)}]}))
    for name, spec in (("api-b", {"type": "ExternalName", "externalName": "catalog." + PEER + ".svc.cluster.local"}),
                       ("catalog-b", {"selector": {"app": "tls-frontend"}, "ports": [{"port": 8445}]})):
        k.kubectl("create", "-f", "-", data=json.dumps({"apiVersion": "v1", "kind": "Service",
                  "metadata": {"name": name}, "spec": spec}).encode())
    return k.kubectl("kustomize", str(directory)).decode()


def setup(k):
    k.kubectl("create", "secret", "generic", BINDING, namespace=PEER)
    k.kubectl("create", "role", "source-writer", "--resource=secrets", "--resource-name=" + BINDING,
              "--verb=get,patch", namespace=PEER)
    k.kubectl("create", "rolebinding", "source-writer", "--role=source-writer",
              "--serviceaccount=test:source-writer", namespace=PEER)
    account = "system:serviceaccount:test:source-writer"
    for verb, name, expected in (("get", BINDING, True), ("patch", BINDING, True), ("list", "", False),
                                 ("get", "catalog-api-database", False), ("patch", "catalog-api-database", False)):
        review = {"apiVersion": "authorization.k8s.io/v1", "kind": "SubjectAccessReview", "spec": {
            "user": account, "groups": ["system:authenticated", "system:serviceaccounts", "system:serviceaccounts:test"],
            "resourceAttributes": {"namespace": PEER, "verb": verb, "resource": "secrets", "name": name}}}
        result = json.loads(k.kubectl("create", "-f", "-", "-o", "json", data=json.dumps(review).encode()))
        check(result["status"]["allowed"] is expected, "Unexpected writer access to Secret in importer namespace")
    ip = ipaddress.ip_address(k.kubectl("get", "service", "catalog-a", "-o", "jsonpath={.spec.clusterIP}").decode())
    policy = {"schemaVersion": 1, "policyId": "spire-import", "revision": 1, "public": {"mode": "disabled"},
              "privateProfiles": [{"name": "tutorial-a", "origins": [A], "addressRanges": [f"{ip}/{ip.max_prefixlen}"],
                                   "assignedTenantIds": [TENANTS["b"]], "transportRef": "direct",
                                   "trustBundleFile": "/etc/open-agentic-catalog/federation/trust/ca.pem"}]}
    k.kubectl("create", "-f", "-", namespace=PEER, data=json.dumps({"apiVersion": "v1", "kind": "ConfigMap",
              "metadata": {"name": "spire-source-policy"}, "data": {"policy.json": json.dumps(policy)}}).encode())
    k.kubectl("create", "configmap", "spire-source-ca", "--from-file=" + str(k.args.work / "trust/ca.pem"), namespace=PEER)

    k.server("entry", "create", "-parentID", DOMAIN + "agent", "-spiffeID", DOMAIN + "source-reader",
             "-selector", "unix:uid:11007", "-jwtSVIDTTL", "300")
    until(lambda: generation(k))  # The first generation exists when the two Helm Pods mount the Secret.


def generation(k):
    # One Secret read supplies the binding and retained filenames. Never emit token values.
    template = ('{{with .data}}{{with index . "catalog-a-reader.json"}}{{.}}{{"\\n"}}{{end}}'
                '{{range $key, $_ := .}}{{$key}}{{"\\n"}}{{end}}{{end}}')
    lines = k.kubectl("get", "secret", BINDING, "-o", "go-template=" + template, namespace=PEER).decode().splitlines()
    if not lines:
        return None
    metadata = json.loads(base64.b64decode(lines[0], validate=True))
    check(metadata["tenantId"] == TENANTS["b"] and metadata["origin"] == A, "Unexpected projected scope")
    issued = {name.removesuffix(".token") for name in lines[1:] if LEAF.fullmatch(name)}
    revision = metadata["revision"]
    check(revision in issued and metadata["secretFile"] == revision + ".token", "Active binding has no matching leaf")
    return revision, datetime.fromisoformat(metadata["expiresAt"]).timestamp(), issued


def pods(k, enabled):
    state = {}
    for role in ("api", "worker"):
        items = json.loads(k.kubectl("get", "pods", "-l", "app.kubernetes.io/component=" + role,
                                     "-o", "json", namespace=PEER))["items"]
        check(len(items) == 1, "Expected one importer Pod for " + role)
        pod = items[0]
        container, = pod["spec"]["containers"]
        status, = pod["status"]["containerStatuses"]
        mounts = {v["name"]: v for v in container["volumeMounts"]}
        check(status["ready"] and "workload" not in mounts and "kube-api" not in mounts,
              "Importer workload authority or readiness changed")
        if enabled:
            mount = mounts["federation-credentials"]
            volume, = [v for v in pod["spec"]["volumes"] if v["name"] == mount["name"]]
            check(mount["readOnly"] and "subPath" not in mount and volume["secret"]["secretName"] == BINDING,
                  "Importer needs a read-only whole Secret volume")
        else:
            check("federation-credentials" not in mounts, "Disabled importer still mounts the credential")
        state[role] = (pod["metadata"]["uid"], status["containerID"], status["restartCount"])
    return state


def writer_identity(k):
    pod = json.loads(k.kubectl("get", "pod", "workloads", "-o", "json"))
    spec = pod["spec"]
    check(spec["serviceAccountName"] == "source-writer" and spec["automountServiceAccountToken"] is False,
          "SPIRE Pod unexpectedly shares the writer credential")
    owners = [c["name"] for c in spec["containers"] if any(m["name"] == "kube-api" for m in c.get("volumeMounts", []))]
    check(owners == ["source-reader"], "Unexpected Kubernetes credential mount")
    status, = [c for c in pod["status"]["containerStatuses"] if c["name"] == "source-reader"]
    check(status["ready"], "Source writer is unavailable")
    return (pod["metadata"]["uid"], status["containerID"], status["restartCount"])


def importer(k):
    b = API(k.args.work, "b", sys.stdin.read().strip(), loopback_port=k.args.peer_port)
    me, _ = b.request("GET", "/api/v1/me")
    check(me["tenantId"] == TENANTS["b"] and me["subject"] == "oidc-other-admin", "Unexpected importer operator")
    return b


def await_projection(operation):
    # A healthy Pod may still serve the previous generation until its kubelet refreshes the volume.
    # An unexpected revision, a failed Job or a broken API assertion fails immediately.
    deadline = time.monotonic() + 120
    while time.monotonic() < deadline:
        value = operation()
        if value is not None:
            return value
        time.sleep(3)
    raise TimeoutError("Mounted source credential did not advance")


def projected(k, *observed):
    state = generation(k)
    check(state is not None, "Writer binding disappeared during qualification")
    current, expiry, issued = state
    check(all(revision in issued for revision in observed), "Catalog used an unknown credential revision")
    if all(revision == current for revision in observed):
        return current, expiry
    return None


def connect_projected(k, b):
    def inspect():
        inspection = inspect_source(b)
        return inspection if projected(k, inspection["credentialRevision"]) else None

    return create_source(b, await_projection(inspect))


def synchronize_projected(k, b, sid, published):
    def observe():
        api_revision = tested_revision(b, sid)
        if not projected(k, api_revision):
            return None
        snapshot = indexed_snapshot(b, sid, published)
        agreed = projected(k, api_revision, snapshot["credentialRevision"])
        return (*agreed, snapshot) if agreed else None

    return await_projection(observe)


def qualify(k):
    a = k.client(k.issue("admin"))
    membership = member(a, "source-reader", "service_client")
    role, _ = a.request("POST", "/api/v1/access/roles", {"name": "Source publication reader", "permissions": ["discovery.read"],
                        "reason": "Read the approved publication"}, 201)
    a.request("POST", "/api/v1/access/assignments", {"roleDefinitionId": role["id"], "membershipId": membership["membershipId"],
              "reason": "Read this publication"}, 201)
    b = importer(k)
    refusal(k.client(b.token), ME, 401, "UNAUTHENTICATED")
    refusal(API(k.args.work, "b", a.token, loopback_port=k.args.peer_port), ME, 401, "UNAUTHENTICATED")
    initial_pods = pods(k, True)
    writer = writer_identity(k)
    published = json.loads((k.args.work / "records.json").read_text())["consumer"][2]
    sid = connect_projected(k, b)
    first, expiry, initial = synchronize_projected(k, b, sid, published)

    def successor():
        current = generation(k)
        return current if current and current[1] > expiry and current[0] != first else None
    until(successor, timeout=320)
    second, later_expiry, renewed = synchronize_projected(k, b, sid, published)
    check(second != first and later_expiry > expiry, "Expected an independently renewed credential")
    check(renewed["snapshotId"] != initial["snapshotId"] and pods(k, True) == initial_pods,
          "Renewal must advance the snapshot without restarting either consumer")
    check(writer_identity(k) == writer, "Writer restarted during automatic renewal")
    result = {"writer": writer, "sourceId": sid, "identifier": published, "initial": initial, "renewed": renewed, "pods": initial_pods}
    (k.args.work / "renewal.json").write_text(json.dumps(result))


def disabled(k):
    b = importer(k)
    check(b.request("GET", "/api/v1/capabilities")[0]["optionalCapabilities"]["credentialResolution"] == "disabled",
          "Credential resolution remained enabled")
    pods(k, False)
    check(generation(k)[1] > time.time(), "Disabled control needs an unexpired upstream credential")
    report = json.loads((k.args.work / "renewal.json").read_text())
    sid = report["sourceId"]
    check_source_refusal(b, sid)
    job = finish(b, queue(b, sid)[0])
    check(job["state"] == "dead_letter" and job["attempt"] == job["attemptLimit"] == 2 and job.get("result") is None,
          f"Disabled bound import ended in {job['state']} after {job['attempt']}/{job['attemptLimit']} attempts")
    preserve(b, sid, report["renewed"], report["identifier"])
