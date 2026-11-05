package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"slices"
	"strings"
	"testing"
	"time"
)

func adoptionFixture(t *testing.T) (guardrailFixture, ExternalObservationAdoptionInput) {
	t.Helper()
	fixture := newGuardrailFixture(t)
	namespace, err := fixture.service.CreateNamespace(fixture.adminContext(), CreateNamespaceInput{Slug: "adopted", DisplayName: "Adopted"})
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := fixture.service.CreatePublisher(fixture.adminContext(), CreatePublisherInput{DisplayName: "External publisher", Domains: []string{"source.example"}})
	if err != nil {
		t.Fatal(err)
	}
	return fixture, validExternalObservationAdoption(namespace.ID, publisher.ID)
}

func TestAdoptExternalObservationCreatesOneAtomicPrivateDraft(t *testing.T) {
	fixture, input := adoptionFixture(t)
	input.Provenance.EntryLocation = "/items/0"
	input.CanonicalIdentifier = "urn:air:source.example:adopted-agent"
	result, err := fixture.service.AdoptExternalObservation(fixture.adminContext(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Resource.CanonicalIdentifier != input.CanonicalIdentifier || result.Version.ResourceID != result.Resource.ID || result.Version.CandidateRevisionID != result.VersionRevision.ID || result.Listing.ResourceID != result.Resource.ID || result.Listing.CandidateRevisionID != result.ListingRevision.ID || result.Listing.Revision != 2 || result.ListingRevision.Visibility != VisibilityPrivate {
		t.Fatalf("adopted draft links = %#v", result)
	}
	if result.VersionRevision.Adoption == nil || result.VersionRevision.Adoption.ObservationID != input.Provenance.ObservationID || result.VersionRevision.Adoption.EntryLocation != "/items/0" || result.VersionRevision.Adoption.AdoptedBy != fixture.admin.Subject || !result.VersionRevision.Adoption.AdoptedAt.Equal(fixture.clock.now) {
		t.Fatalf("adoption provenance = %#v", result.VersionRevision.Adoption)
	}
	if len(fixture.repo.audit) != 3 || fixture.repo.audit[2].Action != "observation.adopted" || len(fixture.repo.outbox) != 3 || fixture.repo.outbox[2].Subject != result.Resource.ID {
		t.Fatalf("adoption evidence = audit %#v outbox %#v", fixture.repo.audit, fixture.repo.outbox)
	}
	if stored, ok := fixture.repo.resources[result.Resource.ID]; !ok || stored.CanonicalIdentifier != input.CanonicalIdentifier {
		t.Fatalf("adopted resource was not persisted: %#v", stored)
	}
	counts := []int{len(fixture.repo.resources), len(fixture.repo.versions), len(fixture.repo.versionRevisions), len(fixture.repo.listings), len(fixture.repo.listingRevisions), len(fixture.repo.audit), len(fixture.repo.outbox)}
	if _, err := fixture.service.AdoptExternalObservation(fixture.adminContext(), input); ErrorCodeOf(err) != CodeConflict {
		t.Fatalf("duplicate adoption returned %v", err)
	}
	after := []int{len(fixture.repo.resources), len(fixture.repo.versions), len(fixture.repo.versionRevisions), len(fixture.repo.listings), len(fixture.repo.listingRevisions), len(fixture.repo.audit), len(fixture.repo.outbox)}
	for index := range counts {
		if counts[index] != after[index] {
			t.Fatalf("duplicate adoption partially persisted state: before %v after %v", counts, after)
		}
	}
}

func TestAdoptExternalObservationRejectsUnsafeOrIncompleteAuthority(t *testing.T) {
	fixture, valid := adoptionFixture(t)
	reader := identityfixture.WithActor(context.Background(), fixture.reader)
	if _, err := fixture.service.AdoptExternalObservation(reader, valid); ErrorCodeOf(err) != CodeForbidden {
		t.Fatalf("reader adoption returned %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*ExternalObservationAdoptionInput)
		code   ErrorCode
	}{
		{name: "identifier", mutate: func(input *ExternalObservationAdoptionInput) { input.CanonicalIdentifier = "external-id" }, code: CodeInvalidArgument},
		{name: "local fields", mutate: func(input *ExternalObservationAdoptionInput) { input.Description = "" }, code: CodeInvalidArgument},
		{name: "visibility", mutate: func(input *ExternalObservationAdoptionInput) { input.Visibility = "shared" }, code: CodeInvalidArgument},
		{name: "listing URL", mutate: func(input *ExternalObservationAdoptionInput) { input.DocumentationURL = "http://source.example/docs" }, code: CodeInvalidArgument},
		{name: "discovery metadata", mutate: func(input *ExternalObservationAdoptionInput) { input.Tags = make([]string, 33) }, code: CodeInvalidArgument},
		{name: "representation preparation", mutate: func(input *ExternalObservationAdoptionInput) { input.Representation.Role = "unknown" }, code: CodeInvalidArgument},
		{name: "provenance", mutate: func(input *ExternalObservationAdoptionInput) { input.Provenance.ExactEntryDigest = "sha256:invalid" }, code: CodeInvalidArgument},
		{name: "normalization profile", mutate: func(input *ExternalObservationAdoptionInput) {
			input.Provenance.NormalizationProfile = "catalog-observation-v2"
		}, code: CodeInvalidArgument},
		{name: "provenance location", mutate: func(input *ExternalObservationAdoptionInput) { input.Provenance.EntryLocation = "/entries/01" }, code: CodeInvalidArgument},
		{name: "namespace", mutate: func(input *ExternalObservationAdoptionInput) { input.NamespaceID = "missing" }, code: CodeInvalidArgument},
		{name: "publisher", mutate: func(input *ExternalObservationAdoptionInput) { input.PublisherID = "missing" }, code: CodeInvalidArgument},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			test.mutate(&input)
			if _, err := fixture.service.AdoptExternalObservation(fixture.adminContext(), input); ErrorCodeOf(err) != test.code {
				t.Fatalf("adoption returned %v", err)
			}
		})
	}
}

func TestAdoptionIdentifierFailuresLeaveNoPartialDraft(t *testing.T) {
	t.Parallel()
	// Resource, version, revision, representation, validation, listing,
	// listing revision, audit and outbox each need an identifier.
	for failAt := 1; failAt <= 9; failAt++ {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			fixture, input := adoptionFixture(t)
			calls := 0
			sentinel := errors.New("adoption identifier unavailable")
			fixture.service.ids = auditIDFunc(func(time.Time) (string, error) {
				calls++
				if calls == failAt {
					return "", sentinel
				}
				return fmt.Sprintf("00000000-0000-7000-8000-%012d", 100+calls), nil
			})
			result, err := fixture.service.AdoptExternalObservation(fixture.adminContext(), input)
			if !errors.Is(err, sentinel) || ErrorCodeOf(err) != CodeInternal || calls != failAt || result.Resource.ID != "" {
				t.Fatalf("identifier failure: result=%+v error=%v calls=%d", result, err, calls)
			}
			r := fixture.repo
			if len(r.resources)+len(r.versions)+len(r.versionRevisions)+len(r.documents)+len(r.evidenceSets)+len(r.validationRuns)+len(r.listings)+len(r.listingRevisions) != 0 || len(r.audit) != 2 || len(r.outbox) != 2 || len(r.namespaces) != 1 || len(r.publishers) != 1 {
				t.Fatal("identifier failure retained partial draft state or evidence")
			}
		})
	}
}

