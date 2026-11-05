package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/ratings"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

func ratingDecodeTarget() ratings.Target {
	return ratings.Target{Origin: "local", ResourceID: "00000000-0000-7000-8000-000000000001", VersionRevisionID: "00000000-0000-7000-8000-000000000002", Identifier: "urn:air:example.com:agents:helper", VersionPresent: true, Version: "1.0", Kind: catalog.KindAgent}
}

func TestRatingRowDecodingPreservesPrivateIdentityAndRejectsCorruptTargets(t *testing.T) {
	target := ratingDecodeTarget()
	raw, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	values := []any{"rating", "tenant", "private-key", "member", raw, "release", "digest", 4, true, "active", int64(2), now, now}
	r, err := scanRating(scriptedValueRow(values))
	if err != nil || r.Score != 4 || r.Target != target || r.PrincipalKey != "private-key" || r.Revision != 2 {
		t.Fatal(r, err)
	}
	public, err := json.Marshal(r)
	if err != nil || strings.Contains(string(public), "private-key") || strings.Contains(string(public), "member") {
		t.Fatal(string(public), err)
	}
	for _, raw := range [][]byte{[]byte("{"), []byte(`{"origin":"foreign"}`)} {
		values[4] = raw
		if _, err := scanRating(scriptedValueRow(values)); err == nil {
			t.Fatal("corrupt retained target accepted")
		}
	}
	if _, err := scanRating(scannerFunc(func(...any) error { return pgx.ErrNoRows })); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatal(err)
	}
	sharing, err := scanRatingSharing(scriptedValueRow([]any{"choice", "tenant", "catalog.example.com", "anonymous", false, int64(3), now}))
	if err != nil || sharing.Enabled || sharing.Revision != 3 || sharing.UpdatedAt != now {
		t.Fatal(sharing, err)
	}
	if _, err := scanRatingSharing(scannerFunc(func(...any) error { return pgx.ErrNoRows })); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatal(err)
	}
	if _, err := NewPostgresRatingsStore(nil); err == nil {
		t.Fatal("nil repository accepted")
	}
}

func TestRestoreRatingsFindsCorruptionWithoutExposingOpinions(t *testing.T) {
	target := ratingDecodeTarget()
	raw, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	valid := []any{"target:" + ratings.Key(target), raw, false}
	rows := &identityScriptedRows{values: [][]any{valid, {"target:wrong-key", raw, false}, {"vote:missing-history", []byte(nil), true}, {"sharing:valid", []byte(`{"host":"catalog.example.com","audience":"anonymous"}`), false}, {"sharing:unsafe", []byte(`{"host":"evil.example/path","audience":"anonymous"}`), false}, {"sharing:malformed", []byte("{"), false}, {"target:malformed", []byte("{"), false}}}
	tx := &identityScriptedTx{queryResults: []identityQueryResult{{rows: rows}}}
	records, more, err := scanRestoreRatings(t.Context(), tx, "tenant", "", 10)
	if err != nil || more || len(records) != 7 || !rows.closed {
		t.Fatal(records, more, err, rows.closed)
	}
	for i, r := range records {
		want := i != 0 && i != 3
		if (r.finding != nil) != want {
			t.Fatalf("record %d: %+v", i, r)
		}
		if r.finding != nil {
			encoded, err := json.Marshal(r.finding)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "score") || strings.Contains(string(encoded), "principal") {
				t.Fatal(string(encoded))
			}
		}
	}
	limited := &identityScriptedRows{values: [][]any{valid, valid}}
	tx.queryResults = []identityQueryResult{{rows: limited}}
	records, more, err = scanRestoreRatings(t.Context(), tx, "tenant", "", 1)
	if err != nil || !more || len(records) != 1 || !limited.closed {
		t.Fatal(records, more, err)
	}
	for _, result := range []identityQueryResult{{err: errors.New("query unavailable")}, {rows: &identityScriptedRows{values: [][]any{{"bad shape"}}}}, {rows: &identityScriptedRows{err: errors.New("iteration failed")}}} {
		tx.queryResults = []identityQueryResult{result}
		if _, _, err := scanRestoreRatings(t.Context(), tx, "tenant", "", 1); err == nil {
			t.Fatal("driver failure lost")
		}
	}
}

