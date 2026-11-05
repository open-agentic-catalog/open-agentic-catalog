package identity

import (
	"context"
	"sync/atomic"
)

type collectionKey struct{}
type collectionContext struct {
	revision int64
	active   atomic.Bool
}

// WithCollectionContext selects current-policy collection reads. It is not a
// grant: repositories must test eligibility before ordering, limits or counts.
func WithCollectionContext(ctx context.Context) (context.Context, Actor, error) {
	ctx = WithPermissionContext(ctx)
	actor, ok := FromContext(ctx)
	if !ok || actor.permissionStore == nil {
		return ctx, Actor{}, E(CodeForbidden, "current Catalog authority is required")
	}
	if _, err := CurrentDecisionDigest(ctx); err != nil {
		return ctx, Actor{}, err
	}
	state, ok := ctx.Value(permissionContextKey{}).(*permissionContext)
	if !ok {
		return ctx, Actor{}, E(CodeDependency, "collection decision state is unavailable")
	}
	state.mu.Lock()
	revision := state.revision
	state.mu.Unlock()
	return context.WithValue(ctx, collectionKey{}, &collectionContext{revision: revision}), actor, nil
}

// CollectionRevision returns the policy revision to be selected with the rows.
// Missing collection context denotes an explicit internal repository caller.
func CollectionRevision(ctx context.Context) (int64, bool) {
	state, ok := ctx.Value(collectionKey{}).(*collectionContext)
	if !ok {
		return 0, false
	}
	return state.revision, true
}

// CollectionAllows is the in-memory equivalent of the SQL selection predicate.
// It does not append per-row command checks or materialize namespace allowlists.
func CollectionAllows(ctx context.Context, permission Permission, namespaceID string) (bool, error) {
	if _, selected := CollectionRevision(ctx); !selected {
		return true, nil
	}
	actor, ok := FromContext(ctx)
	if !ok || actor.permissionStore == nil {
		return false, E(CodeForbidden, "current Catalog authority is required")
	}
	decision, err := actor.permissionStore.CheckPermissions(ctx, actor, []PermissionCheck{{Permission: permission, NamespaceID: namespaceID}})
	if err != nil {
		return false, err
	}
	revision, _ := CollectionRevision(ctx)
	if decision.Revision != revision {
		return false, E(CodeInvalidArgument, "collection authorization changed; restart the read")
	}
	if len(decision.Allowed) != 1 {
		return false, E(CodeDependency, "permission decision count is invalid")
	}
	return decision.Allowed[0], nil
}

// CollectionAdmissionStore is the memory repository's coherent policy-read
// boundary. PostgreSQL selection instead uses its existing SQL transaction.
type CollectionAdmissionStore interface {
	WithCollectionAdmission(context.Context, Actor, int64, func(context.Context) error) error
}

// NeedsCollectionAdmission is false inside an active bounded read and for SQL
// stores, whose transaction owns the snapshot.
func NeedsCollectionAdmission(ctx context.Context) bool {
	state, selected := ctx.Value(collectionKey{}).(*collectionContext)
	if !selected || state.active.Load() {
		return false
	}
	actor, ok := FromContext(ctx)
	if !ok {
		return false
	}
	_, memory := actor.permissionStore.(CollectionAdmissionStore)
	return memory
}

// WithCollectionRead keeps memory policy stable across bounded local selection.
// PostgreSQL repositories implement this at their repeatable-read transaction.
// No external request may run within this callback.
func WithCollectionRead[T any](ctx context.Context, work func(context.Context) (T, error)) (T, error) {
	state, selected := ctx.Value(collectionKey{}).(*collectionContext)
	if !selected || state.active.Load() {
		return work(ctx)
	}
	actor, ok := FromContext(ctx)
	if !ok || actor.permissionStore == nil {
		var zero T
		return zero, E(CodeForbidden, "current Catalog authority is required")
	}
	if store, memory := actor.permissionStore.(CollectionAdmissionStore); memory {
		var result T
		err := store.WithCollectionAdmission(ctx, actor, state.revision, func(bound context.Context) error {
			boundState := &collectionContext{revision: state.revision}
			boundState.active.Store(true)
			defer boundState.active.Store(false)
			bound = context.WithValue(bound, collectionKey{}, boundState)
			var err error
			result, err = work(bound)
			return err
		})
		return result, err
	}
	return work(ctx)
}

// WithCollectionAdmission holds active membership and policy stable for a local read.
func (m *MemoryStore) WithCollectionAdmission(ctx context.Context, actor Actor, revision int64, work func(context.Context) error) error {
	if err := ValidatePermissionActor(actor); err != nil {
		return err
	}
	if work == nil {
		return E(CodeInvalidArgument, "collection operation is required")
	}
	var bindErr error
	ctx, bindErr = BindPermissionActor(ctx, actor)
	if bindErr != nil {
		return bindErr
	}
	if !memoryAdmissionFrom(ctx, m) {
		m.mu.RLock()
		defer m.mu.RUnlock()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	member, exists := m.memberships[actor.MembershipID]
	realm := m.realms[member.RealmID]
	if !exists || member.TenantID != actor.TenantID || member.RealmID != actor.RealmID || member.Subject != actor.Subject || member.ActorType != actor.ActorType || member.State != MembershipActive || realm.State != RealmActive {
		return E(CodeForbidden, "a current Catalog principal is required")
	}
	if m.revisions[actor.TenantID] != revision {
		return E(CodeInvalidArgument, "collection authorization changed; restart the read")
	}
	// Each nested read joins this synchronous policy admission. Escaped contexts
	// lose admission when the callback finishes, including panic paths.
	guard := &memoryAdmission{store: m}
	guard.active.Store(true)
	defer guard.active.Store(false)
	ctx = context.WithValue(ctx, memoryAdmissionKey{}, guard)
	return work(ctx)
}

// NeedsReadAdmission selects a bounded in-memory read fence before repository
// locks are acquired. This prevents both stale-policy disclosure and lock inversion.
func NeedsReadAdmission(ctx context.Context) bool {
	return NeedsCollectionAdmission(ctx) || NeedsPermissionAdmission(ctx)
}

// WithReadAdmission joins collection and exact-action checks under memory policy
// admission. It is used only for local repository work, never outbound calls.
func WithReadAdmission[T any](ctx context.Context, work func(context.Context) (T, error)) (T, error) {
	return WithCollectionRead(ctx, func(bound context.Context) (T, error) {
		if !NeedsPermissionAdmission(bound) {
			return work(bound)
		}
		var result T
		err := WithCurrentPermissionAdmission(bound, func(admitted context.Context) error {
			var err error
			result, err = work(admitted)
			return err
		})
		return result, err
	})
}

// RequireCollectionTenant keeps repository arguments bound to the admitted
// principal, including global readers. Internal owner calls have no marker.
func RequireCollectionTenant(ctx context.Context, tenantID string) error {
	if _, selected := CollectionRevision(ctx); !selected {
		return nil
	}
	actor, ok := FromContext(ctx)
	if !ok || actor.TenantID != tenantID {
		return E(CodeForbidden, "collection crossed tenant boundary")
	}
	return nil
}
