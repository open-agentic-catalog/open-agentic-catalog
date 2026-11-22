//go:build functional

package integration

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/httpserver"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

// Replacement publication changes current release eligibility, not immutable
// public bytes. Exercise the composed withdrawal wiring through owned HTTP.
func TestReplacementPublicationPreservesActiveDiscoveryUntilExplicitChange(t *testing.T) {
	for _, disposition := range []string{"retired", "superseded"} {
		t.Run(disposition, func(t *testing.T) {
			runtime := buildRuntime(t)
			t.Cleanup(runtime.Close)
			server := httptest.NewServer(runtime.HTTP.Handler())
			t.Cleanup(server.Close)
			namespaceResult := request(t, server.URL, http.MethodPost, "/api/v1/namespaces", "admin", "namespace", "", map[string]any{"slug": "replacement", "displayName": "Replacement"})
			requireStatus(t, namespaceResult, http.StatusCreated)
			var namespace catalog.Namespace
			decode(t, namespaceResult, &namespace)
			publisherResult := request(t, server.URL, http.MethodPost, "/api/v1/publishers", "contributor", "publisher", "", map[string]any{"displayName": "Example Publisher", "domains": []string{"example.com"}})
			requireStatus(t, publisherResult, http.StatusCreated)
			var publisher catalog.Publisher
			decode(t, publisherResult, &publisher)
			requireStatus(t, request(t, server.URL, http.MethodPost, "/api/v1/publishers/"+publisher.ID+"/publication-domains/example.com:approve", "admin", "approve-domain", publisherResult.header.Get("ETag"), map[string]any{"reason": "Approve owned publication fixture"}), http.StatusOK)
			resourceResult := request(t, server.URL, http.MethodPost, "/api/v1/resources", "contributor", "resource", "", map[string]any{"namespaceId": namespace.ID, "canonicalIdentifier": "urn:air:example.com:replacement:agent", "kind": "agent"})
			requireStatus(t, resourceResult, http.StatusCreated)
			var resource catalog.Resource
			decode(t, resourceResult, &resource)
			versionResult := request(t, server.URL, http.MethodPost, "/api/v1/resources/"+resource.ID+"/versions", "contributor", "version", "", map[string]any{"version": "1.0.0", "representations": []map[string]any{{"role": "primary", "mediaType": adapter.MediaA2AAgentCard, "data": map[string]any{"name": "Replacement agent", "description": "Answers replacement questions", "version": "1.0.0", "supportedInterfaces": []map[string]any{{"url": "https://agents.example.com/replacement", "protocolBinding": "JSONRPC", "protocolVersion": "1.0"}}, "capabilities": map[string]any{}, "defaultInputModes": []string{"text/plain"}, "defaultOutputModes": []string{"text/plain"}, "skills": []map[string]any{{"id": "replacement", "name": "Replacement", "description": "Answers replacement questions", "tags": []string{"replacement"}}}}}}})
			requireStatus(t, versionResult, http.StatusCreated)
			var version struct {
				Revision catalog.VersionRevision `json:"revision"`
			}
			decode(t, versionResult, &version)
			listingResult := request(t, server.URL, http.MethodPost, "/api/v1/catalog-listings", "contributor", "listing", "", map[string]any{"namespaceId": namespace.ID, "resourceId": resource.ID})
			requireStatus(t, listingResult, http.StatusCreated)
			var listing catalog.CatalogListing
			decode(t, listingResult, &listing)

			publish := func(key, name, priorDisposition string) catalog.ListingRelease {
				t.Helper()
				current := request(t, server.URL, http.MethodGet, "/api/v1/catalog-listings/"+listing.ID, "contributor", "", "", nil)
				requireStatus(t, current, http.StatusOK)
				revision := request(t, server.URL, http.MethodPost, "/api/v1/catalog-listings/"+listing.ID+"/revisions", "contributor", key+"-revision", current.header.Get("ETag"), map[string]any{"versionRevisionId": version.Revision.ID, "displayName": name, "description": "Reviewed replacement publication metadata", "representativeQueries": []string{"Find the replacement agent", "Answer a replacement question"}, "publisherId": publisher.ID, "visibility": "public", "license": "Apache-2.0"})
				requireStatus(t, revision, http.StatusCreated)
				submissionResult := request(t, server.URL, http.MethodPost, "/api/v1/catalog-listings/"+listing.ID+"/submissions", "contributor", key+"-submit", "", map[string]any{})
				requireStatus(t, submissionResult, http.StatusCreated)
				var submission catalog.ReviewSubmission
				decode(t, submissionResult, &submission)
				requireStatus(t, request(t, server.URL, http.MethodPost, "/api/v1/review-submissions/"+submission.ID+":approve", "reviewer", key+"-approve", submissionResult.header.Get("ETag"), map[string]any{"reason": "Independent fixture review"}), http.StatusOK)
				body := map[string]any{"submissionId": submission.ID}
				if priorDisposition != "" {
					body["priorDisposition"], body["priorReason"] = priorDisposition, "Use the replacement metadata"
				}
				result := request(t, server.URL, http.MethodPost, "/api/v1/catalog-listings/"+listing.ID+":publish", "publisher", key+"-publish", revision.header.Get("ETag"), body)
				requireStatus(t, result, http.StatusCreated)
				var outcome struct {
					Release catalog.ListingRelease `json:"release"`
				}
				decode(t, result, &outcome)
				return outcome.Release
			}
			build := func(key string) publication.Snapshot {
				t.Helper()
				result := request(t, server.URL, http.MethodPost, "/api/v1/publication/snapshots", "publisher", key, "", map[string]any{"host": "127.0.0.1"})
				requireStatus(t, result, http.StatusCreated)
				var snapshot publication.Snapshot
				decode(t, result, &snapshot)
				if result.header.Get("ETag") != snapshot.ETag || result.header.Get("Location") != "/api/v1/publication/snapshots/"+snapshot.ID+"?host=127.0.0.1" {
					t.Fatalf("synchronous snapshot response lacks bound headers: %v", result.header)
				}
				retained := request(t, server.URL, http.MethodGet, result.header.Get("Location"), "publisher", "", "", nil)
				requireStatus(t, retained, http.StatusOK)
				if !bytes.Equal(retained.body, result.body) || retained.header.Get("ETag") != snapshot.ETag {
					t.Fatal("snapshot Location did not retrieve the exact candidate")
				}
				requireStatus(t, request(t, server.URL, http.MethodGet, "/api/v1/publication/snapshots/"+snapshot.ID, "publisher", "", "", nil), http.StatusBadRequest)
				return snapshot
			}
			activate := func(snapshot publication.Snapshot, key string) {
				t.Helper()
				requireStatus(t, request(t, server.URL, http.MethodPost, "/api/v1/publication/snapshots/"+snapshot.ID+":activate", "publisher", key, snapshot.ETag, map[string]any{"host": "127.0.0.1", "reason": "Activate reviewed fixture metadata"}), http.StatusOK)
			}
			assertActive := func(wantID string) {
				t.Helper()
				result := request(t, server.URL, http.MethodGet, "/api/v1/publication/snapshots?host=127.0.0.1&state=active&limit=2", "publisher", "", "", nil)
				requireStatus(t, result, http.StatusOK)
				var page struct {
					Items []publication.SnapshotSummary `json:"items"`
				}
				decode(t, result, &page)
				if wantID == "" {
					if len(page.Items) != 0 {
						t.Fatalf("withdrawal left an active snapshot: %+v", page.Items)
					}
				} else if len(page.Items) != 1 || page.Items[0].ID != wantID {
					t.Fatalf("active snapshot = %+v, want %s", page.Items, wantID)
				}
			}
			original := publish("first", "Original publication name", "")
			first := build("first-candidate")
			activate(first, "first-activate")
			publicPaths := []string{"/.well-known/ard.json", "/.well-known/ai-catalog.json"}
			before := make([]response, len(publicPaths))
			for i, path := range publicPaths {
				before[i] = request(t, server.URL, http.MethodGet, path, "", "", "", nil)
				requireStatus(t, before[i], http.StatusOK)
				if before[i].header.Get("ETag") == "" || !bytes.Contains(before[i].body, []byte("Original publication name")) {
					t.Fatalf("invalid initial publication at %s: %s", path, before[i].body)
				}
			}
			replacement := publish("second", "Replacement publication name", disposition)
			assertActive(first.ID)
			old := request(t, server.URL, http.MethodGet, "/api/v1/listing-releases/"+original.ID, "publisher", "", "", nil)
			requireStatus(t, old, http.StatusOK)
			var previous catalog.ListingRelease
			decode(t, old, &previous)
			if string(previous.State) != disposition || previous.ID == replacement.ID {
				t.Fatalf("replacement did not change the prior release: %+v", previous)
			}
			if disposition == "superseded" && previous.ReplacementReleaseID != replacement.ID {
				t.Fatalf("supersession points to a different replacement: %+v", previous)
			}
			assertUnchanged := func() {
				t.Helper()
				for i, path := range publicPaths {
					after := request(t, server.URL, http.MethodGet, path, "", "", "", nil)
					requireStatus(t, after, http.StatusOK)
					if !bytes.Equal(before[i].body, after.body) || before[i].header.Get("ETag") != after.header.Get("ETag") {
						t.Fatalf("replacement changed active bytes or ETag at %s", path)
					}
				}
			}
			assertUnchanged()
			second := build("second-candidate")
			stale := request(t, server.URL, http.MethodPost, "/api/v1/publication/snapshots/"+second.ID+":activate", "publisher", "stale-activation", first.ETag, map[string]any{"host": second.Host, "reason": "Activate replacement metadata"})
			requireStatus(t, stale, http.StatusPreconditionFailed)
			var precondition httpserver.Problem
			decode(t, stale, &precondition)
			if precondition.Code != catalog.CodePreconditionFailed {
				t.Fatalf("stale candidate ETag returned %s", precondition.Code)
			}
			assertActive(first.ID)
			assertUnchanged()
			reload := request(t, server.URL, http.MethodGet, "/api/v1/publication/snapshots/"+second.ID+"?host="+second.Host, "publisher", "", "", nil)
			requireStatus(t, reload, http.StatusOK)
			var candidate publication.Snapshot
			decode(t, reload, &candidate)
			if candidate.ID != second.ID || candidate.State != publication.SnapshotCandidate || candidate.ETag != second.ETag || reload.header.Get("ETag") != candidate.ETag {
				t.Fatalf("failed activation changed candidate %s", second.ID)
			}
			activate(candidate, "second-activate")
			assertActive(second.ID)
			if second.ID == first.ID {
				t.Fatal("candidate reused the original snapshot identity")
			}
			for i, path := range publicPaths {
				after := request(t, server.URL, http.MethodGet, path, "", "", "", nil)
				requireStatus(t, after, http.StatusOK)
				if bytes.Equal(before[i].body, after.body) || before[i].header.Get("ETag") == after.header.Get("ETag") || !bytes.Contains(after.body, []byte("Replacement publication name")) || bytes.Contains(after.body, []byte("Original publication name")) {
					t.Fatalf("activation did not expose replacement metadata at %s: %s", path, after.body)
				}
			}
			release := request(t, server.URL, http.MethodGet, "/api/v1/listing-releases/"+replacement.ID, "publisher", "", "", nil)
			requireStatus(t, release, http.StatusOK)
			retire := func() response {
				return request(t, server.URL, http.MethodPost, "/api/v1/listing-releases/"+replacement.ID+":retire", "publisher", "explicit-retire", release.header.Get("ETag"), map[string]any{"reason": "Withdraw the replacement release explicitly"})
			}
			retired := retire()
			requireStatus(t, retired, http.StatusOK)
			assertWithdrawn := func() {
				t.Helper()
				assertActive("")
				for _, path := range publicPaths {
					requireStatus(t, request(t, server.URL, http.MethodGet, path, "", "", "", nil), http.StatusNotFound)
				}
			}
			assertWithdrawn()
			rollback := request(t, server.URL, http.MethodPost, "/api/v1/publication/snapshots/"+second.ID+":rollback", "publisher", "rollback-withdrawn", second.ETag, map[string]any{"host": second.Host, "reason": "Restore the previously active snapshot"})
			requireStatus(t, rollback, http.StatusUnprocessableEntity)
			var problem httpserver.Problem
			decode(t, rollback, &problem)
			if problem.Code != catalog.CodePublicationNotReady {
				t.Fatalf("rollback of withdrawn metadata returned %s", problem.Code)
			}
			assertWithdrawn()

			recovered := build("after-withdrawal")
			activate(recovered, "activate-after-withdrawal")
			assertActive(recovered.ID)
			// A delayed retry must return the original receipt and leave the newly active feed in place.
			replay := retire()
			requireStatus(t, replay, http.StatusOK)
			if replay.header.Get("Idempotency-Replayed") != "true" || replay.header.Get("ETag") != retired.header.Get("ETag") || !bytes.Equal(replay.body, retired.body) {
				t.Fatal("retirement retry did not return the original receipt")
			}
			history := request(t, server.URL, http.MethodGet, "/api/v1/listing-releases/"+replacement.ID, "publisher", "", "", nil)
			requireStatus(t, history, http.StatusOK)
			var retained catalog.ListingRelease
			decode(t, history, &retained)
			if retained.ID != replacement.ID || retained.State != catalog.ReleaseRetired || retained.Revision != replacement.Revision+1 || history.header.Get("ETag") != retired.header.Get("ETag") {
				t.Fatalf("recovery or replay changed release history: %+v", retained)
			}
			assertActive(recovered.ID)
			ard := request(t, server.URL, http.MethodGet, "/.well-known/ard.json", "", "", "", nil)
			requireStatus(t, ard, http.StatusOK)
			var manifest publication.ARDManifest
			decode(t, ard, &manifest)
			if ard.header.Get("ETag") != recovered.ETag || len(manifest.Entries) != 1 || manifest.Entries[0].Type != "application/ai-registry+json" {
				t.Fatalf("expected only the Registry in the recovered ARD feed: %s", ard.body)
			}
			ai := request(t, server.URL, http.MethodGet, "/.well-known/ai-catalog.json", "", "", "", nil)
			requireStatus(t, ai, http.StatusOK)
			var aiCatalog publication.AICatalog
			decode(t, ai, &aiCatalog)
			if ai.header.Get("ETag") != recovered.AICatalogETag || aiCatalog.Entries == nil || len(aiCatalog.Entries) != 0 {
				t.Fatalf("expected an empty recovered AI Catalog: %s", ai.body)
			}
		})
	}
}
