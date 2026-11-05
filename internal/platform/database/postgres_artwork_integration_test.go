//go:build integration

package database

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/artwork"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/migrations"
)

func TestPostgresRestorePreservesOriginalZeroArtworkPreview(t *testing.T) {
	db := newHousekeepingDatabase(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Microsecond)
	source := createRevisedHousekeepingSource(t, db, "00000000-0000-7000-8000-000000000951", now.Add(-2*time.Hour))
	preview := housekeepingPreview("00000000-0000-7000-8000-000000000952", source, now.Add(-time.Hour), now.Add(-time.Minute))
	digestInput, err := json.Marshal(map[string]any{"id": preview.ID, "tenantId": preview.TenantID, "sourceId": preview.SourceID, "revision": preview.SourceRevision, "intent": preview.Intent, "configurationDigest": preview.ConfigurationDigest, "observationSetDigest": preview.ObservationSetDigest, "currentMatched": preview.CurrentMatched, "proposedMatched": preview.ProposedMatched, "expiresAt": preview.ExpiresAt})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(digestInput)
	preview.Digest = "sha256:" + hex.EncodeToString(digest[:])
	raw, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err = json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	delete(document, "artworkOverridesAffected")
	raw, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.operator.Exec(ctx, `INSERT INTO source_change_previews(tenant_id,id,source_id,digest,document,expires_at,created_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, preview.TenantID, preview.ID, preview.SourceID, preview.Digest, raw, preview.ExpiresAt, preview.CreatedAt); err != nil {
		t.Fatal(err)
	}
	retained, err := db.federation.GetChangePreview(ctx, preview.TenantID, preview.Digest)
	if err != nil || federation.ValidateChangePreviewIntegrity(retained) != nil {
		t.Fatalf("original expired preview read: %#v %v", retained, err)
	}
	tx, err := db.operator.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	records, more, err := scanRestoreSourcePreviews(ctx, tx, preview.TenantID, "", 10)
	if err != nil || more || len(records) != 1 || records[0].finding != nil {
		t.Fatalf("original expired preview restore: %#v %v %v", records, more, err)
	}
	var unchanged bool
	if err = tx.QueryRow(ctx, `SELECT NOT(document ? 'artworkOverridesAffected') AND digest=$3 FROM source_change_previews WHERE tenant_id=$1 AND id=$2`, preview.TenantID, preview.ID, preview.Digest).Scan(&unchanged); err != nil || !unchanged {
		t.Fatalf("restore rewrote immutable preview: %v", err)
	}
}

func TestPostgresArtworkMutationFencesAndAuditRollback(t *testing.T) {
	db, _, _, admin, _, _ := authorizationCoreFixture(t)
	ctx := identity.WithActor(t.Context(), admin)
	now := time.Now().UTC().Truncate(time.Microsecond)
	source := createRevisedHousekeepingSource(t, db, "00000000-0000-7000-8000-000000000953", now)
	managerClock := clock.NewFake(now.Add(2 * time.Second))
	manager, err := federation.NewManager(db.federation, &postgresFederationFetcher{policyDigest: source.PolicyDigest}, managerClock, id.Generator{}, federation.WithEvidenceSink(db.federation.repository, false))
	if err != nil {
		t.Fatal(err)
	}
	target := federation.ArtworkTarget{SourceID: source.ID, PresentationGeneration: 1, Identifier: "urn:air:peer.example:agents:atomic-artwork", Kind: catalog.KindAgent}
	input := federation.ArtworkOverrideInput{SourceRevision: source.Revision, Icon: federation.ArtworkIconSelection{Mode: federation.ArtworkSuppress}, Screenshots: federation.ArtworkScreenshotSelection{Mode: federation.ArtworkInherit}, Reason: "Hide publisher icon"}
	first, err := manager.SetArtworkOverride(ctx, target, input, 0)
	if err != nil {
		t.Fatal("initial artwork control", err)
	}
	input.Icon.Mode, input.Reason = federation.ArtworkInherit, "Restore publisher icon"
	current, err := manager.SetArtworkOverride(ctx, target, input, 1)
	if err != nil {
		t.Fatal("updated artwork control", err)
	}
	audits, err := db.federation.repository.AuditEvents(ctx, source.TenantID, catalog.AuditEventListRequest{Filters: audit.Query{Action: audit.ActionSourceArtworkRevised, TargetType: "source_artwork", TargetID: first.ID}, Limit: 1})
	if err != nil || len(audits) != 1 {
		t.Fatal("retained artwork audit control", audits, err)
	}
	evidence := federation.MutationEvidence{Audit: audits[0]}
	if err = federation.ValidateArtworkMutationEvidence(first, "", evidence); err != nil {
		t.Fatal("original transition must retain valid evidence", err)
	}
	// Compare exact retained data, including the head pointer and unrelated
	// evidence. The optional exclusion isolates the one expected retry append.
	state := func(excluded string) string {
		t.Helper()
		var value string
		if err := db.operator.QueryRow(ctx, `
SELECT jsonb_build_object(
 'source',(SELECT to_jsonb(s) FROM federation_sources s WHERE tenant_id=$1 AND id=$2),
 'heads',COALESCE((SELECT jsonb_agg(to_jsonb(h) ORDER BY h.tenant_id,h.source_id,h.target_key) FROM source_artwork_heads h WHERE $4='' OR h.tenant_id<>$1 OR h.source_id<>$2 OR h.target_key<>$3),'[]'::jsonb),
 'history',COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.tenant_id,r.id) FROM source_artwork_revisions r WHERE r.id::text<>$4),'[]'::jsonb),
 'audit',COALESCE((SELECT jsonb_agg(to_jsonb(a) ORDER BY a.tenant_id,a.id) FROM audit_events a WHERE a.target_id<>$4),'[]'::jsonb),
 'outbox',COALESCE((SELECT jsonb_agg(to_jsonb(o) ORDER BY o.tenant_id,o.id) FROM outbox_events o),'[]'::jsonb)
)::text`, source.TenantID, source.ID, federation.ArtworkTargetKey(target), excluded).Scan(&value); err != nil {
			t.Fatal("read artwork atomicity state", err)
		}
		return value
	}
	before := state("")
	for _, trial := range []struct {
		name, message  string
		sourceRevision int64
	}{{"stale source revision", "source presentation changed", source.Revision - 1}, {"stale artwork head", "artwork revision does not match", source.Revision}} {
		t.Run(trial.name, func(t *testing.T) {
			// The original sealed transition was valid. Its source or head
			// expectation no longer matches the actual committed SQL state.
			value, err := db.federation.SaveArtworkOverride(ctx, first, 0, trial.sourceRevision, evidence)
			var refusal *catalog.DomainError
			if !errors.As(err, &refusal) || refusal.Code != catalog.CodePreconditionFailed || refusal.Message != trial.message || !reflect.DeepEqual(value, federation.ArtworkOverride{}) || state("") != before {
				t.Fatal("stale transition bypassed a commit fence or changed retained state", value, err)
			}
		})
	}
	newID := func() string {
		t.Helper()
		value, err := (id.Generator{}).New(managerClock.Now())
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	candidateID, retryAuditID := newID(), newID()
	unaffected := state(candidateID)
	ids := &authorizationAuditCollisionIDs{values: []string{candidateID, audits[0].ID}}
	faulted, err := federation.NewManager(db.federation, &postgresFederationFetcher{policyDigest: source.PolicyDigest}, managerClock, ids, federation.WithEvidenceSink(db.federation.repository, false))
	if err != nil {
		t.Fatal(err)
	}
	input.Icon.Mode, input.Reason = federation.ArtworkSuppress, "Hide icon after review"
	value, err := faulted.SetArtworkOverride(ctx, target, input, current.Revision)
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != "23505" || pgerr.TableName != "audit_events" || len(ids.values) != 0 || !reflect.DeepEqual(value, federation.ArtworkOverride{}) || state("") != before {
		t.Fatal("late audit failure did not roll back artwork history, head and evidence", value, err, pgerr)
	}
	// Reuse the artwork ID after rollback; only the colliding audit ID changes.
	ids.values = []string{candidateID, retryAuditID}
	retried, err := faulted.SetArtworkOverride(ctx, target, input, current.Revision)
	if err != nil || retried.ID != candidateID || retried.Revision != current.Revision+1 || len(ids.values) != 0 || state(retried.ID) != unaffected {
		t.Fatal("retry failed or changed source, unrelated history/evidence or outbox", retried, err)
	}
	retained, err := db.federation.GetArtworkOverride(ctx, source.TenantID, target)
	if err != nil || !reflect.DeepEqual(retained, retried) {
		t.Fatal("successful retry did not advance the exact current head", retained, err)
	}
	var auditCount int
	if err = db.operator.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND id=$2 AND target_id=$3 AND action=$4 AND before_digest=$5 AND after_digest=$6`, source.TenantID, retryAuditID, retried.ID, audit.ActionSourceArtworkRevised, current.Digest, retried.Digest).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatal("retry omitted its one bound private audit", auditCount, err)
	}
}

