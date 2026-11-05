"""Qualify explicit private Jobs using the unchanged local ANS producers."""

import base64
from datetime import datetime, timedelta, timezone
import hashlib
import json
from pathlib import Path
import sys
import tempfile
import traceback
import urllib.error
import urllib.request
import uuid

if __package__ in (None, ""):
    sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from ans import lifecycle, native
from common import run
from peer_catalog import wait_for


class API(lifecycle.Catalog):
    def call(self, path, body=None, *, method=None, token=None, key=None, etag=None, expected=200):
        headers = {"Accept": "application/json", "Authorization": "Bearer " + (self.token if token is None else token)}
        if body is not None:
            headers.update({"Content-Type": "application/json", "Idempotency-Key": key or uuid.uuid4().hex})
        if etag:
            headers["If-Match"] = etag
        request = urllib.request.Request(self.settings["catalog"] + path, None if body is None else json.dumps(body).encode(), headers, method=method)
        try:
            response = self.opener.open(request, timeout=12)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            raw = response.read((2 << 20) + 1)
            assert len(raw) <= 2 << 20, "private result response exceeded bound"
            self.capture(path, response.status, raw)
            assert response.status == expected, f"{path}: expected {expected}, got {response.status}: {raw[:2000]!r}"
            return json.loads(raw), {k.lower(): v for k, v in response.headers.items()}


def evidence(peer, mode=None):
    document = peer.document("finder-edge", "/_qualification/evidence", None if mode is None else {"mode": mode})
    assert document["overflow"] is False
    assert all(not row["authorizationPresent"] and not row["cookiePresent"] for row in document["requests"])
    return document["requests"]


def select(api, state, text="alpha"):
    document, _ = api.call(lifecycle.PREFIX + "sources:live-search", {"text": text, "pageSize": 20, "sourceIds": [state["source_id"]], "reason": "Select exact local registration"})
    assert document["partial"] is False and len(document["results"]) == 1, document
    row = document["results"][0]
    return {"sourceRevision": row["sourceRevision"], "identifier": row["identifier"], "versionPresent": row["versionPresent"],
            "version": row["version"], "mediaType": row["entry"]["type"], "delivery": "url", "exactEntryDigest": row["exactEntryDigest"],
            "text": text, "reason": "Qualify explicitly selected local ANS registration"}


def enqueue(api, state, request=None, *, expected=202, key=None, token=None):
    body = request if request is not None else select(api, state)
    document, headers = api.call(lifecycle.PREFIX + "sources/" + state["source_id"] + "/trust-assessments", body, expected=expected, key=key, token=token)
    if expected == 202:
        assert document["kind"] == "source.trust_verify" and document["state"] in ("queued", "running", "waiting", "succeeded")
        assert headers["location"].endswith("/" + document["id"])
        state.setdefault("jobs", []).append(document["id"])
    return document


def job(api, identifier):
    return api.call("/api/v1/jobs/" + identifier)[0]


def completed(api, state, identifier, *, success=True):
    terminal = ("succeeded",) if success else ("failed", "dead_letter", "cancelled")
    final = wait_for("explicit trust Job terminal state", lambda: (row if (row := job(api, identifier))["state"] in terminal else None), timeout=80)
    assert not any(name in final for name in ("privateResult", "result", "evidence")), "generic Job disclosed private evidence"
    path = lifecycle.PREFIX + "sources/" + state["source_id"] + "/trust-assessments/" + identifier
    if not success:
        api.call(path, expected=409)
        return final
    result = api.call(path)[0]
    retained = result["result"]
    assert retained["sourceId"] == state["source_id"]
    assert all(retained[field] == "not_assessed" for field in ("softwareTrust", "artifactIntegrity", "rights"))
    assert "sha256:" + hashlib.sha256(base64.b64decode(retained["evidence"]["entry"], validate=True)).hexdigest() == retained["exactEntryDigest"]
    return result


