package adapter

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

// This fake supplies already policy-checked bytes. Transport tests independently
// exercise the hardened fetcher; no database, TLS server or component is run here.
type pluginServiceFetcher struct {
	calls  int
	body   []byte
	now    func() time.Time
	policy string
}

func (f *pluginServiceFetcher) PolicyDigest() string { return f.policy }
func (f *pluginServiceFetcher) FetchExternalRepresentation(_ context.Context, request catalog.ExternalRepresentationFetchRequest) (catalog.ExternalRepresentationFetchResult, error) {
	f.calls++
	return catalog.ExternalRepresentationFetchResult{RequestedURL: request.URL, FinalURL: request.URL, Status: 200, MediaType: "application/zip", Body: append([]byte(nil), f.body...), ContentDigest: fmt.Sprintf("sha256:%x", sha256.Sum256(f.body)), PolicyID: "plugin-test", PolicyRevision: 1, PolicyDigest: f.policy, RetrievedAt: f.now()}, nil
}

func TestPluginServiceReviewPublicationAndArchiveDrift(t *testing.T) {
	c := clock.NewFake(time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC))
	body := pluginArchive(t, catalog.PluginMediaZIP, map[string]string{"plugin.json": pluginManifest, "skills/summarize/SKILL.md": "---\nname: summarize\ndescription: Summarize safely.\n---\nInstructions.", "mcp.json": `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"example":{"type":"stdio","command":"node"}}}`, "bin/inert.sh": "exit 97"})
	fetch := &pluginServiceFetcher{body: body, now: c.Now, policy: "sha256:" + strings.Repeat("a", 64)}
	key := sha256.Sum256([]byte(t.Name()))
	repo := catalog.NewMemoryRepository()
	service, err := catalog.NewService(catalog.Options{Repository: repo, Clock: c, IDs: id.Generator{}, Validator: NewRegistry(), ExternalFetcher: fetch, CursorKey: key[:], Policy: "plugin-test"})
	if err != nil {
		t.Fatal(err)
	}
	actor := identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", MembershipID: "00000000-0000-7000-8000-000000000002", RealmID: "00000000-0000-7000-8000-000000000003", ActorType: identity.ActorHuman, Subject: "author", Roles: []identity.Role{identity.RoleTenantAdmin, identity.RoleContributor, identity.RolePublisher, identity.RoleReader}}
	ctx := identityfixture.WithActor(t.Context(), actor)
	reviewer := actor
	reviewer.Subject = "reviewer"
	reviewer.MembershipID = "00000000-0000-7000-8000-000000000004"
	reviewer.Roles = []identity.Role{identity.RoleReviewer}
	reviewctx := identityfixture.WithActor(t.Context(), reviewer)
	ns, err := service.CreateNamespace(ctx, catalog.CreateNamespaceInput{Slug: "plugins", DisplayName: "Plugins"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := service.CreateResource(ctx, catalog.CreateResourceInput{NamespaceID: ns.ID, CanonicalIdentifier: "urn:air:catalog.example.com:plugins:tools", Kind: catalog.KindPlugin})
	if err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(body))
	input := catalog.RepresentationInput{Role: catalog.RepresentationPrimary, MediaType: catalog.PluginMediaZIP, URL: "https://packages.example.com/tools.zip", ExpectedDigest: &digest}
	for name, mutate := range map[string]func(*catalog.RepresentationInput){
		"unsupported profile":         func(r *catalog.RepresentationInput) { r.ProfileID = "other-profile" },
		"manifest instead of archive": func(r *catalog.RepresentationInput) { r.MediaType = "application/json" },
		"inline archive":              func(r *catalog.RepresentationInput) { r.URL = ""; r.PackageData = body },
		"inline JSON":                 func(r *catalog.RepresentationInput) { r.URL = ""; r.Data = json.RawMessage(`{}`) },
		"empty checksum":              func(r *catalog.RepresentationInput) { empty := ""; r.ExpectedDigest = &empty },
		"HTTP URL":                    func(r *catalog.RepresentationInput) { r.URL = "http://packages.example.com/tools.zip" },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := input
			mutate(&invalid)
			_, _, _, err := service.CreateVersion(ctx, resource.ID, catalog.CreateVersionInput{Version: "1.0.0", Representations: []catalog.RepresentationInput{invalid}})
			if catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument || fetch.calls != 0 {
				t.Fatalf("invalid input reached retrieval or wrong rejection: calls=%d error=%v", fetch.calls, err)
			}
		})
	}
	bad := input
	badDigest := "sha256:" + strings.Repeat("0", 64)
	bad.ExpectedDigest = &badDigest
	if _, _, _, err := service.CreateVersion(ctx, resource.ID, catalog.CreateVersionInput{Version: "1.0.0", Representations: []catalog.RepresentationInput{bad}}); catalog.ErrorCodeOf(err) != catalog.CodeValidationFailed {
		t.Fatalf("checksum mismatch: %v", err)
	}
	_, revision, evidence, err := service.CreateVersion(ctx, resource.ID, catalog.CreateVersionInput{Version: "1.0.0", Representations: []catalog.RepresentationInput{input}})
	if err != nil {
		t.Fatal(err)
	}
	if evidence.State != catalog.EvidenceComplete || len(revision.Representations[0].Plugin.Components) != 2 {
		t.Fatalf("evidence=%+v revision=%+v", evidence, revision)
	}
	retained, err := json.Marshal(revision.Representations)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(retained), "exit 97") || revision.Representations[0].ArtifactID != "" {
		t.Fatal("executable/archive content was retained")
	}
	loaded, _, _, err := service.GetVersionRevision(ctx, revision.ID)
	if err != nil || !reflect.DeepEqual(loaded.Representations, revision.Representations) {
		t.Fatalf("retained inspection: %v", err)
	}
	_, renewed, _, err := service.RevalidateVersionRevision(ctx, revision.ID)
	if err != nil || renewed.State != catalog.EvidenceComplete || renewed.ID == evidence.ID {
		t.Fatalf("revalidation: %+v %v", renewed, err)
	}
	publisher, err := service.CreatePublisher(ctx, catalog.CreatePublisherInput{DisplayName: "Plugins", Domains: []string{"catalog.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	publisher, err = service.ApprovePublisherPublicationDomain(ctx, publisher.ID, "catalog.example.com", "synthetic package fixture", publisher.Revision)
	if err != nil {
		t.Fatal(err)
	}
	listing, err := service.CreateListing(ctx, catalog.CreateListingInput{NamespaceID: ns.ID, ResourceID: resource.ID})
	if err != nil {
		t.Fatal(err)
	}
	listing, _, err = service.CreateListingRevision(ctx, listing.ID, catalog.CreateListingRevisionInput{VersionRevisionID: revision.ID, DisplayName: "Portable tools", Description: "Synthetic Plugin", PublisherID: publisher.ID, Visibility: catalog.VisibilityTenant}, listing.Revision)
	if err != nil {
		t.Fatal(err)
	}
	submission, err := service.Submit(ctx, listing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = service.DecideSubmission(reviewctx, submission.ID, "approve", "metadata reviewed", submission.Revision); err != nil {
		t.Fatal(err)
	}
	// These changes affect only the injected service clock and fake policy.
	// Neither stale retrievals nor a different outbound policy authorize release.
	c.Advance(25 * time.Hour)
	if _, _, _, err = service.Publish(ctx, listing.ID, catalog.PublishInput{SubmissionID: submission.ID}, listing.Revision); catalog.ErrorCodeOf(err) != catalog.CodePublicationNotReady {
		t.Fatalf("expired Plugin retrieval published: %v", err)
	}
	c.Advance(-25 * time.Hour)
	originalPolicy := fetch.policy
	fetch.policy = "sha256:" + strings.Repeat("c", 64)
	if _, _, _, err = service.Publish(ctx, listing.ID, catalog.PublishInput{SubmissionID: submission.ID}, listing.Revision); catalog.ErrorCodeOf(err) != catalog.CodePublicationNotReady {
		t.Fatalf("changed Plugin retrieval policy published: %v", err)
	}
	fetch.policy = originalPolicy
	original := fetch.body
	fetch.body = append(append([]byte(nil), original...), 0)
	if _, _, _, err = service.Publish(ctx, listing.ID, catalog.PublishInput{SubmissionID: submission.ID}, listing.Revision); catalog.ErrorCodeOf(err) != catalog.CodePublicationNotReady {
		t.Fatalf("changed archive published: %v", err)
	}
	fetch.body = original
	if _, _, _, err = service.Publish(ctx, listing.ID, catalog.PublishInput{SubmissionID: submission.ID}, listing.Revision); err != nil {
		t.Fatal(err)
	}
	found, err := service.Search(ctx, catalog.SearchQuery{Kinds: []catalog.Kind{catalog.KindPlugin}})
	if err != nil || len(found.Items) != 1 {
		t.Fatalf("Plugin search: %+v %v", found, err)
	}
	fetch.body = append(append([]byte(nil), original...), 0)
	_, blocked, _, err := service.RevalidateVersionRevision(ctx, revision.ID)
	if err != nil || blocked.State != catalog.EvidenceBlocked {
		t.Fatalf("drift revalidation: %+v %v", blocked, err)
	}
	unchanged, _, _, err := service.GetVersionRevision(ctx, revision.ID)
	after, marshalErr := json.Marshal(unchanged.Representations)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if err != nil || string(after) != string(retained) {
		t.Fatal("revalidation rewrote immutable inspection")
	}
}