type adoptionReadFailure struct {
	Repository
	phase string
	err   error
}

func (r adoptionReadFailure) GetNamespace(ctx context.Context, tenantID, id string) (Namespace, error) {
	if r.phase == "namespace" {
		return Namespace{}, r.err
	}
	return r.Repository.GetNamespace(ctx, tenantID, id)
}

func (r adoptionReadFailure) GetPublisher(ctx context.Context, tenantID, id string) (Publisher, error) {
	if r.phase == "publisher" {
		return Publisher{}, r.err
	}
	return r.Repository.GetPublisher(ctx, tenantID, id)
}

func TestAdoptionPreservesRepositoryFailureClassification(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"namespace", "publisher"} {
		t.Run(phase, func(t *testing.T) {
			fixture, input := adoptionFixture(t)
			sentinel := E(CodeDependencyUnavailable, "repository read failed")
			fixture.service.repo = adoptionReadFailure{Repository: fixture.repo, phase: phase, err: sentinel}
			fixture.service.ids = auditIDFunc(func(time.Time) (string, error) {
				t.Fatal("identifier allocated after a required read failed")
				return "", nil
			})
			result, err := fixture.service.AdoptExternalObservation(fixture.adminContext(), input)
			if !errors.Is(err, sentinel) || ErrorCodeOf(err) != CodeDependencyUnavailable || result.Resource.ID != "" {
				t.Fatalf("dependency failure became absence or success: %+v, %v", result, err)
			}
			if len(fixture.repo.resources) != 0 || len(fixture.repo.audit) != 2 || len(fixture.repo.outbox) != 2 {
				t.Fatal("failed required read changed retained state")
			}
		})
	}
}

