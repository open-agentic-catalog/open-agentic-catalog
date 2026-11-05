package database

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
)

func TestFederationStoredDocumentReads(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"inspection", "preview", "snapshot"} {
		for _, mode := range []string{"success", "missing", "driver error", "invalid JSON", "wrong JSON shape"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				store, tx, ctx, source := federationAdapterFixture(t)
				var expected any
				var read func(context.Context, string, string) (any, error)
				switch kind {
				case "inspection":
					expected = federation.Inspection{ID: source.InspectionID, TenantID: source.TenantID, ResolvedURL: source.Endpoint, CreatedAt: source.CreatedAt, ExpiresAt: source.CreatedAt.Add(time.Hour)}
					read = func(ctx context.Context, tenant, key string) (any, error) {
						return store.GetInspection(ctx, tenant, key)
					}
				case "preview":
					expected = federation.ChangePreview{ID: source.InspectionID, TenantID: source.TenantID, SourceID: source.ID, SourceRevision: 1, Digest: source.PolicyDigest, CreatedAt: source.CreatedAt, ExpiresAt: source.CreatedAt.Add(time.Hour)}
					read = func(ctx context.Context, tenant, key string) (any, error) {
						return store.GetChangePreview(ctx, tenant, key)
					}
				case "snapshot":
					expected = federation.Snapshot{ID: source.InspectionID, TenantID: source.TenantID, SourceID: source.ID}
					read = func(ctx context.Context, tenant, key string) (any, error) { return store.GetSnapshot(ctx, tenant, key) }
				}
				raw, err := json.Marshal(expected)
				if err != nil {
					t.Fatal(err)
				}
				sentinel := errors.New("read document driver error")
				want := catalog.CodeInternal
				switch mode {
				case "invalid JSON":
					raw = []byte(`{`)
				case "wrong JSON shape":
					raw = []byte(`[]`)
				}
				tx.queryRows = []pgx.Row{scriptedValueRow([]any{raw})}
				if mode == "missing" {
					tx.queryRows[0] = scannerFunc(func(...any) error { return pgx.ErrNoRows })
					want = catalog.CodeNotFound
				} else if mode == "driver error" {
					tx.queryRows[0] = scannerFunc(func(...any) error { return sentinel })
					want = catalog.CodeDependencyUnavailable
				}
				key := source.InspectionID
				if kind == "preview" {
					key = source.PolicyDigest
				}
				got, err := read(ctx, source.TenantID, key)
				if mode == "success" {
					if err != nil || !reflect.DeepEqual(got, expected) {
						t.Fatalf("document=%+v want=%+v err=%v", got, expected, err)
					}
				} else if catalog.ErrorCodeOf(err) != want || !reflect.ValueOf(got).IsZero() {
					t.Fatalf("invalid document disclosed: %+v, %v", got, err)
				}
				if len(tx.queryRowArguments) != 1 || !reflect.DeepEqual(tx.queryRowArguments[0], []any{source.TenantID, key}) || len(tx.statements) != 0 {
					t.Fatalf("read binding=%v writes=%d", tx.queryRowArguments, len(tx.statements))
				}
			})
		}
	}
}

func TestFederationPreparedDocumentWritesRetainAudit(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"inspection", "preview"} {
		for _, mode := range []string{"success", "document write failure", "audit write failure", "evidence mismatch"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				store, tx, ctx, source := federationAdapterFixture(t)
				at, expires, digest := source.CreatedAt, source.CreatedAt.Add(time.Hour), source.PolicyDigest
				var expected any
				var write func(federation.MutationEvidence) (any, error)
				action, targetType := audit.ActionSourceInspected, "source_inspection"
				facts := map[string]any{"outcome": "succeeded", "originClass": "public", "policyDigest": digest, "configurationDigest": digest, "inspectionDigest": digest, "expiresAt": expires.Format(time.RFC3339Nano)}
				if kind == "inspection" {
					value := federation.Inspection{ID: source.InspectionID, TenantID: source.TenantID, ResolvedURL: source.Endpoint, CreatedAt: at, ExpiresAt: expires}
					expected = value
					write = func(e federation.MutationEvidence) (any, error) { return store.SaveInspection(ctx, value, e) }
				} else {
					action, targetType = audit.ActionSourceChangePreviewCreated, "source_change_preview"
					value := federation.ChangePreview{ID: source.InspectionID, TenantID: source.TenantID, SourceID: source.ID, SourceRevision: 1, Intent: "remove", Digest: digest, ConfigurationDigest: digest, ObservationSetDigest: digest, CreatedAt: at, ExpiresAt: expires}
					expected = value
					write = func(e federation.MutationEvidence) (any, error) { return store.SaveChangePreview(ctx, value, e) }
					facts = map[string]any{"sourceId": source.ID, "sourceRevision": 1, "intent": "remove", "configurationDigest": digest, "observationSetDigest": digest, "previewDigest": digest, "currentMatched": 0, "proposedMatched": 0, "excludedObservations": 0, "sendsQueriesUpstream": false, "expiresAt": expires.Format(time.RFC3339Nano)}
				}
				record := currentTestAuditFacts(t, action, targetType, source.InspectionID, at, facts)
				sentinel := errors.New("document write driver failure")
				wantWrites := 2
				switch mode {
				case "document write failure":
					tx.execErrors, wantWrites = []error{sentinel}, 1
				case "audit write failure":
					tx.execErrors = []error{nil, sentinel}
				case "evidence mismatch":
					record.TargetID, wantWrites = source.ID, 1
				}
				got, err := write(federation.MutationEvidence{Audit: record})
				if mode == "success" {
					if err != nil || !reflect.DeepEqual(got, expected) || tx.rollbacks != 0 {
						t.Fatalf("document=%+v, %v rollbacks=%d", got, err, tx.rollbacks)
					}
				} else if err == nil || !reflect.ValueOf(got).IsZero() || tx.rollbacks != 1 {
					t.Fatalf("failed write retained partial result: %+v, %v rollbacks=%d", got, err, tx.rollbacks)
				}
				if (mode == "document write failure" || mode == "audit write failure") && !errors.Is(err, sentinel) {
					t.Fatalf("driver cause lost: %v", err)
				}
				if len(tx.statements) != wantWrites || tx.commits != 0 {
					t.Fatalf("writes=%d want=%d commits=%d", len(tx.statements), wantWrites, tx.commits)
				}
				if tx.arguments[0][0] != source.InspectionID || tx.arguments[0][1] != source.TenantID {
					t.Fatalf("document binding=%v", tx.arguments[0])
				}
				bodyIndex := 2
				if kind == "preview" {
					bodyIndex = 4
					if tx.arguments[0][2] != source.ID || tx.arguments[0][3] != digest {
						t.Fatalf("preview binding=%v", tx.arguments[0])
					}
				}
				encoded, err := json.Marshal(expected)
				if err != nil || !reflect.DeepEqual(tx.arguments[0][bodyIndex], encoded) {
					t.Fatalf("stored document changed: %v", err)
				}
			})
		}
	}
}
