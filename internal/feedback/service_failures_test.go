// Dependency failures must leave feedback and audit untouched and release admission.
package feedback

import (
	"context"
	"errors"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// failingContext fails one retained-context lookup, delegating all other reads.
type failingContext struct {
	ContextReader
	operation string
	err       error
}

// GetResource injects a resource-read failure without changing other lookups.
func (f failingContext) GetResource(ctx context.Context, tenant, id string) (catalog.Resource, error) {
	if f.operation == "resource" {
		return catalog.Resource{}, f.err
	}
	return f.ContextReader.GetResource(ctx, tenant, id)
}

// GetRelease injects a failure while resolving exact release context.
func (f failingContext) GetRelease(ctx context.Context, tenant, id string) (catalog.ListingRelease, error) {
	if f.operation == "release" {
		return catalog.ListingRelease{}, f.err
	}
	return f.ContextReader.GetRelease(ctx, tenant, id)
}

// GetVersionRevision injects an immutable revision-read failure.
func (f failingContext) GetVersionRevision(ctx context.Context, tenant, id string) (catalog.VersionRevision, catalog.EvidenceSet, error) {
	if f.operation == "revision" {
		return catalog.VersionRevision{}, catalog.EvidenceSet{}, f.err
	}
	return f.ContextReader.GetVersionRevision(ctx, tenant, id)
}

// GetVersion injects failure at the final resource/version relation check.
func (f failingContext) GetVersion(ctx context.Context, tenant, id string) (catalog.ResourceVersion, error) {
	if f.operation == "version" {
		return catalog.ResourceVersion{}, f.err
	}
	return f.ContextReader.GetVersion(ctx, tenant, id)
}

// idFunction lets a test fail one identifier allocation, not the whole service.
type idFunction func(time.Time) (string, error)

// New implements the existing domain identifier port.
func (f idFunction) New(at time.Time) (string, error) { return f(at) }

// TestFeedbackRequiresDependencies checks each mandatory port independently.
func TestFeedbackRequiresDependencies(t *testing.T) {
	s, _, _, _ := fixture(t)
	for _, missing := range []string{"store", "context", "clock", "ids"} {
		t.Run(missing, func(t *testing.T) {
			store, reader, clock, ids := s.store, s.context, s.clock, s.ids
			switch missing {
			case "store":
				store = nil
			case "context":
				reader = nil
			case "clock":
				clock = nil
			case "ids":
				ids = nil
			}
			if got, err := NewService(store, reader, clock, ids); err == nil || got != nil {
				t.Fatal("incomplete dependencies admitted")
			}
		})
	}
}

// TestFeedbackContextFailuresCannotCreate checks every step of exact-context resolution.
func TestFeedbackContextFailuresCannotCreate(t *testing.T) {
	for _, operation := range []string{"resource", "release", "revision", "version"} {
		t.Run(operation, func(t *testing.T) {
			s, store, sink, ctx := fixture(t)
			cause := errors.New("synthetic lookup failure")
			s.context = failingContext{ContextReader: s.context, operation: operation, err: cause}
			in := input()
			in.ReleaseID = testRelease
			_, err := s.Create(ctx, in)
			if !errors.Is(err, cause) || len(store.records) != 0 || len(sink.records) != 0 || s.active.Load() != 0 {
				t.Fatalf("failed lookup changed state or admission: %v", err)
			}
		})
	}
}

// TestFeedbackIdentifierFailuresCannotCreate covers record IDs and audit construction.
func TestFeedbackIdentifierFailuresCannotCreate(t *testing.T) {
	for _, stage := range []string{"record", "audit", "malformed audit identifier"} {
		t.Run(stage, func(t *testing.T) {
			s, store, sink, ctx := fixture(t)
			original := s.ids
			cause := errors.New("synthetic ID failure")
			calls := 0
			s.ids = idFunction(func(at time.Time) (string, error) {
				calls++
				if stage == "record" || calls == 2 {
					if stage == "malformed audit identifier" {
						return "invalid", nil
					}
					return "", cause
				}
				return original.New(at)
			})
			_, err := s.Create(ctx, input())
			requireCode(t, err, catalog.CodeInternal)
			if stage != "malformed audit identifier" && !errors.Is(err, cause) {
				t.Fatal("identifier cause lost")
			}
			if len(store.records) != 0 || len(sink.records) != 0 || s.active.Load() != 0 {
				t.Fatal("identifier failure changed state or admission")
			}
		})
	}
}

// TestFeedbackAuditFailureCannotCreate checks atomic creation and recovery of admission.
func TestFeedbackAuditFailureCannotCreate(t *testing.T) {
	s, store, sink, ctx := fixture(t)
	sink.fail = true
	if _, err := s.Create(ctx, input()); err == nil {
		t.Fatal("audit failure ignored")
	}
	if len(store.records) != 0 || len(sink.records) != 0 || s.active.Load() != 0 {
		t.Fatal("failed audit committed state or retained admission")
	}
	sink.fail = false
	if _, err := s.Create(ctx, input()); err != nil {
		t.Fatal(err)
	}
	if len(store.records) != 1 || len(sink.records) != 1 || s.active.Load() != 0 {
		t.Fatal("recovered create did not commit exactly once")
	}
}

// TestFeedbackRejectedCommandsPreserveState checks failures before repository mutation.
func TestFeedbackRejectedCommandsPreserveState(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*Service, context.Context, Record) error
		code catalog.ErrorCode
	}{
		{"unauthenticated read", func(s *Service, ctx context.Context, r Record) error {
			_, err := s.Get(identityfixture.WithActor(ctx, identity.Actor{}), r.ID)
			return err
		}, catalog.CodeForbidden},
		{"invalid read ID", func(s *Service, ctx context.Context, _ Record) error { _, err := s.Get(ctx, "invalid"); return err }, catalog.CodeInvalidArgument},
		{"missing record", func(s *Service, ctx context.Context, _ Record) error { _, err := s.Get(ctx, testRelease); return err }, catalog.CodeNotFound},
		{"reader cannot address", func(s *Service, ctx context.Context, r Record) error {
			_, err := s.Change(ctx, r.ID, 1, "address", EditInput{}, "reply")
			return err
		}, catalog.CodeForbidden},
		{"busy edit", func(s *Service, ctx context.Context, r Record) error {
			s.active.Store(8)
			defer s.active.Store(0)
			_, err := s.Change(ctx, r.ID, 1, "edit", EditInput{Category: "other", Comment: "edit"}, "")
			if s.active.Load() != 8 {
				return errors.New("rejected admission leaked a slot")
			}
			return err
		}, catalog.CodeDependencyUnavailable},
		{"invalid edit", func(s *Service, ctx context.Context, r Record) error {
			_, err := s.Change(ctx, r.ID, 1, "edit", EditInput{Category: "other", Comment: " "}, "")
			return err
		}, catalog.CodeInvalidArgument},
		{"missing edit target", func(s *Service, ctx context.Context, _ Record) error {
			_, err := s.Change(ctx, testRelease, 1, "edit", EditInput{}, "")
			return err
		}, catalog.CodeNotFound},
		{"unknown action", func(s *Service, ctx context.Context, r Record) error {
			_, err := s.Change(ctx, r.ID, 1, "unknown", EditInput{}, "")
			return err
		}, catalog.CodeInvalidArgument},
		{"invalid removal ID", func(s *Service, ctx context.Context, _ Record) error {
			a, _ := identity.FromContext(ctx)
			a.Roles = []identity.Role{identity.RoleTenantAdmin}
			_, err := s.Change(identityfixture.WithActor(ctx, a), "invalid", 1, "remove", EditInput{}, "")
			return err
		}, catalog.CodeInvalidArgument},
		{"oversize response", func(s *Service, ctx context.Context, r Record) error {
			a, _ := identity.FromContext(ctx)
			a.Roles = []identity.Role{identity.RoleTenantAdmin}
			_, err := s.Change(identityfixture.WithActor(ctx, a), r.ID, 1, "address", EditInput{}, strings.Repeat("x", MaxTextRunes+1))
			return err
		}, catalog.CodeInvalidArgument},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, store, sink, ctx := fixture(t)
			r, err := s.Create(ctx, input())
			if err != nil {
				t.Fatal(err)
			}
			requireCode(t, tc.run(s, ctx, r), tc.code)
			got, err := store.Get(ctx, testTenant, r.ID)
			if err != nil || got != r || len(sink.records) != 1 || s.active.Load() != 0 {
				t.Fatalf("rejected command changed state or admission: %v", err)
			}
		})
	}
}

