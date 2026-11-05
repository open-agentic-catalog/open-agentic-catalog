package database

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
)

func federationAdapterFixture(t *testing.T) (*PostgresFederationStore, *identityScriptedTx, context.Context, federation.Source) {
	t.Helper()
	pool := &pgxpool.Pool{}
	repository, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresFederationStore(repository)
	if err != nil {
		t.Fatal(err)
	}
	tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}}
	at := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	source := federation.Source{ID: "00000000-0000-7000-8000-000000000010", TenantID: restoreTestTenant, DisplayName: "Partner", Type: federation.SourceARDManifest, Endpoint: "https://partner.example.com/catalog.json", Status: federation.SourceActive, Revision: 1, RetrievalMode: federation.RetrievalIndexed, UpdateMode: federation.UpdateManual, CreatedBy: "admin", CreatedAt: at, UpdatedAt: at, PolicyID: "public", PolicyRevision: 1, PolicyDigest: "sha256:" + strings.Repeat("a", 64), InspectionID: "00000000-0000-7000-8000-000000000020"}
	return store, tx, identityRepositoryContext(pool, tx), source
}

// federationSourceRow uses the shared current-projection values for exact reads.
func federationSourceRow(t *testing.T, source federation.Source) pgx.Row {
	t.Helper()
	return scriptedValueRow(federationSourceValues(t, source))
}

// federationSourceValues keeps current revision, audience and referral evidence
// aligned for both exact reads and administrative pages.
func federationSourceValues(t *testing.T, source federation.Source) []any {
	t.Helper()
	raw, err := marshalFederationSourceConfiguration(source)
	if err != nil {
		t.Fatal(err)
	}
	state, err := json.Marshal(map[string]string{"lastError": source.LastError})
	if err != nil {
		t.Fatal(err)
	}
	assignments, err := json.Marshal(source.DiscoveryAssignments)
	if err != nil {
		t.Fatal(err)
	}
	referrals, err := json.Marshal(source.Referrals.ApprovedSourceIDs)
	if err != nil {
		t.Fatal(err)
	}
	return []any{raw, state, source.Status, source.Revision, source.LastCompleteSnapshotID, source.LastSyncAt, source.UpdatedAt, true, string(source.RetrievalMode), source.PolicyID, source.PolicyRevision, source.PolicyDigest, source.CredentialRevision, assignments, referrals}
}

