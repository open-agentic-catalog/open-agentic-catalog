package ratings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

type Service struct {
	store     Store
	authority Authority
	clock     catalog.Clock
	ids       catalog.IDGenerator
}

func New(store Store, authority Authority, clock catalog.Clock, ids catalog.IDGenerator) (*Service, error) {
	if store == nil || authority == nil || clock == nil || ids == nil {
		return nil, errors.New("rating dependencies are required")
	}
	return &Service{store: store, authority: authority, clock: clock, ids: ids}, nil
}

func human(ctx context.Context) (identity.Actor, string, error) {
	a, ok := identity.FromContext(ctx)
	if !ok {
		return a, "", catalog.E(catalog.CodeUnauthenticated, "rating authentication is required")
	}
	key, err := PrincipalKey(a)
	return a, key, err
}

func (s *Service) admit(ctx context.Context, input Admission, write bool) (Admission, error) {
	if err := ValidateTarget(input.Target); err != nil {
		return input, err
	}
	admitted, err := s.authority.Admit(ctx, input)
	if err != nil {
		return input, err
	}
	if admitted.Target != input.Target {
		return input, catalog.E(catalog.CodePreconditionFailed, "rating target changed; refresh the resource")
	}
	if write {
		if _, err = identity.RequirePermission(ctx, "ratings.own", admitted.NamespaceID); err != nil {
			return input, catalog.PermissionError("current rating permission is required", err)
		}
	}
	return admitted, nil
}

func (s *Service) Read(ctx context.Context, input Admission) (View, error) {
	ctx = identity.WithPermissionContext(ctx)
	a, ok := identity.FromContext(ctx)
	if !ok {
		return View{}, catalog.E(catalog.CodeUnauthenticated, "rating authentication is required")
	}
	admitted, err := s.admit(ctx, input, false)
	if err != nil {
		return View{}, err
	}
	values, err := s.store.Aggregates(ctx, a.TenantID, []Target{admitted.Target})
	if err != nil {
		return View{}, err
	}
	aggregate := values[Key(admitted.Target)]
	if !aggregate.Valid() {
		return View{}, catalog.E(catalog.CodeInternal, "rating aggregate is invalid")
	}
	view := View{Target: admitted.Target, Count: aggregate.Count}
	if aggregate.Count > 0 {
		average := aggregate.Average()
		view.Average = &average
	}
	if key, keyErr := PrincipalKey(a); keyErr == nil {
		own, readErr := s.store.Own(ctx, a.TenantID, admitted.Target, key)
		if readErr == nil {
			view.Own = &own
			view.ChangedEntry = own.EntryDigest != "" && admitted.EntryDigest != "" && own.EntryDigest != admitted.EntryDigest
		} else if catalog.ErrorCodeOf(readErr) != catalog.CodeNotFound {
			return View{}, readErr
		}
	}
	return view, nil
}

// Submit replaces the current opinion. A missing consent preserves an existing
// active choice; first submissions and withdrawn submissions default private.
func (s *Service) Submit(ctx context.Context, input Admission, score int, consent *bool, expected int64) (Record, error) {
	ctx = identity.WithPermissionContext(ctx)
	a, key, err := human(ctx)
	if err != nil {
		return Record{}, err
	}
	if score < 1 || score > 5 || expected < 0 {
		return Record{}, catalog.E(catalog.CodeInvalidArgument, "rating score must be an integer from one to five")
	}
	admitted, err := s.admit(ctx, input, true)
	if err != nil {
		return Record{}, err
	}
	old, err := s.store.Own(ctx, a.TenantID, admitted.Target, key)
	if err != nil && catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		return Record{}, err
	}
	if old.Revision != expected {
		return Record{}, catalog.E(catalog.CodePreconditionFailed, "rating revision does not match")
	}
	if old.State == "removed" {
		return Record{}, catalog.E(catalog.CodeConflict, "a removed rating requires administrator permission to submit again")
	}
	now := catalog.TransitionTime(s.clock.Now(), old.UpdatedAt)
	value := old
	if expected == 0 {
		value.ID, err = s.ids.New(now)
		if err != nil {
			return Record{}, err
		}
		value.TenantID = a.TenantID
		value.PrincipalKey = key
		value.Target = admitted.Target
		value.CreatedAt = now
	}
	value.SubmittedBy = a.MembershipID
	value.Revision = expected + 1
	value.UpdatedAt = now
	value.State = "active"
	value.Score = score
	value.ReleaseID = admitted.ReleaseID
	value.EntryDigest = admitted.EntryDigest
	if old.State != "active" {
		value.Consent = false
	}
	if consent != nil {
		value.Consent = *consent
	}
	return s.save(ctx, value, expected, admitted, "submit", "")
}

