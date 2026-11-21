"""Check the evidence used to accept a native Flux reconciliation."""
import copy
import unittest
from unittest.mock import Mock

from fixtures.flux.handoff import Handoff, NEIGHBOR_HPA, VERSIONS


class ReconcileEvidenceTest(unittest.TestCase):
    def test_hpa_handoff_scopes_refusals_and_preserves_the_neighbor(self):
        spec = {"scaleTargetRef": {"kind": "Deployment", "name": "decoy-fixture"}, "maxReplicas": 1}
        neighbor = {"metadata": {"name": NEIGHBOR_HPA, "uid": "original"}, "spec": spec}
        runner = Mock(report={"neighborHPA": "original", "neighborHPASpec": spec})
        for kind, name, allowed in (("Deployment", "catalog-api", False), ("Deployment", "catalog-worker", False),
                                    ("Deployment", "other", True), ("StatefulSet", "catalog-api", True)):
            with self.subTest(kind=kind, name=name):
                other = {"metadata": {"name": "another-hpa"}, "spec": {"scaleTargetRef": {"kind": kind, "name": name}}}
                runner.get.return_value = {"items": [neighbor, other]}
                if allowed:
                    Handoff.check_hpas(runner)
                else:
                    with self.assertRaisesRegex(ValueError, "targeting HPA"):
                        Handoff.check_hpas(runner)
        for items in ([], [{**neighbor, "metadata": {"name": NEIGHBOR_HPA, "uid": "replacement"}}],
                      [{**neighbor, "spec": {**spec, "maxReplicas": 3}}],
                      [{**neighbor, "metadata": {**neighbor["metadata"], "deletionTimestamp": "now"}}]):
            with self.subTest(items=items), self.assertRaisesRegex(ValueError, "unrelated HPA"):
                runner.get.return_value = {"items": items}
                Handoff.check_hpas(runner)

    def test_owner_may_be_busy_even_with_zero_active_pods(self):
        runner = Mock()
        complete = {"status": {"conditions": [{"type": "Complete", "status": "True"}]}}
        failed = {"status": {"conditions": [{"type": "Failed", "status": "True"}]}}
        for jobs, pods, idle in (
            ([], [], True),
            ([complete, failed], [{"status": {"phase": "Failed"}}], True),
            ([{"status": {"active": 0}}], [], False),
            ([{"status": {"conditions": [{"type": "FailureTarget", "status": "True"}]}}], [], False),
            ([{"status": {"conditions": [{"type": "Complete", "status": "False"}]}}], [], False),
            ([], [{"metadata": {"deletionTimestamp": "now"}, "status": {"phase": "Running"}}], False),
            ([], [{"status": {"phase": "Pending"}}], False),
        ):
            with self.subTest(jobs=jobs, pods=pods):
                runner.get.side_effect = [{"items": jobs}, {"items": pods}]
                self.assertEqual(Handoff.owner_idle(runner), idle)

    def test_requires_current_generation_exact_history_and_completed_request(self):
        ns = "owned-namespace"
        target = VERSIONS["target"]
        history = {"version": 3, "chartVersion": target, "name": "catalog", "namespace": ns, "status": "deployed"}
        ready = {"type": "Ready", "status": "True", "observedGeneration": 7}
        obj = {"metadata": {"generation": 7}, "status": {"storageNamespace": ns, "observedGeneration": 7, "conditions": [ready],
               "history": [history], "lastHandledReconcileAt": "receipt"}}
        runner = Mock(ns=ns)
        runner.get.return_value = obj
        self.assertTrue(Handoff.acknowledged(runner, 3, target, "receipt"))
        cases = [
            ("status", "observedGeneration", 6),
            ("status", "storageNamespace", "other"),
            ("status", "conditions", [{**ready, "observedGeneration": 6}]),
            ("status", "lastHandledReconcileAt", "older"),
            ("status", "conditions", [ready, {"type": "Reconciling", "status": "True"}]),
            ("status", "conditions", [{"type": "Ready", "status": "Unknown"}]),
            ("status", "history", []),
        ]
        for field, value in (("version", 2), ("chartVersion", VERSIONS["initial"]), ("namespace", "other"),
                             ("name", "other"), ("status", "failed")):
            cases.append(("status", "history", [{**history, field: value}]))
        for part, key, changed in cases:
            with self.subTest(key=key, changed=changed):
                modified = copy.deepcopy(obj)
                modified[part][key] = changed
                runner.get.return_value = modified
                self.assertFalse(Handoff.acknowledged(runner, 3, target, "receipt"))

    def test_selection_refuses_mismatched_storage_source_and_remediation(self):
        strategy = {"strategy": {"name": "RemediateOnFailure"}, "remediation": {"retries": 0, "remediateLastFailure": False}}
        release = {"metadata": {"uid": "original"}, "spec": {"releaseName": "catalog", "targetNamespace": "owned", "storageNamespace": "owned",
                   "chartRef": {"kind": "HelmChart", "name": "target"}, "install": {**strategy, "serverSideApply": False},
                   "upgrade": {**strategy, "serverSideApply": "disabled"}}}
        runner = Mock(ns="owned", report={"chartDigests": {"target": "sha256:checked"}, "releaseUID": "original"})
        source = {"status": {"artifact": {"digest": "sha256:checked"}}}
        runner.get.side_effect = [release, source]
        self.assertEqual(release, Handoff.selected_release(runner, "target"))
        runner.get.side_effect = [{**release, "metadata": {"uid": "recreated"}}, source]
        with self.assertRaisesRegex(ValueError, "same release"):
            Handoff.selected_release(runner, "target")
        for path, value in ((("releaseName",), "other"), (("storageNamespace",), "other"), (("targetNamespace",), "other"),
                            (("chartRef", "name"), "initial"), (("install", "serverSideApply"), True),
                            (("upgrade", "serverSideApply"), "enabled"), (("upgrade", "remediation", "retries"), 1),
                            (("upgrade", "remediation", "remediateLastFailure"), True),
                            (("valuesFrom",), [{"kind": "ConfigMap", "name": "mutable-values"}]),
                            (("driftDetection",), {"mode": "enabled"})):
            with self.subTest(path=path):
                changed = copy.deepcopy(release)
                section = changed["spec"]
                for key in path[:-1]:
                    section = section[key]
                section[path[-1]] = value
                runner.get.side_effect = [changed, source]
                with self.assertRaises(ValueError):
                    Handoff.selected_release(runner, "target")
        runner.get.side_effect = [release, {"status": {"artifact": {"digest": "sha256:other"}}}]
        with self.assertRaisesRegex(ValueError, "source no longer matches"):
            Handoff.selected_release(runner, "target")

    def test_refuses_ambient_context_before_reading_or_mutating(self):
        with self.assertRaisesRegex(ValueError, "requires an owned Flux scenario"):
            Handoff("minikube", "default", ".", "helm", "kubectl", "values.json", "{}")
