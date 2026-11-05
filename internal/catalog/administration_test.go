package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

type administrationIDs struct{ next int }

func (g *administrationIDs) New(time.Time) (string, error) {
	g.next++
	return fmt.Sprintf("00000000-0000-7000-8000-%012d", g.next), nil
}

type administrationValidator struct{}

func (administrationValidator) Validate(context.Context, Kind, RepresentationInput) ValidationResult {
	return ValidationResult{ProfileID: "administration-test", NormalizedFacts: map[string]any{}, Unknown: map[string]any{}}
}

func TestCatalogMutationTimesDoNotRegressAcrossReplicaClocks(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	repository := NewMemoryRepository()
	ids := &administrationIDs{}
	ahead := &guardrailClock{now: time.Date(2026, 9, 1, 12, 0, 0, 123456789, time.UTC)}
	behind := &guardrailClock{now: ahead.now.Add(-time.Second)}
	newService := func(clock Clock) *Service {
		service, err := NewService(Options{Repository: repository, Clock: clock, IDs: ids, Validator: administrationValidator{}, CursorKey: []byte("01234567890123456789012345678901"), Policy: "administration-test"})
		if err != nil {
			t.Fatal(err)
		}
		return service
	}
	first, second := newService(ahead), newService(behind)
	actor := identity.Actor{TenantID: tenantID, Subject: "admin", RealmID: "00000000-0000-7000-8000-000000000091", MembershipID: "00000000-0000-7000-8000-000000000092", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin, identity.RoleContributor, identity.RoleReader}}
	ctx := identityfixture.WithActor(t.Context(), actor)
	contributorContext := identityfixture.WithActor(t.Context(), identity.Actor{MembershipID: "00000000-0000-7000-8000-0000f5120eb8", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: tenantID, Subject: "contributor", Roles: []identity.Role{identity.RoleContributor, identity.RoleReader}})
	reviewerContext := identityfixture.WithActor(t.Context(), identity.Actor{MembershipID: "00000000-0000-7000-8000-000075bea508", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: tenantID, Subject: "reviewer", Roles: []identity.Role{identity.RoleReviewer}})
	publisherContext := identityfixture.WithActor(t.Context(), identity.Actor{MembershipID: "00000000-0000-7000-8000-0000a7a77f47", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: tenantID, Subject: "publisher", Roles: []identity.Role{identity.RolePublisher}})

	namespace, err := first.CreateNamespace(ctx, CreateNamespaceInput{Slug: "clock-skew", DisplayName: "Before"})
	if err != nil {
		t.Fatal(err)
	}
	name := "After"
	updatedNamespace, err := second.UpdateNamespace(ctx, namespace.ID, PatchNamespaceInput{DisplayName: &name, Reason: "Exercise a replica with an earlier clock"}, namespace.Revision)
	if err != nil || updatedNamespace.UpdatedAt.Before(namespace.UpdatedAt) || updatedNamespace.UpdatedAt.Nanosecond()%1000 != 0 {
		t.Fatalf("namespace update time = %s after %s, err=%v", updatedNamespace.UpdatedAt, namespace.UpdatedAt, err)
	}

	publisher, err := first.CreatePublisher(ctx, CreatePublisherInput{DisplayName: "Before", Domains: []string{"clock-skew.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	publisher, err = second.UpdatePublisher(ctx, publisher.ID, PatchPublisherInput{DisplayName: &name, Reason: "Exercise a replica with an earlier clock"}, publisher.Revision)
	if err != nil || publisher.UpdatedAt.Before(ahead.now) || publisher.UpdatedAt.Nanosecond()%1000 != 0 {
		t.Fatalf("publisher update time = %s after %s, err=%v", publisher.UpdatedAt, ahead.now, err)
	}
	prior := publisher.UpdatedAt
	publisher, err = second.ApprovePublisherPublicationDomain(ctx, publisher.ID, "clock-skew.example.com", "Approve from the earlier replica", publisher.Revision)
	if err != nil || publisher.UpdatedAt.Before(prior) {
		t.Fatalf("publisher approval time = %s after %s, err=%v", publisher.UpdatedAt, prior, err)
	}
	prior = publisher.UpdatedAt
	revoked, err := second.RevokePublisherPublicationDomain(ctx, publisher.ID, "clock-skew.example.com", "Revoke from the earlier replica", publisher.Revision)
	if err != nil || revoked.Publisher.UpdatedAt.Before(prior) {
		t.Fatalf("publisher revocation time = %s after %s, err=%v", revoked.Publisher.UpdatedAt, prior, err)
	}

	deletable, err := first.CreateResource(ctx, CreateResourceInput{NamespaceID: namespace.ID, CanonicalIdentifier: "urn:air:clock-skew.example.com:default:delete", Kind: KindAgent})
	if err != nil {
		t.Fatal(err)
	}
	if err := second.DeleteResource(ctx, deletable.ID, "Delete from the earlier replica", deletable.Revision); err != nil {
		t.Fatal(err)
	}
	deleted := repository.resources[deletable.ID]
	if deleted.DeletedAt == nil || deleted.UpdatedAt.Before(deletable.UpdatedAt) || deleted.DeletedAt.Before(deletable.UpdatedAt) {
		t.Fatalf("resource deletion time = %#v after %s", deleted, deletable.UpdatedAt)
	}

	resource, err := first.CreateResource(contributorContext, CreateResourceInput{NamespaceID: namespace.ID, CanonicalIdentifier: "urn:air:clock-skew.example.com:default:lifecycle", Kind: KindAgent})
	if err != nil {
		t.Fatal(err)
	}
	version, _, _, err := first.CreateVersion(contributorContext, resource.ID, CreateVersionInput{Version: "1.0.0", Representations: []RepresentationInput{{Role: RepresentationPrimary, MediaType: "application/json", Data: json.RawMessage(`{"name":"before"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	versionPrior := version.UpdatedAt
	version, versionRevision, _, err := second.CreateVersionRevision(contributorContext, version.ID, []RepresentationInput{{Role: RepresentationPrimary, MediaType: "application/json", Data: json.RawMessage(`{"name":"after"}`)}}, version.Revision)
	if err != nil || version.UpdatedAt.Before(versionPrior) || versionRevision.CreatedAt != version.UpdatedAt {
		t.Fatalf("version revision time = %#v / %#v, err=%v", version, versionRevision, err)
	}
	versionPrior = version.UpdatedAt
	listing, err := first.CreateListing(contributorContext, CreateListingInput{NamespaceID: namespace.ID, ResourceID: resource.ID})
	if err != nil {
		t.Fatal(err)
	}
	listingPrior := listing.UpdatedAt
	listing, _, err = second.CreateListingRevision(contributorContext, listing.ID, CreateListingRevisionInput{VersionRevisionID: versionRevision.ID, DisplayName: "Clock skew", Description: "Exercise lifecycle writes from an earlier replica clock.", PublisherID: publisher.ID, Visibility: VisibilityTenant}, listing.Revision)
	if err != nil || listing.UpdatedAt.Before(listingPrior) {
		t.Fatalf("listing revision time = %#v, err=%v", listing, err)
	}
	submission, err := first.Submit(contributorContext, listing.ID)
	if err != nil {
		t.Fatal(err)
	}
	submission, version, err = second.DecideSubmission(reviewerContext, submission.ID, "approve", "Approve from the earlier replica", submission.Revision)
	if err != nil || submission.DecidedAt == nil || submission.DecidedAt.Before(submission.SubmittedAt) || version.UpdatedAt.Before(versionPrior) {
		t.Fatalf("review decision time = %#v / %#v, err=%v", submission, version, err)
	}
	listingPrior = listing.UpdatedAt
	listing, release, _, err := second.Publish(publisherContext, listing.ID, PublishInput{SubmissionID: submission.ID}, listing.Revision)
	if err != nil || listing.UpdatedAt.Before(listingPrior) || release.UpdatedAt.Before(listingPrior) {
		t.Fatalf("publication time = %#v / %#v, err=%v", listing, release, err)
	}

	ahead.now = ahead.now.Add(2 * time.Second)
	deprecated, err := first.TransitionRelease(publisherContext, release.ID, ReleaseDeprecated, "Deprecate from the later replica", "", nil, release.Revision)
	if err != nil || deprecated.UpdatedAt.Before(release.UpdatedAt) {
		t.Fatalf("deprecation time = %#v, err=%v", deprecated, err)
	}
	_, newVersionRevision, _, err := first.CreateVersion(contributorContext, resource.ID, CreateVersionInput{Version: "2.0.0", Representations: []RepresentationInput{{Role: RepresentationPrimary, MediaType: "application/json", Data: json.RawMessage(`{"name":"replacement"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	listing, _, err = second.CreateListingRevision(contributorContext, listing.ID, CreateListingRevisionInput{VersionRevisionID: newVersionRevision.ID, DisplayName: "Clock skew replacement", Description: "Exercise replacement from an earlier replica clock.", PublisherID: publisher.ID, Visibility: VisibilityTenant}, listing.Revision)
	if err != nil {
		t.Fatal(err)
	}
	replacementSubmission, err := first.Submit(contributorContext, listing.ID)
	if err != nil {
		t.Fatal(err)
	}
	replacementSubmission, decidedVersion, err := second.DecideSubmission(reviewerContext, replacementSubmission.ID, "approve", "Approve the replacement from the earlier replica", replacementSubmission.Revision)
	if err != nil || replacementSubmission.DecidedAt == nil || decidedVersion.UpdatedAt.Before(decidedVersion.CreatedAt) {
		t.Fatalf("replacement decision time = %#v / %#v, err=%v", replacementSubmission, decidedVersion, err)
	}
	listing, replacement, retiredPrior, err := second.Publish(publisherContext, listing.ID, PublishInput{SubmissionID: replacementSubmission.ID}, listing.Revision)
	if err != nil || retiredPrior == nil || retiredPrior.ID != deprecated.ID || retiredPrior.UpdatedAt.Before(deprecated.UpdatedAt) || replacement.UpdatedAt.Before(deprecated.UpdatedAt) || listing.UpdatedAt.Before(deprecated.UpdatedAt) {
		t.Fatalf("replacement publication time = %#v / %#v / %#v, err=%v", listing, replacement, retiredPrior, err)
	}
	retired, err := second.TransitionRelease(publisherContext, replacement.ID, ReleaseRetired, "Retire from the earlier replica", "", nil, replacement.Revision)
	if err != nil || retired.UpdatedAt.Before(replacement.UpdatedAt) {
		t.Fatalf("release retirement time = %#v, err=%v", retired, err)
	}
}

func TestPracticalCatalogAdministrationUpdatesAndDeletesOnlySafeDrafts(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	clock := &guardrailClock{now: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)}
	repository := NewMemoryRepository()
	service, err := NewService(Options{Repository: repository, Clock: clock, IDs: &administrationIDs{}, Validator: administrationValidator{}, CursorKey: []byte("01234567890123456789012345678901"), Policy: "administration-test"})
	if err != nil {
		t.Fatal(err)
	}
	admin := identity.Actor{TenantID: tenantID, Subject: "admin", RealmID: "00000000-0000-7000-8000-000000000091", MembershipID: "00000000-0000-7000-8000-000000000092", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}}
	creator := identity.Actor{MembershipID: "00000000-0000-7000-8000-000086346ccf", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: tenantID, Subject: "creator", Roles: []identity.Role{identity.RoleContributor, identity.RoleReader}}
	other := identity.Actor{MembershipID: "00000000-0000-7000-8000-0000e5600e55", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: tenantID, Subject: "other", Roles: []identity.Role{identity.RoleContributor, identity.RoleReader}}
	adminContext := identityfixture.WithActor(t.Context(), admin)
	creatorContext := identityfixture.WithActor(t.Context(), creator)
	otherContext := identityfixture.WithActor(t.Context(), other)

	namespace, err := service.CreateNamespace(adminContext, CreateNamespaceInput{Slug: "stable", DisplayName: "Original", Description: "Original description"})
	if err != nil {
		t.Fatal(err)
	}
	displayName := " Updated namespace "
	description := ""
	updatedNamespace, err := service.UpdateNamespace(adminContext, namespace.ID, PatchNamespaceInput{DisplayName: &displayName, Description: &description, Reason: "Correct the tenant-facing metadata"}, namespace.Revision)
	if err != nil || updatedNamespace.DisplayName != "Updated namespace" || updatedNamespace.Description != "" || updatedNamespace.Slug != namespace.Slug || updatedNamespace.Revision != 2 {
		t.Fatalf("updated namespace = %#v, %v", updatedNamespace, err)
	}
	if _, err := service.UpdateNamespace(adminContext, namespace.ID, PatchNamespaceInput{DisplayName: &displayName, Reason: "Stale update"}, namespace.Revision); ErrorCodeOf(err) != CodePreconditionFailed {
		t.Fatalf("stale namespace update returned %v", err)
	}
	if _, err := service.UpdateNamespace(adminContext, namespace.ID, PatchNamespaceInput{DisplayName: &displayName, Description: &description, Reason: "No semantic change"}, updatedNamespace.Revision); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("no-op namespace update returned %v", err)
	}
	if _, err := service.UpdateNamespace(otherContext, namespace.ID, PatchNamespaceInput{DisplayName: &displayName, Reason: "Unauthorized"}, updatedNamespace.Revision); ErrorCodeOf(err) != CodeForbidden {
		t.Fatalf("non-administrator namespace update returned %v", err)
	}

	publisher, err := service.CreatePublisher(creatorContext, CreatePublisherInput{DisplayName: "Publisher", Domains: []string{"catalog.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	newPublisherName := "Updated publisher"
	newDomains := []string{"CATALOG.EXAMPLE.COM.", "new.example.com"}
	publisher, err = service.UpdatePublisher(adminContext, publisher.ID, PatchPublisherInput{DisplayName: &newPublisherName, Domains: &newDomains, Reason: "Add the new declared domain"}, publisher.Revision)
	if err != nil || publisher.DisplayName != newPublisherName || len(publisher.Domains) != 2 || publisher.Domains[1] != "new.example.com" {
		t.Fatalf("updated publisher = %#v, %v", publisher, err)
	}
	if _, err := service.UpdatePublisher(adminContext, publisher.ID, PatchPublisherInput{DisplayName: &newPublisherName, Domains: &newDomains, Reason: "No semantic change"}, publisher.Revision); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("no-op publisher update returned %v", err)
	}
	publisher, err = service.ApprovePublisherPublicationDomain(adminContext, publisher.ID, "catalog.example.com", "Approved publication domain", publisher.Revision)
	if err != nil {
		t.Fatal(err)
	}
	removeApproved := []string{"new.example.com"}
	if _, err := service.UpdatePublisher(adminContext, publisher.ID, PatchPublisherInput{Domains: &removeApproved, Reason: "Cannot bypass approval revocation"}, publisher.Revision); ErrorCodeOf(err) != CodeConflict {
		t.Fatalf("approved domain removal returned %v", err)
	}
	revocation, err := service.RevokePublisherPublicationDomain(adminContext, publisher.ID, "catalog.example.com", "Retire publication authority", publisher.Revision)
	if err != nil {
		t.Fatal(err)
	}
	revocationAudit := repository.audit[len(repository.audit)-1]
	if err := audit.ValidateNewRecord(revocationAudit); err != nil {
		t.Fatalf("publisher-domain revocation audit = %#v, %v", revocationAudit, err)
	}
	if facts, ok := revocationAudit.Details["facts"].(map[string]any); !ok || facts["affectedReleaseCount"] == nil || facts["affectedReleaseDigest"] == nil {
		t.Fatalf("publisher-domain revocation facts = %#v", revocationAudit.Details)
	}
	publisher, err = service.UpdatePublisher(adminContext, publisher.ID, PatchPublisherInput{Domains: &removeApproved, Reason: "Remove retired declaration"}, revocation.Publisher.Revision)
	if err != nil || len(publisher.Domains) != 1 || publisher.Domains[0] != "new.example.com" {
		t.Fatalf("publisher domain removal = %#v, %v", publisher, err)
	}
	if _, err := service.CreatePublisher(creatorContext, CreatePublisherInput{DisplayName: "Conflict", Domains: []string{"new.example.com"}}); ErrorCodeOf(err) != CodeConflict {
		t.Fatalf("overlapping publisher domain returned %v", err)
	}

	resource, err := service.CreateResource(creatorContext, CreateResourceInput{NamespaceID: namespace.ID, CanonicalIdentifier: "urn:air:new.example.com:stable:deletable", Kind: KindAgent})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteResource(t.Context(), resource.ID, "Anonymous deletion", resource.Revision); ErrorCodeOf(err) != CodeForbidden {
		t.Fatalf("anonymous draft deletion returned %v", err)
	}
	if err := service.DeleteResource(creatorContext, "not-a-uuid", "Invalid identifier", resource.Revision); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("invalid draft identifier returned %v", err)
	}
	if err := service.DeleteResource(creatorContext, resource.ID, "", resource.Revision); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("blank draft deletion reason returned %v", err)
	}
	lostAccessContext := identityfixture.WithActor(t.Context(), identity.Actor{MembershipID: "00000000-0000-7000-8000-0000cad81c79", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: tenantID, Subject: creator.Subject, Roles: []identity.Role{identity.RoleReader}})
	if err := service.DeleteResource(lostAccessContext, resource.ID, "Creator no longer has namespace access", resource.Revision); ErrorCodeOf(err) != CodeForbidden {
		t.Fatalf("creator without current contributor access returned %v", err)
	}
	version, versionRevision, _, err := service.CreateVersion(creatorContext, resource.ID, CreateVersionInput{Version: "1.0.0", Representations: []RepresentationInput{{Role: RepresentationPrimary, MediaType: "text/plain", Content: "retained unpublished draft"}}})
	if err != nil {
		t.Fatal(err)
	}
	artifactID := versionRevision.Representations[0].ArtifactID
	if artifact, _, err := service.GetArtifact(creatorContext, artifactID); err != nil || artifact.ID != artifactID {
		t.Fatalf("active repository-backed draft artifact = %#v, %v", artifact, err)
	}
	if err := service.DeleteResource(otherContext, resource.ID, "Not the creator", resource.Revision); ErrorCodeOf(err) != CodeForbidden {
		t.Fatalf("unowned draft deletion returned %v", err)
	}
	if err := service.DeleteResource(creatorContext, resource.ID, "Stale", resource.Revision+1); ErrorCodeOf(err) != CodePreconditionFailed {
		t.Fatalf("stale draft deletion returned %v", err)
	}
	if err := service.DeleteResource(creatorContext, resource.ID, "Draft is no longer needed", resource.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetResource(identityfixture.WithActor(t.Context(), identity.Actor{MembershipID: "00000000-0000-7000-8000-0000869c4bfc", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: tenantID, Subject: "reader", Roles: []identity.Role{identity.RoleReader}}), resource.ID); ErrorCodeOf(err) != CodeNotFound {
		t.Fatalf("deleted resource read returned %v", err)
	}
	if _, err := service.GetVersion(creatorContext, version.ID); ErrorCodeOf(err) != CodeNotFound {
		t.Fatalf("deleted resource version read returned %v", err)
	}
	if _, _, err := service.GetArtifact(creatorContext, artifactID); ErrorCodeOf(err) != CodeNotFound || !errors.Is(err, ErrInactiveDocument) {
		t.Fatalf("deleted resource document read returned %v", err)
	}
	if resources, err := service.ListResources(identityfixture.WithActor(t.Context(), identity.Actor{MembershipID: "00000000-0000-7000-8000-0000869c4bfc", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: tenantID, Subject: "reader", Roles: []identity.Role{identity.RoleReader}}), ResourceListRequest{Limit: 20}); err != nil || len(resources) != 0 {
		t.Fatalf("resources after deletion = %#v, %v", resources, err)
	}
	if _, err := service.CreateResource(creatorContext, CreateResourceInput{NamespaceID: namespace.ID, CanonicalIdentifier: resource.CanonicalIdentifier, Kind: KindAgent}); ErrorCodeOf(err) != CodeConflict {
		t.Fatalf("deleted canonical identifier was reused: %v", err)
	}

	blocked := func(name string) Resource {
		t.Helper()
		item, createErr := service.CreateResource(creatorContext, CreateResourceInput{NamespaceID: namespace.ID, CanonicalIdentifier: "urn:air:new.example.com:stable:" + name, Kind: KindAgent})
		if createErr != nil {
			t.Fatal(createErr)
		}
		return item
	}
	adopted := blocked("adopted")
	repository.versions["adopted-version"] = ResourceVersion{ID: "adopted-version", TenantID: tenantID, ResourceID: adopted.ID}
	repository.versionRevisions["adopted-revision"] = VersionRevision{ID: "adopted-revision", TenantID: tenantID, ResourceVersionID: "adopted-version", Adoption: &AdoptionProvenance{ObservationID: "retained"}}
	if err := service.DeleteResource(creatorContext, adopted.ID, "Cannot erase provenance", adopted.Revision); ErrorCodeOf(err) != CodeConflict {
		t.Fatalf("adopted draft deletion returned %v", err)
	}
	reviewed := blocked("reviewed")
	repository.listings["reviewed-listing"] = CatalogListing{ID: "reviewed-listing", TenantID: tenantID, ResourceID: reviewed.ID}
	repository.submissions["reviewed-submission"] = ReviewSubmission{ID: "reviewed-submission", TenantID: tenantID, ListingID: "reviewed-listing"}
	if err := service.DeleteResource(creatorContext, reviewed.ID, "Cannot erase review", reviewed.Revision); ErrorCodeOf(err) != CodeConflict {
		t.Fatalf("reviewed draft deletion returned %v", err)
	}
	released := blocked("released")
	repository.listings["released-listing"] = CatalogListing{ID: "released-listing", TenantID: tenantID, ResourceID: released.ID}
	repository.releases["release"] = ListingRelease{ID: "release", TenantID: tenantID, ListingID: "released-listing"}
	if err := service.DeleteResource(creatorContext, released.ID, "Cannot erase release history", released.Revision); ErrorCodeOf(err) != CodeConflict {
		t.Fatalf("released draft deletion returned %v", err)
	}
	adminOwned := blocked("admin-delete")
	if err := service.DeleteResource(adminContext, adminOwned.ID, "Tenant administrator cleanup", adminOwned.Revision); err != nil {
		t.Fatalf("tenant administrator deletion returned %v", err)
	}
}

func TestPublisherUpdatesPreserveTenantAndActivePublicationBoundaries(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	const otherTenantID = "00000000-0000-7000-8000-000000000002"
	clock := &guardrailClock{now: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)}
	repository := NewMemoryRepository()
	service, err := NewService(Options{Repository: repository, Clock: clock, IDs: &administrationIDs{}, Validator: administrationValidator{}, CursorKey: []byte("01234567890123456789012345678901"), Policy: "administration-test"})
	if err != nil {
		t.Fatal(err)
	}
	adminContext := identityfixture.WithActor(t.Context(), identity.Actor{MembershipID: "00000000-0000-7000-8000-0000cbe16062", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: tenantID, Subject: "admin", Roles: []identity.Role{identity.RoleTenantAdmin}})
	contributorContext := identityfixture.WithActor(t.Context(), identity.Actor{MembershipID: "00000000-0000-7000-8000-000086346ccf", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: tenantID, Subject: "creator", Roles: []identity.Role{identity.RoleContributor, identity.RoleReader}})
	namespace, err := service.CreateNamespace(adminContext, CreateNamespaceInput{Slug: "stable", DisplayName: "Stable"})
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := service.CreatePublisher(contributorContext, CreatePublisherInput{DisplayName: "Primary publisher", Domains: []string{"active.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	otherPublisher, err := service.CreatePublisher(contributorContext, CreatePublisherInput{DisplayName: "Other publisher", Domains: []string{"other.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	overlap := []string{"active.example.com", otherPublisher.Domains[0]}
	if _, err := service.UpdatePublisher(adminContext, publisher.ID, PatchPublisherInput{Domains: &overlap, Reason: "Cannot acquire another publisher domain"}, publisher.Revision); ErrorCodeOf(err) != CodeConflict {
		t.Fatalf("cross-publisher domain update returned %v", err)
	}

	resource, err := service.CreateResource(contributorContext, CreateResourceInput{NamespaceID: namespace.ID, CanonicalIdentifier: "urn:air:active.example.com:stable:published", Kind: KindAgent})
	if err != nil {
		t.Fatal(err)
	}
	repository.listings["listing"] = CatalogListing{ID: "listing", TenantID: tenantID, NamespaceID: namespace.ID, ResourceID: resource.ID, CurrentReleaseID: "release", Revision: 1}
	repository.listingRevisions["listing-revision"] = ListingRevision{ID: "listing-revision", TenantID: tenantID, ListingID: "listing", PublisherID: publisher.ID}
	repository.releases["release"] = ListingRelease{ID: "release", TenantID: tenantID, ListingID: "listing", ListingRevisionID: "listing-revision", State: ReleasePublished, Revision: 1}
	replacement := []string{"replacement.example.com"}
	if _, err := service.UpdatePublisher(adminContext, publisher.ID, PatchPublisherInput{Domains: &replacement, Reason: "Cannot remove a published domain"}, publisher.Revision); ErrorCodeOf(err) != CodeConflict {
		t.Fatalf("published domain removal returned %v", err)
	}
	release := repository.releases["release"]
	release.State = ReleaseDeprecated
	repository.releases["release"] = release
	if _, err := service.UpdatePublisher(adminContext, publisher.ID, PatchPublisherInput{Domains: &replacement, Reason: "Cannot remove a deprecated domain"}, publisher.Revision); ErrorCodeOf(err) != CodeConflict {
		t.Fatalf("deprecated domain removal returned %v", err)
	}
	release.State = ReleaseRetired
	repository.releases["release"] = release
	updated, err := service.UpdatePublisher(adminContext, publisher.ID, PatchPublisherInput{Domains: &replacement, Reason: "Retired publication no longer uses the domain"}, publisher.Revision)
	if err != nil || len(updated.Domains) != 1 || updated.Domains[0] != replacement[0] || updated.Revision != 2 {
		t.Fatalf("retired publisher domain update = %#v, %v", updated, err)
	}
	if _, err := service.UpdatePublisher(adminContext, publisher.ID, PatchPublisherInput{Domains: &replacement, Reason: "Stale update"}, publisher.Revision); ErrorCodeOf(err) != CodePreconditionFailed {
		t.Fatalf("stale publisher update returned %v", err)
	}
	otherTenantContext := identityfixture.WithActor(t.Context(), identity.Actor{MembershipID: "00000000-0000-7000-8000-0000644b3fd7", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: otherTenantID, Subject: "other-admin", Roles: []identity.Role{identity.RoleTenantAdmin}})
	newName := "Cross-tenant update"
	if _, err := service.UpdatePublisher(otherTenantContext, publisher.ID, PatchPublisherInput{DisplayName: &newName, Reason: "Must remain isolated"}, updated.Revision); ErrorCodeOf(err) != CodeNotFound {
		t.Fatalf("cross-tenant publisher update returned %v", err)
	}
}
