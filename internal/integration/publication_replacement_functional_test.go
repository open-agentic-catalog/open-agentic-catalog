//go:build functional

package integration

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
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
				result := request(t, server.URL, http.MethodGet, "/api/v1/publication/snapshots?host=127.0.0.1&state=active&limit=1", "publisher", "", "", nil)
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
			assertActive(first.ID)
			assertUnchanged()
			activate(second, "second-activate")
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
			requireStatus(t, request(t, server.URL, http.MethodPost, "/api/v1/listing-releases/"+replacement.ID+":retire", "publisher", "explicit-retire", release.header.Get("ETag"), map[string]any{"reason": "Withdraw the replacement release explicitly"}), http.StatusOK)
			assertActive("")
			for _, path := range publicPaths {
				requireStatus(t, request(t, server.URL, http.MethodGet, path, "", "", "", nil), http.StatusNotFound)
			}
		})
	}
}
