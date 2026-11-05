package database

import (
	"crypto/sha256"
	"encoding/hex"
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

// Construct retained evidence directly; no manager workflow, SQL or remote
// image fetch is needed to check the adapter's integrity boundary.
func retainedArtworkUnitValue(t *testing.T) (federation.ArtworkOverride, []byte) {
	t.Helper()
	value := federation.ArtworkOverride{
		ID: "00000000-0000-7000-8000-000000000051", TenantID: restoreTestTenant,
		Target:   federation.ArtworkTarget{SourceID: "00000000-0000-7000-8000-000000000010", PresentationGeneration: 3, Identifier: "urn:air:peer.example:agents:helper", Kind: catalog.KindAgent},
		Revision: 2, Icon: federation.ArtworkIconSelection{Mode: federation.ArtworkSuppress},
		Screenshots: federation.ArtworkScreenshotSelection{Mode: federation.ArtworkInherit},
		Reason:      "Hide publisher icon", CreatedBy: "administrator", CreatedAt: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC),
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	value.Digest = "sha256:" + hex.EncodeToString(digest[:])
	if err = federation.ValidateArtworkOverrideIntegrity(value); err != nil {
		t.Fatal("invalid retained control", err)
	}
	raw, err = json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return value, raw
}

// PostgreSQL JSONB guarantees valid object JSON and bound identity fields.
// An unguarded numeric field can still violate the retained Go representation.
func artworkWithNumericField(t *testing.T, raw []byte, field string) []byte {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	object[field] = json.RawMessage(`7`)
	changed, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	return changed
}

func TestArtworkReadsRejectCorruptRetainedAuthority(t *testing.T) {
	value, raw := retainedArtworkUnitValue(t)
	const cursor = "00000000-0000-7000-8000-000000000050"
	for _, history := range []bool{false, true} {
		for _, mode := range []string{"valid", "wrong field type", "tampered", "driver"} {
			t.Run(map[bool]string{false: "current/", true: "history/"}[history]+mode, func(t *testing.T) {
				store, tx, ctx, source := federationAdapterFixture(t)
				retained := raw
				sentinel := errors.New("artwork read failed")
				switch mode {
				case "wrong field type":
					retained = artworkWithNumericField(t, raw, "reason")
				case "tampered":
					changed := value
					changed.Icon.Mode = federation.ArtworkInherit
					var err error
					retained, err = json.Marshal(changed)
					if err != nil {
						t.Fatal(err)
					}
				}
				var got federation.ArtworkOverride
				var err error
				if history {
					rows := &identityScriptedRows{values: [][]any{{retained}}}
					tx.queryResults = []identityQueryResult{{rows: rows}}
					if mode == "driver" {
						rows.err = sentinel
					}
					var items []federation.ArtworkOverride
					items, err = store.ListArtworkOverrideHistory(ctx, source.TenantID, value.Target, cursor, 2)
					if err == nil && len(items) == 1 {
						got = items[0]
					}
					if !rows.closed || !reflect.DeepEqual(tx.queryArguments[0], []any{source.TenantID, value.Target.SourceID, int64(3), federation.ArtworkTargetKey(value.Target), cursor, 2}) {
						t.Fatal("history lost scope, continuation, bound or row cleanup")
					}
				} else {
					row := scriptedValueRow([]any{retained})
					if mode == "driver" {
						row = scannerFunc(func(...any) error { return sentinel })
					}
					tx.queryRows = []pgx.Row{row}
					got, err = store.GetArtworkOverride(ctx, source.TenantID, value.Target)
					if !reflect.DeepEqual(tx.queryRowArguments[0], []any{source.TenantID, value.Target.SourceID, int64(3), federation.ArtworkTargetKey(value.Target)}) {
						t.Fatal("current head lost tenant or exact presentation target")
					}
				}
				if mode == "valid" {
					if err != nil || !reflect.DeepEqual(got, value) {
						t.Fatalf("retained artwork=%+v error=%v", got, err)
					}
				} else if err == nil || mode == "driver" && !errors.Is(err, sentinel) {
					t.Fatalf("corrupt or failed read accepted: %v", err)
				}
			})
		}
	}
	store := &PostgresFederationStore{}
	for _, limit := range []int{0, 102} {
		if _, err := store.ListArtworkOverrideHistory(t.Context(), value.TenantID, value.Target, "", limit); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatal("invalid history bound reached persistence", err)
		}
	}
}

