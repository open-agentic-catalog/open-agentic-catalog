"""Exercise one standalone Flux HelmRelease inside the Minikube harness's private cluster."""
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time
from uuid import uuid4


FLUX = "flux-system"
CHART = "open-agentic-catalog"
RELEASE = "catalog"
VERSIONS = {"initial": "0.1.0-flux.1", "target": "0.1.0-flux.2"}
HR = "helmrelease/catalog"
HPA = "catalog-flux-api"
NEIGHBOR_HPA = "catalog-flux-neighbor"
QUOTA = "flux-resume-no-jobs"
OWNER_SELECTOR = "app.kubernetes.io/component=migration,app.kubernetes.io/instance=catalog"


def require(condition, message):
    if not condition:
        raise ValueError(message)


def command(args, data=None, refusal=None):
    result = subprocess.run(args, input=data, text=True, capture_output=True, timeout=240, check=False)
    if refusal:
        require(result.returncode and refusal in result.stderr, "expected native refusal: " + refusal)
    elif result.returncode:
        # Do not log requests, manifests or command lines containing fixture values.
        raise RuntimeError(f"{Path(args[0]).name} {args[1]} failed: {result.stderr.strip()[:800]}")
    return result.stdout


def wait_for(description, read, deadline=150):
    end = time.monotonic() + deadline
    while time.monotonic() < end:
        value = read()
        if value:
            return value
        time.sleep(1)
    raise TimeoutError(description)


