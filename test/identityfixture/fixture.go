// Package identityfixture supplies explicit built-in grants to isolated domain
// fixtures using the real in-memory permission evaluator. It is not a runtime
// fallback, an allow-all policy or evidence of PostgreSQL revocation behavior.
package identityfixture

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

type store struct {
	identity.PermissionStore
	original identity.Actor
	resolved identity.Actor
}

// WithActor preserves existing fixture attribution while binding its explicit
// grants to a real seeded identity store. Invalid principals remain unbound so
// admission-negative fixtures still fail closed.
func WithActor(ctx context.Context, a identity.Actor) context.Context {
	if a.TenantID == "" || !identity.ValidSubject(a.Subject) {
		return identity.WithActor(ctx, a)
	}
	typ := a.ActorType
	if typ == "" {
		typ = identity.ActorHuman
	}
	namespaces := map[string][]identity.Role{}
	for key, roles := range a.Namespaces {
		namespaces[fixtureID(key)] = roles
	}
	seeds := identity.Seed{TenantID: fixtureID(a.TenantID), Issuer: "urn:catalog:unit-fixture", Subject: a.Subject, ActorType: typ, Roles: a.Roles, Namespaces: namespaces}
	m, err := identity.NewMemoryStore([]identity.Seed{seeds})
	if err != nil {
		return identity.WithActor(ctx, a)
	}
	resolved, err := m.ResolvePrincipal(ctx, identity.Principal{TenantID: seeds.TenantID, Issuer: seeds.Issuer, Subject: a.Subject, Claims: a.Claims})
	if err != nil {
		panic(err)
	}
	a = identity.BindPermissionStore(a, &store{PermissionStore: m, original: a, resolved: resolved})
	return identity.WithActor(ctx, a)
}

func (s *store) CheckPermissions(ctx context.Context, a identity.Actor, c []identity.PermissionCheck) (identity.PermissionDecision, error) {
	if a.TenantID != s.original.TenantID || a.Subject != s.original.Subject || a.MembershipID != s.original.MembershipID || a.RealmID != s.original.RealmID || a.ActorType != s.original.ActorType {
		return identity.PermissionDecision{}, identity.E(identity.CodeForbidden, "fixture principal was changed")
	}
	checks := append([]identity.PermissionCheck(nil), c...)
	for i := range checks {
		if checks[i].NamespaceID != "" {
			checks[i].NamespaceID = fixtureID(checks[i].NamespaceID)
		}
	}
	return s.PermissionStore.CheckPermissions(ctx, s.resolved, checks)
}

func (s *store) WithPermissionAdmission(ctx context.Context, a identity.Actor, c []identity.PermissionCheck, work func(context.Context) error) error {
	// Check the original fixture identity before using its normalized keys.
	if _, err := s.CheckPermissions(ctx, a, c); err != nil {
		return err
	}
	checks := append([]identity.PermissionCheck(nil), c...)
	for i := range checks {
		if checks[i].NamespaceID != "" {
			checks[i].NamespaceID = fixtureID(checks[i].NamespaceID)
		}
	}
	return s.PermissionStore.WithPermissionAdmission(identity.WithActor(ctx, s.resolved), s.resolved, checks, func(bound context.Context) error {
		return work(identity.WithActor(bound, identity.BindPermissionStore(s.original, s)))
	})
}

// Some existing repository doubles use symbolic tenant/namespace keys. Keep
// those doubles' attribution intact while giving the real evaluator UUID keys.
// Different symbolic keys remain different scopes; no grant is manufactured.
func fixtureID(key string) string {
	if identity.ValidTenantID(key) {
		return key
	}
	h := sha256.Sum256([]byte(key))
	return fmt.Sprintf("%x-%x-7000-8000-%x", h[:4], h[4:6], h[6:12])
}

// WithCollectionAdmission forwards to the real memory policy lock, preserving
// symbolic repository keys solely at this existing unit-fixture boundary.
func (s *store) WithCollectionAdmission(ctx context.Context, a identity.Actor, revision int64, work func(context.Context) error) error {
	if _, err := s.CheckPermissions(ctx, a, []identity.PermissionCheck{{Permission: "access.manage"}}); err != nil {
		return err
	}
	memory, ok := s.PermissionStore.(identity.CollectionAdmissionStore)
	if !ok {
		return identity.E(identity.CodeDependency, "fixture collection admission is unavailable")
	}
	return memory.WithCollectionAdmission(identity.WithActor(ctx, s.resolved), s.resolved, revision, func(bound context.Context) error {
		return work(identity.WithActor(bound, identity.BindPermissionStore(s.original, s)))
	})
}

func (s *store) ReadSelfAccess(ctx context.Context, a identity.Actor, request identity.SelfAccessRequest) (identity.SelfAccessProjection, error) {
	if _, err := s.CheckPermissions(ctx, a, []identity.PermissionCheck{{Permission: "access.manage"}}); err != nil {
		return identity.SelfAccessProjection{}, err
	}
	self, ok := s.PermissionStore.(identity.SelfAccessStore)
	if !ok {
		return identity.SelfAccessProjection{}, identity.E(identity.CodeDependency, "fixture self-access is unavailable")
	}
	return self.ReadSelfAccess(identity.WithActor(ctx, s.resolved), s.resolved, request)
}
