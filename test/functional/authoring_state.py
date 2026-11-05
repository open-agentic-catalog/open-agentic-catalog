"""Finite retained-state projection for the invocation-owned authoring fixture."""

import re

MAX_ROWS = 1000
MAX_TABLE_BYTES = 4 * 1024 * 1024
MAX_TOTAL_BYTES = 16 * 1024 * 1024
AUDIT_ACTIONS = ("resource.created", "resource_version.created")
OUTBOX_TYPES = ("com.open.agentic.catalog.resource.created.v1", "com.open.agentic.catalog.resource.version.created.v1")
CORE = ("resources", "resource_versions", "resource_version_revisions")
TABLES = (*CORE, "representations", "evidence_sets", "version_validation_runs", "document_blobs", "audit_events", "outbox_events")
# These fields alone change during legal worker dispatch or retention marking.
BOOKKEEPING = ("published_at", "lease_owner", "lease_expires_at", "dispatch_after", "cleanup_after")


def snapshot_sql():
    """Limit rows and total encoded bytes before any string aggregation."""
    rows, stats, fields = [], [], []
    for table in TABLES:
        order = {"version_validation_runs": "tenant_id,version_revision_id,run_number", "document_blobs": "tenant_id,id"}.get(table, "id")
        columns = order.split(",")
        where = ""
        document = "to_jsonb(r)"
        if table == "audit_events":
            where = " WHERE action IN (" + ",".join("'" + x + "'" for x in AUDIT_ACTIONS) + ")"
        if table == "outbox_events":
            where = " WHERE event_type IN (" + ",".join("'" + x + "'" for x in OUTBOX_TYPES) + ")"
            document += " - ARRAY[" + ",".join("'" + x + "'" for x in BOOKKEEPING) + "]"
        rows.append(f"{table}_rows AS MATERIALIZED (SELECT {','.join(columns)}, ({document})::text AS body FROM {table} r{where} ORDER BY {order} LIMIT {MAX_ROWS + 1})")
        stats.append(f"SELECT '{table}' AS name, count(*) AS n, COALESCE(sum(octet_length(body)),0) AS bytes FROM {table}_rows")
        # The scalar subquery executes only when all pre-aggregation bounds pass.
        fields.append(f"'{table}', json_build_object('count',(SELECT n FROM stats WHERE name='{table}'),'digest',CASE WHEN limits.ok THEN (SELECT md5(COALESCE(string_agg(body,'' ORDER BY {order}),'')) FROM {table}_rows) ELSE NULL END)")
    rows.append("stats AS MATERIALIZED (" + " UNION ALL ".join(stats) + ")")
    rows.append(f"limits AS MATERIALIZED (SELECT bool_and(n<={MAX_ROWS} AND bytes<={MAX_TABLE_BYTES}) AND sum(bytes)<={MAX_TOTAL_BYTES} AS ok FROM stats)")
    return "BEGIN READ ONLY; SET LOCAL statement_timeout='3s'; WITH " + ",".join(rows) + " SELECT json_build_object(" + ",".join(fields) + ") FROM limits; COMMIT;"


def checked_snapshot(value):
    assert isinstance(value, dict) and set(value) == set(TABLES), "authoring state inventory incomplete"
    for item in value.values():
        assert (isinstance(item, dict) and set(item) == {"count", "digest"}
                and type(item["count"]) is int and 0 <= item["count"] <= MAX_ROWS
                and isinstance(item["digest"], str) and re.fullmatch(r"[a-f0-9]{32}", item["digest"])), "authoring state bound or digest invalid"
    return value


def created_delta(before, after, operation, *, blob=False):
    """Qualify each table with successful writes before relying on its oracle."""
    changed = {"audit_events", "outbox_events", "resources"} if operation == "createResource" else {
        "audit_events", "outbox_events", "resource_versions", "resource_version_revisions", "representations", "evidence_sets", "version_validation_runs"}
    if blob:
        changed.add("document_blobs")
    for table in TABLES:
        if table in changed:
            assert after[table]["count"] == before[table]["count"] + 1 and after[table]["digest"] != before[table]["digest"], "state oracle missed committed " + table
        else:
            assert after[table] == before[table], "creation changed unrelated " + table


def created_events(value, operation, target, tenant, request_id):
    """Bind the positive audit and notification controls to the creating request."""
    audit, outbox = value["audit"], value["outbox"]
    assert isinstance(audit, list) and len(audit) == 1 and isinstance(outbox, list) and len(outbox) == 1, "wrong creation evidence count"
    audit, outbox = audit[0], outbox[0]
    kind = "resource" if operation == "createResource" else "resource_version"
    action = kind + ".created"
    public_action = action.replace("_", ".")
    assert audit["tenant_id"] == outbox["tenant_id"] == tenant, "wrong creation evidence tenant"
    assert audit["action"] == action and audit["target_type"] == kind and audit["target_id"] == target, "wrong creation audit binding"
    assert audit["request_id"] == request_id and request_id and audit["actor"], "wrong creation request binding"
    assert re.fullmatch(r"sha256:[a-f0-9]{64}", audit["after_digest"]), "missing creation evidence digest"
    assert audit["occurred_at"] == outbox["created_at"], "creation evidence chronology differs"
    assert outbox["subject"] == target and outbox["event_type"] == "com.open.agentic.catalog." + public_action + ".v1" and not outbox["is_internal"], "wrong creation notification binding"
    assert outbox["data"] == {"action": public_action, "targetType": kind, "targetId": target, "digest": audit["after_digest"]}, "wrong creation notification payload"
