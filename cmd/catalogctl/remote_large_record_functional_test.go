//go:build functional

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/httpserver"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/idempotency"
)

// Use a real admitted/reviewed release, not only an oversized HTTP stub. The
// same CLI path must navigate compact Search to complete retained evidence.
func TestRemoteLargeGovernedRecord(t *testing.T) {
	runtime := buildSmokeTestRuntime(t, func(c *config.Config) {
		c.HTTP.MaxBodyBytes = 32 << 20
		c.HTTP.RequestTimeout = config.Duration{Duration: 20 * time.Second}
		c.HTTP.WriteTimeout = config.Duration{Duration: time.Minute}
		c.HTTP.ShutdownTimeout = config.Duration{Duration: time.Minute}
	})
	defer runtime.Close()
	server := httptest.NewServer(runtime.HTTP.Handler())
	defer server.Close()
	receipt, err := executeDemo(t.Context(), server.URL, filepath.Join(t.TempDir(), "receipt.json"))
	if err != nil {
		t.Fatal(err)
	}
	admin, err := runtime.Authenticator.Authenticate(t.Context(), "local-admin")
	if err != nil {
		t.Fatal(err)
	}
	ctx := identity.WithActor(t.Context(), admin)
	item := receipt.Items[0]
	resource, err := runtime.Catalog.GetResource(ctx, item.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	if resource.Kind != catalog.KindAgent {
		t.Fatal("expected Agent positive control")
	}
	card := json.RawMessage(`{"name":"Large","description":"Large record","version":"2.0.0","supportedInterfaces":[{"url":"https://agent.example.com/a2a","protocolBinding":"JSONRPC","protocolVersion":"1.0"}],"capabilities":{},"defaultInputModes":["text/plain"],"defaultOutputModes":["text/plain"],"skills":[{"id":"large","name":"Large","description":"` + strings.Repeat("x", remoteMaxBody) + `","tags":["large"]}]}`)
	body, err := json.Marshal(catalog.CreateVersionInput{Version: "2.0.0", Representations: []catalog.RepresentationInput{{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaA2AAgentCard, Data: card}}})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/api/v1/resources/"+resource.ID+"/versions", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer local-admin")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "large-record-version")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var created struct {
		Revision catalog.VersionRevision `json:"revision"`
		Evidence catalog.EvidenceSet     `json:"evidence"`
	}
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("large admission failed: %d", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<20)).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Evidence.State != catalog.EvidenceComplete {
		t.Fatalf("valid Card blocked: %v", created.Evidence.Findings)
	}
	listing, err := runtime.Catalog.GetListing(ctx, item.ListingID)
	if err != nil {
		t.Fatal(err)
	}
	listing, _, err = runtime.Catalog.CreateListingRevision(ctx, listing.ID, catalog.CreateListingRevisionInput{VersionRevisionID: created.Revision.ID, DisplayName: "Large exact record", Description: "Compact metadata", PublisherID: receipt.PublisherID, Visibility: catalog.VisibilityTenant, Tags: []string{"large-exact"}}, listing.Revision)
	if err != nil {
		t.Fatal(err)
	}
	submission, err := runtime.Catalog.Submit(ctx, listing.ID)
	if err != nil {
		t.Fatal(err)
	}
	reviewer, err := runtime.Authenticator.Authenticate(t.Context(), "local-reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := runtime.Catalog.DecideSubmission(identity.WithActor(t.Context(), reviewer), submission.ID, "approve", "Large fixture review", submission.Revision); err != nil {
		t.Fatal(err)
	}
	_, release, _, err := runtime.Catalog.Publish(ctx, listing.ID, catalog.PublishInput{SubmissionID: submission.ID}, listing.Revision)
	if err != nil {
		t.Fatal(err)
	}
	token := clientSkillToken(t, "local-reader")
	// Recompose the HTTP boundary over the same retained state with a lower
	// admission budget. It must reject a new large body without filtering or
	// truncating a previously admitted record on its authorized read path.
	lower, err := httpserver.New(httpserver.Dependencies{Catalog: runtime.Catalog, Identity: runtime.Identity, Federation: runtime.Federation, Jobs: runtime.Jobs,
		Authenticator: runtime.Authenticator, Idempotency: idempotency.NewMemoryStore(), MaxBodyBytes: 1024,
		RequestTimeout: 20 * time.Second, CursorKey: bytes.Repeat([]byte("c"), 32)})
	if err != nil {
		t.Fatal(err)
	}
	lowerServer := httptest.NewServer(lower.Handler())
	defer lowerServer.Close()
	request, err = http.NewRequestWithContext(t.Context(), http.MethodPost, lowerServer.URL+"/api/v1/resources/"+resource.ID+"/versions", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer local-admin")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "lower-budget-new-admission")
	denied, err := lowerServer.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var problem struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	decodeErr := json.NewDecoder(io.LimitReader(denied.Body, 4096)).Decode(&problem)
	if err := denied.Body.Close(); err != nil {
		t.Fatal(err)
	}
	// The existing Control contract maps over-limit command bodies to 400.
	if decodeErr != nil || denied.StatusCode != http.StatusBadRequest || problem.Code != "INVALID_ARGUMENT" || problem.Detail != "request body exceeds the configured limit" {
		t.Fatalf("lower admission budget not enforced: status=%d code=%s error=%v", denied.StatusCode, problem.Code, decodeErr)
	}
	page, err := clientSkillCall(t, t.Context(), lowerServer.URL, token, "search", "--tag", "large-exact")
	if err != nil || len(page.Data) > 16<<10 {
		t.Fatalf("compact Search failed: bytes=%d error=%v", len(page.Data), err)
	}
	var results catalog.CatalogSearchResponse
	if err := json.Unmarshal(page.Data, &results); err != nil {
		t.Fatal(err)
	}
	if len(results.Items) != 1 || results.Items[0].Local == nil || results.Items[0].Local.Release.ID != release.ID {
		t.Fatal("large release lost from Search")
	}
	if _, err := clientSkillCall(t, t.Context(), lowerServer.URL, token, "record", "--id", release.ID); err == nil {
		t.Fatal("default byte ceiling not enforced")
	}
	reply, err := clientSkillCall(t, t.Context(), lowerServer.URL, token, "record", "--id", release.ID, "--max-response-bytes", "67108864")
	if err != nil {
		t.Fatal(err)
	}
	var record catalog.CatalogRecord
	if err := json.Unmarshal(reply.Data, &record); err != nil {
		t.Fatal(err)
	}
	if len(record.VersionRevision.Representations) != 1 || !bytes.Equal(record.VersionRevision.Representations[0].Data, created.Revision.Representations[0].Data) || record.VersionRevision.ContentSetDigest != created.Revision.ContentSetDigest {
		t.Fatal("full retained Card changed")
	}
	if err := catalog.ValidateVersionRevisionIntegrity(record.VersionRevision, record.Evidence); err != nil {
		t.Fatal(err)
	}
}
