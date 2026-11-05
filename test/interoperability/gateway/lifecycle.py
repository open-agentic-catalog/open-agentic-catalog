"""Catalog enrollment and lifecycle against the unchanged owned Gateway peer."""

import base64
import hashlib
import json
from pathlib import Path
import sys

if __package__ in (None, ""):
    sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from agntcy_lab import LocalHTTP, UnexpectedStatus
from peer_catalog import pages, sync_attempt, wait_for
from peer_contract import CURRENT, TENANT
from gateway import native
from gateway.native import Native, ORIGIN, expected

PREFIX = "/api/v1/federation/"
PUBLISHER = "gateway.gateway-qualification.svc.cluster.local"
REVERSE = "https://catalog-origin.gateway-qualification.svc.cluster.local:8443/.well-known/ard.json"
PHASES = (
    "bootstrap",
    "add",
    "disable",
    "restore",
    "outage",
    "recovery",
    "advertised",
    "reverse",
)


def digest(raw):
    return "sha256:" + hashlib.sha256(raw).hexdigest()


class Catalog(LocalHTTP):
    def __init__(self, settings):
        super().__init__(
            {"catalog": settings["catalog"], "origin": settings["origin"]},
            settings["certificate"],
            settings["token"],
        )
        self.settings, self.captures = settings, []

    def request(self, peer, path, body=None, **options):
        try:
            raw, headers = super().request(peer, path, body, **options)
        except UnexpectedStatus as error:
            self.capture(peer, path, error.status, error.raw)
            raise
        self.capture(peer, path, options.get("expected", 200), raw)
        return raw, headers

    def capture(self, peer, path, status, raw):
        assert len(self.captures) < 512, "Catalog evidence count exceeded"
        assert not any(
            value.encode() in raw
            for value in [
                *self.settings["secrets"].values(),
                self.settings["token"],
                self.settings["reader"],
                self.settings["other"],
            ]
        ), "credential in Catalog response"
        self.captures.append(
            dict(
                peer=peer,
                path=path,
                status=status,
                sha256=digest(raw),
                raw_base64=base64.b64encode(raw).decode(),
            )
        )

    def document(self, path, body=None, *, expected=200, etag=None, token=None):
        saved = self.token
        try:
            self.token = saved if token is None else token
            return super().document("catalog", path, body, expected=expected, etag=etag)
        finally:
            self.token = saved

    def pages(self, path, **query):
        return pages(path, request=self.document, **query)

    def inspect(
        self,
        path="/.well-known/ai-catalog.json",
        *,
        binding="gateway-admin",
        profile="functional-fixture",
        expected=201,
        token=None,
    ):
        return self.document(
            PREFIX + "source-inspections",
            dict(url=ORIGIN + path, networkProfile=profile, credentialBinding=binding),
            expected=expected,
            token=token,
        )[0]

    def enqueue(self, source_id):
        path = PREFIX + "sources/" + source_id
        _, headers = self.document(path)
        result, _ = self.document(
            path + ":sync", {}, expected=202, etag=headers["etag"]
        )
        assert result["existing"] is False, "unexpected existing sync Job"
        return result["job"]

    def complete(self, job_id):
        def finished():
            job, _ = self.document("/api/v1/jobs/" + job_id)
            if job["state"] in ("failed", "dead_letter", "cancelled"):
                # Retain safe API evidence before the owning supervisor cleans up.
                for source in self.pages(PREFIX + "sources"):
                    self.document(PREFIX + "sources/" + source["id"])
                    self.pages(PREFIX + "sources/" + source["id"] + "/snapshots")
                self.pages("/api/v1/audit-events", targetId=job_id)
                raise AssertionError("sync terminated before completion")
            return job if job["state"] == "succeeded" else None

        job = wait_for("Gateway worker completion", finished, timeout=120)
        snapshot, _ = self.document(PREFIX + "snapshots/" + job["result"]["snapshotId"])
        assert (
            snapshot["state"] == "complete" and snapshot["enumerationComplete"] is True
        )
        assert sync_attempt(snapshot, job_id, request=self.document) == job["attempt"]
        return job, snapshot