// These cases check adapter sequencing and bindings with a command-owned fake
// transaction. Database constraints, row locking and SQL execution are separate.
func TestFederationSourceAdapterWrites(t *testing.T) {
	t.Parallel()
	if _, err := NewPostgresFederationStore(nil); err == nil {
		t.Fatal("nil repository accepted")
	}
	for _, operation := range []string{"create", "update", "remove"} {
		for _, mode := range []string{"success", "inspection expired", "inspection absent", "inspection read error", "source write error", "revision write error", "evidence mismatch", "retirement error"} {
			if mode == "retirement error" && operation != "remove" {
				continue
			}
			t.Run(operation+"/"+mode, func(t *testing.T) {
				store, tx, ctx, source := federationAdapterFixture(t)
				current := source
				current.LastCompleteSnapshotID = "retained-snapshot"
				current.LastSyncAt = &current.CreatedAt
				current.LastError = "retained diagnostic"
				source.LastCompleteSnapshotID = "caller-snapshot"
				source.LastSyncAt = &source.CreatedAt
				source.LastError = "caller diagnostic"
				if operation != "create" {
					source.Revision = 2
					tx.queryRows = append(tx.queryRows, federationSourceRow(t, current))
				}
				if operation == "remove" {
					source.Status = federation.SourceRemoved
				}
				sentinel := errors.New("source adapter driver failure")
				inspection := scriptedValueRow([]any{true})
				wantWrites := 2
				if operation == "remove" {
					wantWrites++
				}
				var evidence []federation.MutationEvidence
				switch mode {
				case "inspection expired":
					inspection, wantWrites = scriptedValueRow([]any{false}), 0
				case "inspection absent":
					inspection, wantWrites = scannerFunc(func(...any) error { return pgx.ErrNoRows }), 0
				case "inspection read error":
					inspection, wantWrites = scannerFunc(func(...any) error { return sentinel }), 0
				case "source write error":
					tx.execErrors, wantWrites = []error{sentinel}, 1
				case "revision write error":
					tx.execErrors, wantWrites = []error{nil, sentinel}, 2
				case "retirement error":
					tx.execErrors = []error{nil, nil, sentinel}
				case "evidence mismatch":
					evidence = []federation.MutationEvidence{{Audit: audit.Record{ID: "unbound", TenantID: "other-tenant"}}}
				}
				tx.queryRows = append(tx.queryRows, inspection)
				var got federation.Source
				var err error
				if operation == "create" {
					got, err = store.CreateSource(ctx, source, evidence...)
				} else {
					got, err = store.UpdateSource(ctx, source, 1, evidence...)
				}
				if mode == "success" {
					if err != nil || got.ID != source.ID || got.Revision != source.Revision || got.Status != source.Status {
						t.Fatalf("source=%+v err=%v", got, err)
					}
					if operation == "create" {
						if got.LastCompleteSnapshotID != "" || got.LastSyncAt != nil || got.LastError != "" {
							t.Fatal("create retained caller synchronization state")
						}
					} else if got.LastCompleteSnapshotID != current.LastCompleteSnapshotID || got.LastSyncAt == nil || !got.LastSyncAt.Equal(*current.LastSyncAt) || got.LastError != current.LastError {
						t.Fatal("update replaced authoritative synchronization state")
					}
				} else if err == nil || got.ID != "" || tx.rollbacks != 1 {
					t.Fatalf("failed command disclosed result or retained transaction: %+v, %v rollback=%d", got, err, tx.rollbacks)
				}
				if strings.HasSuffix(mode, "error") && !errors.Is(err, sentinel) {
					t.Fatalf("driver cause lost: %v", err)
				}
				if (mode == "inspection expired" || mode == "inspection absent") && catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
					t.Fatalf("inspection failure: %v", err)
				}
				if len(tx.statements) != wantWrites || len(tx.queryRows) != 0 || tx.commits != 0 {
					t.Fatalf("writes=%d want=%d unread=%d commits=%d", len(tx.statements), wantWrites, len(tx.queryRows), tx.commits)
				}
				lastRead := tx.queryRowArguments[len(tx.queryRowArguments)-1]
				if !reflect.DeepEqual(lastRead, []any{source.TenantID, source.InspectionID}) {
					t.Fatalf("inspection binding=%v", lastRead)
				}
				if wantWrites >= 2 && (tx.arguments[1][0] != source.TenantID || tx.arguments[1][1] != source.ID || tx.arguments[1][2] != source.Revision) {
					t.Fatalf("revision binding=%v", tx.arguments[1])
				}
			})
		}
	}
}

