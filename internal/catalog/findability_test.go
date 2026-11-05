package catalog

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func TestCanonicalResourceIdentifier(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"  URN:AIR:CATALOG.EXAMPLE.COM:Finance:MyAgent  ", "urn:air:catalog.example.com:Finance:MyAgent"},
		{"  Café:e\u0301:🙂  ", "Café:e\u0301:🙂"},
		{" \ufeffOpaque:e\u0301:🙂\ufeff ", "\ufeffOpaque:e\u0301:🙂\ufeff"},
		{strings.Repeat("🙂", 512), strings.Repeat("🙂", 512)},
	} {
		got, err := CanonicalResourceIdentifier(tc.input)
		if err != nil || got != tc.want {
			t.Fatalf("canonical identity = %q, %v; want %q", got, err, tc.want)
		}
	}
	for _, value := range []string{"", "   ", "urn:air:bad", "x\x00y", strings.Repeat("🙂", 513)} {
		if _, err := CanonicalResourceIdentifier(value); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatalf("invalid identity accepted: %q: %v", value, err)
		}
	}
}

func TestExactResourceFindabilityPreservesSelection(t *testing.T) {
	f, _, _, direct, _ := collectionFixture(t)
	target := f.repo.resources["00000000-0000-7000-8000-000000000101"]
	target.CanonicalIdentifier = "urn:air:catalog.example.com:Finance:MyAgent"
	target.UpdatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f.repo.resources[target.ID] = target
	for _, tc := range []struct {
		identifier string
		kind       Kind
		want       int
	}{
		{" URN:AIR:CATALOG.EXAMPLE.COM:Finance:MyAgent ", target.Kind, 1},
		{"urn:air:catalog.example.com:finance:MyAgent", target.Kind, 0},
		{"urn:air:catalog.example.com:Finance:myagent", target.Kind, 0},
		{"absent", target.Kind, 0},
		{target.CanonicalIdentifier, KindPlugin, 0},
		{"urn:air:catalog.example.com:default:collection-0", "", 0},
	} {
		got, err := f.service.ListResources(direct, ResourceListRequest{CanonicalIdentifier: tc.identifier, Kind: tc.kind, Limit: 1})
		if err != nil || len(got) != tc.want || len(got) > 0 && got[0].ID != target.ID {
			t.Fatalf("exact filter %q/%s = %+v, %v", tc.identifier, tc.kind, got, err)
		}
	}
	target.CanonicalIdentifier = "Café:e\u0301:🙂"
	f.repo.resources[target.ID] = target
	for _, value := range []string{target.CanonicalIdentifier, "café:e\u0301:🙂", "Café:é:🙂"} {
		got, err := f.service.ListResources(direct, ResourceListRequest{CanonicalIdentifier: " " + value + " ", Limit: 1})
		want := 0
		if value == target.CanonicalIdentifier {
			want = 1
		}
		if err != nil || len(got) != want {
			t.Fatalf("opaque identity changed spelling: %+v %v", got, err)
		}
	}
	deleted := f.clock.Now()
	target.DeletedAt = &deleted
	f.repo.resources[target.ID] = target
	if got, err := f.service.ListResources(direct, ResourceListRequest{CanonicalIdentifier: target.CanonicalIdentifier, Limit: 1}); err != nil || len(got) != 0 {
		t.Fatal("deleted target leaked", got, err)
	}
	target.DeletedAt = nil
	target.TenantID = "00000000-0000-7000-8000-000000000098"
	f.repo.resources[target.ID] = target
	if got, err := f.service.ListResources(direct, ResourceListRequest{CanonicalIdentifier: target.CanonicalIdentifier, Limit: 1}); err != nil || len(got) != 0 {
		t.Fatal("other tenant leaked", got, err)
	}
}

