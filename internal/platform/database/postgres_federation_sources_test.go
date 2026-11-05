// Federation source reads use controlled projections, never remote destinations.
package database

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
)

// TestFederationSourceReads checks removed-source hiding and endpoint query scope.
func TestFederationSourceReads(t *testing.T) {
	failure := errors.New("source read failure")
	for _, outcome := range []string{"active", "removed", "missing", "driver"} {
		t.Run(outcome, func(t *testing.T) {
			store, tx, ctx, source := federationAdapterFixture(t)
			code := catalog.ErrorCode("")
			if outcome == "removed" {
				source.Status = federation.SourceRemoved
				code = catalog.CodeNotFound
			}
			tx.queryRows = []pgx.Row{federationSourceRow(t, source)}
			if outcome == "missing" {
				tx.queryRows[0] = scannerFunc(func(...any) error { return pgx.ErrNoRows })
				code = catalog.CodeNotFound
			}
			if outcome == "driver" {
				tx.queryRows[0] = scannerFunc(func(...any) error { return failure })
				code = catalog.CodeDependencyUnavailable
			}
			got, err := store.GetSource(ctx, source.TenantID, source.ID)
			source.InspectionID = "" // Write admission input is not part of the retained source document.
			if code == "" {
				if err != nil || !reflect.DeepEqual(got, source) {
					t.Fatalf("source=%+v error=%v", got, err)
				}
			} else if err == nil || catalog.ErrorCodeOf(err) != code || !reflect.DeepEqual(got, federation.Source{}) {
				t.Fatalf("hidden source=%+v error=%v", got, err)
			}
			if !reflect.DeepEqual(tx.queryRowArguments, [][]any{{source.TenantID, source.ID}}) || strings.Contains(tx.queryRowStatements[0], "FOR UPDATE") {
				t.Fatal("read scope/lock changed")
			}
		})
	}
	for _, outcome := range []string{"connected", "absent", "driver"} {
		t.Run(outcome, func(t *testing.T) {
			store, tx, ctx, source := federationAdapterFixture(t)
			tx.queryRows = []pgx.Row{scriptedValueRow([]any{outcome == "connected"})}
			if outcome == "driver" {
				tx.queryRows[0] = scannerFunc(func(...any) error { return failure })
			}
			endpoint := "https://partner.example.com/ard/v1/search"
			got, err := store.SourceEndpointConnected(ctx, source.TenantID, source.ID, endpoint)
			if outcome == "driver" {
				if !errors.Is(err, failure) || got {
					t.Fatalf("failed lookup=%t %v", got, err)
				}
			} else if err != nil || got != (outcome == "connected") {
				t.Fatalf("connected=%t %v", got, err)
			}
			if !reflect.DeepEqual(tx.queryRowArguments, [][]any{{source.TenantID, source.ID, federation.SourceRemoved, endpoint}}) {
				t.Fatalf("endpoint scope=%v", tx.queryRowArguments)
			}
		})
	}
}

// TestFederationSourcePageFailures complements the existing populated page test.
func TestFederationSourcePageFailures(t *testing.T) {
	failure := errors.New("source page failure")
	for _, outcome := range []string{"empty", "query", "scan", "decode", "iteration"} {
		t.Run(outcome, func(t *testing.T) {
			store, tx, ctx, source := federationAdapterFixture(t)
			rows := &identityScriptedRows{values: [][]any{federationSourceValues(t, source)}}
			result := identityQueryResult{rows: rows}
			code := catalog.CodeDependencyUnavailable
			switch outcome {
			case "empty":
				rows.values = nil
				code = ""
			case "query":
				result.err = failure
			case "scan":
				rows.values = [][]any{{}}
			case "decode":
				rows.values[0][0] = []byte(`[]`)
				code = catalog.CodeInternal
			case "iteration":
				rows.err = failure
			}
			tx.queryResults = []identityQueryResult{result}
			request := federation.SourceListRequest{AfterDisplayName: "Before", AfterID: source.ID, Limit: 2}
			got, err := store.ListSourcePage(ctx, source.TenantID, request)
			if code == "" {
				if err != nil || got == nil || len(got) != 0 {
					t.Fatalf("empty=%v %v", got, err)
				}
			} else if err == nil || catalog.ErrorCodeOf(err) != code || got != nil {
				t.Fatalf("page=%v %v", got, err)
			}
			if outcome != "query" && !rows.closed {
				t.Fatal("rows leaked")
			}
			if !reflect.DeepEqual(tx.queryArguments, [][]any{{source.TenantID, federation.SourceRemoved, "Before", source.ID, 2}}) {
				t.Fatalf("page scope=%v", tx.queryArguments)
			}
		})
	}
}

