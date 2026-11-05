// Catalog record units exercise bounded repository calls using retained local rows.
package database

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// catalogRecordReadFixture binds joined metadata and immutable child rows without
// constructing an authoring workflow. Existing decoder tests own field matrices.
func catalogRecordReadFixture() (catalog.CatalogRecord, []any, []any, []any) {
	at := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	r := catalog.CatalogRecord{
		Resource: catalog.Resource{ID: "resource", TenantID: restoreTestTenant, NamespaceID: "namespace", CanonicalIdentifier: "urn:air:catalog.example.com:agents:example", Kind: catalog.KindAgent, CreatedBy: "author", Revision: 1, CreatedAt: at, UpdatedAt: at},
		Version:  catalog.ResourceVersion{ID: "version", TenantID: restoreTestTenant, ResourceID: "resource", Version: "1.2.3", SealedRevisionID: "revision", Revision: 2, CreatedAt: at, UpdatedAt: at},
		Listing:  catalog.CatalogListing{ID: "listing", TenantID: restoreTestTenant, NamespaceID: "namespace", ResourceID: "resource", CurrentReleaseID: "release", Revision: 3, CreatedAt: at, UpdatedAt: at},
		Release:  catalog.ListingRelease{ID: "release", TenantID: restoreTestTenant, ListingID: "listing", SubmissionID: "submission", VersionRevisionID: "revision", ListingRevisionID: "listing-revision", State: catalog.ReleasePublished, ActivatedBy: "publisher", Revision: 1, ActivatedAt: at, UpdatedAt: at},
	}
	rv, v, l, lr := r.Resource, r.Version, r.Listing, r.Release
	joined := []any{rv.ID, rv.TenantID, rv.NamespaceID, rv.CanonicalIdentifier, rv.Kind, rv.CreatedBy, rv.Revision, rv.CreatedAt, rv.UpdatedAt,
		v.ID, v.TenantID, v.ResourceID, v.Version, v.CandidateRevisionID, v.SealedRevisionID, v.Revision, v.CreatedAt, v.UpdatedAt,
		l.ID, l.TenantID, l.NamespaceID, l.ResourceID, l.CandidateRevisionID, l.CurrentReleaseID, l.Revision, l.CreatedAt, l.UpdatedAt,
		lr.ID, lr.TenantID, lr.ListingID, lr.SubmissionID, lr.VersionRevisionID, lr.ListingRevisionID, lr.State, lr.ReplacementReleaseID, lr.DispositionReason, lr.SunsetAt, lr.ActivatedBy, lr.Revision, lr.ActivatedAt, lr.UpdatedAt}
	version, _ := restoreValidRevisionValues()
	listing := []any{"listing-revision", restoreTestTenant, "listing", 1, "revision", "Example", "Description", []string{"tools"}, []string{"agents"}, []string{"find example"}, "publisher", "https://example.com/docs", "", "Apache-2.0", "", catalog.VisibilityPublic, []byte(`[]`), []byte(`{}`), "listing-digest", "author", at}
	return r, joined, version, listing
}

// catalogRecordChildren scripts the representation and package metadata queries
// already used by the exact version reader.
func catalogRecordChildren() []identityQueryResult {
	_, representation := restoreValidRevisionValues()
	return []identityQueryResult{{rows: &identityScriptedRows{values: [][]any{representation}}}, {rows: &identityScriptedRows{}}, {rows: &identityScriptedRows{}}}
}

// requireHydratedCatalogRecord checks parent identity, selected immutable children,
// validation evidence and origin without recreating individual scanner matrices.
func requireHydratedCatalogRecord(t *testing.T, got, want catalog.CatalogRecord) {
	t.Helper()
	if err := catalog.ValidateVersionRevisionIntegrity(got.VersionRevision, got.Evidence); err != nil {
		t.Fatal(err)
	}
	if err := catalog.ValidateEvidenceSetIntegrity(got.Evidence); err != nil {
		t.Fatal(err)
	}
	if got.Resource != want.Resource || got.Version != want.Version || got.Listing != want.Listing || !reflect.DeepEqual(got.Release, want.Release) || got.Source != "local" {
		t.Fatalf("parent projection=%+v", got)
	}
	if got.VersionRevision.ID != want.Release.VersionRevisionID || got.VersionRevision.EvidenceSetID != "latest-evidence" || got.Evidence.ID != "latest-evidence" || got.Evidence.SubjectDigest != got.VersionRevision.ContentSetDigest || got.ListingRevision.ID != want.Release.ListingRevisionID || got.ListingRevision.DisplayName != "Example" || len(got.VersionRevision.Representations) != 1 || string(got.VersionRevision.Representations[0].Data) != `{"name":"example"}` {
		t.Fatalf("hydrated projection=%+v", got)
	}
}

