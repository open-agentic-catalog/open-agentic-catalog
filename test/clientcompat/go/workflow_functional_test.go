//go:build functional

package clientcompat_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	client "github.com/open-agentic-catalog/open-agentic-catalog/test/clientcompat/go/generated"
)

// The generated client owns each tested exchange. Domain calls below prepare
// published search fixtures without duplicating the full HTTP publication suite.
func TestGeneratedConsumerWorkflow(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	admin := f.client("admin")
	reader := f.client("reader")
	r, err := reader.GetCapabilities(ctx)
	data := f.result(r, err, 200)
	parsed, err := client.ParseGetCapabilitiesResponse(&http.Response{StatusCode: http.StatusOK, Header: r.Header, Body: io.NopCloser(bytes.NewReader(data))})
	must(t, err)
	if parsed.JSON200 == nil {
		t.Fatal("capabilities branch absent")
	}
	r, err = reader.SearchCatalog(ctx, client.SearchCatalogJSONRequestBody{Limit: ptr(1)})
	empty := decode[client.CatalogSearchResponse](t, f.result(r, err, 200))
	if len(empty.Items) != 0 {
		t.Fatal("fixture not empty")
	}
	r, err = admin.CreateNamespace(ctx, &client.CreateNamespaceParams{IdempotencyKey: "namespace"}, client.CreateNamespaceJSONRequestBody{Slug: "compat", DisplayName: "Compatibility"})
	ns := decode[client.Namespace](t, f.result(r, err, 201))
	etag := r.Header.Get("ETag")
	params := &client.UpdateNamespaceParams{IdempotencyKey: "namespace-update", IfMatch: etag}
	update := client.UpdateNamespaceJSONRequestBody{DisplayName: ptr("Updated name"), Reason: "Client compatibility"}
	r, err = admin.UpdateNamespace(ctx, ns.Id, params, update)
	first := f.result(r, err, 200)
	nextETag := r.Header.Get("ETag")
	if etag == nextETag {
		t.Fatal("revision did not advance")
	}
	r, err = admin.UpdateNamespace(ctx, ns.Id, params, update)
	replay := f.result(r, err, 200)
	if !bytes.Equal(first, replay) || r.Header.Get("ETag") != nextETag || r.Header.Get("Idempotency-Replayed") != "true" {
		t.Fatal("update replay changed")
	}
	params.IdempotencyKey = "stale-update"
	r, err = admin.UpdateNamespace(ctx, ns.Id, params, update)
	problem := decode[client.Problem](t, f.result(r, err, 412))
	if problem.Code != "PRECONDITION_FAILED" {
		t.Fatal(problem.Code)
	}
	r, err = reader.CreateNamespace(ctx, &client.CreateNamespaceParams{IdempotencyKey: "forbidden"}, client.CreateNamespaceJSONRequestBody{Slug: "denied", DisplayName: "Denied"})
	if problem := decode[client.Problem](t, f.result(r, err, 403)); problem.Code != client.ErrorCode(catalog.CodeForbidden) {
		t.Fatal("unexpected insufficient-role code: " + problem.Code)
	}
	r, err = f.client("bad-token").GetCapabilities(ctx)
	if problem := decode[client.Problem](t, f.result(r, err, 401)); problem.Code != client.ErrorCode(catalog.CodeUnauthenticated) {
		t.Fatal("unexpected invalid-token code: " + problem.Code)
	}

	// A real standards document with nested forward-compatible evidence tests
	// the lossless raw write and persisted read, not just request construction.
	resource, err := admin.CreateResource(ctx, &client.CreateResourceParams{IdempotencyKey: "agent"}, client.CreateResourceJSONRequestBody{NamespaceId: ns.Id, Kind: client.ResourceKind("agent"), CanonicalIdentifier: "urn:air:catalog.example.com:compat:agent"})
	res := decode[client.Resource](t, f.result(resource, err, 201))
	card := `{"name":"Compatibility Agent","description":"Compatibility evidence","version":"1.0.0","supportedInterfaces":[{"url":"https://agents.example.com/compat","protocolBinding":"JSONRPC","protocolVersion":"1.0"}],"capabilities":{"extensions":[{"uri":"https://example.com/compat","params":{"future":{"large":9007199254740993,"nested":[null,false,""]}}}]},"defaultInputModes":["application/json"],"defaultOutputModes":["application/json"],"skills":[{"id":"compat","name":"Compatibility","description":"Find evidence","tags":["compat"]}]}`
	body := `{"version":"1.0.0","representations":[{"role":"primary","mediaType":"application/a2a-agent-card+json","data":` + card + `}]}`
	r, err = admin.CreateResourceVersionWithBody(ctx, res.Id, &client.CreateResourceVersionParams{IdempotencyKey: "version"}, "application/json", strings.NewReader(body))
	versionData := f.result(r, err, 201)
	version := decode[client.VersionEvidenceEnvelope](t, versionData)
	if version.Evidence.State != "complete" {
		t.Fatalf("unexpected evidence: %s", versionData)
	}
	r, err = admin.GetVersionRevision(ctx, version.Revision.Id)
	retained := f.result(r, err, 200)
	retainedEnvelope := decode[client.RevisionEvidenceEnvelope](t, retained)
	if retainedEnvelope.NamespaceId == nil || *retainedEnvelope.NamespaceId != ns.Id {
		t.Fatal("revision response lost its canonical namespace")
	}
	if !bytes.Contains(retained, []byte("9007199254740993")) || !bytes.Contains(retained, []byte(`"nested":[null,false,""]`)) {
		t.Fatalf("raw evidence changed: %s", retained)
	}
	var envelope struct {
		Revision struct {
			Representations []struct {
				Data json.RawMessage `json:"data"`
			} `json:"representations"`
		} `json:"revision"`
	}
	must(t, json.Unmarshal(retained, &envelope))
	if len(envelope.Revision.Representations) != 1 {
		t.Fatal("representation missing")
	}
	// Inline JSON is canonically encoded by the Catalog; compare exact numeric
	// tokens and nested values without assuming object-key ordering is retained.
	if !reflect.DeepEqual(exactJSON(t, envelope.Revision.Representations[0].Data), exactJSON(t, []byte(card))) {
		t.Fatal("retained JSON values changed")
	}

	// Two governed listings provide one genuine continuation and exact record
	// reads; no public snapshot or additional kind-specific package setup.
	actor := f.actor("admin")
	publisher, err := f.service.CreatePublisher(actor, catalog.CreatePublisherInput{DisplayName: "Publisher", Domains: []string{"catalog.example.com"}})
	must(t, err)
	_, err = f.service.ApprovePublisherPublicationDomain(actor, publisher.ID, "catalog.example.com", "Fixture domain", publisher.Revision)
	must(t, err)
	want := map[string]bool{}
	identifiers := map[string]string{}
	var releaseIDs []uuid.UUID
	for i := range 2 {
		resourceID, revisionID := res.Id.String(), version.Revision.Id.String()
		identifier := "urn:air:catalog.example.com:compat:agent"
		if i == 1 {
			identifier = "custom:compat:e\u0301-🙂"
			second, err := f.service.CreateResource(actor, catalog.CreateResourceInput{NamespaceID: ns.Id.String(), Kind: catalog.KindAgent, CanonicalIdentifier: identifier})
			must(t, err)
			_, revision, _, err := f.service.CreateVersion(actor, second.ID, catalog.CreateVersionInput{Version: "1.0.0", Representations: []catalog.RepresentationInput{{Role: "primary", MediaType: "application/a2a-agent-card+json", Data: json.RawMessage(card)}}})
			must(t, err)
			resourceID, revisionID = second.ID, revision.ID
		}
		listing, err := f.service.CreateListing(actor, catalog.CreateListingInput{NamespaceID: ns.Id.String(), ResourceID: resourceID})
		must(t, err)
		listing, _, err = f.service.CreateListingRevision(actor, listing.ID, catalog.CreateListingRevisionInput{VersionRevisionID: revisionID, DisplayName: fmt.Sprintf("Listing %d", i), Description: "Compatibility", PublisherID: publisher.ID, Visibility: catalog.VisibilityTenant, Tags: []string{"compat"}, Extensions: map[string]any{"detail": "listing-detail-sentinel"}}, listing.Revision)
		must(t, err)
		submission, err := f.service.Submit(actor, listing.ID)
		must(t, err)
		_, _, err = f.service.DecideSubmission(f.actor("reviewer"), submission.ID, "approve", "Reviewed test", submission.Revision)
		must(t, err)
		listing, err = f.service.GetListing(actor, listing.ID)
		must(t, err)
		_, release, _, err := f.service.Publish(actor, listing.ID, catalog.PublishInput{SubmissionID: submission.ID}, listing.Revision)
		must(t, err)
		want[release.ID] = true
		identifiers[release.ID] = identifier
		releaseIDs = append(releaseIDs, uuid.MustParse(release.ID))
	}
	publication, overflow, err := f.service.PublicationRecords(actor, tenant, true, 3)
	must(t, err)
	if overflow || len(publication) != 1 || publication[0].Resource.CanonicalIdentifier != "urn:air:catalog.example.com:compat:agent" {
		t.Fatal("external publication selection admitted the opaque local release")
	}
	var cursor *string
	for range 2 {
		r, err = reader.SearchCatalog(ctx, client.SearchCatalogJSONRequestBody{Limit: ptr(1), Cursor: cursor})
		page := decode[client.CatalogSearchResponse](t, f.result(r, err, 200))
		if len(page.Items) != 1 || page.Items[0].Local == nil || page.Partial {
			t.Fatalf("unexpected page: %+v", page)
		}
		id := page.Items[0].Local.Release.Id
		if !want[id.String()] {
			t.Fatal("duplicate or unexpected release")
		}
		delete(want, id.String())
		if page.Items[0].Identifier != identifiers[id.String()] || page.Items[0].Local.Resource.CanonicalIdentifier != identifiers[id.String()] {
			t.Fatal("Search changed the local release identity")
		}
		cursor = page.NextCursor
		r, err = reader.GetCatalogRecord(ctx, id)
		record := decode[client.CatalogRecord](t, f.result(r, err, 200))
		if record.Release.Id != id || record.Resource.CanonicalIdentifier != identifiers[id.String()] {
			t.Fatal("wrong exact record")
		}
		r, err = f.client("other").GetCatalogRecord(ctx, id)
		p := decode[client.Problem](t, f.result(r, err, 404))
		if p.Code != "NOT_FOUND" {
			t.Fatal(p.Code)
		}
	}
	if cursor != nil && *cursor != "" || len(want) != 0 {
		t.Fatal("incomplete pagination")
	}

	qualifyBrowse(t, f, ns.Id, releaseIDs, card)

	// Exercise the pre-release POST action without deleting published evidence.
	r, err = admin.CreateResource(ctx, &client.CreateResourceParams{IdempotencyKey: "draft"}, client.CreateResourceJSONRequestBody{NamespaceId: ns.Id, Kind: client.ResourceKind("agent"), CanonicalIdentifier: "urn:air:catalog.example.com:compat:draft"})
	draft := decode[client.Resource](t, f.result(r, err, 201))
	r, err = admin.DeleteResource(ctx, draft.Id, &client.DeleteResourceParams{IdempotencyKey: "delete", IfMatch: r.Header.Get("ETag")}, client.DeleteResourceJSONRequestBody{Reason: "Remove unused test draft"})
	f.result(r, err, 204)
}

