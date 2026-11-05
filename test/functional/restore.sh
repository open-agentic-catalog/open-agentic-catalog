#!/bin/sh
set -eu

: "${CATALOG_FUNCTIONAL_PROJECT:?functional project is required}"
: "${CATALOG_FUNCTIONAL_IMAGE:?functional image is required}"
: "${CATALOG_FUNCTIONAL_FIXTURE_IMAGE:?functional fixture image is required}"
: "${CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY:?functional fixture directory is required}"
: "${CATALOG_FUNCTIONAL_SUBNET:?functional subnet is required}"
: "${CATALOG_FUNCTIONAL_HTTP_PORT:?functional HTTP port is required}"
: "${CATALOG_FUNCTIONAL_FIXTURE_PORT:?functional fixture port is required}"
: "${CATALOG_FUNCTIONAL_BASE_URL:?functional base URL is required}"
: "${CATALOG_FUNCTIONAL_FIXTURE_URL:?functional fixture URL is required}"
: "${CATALOG_FUNCTIONAL_RECOVERY_POINT:?functional recovery point is required}"
: "${CATALOG_FUNCTIONAL_AUDIT_MANIFEST:?functional audit manifest is required}"

compose_command=${CATALOG_FUNCTIONAL_COMPOSE_COMMAND:-docker compose}
python_command=${CATALOG_FUNCTIONAL_PYTHON_COMMAND:-python3}
tenant_id=00000000-0000-7000-8000-000000000001

catalog_compose() {
	# Intentional splitting preserves the supported multi-word COMPOSE override.
	# shellcheck disable=SC2086
	IMAGE="$CATALOG_FUNCTIONAL_IMAGE" VERSION=functional COMMIT="${CATALOG_FUNCTIONAL_COMMIT:?}" \
		CATALOG_COMPOSE_HTTP_PORT="$CATALOG_FUNCTIONAL_HTTP_PORT" CATALOG_COMPOSE_POSTGRES_PORT=0 \
		CATALOG_COMPOSE_FIXTURE_PORT="$CATALOG_FUNCTIONAL_FIXTURE_PORT" CATALOG_COMPOSE_SUBNET="$CATALOG_FUNCTIONAL_SUBNET" \
		CATALOG_COMPOSE_FEDERATION_POLICY_FILE="$CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY/policy.json" \
		CATALOG_COMPOSE_FEDERATION_TRUST_DIRECTORY="$CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY/trust" \
		CATALOG_HTTP_REQUEST_TIMEOUT=3s CATALOG_WORKER_SOURCE_MAINTENANCE_INTERVAL=1m \
		CATALOG_WORKER_HOUSEKEEPING_INTERVAL=1m CATALOG_WORKER_TERMINAL_JOB_RETENTION=24h \
		CATALOG_WORKER_ACKNOWLEDGED_OUTBOX_RETENTION=1h \
		$compose_command --env-file /dev/null -f compose.yaml -f test/functional/compose.yaml \
		-p "$CATALOG_FUNCTIONAL_PROJECT" "$@"
}

psql_catalog() {
	catalog_compose exec -T postgres psql --username catalog --dbname catalog --set ON_ERROR_STOP=1 --quiet "$@"
}

fixture_state() {
	curl --fail --silent --show-error --connect-timeout 2 --max-time 5 \
		--cacert "$CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY/trust/functional-ca.pem" \
		"$CATALOG_FUNCTIONAL_FIXTURE_URL/state" |
		"$python_command" -c 'import json,sys; print(json.dumps(json.load(sys.stdin),sort_keys=True,separators=(",",":")))'
}