func TestPublicationRatingConsentRechecksChoiceAndExactTotals(t *testing.T) {
	claim := &ratings.Summary{Count: 5, Sum: 19}
	snapshot := publication.Snapshot{TenantID: "tenant", Host: "catalog.example.com", Audience: "anonymous"}
	if err := checkPublicationRatingConsent(t.Context(), &identityScriptedTx{}, snapshot); err != nil {
		t.Fatal("unrated snapshot queried sharing", err)
	}
	snapshot.ARD.Entries = append(snapshot.ARD.Entries, publication.Entry{Ratings: claim})
	if err := checkPublicationRatingConsent(t.Context(), &identityScriptedTx{}, snapshot); catalog.ErrorCodeOf(err) != catalog.CodePublicationNotReady {
		t.Fatal(err)
	}
	snapshot.ARD.Entries[0].Metadata = map[string]any{"catalogReleaseId": "release"}
	for name, tc := range map[string]struct {
		enabled bool
		row     pgx.Row
		result  identityQueryResult
		want    bool
	}{
		"eligible":        {true, nil, identityQueryResult{rows: &identityScriptedRows{values: [][]any{{"release", int64(5), int64(19)}}}}, true},
		"disabled":        {false, nil, identityQueryResult{}, false},
		"missing choice":  {false, scannerFunc(func(...any) error { return pgx.ErrNoRows }), identityQueryResult{}, false},
		"withdrawn":       {true, nil, identityQueryResult{rows: &identityScriptedRows{values: [][]any{{"release", int64(4), int64(15)}}}}, false},
		"edited":          {true, nil, identityQueryResult{rows: &identityScriptedRows{values: [][]any{{"release", int64(5), int64(20)}}}}, false},
		"missing target":  {true, nil, identityQueryResult{rows: &identityScriptedRows{}}, false},
		"wrong release":   {true, nil, identityQueryResult{rows: &identityScriptedRows{values: [][]any{{"other", int64(5), int64(19)}}}}, false},
		"query error":     {true, nil, identityQueryResult{err: errors.New("query failed")}, false},
		"scan error":      {true, nil, identityQueryResult{rows: &identityScriptedRows{values: [][]any{{"bad shape"}}}}, false},
		"iteration error": {true, nil, identityQueryResult{rows: &identityScriptedRows{err: errors.New("iteration failed")}}, false},
	} {
		t.Run(name, func(t *testing.T) {
			row := tc.row
			if row == nil {
				row = scriptedValueRow([]any{tc.enabled})
			}
			tx := &identityScriptedTx{queryRows: []pgx.Row{row}, queryResults: []identityQueryResult{tc.result}}
			err := checkPublicationRatingConsent(t.Context(), tx, snapshot)
			if (err == nil) != tc.want {
				t.Fatal(err)
			}
		})
	}
}

func TestPublicationRatingConsentChecksAICatalogExtensionShape(t *testing.T) {
	for name, value := range map[string]any{
		"valid":            map[string]any{"count": float64(5), "sum": float64(19)},
		"unencodable":      func() {},
		"wrong field type": map[string]any{"count": "five"},
		"missing release":  map[string]any{"count": float64(5), "sum": float64(19)},
	} {
		t.Run(name, func(t *testing.T) {
			entry := publication.AICatalogEntry{Extensions: map[string]any{ratings.IRI: value}}
			if name != "missing release" {
				entry.Extensions["https://ai-catalog.org/extensions/metadata"] = map[string]any{"catalogReleaseId": "release"}
			}
			snapshot := publication.Snapshot{TenantID: "tenant", Host: "catalog.example.com", Audience: "anonymous", AICatalog: publication.AICatalog{Entries: []publication.AICatalogEntry{entry}}}
			rows := &identityScriptedRows{values: [][]any{{"release", int64(5), int64(19)}}}
			tx := &identityScriptedTx{queryRows: []pgx.Row{scriptedValueRow([]any{true})}, queryResults: []identityQueryResult{{rows: rows}}}
			err := checkPublicationRatingConsent(t.Context(), tx, snapshot)
			if (err == nil) != (name == "valid") {
				t.Fatal(err)
			}
		})
	}
}