// TestFederationLiveSourceSelection tests bounded selection before hydration and
// approved host/audience arguments, without treating SQL text as policy execution.
func TestFederationLiveSourceSelection(t *testing.T) {
	failure := errors.New("source selection failure")
	for _, mode := range []string{"catalog", "live", "referrals"} {
		for _, outcome := range []string{"authenticated", "anonymous", "empty", "overflow", "query", "scan", "iteration", "hydrate", "invalid limit"} {
			t.Run(mode+"/"+outcome, func(t *testing.T) {
				store, tx, ctx, source := federationAdapterFixture(t)
				source.RetrievalMode = federation.RetrievalHybrid
				source.RegistrySearchURL = "https://partner.example.com/ard/v1/search"
				source.Live.Enabled = true
				audience := federation.AudienceAuthenticated
				if outcome == "anonymous" {
					audience = federation.AudienceAnonymous
					source.Live.AnonymousDisclosureAcknowledged = true
					source.Referrals.AnonymousDisclosureAcknowledged = true
				}
				source.DiscoveryAssignments = []federation.DiscoveryAssignment{{Host: "catalog.example.com", Audience: audience}}
				source.Referrals.Enabled = mode == "referrals"
				rows := &identityScriptedRows{values: [][]any{{source.ID}}}
				result := identityQueryResult{rows: rows}
				limit := 1
				code := catalog.ErrorCode("")
				reads := 1
				switch outcome {
				case "empty":
					rows.values = nil
					reads = 0
				case "overflow":
					rows.values = append(rows.values, []any{"00000000-0000-7000-8000-000000000011"})
				case "query":
					result.err = failure
					code = catalog.CodeDependencyUnavailable
					reads = 0
				case "scan":
					rows.values = [][]any{{}}
					code = catalog.CodeDependencyUnavailable
					reads = 0
				case "iteration":
					rows.err = failure
					code = catalog.CodeDependencyUnavailable
					reads = 0
				case "hydrate":
					code = catalog.CodeDependencyUnavailable
				case "invalid limit":
					limit = 0
					code = catalog.CodeInvalidArgument
					reads = 0
				}
				tx.queryResults = []identityQueryResult{result}
				tx.queryRows = []pgx.Row{federationSourceRow(t, source)}
				if outcome == "hydrate" {
					tx.queryRows[0] = scannerFunc(func(...any) error { return failure })
				}
				var got []federation.Source
				var more bool
				var err error
				switch mode {
				case "catalog":
					got, more, err = store.ListCatalogLiveSources(ctx, source.TenantID, source.PolicyDigest, limit)
				case "live":
					got, more, err = store.ListDiscoveryLiveSources(ctx, source.TenantID, "catalog.example.com", audience, source.PolicyDigest, limit)
				case "referrals":
					got, more, err = store.ListDiscoveryReferralSources(ctx, source.TenantID, "catalog.example.com", audience, source.PolicyDigest, limit)
				}
				source.InspectionID = "" // Reads expose configuration, not inspection admission input.
				if code != "" {
					if err == nil || catalog.ErrorCodeOf(err) != code || got != nil || more {
						t.Fatalf("selection=%v %t %v", got, more, err)
					}
				} else {
					if err != nil || got == nil || more != (outcome == "overflow") {
						t.Fatalf("selection=%v %t %v", got, more, err)
					}
					if outcome == "empty" {
						if len(got) != 0 {
							t.Fatal("empty source selection")
						}
					} else if len(got) != 1 || !reflect.DeepEqual(got[0], source) {
						t.Fatalf("hydration=%+v", got)
					}
				}
				if len(tx.queryRowStatements) != reads {
					t.Fatalf("unexpected hydration=%d want=%d", len(tx.queryRowStatements), reads)
				}
				if outcome == "invalid limit" {
					if len(tx.queryStatements) != 0 {
						t.Fatal("invalid limit reached SQL")
					}
					return
				}
				wantArgs := []any{source.TenantID, "catalog.example.com", audience, source.PolicyDigest, 2}
				if mode == "catalog" {
					wantArgs = []any{source.TenantID, source.PolicyDigest, 2}
				}
				if !reflect.DeepEqual(tx.queryArguments, [][]any{wantArgs}) {
					t.Fatalf("selection scope=%v", tx.queryArguments)
				}
				if outcome != "query" && !rows.closed {
					t.Fatal("selection rows leaked")
				}
				if reads > 0 && !reflect.DeepEqual(tx.queryRowArguments, [][]any{{source.TenantID, source.ID}}) {
					t.Fatal("overflow ID was hydrated")
				}
				query := tx.queryStatements[0]
				if mode == "referrals" {
					if !strings.Contains(query, "{referrals,enabled}") {
						t.Fatal("referral selection missing policy")
					}
				} else if !strings.Contains(query, "{live,referralOnly}") {
					t.Fatal("live selection missing referral-only exclusion")
				}
				if mode != "catalog" && outcome == "anonymous" && !strings.Contains(query, "anonymousDisclosureAcknowledged") {
					t.Fatal("anonymous selection lost disclosure constraints")
				}
			})
		}
	}
	store, tx, ctx, source := federationAdapterFixture(t)
	if _, _, err := store.listDiscoverySources(ctx, source.TenantID, "host", federation.AudienceAnonymous, source.PolicyDigest, 1, "arbitrary"); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument || len(tx.queryStatements) != 0 {
		t.Fatalf("unsupported selection mode: %v", err)
	}
}