def publisher(peer, names, registry=False):
    document = peer.request("/.well-known/ai-catalog.json", credential="")
    wanted = expected(*["qualified-" + name for name in names])
    entries = document["entries"]
    if registry:
        assert len(entries) == len(wanted) + 1
        entries = [
            entry
            for entry in entries
            if entry["type"] != "application/ai-registry+json"
        ]
    assert {entry["identifier"] for entry in entries} == wanted and len(entries) == len(
        wanted
    ), "native Publisher membership differs from the registered fixtures"
    raw = base64.b64decode(peer.captures[-1]["raw_base64"])
    return document, raw


def retained(api, peer, state, names, snapshot, *, registry=False):
    source_id = state["source_id"]
    document, raw = publisher(peer, names, registry=registry)
    native = {entry["identifier"]: entry for entry in document["entries"]}
    assert snapshot["sourceId"] == source_id and snapshot["memberCount"] == len(native)
    assert (
        snapshot["bodyDigest"] == digest(raw)
        and snapshot["withdrawalAuthority"] is True
    )
    source, _ = api.document(PREFIX + "sources/" + source_id)
    assert source["lastCompleteSnapshotId"] == snapshot["id"]
    rows = api.pages("/api/v1/observations", sourceId=source_id, freshness="current")
    assert len(rows) == len(native) and {row["identifier"] for row in rows} == set(
        native
    )
    evidence = []
    for row in rows:
        assert row["sourceId"] == source_id and row["tenantId"] == TENANT
        assert row["rawEntryAvailable"] is True
        if native[row["identifier"]]["type"] == "application/ai-registry+json":
            assert registry and row.get("kind", "") == ""
            assert row["compatibility"]["support"] == "unknown"
            assert not row.get("normalizedEntryDigest")
        else:
            assert row["kind"] == "mcp_server" and row["normalizedEntryDigest"]
        entry, _ = api.request("catalog", "/api/v1/observations/" + row["id"] + "/raw")
        assert json.loads(entry) == native[row["identifier"]] and entry in raw
        assert row["exactEntryDigest"] == digest(entry)
        occurrences = api.pages(
            "/api/v1/observations/" + row["id"] + "/occurrences",
            identity=lambda item: (
                item["snapshotId"],
                item["retrievalDigest"],
                item["entryLocation"],
            ),
        )
        occurrence = [
            item for item in occurrences if item["snapshotId"] == snapshot["id"]
        ]
        assert len(occurrence) == 1 and occurrence[0]["exactEntryDigest"] == digest(
            entry
        )
        assert (
            occurrence[0]["retrievalDigest"] == digest(raw)
            and occurrence[0]["sourceId"] == source_id
            and occurrence[0]["observationId"] == row["id"]
            and occurrence[0]["sourceRevision"] == snapshot["sourceRevision"]
            and occurrence[0]["evidenceComplete"] is True
            and occurrence[0].get("normalizedEntryDigest")
            == row.get("normalizedEntryDigest")
        )
        evidence.append(
            {
                key: row.get(key)
                for key in (
                    "id",
                    "identifier",
                    "sourceId",
                    "exactEntryDigest",
                    "identityDigest",
                    "normalizedEntryDigest",
                    "freshness",
                )
            }
        )
    assert (
        sorted(row["id"] for row in api.pages("/api/v1/resources"))
        == state["resources"]
    ), "external sync created a governed Resource"
    state.update(
        snapshot_id=snapshot["id"], manifest_sha256=digest(raw), observations=evidence
    )
    return dict(snapshot=snapshot, observations=evidence, resources_unchanged=True)


