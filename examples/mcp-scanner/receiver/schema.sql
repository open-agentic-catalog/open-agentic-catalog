-- Apply only to a dedicated receiver database/account, never Catalog storage.
CREATE TABLE IF NOT EXISTS mcp_receiver_settings (
 singleton boolean PRIMARY KEY CHECK(singleton), tenant text NOT NULL,
 origin text NOT NULL, mapping_digest text NOT NULL,
 capacity_loss bigint, delivery_loss bigint, reconciled_at timestamptz,
 reconciliation_failure text NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS mcp_receiver_inbox (
 source text NOT NULL,id text NOT NULL,event jsonb NOT NULL,
 accepted_at timestamptz NOT NULL,completed_at timestamptz,
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 3),
 next_attempt_at timestamptz NOT NULL,outcome text NOT NULL DEFAULT '',
 PRIMARY KEY(source,id)
);
CREATE INDEX IF NOT EXISTS mcp_receiver_pending ON mcp_receiver_inbox(next_attempt_at,accepted_at,source,id) WHERE completed_at IS NULL;
CREATE TABLE IF NOT EXISTS mcp_receiver_scans (
 scan_key text PRIMARY KEY,report bytea NOT NULL CHECK(octet_length(report)<=4194304),
 created_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS mcp_receiver_observations (
 resource_id text PRIMARY KEY,release_id text NOT NULL,
 scan_key text REFERENCES mcp_receiver_scans(scan_key),document jsonb NOT NULL,
 checked_at timestamptz NOT NULL
);
