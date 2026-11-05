package identity

import "context"

type internalAuthorityKey struct{}
type internalAuthority struct {
	kind, tenant, target string
	verify               func(context.Context) error
	lease                InternalLease
}

type InternalLease struct {
	JobID, Owner, Kind, TenantID, Target string
	Attempt                              int
}

// WithInternalJobAuthority requires the claimed lease binding separately from
// actor classification and audit attribution.
func WithInternalJobAuthority(ctx context.Context, kind, tenant, target, jobID, owner string, attempt int, verify func(context.Context) error) (context.Context, error) {
	if !ValidTenantID(jobID) || owner == "" || len(owner) > 256 || attempt < 1 || kind != "source.trust_verify" && kind != "source.sync" && kind != "publication.generate" {
		return nil, E(CodeForbidden, "internal Job lease binding is invalid")
	}
	bound, err := withInternalAuthority(ctx, kind, tenant, target, verify)
	if err != nil {
		return nil, err
	}
	authority, ok := bound.Value(internalAuthorityKey{}).(*internalAuthority)
	if !ok {
		return nil, E(CodeInternal, "internal execution binding is missing")
	}
	authority.lease = InternalLease{JobID: jobID, Owner: owner, Kind: kind, TenantID: tenant, Target: target, Attempt: attempt}
	return bound, nil
}

func InternalLeaseFrom(ctx context.Context) (InternalLease, bool) {
	authority, ok := ctx.Value(internalAuthorityKey{}).(*internalAuthority)
	if !ok || authority.lease.JobID == "" {
		return InternalLease{}, false
	}
	return authority.lease, true
}

// WithInternalAuthority is used only by trusted composition after selecting a
// configured discovery host or validating a claimed Job. It has one action,
// tenant and target, never administrator or arbitrary user-grant authority.
func WithInternalAuthority(ctx context.Context, kind, tenant, target string, verify func(context.Context) error) (context.Context, error) {
	if kind != "federation.discover" {
		return nil, E(CodeForbidden, "a claimed Job binding is required")
	}
	bound, err := withInternalAuthority(ctx, kind, tenant, target, verify)
	if err != nil {
		return nil, err
	}
	// Host-bound discovery uses its own narrow internal authority. The original
	// caller's preparatory checks must remain on the caller context, rather than
	// being rechecked against the internal service principal at SQL admission.
	return context.WithValue(bound, permissionContextKey{}, &permissionContext{}), nil
}

func withInternalAuthority(ctx context.Context, kind, tenant, target string, verify func(context.Context) error) (context.Context, error) {
	if !ValidTenantID(tenant) || target == "" || verify == nil || kind != "source.trust_verify" && kind != "source.sync" && kind != "publication.generate" && kind != "federation.discover" {
		return nil, E(CodeForbidden, "internal execution binding is invalid")
	}
	authority := &internalAuthority{kind: kind, tenant: tenant, target: target, verify: verify}
	return context.WithValue(ctx, internalAuthorityKey{}, authority), nil
}

// RequireInternalTarget refuses reuse of a claimed/configured execution for
// another kind, tenant or target. A client Actor alone has no internal binding.
func RequireInternalTarget(ctx context.Context, kind, tenant, target string) error {
	authority, ok := ctx.Value(internalAuthorityKey{}).(*internalAuthority)
	if !ok {
		return nil
	} // Ordinary callers still require their current permission.
	if authority.kind != kind || authority.tenant != tenant || authority.target != target {
		return E(CodeForbidden, "internal execution target does not match")
	}
	return authority.verify(ctx)
}

func internalPermission(ctx context.Context, action Permission, scope string) (Actor, bool, error) {
	authority, ok := ctx.Value(internalAuthorityKey{}).(*internalAuthority)
	if !ok {
		return Actor{}, false, nil
	}
	a, ok := FromContext(ctx)
	if !ok || a.TenantID != authority.tenant || a.ActorType != ActorService || scope != "" {
		return Actor{}, true, E(CodeForbidden, "internal execution principal does not match")
	}
	want := Permission(authority.kind)
	if authority.kind == "publication.generate" {
		want = "publication.generate"
	}
	if action != want {
		return Actor{}, true, E(CodeForbidden, "internal execution action does not match")
	}
	if err := ctx.Err(); err != nil {
		return Actor{}, true, Wrap(CodeDependency, "internal execution canceled", err)
	}
	if err := authority.verify(ctx); err != nil {
		return Actor{}, true, err
	}
	return a, true, nil
}