func TestFederationSourceUpdatePreparationGuards(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"read error", "stale revision", "nonconsecutive revision", "observation read error", "observation overflow", "changed observations", "unchanged observations"} {
		t.Run(mode, func(t *testing.T) {
			store, tx, ctx, current := federationAdapterFixture(t)
			current.InspectionID = ""
			tx.queryRows = []pgx.Row{federationSourceRow(t, current)}
			proposed := current
			proposed.Revision = 2
			sentinel := errors.New("source guard driver failure")
			want := catalog.CodePreconditionFailed
			var evidence []federation.MutationEvidence
			switch mode {
			case "read error":
				tx.queryRows[0] = scannerFunc(func(...any) error { return sentinel })
				want = catalog.CodeDependencyUnavailable
			case "stale revision":
				current.Revision = 3
				tx.queryRows[0] = federationSourceRow(t, current)
			case "nonconsecutive revision":
				proposed.Revision = 3
			default:
				expected := federation.ObservationSet{Count: 2, Digest: "sha256:" + strings.Repeat("c", 64)}
				evidence = []federation.MutationEvidence{{ExpectedObservationSet: &expected}}
				actual := expected
				if mode == "changed observations" {
					actual.Digest = "sha256:" + strings.Repeat("d", 64)
				}
				row := scriptedValueRow([]any{actual.Count, mode == "observation overflow", actual.Digest})
				if mode == "observation read error" {
					row = scannerFunc(func(...any) error { return sentinel })
					want = catalog.CodeDependencyUnavailable
				} else if mode == "observation overflow" {
					want = catalog.CodeWorkLimitExceeded
				}
				tx.queryRows = append(tx.queryRows, row)
			}
			got, err := store.UpdateSource(ctx, proposed, 1, evidence...)
			if mode == "unchanged observations" {
				if err != nil || got.Revision != 2 || len(tx.statements) != 2 {
					t.Fatalf("unchanged observations: %+v, %v writes=%d", got, err, len(tx.statements))
				}
				return
			}
			if catalog.ErrorCodeOf(err) != want || got.ID != "" || len(tx.statements) != 0 {
				t.Fatalf("source guard=%+v, %v writes=%d", got, err, len(tx.statements))
			}
			// These two preconditions deliberately leave the ambient transaction
			// usable so the caller can retain safe source-test attempt evidence.
			if (mode == "stale revision" || mode == "nonconsecutive revision") && tx.rollbacks != 0 {
				t.Fatal("no-write revision guard poisoned the command transaction")
			}
		})
	}
}

// TestFederationSourceRevisionParticipation checks revision defaults and bound
// audience/referral inserts, stopping at the first failed participation write.
func TestFederationSourceRevisionParticipation(t *testing.T) {
	for _, mode := range []string{"registry default", "hybrid default", "indexed default", "audience failure", "referral failure"} {
		t.Run(mode, func(t *testing.T) {
			_, tx, ctx, source := federationAdapterFixture(t)
			source.RetrievalMode = ""
			source.UpdateMode = ""
			source.UpdatedAt = time.Time{}
			wantMode := federation.RetrievalIndexed
			if mode == "registry default" {
				source.Type = federation.SourceRegistry
				source.RegistrySearchURL = "https://partner.example.com/ard/v1/search"
				wantMode = federation.RetrievalLive
			}
			if mode == "hybrid default" {
				source.Live.Enabled = true
				wantMode = federation.RetrievalHybrid
			}
			source.DiscoveryAssignments = []federation.DiscoveryAssignment{{Host: "catalog.example.com", Audience: federation.AudienceAuthenticated}}
			target := "00000000-0000-7000-8000-000000000040"
			source.Referrals.Enabled = true
			source.Referrals.ApprovedSourceIDs = []string{target}
			raw, err := marshalFederationSourceConfiguration(source)
			if err != nil {
				t.Fatal(err)
			}
			failure := errors.New("participation write failure")
			writes := 3
			if mode == "audience failure" {
				tx.execErrors = []error{nil, failure}
				writes = 2
			}
			if mode == "referral failure" {
				tx.execErrors = []error{nil, nil, failure}
			}
			err = saveFederationSourceRevision(ctx, tx, source, raw)
			if strings.HasSuffix(mode, "failure") {
				if !errors.Is(err, failure) {
					t.Fatalf("write error=%v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if len(tx.statements) != writes || !reflect.DeepEqual(tx.arguments[0], []any{source.TenantID, source.ID, source.Revision, wantMode, federation.UpdateManual, source.RegistryListURL, source.PolicyID, source.PolicyRevision, source.PolicyDigest, source.CredentialRevision, raw, source.CreatedAt, source.InspectionID}) {
				t.Fatalf("revision args=%v", tx.arguments)
			}
			if !reflect.DeepEqual(tx.arguments[1], []any{source.TenantID, source.ID, source.Revision, "catalog.example.com", federation.AudienceAuthenticated}) {
				t.Fatal("audience binding lost")
			}
			if writes == 3 && !reflect.DeepEqual(tx.arguments[2], []any{source.TenantID, source.ID, source.Revision, target}) {
				t.Fatal("referral binding lost")
			}
			if tx.commits != 0 || tx.rollbacks != 0 {
				t.Fatal("helper took transaction ownership")
			}
		})
	}
}