// Change allows owner withdrawal after target visibility is lost. Moderators
// can remove or permit a fresh submission; they cannot select a score.
func (s *Service) Change(ctx context.Context, id string, expected int64, action, reason string) (Record, error) {
	ctx = identity.WithPermissionContext(ctx)
	a, ok := identity.FromContext(ctx)
	if !ok {
		return Record{}, catalog.E(catalog.CodeUnauthenticated, "rating authentication is required")
	}
	if !identity.ValidTenantID(id) || expected < 1 {
		return Record{}, catalog.E(catalog.CodeInvalidArgument, "rating command is invalid")
	}
	if action != "withdraw" && action != "remove" && action != "permit" {
		return Record{}, catalog.E(catalog.CodeInvalidArgument, "rating action is invalid")
	}
	if action != "withdraw" {
		if _, err := identity.RequirePermission(ctx, "ratings.moderate", ""); err != nil {
			return Record{}, catalog.PermissionError("rating moderation is not permitted", err)
		}
	}
	value, err := s.store.Get(ctx, a.TenantID, id)
	if err != nil {
		return Record{}, err
	}
	if action == "withdraw" {
		_, key, keyErr := human(ctx)
		if keyErr != nil {
			return Record{}, keyErr
		}
		if value.PrincipalKey != key {
			return Record{}, catalog.E(catalog.CodeNotFound, "rating was not found")
		}
	}
	if value.Revision != expected {
		return Record{}, catalog.E(catalog.CodePreconditionFailed, "rating revision does not match")
	}
	if action == "permit" && value.State != "removed" || action == "withdraw" && value.State != "active" || action == "remove" && value.State == "removed" {
		return Record{}, catalog.E(catalog.CodeConflict, "rating action is not available")
	}
	value.State = "withdrawn"
	if action == "remove" {
		value.State = "removed"
	}
	value.Score = 0
	value.Consent = false
	value.Revision++
	value.UpdatedAt = catalog.TransitionTime(s.clock.Now(), value.UpdatedAt)
	return s.save(ctx, value, expected, Admission{}, action, reason)
}

func (s *Service) Get(ctx context.Context, id string) (Record, error) {
	ctx = identity.WithPermissionContext(ctx)
	a, ok := identity.FromContext(ctx)
	if !ok {
		return Record{}, catalog.E(catalog.CodeUnauthenticated, "rating authentication is required")
	}
	if !identity.ValidTenantID(id) {
		return Record{}, catalog.E(catalog.CodeInvalidArgument, "rating identifier is invalid")
	}
	value, err := s.store.Get(ctx, a.TenantID, id)
	if err != nil {
		return value, err
	}
	if key, keyErr := PrincipalKey(a); keyErr == nil && key == value.PrincipalKey {
		return value, nil
	}
	if _, err := identity.RequirePermission(ctx, "ratings.moderate", ""); err != nil {
		return Record{}, catalog.E(catalog.CodeNotFound, "rating was not found")
	}
	return value, nil
}

func (s *Service) List(ctx context.Context, q ListInput, inbox bool) ([]Record, error) {
	ctx = identity.WithPermissionContext(ctx)
	a, ok := identity.FromContext(ctx)
	if !ok {
		return nil, catalog.E(catalog.CodeUnauthenticated, "rating authentication is required")
	}
	if q.Limit < 1 || q.Limit > 100 || q.AfterID != "" && !identity.ValidTenantID(q.AfterID) || q.State != "" && !validState(q.State) {
		return nil, catalog.E(catalog.CodeInvalidArgument, "rating page is invalid")
	}
	q.PrincipalKey = ""
	if inbox {
		if _, err := identity.RequirePermission(ctx, "ratings.moderate", ""); err != nil {
			return nil, catalog.PermissionError("rating moderation is not permitted", err)
		}
	} else {
		var err error
		_, q.PrincipalKey, err = human(ctx)
		if err != nil {
			return nil, err
		}
	}
	q.Limit++
	return s.store.List(ctx, a.TenantID, q)
}

func (s *Service) save(ctx context.Context, value Record, expected int64, admission Admission, action, reason string) (Record, error) {
	a, _ := identity.FromContext(ctx)
	id, err := s.ids.New(value.UpdatedAt)
	if err != nil {
		return Record{}, err
	}
	execution := audit.ExecutionFrom(ctx)
	execution.AuthorizationDigest = identity.DecisionDigest(ctx)
	evidence, err := audit.NewRecord(id, a.TenantID, audit.Principal{ID: a.MembershipID, Type: audit.PrincipalCatalogMembership, ActorType: string(a.ActorType), RealmID: a.RealmID}, "rating."+action, "rating", value.ID, reason, execution, map[string]any{"revision": value.Revision, "state": value.State}, value.UpdatedAt)
	if err != nil {
		return Record{}, catalog.Wrap(catalog.CodeInvalidArgument, "rating reason or audit context is invalid", err)
	}
	return s.store.Save(ctx, value, expected, admission, evidence)
}