def bootstrap(api, peer, state):
    for label, options, code in (
        ("reader", {"token": api.settings["reader"]}, "FORBIDDEN"),
        ("wrong-tenant", {"token": api.settings["other"]}, "SOURCE_POLICY_DENIED"),
        ("untrusted-ca", {"profile": "gateway-untrusted"}, "DEPENDENCY_UNAVAILABLE"),
    ):
        status = 503 if label == "untrusted-ca" else 403
        problem = api.inspect(expected=status, **options)
        assert problem["code"] == code, "different source refusal: " + label
    inspection = api.inspect()
    assert (
        inspection["sourceType"] == "ard_manifest"
        and inspection["profileId"] == CURRENT
    )
    assert (
        inspection["entryCount"] == 2
        and not inspection.get("registrySearchUrl")
        and inspection["capabilities"]["enumerable"] is True
    )
    source, _ = api.document(
        PREFIX + "sources",
        dict(
            inspectionId=inspection["id"],
            displayName="Owned native Gateway manifest",
            retrievalMode="indexed",
            updateMode="manual_review",
            networkProfile="functional-fixture",
            credentialBinding="gateway-admin",
            scope={"publishers": [PUBLISHER]},
            reason="Qualify native Publisher without Registry advertisement",
        ),
        expected=201,
    )
    state.update(
        source_id=source["id"],
        inspection_id=inspection["id"],
        resources=sorted(row["id"] for row in api.pages("/api/v1/resources")),
    )
    api.document(
        PREFIX + "sources/" + source["id"], expected=404, token=api.settings["other"]
    )
    job, snapshot = api.complete(api.enqueue(source["id"])["id"])
    return dict(
        inspection=inspection,
        job=job,
        **retained(api, peer, state, ["alpha", "beta"], snapshot),
    )


def mutation(api, peer, state, phase):
    if phase == "add":
        peer.request(
            "/api/servers/register",
            status=201,
            form=dict(
                name="qualified-gamma",
                description="qualified gamma metadata fixture",
                path="/qualified-gamma",
                tags="qualified,gamma",
                license="Apache-2.0",
                version="1.0.0",
                deployment="local",
                visibility="public",
                local_runtime=json.dumps(
                    dict(
                        type="command",
                        package="/not-installed/qualification-only",
                        args=[],
                        env={},
                        required_env=[],
                    )
                ),
            ),
        )
    enabled = phase != "disable"
    name = "gamma" if phase == "add" else "beta"
    changed = peer.request(
        "/api/servers/toggle",
        form=dict(path="/qualified-" + name, new_state=str(enabled).lower()),
    )
    assert changed["new_enabled_state"] is enabled
    job, snapshot = api.complete(api.enqueue(state["source_id"])["id"])
    expected_delta = {"add": (1, 0), "disable": (0, 1), "restore": (1, 0)}[phase]
    assert (snapshot["added"], snapshot["withdrawn"]) == expected_delta
    names = ["alpha", "gamma"] if phase == "disable" else ["alpha", "beta", "gamma"]
    return dict(job=job, **retained(api, peer, state, names, snapshot))


def outage(api, state):
    before = {
        row["id"]
        for row in api.pages(PREFIX + "sources/" + state["source_id"] + "/snapshots")
    }
    job = api.enqueue(state["source_id"])
    state["recovery_job"] = job["id"]

    def failed():
        snapshots = api.pages(PREFIX + "sources/" + state["source_id"] + "/snapshots")
        found = [
            row
            for row in snapshots
            if row["id"] not in before and row["state"] == "failed"
        ]
        return found[0] if found else None

    snapshot = wait_for("Gateway unavailable attempt", failed, timeout=45)
    state["failed_attempt"] = sync_attempt(snapshot, job["id"], request=api.document)
    state["failed_snapshot_id"] = snapshot["id"]
    source, _ = api.document(PREFIX + "sources/" + state["source_id"])
    assert source["lastCompleteSnapshotId"] == state["snapshot_id"]
    current = api.pages(
        "/api/v1/observations", sourceId=state["source_id"], freshness="current"
    )
    for key in ("id", "identifier", "identityDigest", "exactEntryDigest"):
        assert sorted(row[key] for row in current) == sorted(
            row[key] for row in state["observations"]
        )
    attempted, _ = api.document("/api/v1/jobs/" + job["id"])
    assert (
        attempted["state"] in ("running", "waiting")
        and attempted["attempt"] >= state["failed_attempt"]
    )
    return dict(
        job=attempted,
        failed_snapshot=snapshot,
        last_complete=source["lastCompleteSnapshotId"],
        preserved_members=len(current),
        attempt=state["failed_attempt"],
    )


def recovery(api, peer, state):
    previous = state["observations"]
    job, snapshot = api.complete(state["recovery_job"])
    assert (
        job["attempt"] > state["failed_attempt"]
        and snapshot["id"] != state["snapshot_id"]
    )
    assert all(snapshot[key] == 0 for key in ("added", "updated", "withdrawn"))
    result = retained(api, peer, state, ["alpha", "beta", "gamma"], snapshot)
    for key in (
        "id",
        "identifier",
        "identityDigest",
        "exactEntryDigest",
        "normalizedEntryDigest",
    ):
        assert sorted(row[key] for row in previous) == sorted(
            row[key] for row in state["observations"]
        )
    return dict(job=job, **result)


