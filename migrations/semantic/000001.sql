-- Optional derived search state. Applied explicitly after core migrations;
-- pgvector is provisioned by the database administrator, never a runtime role.
CREATE TABLE catalog_semantic_schema (
  version integer PRIMARY KEY CHECK (version=1),
  checksum text NOT NULL CHECK (checksum ~ '^[a-f0-9]{64}$')
);

CREATE TABLE semantic_generations (
  tenant_id uuid NOT NULL REFERENCES tenants(id),
  id uuid NOT NULL,
  profile text NOT NULL CHECK (profile ~ '^sha256:[a-f0-9]{64}$'),
  scope text NOT NULL CHECK (scope='control' OR scope ~ '^public:[a-f0-9-]{36}$'),
  disclosure text GENERATED ALWAYS AS (CASE WHEN scope='control' THEN 'control' ELSE 'public' END) STORED,
  source_generation text NOT NULL CHECK (octet_length(source_generation) BETWEEN 1 AND 256),
  dimensions integer NOT NULL CHECK (dimensions BETWEEN 1 AND 4096),
  state text NOT NULL CHECK (state IN ('building','ready')),
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL CHECK (updated_at>=created_at),
  PRIMARY KEY (tenant_id,id),
  UNIQUE (tenant_id,id,profile,dimensions,disclosure),
  FOREIGN KEY (tenant_id,id) REFERENCES jobs(tenant_id,id)
);
CREATE INDEX semantic_generations_lookup ON semantic_generations(tenant_id,profile,scope,source_generation,created_at DESC,id);
ALTER TABLE semantic_generations ENABLE ROW LEVEL SECURITY;
ALTER TABLE semantic_generations FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON semantic_generations
  USING (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid);

CREATE TABLE semantic_vectors (
  tenant_id uuid NOT NULL REFERENCES tenants(id),
  profile text NOT NULL CHECK (profile ~ '^sha256:[a-f0-9]{64}$'),
  disclosure text NOT NULL CHECK (disclosure IN ('control','public')),
  input_digest text NOT NULL CHECK (input_digest ~ '^sha256:[a-f0-9]{64}$'),
  dimensions integer NOT NULL CHECK (dimensions BETWEEN 1 AND 4096),
  embedding @vector@.vector NOT NULL,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id,profile,disclosure,input_digest),
  UNIQUE (tenant_id,profile,disclosure,input_digest,dimensions),
  CHECK (@vector@.vector_dims(embedding)=dimensions),
  CHECK (@vector@.vector_norm(embedding) BETWEEN 0.9999 AND 1.0001)
);
ALTER TABLE semantic_vectors ENABLE ROW LEVEL SECURITY;
ALTER TABLE semantic_vectors FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON semantic_vectors
  USING (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid);

CREATE TABLE semantic_members (
  tenant_id uuid NOT NULL,
  generation_id uuid NOT NULL,
  item_key text NOT NULL CHECK (octet_length(item_key) BETWEEN 1 AND 1024),
  profile text NOT NULL,
  dimensions integer NOT NULL,
  disclosure text NOT NULL CHECK (disclosure IN ('control','public')),
  input_digest text,
  failure text NOT NULL DEFAULT '' CHECK (failure IN ('','input_too_large','input_rejected')),
  PRIMARY KEY (tenant_id,generation_id,item_key),
  FOREIGN KEY (tenant_id,generation_id,profile,dimensions,disclosure) REFERENCES semantic_generations(tenant_id,id,profile,dimensions,disclosure) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id,profile,disclosure,input_digest,dimensions) REFERENCES semantic_vectors(tenant_id,profile,disclosure,input_digest,dimensions),
  CHECK ((failure='' AND input_digest IS NOT NULL) OR (failure<>'' AND input_digest IS NULL))
);
ALTER TABLE semantic_members ENABLE ROW LEVEL SECURITY;
ALTER TABLE semantic_members FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON semantic_members
  USING (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid);

CREATE FUNCTION catalog_semantic_generation_guard() RETURNS trigger LANGUAGE plpgsql AS $guard$
BEGIN
  IF NEW.state<>OLD.state AND (OLD.state<>'building' OR NEW.state<>'ready') THEN
    RAISE EXCEPTION 'invalid semantic generation transition';
  END IF;
  IF NEW.updated_at<OLD.updated_at THEN RAISE EXCEPTION 'semantic generation time cannot regress'; END IF;
  RETURN NEW;
END
$guard$;
CREATE TRIGGER semantic_generation_guard BEFORE UPDATE ON semantic_generations
  FOR EACH ROW EXECUTE FUNCTION catalog_semantic_generation_guard();
