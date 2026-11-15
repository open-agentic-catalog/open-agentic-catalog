"""Independent exact expectations for native Search and passive imported metadata."""

import hashlib

SOURCE = "https://peer-a.test:8444/ard/v1"
PRIVATE = "https://private-a.test:8444/ard/v1"
CONTROL = "https://peer-a.test:8444/control-registry"
KEY = "owned-agentfield-management"
PATH = "/api/ui/v1/ard"
PHASES = ("setup", "native-control", "seed", "search", "import", "restart", "refusals", "tls-refusal", "tls-recovery",
          "draft", "withdrawal", "confinement", "final-checks")
ENTRY_FIELDS = ("identifier", "displayName", "description", "type", "url", "data", "tags",
                "capabilities", "representativeQueries", "version", "updatedAt", "trustManifest")


def native_entry(original):
    """Account for the pinned struct's omitempty and closed trust projection."""
    # These three Go string fields have no omitempty tag. RawMessage data
    # retains JSON values such as {}, [], false, 0 and null when present.
    value = {key: original.get(key) or "" for key in ("identifier", "type", "displayName")}
    value.update({key: original[key] for key in ENTRY_FIELDS if key in original and key != "trustManifest"
                  and (key == "data" or original[key])})
    if original.get("trustManifest") is not None:
        trust = original["trustManifest"]
        value["trustManifest"] = {"identity": trust.get("identity", "")}
        if trust.get("identityType"):
            value["trustManifest"]["identityType"] = trust["identityType"]
    return value


def check_search(original, native, source):
    """Require one meaningful success, exact native fields and explicit source changes."""
    assert native.get("sources") == [{"url": source, "status": "ok"}], "native source did not succeed"
    assert original.get("results"), "empty upstream Search cannot establish discovery"
    rows = native.get("results")
    assert isinstance(rows, list) and len(rows) == len(original["results"]), "native membership mismatch"
    assert len({row["identifier"] for row in rows}) == len(rows), "duplicate native identity"
    wanted = {row["identifier"]: row for row in original["results"]}
    assert {row["identifier"] for row in rows} == set(wanted), "native identity mismatch"
    for row in rows:
        expected = wanted[row["identifier"]]
        assert row == {**native_entry(expected), "score": expected["score"], "source": source}, "native Search metadata mismatch"
    assert "pageToken" not in native and "referrals" not in native, "native continuation/referral behavior changed"
    return {"retainedFields": sorted(set().union(*(native_entry(row) for row in original["results"]))),
            "omittedFields": sorted(set().union(*(set(row) - set(native_entry(row)) - {"score", "source"} for row in original["results"]))),
            "sourceReplaced": any(row.get("source") != source for row in original["results"]),
            "upstreamContinuationPresent": bool(original.get("pageToken")), "nativeContinuationPresent": False}


def import_fields(entry, source):
    """Predict the native importer subset independently of its returned dashboard."""
    value = {"id": "ext_" + hashlib.sha256(entry["identifier"].encode()).hexdigest()[:16],
             "source_registry": source, "identifier": entry["identifier"],
             "type": entry["type"], "display_name": entry["displayName"]}
    for wire, saved in (("description", "description"), ("url", "url"), ("data", "data"),
                        ("representativeQueries", "representative_queries")):
        if wire in entry and (wire == "data" or entry[wire]):
            value[saved] = entry[wire]
    # This native version recognizes legacy urn:ai, not Catalog's urn:air.
    parts = entry["identifier"].split(":")
    if len(parts) >= 3 and parts[:2] == ["urn", "ai"]:
        value["publisher"] = parts[2]
    identity = (entry.get("trustManifest") or {}).get("identity")
    if identity:
        value["trust_summary"] = identity
    return value


def check_dashboard(dashboard, entries):
    """Fresh state, summary and views must all agree; imports are never callable."""
    assert dashboard["config"]["external_invocation_enabled"] is False, "external invocation enabled"
    assert dashboard["state"]["bindings"] == {}, "native binding was created"
    assert dashboard["summary"]["imported_resources"] == len(entries), "native import summary mismatch"
    assert dashboard["summary"]["callable_external_resources"] == 0, "callable resource appeared"
    views = dashboard["imports"]
    assert len(views) == len(entries), "native import membership mismatch"
    actual = {view["entry"]["identifier"]: view for view in views}
    assert len(actual) == len(views) and set(actual) == {entry["identifier"] for entry, _ in entries}, "native import identity mismatch"
    stored = []
    for entry, source in entries:
        view = actual[entry["identifier"]]
        assert view["status"] == "imported" and "binding" not in view, "import became callable"
        saved = dict(view["entry"])
        imported_at = saved.pop("imported_at")
        assert isinstance(imported_at, str) and imported_at not in ("", "0001-01-01T00:00:00Z"), "native import time missing"
        assert saved == import_fields(entry, source), "native imported metadata mismatch"
        stored.append(view["entry"])
    assert sorted(dashboard["state"]["imports"], key=lambda row: row["id"]) == sorted(stored, key=lambda row: row["id"]), "native state/view mismatch"
    return sorted(stored, key=lambda row: row["id"])


def check_refusal(response, source, status, error):
    assert response.get("results") == [], "failed discovery returned a resource"
    assert len(response.get("sources", [])) == 1, "missing native source refusal"
    failure = response["sources"][0]
    assert failure["url"] == source and failure["status"] == status, "wrong native source refusal"
    assert error in failure.get("error", ""), "native refusal cause changed"


def finish(report):
    return (not report.get("error") and report.get("control") is None and report.get("cleanup") == "complete"
            and report.get("phases") == dict.fromkeys(PHASES, "passed"))