// TestCurrentCatalogRecordRead checks exact selection and stops hydration at the
// first failed parent/revision rather than returning a partial public record.
func TestCurrentCatalogRecordRead(t *testing.T) {
	pool := &pgxpool.Pool{}
	repo := &PostgresRepository{pool: pool}
	failure := errors.New("record hydration failure")
	for _, outcome := range []string{"record", "missing", "version failure", "listing failure"} {
		t.Run(outcome, func(t *testing.T) {
			want, joined, version, listing := catalogRecordReadFixture()
			joined = append(joined, "Example publisher", "Example namespace")
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scriptedValueRow(joined), scriptedValueRow(version), scriptedValueRow(listing)}, queryResults: catalogRecordChildren()}
			code := catalog.ErrorCode("")
			reads, queries := 3, 3
			switch outcome {
			case "missing":
				tx.queryRows[0] = scannerFunc(func(...any) error { return pgx.ErrNoRows })
				code = catalog.CodeNotFound
				reads, queries = 1, 0
			case "version failure":
				tx.queryRows[1] = scannerFunc(func(...any) error { return failure })
				code = catalog.CodeDependencyUnavailable
				reads, queries = 2, 0
			case "listing failure":
				tx.queryRows[2] = scannerFunc(func(...any) error { return failure })
				code = catalog.CodeDependencyUnavailable
			}
			got, err := repo.GetCatalogRecord(identityRepositoryContext(pool, tx), restoreTestTenant, want.Release.ID)
			if code == "" {
				if err != nil {
					t.Fatal(err)
				}
				requireHydratedCatalogRecord(t, got, want)
				if got.Display == nil || got.Display.PublisherName != "Example publisher" || got.Display.NamespaceName != "Example namespace" || got.Display.License != "Apache-2.0" {
					t.Fatalf("exact record display=%+v", got.Display)
				}
			} else if err == nil || catalog.ErrorCodeOf(err) != code || !reflect.DeepEqual(got, catalog.CatalogRecord{}) {
				t.Fatalf("read=%+v error=%v", got, err)
			}
			if code == catalog.CodeDependencyUnavailable && !errors.Is(err, failure) {
				t.Fatalf("lost cause: %v", err)
			}
			if len(tx.queryRowStatements) != reads || len(tx.queryStatements) != queries || !reflect.DeepEqual(tx.queryRowArguments[0], []any{restoreTestTenant, []string{want.Release.ID}}) {
				t.Fatalf("selection reads=%v queries=%v", tx.queryRowArguments, tx.queryArguments)
			}
			if reads > 1 && !reflect.DeepEqual(tx.queryRowArguments[1], []any{restoreTestTenant, []string{want.Release.VersionRevisionID}, false}) {
				t.Fatal("version lookup lost tenant or visibility")
			}
			if reads > 2 && !reflect.DeepEqual(tx.queryRowArguments[2], []any{restoreTestTenant, []string{want.Release.ListingRevisionID}, false}) {
				t.Fatal("listing lookup lost tenant or visibility")
			}
		})
	}
}