func validState(state string) bool {
	return state == "active" || state == "withdrawn" || state == "removed"
}

// ValidateSave runs again under each repository's write lock.
func ValidateSave(old, value Record, expected int64, e audit.Record) error {
	if old.Revision != expected {
		return catalog.E(catalog.CodePreconditionFailed, "rating revision does not match")
	}
	if ValidateTarget(value.Target) != nil || !identity.ValidTenantID(value.SubmittedBy) || !identity.ValidTenantID(value.ID) || !identity.ValidTenantID(value.TenantID) || !digestPattern.MatchString(value.PrincipalKey) || !validState(value.State) || value.Revision != expected+1 || value.CreatedAt.IsZero() || value.UpdatedAt.Before(value.CreatedAt) || value.EntryDigest != "" && !digestPattern.MatchString(value.EntryDigest) {
		return catalog.E(catalog.CodeInvalidArgument, "rating record is invalid")
	}
	if value.State == "active" && (value.Score < 1 || value.Score > 5) || value.State != "active" && (value.Score != 0 || value.Consent) {
		return catalog.E(catalog.CodeInvalidArgument, "rating score or consent is invalid")
	}
	if expected > 0 && (old.ID != value.ID || old.TenantID != value.TenantID || old.PrincipalKey != value.PrincipalKey || old.Target != value.Target || !old.CreatedAt.Equal(value.CreatedAt) || value.UpdatedAt.Before(old.UpdatedAt)) {
		return catalog.E(catalog.CodeConflict, "rating identity cannot change")
	}
	if expected == 0 && (e.Action != "rating.submit" || !value.CreatedAt.Equal(value.UpdatedAt)) {
		return catalog.E(catalog.CodeConflict, "invalid rating creation")
	}
	if e.Action != "rating.submit" && (old.ReleaseID != value.ReleaseID || old.EntryDigest != value.EntryDigest || old.SubmittedBy != value.SubmittedBy) {
		return catalog.E(catalog.CodeConflict, "rating evidence cannot change")
	}
	switch e.Action {
	case "rating.submit":
		if value.State != "active" || old.State == "removed" {
			return catalog.E(catalog.CodeConflict, "invalid rating submission")
		}
	case "rating.withdraw":
		if old.State != "active" || value.State != "withdrawn" {
			return catalog.E(catalog.CodeConflict, "invalid rating withdrawal")
		}
	case "rating.remove":
		if expected == 0 || old.State == "removed" || value.State != "removed" {
			return catalog.E(catalog.CodeConflict, "invalid rating removal")
		}
	case "rating.permit":
		if old.State != "removed" || value.State != "withdrawn" {
			return catalog.E(catalog.CodeConflict, "invalid rating permission")
		}
	default:
		return catalog.E(catalog.CodeInvalidArgument, "rating audit action is invalid")
	}
	if e.TenantID != value.TenantID || e.TargetID != value.ID || e.TargetType != "rating" || !e.At.Equal(value.UpdatedAt) || audit.ValidateNewRecord(e) != nil {
		return catalog.E(catalog.CodeInvalidArgument, "rating audit binding is invalid")
	}
	facts, err := json.Marshal(e.Details["facts"])
	if err != nil {
		return catalog.E(catalog.CodeInvalidArgument, "rating audit facts are invalid")
	}
	want, err := json.Marshal(map[string]any{"revision": value.Revision, "state": value.State})
	if err != nil {
		return catalog.E(catalog.CodeInvalidArgument, "rating audit facts are invalid")
	}
	if !bytes.Equal(facts, want) {
		return catalog.E(catalog.CodeInvalidArgument, "rating audit facts do not match")
	}
	return nil
}

// SelectedSummaries decorates a bounded, already-authorized discovery page.
// Remote callers must use Read, which checks exact current target visibility.
func (s *Service) SelectedSummaries(ctx context.Context, targets []Target) (map[string]Aggregate, error) {
	a, ok := identity.FromContext(ctx)
	if !ok {
		return nil, catalog.E(catalog.CodeUnauthenticated, "rating authentication is required")
	}
	if len(targets) > 100 {
		return nil, catalog.E(catalog.CodeInvalidArgument, "rating page exceeds its bound")
	}
	return s.store.Aggregates(ctx, a.TenantID, targets)
}
