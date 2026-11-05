// Semantic status tests cover authorization and disclosure without provider probes.
package catalog

import (
	"errors"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"reflect"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// TestSemanticStatus requires administrator access even when indexing is unavailable.
func TestSemanticStatus(t *testing.T) {
	for _, capability := range []string{"enabled", "disabled", "excluded", "store failure"} {
		for _, role := range []string{"admin", "reader", "anonymous"} {
			t.Run(capability+"/"+role, func(t *testing.T) {
				s, f, actor := newSemanticSearchTest(t)
				if role == "admin" {
					actor.Roles = []identity.Role{identity.RoleTenantAdmin}
				}
				ctx := identityfixture.WithActor(t.Context(), actor)
				if role == "anonymous" {
					ctx = t.Context()
				}
				switch capability {
				case "disabled":
					s.semantic = nil
				case "excluded":
					s.semantic.options.ExcludedTenants = []string{actor.TenantID}
				case "store failure":
					f.storeError = errors.New("status store failed")
				}
				status, err := s.SemanticStatus(ctx)
				wantCalls := 0
				switch {
				case role != "admin":
					if ErrorCodeOf(err) != CodeForbidden || status != nil {
						t.Fatalf("unauthorized status=%+v err=%v", status, err)
					}
				case capability == "disabled" || capability == "excluded":
					if err != nil || status != nil {
						t.Fatalf("disabled status=%+v err=%v", status, err)
					}
				default:
					wantCalls = 1
					if !errors.Is(err, f.storeError) {
						t.Fatalf("error=%v, want %v", err, f.storeError)
					}
					if err == nil && !reflect.DeepEqual(status, &SemanticIndexStatus{State: "ready", JobID: f.g.ID, Indexed: 2}) {
						t.Fatalf("status=%+v", status)
					}
					if f.lookup != (semanticLookup{tenant: actor.TenantID, profile: f.g.Profile, dimensions: 2}) {
						t.Fatalf("lookup=%+v", f.lookup)
					}
				}
				if f.statusCalls != wantCalls || f.activeCalls != 0 || f.queries != 0 {
					t.Fatalf("status/active/query calls = %d/%d/%d", f.statusCalls, f.activeCalls, f.queries)
				}
			})
		}
	}
}

// TestSemanticProviderState reports only tenant-permitted fixed states without inference or store reads.
func TestSemanticProviderState(t *testing.T) {
	for _, tc := range []struct {
		name, state, want                         string
		disabled, excluded, anonymous, noCallback bool
	}{
		{name: "healthy", state: "healthy", want: "healthy"},
		{name: "degraded", state: "degraded", want: "degraded"},
		{name: "unknown", state: "unknown", want: "unknown"},
		{name: "empty", want: "unknown"},
		{name: "private detail", state: "private-provider-endpoint", want: "unknown"},
		{name: "disabled", disabled: true, state: "healthy", want: "unknown"},
		{name: "excluded", excluded: true, state: "healthy", want: "unknown"},
		{name: "anonymous", anonymous: true, state: "healthy", want: "unknown"},
		{name: "no callback", noCallback: true, want: "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, actor := newSemanticSearchTest(t)
			ctx := identityfixture.WithActor(t.Context(), actor)
			calls := 0
			s.semantic.options.ProviderState = func() string { calls++; return tc.state }
			profile := s.semantic.options.RankingProfile
			if tc.noCallback {
				s.semantic.options.ProviderState = nil
			}
			if tc.excluded {
				s.semantic.options.ExcludedTenants = []string{actor.TenantID}
			}
			if tc.anonymous {
				ctx = t.Context()
			}
			if tc.disabled {
				s.semantic = nil
			}
			wantEnabled := !tc.disabled && !tc.excluded && !tc.anonymous
			if !wantEnabled {
				profile = CoreSearchProfile
			}
			if enabled, got := s.SemanticConfiguration(ctx); enabled != wantEnabled || got != profile {
				t.Fatalf("configuration = %v/%s", enabled, got)
			}
			if calls != 0 {
				t.Fatal("configuration probed provider state")
			}
			if got := s.SemanticProviderState(ctx); got != tc.want {
				t.Fatalf("state = %q, want %q", got, tc.want)
			}
			wantCalls := 0
			if wantEnabled && !tc.noCallback {
				wantCalls = 1
			}
			if calls != wantCalls || f.queries != 0 || f.activeCalls != 0 || f.statusCalls != 0 {
				t.Fatalf("callback/query/active/status calls = %d/%d/%d/%d", calls, f.queries, f.activeCalls, f.statusCalls)
			}
		})
	}
}