def advertised(api, peer, state):
    publisher(peer, ["alpha", "beta", "gamma"], registry=True)
    # Restarting the unchanged peer discards its lazy Python import state.
    # Qualify real authenticated Search before the Catalog's shorter probe budget.
    search = peer.walk(
        "search",
        expected(
            "qualified-alpha", "qualified-beta", "qualified-gamma", "qualified-private"
        ),
    )
    native.check_registry_nulls(search, "search")
    # A changed interface requires replacement. Keep one connected source for
    # this endpoint and preserve the removed source's immutable evidence.
    inspection = api.inspect()
    assert inspection["sourceType"] == "hybrid" and inspection["profileId"] == CURRENT
    assert inspection["registrySearchUrl"] == ORIGIN + "/api/ard/search"
    # An explicit Search probe reaches Registry decoding, independent of the manifest.
    for binding in ("", "gateway-invalid"):
        problem = api.inspect("/api/ard/search", binding=binding, expected=400)
        assert problem["code"] == "INVALID_ARGUMENT" and problem["detail"] == (
            "Registry Search probe returned HTTP 401"
        )
    problem = api.inspect("/api/ard/search", expected=400)
    assert problem["code"] == "INVALID_ARGUMENT" and problem["detail"] == (
        "Registry Search probe returned incompatible JSON"
    )
    path = PREFIX + "sources/" + state["source_id"]
    previous, headers = api.document(path)
    retained_before, _ = api.document(PREFIX + "snapshots/" + state["snapshot_id"])
    preview, _ = api.document(path + "/change-previews", dict(intent="remove"))
    api.request(
        "catalog",
        path + ":remove",
        dict(
            changePreviewDigest=preview["digest"],
            reason="Replace the source after a reviewed native interface change",
        ),
        expected=204,
        etag=headers["etag"],
    )
    hidden, _ = api.document(path, expected=404)
    assert hidden["code"] == "NOT_FOUND", "removed source remains accessible"
    source, _ = api.document(
        PREFIX + "sources",
        dict(
            inspectionId=inspection["id"],
            displayName="Owned Gateway advertised interface",
            retrievalMode="indexed",
            updateMode="manual_review",
            networkProfile="functional-fixture",
            credentialBinding="gateway-admin",
            scope={"publishers": [PUBLISHER]},
            reason="Enroll the separately inspected native hybrid manifest",
        ),
        expected=201,
    )
    assert (
        source["id"] != previous["id"]
        and source["endpoint"] == previous["endpoint"]
        and source["type"] == "hybrid"
        and source["registrySearchUrl"] == inspection["registrySearchUrl"]
        and source["retrievalMode"] == "indexed"
    )
    retained_after, _ = api.document(PREFIX + "snapshots/" + state["snapshot_id"])
    assert retained_before == retained_after, "reinspection rewrote a prior snapshot"
    job, snapshot = api.complete(api.enqueue(source["id"])["id"])
    assert snapshot["memberCount"] == 4 and snapshot["state"] == "complete"
    retained_state = dict(source_id=source["id"], resources=state["resources"])
    evidence = retained(
        api, peer, retained_state, ["alpha", "beta", "gamma"], snapshot, registry=True
    )
    return dict(
        native_search=search,
        inspection=inspection,
        preview=preview,
        replaced_source_id=previous["id"],
        previous_snapshot=retained_before,
        previous_snapshot_unchanged=True,
        source_id=source["id"],
        job=job,
        **evidence,
        live_search="not-selected-invalid-native-search",
        members=4,
    )