// A failed policy dependency is an unavailable decision, never a deliberate
// refusal or an invitation to try another permission against another snapshot.
type unavailableFeedbackPermissions struct {
	identity.PermissionStore
	calls int
}

func (s *unavailableFeedbackPermissions) CheckPermissions(context.Context, identity.Actor, []identity.PermissionCheck) (identity.PermissionDecision, error) {
	s.calls++
	return identity.PermissionDecision{}, identity.E(identity.CodeDependency, "policy unavailable")
}
func TestFeedbackPermissionDependencyDoesNotFallBackToModeration(t *testing.T) {
	service, _, sink, ctx := fixture(t)
	a, _ := identity.FromContext(ctx)
	permissions := &unavailableFeedbackPermissions{}
	ctx = identity.WithActor(ctx, identity.BindPermissionStore(a, permissions))
	for _, run := range []func() error{
		func() error { _, err := service.Create(ctx, input()); return err },
		func() error { _, err := service.Get(ctx, testResource); return err },
		func() error { _, err := service.List(ctx, ListInput{Limit: 1}, false); return err },
	} {
		before := permissions.calls
		if err := run(); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
			t.Fatalf("policy failure lost: %v", err)
		}
		if permissions.calls != before+1 || len(sink.records) != 0 {
			t.Fatal("dependency failure retried another policy or persisted evidence")
		}
	}
}