func TestReviewQueueContextUsesIndependentCurrentPermissions(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		history, metadata, content bool
	}{{"history", true, false, false}, {"metadata", true, true, false}, {"content", true, false, true}, {"both", true, true, true}, {"no history", false, true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			f, _, _, _, grouped := collectionFixture(t)
			actor, _ := identity.FromContext(grouped)
			// Use a separate policy store so each expected result has an
			// independently chosen permission set.
			store, err := identity.NewMemoryStore([]identity.Seed{{TenantID: guardrailTenantID, Issuer: "queue-context", Subject: "owner", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}}, {TenantID: guardrailTenantID, Issuer: "queue-context", Subject: actor.Subject, ActorType: identity.ActorHuman}}, &collectionEvidence{})
			if err != nil {
				t.Fatal(err)
			}
			authority, err := identity.NewService(identity.ServiceOptions{Store: store, Clock: f.clock, IDs: &guardrailIDs{next: 2000}, CursorKey: []byte("01234567890123456789012345678901")})
			if err != nil {
				t.Fatal(err)
			}
			as := func(subject string) context.Context {
				a, e := store.ResolvePrincipal(t.Context(), identity.Principal{TenantID: guardrailTenantID, Issuer: "queue-context", Subject: subject})
				if e != nil {
					t.Fatal(e)
				}
				return identity.WithActor(t.Context(), a)
			}
			owner := as("owner")
			user := as(actor.Subject)
			a, _ := identity.FromContext(user)
			permissions := []identity.Permission{}
			if tc.history {
				permissions = append(permissions, "resource.read_history")
			}
			if tc.metadata {
				permissions = append(permissions, "resource.read")
			}
			if tc.content {
				permissions = append(permissions, "resource.read_content")
			}
			role, err := authority.SaveRoleDefinition(owner, "", 0, identity.RoleDefinitionInput{Name: "Queue context", Permissions: permissions, Reason: "Qualify independent queue context"}, false, "role")
			if err != nil {
				t.Fatal(err)
			}
			grant, err := authority.GrantAuthorizationAssignment(owner, identity.AccessAssignmentInput{RoleDefinitionID: role.ID, MembershipID: a.MembershipID, NamespaceID: collectionNamespace, Reason: "Explicit queue permission"}, "grant")
			if err != nil {
				t.Fatal(err)
			}
			if !tc.history {
				got, err := f.service.ListSubmissions(user, SubmissionListRequest{HistoryPageRequest: HistoryPageRequest{Limit: 1}})
				if err != nil || len(got) != 0 {
					t.Fatal("context permissions broadened history admission", got, err)
				}
				return
			}
			listing := f.repo.listings["00000000-0000-7000-8000-000000000401"]
			submitted := f.repo.listingRevisions["00000000-0000-7000-8000-000000000501"]
			submitted.DisplayName = strings.Repeat("🙂", 160)
			f.repo.listingRevisions[submitted.ID] = submitted
			latest := submitted
			latest.ID = "later-revision"
			latest.DisplayName = "Renamed later"
			f.repo.listingRevisions[latest.ID] = latest
			listing.CandidateRevisionID = latest.ID
			f.repo.listings[listing.ID] = listing
			submission := f.repo.submissions["00000000-0000-7000-8000-000000000801"]
			submission.ListingRevisionID = submitted.ID
			f.repo.submissions[submission.ID] = submission
			got, err := f.service.ListSubmissions(user, SubmissionListRequest{HistoryPageRequest: HistoryPageRequest{Limit: 1}, ListingID: listing.ID})
			if err != nil || len(got) != 1 {
				t.Fatal(got, err)
			}
			wantIdentity, wantName := "", ""
			if tc.metadata {
				wantIdentity = f.repo.resources[listing.ResourceID].CanonicalIdentifier
			}
			if tc.content {
				wantName = submitted.DisplayName
			}
			if got[0].CanonicalIdentifier != wantIdentity || got[0].SubmittedDisplayName != wantName {
				t.Fatalf("wrong disclosure: %+v", got[0])
			}
			if f.repo.submissions[submission.ID].SubmittedDisplayName != "" {
				t.Fatal("projection mutated retained evidence")
			}
			delete(f.repo.listingRevisions, submitted.ID)
			got, err = f.service.ListSubmissions(user, SubmissionListRequest{HistoryPageRequest: HistoryPageRequest{Limit: 1}, ListingID: listing.ID})
			if err != nil || len(got) != 1 || got[0].SubmittedDisplayName != "" {
				t.Fatal("missing context broke queue", got, err)
			}
			if _, err := authority.RevokeAuthorizationAssignment(owner, grant.ID, grant.Revision, "Remove current queue access", "revoke"); err != nil {
				t.Fatal(err)
			}
			got, err = f.service.ListSubmissions(user, SubmissionListRequest{HistoryPageRequest: HistoryPageRequest{Limit: 1}})
			if err != nil || len(got) != 0 {
				t.Fatal("revoked authority disclosed queue", got, err)
			}
		})
	}
}