class Handoff:
    def __init__(self, profile, ns, work, helm, kubectl, values, image_info):
        self.work = Path(work)
        require(re.fullmatch(r"open-agentic-catalog-flux-[a-f0-9]{12}", profile)
                and ns == "open-agentic-catalog-e2e" and os.environ.get("MINIKUBE_PROFILE") == profile
                and Path(os.environ["KUBECONFIG"]) == self.work / "kubeconfig", "requires an owned Flux scenario")
        self.images = json.loads(image_info)
        self.ns = ns
        self.profile = profile
        self.helm = [helm, "--kube-context", profile, "--kubeconfig", os.environ["KUBECONFIG"], "-n", ns]
        self.kube = [kubectl, "--kubeconfig", os.environ["KUBECONFIG"], "--context", profile]
        self.values_file = Path(values)
        self.charts = self.work / "charts"
        self.receipt = self.work / "flux-result.json"
        self.report = json.loads(self.receipt.read_text()) if self.receipt.exists() else {}

    def k(self, *args, namespace=None, **kwargs):
        return command([*self.kube, "-n", namespace or self.ns, *args], **kwargs)

    def get(self, resource, *args, namespace=None):
        return json.loads(self.k("get", resource, *args, "-o", "json", namespace=namespace))

    def apply(self, kind, name, spec, api="v1", namespace=None):
        manifest = {"apiVersion": api, "kind": kind, "metadata": {"name": name}, **spec}
        self.k("apply", "-f", "-", data=json.dumps(manifest), namespace=namespace)

    def patch(self, spec):
        self.k("patch", HR, "--type=merge", "-p", json.dumps({"spec": spec}))

    def history(self):
        return json.loads(command([*self.helm, "history", RELEASE, "-o", "json"]))

    def acknowledged(self, revision, version, request=None):
        obj = self.get(HR)
        status = obj.get("status", {})
        conditions = status.get("conditions", [])
        history = status.get("history", [])
        return (status.get("observedGeneration") == obj["metadata"]["generation"] and status.get("storageNamespace") == self.ns
                and (not request or status.get("lastHandledReconcileAt") == request)
                and any(c["type"] == "Ready" and c["status"] == "True" and c.get("observedGeneration") == obj["metadata"]["generation"] for c in conditions)
                and not any(c["type"] == "Reconciling" and c["status"] == "True" for c in conditions)
                and history and history[0]["version"] == revision and history[0]["chartVersion"] == version
                and history[0]["name"] == RELEASE and history[0]["namespace"] == self.ns
                and history[0]["status"] == "deployed")

    def controller_receipts(self):
        raw = self.k("logs", "deployment/helm-controller", "--tail=500", namespace=FLUX)
        events = [json.loads(line) for line in raw.splitlines() if line.startswith("{")]
        return {e["reconcileID"] for e in events if e.get("name") == RELEASE and e.get("namespace") == self.ns
                and e.get("msg") == "reconciliation is suspended for this object"}

    def owner_idle(self):
        jobs = self.get("jobs", "-l", OWNER_SELECTOR)["items"]
        pods = self.get("pods", "-l", OWNER_SELECTOR)["items"]
        return (all(any(c["type"] in ("Complete", "Failed") and c["status"] == "True"
                        for c in job.get("status", {}).get("conditions", [])) for job in jobs)
                and all(p.get("status", {}).get("phase") in ("Succeeded", "Failed") for p in pods))

    def wait_for_suspension(self, previous):
        receipt = wait_for("helm-controller did not observe suspension", lambda: self.controller_receipts() - previous)
        history = self.history()
        require(self.get(HR)["spec"]["suspend"] and history[-1]["status"] in ("deployed", "failed"),
                "release is not suspended with a completed Helm operation")
        wait_for("owner migration Job or Pod still pending", self.owner_idle)
        self.report["suspensionReconciles"] = [*self.report.get("suspensionReconciles", []), sorted(receipt)[0]]
        return history[-1]["revision"]

    def suspend(self):
        # This version returns before updating status on suspension. Its per-object
        # reconciles are serialized; observe a new completed suspension decision.
        previous = self.controller_receipts()
        self.patch({"suspend": True})
        return self.wait_for_suspension(previous)

    def install(self):
        self.charts.mkdir()
        exported = Path(".tools/minikube-flux/controllers.yaml").read_text()
        pins = json.loads(Path(__file__).with_name("pins.json").read_text())["images"]
        aliases = self.images["aliases"]
        for name in ("helm-controller", "source-controller"):
            original = "image: " + pins[name].split("@")[0] + "\n"
            require(exported.count(original) == 1, "unexpected controller export")
            exported = exported.replace(original, "image: " + aliases[name] + "\n")
        exported = exported.replace("imagePullPolicy: IfNotPresent", "imagePullPolicy: Never")
        command([*self.kube, "apply", "--server-side", "-f", "-"], data=exported)
        for name in ("helm-controller", "source-controller"):
            self.k("rollout", "status", "deployment/"+name, "--timeout=3m", namespace=FLUX)
        for version in VERSIONS.values():
            command([*self.helm, "package", "deployments/helm/"+CHART, "--version", version, "-d", str(self.charts)])
        command([*self.helm, "repo", "index", str(self.charts)])
        self.k("create", "configmap", "catalog-charts", "--from-file="+str(self.charts), namespace=FLUX)
        self.k("patch", "configmap/catalog-charts", "--type=merge", "-p", '{"immutable":true}', namespace=FLUX)
        server = {"app": "catalog-charts"}
        http = {
            "name": "http", "image": aliases["chart-server"], "imagePullPolicy": "Never",
            "command": ["python", "-I", "-B", "-m", "http.server", "8080", "--directory", "/charts"],
            "securityContext": {"allowPrivilegeEscalation": False, "readOnlyRootFilesystem": True,
                                "runAsNonRoot": True, "runAsUser": 65532, "capabilities": {"drop": ["ALL"]}},
            "volumeMounts": [{"name": "files", "mountPath": "/charts", "readOnly": True}],
        }
        pod = {"automountServiceAccountToken": False, "containers": [http],
               "volumes": [{"name": "files", "configMap": {"name": "catalog-charts"}}]}
        self.apply("Deployment", "catalog-charts", {
            "spec": {"selector": {"matchLabels": server}, "template": {"metadata": {"labels": server}, "spec": pod}},
        }, "apps/v1", FLUX)
        self.apply("Service", "catalog-charts", {"spec": {"selector": server, "ports": [{"port": 80, "targetPort": 8080}]}}, namespace=FLUX)
        self.k("rollout", "status", "deployment/catalog-charts", "--timeout=2m", namespace=FLUX)
        self.apply("HelmRepository", "catalog-charts", {"spec": {"url": "http://catalog-charts.flux-system.svc", "interval": "1h"}}, "source.toolkit.fluxcd.io/v1")
        for name, label in (("source-controller", "app=source-controller"), ("helm-controller", "app=helm-controller"), ("chart-server", "app=catalog-charts")):
            pods = self.get("pods", "-l", label, namespace=FLUX)["items"]
            require(len(pods) == 1 and pods[0]["spec"]["containers"][0]["image"] == aliases[name]
                    and pods[0]["status"]["containerStatuses"][0]["imageID"] == self.images["ids"][name+"-config"],
                    "wrong running "+name+" image")
        self.report["chartDigests"] = {}
        for key, version in VERSIONS.items():
            self.apply("HelmChart", key, {"spec": {"chart": CHART, "version": version, "interval": "1h",
                       "sourceRef": {"kind": "HelmRepository", "name": "catalog-charts"}}}, "source.toolkit.fluxcd.io/v1")
            self.k("wait", "helmchart/"+key, "--for=condition=Ready", "--timeout=180s")
            digest = "sha256:" + hashlib.sha256((self.charts / (CHART+"-"+version+".tgz")).read_bytes()).hexdigest()
            require(self.get("helmchart/"+key)["status"]["artifact"]["digest"] == digest, "Flux source bytes differ from coordinator chart")
            self.report["chartDigests"][key] = digest
        strategy = {"strategy": {"name": "RemediateOnFailure"}, "remediation": {"retries": 0, "remediateLastFailure": False}}
        # Hold the first hook before it can start a Pod. Only this disposable
        # application namespace is limited; the Flux controllers keep running.
        self.apply("ResourceQuota", "hold-first-install", {"spec": {"hard": {"pods": "0"}}})
        self.k("wait", "quota/hold-first-install", "--for=jsonpath={.status.hard.pods}=0", "--timeout=30s")
        self.apply("HelmRelease", RELEASE, {"spec": {"releaseName": RELEASE, "targetNamespace": self.ns, "storageNamespace": self.ns,
                   "chartRef": {"kind": "HelmChart", "name": "initial"}, "values": json.loads(self.values_file.read_text()),
                   "interval": "1h", "timeout": "3m", "install": {**strategy, "serverSideApply": False},
                   "upgrade": {**strategy, "serverSideApply": "disabled"}}}, "helm.toolkit.fluxcd.io/v2")
        self.k("wait", "--for=create", "job/catalog-migrate", "--timeout=90s")
        previous = self.controller_receipts()
        self.patch({"suspend": True})
        pending = self.history()[-1]
        require(self.get(HR)["spec"]["suspend"] and pending["status"] == "pending-install" and not self.owner_idle(),
                "suspension must be exercised while the initial Helm install and migration are still pending")
        self.report["pendingInstallRevision"] = pending["revision"]
        self.k("delete", "quota/hold-first-install")
        require(self.wait_for_suspension(previous) == 1 and self.history()[-1]["status"] == "deployed",
                "initial install did not finish before handoff")
        self.patch({"suspend": False})
        wait_for("initial Flux install did not finish", lambda: self.acknowledged(1, VERSIONS["initial"]), 210)
        self.report.update(namespace=self.ns, release=RELEASE, releaseUID=self.get(HR)["metadata"]["uid"],
                           initialRevision=1, controllerVersion="v1.7.1", sourceVersion="v1.10.1")

    def selected_release(self, stage):
        obj = self.get(HR)
        spec = obj["spec"]
        require(obj["metadata"]["uid"] == self.report["releaseUID"] and spec["releaseName"] == RELEASE and spec["targetNamespace"] == self.ns
                and spec["storageNamespace"] == self.ns and spec["chartRef"] == {"kind": "HelmChart", "name": stage},
                "Flux and the coordinator must select the same release and chart")
        require(not spec.get("valuesFrom") and spec.get("driftDetection", {}).get("mode", "disabled") == "disabled",
                "handoff requires frozen inline values and disabled drift detection")
        require(not spec["install"]["serverSideApply"] and spec["upgrade"]["serverSideApply"] == "disabled",
                "this handoff requires client-side apply from the initial install")
        for action in ("install", "upgrade"):
            require(spec[action]["strategy"]["name"] == "RemediateOnFailure"
                    and spec[action]["remediation"]["retries"] == 0 and not spec[action]["remediation"]["remediateLastFailure"],
                    "unexpected retry or remediation policy")
        require(self.get("helmchart/"+stage)["status"]["artifact"]["digest"] == self.report["chartDigests"][stage],
                "Flux source no longer matches the frozen chart archive")
        return obj

    def runtime(self, initial_image=None):
        result = {}
        image = initial_image or json.loads(self.values_file.read_text())["image"]
        ids = self.images["ids"]
        require(ids["application"] != ids["flux-target-config"], "test images must be distinguishable")
        expected_id = ids["application"] if initial_image else ids["flux-target-config"]
        for role in ("api", "worker"):
            deploy = self.get("deployment/catalog-"+role)
            annotations = deploy["metadata"]["annotations"]
            require(annotations["meta.helm.sh/release-name"] == RELEASE and annotations["meta.helm.sh/release-namespace"] == self.ns,
                    "unexpected workload release ownership")
            pods = self.get("pods", "-l", "app.kubernetes.io/instance=catalog,app.kubernetes.io/component="+role)["items"]
            require(len(pods) == 2 and deploy["status"]["readyReplicas"] == 2, "expected two ready target Pods")
            result[role] = sorted([p["metadata"]["uid"], p["status"]["containerStatuses"][0]["imageID"]] for p in pods)
            require(all(p["spec"]["containers"][0]["image"] == image["repository"]+":"+image["tag"]
                        and p["status"]["containerStatuses"][0]["imageID"] == expected_id
                        and p["status"]["containerStatuses"][0]["ready"] and not p["metadata"].get("deletionTimestamp") for p in pods),
                    "unexpected workload image or readiness")
        return result

    def add_hpa(self, name, deployment, replicas):
        self.apply("HorizontalPodAutoscaler", name, {"spec": {"scaleTargetRef": {"apiVersion": "apps/v1", "kind": "Deployment", "name": deployment},
                   "minReplicas": replicas, "maxReplicas": replicas, "metrics": [{"type": "Resource", "resource": {"name": "cpu",
                   "target": {"type": "Utilization", "averageUtilization": 70}}}]}}, "autoscaling/v2")
        return self.get("hpa/"+name)

    def check_hpas(self):
        inventory = self.get("hpa")["items"]
        for item in inventory:
            target = item["spec"]["scaleTargetRef"]
            require(target["kind"] != "Deployment" or target["name"] not in ("catalog-api", "catalog-worker"),
                    "targeting HPA returned during maintenance")
        neighbor = next((item for item in inventory if item["metadata"]["name"] == NEIGHBOR_HPA), None)
        require(neighbor and neighbor["metadata"]["uid"] == self.report["neighborHPA"]
                and not neighbor["metadata"].get("deletionTimestamp") and neighbor["spec"] == self.report["neighborHPASpec"],
                "maintenance removed, replaced or changed the unrelated HPA")

    def pause(self):
        initial = self.selected_release("initial")
        require(not initial["metadata"].get("ownerReferences") and self.acknowledged(1, VERSIONS["initial"])
                and json.loads(command([*self.helm, "get", "values", RELEASE, "-o", "json"])) == initial["spec"]["values"],
                "initial release is not ready at the reviewed Flux revision")
        before = self.runtime(initial["spec"]["values"]["image"])
        self.report["initialPods"] = before
        neighbor = self.add_hpa(NEIGHBOR_HPA, "decoy-fixture", 1)
        self.report["neighborHPA"] = neighbor["metadata"]["uid"]
        self.report["neighborHPASpec"] = neighbor["spec"]
        hpa = self.add_hpa(HPA, "catalog-api", 2)
        self.report["originalHPA"] = hpa["metadata"]["uid"]
        self.report["hpaSpec"] = hpa["spec"]
        require(self.suspend() == 1, "unexpected Flux initial release")
        args = [str(self.work/"catalog-deploy"), "--context", self.profile, "--namespace", self.ns, "--release", RELEASE,
                "--chart", str(self.charts/(CHART+"-"+VERSIONS["target"]+".tgz")), "--values", str(self.values_file),
                "--helm", self.helm[0], "--kubectl", self.kube[0], "--check"]
        command(args, refusal="suspend and remove targeting HPA")
        require(self.runtime(initial["spec"]["values"]["image"]) == before, "failed preflight replaced live workloads")
        self.k("delete", "hpa/"+HPA)
        self.check_hpas()
        self.patch({"chartRef": {"kind": "HelmChart", "name": "target"}, "values": json.loads(self.values_file.read_text()),
                    "upgrade": {"disableHooks": True}})
        command(args)
        self.report["suspendedGeneration"] = self.selected_release("target")["metadata"]["generation"]

    def guard(self):
        self.apply("ResourceQuota", QUOTA, {"spec": {"hard": {"count/jobs.batch": "0"}}})
        wait_for("Job quota was not enforced", lambda: self.get("quota/"+QUOTA).get("status", {}).get("hard", {}).get("count/jobs.batch") == "0")
        self.k("create", "job", "catalog-quota-probe", "--image=unused", "--dry-run=server", refusal="exceeded quota: "+QUOTA)

    def check_target(self, status="deployed"):
        revision = self.history()[-1]
        require(revision["status"] == status and revision["chart"] == CHART+"-"+VERSIONS["target"],
                "expected the frozen target chart at a "+status+" revision")
        desired = self.selected_release("target")["spec"]
        require(desired["suspend"], "release must remain suspended")
        require(json.loads(command([*self.helm, "get", "values", RELEASE, "-o", "json"])) ==
                desired["values"] == json.loads(self.values_file.read_text()), "release inputs do not agree")
        self.check_hpas()
        return revision["revision"]

    def verify_adoption(self, revision, pods, request=None):
        wait_for("Flux did not adopt the frozen target", lambda: self.acknowledged(revision, VERSIONS["target"], request))
        obj = self.selected_release("target")
        require(not obj["spec"].get("suspend") and obj["spec"]["upgrade"]["disableHooks"],
                "expected an active HelmRelease with upgrade hooks disabled")
        latest = self.history()[-1]
        require(latest["revision"] == revision and latest["status"] == "deployed" and latest["chart"] == CHART+"-"+VERSIONS["target"],
                "Helm history disagrees with the revision adopted by Flux")
        values = json.loads(command([*self.helm, "get", "values", RELEASE, "-o", "json"]))
        require(values == obj["spec"]["values"] == json.loads(self.values_file.read_text()), "adopted values differ from the frozen target")
        require(self.runtime() == pods, "adoption replaced a target Pod")
        self.check_hpas()

    def resume(self):
        revision = self.check_target()
        obj = self.selected_release("target")
        require(obj["metadata"]["generation"] == self.report["suspendedGeneration"] and obj["status"]["history"][0]["version"] == 1
                and obj["spec"]["upgrade"]["disableHooks"], "Flux authority changed during coordinated maintenance")
        pods = self.runtime()
        self.guard()
        self.patch({"suspend": False})
        self.verify_adoption(revision+1, pods)
        self.report["coordinatorRevision"] = revision
        self.report["fluxResumeRevision"] = revision+1
        self.report["resumedPods"] = pods

    def restore_hooks(self, revision, pods):
        self.patch({"upgrade": {"disableHooks": False}})
        token = str(uuid4())
        self.k("annotate", HR, "reconcile.fluxcd.io/requestedAt="+token, "--overwrite")
        wait_for("restored hook policy was not reconciled", lambda: self.acknowledged(revision, VERSIONS["target"], token))
        latest = self.history()[-1]
        require(latest["revision"] == revision and latest["status"] == "deployed"
                and not self.selected_release("target")["spec"]["upgrade"].get("disableHooks")
                and self.runtime() == pods,
                "restoring normal hooks changed the adopted target")
        self.check_hpas()

    def restore_hpa(self):
        hpa = self.add_hpa(HPA, "catalog-api", 2)
        require(hpa["spec"] == self.report["hpaSpec"], "restored HPA differs from reviewed configuration")
        return hpa["metadata"]["uid"]

    def pause_control(self):
        self.report["restoredHPA"] = self.restore_hpa()
        require(self.report["originalHPA"] != self.report["restoredHPA"], "HPA was not restored explicitly")

        require(self.suspend() == self.report["fluxResumeRevision"], "unexpected revision before negative control")
        self.k("delete", "hpa/"+HPA)
        self.k("delete", "quota/"+QUOTA)

    def hooks_control(self):
        revision = self.check_target()
        pods = self.runtime()
        self.guard()
        self.patch({"suspend": False})
        def refused():
            obj = self.get(HR)
            return any(c["type"] == "Ready" and c["status"] == "False"
                       and c.get("observedGeneration") == obj["metadata"]["generation"] and "catalog-migrate" in c["message"]
                       and "exceeded quota: "+QUOTA in c["message"] for c in obj.get("status", {}).get("conditions", []))
        wait_for("ordinary resume did not attempt the owner migration Job", refused)
        require(self.suspend() == revision+1 and self.history()[-1]["status"] == "failed" and self.runtime() == pods,
                "failed resume unexpectedly rolled back, restarted or uninstalled the release")
        self.check_hpas()
        self.report["blockedHooksEnabledRevision"] = revision+1
        self.report["blockedPods"] = pods

    def verify_failed_handback(self):
        revision = self.check_target("failed")
        require(revision == self.report["blockedHooksEnabledRevision"] and self.runtime() == self.report["blockedPods"],
                "refused handback changed while suspended")
        status = self.get(HR)["status"]
        latest = status["history"][0]
        require(latest["version"] == revision and latest["status"] == "failed"
                and any(c["type"] == "Ready" and c["status"] == "False" and c["reason"] == "UpgradeFailed"
                        for c in status["conditions"]), "Flux lost the failed upgrade while suspended")
        require(not self.get("jobs", "-l", OWNER_SELECTOR)["items"], "expected the quota to have refused the owner Job")
        return revision

    def controller_uid(self):
        pods = self.get("pods", "-l", "app=helm-controller", namespace=FLUX)["items"]
        require(len(pods) == 1 and not pods[0]["metadata"].get("deletionTimestamp"), "expected one active helm-controller Pod")
        container = pods[0]["status"]["containerStatuses"][0]
        require(container["ready"] and container["imageID"] == self.images["ids"]["helm-controller-config"],
                "helm-controller is not ready on the pinned image")
        return pods[0]["metadata"]["uid"]

    def stop_controller(self):
        self.verify_failed_handback()
        self.report["controllerPodBeforeRestart"] = self.controller_uid()
        self.k("scale", "deployment/helm-controller", "--current-replicas=1", "--replicas=0", namespace=FLUX)
        wait_for("helm-controller Pod still present", lambda: not self.get("pods", "-l", "app=helm-controller", namespace=FLUX)["items"])
        self.verify_failed_handback()

    def restart_controller(self):
        self.k("scale", "deployment/helm-controller", "--current-replicas=0", "--replicas=1", namespace=FLUX)
        self.k("rollout", "status", "deployment/helm-controller", "--timeout=3m", namespace=FLUX)
        replacement = self.controller_uid()
        require(replacement != self.report["controllerPodBeforeRestart"], "helm-controller Pod was not replaced")
        # The old Pod is gone. Require the replacement's own observation of
        # this suspended release before allowing the operator to reset retries.
        revision = self.wait_for_suspension(set(self.report["suspensionReconciles"]))
        require(revision == self.verify_failed_handback(), "controller restart changed the failed Helm revision")
        self.report["controllerPodAfterRestart"] = replacement
        self.report["suspendedRevisionAfterRestart"] = revision

    def recover_handback(self):
        revision = self.verify_failed_handback()
        pods = self.report["blockedPods"]

        # Retry the known admission refusal once. Keep the Job quota in force
        # while Flux adopts the already migrated target and restores normal hooks.
        request = str(uuid4())
        annotations = {"reconcile.fluxcd.io/requestedAt": request, "reconcile.fluxcd.io/resetAt": request}
        self.k("patch", HR, "--type=merge", "-p", json.dumps({
            "metadata": {"annotations": annotations}, "spec": {"suspend": False, "upgrade": {"disableHooks": True}},
        }))
        recovered = revision+1
        self.verify_adoption(recovered, pods, request)
        require(self.get(HR)["status"].get("lastHandledResetAt") == request, "Flux did not acknowledge the operator retry")
        self.restore_hooks(recovered, pods)
        self.report["recoveredRevision"] = recovered
        self.report["recoveredHPA"] = self.restore_hpa()
        self.k("delete", "quota/"+QUOTA)

    def run(self, phase):
        if phase == "result":
            require(self.report["initialRevision"] < self.report["coordinatorRevision"] < self.report["fluxResumeRevision"]
                    < self.report["blockedHooksEnabledRevision"] < self.report["recoveredRevision"]
                    and self.report["suspendedRevisionAfterRestart"] == self.report["blockedHooksEnabledRevision"]
                    and len({self.report[key] for key in ("originalHPA", "restoredHPA", "recoveredHPA")}) == 3,
                    "incomplete Flux handoff evidence")
            print(json.dumps(self.report))
            return
        phases = {"install": self.install, "pause": self.pause, "resume": self.resume,
                  "restore-hooks": lambda: self.restore_hooks(self.report["fluxResumeRevision"], self.report["resumedPods"]),
                  "pause-control": self.pause_control, "hooks-control": self.hooks_control, "stop-controller": self.stop_controller,
                  "restart-controller": self.restart_controller, "recover-handback": self.recover_handback}
        phases[phase]()
        self.receipt.write_text(json.dumps(self.report))


if __name__ == "__main__":
    Handoff(*sys.argv[2:]).run(sys.argv[1])
