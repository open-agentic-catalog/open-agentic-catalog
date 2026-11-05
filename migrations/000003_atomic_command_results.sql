ALTER TABLE idempotency_keys
  ADD COLUMN reservation_id uuid,
  ADD COLUMN protocol_version smallint NOT NULL DEFAULT 1,
  ADD COLUMN authorization_digest text,
  ADD COLUMN execution_token uuid,
  ADD COLUMN started_at timestamptz,
  ADD COLUMN completed_at timestamptz,
  ADD COLUMN atomic_response_status integer,
  ADD COLUMN atomic_response_headers jsonb,
  ADD COLUMN atomic_response_body bytea,
  ADD COLUMN released_at timestamptz,
  ADD COLUMN repair_reason text;

UPDATE idempotency_keys
   SET reservation_id = uuidv7(),
       started_at = created_at,
       completed_at = CASE WHEN response_status IS NOT NULL THEN created_at END;

ALTER TABLE idempotency_keys
  ALTER COLUMN reservation_id SET DEFAULT uuidv7(),
  ALTER COLUMN reservation_id SET NOT NULL,
  ALTER COLUMN started_at SET DEFAULT clock_timestamp(),
  ALTER COLUMN started_at SET NOT NULL,
  ADD CONSTRAINT idempotency_reservation_tenant_unique UNIQUE (tenant_id, reservation_id),
  ADD CONSTRAINT idempotency_protocol_version_valid CHECK (protocol_version IN (1, 2)),
  ADD CONSTRAINT idempotency_authorization_digest_valid CHECK (
    protocol_version = 1 OR authorization_digest IS NOT NULL AND authorization_digest ~ '^sha256:[0-9a-f]{64}$'
  ),
  ADD CONSTRAINT idempotency_execution_token_required CHECK (
    protocol_version = 1 OR (execution_token IS NOT NULL) = (released_at IS NULL)
  ),
  ADD CONSTRAINT idempotency_response_status_valid CHECK (response_status IS NULL OR response_status BETWEEN 100 AND 599),
  ADD CONSTRAINT idempotency_atomic_response_status_valid CHECK (atomic_response_status IS NULL OR atomic_response_status BETWEEN 100 AND 599),
  ADD CONSTRAINT idempotency_response_protocol_consistent CHECK (
    protocol_version = 1 AND atomic_response_status IS NULL AND atomic_response_headers IS NULL AND atomic_response_body IS NULL
    OR protocol_version = 2 AND response_status IS NULL AND response_headers IS NULL AND response_body IS NULL
  ),
  ADD CONSTRAINT idempotency_completion_consistent CHECK (
    (completed_at IS NULL OR response_status IS NOT NULL)
    AND protocol_version = 1
    OR protocol_version = 2 AND (atomic_response_status IS NULL) = (completed_at IS NULL)
  ),
  ADD CONSTRAINT idempotency_release_consistent CHECK (
    released_at IS NULL AND repair_reason IS NULL
    OR protocol_version = 2 AND released_at IS NOT NULL AND execution_token IS NULL
       AND response_status IS NULL AND atomic_response_status IS NULL
       AND repair_reason IS NOT NULL AND length(btrim(repair_reason)) BETWEEN 1 AND 1000
  );

CREATE INDEX idempotency_pending_repair
  ON idempotency_keys (tenant_id, started_at, reservation_id)
  WHERE released_at IS NULL AND (
    protocol_version = 1 AND response_status IS NULL
    OR protocol_version = 2 AND atomic_response_status IS NULL
  );

COMMENT ON COLUMN idempotency_keys.protocol_version IS 'Protocol 1 rows completed after the command transaction, are uncertain while pending, and lack replay authorization evidence. Protocol 2 command responses commit with every PostgreSQL command mutation.';
COMMENT ON COLUMN idempotency_keys.reservation_id IS 'Opaque UUIDv7 operator target for a tenant-bounded idempotency repair; the client key is never required in repair output.';
COMMENT ON COLUMN idempotency_keys.authorization_digest IS 'One-way digest of the authenticated actor authorization projection required for protocol 2 replay.';
COMMENT ON COLUMN idempotency_keys.atomic_response_status IS 'Protocol 2 response status stored separately so protocol 1 application replicas see the reservation as pending and fail closed.';
COMMENT ON COLUMN idempotency_keys.released_at IS 'A protocol 2 reservation proven inactive and explicitly released by the reviewed repair command.';
