"""Exact desired-state admission for the standalone Catalog Application."""

import re

ARGO = "argocd"
APP = "catalog-publication"
PROJECT = "catalog-qualification"
GIT = "https://catalog-git.argocd.svc:8443/catalog.git"
CLUSTER = "https://kubernetes.default.svc"
RESOURCE = "application/" + APP
TRACK = "argocd.argoproj.io/tracking-id"
ROLES = ("api", "worker")
HOOKS = (
    ("ServiceAccount", "catalog-migration", "-20"),
    ("ConfigMap", "catalog-migration", "-10"),
    ("Job", "catalog-migrate", "-5"),
)


def require(ok, message):
    if not ok:
        raise ValueError(message)


def source(sha):
    require(re.fullmatch("[a-f0-9]{40}", sha), "expected a full fixture commit ID")
    return {
        "repoURL": GIT,
        "path": "chart",
        "targetRevision": sha,
        "helm": {"releaseName": "catalog", "valueFiles": ["fixture.json"]},
    }


def application(ns, sha):
    return {
        "source": source(sha),
        "destination": {"server": CLUSTER, "namespace": ns},
        "project": PROJECT,
        "syncPolicy": {"automated": {"enabled": False}, "retry": {"limit": 0}},
    }


def validate_app(obj, ns, sha):
    expected = application(ns, sha)
    # The native client serializes the zero-valued retry limit with omitempty.
    if obj["spec"].get("syncPolicy", {}).get("retry") == {}:
        expected["syncPolicy"]["retry"] = {}
    require(obj["spec"] == expected, "desired revision, overrides, authority or manual policy changed")
    meta = obj["metadata"]
    require(
        not meta.get("ownerReferences")
        and TRACK not in meta.get("annotations", {})
        and "argocd.argoproj.io/instance" not in meta.get("labels", {}),
        "Application is not standalone",
    )
    state = obj.get("status", {}).get("operationState")
    require(
        obj.get("operation") is None and (state is None or state.get("phase") in ("Succeeded", "Failed", "Error")),
        "another operation is active or its outcome is unknown",
    )


def validate_sync(operation, sha):
    """The actual operation must be the maintenance operator's full, hook-based sync."""
    sync = operation.get("sync", {})
    require(
        operation.get("initiatedBy") == {"username": "maintenance"}
        and operation.get("retry", {}) in ({}, {"limit": 0})
        and sync == {"revision": sha, "source": source(sha), "syncStrategy": {"hook": {}}},
        "unapproved sync initiator, revision, strategy or overrides",
    )


def validate_installed(obj, ns, sha):
    validate_app(obj, ns, sha)
    status = obj.get("status", {})
    state = status.get("operationState") or {}
    result = state.get("syncResult") or {}
    require(
        state.get("phase") == "Succeeded"
        and result.get("revision") == sha
        and result.get("source") == source(sha)
        and status.get("sync", {}).get("status") == "Synced"
        and status["sync"].get("revision") == sha
        and status.get("health", {}).get("status") == "Healthy",
        "Application has no successful healthy installation at the approved revision",
    )
    validate_sync(state["operation"], sha)
