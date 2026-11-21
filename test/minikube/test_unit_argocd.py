"""Admission checks for a standalone native maintenance operation."""

import copy
import unittest

from fixtures.argocd import admission as campaign


class ApplicationAdmissionTest(unittest.TestCase):
    def setUp(self):
        self.sha = "1" * 40
        self.obj = {
            "spec": campaign.application("catalog-test", self.sha),
            "metadata": {},
        }

    def validate(self, obj):
        campaign.validate_app(obj, "catalog-test", self.sha)

    def test_manual_exact_source_allowed_with_completed_previous_operation(self):
        self.obj["status"] = {"operationState": {"phase": "Succeeded"}}
        self.validate(self.obj)

    def test_native_omitted_zero_limit_preserves_manual_sync_only(self):
        self.obj["spec"]["syncPolicy"]["retry"] = {}
        self.validate(self.obj)
        self.obj["spec"]["syncPolicy"]["retry"] = {"limit": 1}
        with self.assertRaisesRegex(ValueError, "manual policy"):
            self.validate(self.obj)

    def test_implicit_automation_overrides_and_revision_drift_are_refused(self):
        for key, change in [
            ("syncPolicy", {"automated": {}, "retry": {"limit": 0}}),
            ("syncPolicy", {"automated": {"enabled": None}, "retry": {"limit": 0}}),
            ("syncPolicy", {"automated": {"enabled": True}, "retry": {"limit": 0}}),
            ("syncPolicy", {**self.obj["spec"]["syncPolicy"], "syncOptions": ["ApplyOutOfSyncOnly=true"]}),
            ("source", {**campaign.source(self.sha), "targetRevision": "main"}),
            (
                "source",
                {
                    **campaign.source(self.sha),
                    "helm": {"parameters": [{"name": "api.replicas", "value": "1"}]},
                },
            ),
            ("sources", [campaign.source(self.sha)]),
            ("destination", {"server": campaign.CLUSTER, "namespace": "elsewhere"}),
        ]:
            with (
                self.subTest(change=change),
                self.assertRaisesRegex(ValueError, "desired revision"),
            ):
                obj = copy.deepcopy(self.obj)
                obj["spec"][key] = change
                self.validate(obj)

    def test_parent_and_running_or_pending_operation_refused(self):
        for update in (
            {"metadata": {"ownerReferences": [{"kind": "ApplicationSet"}]}},
            {"metadata": {"annotations": {campaign.TRACK: "parent"}}},
            {"operation": {"sync": {}}},
            {"operation": {}},
            {"status": {"operationState": {}}},
            {"status": {"operationState": {"phase": "Unknown"}}},
            {"status": {"operationState": {"phase": "Terminating"}}},
            {"status": {"operationState": {"phase": "Running"}}},
        ):
            with self.subTest(update=update), self.assertRaises(ValueError):
                self.validate({**self.obj, **update})

    def test_healthy_comparison_cannot_substitute_for_a_successful_installation(self):
        src = campaign.source(self.sha)
        op = {
            "initiatedBy": {"username": "maintenance"},
            "sync": {"source": src, "revision": self.sha, "syncStrategy": {"hook": {}}},
        }
        state = {"phase": "Succeeded", "operation": op, "syncResult": {"source": src, "revision": self.sha}}
        self.obj["status"] = {
            "health": {"status": "Healthy"},
            "sync": {"status": "Synced", "revision": self.sha},
            "operationState": state,
        }
        campaign.validate_installed(self.obj, "catalog-test", self.sha)
        for change in (
            {"phase": "Failed"},
            {"syncResult": {"revision": "2" * 40, "source": src}},
            {"syncResult": {"revision": self.sha, "source": {**src, "path": "other"}}},
        ):
            with self.subTest(change=change), self.assertRaisesRegex(ValueError, "successful healthy installation"):
                self.obj["status"]["operationState"] = {**state, **change}
                campaign.validate_installed(self.obj, "catalog-test", self.sha)

    def test_only_the_authorized_full_native_sync_is_accepted(self):
        sync = {"revision": self.sha, "source": campaign.source(self.sha), "syncStrategy": {"hook": {}}}
        op = {"initiatedBy": {"username": "maintenance"}, "sync": sync}
        campaign.validate_sync(op, self.sha)
        for changed in (
            {"initiatedBy": {"automated": True}},
            {"initiatedBy": {"username": "observer"}},
            {"sync": {**sync, "revision": "2" * 40}},
            {"sync": {**sync, "syncStrategy": {"apply": {}}}},
            {"sync": {**sync, "syncStrategy": {"hook": {"force": True}}}},
            {"sync": {**sync, "resources": [{"kind": "Deployment", "name": "catalog-api"}]}},
            {"sync": {**sync, "source": {**sync["source"], "path": "another-chart"}}},
            {"sync": {**sync, "manifests": ["override"]}},
            {"sync": {**sync, "syncOptions": ["ApplyOutOfSyncOnly=true"]}},
            {"sync": {**sync, "dryRun": True}},
            {"retry": {"limit": 1}},
        ):
            with self.subTest(changed=changed), self.assertRaisesRegex(ValueError, "unapproved sync"):
                campaign.validate_sync({**op, **changed}, self.sha)


class PreparedInputsTest(unittest.TestCase):
    def test_preload_is_verified_before_use_and_explicit_install_reuses_local_bytes(
        self,
    ):
        import hashlib
        from pathlib import Path
        import tempfile
        from unittest.mock import patch
        from fixtures.argocd import install

        data = b"fixture preload"
        pin = {"name": "fixture.tar.lz4", "sha256": hashlib.sha256(data).hexdigest()}
        with tempfile.TemporaryDirectory() as tmp:
            target = Path(tmp) / "cache"
            target.mkdir()
            archive = Path(tmp) / pin["name"]
            with (
                patch.object(install, "CACHE", target),
                patch.dict(install.PINS, {"preload": pin}),
                patch.dict(
                    install.os.environ,
                    {"CATALOG_MINIKUBE_PRELOAD_ARCHIVE": str(archive)},
                ),
                patch.object(install.subprocess, "run") as external,
            ):
                for value in (None, b"wrong archive"):
                    if value:
                        archive.write_bytes(value)
                    with self.assertRaisesRegex(ValueError, "incorrect Kubernetes preload"):
                        install.prepare_preload(check=True)
                archive.write_bytes(data)
                self.assertEqual(install.prepare_preload(check=False), target / pin["name"])
                with patch.dict(install.os.environ, {"CATALOG_MINIKUBE_PRELOAD_ARCHIVE": ""}):
                    self.assertEqual(install.prepare_preload(check=True).read_bytes(), data)
                external.assert_not_called()

    def test_stale_pin_refuses_before_invoking_docker_or_downloading(self):
        from unittest.mock import patch
        from fixtures.argocd import install

        with (
            patch.object(install.os, "access", return_value=True),
            patch.object(install, "verify", return_value=False),
            patch.object(install.subprocess, "run") as run,
            patch.object(install, "acquire_files") as acquire,
        ):
            with self.assertRaisesRegex(ValueError, "prepare the verified CLI"):
                install.prepare(check=True)
            run.assert_not_called()
            acquire.assert_not_called()