def positive(result, version="V1"):
    assessment = result["result"]["assessment"]
    assert result["current"] is True and result["applicability"] == "current", result
    assert assessment["currentRegistration"] is True and assessment["schemaVersion"] == version, assessment
    for key in ("receipt", "producer", "log", "checkpoint", "inclusion", "target", "status"):
        assert assessment[key]["state"] == "valid", (key, assessment)
    assert assessment["subject"] and assessment["logId"]
    for key in ("registrationKeyFingerprint", "logKeyFingerprint", "envelopeLogKeyFingerprint"):
        value = assessment[key]
        assert len(value) == 64 and all(char in "0123456789abcdef" for char in value), (key, value)


def offline(settings, bundle, *, policy=None, at=None, success=True):
    directory = Path(settings["trust_output"])
    selected = json.loads((directory / "policy.json").read_text()) if policy is None else policy
    with tempfile.TemporaryDirectory(prefix="offline-", dir=directory) as temporary:
        here = Path(temporary)
        for name in ("ra.pem", "tl.pem", "other.pem", "ra-next.pem", "tl-next.pem"):
            if (directory / name).exists():
                (here / name).write_bytes((directory / name).read_bytes())
        (here / "policy.json").write_text(json.dumps(selected))
        (here / "bundle.json").write_text(json.dumps(bundle))
        args = [settings["catalogctl"], "trust", "verify-bundle", "--policy", str(here / "policy.json"), "--file", str(here / "bundle.json")]
        if at:
            args.extend(["--at", at])
        result = run(args, timeout=15, check=False)
        assert (result.code == 0) is success, result.stderr
        document = json.loads(result.stdout)
        assert document["currentRegistration"] is success
        return document


def checkpoint(raw):
    """Inspect the native signed body; the Catalog/CLI authenticate signatures."""
    body, signatures = raw.split(b"\n\n", 1)
    origin, size, root = body.decode("ascii").split("\n")
    assert origin == "owned-ans" and size.isascii() and size.isdecimal() and str(int(size)) == size and int(size) > 0
    assert len(base64.b64decode(root, validate=True)) == 32 and signatures
    return int(size), root, body.decode("ascii") + "\n"


def history_binding(raw, size, root, text):
    document = json.loads(raw)
    assert len(document["checkpoints"]) == 1, "native exact-size history is unavailable or ambiguous"
    row = document["checkpoints"][0]
    assert row["logSize"] == size and row["rootHash"] == root and row["originName"] == "owned-ans" and row["checkpointText"] == text
    assert any(signature["signatureType"] == "c2sp" for signature in row["signatures"])


def history_requests(rows, subject, size, *, positive):
    expected = ["/v1/agents/" + subject + "/receipt", "/checkpoint",
                f"/v1/log/checkpoint/history?fromSize={size}&toSize={size}&limit=1"]
    if positive:
        expected.append("/v1/agents/" + subject + "/status-token")
    assert [row["path"] for row in rows] == expected, "history acquisition escaped the exact bounded sequence"
    assert all(not row["authorizationPresent"] and not row["cookiePresent"] for row in rows)


def key_fingerprint(settings, name):
    pem = (Path(settings["trust_output"]) / name).read_text().splitlines()
    return hashlib.sha256(base64.b64decode("".join(pem[1:-1]), validate=True)).hexdigest()


def signer_fingerprints(result, registration, receipt, envelope):
    positive(result)
    assessment = result["result"]["assessment"]
    assert (assessment["registrationKeyFingerprint"], assessment["logKeyFingerprint"], assessment["envelopeLogKeyFingerprint"]) == (registration, receipt, envelope)