verify_restore() {
	expected_integrity=$1
	required_code=$2
	forbidden_code=$3
	cursor=
	pages=0
	found_required=0
	found_forbidden=0
	found_invalid=0
	while :; do
		if [ -n "$cursor" ]; then
			report=$(catalog_compose run --rm --no-deps migrate restore verify --config /config/config.json \
				--tenant "$tenant_id" --recovery-point "$CATALOG_FUNCTIONAL_RECOVERY_POINT" \
				--api-role catalog_api --worker-role catalog_worker --limit 7 --cursor "$cursor")
		else
			report=$(catalog_compose run --rm --no-deps migrate restore verify --config /config/config.json \
				--tenant "$tenant_id" --recovery-point "$CATALOG_FUNCTIONAL_RECOVERY_POINT" \
				--api-role catalog_api --worker-role catalog_worker --limit 7)
		fi
		printf '%s' "$report" | "$python_command" -c '
import json,sys
d=json.load(sys.stdin)
required={"formatVersion","checkSetVersion","recoveryPoint","tenantId","schemaVersion","privilegeManifestVersion","traversal","integrity","processed","decodedBytes","findings","assuranceBoundary"}
if not required.issubset(d) or d["formatVersion"] != 1 or d["checkSetVersion"] != 7 or d["processed"] > 7 or d["decodedBytes"] > 83886080:
    raise SystemExit("invalid bounded restore verification report")
if d["traversal"] not in ("continued","complete") or d["integrity"] not in ("valid","invalid"):
    raise SystemExit("invalid restore verification conclusion")
for finding in d["findings"]:
    if set(finding) - {"code","class","severity","domain","recordId","disposition"}:
        raise SystemExit("open restore finding")
'
		integrity=$(printf '%s' "$report" | "$python_command" -c 'import json,sys; print(json.load(sys.stdin)["integrity"])')
		if [ "$integrity" = invalid ]; then
			if [ "$expected_integrity" = valid ]; then
				printf 'Unexpected invalid restore page: %s\n' "$report" >&2
				return 1
			fi
			found_invalid=1
		fi
		if [ -n "$required_code" ] && printf '%s' "$report" | "$python_command" -c 'import json,sys; code=sys.argv[1]; raise SystemExit(0 if any(x["code"] == code for x in json.load(sys.stdin)["findings"]) else 1)' "$required_code"; then
			found_required=1
		fi
		if [ -n "$forbidden_code" ] && printf '%s' "$report" | "$python_command" -c 'import json,sys; code=sys.argv[1]; raise SystemExit(0 if any(x["code"] == code for x in json.load(sys.stdin)["findings"]) else 1)' "$forbidden_code"; then
			found_forbidden=1
		fi
		pages=$((pages + 1))
		traversal=$(printf '%s' "$report" | "$python_command" -c 'import json,sys; print(json.load(sys.stdin)["traversal"])')
		if [ "$traversal" = complete ]; then
			break
		fi
		cursor=$(printf '%s' "$report" | "$python_command" -c 'import json,sys; print(json.load(sys.stdin).get("continuation",""))')
		[ -n "$cursor" ] || { printf '%s\n' 'Continued restore report omitted its checkpoint.' >&2; return 1; }
		[ "$pages" -lt 200 ] || { printf '%s\n' 'Restore verification did not complete within its functional page bound.' >&2; return 1; }
	done
	[ "$pages" -gt 1 ] || { printf '%s\n' 'Restore verification did not exercise continuation.' >&2; return 1; }
	if [ "$expected_integrity" = invalid ] && [ "$found_invalid" -ne 1 ]; then
		printf '%s\n' 'Restore verification did not find invalid retained state across the complete traversal.' >&2
		return 1
	fi
	[ -z "$required_code" ] || [ "$found_required" -eq 1 ] || { printf 'Restore verification omitted required finding %s.\n' "$required_code" >&2; return 1; }
	[ -z "$forbidden_code" ] || [ "$found_forbidden" -eq 0 ] || { printf 'Restore verification retained forbidden finding %s.\n' "$forbidden_code" >&2; return 1; }
}

# The verifier is an offline operator path: both product roles must remain down.
status=$(curl --silent --connect-timeout 1 --max-time 2 --output /dev/null --write-out '%{http_code}' "$CATALOG_FUNCTIONAL_BASE_URL/livez" 2>/dev/null || true)
[ "$status" = 000 ] || { printf 'Restore exercise unexpectedly reached API status %s.\n' "$status" >&2; exit 1; }
before_fixture=$(fixture_state)
before_publication=$(psql_catalog --tuples-only --no-align --command "SELECT id::text||':'||generation::text||':'||etag||':'||encode(digest(document::text,'sha256'),'hex') FROM publication_snapshots WHERE tenant_id='$tenant_id' AND state='active'")
[ -n "$before_publication" ] || { printf '%s\n' 'Restore fixture has no active publication.' >&2; exit 1; }

psql_catalog --command "INSERT INTO catalog_schema_migrations(version,checksum) VALUES (999,'functional-unknown')" >/dev/null
if catalog_compose run --rm --no-deps migrate migrate plan --config /config/config.json >/dev/null 2>&1; then
	printf '%s\n' 'Migration planning accepted an unknown applied version.' >&2
	exit 1
fi
psql_catalog --command "DELETE FROM catalog_schema_migrations WHERE version=999" >/dev/null
catalog_compose run --rm --no-deps migrate migrate plan --config /config/config.json >/dev/null
catalog_compose run --rm --no-deps migrate profiles verify >/dev/null

verify_restore valid '' RESTORE_SEARCH_PROJECTION_DRIFT
catalog_compose run --rm --no-deps --volume "$CATALOG_FUNCTIONAL_AUDIT_MANIFEST:/tmp/audit-manifest.json:ro" migrate \
	audit compare-restore --config /config/config.json --file /tmp/audit-manifest.json \
	--recovery-point "$CATALOG_FUNCTIONAL_RECOVERY_POINT" >/dev/null

observation_id=$(psql_catalog --tuples-only --no-align --command "SELECT id FROM observations WHERE tenant_id='$tenant_id' AND normalized_entry_digest IS NOT NULL ORDER BY id LIMIT 1")
[ -n "$observation_id" ] || { printf '%s\n' 'Restore fixture has no normalized observation to repair.' >&2; exit 1; }
psql_catalog --command "
ALTER TABLE observations
  DROP CONSTRAINT observations_search_display_pair_check,
  DROP CONSTRAINT observations_search_description_pair_check,
  DROP CONSTRAINT observations_search_tags_pair_check,
  DROP CONSTRAINT observations_search_queries_pair_check,
  DROP CONSTRAINT observations_search_publisher_pair_check,
  DROP CONSTRAINT observations_search_vector_pair_check;
