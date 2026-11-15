//go:build integration

package database

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/idempotency"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

// Match the configured-host coordinator's sequential production calls, with
// real exact-role stores. The database supplies the later-host write failure.
type previewPublicationStores struct{ managers []*publication.Manager }

func (*previewPublicationStores) WithdrawPublicationDomain(context.Context, string, string, string, string, string) ([]string, error) {
	panic("unexpected domain withdrawal")
}
func (p *previewPublicationStores) WithdrawPublicationRelease(ctx context.Context, tenant, release, action, target, cause string) ([]string, error) {
	for _, manager := range p.managers {
		if _, err := manager.WithdrawRelease(ctx, tenant, release, publication.WithdrawalCause{Cause: cause, OriginatingAction: action, OriginatingTargetID: target}); err != nil {
			return nil, err
		}
	}
	return []string{}, nil
}
func (p *previewPublicationStores) PreviewPublicationRelease(ctx context.Context, tenant, release string) ([]catalog.ReleaseWithdrawalSnapshot, error) {
	budget := &publication.WithdrawalPreviewBudget{}
	rows := []catalog.ReleaseWithdrawalSnapshot{}
	for _, manager := range p.managers {
		row, matched, err := manager.PreviewReleaseWithdrawal(ctx, tenant, release, budget)
		if err != nil {
			return nil, err
		}
		if matched {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func TestPostgresWithdrawalPreviewAndLaterHostFailureKeepCommandAtomic(t *testing.T) {
	f := newMCPToolFixture(t)
	record := f.seed(t, 0, "Withdrawal preview fixture", "", false)
	port := &previewPublicationStores{}
	key := sha256.Sum256([]byte(t.Name()))
	service, err := catalog.NewService(catalog.Options{Repository: f.repo, Clock: clock.Real{}, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: key[:], Policy: "mcp-tools-test", PublicationWithdrawer: port})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := NewPostgresIdentityStore(f.repo)
	if err != nil {
		t.Fatal(err)
	}
	ctx := identity.WithActor(t.Context(), identity.BindPermissionStore(f.authorActor, policy))
	stores := make([]*PostgresPublicationStore, 0, 2)
	snapshots := make([]publication.Snapshot, 0, 2)
	for _, host := range []string{"a.preview.example", "z.preview.example"} {
		store, err := NewPostgresPublicationStore(f.repo, host)
		if err != nil {
			t.Fatal(err)
		}
		manager, err := publication.NewManager(publication.Options{Records: service, Store: store, Clock: clock.Real{}, IDs: id.Generator{}, BaseURL: "https://" + host, Host: host, PublisherDomain: "catalog.example.com", ARDProfile: "ard-test", AICatalogProfile: "ai-test", CursorKey: key[:], Audience: publication.AudienceAuthenticated})
		if err != nil {
			t.Fatal(err)
		}
		candidate, err := manager.BuildCandidate(ctx, record.Resource.TenantID)
		if err != nil {
			t.Fatal(err)
		}
		active, err := manager.Activate(ctx, record.Resource.TenantID, candidate.ID, "Owned withdrawal preview baseline")
		if err != nil {
			t.Fatal(err)
		}
		stores = append(stores, store)
		snapshots = append(snapshots, active)
		port.managers = append(port.managers, manager)
	}
	counts := func() (int, int) {
		t.Helper()
		var audits, outbox int
		if err := f.db.operator.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM audit_events WHERE tenant_id=$1),(SELECT count(*) FROM outbox_events WHERE tenant_id=$1)`, record.Resource.TenantID).Scan(&audits, &outbox); err != nil {
			t.Fatal(err)
		}
		return audits, outbox
	}
	beforeAudit, beforeOutbox := counts()
	preview, err := service.PreviewReleaseWithdrawal(ctx, record.Release.ID, catalog.ReleaseRetired)
	if err != nil || len(preview.Snapshots) != 2 || preview.ReleaseRevision != record.Release.Revision {
		t.Fatal(preview, err)
	}
	if bounded, _, err := stores[0].ActiveForWithdrawalPreview(ctx, record.Resource.TenantID, 1); catalog.ErrorCodeOf(err) != catalog.CodeWorkLimitExceeded || bounded.ID != "" {
		t.Fatal("SQL did not refuse document transfer above the remaining byte budget", bounded, err)
	}
	for i, row := range preview.Snapshots {
		if row.SnapshotID != snapshots[i].ID || row.Host != snapshots[i].Host || row.Audience != "authenticated" || row.ETag != snapshots[i].ETag {
			t.Fatal("wrong exact-role preview reference", row)
		}
	}
	foreign := semanticDisclosureActor(t, f.db, housekeepingTenantB)
	if _, err := service.PreviewReleaseWithdrawal(foreign, record.Release.ID, catalog.ReleaseRetired); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatal("foreign release existence disclosed", err)
	}
	previewAudit, previewOutbox := counts()
	if previewAudit != beforeAudit || previewOutbox != beforeOutbox {
		t.Fatal("read-only preview produced transactional evidence", beforeAudit, previewAudit, beforeOutbox, previewOutbox)
	}
	// Only this fixture schema is modified. No lifecycle guard is disabled.
	if _, err = f.db.operator.Exec(t.Context(), `CREATE FUNCTION fail_later_preview_withdrawal() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF OLD.host='z.preview.example' AND NEW.state='retired' THEN RAISE EXCEPTION 'owned second-host withdrawal failure'; END IF; RETURN NEW; END $$;
CREATE TRIGGER fail_later_preview_withdrawal BEFORE UPDATE ON publication_snapshots FOR EACH ROW EXECUTE FUNCTION fail_later_preview_withdrawal()`); err != nil {
		t.Fatal(err)
	}
	commands, err := NewPostgresIdempotencyStore(f.repo)
	if err != nil {
		t.Fatal(err)
	}
	scope := idempotency.Scope{TenantID: record.Resource.TenantID, Actor: f.authorActor.Subject, Route: "POST /api/v1/listing-releases/{releaseId}:retire", Key: "withdrawal-preview-later-failure", AuthorizationDigest: fmt.Sprintf("sha256:%x", key)}
	expiry := time.Now().UTC().Add(time.Hour)
	var transitionErr error
	execution, err := commands.Execute(ctx, scope, "withdrawal-preview-failure", expiry, func(commandContext context.Context) idempotency.Response {
		_, transitionErr = service.TransitionRelease(commandContext, record.Release.ID, catalog.ReleaseRetired, "Owned failed retirement", "", nil, record.Release.Revision)
		if transitionErr != nil {
			idempotency.AbortCommand(commandContext, transitionErr)
		}
		return idempotency.Response{Digest: "withdrawal-preview-failure", Status: 500, Body: []byte(`{"code":"INTERNAL"}`)}
	})
	if err != nil || transitionErr == nil || execution.Response.Status != 500 {
		t.Fatal("later-host failure control", execution, transitionErr, err)
	}
	var injectedFailure *pgconn.PgError
	if !errors.As(transitionErr, &injectedFailure) || injectedFailure.Code != "P0001" || injectedFailure.Message != "owned second-host withdrawal failure" {
		t.Fatal("failure did not reach the injected second-host write", transitionErr)
	}
	current, err := service.GetRelease(ctx, record.Release.ID)
	if err != nil || current.State != record.Release.State || current.Revision != record.Release.Revision {
		t.Fatal("failed command retained release change", current, err)
	}
	for i, store := range stores {
		active, err := store.Active(ctx, record.Resource.TenantID)
		if err != nil || active.ID != snapshots[i].ID {
			t.Fatal("earlier host did not roll back", active, err)
		}
	}
	afterAudit, afterOutbox := counts()
	if afterAudit != beforeAudit || afterOutbox != beforeOutbox {
		t.Fatal("failed command retained evidence", beforeAudit, afterAudit, beforeOutbox, afterOutbox)
	}
	replay, err := commands.Execute(ctx, scope, "withdrawal-preview-failure", expiry, func(context.Context) idempotency.Response {
		t.Fatal("failed exact key executed again")
		return idempotency.Response{}
	})
	if err != nil || !replay.Replayed || replay.Response.Status != 500 {
		t.Fatal("failure replay", replay, err)
	}
	if _, err = f.db.operator.Exec(t.Context(), `DROP TRIGGER fail_later_preview_withdrawal ON publication_snapshots; DROP FUNCTION fail_later_preview_withdrawal()`); err != nil {
		t.Fatal(err)
	}
	// State reads and a new advisory observation remain separate from replay.
	refreshed, err := service.PreviewReleaseWithdrawal(ctx, record.Release.ID, catalog.ReleaseRetired)
	if err != nil || len(refreshed.Snapshots) != 2 {
		t.Fatal(refreshed, err)
	}
	scope.Key = "withdrawal-preview-success"
	success, err := commands.Execute(ctx, scope, "withdrawal-preview-success", expiry, func(commandContext context.Context) idempotency.Response {
		_, transitionErr = service.TransitionRelease(commandContext, record.Release.ID, catalog.ReleaseRetired, "Owned successful retirement", "", nil, record.Release.Revision)
		if transitionErr != nil {
			idempotency.AbortCommand(commandContext, transitionErr)
		}
		return idempotency.Response{Digest: "withdrawal-preview-success", Status: 200, Body: []byte(`{"state":"retired"}`)}
	})
	if err != nil || transitionErr != nil || success.Response.Status != 200 {
		t.Fatal(success, transitionErr, err)
	}
	retiredAudit, retiredOutbox := counts()
	empty, err := service.PreviewReleaseWithdrawal(ctx, record.Release.ID, catalog.ReleaseRevoked)
	if err != nil || empty.Snapshots == nil || len(empty.Snapshots) != 0 || empty.ReleaseRevision != record.Release.Revision+1 {
		t.Fatal("post-withdrawal SQL preview did not establish complete observed absence", empty, err)
	}
	emptyAudit, emptyOutbox := counts()
	if emptyAudit != retiredAudit || emptyOutbox != retiredOutbox {
		t.Fatal("empty preview produced transactional evidence", retiredAudit, emptyAudit, retiredOutbox, emptyOutbox)
	}
	for _, store := range stores {
		if _, err := store.Active(ctx, record.Resource.TenantID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
			t.Fatal("previewed snapshot remained active", err)
		}
	}
}