// SQL-role and RLS probes intentionally exclude the authenticated command actor.
//
//nolint:contextcheck // Actor-free SQL probes test role and tenant fences independently.
func qualifyArtworkOverlayPostgres(t *testing.T, manager *federation.Manager, store, worker *PostgresFederationStore, operatorPool, apiPool, workerPool *pgxpool.Pool, ctx context.Context, source federation.Source, otherTenant string) {
	t.Helper()
	rawCtx := t.Context()
	target := federation.ArtworkTarget{SourceID: source.ID, PresentationGeneration: 1, Identifier: "urn:air:remote.example:agents:artwork-overlay", Kind: catalog.KindAgent}
	input := federation.ArtworkOverrideInput{SourceRevision: source.Revision, Icon: federation.ArtworkIconSelection{Mode: federation.ArtworkSuppress}, Screenshots: federation.ArtworkScreenshotSelection{Mode: federation.ArtworkInherit}, Reason: "Hide publisher icon"}
	first, err := manager.SetArtworkOverride(ctx, target, input, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = probeArtworkStoreAsSQLRole(t, store, otherTenant, target); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("cross tenant overlay: %v", err)
	}
	if _, err = manager.SetArtworkOverride(ctx, target, input, 0); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("stale overlay accepted: %v", err)
	}
	input.Icon.Mode = federation.ArtworkInherit
	second, err := manager.SetArtworkOverride(ctx, target, input, 1)
	if err != nil || second.Revision != 2 {
		t.Fatalf("reset overlay: %#v %v", second, err)
	}
	history, err := store.ListArtworkOverrideHistory(ctx, source.TenantID, target, "", 3)
	if err != nil || len(history) != 2 || history[0].Digest != first.Digest {
		t.Fatalf("retained overlay history: %#v %v", history, err)
	}
	otherTarget := target
	otherTarget.Identifier = "urn:air:remote.example:agents:other-artwork-overlay"
	if _, err = manager.SetArtworkOverride(ctx, otherTarget, input, 0); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, tenant, source string
		generation           int64
		limit, want          int
		overflow             bool
	}{
		{"current heads, not revisions", source.TenantID, source.ID, 1, 2, 2, false},
		{"bounded overflow", source.TenantID, source.ID, 1, 1, 1, true},
		{"different authority generation", source.TenantID, source.ID, 2, 2, 0, false},
		{"different tenant", otherTenant, source.ID, 1, 2, 0, false},
		{"different source", source.TenantID, "00000000-0000-7000-8000-000000000999", 1, 2, 0, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			count, overflow, countErr := store.ArtworkOverrideCount(rawCtx, test.tenant, test.source, test.generation, test.limit)
			if countErr != nil || count != test.want || overflow != test.overflow {
				t.Fatalf("artwork count: %d, overflow=%t, error=%v", count, overflow, countErr)
			}
		})
	}
	preview, err := manager.PreviewChange(ctx, source.ID, federation.PatchInput{Intent: federation.ChangeRemove, Reason: "Review artwork impact before source removal"})
	if err != nil || preview.ArtworkOverridesAffected != 2 {
		t.Fatalf("source removal preview artwork impact: %#v %v", preview, err)
	}
	unchanged, err := manager.GetSource(ctx, source.ID)
	if err != nil || unchanged.Revision != source.Revision || unchanged.Status != source.Status {
		t.Fatalf("preview changed source authority: %#v %v", unchanged, err)
	}
	qualifyArtworkSQLGuards(t, operatorPool, first, second, otherTenant)
	batch, err := store.ArtworkReadBatch(ctx, source.TenantID, []federation.ArtworkReadRequest{{Target: target, Live: true}})
	if err != nil || !batch[target].Eligible || batch[target].Override == nil || batch[target].Override.Digest != second.Digest {
		t.Fatalf("live target batch without observation: %#v %v", batch, err)
	}
	workerRead, err := probeArtworkStoreAsSQLRole(t, worker, source.TenantID, target)
	if catalog.ErrorCodeOf(err) != catalog.CodeForbidden || workerRead.ID != "" {
		t.Fatalf("worker read: %#v %v", workerRead, err)
	}
	_, err = probeArtworkWorkerWrite(t, workerPool, source.TenantID)
	requireInsufficientPrivilege(t, err)
	tx, err := apiPool.Begin(rawCtx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackForCleanup(rawCtx, tx)()
	if _, err = tx.Exec(rawCtx, `SELECT set_config('app.tenant_id',$1,true)`, source.TenantID); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(rawCtx, `UPDATE source_artwork_revisions SET document='{}'::jsonb WHERE tenant_id=$1 AND id=$2`, source.TenantID, first.ID)
	requireInsufficientPrivilege(t, err)
	_ = tx.Rollback(rawCtx)
	tx, err = apiPool.Begin(rawCtx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackForCleanup(rawCtx, tx)()
	if _, err = tx.Exec(rawCtx, `SELECT set_config('app.tenant_id',$1,true)`, otherTenant); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = tx.QueryRow(rawCtx, `SELECT count(*) FROM source_artwork_revisions WHERE tenant_id=$1`, source.TenantID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("RLS disclosed artwork: %d %v", count, err)
	}
	_ = tx.Rollback(rawCtx)
	// Reapply the new DDL to the actual former constraint with retained rows.
	// Roll back this owner-only probe, leaving the normal migration ledger intact.
	upgrade, err := operatorPool.Begin(rawCtx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackForCleanup(rawCtx, upgrade)()
	if _, err = upgrade.Exec(rawCtx, `ALTER TABLE source_artwork_revisions DROP CONSTRAINT source_artwork_revisions_check, ADD CONSTRAINT source_artwork_revisions_check CHECK(octet_length(version)<=128 AND (version_present OR version=''))`); err != nil {
		t.Fatal(err)
	}
	var schema string
	if err = upgrade.QueryRow(rawCtx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if err = verifyRestoreConstraints(rawCtx, upgrade, schema); err == nil || !strings.Contains(err.Error(), "observed digest 9c1cf7ceaef3a781e39cf1ee8039869bd2dcbb6fa794a8818f4fc919648c7140") {
		t.Fatalf("former artwork constraint did not reproduce its qualified current-schema inventory: %v", err)
	}
	migration, err := migrations.Files.ReadFile("000040_unicode_version_limits.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = upgrade.Exec(rawCtx, string(migration)); err != nil {
		t.Fatalf("upgrade retained artwork constraint: %v", err)
	}
	if err = verifyRestoreConstraints(rawCtx, upgrade, schema); err != nil {
		t.Fatalf("upgraded constraint inventory differs beyond the reviewed change: %v", err)
	}
	var originalDigest string
	if err = upgrade.QueryRow(rawCtx, `SELECT digest FROM source_artwork_revisions WHERE tenant_id=$1 AND id=$2`, source.TenantID, first.ID).Scan(&originalDigest); err != nil || originalDigest != first.Digest {
		t.Fatalf("upgrade changed immutable artwork: %v", err)
	}
	_ = upgrade.Rollback(rawCtx)
	// Migration 40 permits 128 Unicode code points in the retained identity.
	// Existing ASCII history and the exact target lookup must remain intact.
	unicodeTarget := target
	unicodeTarget.VersionPresent = true
	unicodeTarget.Version = strings.Repeat("🙂", 128)
	unicodeOverride, err := manager.SetArtworkOverride(ctx, unicodeTarget, input, 0)
	if err != nil || unicodeOverride.Target != unicodeTarget {
		t.Fatalf("Unicode artwork persistence: %#v %v", unicodeOverride, err)
	}
	retained, err := store.GetArtworkOverride(ctx, source.TenantID, unicodeTarget)
	if err != nil || retained.Digest != unicodeOverride.Digest || retained.Target != unicodeTarget {
		t.Fatalf("Unicode artwork exact read: %#v %v", retained, err)
	}
	for _, invalid := range []struct {
		version string
		present bool
	}{{unicodeTarget.Version + "x", true}, {"x", false}} {
		_, err = operatorPool.Exec(rawCtx, `INSERT INTO source_artwork_revisions(tenant_id,id,source_id,presentation_generation,target_key,identifier,version_present,version,kind,revision,document,digest,created_by,created_at)
SELECT tenant_id,$3::uuid,source_id,presentation_generation,target_key||'-invalid',identifier,$5,$4,kind,revision,
 jsonb_set(jsonb_set(jsonb_set(document,'{id}',to_jsonb($3::text)),'{target,version}',to_jsonb($4::text)),'{target,versionPresent}',to_jsonb($5::boolean)),digest,created_by,created_at
 FROM source_artwork_revisions WHERE tenant_id=$1 AND id=$2`, source.TenantID, unicodeOverride.ID, "00000000-0000-7000-8000-000000000998", invalid.version, invalid.present)
		var pgerr *pgconn.PgError
		if !errors.As(err, &pgerr) || pgerr.Code != "23514" || pgerr.ConstraintName != "source_artwork_revisions_check" {
			t.Fatalf("SQL version/presence guard failed: %v", err)
		}
	}
	unicodeTarget.Version += "x"
	if _, err = manager.SetArtworkOverride(ctx, unicodeTarget, input, 0); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("oversized artwork version accepted: %v", err)
	}
	if err = operatorPool.QueryRow(rawCtx, `SELECT count(*) FROM source_artwork_revisions WHERE tenant_id=$1 AND source_id=$2`, source.TenantID, source.ID).Scan(&count); err != nil || count != 4 {
		t.Fatalf("Unicode admission or rejection changed history: %d %v", count, err)
	}
	history, err = store.ListArtworkOverrideHistory(ctx, source.TenantID, target, "", 3)
	if err != nil || len(history) != 2 || history[0].Digest != first.Digest || history[1].Digest != second.Digest {
		t.Fatalf("existing artwork history changed: %#v %v", history, err)
	}
}

// Use schema-owner authority to reach the database guards. API-role denial
// alone would not prove immutable history or exact head-to-revision binding.
func qualifyArtworkSQLGuards(t *testing.T, owner *pgxpool.Pool, first, current federation.ArtworkOverride, otherTenant string) {
	t.Helper()
	ctx := t.Context()
	var headID string
	var revision int64
	if err := owner.QueryRow(ctx, `SELECT current_revision_id::text,revision FROM source_artwork_heads WHERE tenant_id=$1 AND source_id=$2 AND target_key=$3`, current.TenantID, current.Target.SourceID, federation.ArtworkTargetKey(current.Target)).Scan(&headID, &revision); err != nil || headID != current.ID || revision != 2 {
		t.Fatalf("invalid retained head control: %s %d, %v", headID, revision, err)
	}
	for _, tc := range []struct {
		name, statement, message string
		arguments                []any
	}{
		{"history update", `UPDATE source_artwork_revisions SET document=document WHERE tenant_id=$1 AND id=$2`, "federation source history is immutable", []any{first.TenantID, first.ID}},
		{"history deletion", `DELETE FROM source_artwork_revisions WHERE tenant_id=$1 AND id=$2`, "federation source history is immutable", []any{first.TenantID, first.ID}},
		{"head deletion", `DELETE FROM source_artwork_heads WHERE tenant_id=$1 AND current_revision_id=$2`, "artwork history cannot be deleted", []any{current.TenantID, current.ID}},
		{"skipped head revision", `UPDATE source_artwork_heads SET revision=revision+2 WHERE tenant_id=$1 AND current_revision_id=$2`, "invalid artwork head transition", []any{current.TenantID, current.ID}},
		{"changed head generation", `UPDATE source_artwork_heads SET revision=revision+1,presentation_generation=presentation_generation+1 WHERE tenant_id=$1 AND current_revision_id=$2`, "invalid artwork head transition", []any{current.TenantID, current.ID}},
		{"wrong retained revision", `UPDATE source_artwork_heads SET revision=revision+1,current_revision_id=$3 WHERE tenant_id=$1 AND current_revision_id=$2`, "artwork revision binding mismatch", []any{current.TenantID, current.ID, first.ID}},
		{"cross-tenant revision binding", `INSERT INTO source_artwork_heads(tenant_id,source_id,presentation_generation,target_key,revision,current_revision_id) SELECT $1,source_id,presentation_generation,target_key,1,id FROM source_artwork_revisions WHERE tenant_id=$2 AND id=$3`, "artwork revision binding mismatch", []any{otherTenant, first.TenantID, first.ID}},
		{"cross-target revision binding", `INSERT INTO source_artwork_heads(tenant_id,source_id,presentation_generation,target_key,revision,current_revision_id) SELECT tenant_id,source_id,presentation_generation,target_key||'-different',1,id FROM source_artwork_revisions WHERE tenant_id=$1 AND id=$2`, "artwork revision binding mismatch", []any{first.TenantID, first.ID}},
		{"noninitial head revision", `INSERT INTO source_artwork_heads(tenant_id,source_id,presentation_generation,target_key,revision,current_revision_id) SELECT tenant_id,source_id,presentation_generation,target_key||'-different',2,id FROM source_artwork_revisions WHERE tenant_id=$1 AND id=$2`, "invalid initial artwork revision", []any{current.TenantID, current.ID}},
		{"invalid source generation", `UPDATE federation_sources SET configuration=jsonb_set(configuration,'{presentationGeneration}','0'::jsonb),revision=revision+1,updated_at=updated_at+interval '1 second' WHERE tenant_id=$1 AND id=$2`, "invalid source presentation generation", []any{current.TenantID, current.Target.SourceID}},
		{"unearned source generation", `UPDATE federation_sources SET configuration=jsonb_set(configuration,'{presentationGeneration}','2'::jsonb),revision=revision+1,updated_at=updated_at+interval '1 second' WHERE tenant_id=$1 AND id=$2`, "invalid source presentation generation transition", []any{current.TenantID, current.Target.SourceID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := owner.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackForCleanup(ctx, tx)()
			_, err = tx.Exec(ctx, tc.statement, tc.arguments...)
			var pgError *pgconn.PgError
			if !errors.As(err, &pgError) || pgError.Code != "P0001" || pgError.Message != tc.message {
				t.Fatalf("intended database guard did not refuse the write: %v", err)
			}
		})
	}
	var unchanged bool
	var retained, audits, notifications int
	if err := owner.QueryRow(ctx, `SELECT
 (SELECT current_revision_id=$2::uuid AND revision=2 FROM source_artwork_heads WHERE tenant_id=$1 AND current_revision_id=$2::uuid),
 (SELECT count(*) FROM source_artwork_revisions WHERE tenant_id=$1 AND id IN ($2::uuid,$3::uuid)),
 (SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action=$4 AND target_id IN ($2::text,$3::text)),
 (SELECT count(*) FROM outbox_events WHERE tenant_id=$1 AND subject IN ($2::text,$3::text))`, current.TenantID, current.ID, first.ID, audit.ActionSourceArtworkRevised).Scan(&unchanged, &retained, &audits, &notifications); err != nil || !unchanged || retained != 2 || audits != 2 || notifications != 0 {
		t.Fatalf("guard probes changed retained state or audit-only evidence: unchanged=%v retained=%d audits=%d notifications=%d, %v", unchanged, retained, audits, notifications, err)
	}
}

type postgresArtworkFetcher struct{}

func (postgresArtworkFetcher) Fetch(context.Context, artwork.FetchRequest) (artwork.FetchResult, error) {
	var body bytes.Buffer
	err := png.Encode(&body, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	return artwork.FetchResult{Body: body.Bytes(), MediaType: "image/png"}, err
}
func postgresArtworkService(t *testing.T) *artwork.Service {
	t.Helper()
	service, err := artwork.New(postgresArtworkFetcher{}, artwork.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return service
}
func postgresArtworkInput() map[string]any {
	return map[string]any{artwork.IRI: artwork.Input{SchemaVersion: 1, Icon: &artwork.AssetInput{URL: "https://images.example/fixture.png", Alt: "Fixture artwork", Rights: &artwork.Rights{Basis: "owned", Attribution: "Synthetic fixture"}}}, "unrelated": map[string]any{"largePrivateValue": "not a publication extension"}}
}

// These SQL-role probes intentionally carry no application actor. Their tenant
// context tests RLS separately from the authenticated-tenant admission fence.
func probeArtworkStoreAsSQLRole(t *testing.T, store *PostgresFederationStore, tenant string, target federation.ArtworkTarget) (federation.ArtworkOverride, error) {
	t.Helper()
	return store.GetArtworkOverride(t.Context(), tenant, target)
}
func probeArtworkWorkerWrite(t *testing.T, pool *pgxpool.Pool, tenant string) (pgconn.CommandTag, error) {
	t.Helper()
	return pool.Exec(t.Context(), `UPDATE source_artwork_heads SET revision=revision+1 WHERE tenant_id=$1`, tenant)
}