func TestAdoptionEntryLocationIsCanonicalAndBounded(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"/entries/0", "/entries/42", "/entries/18446744073709551615", "/items/0", "/items/42", "/items/18446744073709551615"} {
		if !validAdoptionEntryLocation(value) {
			t.Fatalf("canonical entry location %q was rejected", value)
		}
	}
	for _, value := range []string{
		"entries/0",
		"/entries/",
		"/entries/00",
		"/entries/-1",
		"/entries/18446744073709551616",
		"/entries/1/child",
		" /entries/1",
		"/entries/1 ",
		"/entries/" + strings.Repeat("1", 1025),
		"/items/", "/items/00", "/items/+1", "/items/-1", "/items/18446744073709551616",
		"/items/1/child", "/items/1 ", "/items/" + strings.Repeat("1", 1025), "/unknown/0",
	} {
		if validAdoptionEntryLocation(value) {
			t.Fatalf("non-canonical entry location %q was accepted", value)
		}
	}
}

func validExternalObservationAdoption(namespaceID, publisherID string) ExternalObservationAdoptionInput {
	digest := "sha256:" + strings.Repeat("a", 64)
	return ExternalObservationAdoptionInput{
		NamespaceID:         namespaceID,
		CanonicalIdentifier: "urn:air:source.example:default:adopted-agent",
		Kind:                KindAgent,
		Version:             "1.0.0",
		Representation:      RepresentationInput{Role: RepresentationPrimary, MediaType: "application/a2a-agent-card+json", Data: json.RawMessage(`{"name":"Adopted Agent"}`)},
		DisplayName:         "Adopted Agent",
		Description:         "A reviewed external observation starting normal local governance.",
		Tags:                []string{"external", "agent"},
		PublisherID:         publisherID,
		Provenance: AdoptionProvenance{
			ObservationID:         "observation-one",
			SourceID:              "source-one",
			SourceRevision:        3,
			SnapshotID:            "snapshot-one",
			RetrievalDigest:       digest,
			EntryLocation:         "/entries/0",
			ExactEntryDigest:      digest,
			NormalizedEntryDigest: "sha256:" + strings.Repeat("b", 64),
			NormalizationProfile:  "catalog-observation-v1",
		},
	}
}

func TestAdoptionCommitAdmissionRunsAfterPreparationAndBeforeMutation(t *testing.T) {
	for _, denied := range []bool{false, true} {
		t.Run(fmt.Sprint(denied), func(t *testing.T) {
			fixture, input := adoptionFixture(t)
			before := []int{len(fixture.repo.resources), len(fixture.repo.versionRevisions), len(fixture.repo.listings), len(fixture.repo.audit), len(fixture.repo.outbox)}
			called := false
			input.AdmitCommit = func(ctx context.Context, commit func(context.Context) error) error {
				called = true
				if denied {
					return E(CodePreconditionFailed, "source changed")
				}
				return commit(ctx)
			}
			got, err := fixture.service.AdoptExternalObservation(fixture.adminContext(), input)
			if !called {
				t.Fatal("commit admission skipped")
			}
			if denied {
				after := []int{len(fixture.repo.resources), len(fixture.repo.versionRevisions), len(fixture.repo.listings), len(fixture.repo.audit), len(fixture.repo.outbox)}
				if !slices.Equal(before, after) {
					t.Fatalf("denied commit changed state/evidence: %v -> %v", before, after)
				}
				if ErrorCodeOf(err) != CodePreconditionFailed || got.Resource.ID != "" {
					t.Fatal("denied commit persisted", got, err)
				}
			} else if err != nil || got.Resource.ID == "" {
				t.Fatal("allowed commit failed", err)
			}
		})
	}
}
