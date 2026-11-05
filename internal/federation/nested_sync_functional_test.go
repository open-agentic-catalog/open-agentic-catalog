//go:build functional

package federation

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/events"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

func connectNestedHTTPSSource(t *testing.T, ctx context.Context, manager *Manager, origin string) Source {
	t.Helper()
	inspection, err := manager.InspectWithAccess(ctx, InspectionInput{URL: origin + "/root.json", NetworkProfile: "owned-https"})
	if err != nil {
		t.Fatal(err)
	}
	source, err := manager.Connect(ctx, ConnectInput{
		InspectionID: inspection.ID, DisplayName: "Nested HTTPS Catalog", UpdateMode: UpdateManual,
		Scope:   Scope{Publishers: []string{"remote.example"}},
		Nesting: NestingPolicy{Enabled: true, MaxDepth: 4, AllowedOrigins: []string{origin}},
		Reason:  "Approve the inspected source",
	})
	if err != nil {
		t.Fatal(err)
	}
	return source
}

// Exercise the synchronization operation with valid job attribution, without
// claiming worker scheduling or PostgreSQL transaction coverage.
func syncNestedHTTPSSource(t *testing.T, ctx context.Context, manager *Manager, source Source, evidence *recordingFederationEvidence) (Snapshot, error) {
	t.Helper()
	jobID, err := (id.Generator{}).New(manager.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	ctx = audit.WithJobExecution(ctx, audit.JobExecution{ID: jobID, Kind: "source.sync", Attempt: 1, Initiator: auditEvidenceMembershipID, Trigger: "manual"})
	auditCount, outboxCount := len(evidence.audit), len(evidence.outbox)
	snapshot, syncErr := manager.SyncAtRevision(ctx, source.ID, source.Revision)
	if ctx.Err() != nil {
		t.Fatal(ctx.Err())
	}
	if len(evidence.audit) != auditCount+1 || len(evidence.outbox) != outboxCount+1 {
		t.Fatalf("synchronization did not retain one audit and notification: audit=%d, outbox=%d, err=%v", len(evidence.audit)-auditCount, len(evidence.outbox)-outboxCount, syncErr)
	}
	wantAction := audit.ActionSourceSyncCompleted
	if syncErr != nil {
		wantAction = audit.ActionSourceSyncFailed
	}
	record := evidence.audit[auditCount]
	rawFacts, err := json.Marshal(auditFacts(t, record))
	if err != nil {
		t.Fatal(err)
	}
	var facts sourceSyncFacts
	if err := json.Unmarshal(rawFacts, &facts); err != nil {
		t.Fatal(err)
	}
	if record.Action != wantAction || record.TenantID != source.TenantID || record.TargetID != snapshot.ID || facts.SourceID != source.ID || facts.JobID != jobID || facts.JobKind != "source.sync" || facts.JobAttempt != 1 || facts.Withdrawn != snapshot.Withdrawn {
		t.Fatalf("synchronization attribution or outcome = %#v", record)
	}
	var notification events.NotificationReference
	if err := json.Unmarshal(evidence.outbox[outboxCount].Data, &notification); err != nil {
		t.Fatal(err)
	}
	if notification.TargetID != snapshot.ID || notification.Action != wantAction || notification.Digest != record.AfterDigest {
		t.Fatalf("synchronization notification = %#v", notification)
	}
	return snapshot, syncErr
}

func nestedHTTPSCatalogEntry(name, endpoint string) string {
	return fmt.Sprintf(`{"identifier":"urn:air:remote.example:catalog:%s","displayName":%q,"type":"application/ai-catalog+json","url":%q}`, name, name, endpoint)
}

func nestedHTTPSAgentDocument(name string) string {
	return fmt.Sprintf(`{"specVersion":"1.0","entries":[{"identifier":"urn:air:remote.example:default:%s","displayName":%q,"type":"application/a2a-agent-card+json","data":{"name":%q}}]}`, name, name, name)
}

func TestNestedSyncHTTPSDeduplicatesRemoteDocumentsAndRedirectAliases(t *testing.T) {
	rootDocument := func(origin string) string {
		entries := make([]string, 0, 6)
		for _, entry := range []struct{ name, path string }{
			{"first", "/branch.json"}, {"same-url", "/branch.json"}, {"mirror", "/mirror.json"},
			{"seen-alias", "/seen-alias.json"}, {"new-alias", "/new-alias.json"}, {"canonical", "/canonical.json"},
		} {
			entries = append(entries, nestedHTTPSCatalogEntry(entry.name, origin+entry.path))
		}
		return `{"entries":[` + strings.Join(entries, ",") + `]}`
	}
	var mu sync.Mutex
	var requests []string
	server, outbound := newFederationHTTPSOrigin(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		mu.Unlock()
		if r.TLS == nil || r.Method != http.MethodGet {
			t.Errorf("nested retrieval was not an HTTPS GET: %s", r.Method)
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		origin := "https://" + r.Host
		w.Header().Set("Content-Type", aiCatalogDocumentMediaType)
		switch r.URL.Path {
		case "/root.json":
			w.Header().Set("Content-Type", ardDocumentMediaType)
			_, _ = io.WriteString(w, rootDocument(origin))
		case "/branch.json", "/mirror.json":
			_, _ = io.WriteString(w, `{"specVersion":"1.0","entries":[`+nestedHTTPSCatalogEntry("leaf", origin+"/leaf.json")+`]}`)
		case "/leaf.json":
			_, _ = io.WriteString(w, nestedHTTPSAgentDocument("leaf-agent"))
		case "/canonical.json":
			_, _ = io.WriteString(w, nestedHTTPSAgentDocument("canonical-agent"))
		case "/seen-alias.json":
			http.Redirect(w, r, "/branch.json", http.StatusFound)
		case "/new-alias.json":
			http.Redirect(w, r, "/canonical.json", http.StatusFound)
		default:
			t.Errorf("unexpected nested retrieval: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	evidence := &recordingFederationEvidence{}
	store := NewMemoryStore(evidence)
	manager, err := NewManager(store, outbound, clock.NewFake(time.Date(2026, time.September, 22, 12, 0, 0, 0, time.UTC)), id.Generator{}, WithEvidenceSink(evidence, true))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(auditEvidenceContext(), 15*time.Second)
	defer cancel()
	source := connectNestedHTTPSSource(t, ctx, manager, server.URL)
	mu.Lock()
	requests = nil // Inspection is separate from the traversal being measured.
	mu.Unlock()
	snapshot, err := syncNestedHTTPSSource(t, ctx, manager, source, evidence)
	if err != nil || snapshot.State != SnapshotComplete || !snapshot.EnumerationComplete || !snapshot.WithdrawalAuthority || snapshot.MemberCount != 9 || snapshot.Withdrawn != 0 || snapshot.BodyDigest != digestText(rootDocument(server.URL)) {
		t.Fatalf("duplicate traversal snapshot = %#v, err=%v", snapshot, err)
	}
	if !hasFinding(snapshot.Findings, "NESTED_CATALOG_CYCLE") || hasFinding(snapshot.Findings, "NESTED_CATALOG_INCOMPLETE") {
		t.Fatalf("duplicate traversal findings = %#v", snapshot.Findings)
	}
	server.Close()
	mu.Lock()
	gotRequests := slices.Clone(requests)
	mu.Unlock()
	// A redirect can contact an already visited target. Its descendants must not
	// be traversed again, and a new redirect target becomes visited too.
	wantRequests := []string{"GET /root.json", "GET /branch.json", "GET /leaf.json", "GET /mirror.json", "GET /seen-alias.json", "GET /branch.json", "GET /new-alias.json", "GET /canonical.json"}
	if !slices.Equal(gotRequests, wantRequests) {
		t.Fatalf("nested traversal requests = %v, want %v", gotRequests, wantRequests)
	}
	if len(snapshot.Retrievals) != 6 {
		t.Fatalf("retrieval evidence count = %d, want 6", len(snapshot.Retrievals))
	}
	for index, endpoints := range map[int][2]string{4: {"/seen-alias.json", "/branch.json"}, 5: {"/new-alias.json", "/canonical.json"}} {
		retrieval := snapshot.Retrievals[index]
		if retrieval.RequestedURLDigest != digestText(server.URL+endpoints[0]) || retrieval.FinalURLDigest != digestText(server.URL+endpoints[1]) || retrieval.Status != http.StatusOK || !retrieval.ReceivedBody || retrieval.Reused {
			t.Fatalf("redirect provenance = %#v", retrieval)
		}
	}
	current, err := manager.ObservationPage(ctx, ObservationListRequest{SourceID: source.ID, CurrentOnly: true, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	gotIdentifiers := make([]string, 0, len(current))
	for _, observation := range current {
		gotIdentifiers = append(gotIdentifiers, observation.Identifier)
		if observation.Freshness != FreshCurrent || observation.SnapshotID != snapshot.ID || !observation.RawEntryAvailable || len(observation.RawEntry) != 0 {
			t.Fatalf("retained nested observation = %#v", observation)
		}
		retained, err := manager.GetObservation(ctx, observation.ID)
		if err != nil || len(retained.RawEntry) == 0 || retained.ExactEntryDigest != observation.ExactEntryDigest {
			t.Fatalf("retained nested exact evidence lost: %v", err)
		}
	}
	wantIdentifiers := []string{
		"urn:air:remote.example:catalog:first", "urn:air:remote.example:catalog:same-url", "urn:air:remote.example:catalog:mirror",
		"urn:air:remote.example:catalog:seen-alias", "urn:air:remote.example:catalog:new-alias", "urn:air:remote.example:catalog:canonical",
		"urn:air:remote.example:catalog:leaf", "urn:air:remote.example:default:leaf-agent", "urn:air:remote.example:default:canonical-agent",
	}
	slices.Sort(gotIdentifiers)
	slices.Sort(wantIdentifiers)
	if !slices.Equal(gotIdentifiers, wantIdentifiers) {
		t.Fatalf("retained membership = %v, want %v", gotIdentifiers, wantIdentifiers)
	}
	storedSource, err := manager.GetSource(ctx, source.ID)
	if err != nil || storedSource.Status != SourceActive || storedSource.LastCompleteSnapshotID != snapshot.ID {
		t.Fatalf("source after duplicate traversal = %#v, err=%v", storedSource, err)
	}
}

func TestNestedSyncHTTPSRejectsInvalidChildAndRecovers(t *testing.T) {
	for _, tc := range []struct{ name, mediaType, body string }{
		{"malformed JSON", aiCatalogDocumentMediaType, `{"specVersion":"1.0","entries":[`},
		{"contradictory profile", ardDocumentMediaType, nestedHTTPSAgentDocument("unaccepted")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var phase atomic.Int32
			var mu sync.Mutex
			type request struct{ path, etag, modifiedSince string }
			var requests []request
			server, outbound := newFederationHTTPSOrigin(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				requests = append(requests, request{r.URL.Path, r.Header.Get("If-None-Match"), r.Header.Get("If-Modified-Since")})
				mu.Unlock()
				if r.TLS == nil || r.Method != http.MethodGet {
					t.Errorf("nested retrieval was not an HTTPS GET: %s", r.Method)
					http.Error(w, "unexpected request", http.StatusBadRequest)
					return
				}
				switch r.URL.Path {
				case "/root.json":
					w.Header().Set("Content-Type", ardDocumentMediaType)
					w.Header().Set("ETag", `"root-v1"`)
					if r.Header.Get("If-None-Match") == `"root-v1"` {
						w.WriteHeader(http.StatusNotModified)
						return
					}
					_, _ = io.WriteString(w, `{"entries":[`+nestedHTTPSCatalogEntry("child", "https://"+r.Host+"/child.json")+`]}`)
				case "/child.json":
					w.Header().Set("Content-Type", aiCatalogDocumentMediaType)
					switch phase.Load() {
					case 0:
						w.Header().Set("ETag", `"child-v1"`)
						_, _ = io.WriteString(w, nestedHTTPSAgentDocument("before"))
					case 1:
						w.Header().Set("Content-Type", tc.mediaType)
						w.Header().Set("ETag", `"child-invalid"`)
						_, _ = io.WriteString(w, tc.body)
					case 2:
						w.Header().Set("ETag", `"child-v2"`)
						_, _ = io.WriteString(w, nestedHTTPSAgentDocument("after"))
					}
				default:
					t.Errorf("unexpected nested retrieval: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			evidence := &recordingFederationEvidence{}
			store := NewMemoryStore(evidence)
			fakeClock := clock.NewFake(time.Date(2026, time.September, 22, 12, 0, 0, 0, time.UTC))
			manager, err := NewManager(store, outbound, fakeClock, id.Generator{}, WithEvidenceSink(evidence, true))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(auditEvidenceContext(), 15*time.Second)
			defer cancel()
			source := connectNestedHTTPSSource(t, ctx, manager, server.URL)
			mu.Lock()
			requests = nil
			mu.Unlock()
			baseline, err := syncNestedHTTPSSource(t, ctx, manager, source, evidence)
			if err != nil || baseline.State != SnapshotComplete || !baseline.EnumerationComplete || !baseline.WithdrawalAuthority || baseline.CompletedAt == nil || baseline.MemberCount != 2 || baseline.Withdrawn != 0 || len(baseline.RetrievalStates) != 2 {
				t.Fatalf("complete baseline = %#v, err=%v", baseline, err)
			}
			// Detach expected evidence from the memory store's shared slices and
			// completion pointer so an in-place rewrite cannot change both sides.
			completedAt := *baseline.CompletedAt
			baseline.CompletedAt = &completedAt
			baseline.Findings = slices.Clone(baseline.Findings)
			baseline.Retrievals = slices.Clone(baseline.Retrievals)
			baseline.RetrievalInvalidations = slices.Clone(baseline.RetrievalInvalidations)
			baseline.RetrievalStates = slices.Clone(baseline.RetrievalStates)
			for index := range baseline.RetrievalStates {
				baseline.RetrievalStates[index].Body = slices.Clone(baseline.RetrievalStates[index].Body)
				baseline.RetrievalStates[index].VaryNames = slices.Clone(baseline.RetrievalStates[index].VaryNames)
			}
			var rootState, childState RetrievalState
			for _, state := range baseline.RetrievalStates {
				switch state.RequestedURL {
				case server.URL + "/root.json":
					rootState = state
				case server.URL + "/child.json":
					childState = state
				}
			}
			if rootState.ETag != `"root-v1"` || childState.ETag != `"child-v1"` || childState.RepresentationDigest != digestText(nestedHTTPSAgentDocument("before")) {
				t.Fatalf("baseline validators = root %#v, child %#v", rootState, childState)
			}
			persistedRoot, found, err := store.GetRetrievalState(ctx, source.TenantID, source.ID, source.Revision, rootState.RequestKey)
			if err != nil || !found || persistedRoot.ETag != rootState.ETag || persistedRoot.RepresentationDigest != rootState.RepresentationDigest {
				t.Fatalf("parent validator was not persisted: found=%v, err=%v", found, err)
			}
			before, err := manager.ObservationPage(ctx, ObservationListRequest{SourceID: source.ID, CurrentOnly: true, Limit: 10})
			if err != nil || len(before) != 2 {
				t.Fatalf("baseline membership = %#v, err=%v", before, err)
			}
			for index := range before {
				if len(before[index].RawEntry)+len(before[index].NormalizedEntry) != 0 {
					t.Fatal("metadata page hydrated documents")
				}
				before[index], err = manager.GetObservation(ctx, before[index].ID)
				if err != nil {
					t.Fatal(err)
				}
				before[index].RawEntry = slices.Clone(before[index].RawEntry)
				before[index].NormalizedEntry = slices.Clone(before[index].NormalizedEntry)
				before[index].Compatibility.Findings = slices.Clone(before[index].Compatibility.Findings)
				before[index].SearchTags = slices.Clone(before[index].SearchTags)
				before[index].SearchQueries = slices.Clone(before[index].SearchQueries)
			}
			phase.Store(1)
			fakeClock.Advance(time.Minute)
			partial, err := syncNestedHTTPSSource(t, ctx, manager, source, evidence)
			if catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || partial.State != SnapshotIncomplete || partial.EnumerationComplete || partial.WithdrawalAuthority || partial.Withdrawn != 0 || !hasFinding(partial.Findings, "NESTED_CATALOG_INCOMPLETE") || len(partial.RetrievalStates) != 0 || !slices.Equal(partial.RetrievalInvalidations, []string{childState.RequestKey}) {
				t.Fatalf("invalid child outcome = %#v, err=%v", partial, err)
			}
			current, err := manager.ObservationPage(ctx, ObservationListRequest{SourceID: source.ID, CurrentOnly: true, Limit: 10})
			if err != nil || len(current) != len(before) {
				t.Fatalf("membership after rejected child = %#v, err=%v", current, err)
			}
			for index, observation := range current {
				if len(observation.RawEntry)+len(observation.NormalizedEntry) != 0 {
					t.Fatal("metadata page hydrated documents")
				}
				observation, err = manager.GetObservation(ctx, observation.ID)
				if err != nil {
					t.Fatal(err)
				}
				// Seeing the unchanged parent again can advance LastSeenAt; its
				// accepted content and current membership must remain unchanged.
				if observation.LastSeenAt.Before(before[index].LastSeenAt) {
					t.Fatalf("rejected child moved observation %s backwards in time", observation.ID)
				}
				observation.LastSeenAt = before[index].LastSeenAt
				if !reflect.DeepEqual(observation, before[index]) || observation.Freshness != FreshCurrent {
					t.Fatalf("rejected child changed current observation: got %#v, want %#v", observation, before[index])
				}
			}
			storedSource, err := manager.GetSource(ctx, source.ID)
			if err != nil || storedSource.Status != SourceError || storedSource.Revision != source.Revision || storedSource.LastCompleteSnapshotID != baseline.ID {
				t.Fatalf("source after rejected child = %#v, err=%v", storedSource, err)
			}
			if state, found, err := store.GetRetrievalState(ctx, source.TenantID, source.ID, source.Revision, childState.RequestKey); err != nil || found {
				t.Fatalf("rejected child retained a validator: %#v, found=%v, err=%v", state, found, err)
			}
			if state, found, err := store.GetRetrievalState(ctx, source.TenantID, source.ID, source.Revision, rootState.RequestKey); err != nil || !found || !reflect.DeepEqual(state, persistedRoot) {
				t.Fatalf("incomplete synchronization replaced parent state: %#v, found=%v, err=%v", state, found, err)
			}
			if saved, err := manager.GetSnapshot(ctx, baseline.ID); err != nil || !reflect.DeepEqual(saved, baseline) {
				t.Fatalf("complete snapshot was rewritten: %#v, err=%v", saved, err)
			}
			phase.Store(2)
			fakeClock.Advance(time.Minute)
			recovered, err := syncNestedHTTPSSource(t, ctx, manager, source, evidence)
			if err != nil || recovered.State != SnapshotComplete || !recovered.EnumerationComplete || !recovered.WithdrawalAuthority || recovered.MemberCount != 2 || recovered.Withdrawn != 1 {
				t.Fatalf("recovered snapshot = %#v, err=%v", recovered, err)
			}
			storedSource, err = manager.GetSource(ctx, source.ID)
			if err != nil || storedSource.Status != SourceActive || storedSource.LastError != "" || storedSource.Revision != source.Revision || storedSource.LastCompleteSnapshotID != recovered.ID {
				t.Fatalf("recovered source = %#v, err=%v", storedSource, err)
			}
			all, err := manager.ObservationPage(ctx, ObservationListRequest{SourceID: source.ID, Limit: 10})
			if err != nil || len(all) != 3 {
				t.Fatalf("recovered observation history = %#v, err=%v", all, err)
			}
			gotFreshness := make(map[string]Freshness)
			for _, observation := range all {
				gotFreshness[observation.Identifier] = observation.Freshness
			}
			wantFreshness := map[string]Freshness{"urn:air:remote.example:catalog:child": FreshCurrent, "urn:air:remote.example:default:before": FreshWithdrawn, "urn:air:remote.example:default:after": FreshCurrent}
			if !reflect.DeepEqual(gotFreshness, wantFreshness) {
				t.Fatalf("recovery membership = %v, want %v", gotFreshness, wantFreshness)
			}
			if state, found, err := store.GetRetrievalState(ctx, source.TenantID, source.ID, source.Revision, childState.RequestKey); err != nil || !found || state.ETag != `"child-v2"` || state.RepresentationDigest != digestText(nestedHTTPSAgentDocument("after")) || string(state.Body) != nestedHTTPSAgentDocument("after") {
				t.Fatalf("recovered child state = %#v, found=%v, err=%v", state, found, err)
			}
			for _, snapshot := range []Snapshot{partial, recovered} {
				if len(snapshot.Retrievals) != 2 {
					t.Fatalf("nested retrieval evidence = %#v", snapshot.Retrievals)
				}
				root, child := snapshot.Retrievals[0], snapshot.Retrievals[1]
				if root.Status != http.StatusNotModified || !root.Reused || root.ReceivedBody || root.RepresentationDigest != rootState.RepresentationDigest || child.Status != http.StatusOK || child.Reused || !child.ReceivedBody {
					t.Fatalf("parent reuse skipped fresh child retrieval: %#v", snapshot.Retrievals)
				}
			}
			if partial.Retrievals[1].RepresentationDigest != digestText(tc.body) || recovered.Retrievals[1].RepresentationDigest != digestText(nestedHTTPSAgentDocument("after")) {
				t.Fatal("child retrieval evidence did not retain the rejected and recovered response digests")
			}
			server.Close()
			mu.Lock()
			gotRequests := slices.Clone(requests)
			mu.Unlock()
			wantRequests := []request{{"/root.json", "", ""}, {"/child.json", "", ""}, {"/root.json", `"root-v1"`, ""}, {"/child.json", `"child-v1"`, ""}, {"/root.json", `"root-v1"`, ""}, {"/child.json", "", ""}}
			if !slices.Equal(gotRequests, wantRequests) {
				t.Fatalf("conditional requests = %#v, want %#v", gotRequests, wantRequests)
			}
		})
	}
}
