package catalog

import (
	"context"
	"reflect"
	"testing"
)

// Count mutation handoffs so rejection assertions also catch a service guard
// accidentally delegated to persistence.
type eligibilityMutationProbe struct {
	Repository
	writes int
}

func (r *eligibilityMutationProbe) UpdatePublisher(ctx context.Context, params UpdatePublisherParams) (Publisher, error) {
	r.writes++
	return r.Repository.UpdatePublisher(ctx, params)
}

func (r *eligibilityMutationProbe) TransitionRelease(ctx context.Context, params TransitionReleaseParams) (ListingRelease, error) {
	r.writes++
	return r.Repository.TransitionRelease(ctx, params)
}

func TestPublisherPatchValidatesAllFieldsBeforeMutation(t *testing.T) {
	t.Parallel()
	tooManyDomains := make([]string, 65)
	for index := range tooManyDomains {
		tooManyDomains[index] = "new.example.com"
	}
	for _, test := range []struct {
		name    string
		domains []string
	}{
		{name: "invalid domain", domains: []string{"https://new.example.com"}},
		{name: "too many declarations", domains: tooManyDomains},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newGuardrailFixture(t)
			before := Publisher{ID: lifecycleSubmissionID, TenantID: guardrailTenantID, DisplayName: "Original publisher", Domains: []string{"old.example.com"}, Revision: 1, CreatedAt: f.clock.now, UpdatedAt: f.clock.now}
			f.repo.publishers[before.ID] = clone(before)
			probe := &eligibilityMutationProbe{Repository: f.repo}
			f.service.repo = probe
			display, domains := "New publisher", test.domains
			_, err := f.service.UpdatePublisher(f.adminContext(), before.ID, PatchPublisherInput{DisplayName: &display, Domains: &domains, Reason: "Update declared publisher metadata"}, before.Revision)
			if ErrorCodeOf(err) != CodeInvalidArgument {
				t.Fatalf("invalid patch returned %v", err)
			}
			if probe.writes != 0 || !reflect.DeepEqual(f.repo.publishers[before.ID], before) || len(f.repo.audit) != 0 || len(f.repo.outbox) != 0 {
				t.Fatalf("rejected patch mutated publisher or evidence: writes=%d publisher=%#v", probe.writes, f.repo.publishers[before.ID])
			}

			// Correct only the invalid field; the original revision stays usable.
			domains = []string{" NEW.EXAMPLE.COM. ", "new.example.com"}
			updated, err := f.service.UpdatePublisher(f.adminContext(), before.ID, PatchPublisherInput{DisplayName: &display, Domains: &domains, Reason: "Update declared publisher metadata"}, before.Revision)
			if err != nil || updated.DisplayName != "New publisher" || !reflect.DeepEqual(updated.Domains, []string{"new.example.com"}) || updated.Revision != 2 || probe.writes != 1 || len(f.repo.audit) != 1 || len(f.repo.outbox) != 1 {
				t.Fatalf("corrected patch = %#v, error=%v writes=%d", updated, err, probe.writes)
			}
		})
	}
}

func TestReleaseReplacementMustRemainDiscoverable(t *testing.T) {
	t.Parallel()
	// The contract permits published or deprecated replacements, including
	// another listing, for both deprecation guidance and supersession.
	for _, target := range []ReleaseState{ReleaseDeprecated, ReleaseSuperseded} {
		for _, state := range []ReleaseState{ReleasePublished, ReleaseDeprecated, ReleaseRetired, ReleaseSuperseded, ReleaseRevoked} {
			t.Run(string(target)+"/"+string(state), func(t *testing.T) {
				f := newGuardrailFixture(t)
				const releaseID = "00000000-0000-7000-8000-000000000070"
				const replacementID = "00000000-0000-7000-8000-000000000071"
				listing := CatalogListing{ID: lifecycleListingID, TenantID: guardrailTenantID, NamespaceID: "namespace", ResourceID: "resource", CurrentReleaseID: releaseID, Revision: 1, UpdatedAt: f.clock.now}
				release := ListingRelease{ID: releaseID, TenantID: guardrailTenantID, ListingID: listing.ID, State: ReleasePublished, Revision: 1, UpdatedAt: f.clock.now}
				replacement := ListingRelease{ID: replacementID, TenantID: guardrailTenantID, ListingID: "other-listing", State: state, Revision: 3, UpdatedAt: f.clock.now}
				f.repo.resources[listing.ResourceID] = Resource{ID: listing.ResourceID, TenantID: guardrailTenantID}
				f.repo.listings[listing.ID] = listing
				f.repo.listings[replacement.ListingID] = CatalogListing{ID: replacement.ListingID, TenantID: guardrailTenantID, ResourceID: listing.ResourceID, NamespaceID: listing.NamespaceID, Revision: 1}
				f.repo.releases[release.ID], f.repo.releases[replacement.ID] = release, replacement
				probe := &eligibilityMutationProbe{Repository: f.repo}
				f.service.repo = probe

				updated, err := f.service.TransitionRelease(f.adminContext(), releaseID, target, "Guide clients to another release", replacementID, nil, release.Revision)
				if state != ReleasePublished && state != ReleaseDeprecated {
					if ErrorCodeOf(err) != CodeInvalidArgument || probe.writes != 0 || !reflect.DeepEqual(f.repo.releases[release.ID], release) || !reflect.DeepEqual(f.repo.releases[replacement.ID], replacement) || !reflect.DeepEqual(f.repo.listings[listing.ID], listing) || len(f.repo.audit) != 0 || len(f.repo.outbox) != 0 {
						t.Fatalf("ineligible replacement returned %v, writes=%d release=%#v", err, probe.writes, f.repo.releases[release.ID])
					}
					// Change only replacement eligibility to prove no other guard
					// masks the rejection, with the original revision still usable.
					replacement.State = ReleaseDeprecated
					f.repo.releases[replacement.ID] = replacement
					updated, err = f.service.TransitionRelease(f.adminContext(), releaseID, target, "Guide clients to another release", replacementID, nil, release.Revision)
				}
				if err != nil || updated.State != target || updated.ReplacementReleaseID != replacementID || updated.Revision != 2 || probe.writes != 1 || len(f.repo.audit) != 1 || len(f.repo.outbox) != 1 {
					t.Fatalf("eligible replacement = %#v, error=%v writes=%d", updated, err, probe.writes)
				}
				if !reflect.DeepEqual(f.repo.releases[replacement.ID], replacement) {
					t.Fatalf("transition changed replacement evidence: %#v", f.repo.releases[replacement.ID])
				}
			})
		}
	}
}
