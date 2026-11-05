package database

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

type searchHydrationRows struct {
	identityScriptedRows
	scan func(...any) error
}

func (r *searchHydrationRows) Scan(destinations ...any) error { return r.scan(destinations...) }

// Real-role integration tests own SQL and full-record equivalence. These small
// rows exercise cancellation/driver faults and prove every opened result closes.
func searchHydrationResults() ([]identityQueryResult, []*searchHydrationRows) {
	rows := make([]*searchHydrationRows, 2)
	results := make([]identityQueryResult, len(rows))
	for phase := range rows {
		rows[phase] = &searchHydrationRows{identityScriptedRows: identityScriptedRows{values: [][]any{{}}}}
		rows[phase].scan = func(destinations ...any) error {
			for _, destination := range destinations {
				reflect.ValueOf(destination).Elem().SetZero()
			}
			assign := func(index int, value string) { reflect.ValueOf(destinations[index]).Elem().SetString(value) }

			if phase == 0 {
				assign(7, "version")
				assign(18, "release")
				assign(27, "catalog-validation-v7")
			} else {
				assign(0, "version")
				assign(1, "representation")
			}
			return nil
		}
		results[phase] = identityQueryResult{rows: rows[phase]}
	}
	return results, rows
}

func TestSearchHydrationFaultsCloseRows(t *testing.T) {
	for phase := 0; phase < 2; phase++ {
		for _, fault := range []string{"query", "scan", "iterate"} {
			t.Run(strconv.Itoa(phase)+"/"+fault, func(t *testing.T) {
				results, rows := searchHydrationResults()
				failure := errors.New("injected database fault")
				switch fault {
				case "query":
					results[phase].err = failure
				case "scan":
					rows[phase].values = [][]any{{}}
					rows[phase].scan = func(...any) error { return failure }
				case "iterate":
					rows[phase].err = failure
				}
				tx := &identityScriptedTx{queryResults: results}
				if _, err := readSearchRecords(t.Context(), tx, "tenant", []string{"release"}); !errors.Is(err, failure) {
					t.Fatalf("injected error lost: %v", err)
				}
				for index := 0; index <= phase; index++ {
					if index == phase && fault == "query" {
						continue
					}
					if !rows[index].closed {
						t.Fatalf("result %d left open", index)
					}
				}
				for _, arguments := range tx.queryArguments {
					ids, ok := arguments[1].([]string)
					if arguments[0] != "tenant" || !ok || len(ids) != 1 {
						t.Fatal("query lost tenant or bounded ID selection")
					}
				}
			})
		}
	}
}

func TestSearchSummaryIntegrity(t *testing.T) {
	for _, mode := range []string{"missing parent", "missing representations", "unexpected revision", "too many representations", "invalid profile", "malformed retrievals", "expiry mismatch"} {
		t.Run(mode, func(t *testing.T) {
			results, rows := searchHydrationResults()
			code := catalog.CodeDependencyUnavailable
			if mode == "malformed retrievals" {
				code = catalog.CodeInternal
			}
			switch mode {
			case "missing parent":
				rows[0].values = nil
				code = catalog.CodeNotFound
			case "missing representations":
				rows[1].values = nil
			case "too many representations":
				rows[1].values = make([][]any, 17)
			default:
				phase := 0
				if mode == "unexpected revision" {
					phase = 1
				}
				scan := rows[phase].scan
				rows[phase].scan = func(dst ...any) error {
					if err := scan(dst...); err != nil {
						return err
					}
					switch mode {
					case "unexpected revision":
						reflect.ValueOf(dst[0]).Elem().SetString("other")
					case "invalid profile":
						reflect.ValueOf(dst[27]).Elem().SetString("obsolete")
					case "malformed retrievals":
						reflect.ValueOf(dst[28]).Elem().SetBytes([]byte("["))
					case "expiry mismatch":
						reflect.ValueOf(dst[26]).Elem().Set(reflect.ValueOf(new(time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC))))
					}
					return nil
				}
			}
			if _, err := readSearchRecords(t.Context(), &identityScriptedTx{queryResults: results}, "tenant", []string{"release"}); catalog.ErrorCodeOf(err) != code {
				t.Fatalf("invalid stored metadata accepted: %v", err)
			}
			if !rows[0].closed {
				t.Fatal("summary rows left open")
			}
		})
	}
	tx := &identityScriptedTx{}
	if items, err := readSearchRecords(context.Background(), tx, "tenant", nil); err != nil || len(items) != 0 || len(tx.queryStatements) != 0 {
		t.Fatal("empty selection performed database work")
	}
}