// TestFederationRetrievalStateRead preserves exact validators and response bytes;
// cache absence remains a normal miss, not a failed ambient command.
func TestFederationRetrievalStateRead(t *testing.T) {
	failure := errors.New("retrieval read failure")
	for _, outcome := range []string{"retained", "missing", "driver"} {
		t.Run(outcome, func(t *testing.T) {
			store, tx, ctx, source := federationAdapterFixture(t)
			snapshot, _ := federationSyncFixture(t, source)
			want := snapshot.RetrievalStates[0]
			want.CredentialRevision = source.CredentialRevision
			want.LastModified = "Wed, 09 Sep 2026 12:00:00 GMT"
			want.ListTerminal = true
			tx.queryRows = []pgx.Row{scriptedValueRow([]any{want.RequestedURL, want.FinalURL, want.RequestIdentityDigest, want.ProfileID, want.PolicyDigest, want.CredentialRevision, want.RepresentationDigest, want.MediaType, want.Body, want.ETag, want.LastModified, want.VaryNames, want.VarySelectionDigest, want.ListTerminal, want.UpdatedAt})}
			if outcome == "missing" {
				tx.queryRows[0] = scannerFunc(func(...any) error { return pgx.ErrNoRows })
			}
			if outcome == "driver" {
				tx.queryRows[0] = scannerFunc(func(...any) error { return failure })
			}
			got, found, err := store.GetRetrievalState(ctx, source.TenantID, source.ID, source.Revision, want.RequestKey)
			switch outcome {
			case "retained":
				if err != nil || !found || !reflect.DeepEqual(got, want) {
					t.Fatalf("retrieval=%+v %t %v", got, found, err)
				}
			case "missing":
				if err != nil || found || !reflect.DeepEqual(got, federation.RetrievalState{}) || commandTransactionFrom(ctx).failed != nil {
					t.Fatalf("cache miss=%+v %t %v", got, found, err)
				}
			case "driver":
				if !errors.Is(err, failure) || found || !reflect.DeepEqual(got, federation.RetrievalState{}) {
					t.Fatalf("failure=%+v %t %v", got, found, err)
				}
			}
			if !reflect.DeepEqual(tx.queryRowArguments, [][]any{{source.TenantID, source.ID, source.Revision, want.RequestKey}}) || len(tx.statements) != 0 {
				t.Fatalf("retrieval scope=%v", tx.queryRowArguments)
			}
		})
	}
}