def signer_changes(phase, api, peer, state, settings):
    directory = Path(settings["trust_output"])
    if phase in ("ra-signer", "tl-signer"):
        component = phase[:2]
        if component == "ra":
            now = datetime.now(timezone.utc)
            record = peer.document("tl", "/internal/v1/producer-keys", {"key_id": "ans-ra-signer-next", "ra_id": "ans-ra-local", "algorithm": "ES256",
                "public_key_pem": (directory / "ra-next.pem").read_text(), "valid_from": (now - timedelta(minutes=1)).isoformat(), "expires_at": (now + timedelta(days=1)).isoformat()}, auth=True)
            assert record["status"] == "active" and record["key_id"] == "ans-ra-signer-next"
        old_size, old_root, old_text = checkpoint(peer.request("tl", "/checkpoint"))
        old_history = peer.request("tl", f"/v1/log/checkpoint/history?fromSize={old_size}&toSize={old_size}&limit=1")
        history_binding(old_history, old_size, old_root, old_text)
        definition = native.seed_definition(component + "-next", "MCP")
        with tempfile.TemporaryDirectory(prefix="csr-", dir=directory) as temporary:
            body = {**definition, "identityCsrPEM": native.csr(definition["agentHost"], Path(temporary), "identity"), "serverCsrPEM": native.csr(definition["agentHost"], Path(temporary), "server")}
            registered = peer.document("ra", "/v1/agents/register", body, auth=True, expected=202)
        for stage in ("verify-acme", "verify-dns"):
            peer.document("ra", "/v1/agents/" + registered["agentId"] + "/" + stage, {}, auth=True, expected=202)
        wait_for("new signer acknowledged native event", lambda: any(row["agentId"] == registered["agentId"] for row in peer.document("ra", "/v1/agents/events?limit=100")["items"]))
        wait_for("new signer native checkpoint", lambda: peer.document("tl", "/v1/log/checkpoint")["logSize"] > old_size)
        wait_for("new signer Finder projection", lambda: peer.document("finder", "/v1/search", {"query": {"text": component + "-next"}, "federation": "none"})["results"])
        new_size, _, _ = checkpoint(peer.request("tl", "/checkpoint"))
        assert peer.request("tl", f"/v1/log/checkpoint/history?fromSize={old_size}&toSize={old_size}&limit=1") == old_history
        state.setdefault("signer_native", {})[component] = {"oldSize": old_size, "newSize": new_size, "subject": registered["agentId"],
            "oldFingerprint": key_fingerprint(settings, component + ".pem"), "newFingerprint": key_fingerprint(settings, component + "-next.pem"),
            "oldHistorySHA256": hashlib.sha256(old_history).hexdigest()}
        assert state["signer_native"][component]["oldFingerprint"] != state["signer_native"][component]["newFingerprint"]

    removal = phase in ("ra-key-removal", "tl-key-removal")
    tl_changed = phase in ("tl-signer", "tl-key-removal", "signers-restored")
    new_text = "tl-next" if tl_changed else "ra-next"
    old_job = enqueue(api, state)["id"]
    old = completed(api, state, old_job)
    new_job = enqueue(api, state, select(api, state, new_text))["id"]
    new = completed(api, state, new_job)
    signer_fingerprints(new, key_fingerprint(settings, "ra-next.pem"), key_fingerprint(settings, "tl-next.pem" if tl_changed else "tl.pem"),
                        key_fingerprint(settings, "tl-next.pem" if tl_changed else "tl.pem"))
    if removal:
        assessment = old["result"]["assessment"]
        assert not old["current"] and not assessment["currentRegistration"]
        check, code = ("producer", "producer_signature") if phase == "ra-key-removal" else ("log", "log_signature")
        assert (assessment[check]["state"], assessment[check]["code"]) == ("invalid", code), assessment
        assert assessment["receipt"]["state"] == "valid", "fresh receipt must use an accepted signer"
    else:
        signer_fingerprints(old, key_fingerprint(settings, "ra.pem"), key_fingerprint(settings, "tl-next.pem" if tl_changed else "tl.pem"), key_fingerprint(settings, "tl.pem"))
        assert old["result"]["assessment"]["logId"] == state["assessment"]["logId"] and old["result"]["assessment"]["subject"] == state["assessment"]["subject"]
        assert old["result"]["evidence"]["entry"] == state["bundle"]["entry"]
        if tl_changed:
            # The supervisor independently authenticates these exact bytes with
            # only the new TL key, using Go's standard-library ECDSA verifier.
            state["signer_evidence"] = {name: {key: result["result"]["evidence"][key] for key in ("checkpoint", "statusToken")} for name, result in (("old", old), ("new", new))}
    for identifier, result in ((old_job, old), (new_job, new)):
        assessment = result["result"]["assessment"]
        assert job(api, identifier)["attempt"] == 1
        assert offline(settings, result["result"]["evidence"], at=assessment["checkedAt"], success=result["current"]) == assessment
    retained = completed(api, state, state["v1"])
    assert retained["result"]["evidence"] == state["bundle"] and retained["result"]["assessment"] == state["assessment"]
    if removal:
        assert not retained["current"] and retained["applicability"] == "historical"
    return {**({"old_key_rejected": True, "new_registration_current": True} if removal else {"old_and_new_registrations": True, "exact_fingerprints": True}),
        **({"native_new_signer": True} if phase in ("ra-signer", "tl-signer") else {}), "offline_agrees": True, "historical_bytes": True, "single_attempt": True}