func TestGeneratedJobRetry(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := f.client("admin")
	id := uuid.MustParse("00000000-0000-7000-8000-000000000090")
	_, _, err := f.jobStore.Enqueue(ctx, jobs.Job{ID: id.String(), TenantID: tenant, Kind: "publication.generate", Actor: "admin", PayloadVersion: 1, Payload: json.RawMessage(`{"host":"catalog.example.com"}`), State: jobs.StateFailed, NotBefore: f.now, AttemptLimit: 3, DeduplicationKey: "prepared-failure", CreatedAt: f.now, UpdatedAt: f.now})
	must(t, err)
	params := &client.RetryJobParams{IdempotencyKey: "job-retry"}
	body := client.RetryJobJSONRequestBody{Reason: "Retry prepared test failure"}
	r, err := c.RetryJob(ctx, id, params, body)
	first := f.result(r, err, 202)
	job := decode[client.Job](t, first)
	location := r.Header.Get("Location")
	if job.ParentJobId == nil || *job.ParentJobId != id || job.State != "queued" || location != "/api/v1/jobs/"+job.Id.String() {
		t.Fatal("incorrect accepted Job")
	}
	r, err = c.RetryJob(ctx, id, params, body)
	if !bytes.Equal(first, f.result(r, err, 202)) || r.Header.Get("Idempotency-Replayed") != "true" || r.Header.Get("Location") != location {
		t.Fatal("retry replay changed")
	}
	r, err = c.GetJob(ctx, job.Id)
	got := decode[client.Job](t, f.result(r, err, 200))
	if got.Id != job.Id || got.State != "queued" {
		t.Fatal("queued Job missing")
	}
}
