//go:build functional

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/demodata"
)

// Supplement protocol fault controls with real routing, authorization, native
// validation and hardened HTTPS retrieval. Reuse the existing CLI binary and
// four-kind input; no seed, review, release or publication journey is needed.
func qualifyImportRealAPI(t *testing.T, binary, directory string, manifest importManifest) {
	t.Helper()
	fault := &atomic.Int32{}
	_, server, origin, _, writes := configuredDemoRuntimeWithPeerControl(t, fault)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	client := &smokeHTTPClient{baseURL: server.URL, client: server.Client()}
	request := func(method, path, token string, body, target any) {
		t.Helper()
		status := http.StatusOK
		if method == http.MethodPost {
			status = http.StatusCreated
		}
		response, err := client.request(ctx, path, method, path, token, "import-setup-"+path, "", body, status)
		if err != nil {
			t.Fatal(err)
		}
		if err := decodeSmokeResponse(response, target); err != nil {
			t.Fatal(err)
		}
	}
	var namespace catalog.Namespace
	request(http.MethodPost, "/api/v1/namespaces", "local-admin", catalog.CreateNamespaceInput{Slug: "native-import", DisplayName: "Native import"}, &namespace)
	var publisher catalog.Publisher
	request(http.MethodPost, "/api/v1/publishers", "local-admin", catalog.CreatePublisherInput{DisplayName: "Native import", Domains: []string{"example.com"}}, &publisher)
	plan := demodata.DefaultPlan("expanded", origin)
	var plugin demodata.PreparedResource
	for _, item := range plan.Resources {
		if item.Resource.Kind == catalog.KindPlugin {
			plugin = item
			break
		}
	}
	if len(plugin.Archive) == 0 {
		t.Fatal("missing owned Plugin archive fixture")
	}
	manifest.Resources = slices.Clone(manifest.Resources)
	for index := range manifest.Resources {
		item := &manifest.Resources[index]
		item.NamespaceID, item.Listing.PublisherID = namespace.ID, publisher.ID
		if item.Kind == catalog.KindPlugin {
			item.Version = plugin.Resource.Version
			item.Representations = []importRepresentation{{Role: catalog.RepresentationPrimary, MediaType: catalog.PluginMediaZIP, URL: plugin.Representation.URL}}
		}
	}
	filename := writeImportTestFile(t, directory, "real-api.yaml", importTestJSON(t, manifest), 0600)
	token := writeImportTestFile(t, directory, "real-api-token", []byte("local-contributor"), 0600)
	state := filepath.Join(directory, "real-api-private", "state.json")
	run := func(action string, success bool) importOutcome {
		t.Helper()
		command := exec.CommandContext(ctx, binary, "import", action, "--file", filename, "--state-file", state, "--base-url", server.URL, "--token-file", token) // #nosec G204,G702 -- reused owned executable, fixed local API and private synthetic inputs.
		command.Env = append(os.Environ(), "CATALOG_CONFIG_FILE=/missing", "CATALOG_REMOTE_TOKEN=ignored-token")
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		if (err == nil) != success || success && stderr.Len() != 0 {
			t.Fatalf("real API import: %v %s %s", err, &stdout, &stderr)
		}
		var outcome importOutcome
		if err := json.Unmarshal(stdout.Bytes(), &outcome); err != nil {
			t.Fatal(err)
		}
		return outcome
	}
	before := writes.Load()
	result := run("apply", true)
	if result.Disposition != "complete" || len(result.Items) != 4 || writes.Load()-before != 16 {
		t.Fatal("real four-kind import did not create exactly four drafts")
	}
	for index, item := range result.Items {
		if item.Key != manifest.Resources[index].Key || item.Disposition != "complete" || len(item.Steps) != 4 {
			t.Fatal("real API lost manifest order or acknowledged progress")
		}
		var resource catalog.Resource
		request(http.MethodGet, "/api/v1/resources/"+item.Steps[0].Ack.ID, "local-contributor", nil, &resource)
		if resource.Kind != manifest.Resources[index].Kind || resource.NamespaceID != namespace.ID || resource.CreatedBy != "local-contributor" || resource.CanonicalIdentifier != manifest.Resources[index].CanonicalIdentifier {
			t.Fatal("real API changed resource identity, kind or author")
		}
		var retained struct {
			Revision catalog.VersionRevision `json:"revision"`
			Evidence catalog.EvidenceSet     `json:"evidence"`
		}
		request(http.MethodGet, "/api/v1/version-revisions/"+item.Steps[1].Ack.RevisionID, "local-contributor", nil, &retained)
		if retained.Revision.EvidenceSetID != item.Steps[1].Ack.EvidenceID || retained.Evidence.SubjectDigest != retained.Revision.ContentSetDigest || retained.Evidence.State != catalog.EvidenceComplete || len(retained.Revision.Representations) != len(manifest.Resources[index].Representations) {
			t.Fatal("real native validation did not retain complete bound evidence")
		}
		for _, representation := range retained.Revision.Representations {
			if representation.ValidationState != "valid" || representation.ProfileID == "" || representation.ContentDigest == "" {
				t.Fatalf("invalid native representation: %+v", representation)
			}
		}
		representation := retained.Revision.Representations[0]
		switch resource.Kind {
		case catalog.KindAgent:
			if representation.ProfileID != adapter.ProfilePrivateAgent || representation.Role != catalog.RepresentationPrimary {
				t.Fatal("Agent native profile or role changed")
			}
		case catalog.KindMCPServer:
			if len(retained.Revision.Representations) != 2 || representation.ProfileID != adapter.ProfileMCPServerCard || representation.Role != catalog.RepresentationPrimary || retained.Revision.Representations[1].ProfileID != adapter.ProfileMCPRegistry || retained.Revision.Representations[1].Role != catalog.RepresentationDistribution {
				t.Fatal("MCP native profiles or document roles changed")
			}
		case catalog.KindSkill:
			if representation.ProfileID != adapter.ProfileAgentSkills || representation.Package == nil || representation.Package.FileCount != 2 || len(representation.Package.Files) != 2 || !reflect.DeepEqual(representation.Handoff, manifest.Resources[index].Representations[0].Handoff) {
				t.Fatal("complete Skill inventory or immutable handoff was lost")
			}
			var executable bool
			for _, file := range representation.Package.Files {
				if file.Path == "safe-skill/scripts/run.sh" && file.Executable && file.Digest != "" {
					executable = true
				}
			}
			if !executable {
				t.Fatal("Skill executable evidence was lost")
			}
		case catalog.KindPlugin:
			if representation.ProfileID != catalog.PluginProfile || representation.Plugin == nil || len(representation.Plugin.Documents) == 0 || len(representation.Plugin.Files) == 0 || len(retained.Evidence.ExternalRetrievals) != 1 {
				t.Fatal("Plugin archive was not retrieved and inspected")
			}
			retrieval := retained.Evidence.ExternalRetrievals[0]
			if retrieval.Status != http.StatusOK || retrieval.RequestedURL != plugin.Representation.URL || retrieval.ContentDigest != plugin.BodyDigest || representation.ContentDigest != plugin.BodyDigest {
				t.Fatal("Plugin evidence does not bind the owned archive bytes")
			}
		}
		var revision catalog.ListingRevision
		request(http.MethodGet, "/api/v1/listing-revisions/"+item.Steps[3].Ack.ID, "local-contributor", nil, &revision)
		if revision.VersionRevisionID != retained.Revision.ID || revision.ListingID != item.Steps[2].Ack.ID || revision.PublisherID != publisher.ID {
			t.Fatal("listing did not retain the imported version and publisher")
		}
		for _, path := range []string{"/api/v1/catalog-listings/" + revision.ListingID + "/releases", "/api/v1/review-submissions?listingId=" + revision.ListingID} {
			var page struct {
				Items []json.RawMessage `json:"items"`
			}
			request(http.MethodGet, path, "local-admin", nil, &page)
			if len(page.Items) != 0 {
				t.Fatal("draft import unexpectedly submitted or released an item")
			}
		}
	}
	// Completed receipt verification needs no new archive retrieval or writes.
	fault.Store(1)
	again := run("resume", true)
	if !reflect.DeepEqual(again.Items, result.Items) || writes.Load()-before != 16 {
		t.Fatal("completed resume changed identities or repeated mutations")
	}
	// The fixture's exact archive policy rejects changed bytes before a Version
	// can commit. This definitive refusal differs from retained blocked evidence.
	fault.Store(2)
	blocked := manifest.Resources[3]
	blocked.Key, blocked.CanonicalIdentifier = "invalid-plugin", "custom:invalid-plugin"
	filename = writeImportTestFile(t, directory, "blocked-api.yaml", importTestJSON(t, importManifest{Format: 1, Resources: []importResource{blocked}}), 0600)
	state = filepath.Join(directory, "blocked-api-private", "state.json")
	failed := run("apply", false)
	if failed.Disposition != "failed" || len(failed.Items) != 1 || failed.Items[0].Steps[0].State != "acknowledged" || failed.Items[0].Steps[1].State != "failed" || failed.Items[0].Steps[2].State != "ready" || writes.Load()-before != 18 {
		t.Fatal("real Plugin archive refusal continued into listing creation")
	}
	var versions struct {
		Items []json.RawMessage `json:"items"`
	}
	request(http.MethodGet, "/api/v1/resources/"+failed.Items[0].Steps[0].Ack.ID+"/versions", "local-contributor", nil, &versions)
	if len(versions.Items) != 0 {
		t.Fatal("rejected Plugin archive left a committed Version")
	}
	refused := run("resume", false)
	if refused.Disposition != "uncertain" || writes.Load()-before != 18 {
		t.Fatal("resume repeated a definitively refused Version command")
	}
}