def checkpoint_advancement(phase, api, peer, state, settings):
    # Prepare all CSR/ACME work before the request gate. Only the final native
    # DNS activation and checkpoint publication occur inside the attempt budget.
    definition = native.seed_definition("cp-" + phase, "HTTP-API")
    with tempfile.TemporaryDirectory(prefix="csr-", dir=settings["trust_output"]) as temporary:
        body = {**definition, "identityCsrPEM": native.csr(definition["agentHost"], Path(temporary), "identity"),
                "serverCsrPEM": native.csr(definition["agentHost"], Path(temporary), "server")}
        pending = peer.document("ra", "/v1/agents/register", body, auth=True, expected=202)
    peer.document("ra", "/v1/agents/" + pending["agentId"] + "/verify-acme", {}, auth=True, expected=202)
    subject = state["assessment"]["subject"]
    old_size, old_root, old_text = checkpoint(peer.request("tl", "/checkpoint"))
    path = f"/v1/log/checkpoint/history?fromSize={old_size}&toSize={old_size}&limit=1"
    original = peer.request("tl", path)
    history_binding(original, old_size, old_root, old_text)
    receipt = peer.request("tl", "/v1/agents/" + subject + "/receipt")
    mode = "history-gate" if phase == "history" else phase
    before = len(evidence(peer, mode))
    try:
        pending_job = enqueue(api, state)
        wait_for("owned checkpoint request gate", lambda: peer.document("finder-edge", "/_qualification/evidence")["checkpointGate"] == "waiting", timeout=30)
        peer.document("ra", "/v1/agents/" + pending["agentId"] + "/verify-dns", {}, auth=True, expected=202)
        wait_for("larger native signed checkpoint", lambda: peer.document("tl", "/v1/log/checkpoint")["logSize"] > old_size, timeout=5)
        evidence(peer, "release-checkpoint")
        result = completed(api, state, pending_job["id"])
        assert job(api, pending_job["id"])["attempt"] == 1, "checkpoint scenario retried instead of completing its assessment"
        assessment, bundle = result["result"]["assessment"], result["result"]["evidence"]
        rows = evidence(peer)[before:]
        history_requests(rows, subject, old_size, positive=phase == "history")
        assert base64.b64decode(bundle["receipt"], validate=True) == receipt, "receipt bytes/tree changed during the gate"
        latest = base64.b64decode(bundle["checkpoint"], validate=True)
        new_size, new_root, _ = checkpoint(latest)
        assert new_size > old_size and new_root != old_root
        assert hashlib.sha256(latest).hexdigest() == rows[1]["nativeSHA256"] == rows[1]["responseSHA256"]
        if phase == "history":
            positive(result)
            history = base64.b64decode(bundle["checkpointHistory"], validate=True)
            assert history == original, "native historical signed bytes changed"
            assert hashlib.sha256(history).hexdigest() == rows[2]["nativeSHA256"] == rows[2]["responseSHA256"]
        else:
            assert result["current"] is False and assessment["currentRegistration"] is False
            expected = {"history-missing": ("unavailable", "checkpoint_history_missing"), "history-corrupt": ("invalid", "checkpoint_binding")}[phase]
            assert (assessment["checkpoint"]["state"], assessment["checkpoint"]["code"]) == expected, assessment
            assert all(assessment[key]["state"] == "valid" for key in ("receipt", "producer", "log"))
            assert assessment["status"]["state"] == "not_assessed" and not base64.b64decode(bundle["statusToken"], validate=True)
            if phase == "history-corrupt":
                history = base64.b64decode(bundle["checkpointHistory"], validate=True)
                history_binding(history, old_size, old_root, old_text)
                assert hashlib.sha256(original).hexdigest() == rows[2]["nativeSHA256"] != rows[2]["responseSHA256"]
                assert hashlib.sha256(history).hexdigest() == rows[2]["responseSHA256"]
        checked = offline(settings, bundle, at=assessment["checkedAt"], success=phase == "history")
        assert checked == assessment, "offline/worker assessment differs for exact evidence and time"
        assert peer.document("finder-edge", "/_qualification/evidence")["checkpointGate"] == "released"
        state.setdefault("checkpoint_history", {})[phase] = {"oldSize": old_size, "newSize": new_size, "oldRoot": old_root, "newRoot": new_root,
            "historyPath": path, "originalHistorySHA256": hashlib.sha256(original).hexdigest(), "jobId": pending_job["id"]}
        return {"native_log_advanced": True, "exact_history_request": True, "native_signed_evidence": True,
                "offline_agrees": True, "single_attempt": True}
    finally:
        evidence(peer, "native")