def reverse(api, peer):
    before_inventory = peer.request("/api/ard/agents?pageSize=100")
    wanted = expected(
        "qualified-alpha", "qualified-beta", "qualified-gamma", "qualified-private"
    )
    assert {row["identifier"] for row in before_inventory["items"]} == wanted
    raw, _ = api.request("origin", "/.well-known/ard.json")
    direct, _ = api.request("catalog", "/.well-known/ard.json")
    assert raw == direct and len(json.loads(raw)["entries"]) > 0, (
        "reverse target is not the healthy Catalog manifest"
    )
    before = json.loads(api.request("origin", "/requests")[0])["requests"]
    config = dict(
        ai_catalog=dict(
            enabled=True,
            sync_on_startup=False,
            polite_interval_ms=0,
            fetch_timeout_seconds=5,
            max_depth=0,
            sources=[dict(source_id="owned-catalog", uri=REVERSE)],
        )
    )
    configured = peer.request("/api/federation/config", body=config, status=201)
    assert configured["config"]["ai_catalog"]["sources"][0]["uri"] == REVERSE
    result = peer.request(
        "/api/federation/ai_catalog/sync?source_id=owned-catalog", body={}
    )
    status = peer.request("/api/federation/ai_catalog/status")
    after = json.loads(api.request("origin", "/requests")[0])["requests"]
    assert before == after, "native reverse importer contacted the private manifest"
    assert len(result["results"]) == 1
    outcome = result["results"][0]
    assert (
        outcome["success"] is True
        and outcome["servers_synced"] == outcome["agents_synced"] == 0
        and outcome["new_generation"] >= 1
    )
    assert len(status["sources"]) == 1
    state = status["sources"][0]
    assert (
        state["source_id"] == "owned-catalog"
        and state["generation"] >= outcome["new_generation"]
        and state["consecutive_failures"] == 0
        and state["last_error"] is None
    )
    assert (
        state["servers_synced"]
        == state["agents_synced"]
        == state["skills_synced"]
        == state["rejected"]
        == 0
    )
    # A healthy control after refusal proves the counter's listener remained available.
    api.request("origin", "/.well-known/ard.json")
    final = json.loads(api.request("origin", "/requests")[0])["requests"]
    assert final == after + 1
    after_inventory = peer.request("/api/ard/agents?pageSize=100")
    assert {row["identifier"] for row in after_inventory["items"]} == wanted and len(
        after_inventory["items"]
    ) == len(wanted), "reverse import changed native inventory"
    return dict(
        configured=True,
        native_inventory_unchanged=True,
        sync=result,
        status=status,
        target_sha256=digest(raw),
        requests_before=before,
        requests_after=after,
        healthy_control_after=final,
        disposition="private-address-refusal-reported-as-successful-empty",
    )


def execute(phase, api, peer, state):
    if phase == "bootstrap":
        return bootstrap(api, peer, state)
    if phase in ("add", "disable", "restore"):
        return mutation(api, peer, state, phase)
    if phase == "outage":
        return outage(api, state)
    if phase == "recovery":
        return recovery(api, peer, state)
    if phase == "advertised":
        return advertised(api, peer, state)
    if phase == "reverse":
        return reverse(api, peer)
    raise ValueError("unknown Gateway lifecycle phase")


def main():
    if not __debug__:
        raise RuntimeError("Gateway qualification requires assertions")
    phase, settings_path, state_path, output = sys.argv[1:]
    assert phase in PHASES
    settings = json.loads(Path(settings_path).read_text())
    state_file = Path(state_path)
    state = json.loads(state_file.read_text()) if state_file.exists() else {}
    native.DIRECTORY = Path(settings["schemaDirectory"])
    api = Catalog(settings)
    peer = Native(
        settings["secrets"],
        origin=settings["peer"],
        certificate=settings["certificate"],
    )
    result = dict(phase=phase, status="fail")
    try:
        result.update(evidence=execute(phase, api, peer, state), status="pass")
    except (
        AssertionError,
        RuntimeError,
        OSError,
        ValueError,
        KeyError,
        TypeError,
    ) as error:
        result["error"] = str(error) or type(error).__name__
    result.update(captures=api.captures, native_captures=peer.captures)
    raw = json.dumps(result)
    for secret in [
        *settings["secrets"].values(),
        settings["token"],
        settings["reader"],
        settings["other"],
    ]:
        raw = raw.replace(secret, "[redacted]")
    Path(output).write_text(raw + "\n")
    state_file.write_text(json.dumps(state))
    print(
        json.dumps(
            dict(phase=phase, status=result["status"], error=result.get("error", ""))
        )
    )
    return int(result["status"] != "pass")


if __name__ == "__main__":
    sys.exit(main())
