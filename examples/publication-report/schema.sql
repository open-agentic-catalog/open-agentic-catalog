-- Receiver-owned state. Never apply this file to the Catalog database.
CREATE TABLE IF NOT EXISTS publication_receiver_settings (
 singleton boolean PRIMARY KEY CHECK(singleton), tenant text NOT NULL,
 host text NOT NULL, origin text NOT NULL
);
CREATE TABLE IF NOT EXISTS publication_receiver_inbox (
 source text NOT NULL, id text NOT NULL, event jsonb NOT NULL,
 accepted_at timestamptz NOT NULL, completed_at timestamptz,
 PRIMARY KEY(source,id)
);
CREATE INDEX IF NOT EXISTS publication_receiver_pending ON publication_receiver_inbox(accepted_at,source,id) WHERE completed_at IS NULL;
CREATE TABLE IF NOT EXISTS publication_receiver_report (
 singleton boolean PRIMARY KEY CHECK(singleton), last_success jsonb,
 attempted_at timestamptz, failure text NOT NULL DEFAULT '',
 capacity_loss bigint NOT NULL DEFAULT 0 CHECK(capacity_loss>=0),
 delivery_loss bigint NOT NULL DEFAULT 0 CHECK(delivery_loss>=0),
 loss_observed_at timestamptz
);
INSERT INTO publication_receiver_report(singleton) VALUES(true) ON CONFLICT DO NOTHING;