// These tests exercise driver decoding and failure propagation. SQL eligibility,
// row locks, rollup atomicity and RLS are owned by ratings_integration_test.go.
func TestRatingsReadProjectionsBoundInputsAndPreserveDriverFailures(t *testing.T) {
	const tenant = "00000000-0000-7000-8000-000000000001"
	pool := &pgxpool.Pool{}
	repository, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresRatingsStore(repository)
	if err != nil {
		t.Fatal(err)
	}
	target := ratingDecodeTarget()
	raw, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	record := []any{"rating", tenant, "private-key", "member", raw, "release", "digest", 4, false, "active", int64(1), now, now}
	for _, method := range []string{"get", "own", "list", "aggregates", "sharing"} {
		for _, failure := range []string{"none", "query", "scan", "iteration"} {
			if (method == "get" || method == "own" || method == "sharing") && (failure == "query" || failure == "iteration") {
				continue
			}
			t.Run(method+"/"+failure, func(t *testing.T) {
				values := record
				if method == "aggregates" {
					values = []any{ratings.Key(target), int64(1), int64(4), int64(0), int64(0)}
				}
				if method == "sharing" {
					values = []any{"choice", tenant, "catalog.example.com", "anonymous", true, int64(1), now}
				}
				if failure == "scan" {
					values = []any{"invalid driver row"}
				}
				rows := &identityScriptedRows{values: [][]any{values}}
				result := identityQueryResult{rows: rows}
				if failure == "query" {
					result.err = errors.New("query failed")
				}
				if failure == "iteration" {
					rows.err = errors.New("read interrupted")
				}
				tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scriptedValueRow(values)}, queryResults: []identityQueryResult{result}}
				ctx := identityRepositoryContext(pool, tx)
				var err error
				switch method {
				case "get":
					var got ratings.Record
					got, err = store.Get(ctx, tenant, "rating")
					if err == nil && got.Score != 4 {
						t.Fatal(got)
					}
				case "own":
					var got ratings.Record
					got, err = store.Own(ctx, tenant, target, "private-key")
					if err == nil && got.PrincipalKey != "private-key" {
						t.Fatal(got)
					}
				case "list":
					var got []ratings.Record
					got, err = store.List(ctx, tenant, ratings.ListInput{Limit: 25})
					if err == nil && (len(got) != 1 || got[0].Score != 4) {
						t.Fatal(got)
					}
				case "aggregates":
					var got map[string]ratings.Aggregate
					got, err = store.Aggregates(ctx, tenant, []ratings.Target{target})
					if err == nil && got[ratings.Key(target)].Average() != 4 {
						t.Fatal(got)
					}
				case "sharing":
					var got ratings.Sharing
					got, err = store.GetSharing(ctx, tenant, "catalog.example.com", "anonymous")
					if err == nil && !got.Enabled {
						t.Fatal(got)
					}
				}
				if (err == nil) != (failure == "none") {
					t.Fatal(err)
				}
				if (method == "list" || method == "aggregates") && failure != "query" && !rows.closed {
					t.Fatal("rows not closed")
				}
			})
		}
	}
	for _, limit := range []int{0, 102} {
		if _, err := store.List(t.Context(), tenant, ratings.ListInput{Limit: limit}); err == nil {
			t.Fatal("invalid page")
		}
	}
	if _, err := store.Aggregates(t.Context(), tenant, make([]ratings.Target, catalog.MaxPublicationRecords+1)); err == nil {
		t.Fatal("unbounded aggregate request")
	}
	if _, err := store.Aggregates(t.Context(), tenant, []ratings.Target{{}}); err == nil {
		t.Fatal("malformed aggregate target")
	}
	tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{{rows: &identityScriptedRows{values: [][]any{{ratings.Key(target), int64(1), int64(6), int64(0), int64(0)}}}}}}
	if _, err := store.Aggregates(identityRepositoryContext(pool, tx), tenant, []ratings.Target{target}); err == nil {
		t.Fatal("impossible totals admitted")
	}
}

func TestRatingSignalProjectionOmitsMalformedOptionalClaims(t *testing.T) {
	const tenant = "00000000-0000-7000-8000-000000000001"
	pool := &pgxpool.Pool{}
	repository, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresFederationStore(repository)
	if err != nil {
		t.Fatal(err)
	}
	target := ratings.Target{Origin: "foreign", SourceID: tenant, PresentationGeneration: 1, Identifier: "urn:air:example.com:agents:helper", Kind: catalog.KindAgent}
	raw, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	peer := []byte(`{"reporter":"https://peer.example","identifier":"urn:air:example.com:agents:helper","versionPresent":false,"minimum":1,"maximum":5,"count":5,"sum":20,"measuredAt":"2026-10-08T00:00:00Z"}`)
	resource, changed := "authorized-import", true
	for _, failure := range []string{"none", "bad peer", "bad target", "query", "scan", "iteration"} {
		t.Run(failure, func(t *testing.T) {
			values := []any{raw, "https://peer.example/ard", peer, &resource, &changed}
			if failure == "bad peer" {
				values[2] = []byte(`{"score":5}`)
			}
			if failure == "bad target" {
				values[0] = []byte("{")
			}
			if failure == "scan" {
				values = []any{"bad shape"}
			}
			rows := &identityScriptedRows{values: [][]any{values}}
			result := identityQueryResult{rows: rows}
			if failure == "query" {
				result.err = errors.New("query failed")
			}
			if failure == "iteration" {
				rows.err = errors.New("iteration failed")
			}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{result}}
			got, err := store.RatingSignals(identityRepositoryContext(pool, tx), tenant, []federation.RatingSignalTarget{{Target: target, EntryDigest: "exact-digest"}})
			valid := failure == "none" || failure == "bad peer"
			if (err == nil) != valid {
				t.Fatal(err)
			}
			if valid {
				signal := got[ratings.Key(target)]
				if signal.ImportedResourceID != resource || !signal.ChangedEntry || (signal.Peer != nil) != (failure == "none") {
					t.Fatal(signal)
				}
			}
			if failure != "query" && !rows.closed {
				t.Fatal("rows not closed")
			}
		})
	}
	if _, err := store.RatingSignals(t.Context(), tenant, make([]federation.RatingSignalTarget, 101)); err == nil {
		t.Fatal("unbounded signal query")
	}
}