func TestSearchHydrationDeduplicatesSharedRevisionReads(t *testing.T) {
	results, rows := searchHydrationResults()
	rows[0].values = [][]any{{}, {}}
	baseScan := rows[0].scan
	rows[0].scan = func(destinations ...any) error {
		if err := baseScan(destinations...); err != nil {
			return err
		}
		reflect.ValueOf(destinations[18]).Elem().SetString("release-" + strconv.Itoa(rows[0].index))
		reflect.ValueOf(destinations[12]).Elem().SetString("listing-" + strconv.Itoa(rows[0].index))
		return nil
	}
	tx := &identityScriptedTx{queryResults: results}
	items, err := readSearchRecords(t.Context(), tx, "tenant", []string{"release-2", "release-1"})
	if err != nil || len(items) != 2 || len(tx.queryStatements) != 2 {
		t.Fatalf("shared revision batch failed: %v", err)
	}
	if !reflect.DeepEqual(tx.queryArguments[1][1], []string{"version"}) {
		t.Fatal("shared version was not selected once")
	}
	for id, item := range items {
		if item.Release.ID != id || item.VersionRevision.ID != "version" {
			t.Fatal("shared revision hydration lost a release or child")
		}
	}
}

func TestSearchSummaryBindsExternalRetrievalMetadata(t *testing.T) {
	retrieved := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	expires := retrieved.Add(catalog.ExternalRepresentationFreshness)
	for _, fault := range []string{"valid", "representation", "url", "digest", "size", "media type", "unretained", "status", "missing retrieval"} {
		t.Run(fault, func(t *testing.T) {
			binding := catalog.ExternalRepresentationBinding{ID: "representation", URL: "https://source.example/card.json", MediaType: "application/json; charset=utf-8", ContentDigest: "sha256:" + strings.Repeat("a", 64), Size: 123, Retained: true}
			retrieval := catalog.ExternalRetrieval{RepresentationID: binding.ID, RequestedURL: binding.URL, FinalURL: binding.URL, Status: 200, MediaType: "application/json", ContentDigest: binding.ContentDigest, Size: binding.Size, PolicyID: "default", PolicyRevision: 1, PolicyDigest: "sha256:" + strings.Repeat("b", 64), RetrievedAt: retrieved, ExpiresAt: expires}
			switch fault {
			case "representation":
				binding.ID = "another-representation"
			case "url":
				binding.URL = "https://source.example/other.json"
			case "digest":
				binding.ContentDigest = "sha256:" + strings.Repeat("c", 64)
			case "size":
				binding.Size++
			case "media type":
				binding.MediaType = "text/plain"
			case "unretained":
				binding.Retained = false
			case "status":
				retrieval.Status = 404
			}
			retrievals := []catalog.ExternalRetrieval{retrieval}
			expiry := &expires
			if fault == "missing retrieval" {
				retrievals = nil
				expiry = nil // Keep aggregate expiry valid so the completeness guard owns rejection.
			}
			raw, err := json.Marshal(retrievals)
			if err != nil {
				t.Fatal(err)
			}
			results, rows := searchHydrationResults()
			baseScan := rows[0].scan
			rows[0].scan = func(dst ...any) error {
				if err := baseScan(dst...); err != nil {
					return err
				}
				setScanDestination(t, dst, 9, "latest-evidence")
				setScanDestination(t, dst, 24, catalog.EvidenceComplete)
				setScanDestination(t, dst, 26, expiry)
				setScanDestination(t, dst, 28, raw)
				return nil
			}
			rows[1].scan = func(dst ...any) error {
				setScanDestination(t, dst, 0, "version")
				setScanDestination(t, dst, 1, binding.ID)
				setScanDestination(t, dst, 2, binding.URL)
				setScanDestination(t, dst, 3, binding.MediaType)
				setScanDestination(t, dst, 4, binding.ContentDigest)
				setScanDestination(t, dst, 5, binding.Size)
				setScanDestination(t, dst, 6, binding.Retained)
				return nil
			}
			items, err := readSearchRecords(t.Context(), &identityScriptedTx{queryResults: results}, "tenant", []string{"release"})
			if fault == "valid" {
				if err != nil || len(items) != 1 {
					t.Fatalf("valid external summary: %v", err)
				}
				item := items["release"]
				if item.Evidence.ID != "latest-evidence" || item.VersionRevision.EvidenceSetID != item.Evidence.ID || item.Evidence.State != catalog.EvidenceComplete || !item.Evidence.ExpiresAt.Equal(expires) || item.Source != "local" || item.ListingRevision.Tags == nil {
					t.Fatalf("external evidence or collection metadata lost: %+v", item)
				}
			} else if catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || items != nil {
				t.Fatalf("invalid external summary returned partial results: items=%v, err=%v", items, err)
			}
			if !rows[0].closed || !rows[1].closed {
				t.Fatal("external binding read left rows open")
			}
		})
	}
}

// Query text is part of this resource-use contract; real PostgreSQL owns
// column validity, privileges and row semantics.
func TestSearchSummaryQueriesExcludeRetainedBodies(t *testing.T) {
	for _, sql := range []string{catalogSummaryReadSQL, catalogSummaryBindingsSQL} {
		for _, forbidden := range []string{"inline_document", "normalized_facts", "unknown_properties", ".findings", "skill_package_files", "SELECT *"} {
			if strings.Contains(sql, forbidden) {
				t.Fatalf("collection query loads detail-only field %s", forbidden)
			}
		}
		if !strings.Contains(sql, "tenant_id=$1") || !strings.Contains(sql, "ANY($2::uuid[])") {
			t.Fatal("unscoped collection metadata query")
		}
	}
}