ALTER TABLE observations DISABLE TRIGGER observations_catalog_search_projection;
UPDATE observations SET search_display_name=NULL,search_description=NULL,search_tags=NULL,search_representative_queries=NULL,search_publisher=NULL,search_vector=NULL WHERE tenant_id='$tenant_id' AND id='$observation_id';
ALTER TABLE observations ENABLE TRIGGER observations_catalog_search_projection;
ALTER TABLE observations
  ADD CONSTRAINT observations_search_display_pair_check CHECK ((normalized_entry_digest IS NULL) = (search_display_name IS NULL)) NOT VALID,
  ADD CONSTRAINT observations_search_description_pair_check CHECK ((normalized_entry_digest IS NULL) = (search_description IS NULL)) NOT VALID,
  ADD CONSTRAINT observations_search_tags_pair_check CHECK ((normalized_entry_digest IS NULL) = (search_tags IS NULL)) NOT VALID,
  ADD CONSTRAINT observations_search_queries_pair_check CHECK ((normalized_entry_digest IS NULL) = (search_representative_queries IS NULL)) NOT VALID,
  ADD CONSTRAINT observations_search_publisher_pair_check CHECK ((normalized_entry_digest IS NULL) = (search_publisher IS NULL)) NOT VALID,
  ADD CONSTRAINT observations_search_vector_pair_check CHECK ((normalized_entry_digest IS NULL) = (search_vector IS NULL)) NOT VALID" >/dev/null
# The deliberate missing projection also invalidates the current validated schema.
verify_restore invalid RESTORE_SEARCH_PROJECTION_DRIFT ''
repair=$(catalog_compose run --rm --no-deps migrate repair search-projections --config /config/config.json \
	--tenant "$tenant_id" --reason 'Restore derived search projection' --operator-label functional-restore --limit 1)
printf '%s' "$repair" | "$python_command" -c 'import json,sys; d=json.load(sys.stdin); raise SystemExit(0 if d["processed"] == 1 and d["complete"] is True and d["hasMore"] is False else "unexpected repair result")'
psql_catalog --command "
ALTER TABLE observations VALIDATE CONSTRAINT observations_search_display_pair_check;
ALTER TABLE observations VALIDATE CONSTRAINT observations_search_description_pair_check;
ALTER TABLE observations VALIDATE CONSTRAINT observations_search_tags_pair_check;
ALTER TABLE observations VALIDATE CONSTRAINT observations_search_queries_pair_check;
ALTER TABLE observations VALIDATE CONSTRAINT observations_search_publisher_pair_check;
ALTER TABLE observations VALIDATE CONSTRAINT observations_search_vector_pair_check" >/dev/null
verify_restore valid '' RESTORE_SEARCH_PROJECTION_DRIFT

invalid_snapshot=00000000-0000-7000-8000-00000000f003
psql_catalog --command "
ALTER TABLE publication_snapshots DISABLE TRIGGER publication_snapshots_lifecycle;
INSERT INTO publication_snapshots(id,tenant_id,host,document_family,profile_id,generation,state,etag,document,created_at,activated_at)
SELECT '$invalid_snapshot',tenant_id,host,document_family,profile_id,(SELECT max(generation)+1 FROM publication_snapshots WHERE tenant_id='$tenant_id' AND host=active.host),'candidate',etag,document,created_at,NULL
  FROM publication_snapshots active WHERE tenant_id='$tenant_id' AND state='active' ORDER BY generation DESC LIMIT 1;
ALTER TABLE publication_snapshots ENABLE TRIGGER publication_snapshots_lifecycle" >/dev/null
verify_restore invalid RESTORE_PUBLICATION_SNAPSHOT_INVALID ''
psql_catalog --command "ALTER TABLE publication_snapshots DISABLE TRIGGER publication_snapshots_retained_row; DELETE FROM publication_snapshots WHERE id='$invalid_snapshot'; ALTER TABLE publication_snapshots ENABLE TRIGGER publication_snapshots_retained_row" >/dev/null
verify_restore valid '' RESTORE_PUBLICATION_SNAPSHOT_INVALID

after_fixture=$(fixture_state)
[ "$after_fixture" = "$before_fixture" ] || { printf '%s\n' 'Read-only verification contacted the federation fixture.' >&2; exit 1; }
after_publication=$(psql_catalog --tuples-only --no-align --command "SELECT id::text||':'||generation::text||':'||etag||':'||encode(digest(document::text,'sha256'),'hex') FROM publication_snapshots WHERE tenant_id='$tenant_id' AND state='active'")
[ "$after_publication" = "$before_publication" ] || { printf '%s\n' 'Restore qualification changed the active publication.' >&2; exit 1; }

printf '%s\n' 'Isolated packaged restore qualification and selective repair checks passed.'