func TestRatingWriteAdmissionRejectsStaleHumanAndReleaseEvidence(t *testing.T) {
	const tenant = "00000000-0000-7000-8000-000000000001"
	actor := identity.Actor{TenantID: tenant, Issuer: "https://issuer.example", Subject: "alice", MembershipID: tenant, ActorType: identity.ActorHuman}
	ctx := identityfixture.WithActor(t.Context(), actor)
	key, err := ratings.PrincipalKey(actor)
	if err != nil {
		t.Fatal(err)
	}
	record := ratings.Record{TenantID: tenant, SubmittedBy: actor.MembershipID, PrincipalKey: key, Target: ratingDecodeTarget(), ReleaseID: tenant}
	admission := ratings.Admission{Target: record.Target, NamespaceID: tenant}
	for name, tc := range map[string]struct {
		active     bool
		memberRow  pgx.Row
		releaseRow pgx.Row
		change     func(*ratings.Record, *ratings.Admission)
		want       bool
	}{
		"current":             {true, nil, scriptedValueRow([]any{tenant}), nil, true},
		"inactive":            {false, nil, nil, nil, false},
		"membership failure":  {false, scannerFunc(func(...any) error { return errors.New("member unavailable") }), nil, nil, false},
		"different principal": {true, nil, nil, func(r *ratings.Record, _ *ratings.Admission) { r.PrincipalKey = "other" }, false},
		"different member":    {true, nil, nil, func(r *ratings.Record, _ *ratings.Admission) { r.SubmittedBy = "other" }, false},
		"changed target":      {true, nil, nil, func(_ *ratings.Record, a *ratings.Admission) { a.Target.Version = "other" }, false},
		"withdrawn release":   {true, nil, scannerFunc(func(...any) error { return pgx.ErrNoRows }), nil, false},
		"changed namespace":   {true, nil, scriptedValueRow([]any{"other"}), nil, false},
		"release failure":     {true, nil, scannerFunc(func(...any) error { return errors.New("release unavailable") }), nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			r, a := record, admission
			if tc.change != nil {
				tc.change(&r, &a)
			}
			member := tc.memberRow
			if member == nil {
				member = scriptedValueRow([]any{tc.active})
			}
			tx := &identityScriptedTx{queryRows: []pgx.Row{member, tc.releaseRow}}
			err := admitRatingWrite(ctx, tx, r, a)
			if (err == nil) != tc.want {
				t.Fatal(err)
			}
		})
	}
	if err := admitRatingWrite(t.Context(), &identityScriptedTx{}, record, admission); err == nil {
		t.Fatal("missing human")
	}
}