def exercise(phase, api, peer, state, settings):
    checks = {}
    if phase in ("history", "history-missing", "history-corrupt"):
        return checkpoint_advancement(phase, api, peer, state, settings)
    if phase in ("ra-signer", "ra-key-removal", "ra-restored", "tl-signer", "tl-key-removal", "signers-restored"):
        return signer_changes(phase, api, peer, state, settings)
    if phase in ("disabled", "unselected", "disabled-again"):
        before = len(evidence(peer))
        if phase == "disabled":
            assert before == 0, "metadata-only native/Catalog phases fetched evidence"
        jobs_before = {row["id"] for row in api.pages("/api/v1/jobs")}
        request = select(api, state)
        refused = enqueue(api, state, request, expected=503)
        assert refused["code"] == "DEPENDENCY_UNAVAILABLE"
        for text in ("alpha",):
            select(api, state, text)
        assert len(evidence(peer)) == before
        assert {row["id"] for row in api.pages("/api/v1/jobs")} == jobs_before
        checks["discovery_and_refusal_have_zero_evidence_calls"] = True
        if phase == "disabled-again":
            old = completed(api, state, state["v1"])
            assert old["current"] is False and old["applicability"] == "historical"
            checks["disabled_policy_preserves_private_history"] = True
    elif phase == "selected":
        original, headers = api.call(lifecycle.PREFIX + "sources/" + state["source_id"])
        changes = {"trustVerificationPolicy": "owned-ans", "scope": {**original["scope"], "publishers": original["scope"]["publishers"] + ["trust-v2.ans.test", "ra-next.ans.test", "tl-next.ans.test"]}}
        preview, _ = api.call(lifecycle.PREFIX + "sources/" + state["source_id"] + "/change-previews", {"intent": "update", **changes})
        api.call(lifecycle.PREFIX + "sources/" + state["source_id"], {**changes, "changePreviewDigest": preview["digest"], "reason": "Qualify private assessments"}, method="PATCH", etag=headers["etag"])
        definition = native.seed_definition("trust-v2", "MCP")
        with tempfile.TemporaryDirectory(prefix="csr-", dir=settings["trust_output"]) as temporary:
            body = {**definition, "identityCsrPEM": native.csr(definition["agentHost"], Path(temporary), "identity"), "serverCsrPEM": native.csr(definition["agentHost"], Path(temporary), "server")}
            pending = peer.document("ra", "/v2/ans/agents", body, auth=True, expected=202)
        state["v2_agent"] = pending["agentId"]
        for stage in ("verify-acme", "verify-dns"):
            peer.document("ra", "/v2/ans/agents/" + state["v2_agent"] + "/" + stage, {}, auth=True, expected=202)
        wait_for("native V2 feed acknowledgement", lambda: next((r for r in peer.document("ra", "/v1/agents/events?limit=100")["items"] if r["agentId"] == state["v2_agent"]), None))
        wait_for("native V2 Finder projection", lambda: peer.document("finder", "/v1/search", {"query": {"text": "trust-v2"}, "federation": "none"})["results"])
        policy = settings["offline_policy"]
        before = len(evidence(peer))
        request = select(api, state)
        key = uuid.uuid4().hex
        first = enqueue(api, state, request, key=key)
        result = completed(api, state, first["id"])
        positive(result)
        state["v1"] = first["id"]
        state["bundle"] = result["result"]["evidence"]
        state["assessment"] = result["result"]["assessment"]
        state["replay_request"], state["replay_key"] = request, key
        after = len(evidence(peer))
        assert 3 <= after - before <= 4
        replay = enqueue(api, state, request, key=key)
        assert replay["id"] == first["id"] and len(evidence(peer)) == after
        for token, expected in ((settings["reader"], 403), (settings["other"], 404)):
            path = lifecycle.PREFIX + "sources/" + state["source_id"] + "/trust-assessments/" + first["id"]
            api.call(path, token=token, expected=expected)
        enqueue(api, state, request, token=settings["reader"], expected=403)
        enqueue(api, state, request, token=settings["other"], expected=404)
        second = enqueue(api, state, select(api, state, "trust-v2"))
        v2 = completed(api, state, second["id"])
        positive(v2, "V2")
        assert v2["result"]["assessment"]["subject"] != result["result"]["assessment"]["subject"]
        offline_v1 = offline(settings, state["bundle"], at=state["assessment"]["checkedAt"])
        for name, field in (("ra.pem", "registrationKeyFingerprint"), ("tl.pem", "logKeyFingerprint")):
            pem = (Path(settings["trust_output"]) / name).read_text().splitlines()
            fingerprint = hashlib.sha256(base64.b64decode("".join(pem[1:-1]), validate=True)).hexdigest()
            assert fingerprint == state["assessment"][field] == v2["result"]["assessment"][field] == offline_v1[field]
        offline(settings, v2["result"]["evidence"], at=v2["result"]["assessment"]["checkedAt"])
        (Path(settings["trust_output"]) / "v1.json").write_text(json.dumps(state["bundle"]))
        (Path(settings["trust_output"]) / "v2.json").write_text(json.dumps(v2["result"]["evidence"]))
        wrong = {**policy, "logKeyFiles": ["other.pem"]}
        offline(settings, state["bundle"], policy=wrong, success=False)
        transplant = {**state["bundle"], "receipt": v2["result"]["evidence"]["receipt"]}
        transplanted = offline(settings, transplant, success=False)
        assert transplanted["target"]["state"] != "valid"
        checks.update(real_v1=True, real_v2=True, explicit_only=True, bounded_requests=True, replay_no_fetch=True, private_tenant_authority=True, actual_command_offline=True, wrong_key_and_transplant=True)
    elif phase == "unsupported":
        before = len(evidence(peer))
        try:
            evidence(peer, "unsupported-framework")
            result = completed(api, state, enqueue(api, state)["id"])
            assessment = result["result"]["assessment"]
            assert not result["current"] and assessment["target"]["state"] == "unsupported", assessment
            assert assessment["target"]["code"] == "trust_framework_unsupported"
            assert len(evidence(peer)) == before
            checks["unsupported_framework_zero_evidence"] = True
        finally:
            evidence(peer, "native")
    elif phase == "corruption":
        expectations = {"receipt-signature": ("receipt", "invalid"), "receipt-root": ("checkpoint", "invalid"), "checkpoint": ("checkpoint", "invalid"), "missing-status": ("status", "unavailable"), "gone-status": ("status", "unavailable")}
        try:
            for mode, (field, expected) in expectations.items():
                evidence(peer, mode)
                result = completed(api, state, enqueue(api, state)["id"])
                assessment = result["result"]["assessment"]
                assert result["current"] is False and assessment["currentRegistration"] is False
                assert assessment[field]["state"] == expected, (mode, assessment)
                if field == "status":
                    assert assessment["receipt"]["state"] == assessment["target"]["state"] == "valid"
                checks[mode] = True
        finally:
            evidence(peer, "native")
        tampered = dict(state["bundle"])
        receipt = bytearray(base64.b64decode(tampered["receipt"]))
        receipt[-1] ^= 1
        tampered["receipt"] = base64.b64encode(receipt).decode()
        assert offline(settings, tampered, success=False)["receipt"]["state"] == "invalid"
        checks["offline_tamper"] = True
    elif phase in ("restart", "finder-independent", "cancel"):
        before = len(evidence(peer))
        pending = enqueue(api, state)
        state["pending"] = pending["id"]
        assert pending["state"] == "queued" and len(evidence(peer)) == before
        api.call(lifecycle.PREFIX + "sources/" + state["source_id"] + "/trust-assessments/" + pending["id"], expected=409)
        if phase == "cancel":
            api.call("/api/v1/jobs/" + pending["id"] + ":cancel", {"reason": "Qualify pre-acquisition cancellation"})
            final = completed(api, state, pending["id"], success=False)
            assert final["state"] == "cancelled" and len(evidence(peer)) == before
        checks["queued_without_evidence"] = True
    elif phase in ("restart-complete", "finder-complete"):
        result = completed(api, state, state["pending"])
        positive(result)
        retained = completed(api, state, state["v1"])
        assert retained["result"]["evidence"] == state["bundle"]
        checks["durable_job_and_exact_history"] = True
    elif phase == "cancel-running":
        try:
            evidence(peer, "delay")
            before = len(evidence(peer))
            pending = enqueue(api, state)
            wait_for("in-flight evidence request", lambda: len(evidence(peer)) > before)
            api.call("/api/v1/jobs/" + pending["id"] + ":cancel", {"reason": "Qualify in-flight cancellation"})
            final = completed(api, state, pending["id"], success=False)
            assert final["state"] == "cancelled" and len(evidence(peer)) == before + 1, final
            checks["inflight_cancel_stops_followup_fetch_and_completion"] = True
        finally:
            evidence(peer, "native")
    elif phase == "policy-change":
        evidence(peer, "delay")
        before = len(evidence(peer))
        pending = enqueue(api, state)
        wait_for("first evidence request started", lambda: len(evidence(peer)) > before)
        source, headers = api.call(lifecycle.PREFIX + "sources/" + state["source_id"])
        api.call(lifecycle.PREFIX + "sources/" + state["source_id"] + ":pause", {"reason": "Qualify source fence during acquisition"}, etag=headers["etag"])
        final = completed(api, state, pending["id"], success=False)
        assert final["failureClass"] == "policy_denied" and len(evidence(peer)) == before + 1, final
        evidence(peer, "native")
        _, headers = api.call(lifecycle.PREFIX + "sources/" + state["source_id"])
        api.call(lifecycle.PREFIX + "sources/" + state["source_id"] + ":resume", {"reason": "Restore owned source"}, etag=headers["etag"])
        checks["source_change_stops_followup_fetch_and_completion"] = True
    elif phase == "tl-outage":
        pending = enqueue(api, state)
        state["outage_job"] = pending["id"]
        waiting = wait_for("TL failure classified for retry", lambda: (row if (row := job(api, pending["id"]))["state"] == "waiting" else None), timeout=45)
        assert waiting["failureClass"] == "transient_upstream", waiting
        select(api, state)
        checks["evidence_outage_does_not_break_discovery"] = True
    elif phase == "tl-recovery":
        result = completed(api, state, state["outage_job"])
        positive(result)
        assert job(api, state["outage_job"])["attempt"] > 1
        state["key_baseline"] = state["outage_job"]
        checks["same_job_recovered"] = True
    elif phase in ("overlap", "key-removal", "restored", "expiry"):
        old = completed(api, state, state["key_baseline"])
        assert not old["current"] and old["applicability"] == "historical"
        result = completed(api, state, enqueue(api, state)["id"])
        if phase == "key-removal":
            assert not result["current"] and result["result"]["assessment"]["receipt"]["state"] == "invalid"
        else:
            positive(result)
        if phase == "expiry":
            identifier = state["jobs"][-1]
            expired = wait_for("retained assessment expires", lambda: (row if (row := completed(api, state, identifier))["applicability"] == "expired" else None), timeout=15)
            assert not expired["current"] and expired["result"]["assessment"]["receipt"]["state"] == "valid"
        state["key_baseline"] = state["jobs"][-1]
        checks["loaded_key_policy_and_historical_evidence"] = True
    elif phase == "revocation":
        pending = enqueue(api, state, select(api, state, "trust-v2"))
        assert pending["state"] == "queued"
        state["revoked_job"] = pending["id"]
        peer.document("ra", "/v2/ans/agents/" + state["v2_agent"] + "/revoke", {"reason": "CESSATION_OF_OPERATION", "comments": "Owned native lifecycle test"}, auth=True)
        wait_for("native V2 revocation feed", lambda: any(row["agentId"] == state["v2_agent"] and row["eventType"] == "AGENT_REVOKED" for row in peer.document("ra", "/v1/agents/events?limit=100")["items"]))
        wait_for("native V2 removed from Finder", lambda: not peer.document("finder", "/v1/search", {"query": {"text": "trust-v2"}, "federation": "none"})["results"])
        # Native terminal status returns HTTP 410; never turn it into signed revocation proof.
        raw = peer.request("tl", "/v1/agents/" + state["v2_agent"] + "/status-token", expected=410)
        assert raw
        assert completed(api, state, state["v1"])["result"]["evidence"] == state["bundle"]
        checks["native_revocation_and_historical_preservation"] = True
    elif phase == "revocation-complete":
        result = completed(api, state, state["revoked_job"])
        assessment = result["result"]["assessment"]
        assert not result["current"] and not assessment["currentRegistration"], assessment
        assert assessment["receipt"]["state"] == "valid", assessment
        assert assessment["status"]["state"] != "valid", assessment
        assert completed(api, state, state["v1"])["result"]["evidence"] == state["bundle"]
        checks["native_terminal_registration_not_current"] = True
    else:
        raise ValueError("unselected trust phase")
    return checks


