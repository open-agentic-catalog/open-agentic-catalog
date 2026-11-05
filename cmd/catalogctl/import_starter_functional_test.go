//go:build functional

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// Reuse the existing compiled CLI and in-memory API fixture. The native assets
// come from the checked-in starter, not reconstructed test representations.
func qualifyImportStarter(t *testing.T, binary string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	source := filepath.Join("..", "..", "examples", "native-import")
	directory := t.TempDir()
	if err := os.CopyFS(directory, os.DirFS(source)); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(directory, "import.yaml")
	read := func(name string) []byte {
		t.Helper()
		raw, err := os.ReadFile(name) // #nosec G304 -- fixed starter files and invocation-owned inputs/receipt.
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	original := read(filename)
	agent := read(filepath.Join(directory, "agent.json"))
	skillFile := filepath.Join(directory, "skills", "review-notes", "SKILL.md")
	skill := read(skillFile)
	run := func(t *testing.T, refusal string, args ...string) []byte {
		t.Helper()
		command := exec.CommandContext(ctx, binary, append([]string{"import"}, args...)...) // #nosec G204,G702 -- existing owned CLI binary and explicit local fixture inputs.
		command.Env = append(os.Environ(), "CATALOG_CONFIG_FILE=/missing/unread-runtime-sentinel", "CATALOG_DATABASE_DSN=postgres://invalid/unread-runtime-sentinel", "CATALOG_REMOTE_TOKEN=ignored-private-sentinel")
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		if refusal == "" {
			if err != nil || stderr.Len() != 0 {
				t.Fatalf("starter CLI failed: %v %s", err, &stderr)
			}
		} else {
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 || stderr.Len() == 0 || strings.Count(stderr.String(), "\n") != 1 {
				t.Fatalf("starter refusal did not exit 1 with one diagnostic: %v", err)
			}
			if !strings.Contains(stderr.String(), refusal) {
				t.Fatalf("starter refusal reached a different guard: %s", &stderr)
			}
		}
		for _, secret := range []string{"local-contributor", "ignored-private-sentinel", "unread-runtime-sentinel", "Traceback"} {
			if strings.Contains(stdout.String()+stderr.String(), secret) {
				t.Fatal("starter output disclosed a private input or traceback")
			}
		}
		return stdout.Bytes()
	}
	var preview importBatchPreview
	raw := run(t, "", "preview", "--file", filepath.Join(source, "import.yaml"))
	if json.Unmarshal(raw, &preview) != nil || !preview.LocallyValid || len(preview.Resources) != 2 {
		t.Fatal("the exact checked-in starter does not pass offline preview")
	}
	for index, profile := range []string{adapter.ProfileA2AAgentCard, adapter.ProfileAgentSkills} {
		item := preview.Resources[index]
		if !item.LocallyValid || item.ServerLimitsChecked || len(item.PendingServerChecks) == 0 || len(item.Commands) != 4 || len(item.Representations) != 1 || item.Representations[0].ProfileID != profile || item.Representations[0].LocalValidation != "checked" || item.Representations[0].PackageFiles != 0 {
			t.Fatal("starter preview lost native validation or claims server/package checks")
		}
	}
	manifest, err := decodeImportManifest(original)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"namespaceId", "publisherId"} {
		t.Run("missing "+field, func(t *testing.T) {
			value := manifest.Resources[0].NamespaceID
			if field == "publisherId" {
				value = manifest.Resources[0].Listing.PublisherID
			}
			changed := bytes.ReplaceAll(original, []byte(field+": "+value), []byte(field+`: ""`))
			writeImportTestFile(t, directory, "import.yaml", changed, 0600)
			run(t, "import requires a stable lowercase key, existing namespace/publisher UUIDs and a supported kind", "preview", "--file", filename)
		})
	}
	writeImportTestFile(t, directory, "import.yaml", original, 0600)
	writeImportTestFile(t, directory, "agent.json", []byte(`{"name":"Invalid native Agent"}`), 0600)
	raw = run(t, "import preview found local validation errors; inspect the JSON findings", "preview", "--file", filename)
	if json.Unmarshal(raw, &preview) != nil || preview.LocallyValid || len(preview.Resources) != 2 || preview.Resources[0].LocallyValid {
		t.Fatal("invalid native starter content did not produce local findings")
	}
	writeImportTestFile(t, directory, "agent.json", agent, 0600)

	runtime := buildSmokeTestRuntime(t)
	t.Cleanup(runtime.Close)
	var mu sync.Mutex
	mutations := 0
	lastMutationStatus := 0
	var lostKey, replayKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		reply := httptest.NewRecorder()
		runtime.HTTP.Handler().ServeHTTP(reply, r)
		mutation := r.Method == http.MethodPost && (strings.HasPrefix(r.URL.Path, "/api/v1/resources") || strings.HasPrefix(r.URL.Path, "/api/v1/catalog-listings"))
		if mutation {
			mutations++
			lastMutationStatus = reply.Code
			if mutations == 7 {
				replayKey = r.Header.Get("Idempotency-Key")
			}
		}
		for name, values := range reply.Header() {
			w.Header()[name] = values
		}
		if mutation && mutations == 6 && reply.Code == http.StatusCreated {
			// The Skill Version commits, but the client gets an incomplete response.
			lostKey = r.Header.Get("Idempotency-Key")
			w.Header().Del("Content-Length")
			w.WriteHeader(reply.Code)
			_, _ = w.Write([]byte(`{"committed":`))
			return
		}
		w.WriteHeader(reply.Code)
		_, _ = w.Write(reply.Body.Bytes())
	}))
	t.Cleanup(server.Close)
	client := &smokeHTTPClient{baseURL: server.URL, client: server.Client()}
	request := func(method, path string, input, target any) {
		t.Helper()
		status := http.StatusOK
		if method == http.MethodPost {
			status = http.StatusCreated
		}
		reply, err := client.request(ctx, path, method, path, "local-admin", "starter-setup-"+path, "", input, status)
		if err != nil {
			t.Fatal(err)
		}
		if err := decodeSmokeResponse(reply, target); err != nil {
			t.Fatal(err)
		}
	}
	var namespace catalog.Namespace
	request(http.MethodPost, "/api/v1/namespaces", catalog.CreateNamespaceInput{Slug: "import-starter", DisplayName: "Import starter"}, &namespace)
	var publisher catalog.Publisher
	request(http.MethodPost, "/api/v1/publishers", catalog.CreatePublisherInput{DisplayName: "Import starter", Domains: []string{"example.com"}}, &publisher)
	prepared := bytes.ReplaceAll(original, []byte(manifest.Resources[0].NamespaceID), []byte(namespace.ID))
	prepared = bytes.ReplaceAll(prepared, []byte(manifest.Resources[0].Listing.PublisherID), []byte(publisher.ID))
	writeImportTestFile(t, directory, "import.yaml", prepared, 0600)
	token := writeImportTestFile(t, directory, "token", []byte("local-contributor"), 0600)
	state := filepath.Join(directory, "private", "state.json")
	apply := func(action, refusal string) importOutcome {
		t.Helper()
		raw := run(t, refusal, action, "--file", filename, "--state-file", state, "--base-url", server.URL, "--token-file", token)
		var result importOutcome
		if json.Unmarshal(raw, &result) != nil || len(result.Items) != 2 {
			t.Fatal("starter outcome omits its two ordered items")
		}
		return result
	}
	const unacknowledged = "import: command was not acknowledged; inspect its receipt before recovery"
	first := apply("apply", unacknowledged)
	if first.Disposition != "uncertain" || first.CurrentKey != "review-notes" || first.Items[0].Disposition != "complete" || first.Items[1].Steps[1].State != "uncertain" {
		t.Fatal("committed response loss did not preserve the completed Agent and uncertain Skill")
	}
	receipt := read(state)
	writeImportTestFile(t, directory, "skills/review-notes/SKILL.md", append(bytes.Clone(skill), []byte("\nChanged after apply.\n")...), 0600)
	apply("resume", "import: origin, resolved principal or prepared inputs changed; refuse resume")
	if !bytes.Equal(receipt, read(state)) {
		t.Fatal("changed starter asset replaced the original recovery receipt")
	}
	mu.Lock()
	calls := mutations
	mu.Unlock()
	if calls != 6 {
		t.Fatal("changed starter asset allowed a mutation")
	}
	writeImportTestFile(t, directory, "skills/review-notes/SKILL.md", skill, 0600)
	completed := apply("resume", "")
	mu.Lock()
	calls, originalKey, recoveredKey := mutations, lostKey, replayKey
	mu.Unlock()
	if completed.Disposition != "complete" || calls != 9 || originalKey == "" || recoveredKey != originalKey {
		t.Fatal("starter recovery did not replay the committed command with its original key")
	}
	for index, item := range completed.Items {
		if item.Key != manifest.Resources[index].Key || item.Disposition != "complete" || len(item.Steps) != 4 {
			t.Fatal("starter recovery changed item order or missed creation steps")
		}
		for step, result := range item.Steps {
			if result.Key != first.Items[index].Steps[step].Key || result.State != "acknowledged" {
				t.Fatal("starter recovery changed an original key or lost an acknowledgement")
			}
		}
		var resource catalog.Resource
		request(http.MethodGet, "/api/v1/resources/"+item.Steps[0].Ack.ID, nil, &resource)
		if resource.Kind != manifest.Resources[index].Kind || resource.CanonicalIdentifier != manifest.Resources[index].CanonicalIdentifier || resource.NamespaceID != namespace.ID || resource.CreatedBy != "local-contributor" {
			t.Fatal("starter draft identity, kind, namespace or author changed")
		}
		var retained struct {
			Revision catalog.VersionRevision `json:"revision"`
			Evidence catalog.EvidenceSet     `json:"evidence"`
		}
		request(http.MethodGet, "/api/v1/version-revisions/"+item.Steps[1].Ack.RevisionID, nil, &retained)
		if len(retained.Revision.Representations) != 1 || retained.Evidence.State != catalog.EvidenceComplete || retained.Evidence.ID != item.Steps[1].Ack.EvidenceID || retained.Evidence.SubjectDigest != retained.Revision.ContentSetDigest {
			t.Fatal("starter draft does not retain valid bound native evidence")
		}
		representation := retained.Revision.Representations[0]
		if representation.ValidationState != "valid" || representation.Package != nil || representation.Handoff != nil {
			t.Fatal("starter native document became invalid or acquired package/handoff evidence")
		}
		if index == 0 {
			var expected, actual any
			if json.Unmarshal(agent, &expected) != nil || json.Unmarshal(representation.Data, &actual) != nil || !reflect.DeepEqual(expected, actual) || representation.ProfileID != adapter.ProfileA2AAgentCard {
				t.Fatal("starter Agent Card changed in retained readback")
			}
		} else {
			reply, err := client.requestWithHeaders(ctx, "starter Skill bytes", http.MethodGet, "/api/v1/artifacts/"+representation.ArtifactID+"/content", "local-contributor", "", "", nil, map[string]string{"Accept": representation.MediaType}, http.StatusOK)
			if err != nil || !bytes.Equal(reply.Body, skill) || representation.ContentDigest != importDigest(skill) || representation.ProfileID != adapter.ProfileAgentSkills {
				t.Fatal("starter Skill document bytes or native evidence changed", err)
			}
		}
		var listing catalog.ListingRevision
		request(http.MethodGet, "/api/v1/listing-revisions/"+item.Steps[3].Ack.ID, nil, &listing)
		if listing.VersionRevisionID != retained.Revision.ID || listing.PublisherID != publisher.ID || listing.Visibility != catalog.VisibilityPrivate {
			t.Fatal("starter Listing lost its private imported version or Publisher")
		}
		for _, path := range []string{"/api/v1/catalog-listings/" + listing.ListingID + "/releases", "/api/v1/review-submissions?listingId=" + listing.ListingID} {
			var page struct{ Items []json.RawMessage }
			request(http.MethodGet, path, nil, &page)
			if len(page.Items) != 0 {
				t.Fatal("starter import created a review or release")
			}
		}
		var versions struct{ Items []json.RawMessage }
		request(http.MethodGet, "/api/v1/resources/"+resource.ID+"/versions", nil, &versions)
		if len(versions.Items) != 1 {
			t.Fatal("starter response-loss recovery duplicated a Version")
		}
	}
	for path, count := range map[string]int{"/api/v1/resources?limit=100": 2, "/api/v1/publication/snapshots?host=127.0.0.1&limit=100": 0} {
		var page struct {
			Items      []json.RawMessage
			NextCursor string
		}
		request(http.MethodGet, path, nil, &page)
		if len(page.Items) != count || page.NextCursor != "" {
			t.Fatal("starter import duplicated Resources or created a publication snapshot")
		}
	}
	if again := apply("resume", ""); !reflect.DeepEqual(again, completed) {
		t.Fatal("completed starter resume changed retained identities or outcome")
	}
	mu.Lock()
	calls = mutations
	mu.Unlock()
	if calls != 9 {
		t.Fatal("completed starter resume repeated a mutation")
	}
	for path, mode := range map[string]os.FileMode{state: 0600, filepath.Dir(state): 0700} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatal("starter receipt or its directory is not private", err)
		}
	}
	// A fresh receipt cannot adopt the completed import by matching identifiers.
	completedState, completedReceipt := state, read(state)
	state = filepath.Join(directory, "fresh-receipt", "state.json")
	collision := apply("apply", unacknowledged)
	mu.Lock()
	calls, status := mutations, lastMutationStatus
	mu.Unlock()
	if collision.Disposition != "failed" || collision.CurrentKey != "review-helper" || collision.Items[0].Steps[0].State != "failed" || collision.Items[0].Steps[1].State != "ready" || collision.Items[1].Disposition != "unattempted" || calls != 10 || status != http.StatusConflict {
		t.Fatal("fresh-receipt import did not stop at the existing Resource conflict")
	}
	var resources struct {
		Items      []json.RawMessage
		NextCursor string
	}
	request(http.MethodGet, "/api/v1/resources?limit=100", nil, &resources)
	if len(resources.Items) != 2 || resources.NextCursor != "" || !bytes.Equal(completedReceipt, read(completedState)) {
		t.Fatal("fresh-receipt refusal changed Resources or the completed receipt")
	}
	if !bytes.Equal(original, read(filepath.Join(source, "import.yaml"))) || !bytes.Equal(agent, read(filepath.Join(source, "agent.json"))) || !bytes.Equal(skill, read(filepath.Join(source, "skills", "review-notes", "SKILL.md"))) {
		t.Fatal("starter qualification changed checked-in input files")
	}
	t.Run("saved MCP Registry record", func(t *testing.T) {
		response := read(filepath.Join(source, "mcp-registry-response.json"))
		var wrapper struct {
			Server json.RawMessage `json:"server"`
		}
		if json.Unmarshal(response, &wrapper) != nil || len(wrapper.Server) == 0 {
			t.Fatal("offline Registry fixture has no native server")
		}
		registryManifest := read(filepath.Join(source, "mcp-registry-import.yaml"))
		parsed, err := decodeImportManifest(registryManifest)
		if err != nil || len(parsed.Resources) != 1 || parsed.Resources[0].Listing.PublisherID == "" {
			t.Fatal("Registry starter manifest is invalid", err)
		}
		registryManifest = bytes.ReplaceAll(registryManifest, []byte(parsed.Resources[0].NamespaceID), []byte(namespace.ID))
		registryManifest = bytes.ReplaceAll(registryManifest, []byte(parsed.Resources[0].Listing.PublisherID), []byte(publisher.ID))
		registryFile := writeImportTestFile(t, directory, "registry.yaml", registryManifest, 0600)
		writeImportTestFile(t, directory, "server.json", wrapper.Server, 0600)
		var preview importBatchPreview
		if json.Unmarshal(run(t, "", "preview", "--file", registryFile), &preview) != nil || !preview.LocallyValid || len(preview.Resources) != 1 || len(preview.Resources[0].Representations) != 1 || preview.Resources[0].Representations[0].ProfileID != adapter.ProfileMCPRegistry {
			t.Fatal("extracted Registry document does not pass offline native preview")
		}
		writeImportTestFile(t, directory, "server.json", response, 0600)
		run(t, "import preview found local validation errors; inspect the JSON findings", "preview", "--file", registryFile)
		writeImportTestFile(t, directory, "server.json", wrapper.Server, 0600)
		var result importOutcome
		raw := run(t, "", "apply", "--file", registryFile, "--state-file", filepath.Join(directory, "registry-private", "state.json"), "--base-url", server.URL, "--token-file", token)
		if json.Unmarshal(raw, &result) != nil || result.Disposition != "complete" || len(result.Items) != 1 || len(result.Items[0].Steps) != 4 {
			t.Fatal("Registry import did not complete")
		}
		item := result.Items[0]
		var retained struct {
			Revision catalog.VersionRevision `json:"revision"`
			Evidence catalog.EvidenceSet     `json:"evidence"`
		}
		request(http.MethodGet, "/api/v1/version-revisions/"+item.Steps[1].Ack.RevisionID, nil, &retained)
		if retained.Evidence.State != catalog.EvidenceComplete || len(retained.Revision.Representations) != 1 || retained.Revision.Representations[0].ProfileID != adapter.ProfileMCPRegistry {
			t.Fatal("Registry import lost native validation")
		}
		var expected, actual any
		if json.Unmarshal(wrapper.Server, &expected) != nil || json.Unmarshal(retained.Revision.Representations[0].Data, &actual) != nil || !reflect.DeepEqual(expected, actual) {
			t.Fatal("Registry import changed native extension fields or retained the response wrapper")
		}
		var listing catalog.ListingRevision
		request(http.MethodGet, "/api/v1/listing-revisions/"+item.Steps[3].Ack.ID, nil, &listing)
		if listing.Visibility != catalog.VisibilityPrivate || listing.VersionRevisionID != retained.Revision.ID || listing.PublisherID != publisher.ID {
			t.Fatal("Registry import lost its private Listing or destination binding")
		}
		for _, path := range []string{"/api/v1/catalog-listings/" + listing.ListingID + "/releases", "/api/v1/review-submissions?listingId=" + listing.ListingID} {
			var page struct{ Items []json.RawMessage }
			request(http.MethodGet, path, nil, &page)
			if len(page.Items) != 0 {
				t.Fatal("Registry import created review or release authority")
			}
		}
	})
}
