package catalog

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

func TestAdoptVersionPreservesResourceListingAndOrigin(t *testing.T) {
	f, input := adoptionFixture(t)
	ctx := f.adminContext()
	old, err := f.service.AdoptExternalObservation(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := f.repo.GetOriginatingPrincipal(ctx, old.Resource.TenantID, "resource", old.Resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	input.Version = "release-next"
	input.Provenance.ObservationID = "observation-two"
	input.Provenance.SourceRevision++
	created, err := f.service.AdoptExternalVersion(ctx, ExternalVersionAdoptionInput{ResourceID: old.Resource.ID, PriorVersionRevisionID: old.VersionRevision.ID, Selection: input})
	if err != nil {
		t.Fatal(err)
	}
	if created.ResourceID != old.Resource.ID || created.Version.ResourceID != old.Resource.ID || created.Version.Version != "release-next" || created.VersionRevision.Adoption == nil || created.VersionRevision.Adoption.ObservationID != "observation-two" {
		t.Fatal("wrong adopted version", created)
	}
	if !reflect.DeepEqual(f.repo.resources[old.Resource.ID], old.Resource) || !reflect.DeepEqual(f.repo.listings[old.Listing.ID], old.Listing) || !reflect.DeepEqual(f.repo.listingRevisions[old.ListingRevision.ID], old.ListingRevision) || !reflect.DeepEqual(f.repo.versionRevisions[old.VersionRevision.ID], old.VersionRevision) {
		t.Fatal("append changed retained state")
	}
	after, err := f.repo.GetOriginatingPrincipal(ctx, old.Resource.TenantID, "resource", old.Resource.ID)
	if err != nil || after != origin {
		t.Fatal("Resource origin changed", err)
	}
	last := f.repo.audit[len(f.repo.audit)-1]
	if last.Action != "resource_version.created" || last.TargetType != "resource_version" || last.TargetID != created.Version.ID {
		t.Fatal("wrong creation evidence", last)
	}
	before := len(f.repo.audit)
	if _, err = f.service.AdoptExternalVersion(ctx, ExternalVersionAdoptionInput{ResourceID: old.Resource.ID, PriorVersionRevisionID: old.VersionRevision.ID, Selection: input}); ErrorCodeOf(err) != CodeConflict {
		t.Fatal("duplicate label", err)
	}
	if len(f.repo.versions) != 2 || len(f.repo.audit) != before {
		t.Fatal("duplicate partially committed")
	}
}

func TestAdoptVersionProofAndAuthorityRefusals(t *testing.T) {
	for _, test := range []struct {
		name string
		code ErrorCode
	}{
		{"wrong-source", CodePreconditionFailed},
		{"wrong-kind", CodePreconditionFailed},
		{"wrong-identifier", CodePreconditionFailed},
		{"same-version", CodePreconditionFailed},
		{"missing-proof", CodeNotFound},
		{"manual-proof", CodeNotFound},
		{"other-resource", CodeNotFound},
		{"no-edit", CodeForbidden},
		{"revoked-before-commit", CodeForbidden},
		{"invalid-provenance", CodeInvalidArgument},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, input := adoptionFixture(t)
			ctx := f.adminContext()
			var old ExternalObservationAdoption
			var err error
			if test.name == "manual-proof" {
				old.Resource, err = f.service.CreateResource(ctx, CreateResourceInput{NamespaceID: input.NamespaceID, CanonicalIdentifier: input.CanonicalIdentifier, Kind: input.Kind})
				if err != nil {
					t.Fatal(err)
				}
				old.Version, old.VersionRevision, _, err = f.service.CreateVersion(ctx, old.Resource.ID, CreateVersionInput{Version: input.Version, Representations: []RepresentationInput{input.Representation}})
			} else {
				old, err = f.service.AdoptExternalObservation(ctx, input)
			}
			if err != nil {
				t.Fatal(err)
			}
			input.Version = "2.0"
			request := ExternalVersionAdoptionInput{ResourceID: old.Resource.ID, PriorVersionRevisionID: old.VersionRevision.ID, Selection: input}
			switch test.name {
			case "wrong-source":
				request.Selection.Provenance.SourceID = "other"
			case "wrong-kind":
				request.Selection.Kind = KindMCPServer
			case "wrong-identifier":
				request.Selection.CanonicalIdentifier = "urn:air:other.example:agent:other"
			case "same-version":
				request.Selection.Version = old.Version.Version
			case "missing-proof":
				request.PriorVersionRevisionID = "missing"
			case "other-resource":
				other, err := f.service.CreateResource(ctx, CreateResourceInput{NamespaceID: old.Resource.NamespaceID, CanonicalIdentifier: "urn:air:source.example:agents:other", Kind: old.Resource.Kind})
				if err != nil {
					t.Fatal(err)
				}
				request.ResourceID = other.ID
			case "no-edit":
				ctx = identityfixture.WithActor(context.Background(), f.reader)
			case "revoked-before-commit":
				request.Selection.AdmitCommit = func(context.Context, func(context.Context) error) error {
					return E(CodeForbidden, "current authority revoked")
				}
			case "invalid-provenance":
				request.Selection.Provenance.NormalizedEntryDigest = "invalid"
			}
			snapshot := func() map[string]any {
				return clone(map[string]any{
					"resources": f.repo.resources, "versions": f.repo.versions,
					"revisions": f.repo.versionRevisions, "documents": f.repo.documents,
					"evidence": f.repo.evidenceSets, "validationRuns": f.repo.validationRuns,
					"listings": f.repo.listings, "listingRevisions": f.repo.listingRevisions,
					"audit": f.repo.audit, "outbox": f.repo.outbox,
				})
			}
			before := snapshot()
			if _, err = f.service.AdoptExternalVersion(ctx, request); ErrorCodeOf(err) != test.code {
				t.Fatalf("refusal: got %v, want %s", err, test.code)
			}
			if !reflect.DeepEqual(snapshot(), before) {
				t.Fatal("failed append changed state")
			}
		})
	}
}

func TestAdoptionProjectionSelectsExactVersionBeyondHundred(t *testing.T) {
	f, input := adoptionFixture(t)
	ctx := f.adminContext()
	old, err := f.service.AdoptExternalObservation(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 150; n++ {
		input.Version = fmt.Sprintf("opaque-%03d", n)
		if _, err = f.service.AdoptExternalVersion(ctx, ExternalVersionAdoptionInput{ResourceID: old.Resource.ID, PriorVersionRevisionID: old.VersionRevision.ID, Selection: input}); err != nil {
			t.Fatal(err)
		}
	}
	ctx, _, err = identity.WithCollectionContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	lookups := []AdoptionLookup{{SourceID: input.Provenance.SourceID, Identifier: input.CanonicalIdentifier, Kind: input.Kind, VersionPresent: true, Version: input.Version}}
	got, err := f.repo.FindAdoptions(ctx, old.Resource.TenantID, lookups)
	if err != nil || len(got) != 1 || got[0].ResourceID != old.Resource.ID {
		t.Fatal("exact large-history projection", len(got), err)
	}
	lookups[0].Version = "not-adopted"
	got, err = f.repo.FindAdoptions(ctx, old.Resource.TenantID, lookups)
	if err != nil || len(got) != 0 {
		t.Fatal("unadopted version inherited import", got, err)
	}
}