// TestPublicationRecordSelection checks limit+1 before any document hydration,
// and verifies failure cleanup through the same tenant transaction.
func TestPublicationRecordSelection(t *testing.T) {
	pool := &pgxpool.Pool{}
	repo := &PostgresRepository{pool: pool}
	failure := errors.New("publication projection failure")
	for _, outcome := range []string{"public", "tenant", "empty", "overflow", "query failure", "scan failure", "iteration failure", "version failure", "listing failure"} {
		t.Run(outcome, func(t *testing.T) {
			want, joined, version, listing := catalogRecordReadFixture()
			rows := &identityScriptedRows{values: [][]any{joined}}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scriptedValueRow(version), scriptedValueRow(listing)}, queryResults: append([]identityQueryResult{{rows: rows}}, catalogRecordChildren()...)}
			code := catalog.ErrorCode("")
			reads, queries := 2, 4
			switch outcome {
			case "empty":
				rows.values = nil
				reads, queries = 0, 1
			case "overflow":
				second := append([]any(nil), joined...)
				second[27] = "second-release"
				rows.values = append(rows.values, second)
				reads, queries = 0, 1
			case "query failure":
				tx.queryResults[0].err = failure
				code = catalog.CodeDependencyUnavailable
				reads, queries = 0, 1
			case "scan failure":
				rows.values = [][]any{{"partial"}}
				code = catalog.CodeDependencyUnavailable
				reads, queries = 0, 1
			case "iteration failure":
				rows.err = failure
				code = catalog.CodeDependencyUnavailable
				reads, queries = 0, 1
			case "version failure":
				tx.queryRows[0] = scannerFunc(func(...any) error { return failure })
				code = catalog.CodeDependencyUnavailable
				reads, queries = 1, 1
			case "listing failure":
				tx.queryRows[1] = scannerFunc(func(...any) error { return failure })
				code = catalog.CodeDependencyUnavailable
			}
			request := catalog.PublicationRecordRequest{Limit: 1, IncludeTenant: outcome == "tenant"}
			got, err := repo.PublicationRecords(identityRepositoryContext(pool, tx), restoreTestTenant, request)
			if code != "" {
				if err == nil || catalog.ErrorCodeOf(err) != code || !reflect.DeepEqual(got, catalog.PublicationRecordPage{}) {
					t.Fatalf("page=%+v error=%v", got, err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				switch outcome {
				case "overflow":
					if !got.Overflow || got.Records != nil {
						t.Fatalf("partial overflow=%+v", got)
					}
				case "empty":
					if got.Records == nil || len(got.Records) != 0 || got.Overflow {
						t.Fatalf("empty=%+v", got)
					}
				default:
					if len(got.Records) != 1 || got.Overflow {
						t.Fatalf("page=%+v", got)
					}
					requireHydratedCatalogRecord(t, got.Records[0], want)
				}
			}
			if len(tx.queryRowStatements) != reads || len(tx.queryStatements) != queries {
				t.Fatalf("unbounded/unexpected hydration reads=%d queries=%d", len(tx.queryRowStatements), len(tx.queryStatements))
			}
			if !reflect.DeepEqual(tx.queryArguments[0], []any{restoreTestTenant, request.IncludeTenant, 2}) {
				t.Fatalf("selection=%v", tx.queryArguments[0])
			}
			if outcome != "query failure" && !rows.closed {
				t.Fatal("selection rows leaked")
			}
			if !strings.Contains(tx.queryStatements[0], "LIMIT $3") || !strings.Contains(tx.queryStatements[0], "publisher.approved_publication_domains") {
				t.Fatal("missing publication selection boundary")
			}
		})
	}
	for _, limit := range []int{0, -1, catalog.MaxPublicationRecords + 1} {
		tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}}
		got, err := repo.PublicationRecords(identityRepositoryContext(pool, tx), restoreTestTenant, catalog.PublicationRecordRequest{Limit: limit})
		if catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument || !reflect.DeepEqual(got, catalog.PublicationRecordPage{}) || len(tx.queryStatements) != 0 {
			t.Fatalf("invalid limit=%d result=%+v error=%v", limit, got, err)
		}
	}
}

// TestPublishedReleaseLookupBoundaries ensures overflow cannot be mistaken for a
// complete withdrawal scope and closes every opened result.
func TestPublishedReleaseLookupBoundaries(t *testing.T) {
	pool := &pgxpool.Pool{}
	repo := &PostgresRepository{pool: pool}
	failure := errors.New("release lookup failure")
	for _, outcome := range []string{"empty", "complete", "overflow", "query", "scan", "iteration", "invalid limit"} {
		t.Run(outcome, func(t *testing.T) {
			rows := &identityScriptedRows{values: [][]any{{"release"}}}
			result := identityQueryResult{rows: rows}
			limit := 1
			code := catalog.ErrorCode("")
			switch outcome {
			case "empty":
				rows.values = nil
			case "overflow":
				rows.values = append(rows.values, []any{"next"})
			case "query":
				result.err = failure
				code = catalog.CodeDependencyUnavailable
			case "scan":
				rows.values = [][]any{{}}
				code = catalog.CodeDependencyUnavailable
			case "iteration":
				rows.err = failure
				code = catalog.CodeDependencyUnavailable
			case "invalid limit":
				limit = 0
				code = catalog.CodeInvalidArgument
			}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{result}}
			ids, overflow, err := repo.PublishedReleaseIDsByVersionRevision(identityRepositoryContext(pool, tx), restoreTestTenant, "revision", limit)
			if code != "" {
				if err == nil || catalog.ErrorCodeOf(err) != code || ids != nil || overflow {
					t.Fatalf("lookup=%v %t %v", ids, overflow, err)
				}
			} else {
				if err != nil || overflow != (outcome == "overflow") {
					t.Fatalf("lookup=%v %t %v", ids, overflow, err)
				}
				if overflow {
					if ids != nil {
						t.Fatal("partial release set leaked")
					}
				} else {
					want := []string{}
					if outcome == "complete" {
						want = append(want, "release")
					}
					if !reflect.DeepEqual(ids, want) {
						t.Fatalf("ids=%v want=%v", ids, want)
					}
				}
			}
			if outcome == "invalid limit" {
				if len(tx.queryStatements) != 0 {
					t.Fatal("invalid bound reached SQL")
				}
				return
			}
			if !reflect.DeepEqual(tx.queryArguments, [][]any{{restoreTestTenant, "revision", 2}}) {
				t.Fatalf("release selection=%v", tx.queryArguments)
			}
			if outcome != "query" && !rows.closed {
				t.Fatal("rows not closed")
			}
		})
	}
}