def main():
    phase, settings_path, report_path = sys.argv[1:]
    settings = json.loads(Path(settings_path).read_text())
    path = Path(settings["trust_state"])
    state = json.loads(path.read_text()) if path.exists() else {"source_id": json.loads(Path(settings["lifecycle_state"]).read_text())["source_id"]}
    api, peer = API(settings), native.Native(settings)
    if "offline_policy" in settings:
        (Path(settings["trust_output"]) / "policy.json").write_text(json.dumps(settings["offline_policy"]))
    report = {"phase": phase, "status": "fail", "checks": {}}
    try:
        report["checks"] = exercise(phase, api, peer, state, settings)
        report["status"] = "pass"
    except Exception as error:
        report["error"] = str(error)
        traceback.print_exc()
    report.update(source_id=state["source_id"], jobs=state.get("jobs", []), captures=api.captures, native_captures=peer.captures,
                  evidence_requests=evidence(peer), checkpoint_history=state.get("checkpoint_history", {}),
                  signer_native=state.get("signer_native", {}), signer_evidence=state.get("signer_evidence", {}))
    Path(report_path).write_text(json.dumps(report, indent=2) + "\n")
    Path(report_path).chmod(0o600)
    path.write_text(json.dumps(state))
    path.chmod(0o600)
    if report["status"] != "pass":
        raise SystemExit(1)


if __name__ == "__main__":
    main()