func TestArtworkBatchProjectsCurrentSourceAndRetainedOverride(t *testing.T) {
	value, raw := retainedArtworkUnitValue(t)
	entryDigest := audit.DigestBytes([]byte(`{"identifier":"urn:air:peer.example:agents:helper","type":"application/a2a-agent-card+json","url":"https://peer.example/card"}`))
	for _, mode := range []string{"override", "inherit", "corrupt override", "corrupt source", "corrupt candidates"} {
		t.Run(mode, func(t *testing.T) {
			store, tx, ctx, source := federationAdapterFixture(t)
			source.PresentationGeneration = value.Target.PresentationGeneration
			source.Revision = 7
			configuration, err := marshalFederationSourceConfiguration(source)
			if err != nil {
				t.Fatal(err)
			}
			override, candidates := raw, []byte(`{}`)
			switch mode {
			case "inherit":
				override, candidates = nil, nil
			case "corrupt override":
				override = artworkWithNumericField(t, raw, "reason")
			case "corrupt source":
				configuration = artworkWithNumericField(t, configuration, "displayName")
			case "corrupt candidates":
				candidates = []byte(`{"nativeIcon":7}`)
			}
			rows := &identityScriptedRows{values: [][]any{{value.Target.SourceID, int64(3), value.Target.Identifier, "", false, value.Target.Kind, configuration, federation.SourceError, int64(7), override, candidates, entryDigest, true}}}
			tx.queryResults = []identityQueryResult{{rows: rows}}
			requests := []federation.ArtworkReadRequest{{Target: value.Target, EntryDigest: entryDigest}}
			got, err := store.ArtworkReadBatch(ctx, source.TenantID, requests)
			if mode == "override" || mode == "inherit" {
				read, ok := got[value.Target]
				if err != nil || len(got) != 1 || !ok || !read.Eligible || read.EntryDigest != entryDigest || read.Source.Status != federation.SourceError || read.Source.Revision != 7 || read.Source.PresentationGeneration != 3 {
					t.Fatalf("artwork projection=%+v error=%v", got, err)
				}
				if mode == "override" && (read.Override == nil || !reflect.DeepEqual(*read.Override, value)) || mode == "inherit" && read.Override != nil {
					t.Fatal("optional override projection changed")
				}
			} else if err == nil || got != nil {
				t.Fatal("corrupt retained presentation disclosed", err)
			}
			if !rows.closed {
				t.Fatal("artwork rows remained open")
			}
			if tx.queryArguments[0][0] != source.TenantID {
				t.Fatal("batch lost tenant")
			}
			var selections []map[string]any
			encodedSelections, ok := tx.queryArguments[0][1].([]byte)
			if !ok {
				t.Fatal("batch selection did not use encoded JSON")
			}
			if err = json.Unmarshal(encodedSelections, &selections); err != nil || len(selections) != 1 || selections[0]["identifier"] != value.Target.Identifier || selections[0]["generation"] != float64(3) || selections[0]["digest"] != entryDigest || selections[0]["live"] != false {
				t.Fatal("batch lost target or freshness bindings", err)
			}
		})
	}
	if _, err := (&PostgresFederationStore{}).ArtworkReadBatch(t.Context(), value.TenantID, make([]federation.ArtworkReadRequest, 101)); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatal("oversized batch reached persistence", err)
	}
}

// Driver fixtures check bounded-result interpretation and failure propagation.
// Real SQL head counting, generation isolation and RLS use integration tests.
func TestArtworkOverrideCountPreservesBoundedResultsAndReadFailure(t *testing.T) {
	failure := errors.New("artwork count interrupted")
	for _, test := range []struct {
		name        string
		count, want int
		overflow    bool
		err         error
	}{
		{name: "empty"},
		{name: "exact limit", count: 2, want: 2},
		{name: "limit plus one", count: 3, want: 2, overflow: true},
		{name: "driver failure", err: failure},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, tx, ctx, source := federationAdapterFixture(t)
			row := scriptedValueRow([]any{test.count})
			if test.err != nil {
				row = scannerFunc(func(...any) error { return test.err })
			}
			tx.queryRows = []pgx.Row{row}
			count, overflow, err := store.ArtworkOverrideCount(ctx, source.TenantID, source.ID, 3, 2)
			if count != test.want || overflow != test.overflow || !errors.Is(err, test.err) {
				t.Fatalf("count=%d, overflow=%t, error=%v", count, overflow, err)
			}
			want := [][]any{{source.TenantID, source.ID, int64(3), 3}}
			if !reflect.DeepEqual(tx.queryRowArguments, want) {
				t.Fatalf("count scope and selection bound: %#v", tx.queryRowArguments)
			}
		})
	}
}