func TestForeignRatingAdmissionFencesCurrentSourceAndObservation(t *testing.T) {
	for _, mode := range []string{"indexed current", "indexed source error", "live current", "source revision", "presentation generation", "removed source", "live disabled", "live source error", "observation changed"} {
		t.Run(mode, func(t *testing.T) {
			_, tx, ctx, source := federationAdapterFixture(t)
			actor := authorizationUnitActor()
			actor.Issuer = "https://issuer.example"
			key, err := ratings.PrincipalKey(actor)
			if err != nil {
				t.Fatal(err)
			}
			ctx = identityfixture.WithActor(ctx, actor)
			target := ratings.Target{Origin: "foreign", SourceID: source.ID, PresentationGeneration: 1, Identifier: "urn:air:peer.example:agents:helper", Kind: catalog.KindAgent}
			r := ratings.Record{TenantID: source.TenantID, PrincipalKey: key, SubmittedBy: actor.MembershipID, Target: target}
			a := ratings.Admission{Target: target, SourceRevision: source.Revision, EntryDigest: "sha256:" + strings.Repeat("b", 64), Live: strings.HasPrefix(mode, "live")}
			if a.Live {
				source.RetrievalMode = federation.RetrievalHybrid
				source.RegistrySearchURL = "https://partner.example.com/search"
				source.Live = federation.LivePolicy{Enabled: true, QueryPrivacyAcknowledged: true, FederationMode: "none"}
				source.DiscoveryAssignments = []federation.DiscoveryAssignment{{Host: "catalog.example.com", Audience: federation.AudienceAuthenticated}}
			}
			switch mode {
			case "indexed source error", "live source error":
				source.Status = federation.SourceError
			case "source revision":
				source.Revision++
			case "presentation generation":
				source.Revision++
				source.Endpoint = "https://partner.example.com/replacement-catalog.json"
				source.PresentationGeneration = 2
			case "removed source":
				source.Revision++
				source.Status = federation.SourceRemoved
			case "live disabled":
				source.Revision++
				source.RetrievalMode = federation.RetrievalIndexed
				source.Live = federation.LivePolicy{}
			}
			// Administrative changes advance the source revision. These cases
			// prove stale-admission refusal without isolating later state checks.
			tx.queryRows = []pgx.Row{scriptedValueRow([]any{true}), federationSourceRow(t, source), scriptedValueRow([]any{mode != "observation changed"})}
			err = admitRatingWrite(ctx, tx, r, a)
			valid := mode == "indexed current" || mode == "indexed source error" || mode == "live current"
			if valid && err != nil || !valid && catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
				t.Fatalf("foreign rating admission=%v", err)
			}
			wantReads := 2
			if mode == "indexed current" || mode == "indexed source error" || mode == "observation changed" {
				wantReads = 3
				if !reflect.DeepEqual(tx.queryRowArguments[2], []any{source.TenantID, source.ID, target.Identifier, false, "", target.Kind, a.EntryDigest}) {
					t.Fatal("rating observation lost exact source, identity or digest")
				}
			}
			if len(tx.queryRowStatements) != wantReads || len(tx.statements) != 0 {
				t.Fatal("stale source checked observations or admission mutated data")
			}
		})
	}
}

func TestRatingSharingSavePreservesRevisionAndAuditFailure(t *testing.T) {
	const tenant = "00000000-0000-7000-8000-000000000001"
	pool := &pgxpool.Pool{}
	repository, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresRatingsStore(repository)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	for _, expected := range []int64{0, 1} {
		for _, failure := range []string{"none", "read", "revision", "write", "audit"} {
			if expected == 0 && failure == "read" {
				continue
			}
			t.Run(fmt.Sprintf("revision-%d/%s", expected, failure), func(t *testing.T) {
				r := ratings.Sharing{ID: tenant, TenantID: tenant, Host: "catalog.example.com", Audience: "anonymous", Enabled: true, Revision: expected + 1, UpdatedAt: now}
				e, err := audit.NewRecord("00000000-0000-7000-8000-000000000099", tenant, audit.Principal{ID: "operator", Type: audit.PrincipalServiceExecutor}, "rating.sharing", "rating_sharing", r.ID, "Explicit audience opt-in", audit.Execution{}, map[string]any{"revision": r.Revision, "enabled": r.Enabled}, now)
				if err != nil {
					t.Fatal(err)
				}
				rows := []pgx.Row{scriptedValueRow([]any{r.ID, r.TenantID, r.Host, r.Audience, false, expected, now})}
				if failure == "read" {
					rows = []pgx.Row{scannerFunc(func(...any) error { return pgx.ErrNoRows })}
				}
				if failure == "revision" {
					r.Revision++
				}
				var failures []error
				if failure == "write" {
					failures = []error{errors.New("write failed")}
				}
				if failure == "audit" {
					failures = []error{nil, errors.New("audit unavailable")}
				}
				tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{execErrors: failures}, queryRows: rows}
				got, err := store.SaveSharing(identityRepositoryContext(pool, tx), r, expected, e)
				if (err == nil) != (failure == "none") {
					t.Fatal(got, err)
				}
				if err == nil && (got.Revision != expected+1 || !got.Enabled) {
					t.Fatal(got)
				}
				if failure == "audit" && len(tx.statements) != 2 {
					t.Fatal("audit was not attempted after choice write")
				}
			})
		}
	}
}