// TestOutboxDeferralAdmission covers pure argument rejection without touching the
// pool; actual claim/lease/deferral SQL stays with integration qualification.
func TestOutboxDeferralAdmission(t *testing.T) {
	repo := &PostgresRepository{}
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	for _, at := range []time.Time{now.Add(-time.Second), now, now.Add(time.Hour + time.Nanosecond)} {
		if err := repo.DeferOutbox(t.Context(), restoreTestTenant, "event", "worker", now, at); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("invalid deferral %v: %v", at, err)
		}
	}
}

// TestPublicationProjectionByteBudget uses individually small documents to
// exercise the aggregate ceiling, without hydrating the remaining selected row.
func TestPublicationProjectionByteBudget(t *testing.T) {
	pool := &pgxpool.Pool{}
	repo := &PostgresRepository{pool: pool}
	document := []byte(`{"name":"example","extension":"` + strings.Repeat("x", 1<<20) + `"}`)
	if int64(len(document)) > catalog.MaxCatalogDocumentBytes {
		t.Fatal("fixture exceeds individual document bound")
	}
	for _, count := range []int{31, 33} {
		t.Run(fmt.Sprintf("records=%d", count), func(t *testing.T) {
			selection := &identityScriptedRows{}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{{rows: selection}}}
			for index := range count {
				_, joined, version, listing := catalogRecordReadFixture()
				representation := representationReadValues()
				bindings := map[string]string{}
				for offset, key := range []string{"resource", "namespace", "version", "revision", "listing", "listing-revision", "release", "representation"} {
					bindings[key] = fmt.Sprintf("00000000-0000-7000-8000-%012d", index*10+offset+100)
				}
				for _, values := range [][]any{joined, version, listing, representation} {
					for column, value := range values {
						if text, ok := value.(string); ok {
							if id, exists := bindings[text]; exists {
								values[column] = id
							}
						}
					}
				}
				joined[3] = fmt.Sprintf("urn:air:catalog.example.com:agents:example-%d", index)
				// Golden digests bind the fixed large document and current evidence.
				// The successful control checks the public integrity validators.
				representation[6], representation[7], representation[8] = document, "sha256:656b4863bfffe7398bb94840b2ddc8d3b7b5fee19e160cb4915a6c9f97fd9da1", int64(len(document))
				version[4], version[12], version[13] = "sha256:e53399198bd3ee2d9685fad3d95b394f401b095c4b1ace5d125073ea06a6b98b", "sha256:e53399198bd3ee2d9685fad3d95b394f401b095c4b1ace5d125073ea06a6b98b", "sha256:7b1eb307c92ed5e76d35eba4efffd52286d5afd4583c1c7e253081a5fe04f083"
				selection.values = append(selection.values, joined)
				tx.queryRows = append(tx.queryRows, scriptedValueRow(version), scriptedValueRow(listing))
				tx.queryResults = append(tx.queryResults, identityQueryResult{rows: &identityScriptedRows{values: [][]any{representation}}}, identityQueryResult{rows: &identityScriptedRows{}}, identityQueryResult{rows: &identityScriptedRows{}})
			}
			got, err := repo.PublicationRecords(identityRepositoryContext(pool, tx), restoreTestTenant, catalog.PublicationRecordRequest{Limit: count})
			if count == 31 {
				if err != nil || len(got.Records) != count || got.Overflow {
					t.Fatalf("bounded projection: count=%d overflow=%t error=%v", len(got.Records), got.Overflow, err)
				}
				for _, item := range got.Records {
					if err := catalog.ValidateVersionRevisionIntegrity(item.VersionRevision, item.Evidence); err != nil {
						t.Fatal(err)
					}
					if err := catalog.ValidateEvidenceSetIntegrity(item.Evidence); err != nil {
						t.Fatal(err)
					}
				}
			} else if catalog.ErrorCodeOf(err) != catalog.CodeWorkLimitExceeded || !reflect.DeepEqual(got, catalog.PublicationRecordPage{}) || len(tx.queryRowStatements) != 64 || len(tx.queryStatements) != 97 || tx.rollbacks != 1 {
				t.Fatalf("over-budget projection count=%d error=%v reads=%d queries=%d rollback=%d", len(got.Records), err, len(tx.queryRowStatements), len(tx.queryStatements), tx.rollbacks)
			}
			if !selection.closed {
				t.Fatal("selection result leaked")
			}
		})
	}
}
