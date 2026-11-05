// Feedback commands enforce membership ownership, immutable context and text bounds.
package feedback

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"unicode"
	"unicode/utf8"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// Service owns feedback policy. Admission bounds concurrent writes per process,
// not requests per second or retained records across a deployment.
type Service struct {
	store   Store
	context ContextReader
	clock   catalog.Clock
	ids     catalog.IDGenerator
	active  atomic.Int32
}

// NewService requires persistence, context reads and deterministic time/IDs.
func NewService(store Store, reader ContextReader, clock catalog.Clock, ids catalog.IDGenerator) (*Service, error) {
	if store == nil || reader == nil || clock == nil || ids == nil {
		return nil, errors.New("feedback dependencies are required")
	}
	return &Service{store: store, context: reader, clock: clock, ids: ids}, nil
}

// actor requires current feedback authority; HTTP admission applies OAuth ceilings.
func actor(ctx context.Context, human, admin bool) (identity.Actor, error) {
	a, ok := identity.FromContext(ctx)
	if !ok || !identity.ValidTenantID(a.TenantID) || !identity.ValidTenantID(a.MembershipID) || !identity.ValidTenantID(a.RealmID) || !identity.ValidActorType(a.ActorType) || human && a.ActorType != identity.ActorHuman {
		return identity.Actor{}, catalog.E(catalog.CodeForbidden, "feedback access is not permitted")
	}
	if human && a.ActorType != identity.ActorHuman {
		return identity.Actor{}, catalog.E(catalog.CodeForbidden, "human feedback membership is required")
	}
	action := identity.Permission("feedback.own")
	if admin {
		action = "feedback.moderate"
	}
	if _, err := identity.RequirePermission(ctx, action, ""); err != nil {
		if human || admin || identity.ErrorCodeOf(err) != identity.CodeForbidden {
			return identity.Actor{}, catalog.PermissionError("current feedback permission is required", err)
		}
		if _, err := identity.RequirePermission(ctx, "feedback.moderate", ""); err != nil {
			return identity.Actor{}, catalog.PermissionError("current feedback permission is required", err)
		}
	}
	return a, nil
}

// ValidCategory reports the closed category vocabulary shared with OpenAPI.
func ValidCategory(value string) bool {
	switch value {
	case "problem", "documentation", "suggestion", "positive", "other":
		return true
	}
	return false
}

// ValidState reports normal and terminal feedback states.
func ValidState(value string) bool {
	switch value {
	case "open", "addressed", "withdrawn", "removed":
		return true
	}
	return false
}

// validText permits plain multiline Unicode, excluding invisible control codes.
func validText(value string, required bool) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > MaxTextRunes || required && strings.TrimSpace(value) == "" {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) && r != '\n' && r != '\t' && r != '\r' {
			return false
		}
	}
	return true
}

// admit bounds in-flight feedback writes without queueing or a new dependency.
func (s *Service) admit() (func(), error) {
	if s.active.Add(1) > 8 {
		s.active.Add(-1)
		return nil, catalog.E(catalog.CodeDependencyUnavailable, "feedback writes are busy; retry later")
	}
	return func() { s.active.Add(-1) }, nil
}

// Create derives ownership and version context and records one private submission.
func (s *Service) Create(ctx context.Context, input CreateInput) (Record, error) {
	ctx = identity.WithPermissionContext(ctx)
	a, err := actor(ctx, true, false)
	if err != nil {
		return Record{}, err
	}
	release, err := s.admit()
	if err != nil {
		return Record{}, err
	}
	defer release()
	if !identity.ValidTenantID(input.ResourceID) || !ValidCategory(input.Category) || !validText(input.Comment, true) || input.ReleaseID != "" && !identity.ValidTenantID(input.ReleaseID) || input.VersionRevisionID != "" && !identity.ValidTenantID(input.VersionRevisionID) {
		return Record{}, catalog.E(catalog.CodeInvalidArgument, "feedback input is invalid")
	}
	resource, err := s.context.GetResource(ctx, a.TenantID, input.ResourceID)
	if err != nil {
		return Record{}, err
	}
	if _, err := identity.RequirePermission(ctx, "resource.read", resource.NamespaceID); err != nil {
		return Record{}, catalog.PermissionError("feedback resource is not readable", err)
	}
	if input.ReleaseID != "" {
		r, err := s.context.GetRelease(ctx, a.TenantID, input.ReleaseID)
		if err != nil {
			return Record{}, err
		}
		if input.VersionRevisionID != "" && input.VersionRevisionID != r.VersionRevisionID {
			return Record{}, catalog.E(catalog.CodeInvalidArgument, "feedback context does not match")
		}
		input.VersionRevisionID = r.VersionRevisionID
	}
	if input.VersionRevisionID != "" {
		r, _, err := s.context.GetVersionRevision(ctx, a.TenantID, input.VersionRevisionID)
		if err != nil {
			return Record{}, err
		}
		v, err := s.context.GetVersion(ctx, a.TenantID, r.ResourceVersionID)
		if err != nil {
			return Record{}, err
		}
		if v.ResourceID != input.ResourceID {
			return Record{}, catalog.E(catalog.CodeInvalidArgument, "feedback context does not match")
		}
	}
	now := catalog.TransitionTime(s.clock.Now())
	id, err := s.ids.New(now)
	if err != nil {
		return Record{}, catalog.Wrap(catalog.CodeInternal, "create feedback identifier", err)
	}
	r := Record{ID: id, TenantID: a.TenantID, MembershipID: a.MembershipID, ResourceID: input.ResourceID, ReleaseID: input.ReleaseID, VersionRevisionID: input.VersionRevisionID, Category: input.Category, Comment: input.Comment, State: "open", Revision: 1, CreatedAt: now, UpdatedAt: now}
	return s.save(ctx, r, 0, "created")
}

// Get returns current text only to its submitter or a same-tenant administrator.
func (s *Service) Get(ctx context.Context, id string) (Record, error) {
	ctx = identity.WithPermissionContext(ctx)
	a, err := actor(ctx, false, false)
	if err != nil {
		return Record{}, err
	}
	if !identity.ValidTenantID(id) {
		return Record{}, catalog.E(catalog.CodeInvalidArgument, "feedback identifier is invalid")
	}
	r, err := s.store.Get(ctx, a.TenantID, id)
	if err != nil {
		return Record{}, err
	}
	if r.MembershipID != a.MembershipID {
		if _, err := identity.RequirePermission(ctx, "feedback.moderate", ""); err != nil {
			if identity.ErrorCodeOf(err) != identity.CodeForbidden {
				return Record{}, catalog.PermissionError("current feedback permission is required", err)
			}
			return Record{}, catalog.E(catalog.CodeNotFound, "feedback was not found")
		}
	}
	if _, err = s.context.GetResource(ctx, a.TenantID, r.ResourceID); err != nil {
		return Record{}, err
	}
	return r, nil
}

// List separates the caller's own submissions from the administrator inbox.
func (s *Service) List(ctx context.Context, input ListInput, inbox bool) ([]Record, error) {
	ctx = identity.WithPermissionContext(ctx)
	a, err := actor(ctx, !inbox, inbox)
	if err != nil {
		return nil, err
	}
	if input.Limit < 1 || input.Limit > 100 || input.AfterID != "" && !identity.ValidTenantID(input.AfterID) || input.ResourceID != "" && !identity.ValidTenantID(input.ResourceID) || input.Category != "" && !ValidCategory(input.Category) || input.State != "" && !ValidState(input.State) {
		return nil, catalog.E(catalog.CodeInvalidArgument, "feedback page is invalid")
	}
	input.MembershipID = ""
	if !inbox {
		input.MembershipID = a.MembershipID
	}
	input.Limit++
	return s.store.List(ctx, a.TenantID, input)
}

// Change applies a single revision-fenced action; responses never become threads.
// Reopening clears the previous response, and addressing may have no response.
func (s *Service) Change(ctx context.Context, id string, expected int64, action string, input EditInput, response string) (Record, error) {
	ctx = identity.WithPermissionContext(ctx)
	admin := action == "address" || action == "reopen" || action == "remove"
	a, err := actor(ctx, !admin, admin)
	if err != nil {
		return Record{}, err
	}
	release, err := s.admit()
	if err != nil {
		return Record{}, err
	}
	defer release()
	var r Record
	if action == "remove" {
		if !identity.ValidTenantID(id) {
			return Record{}, catalog.E(catalog.CodeInvalidArgument, "feedback identifier is invalid")
		}
		r, err = s.store.Get(ctx, a.TenantID, id)
	} else {
		r, err = s.Get(ctx, id)
	}
	if err != nil {
		return Record{}, err
	}
	if !admin && r.MembershipID != a.MembershipID {
		return Record{}, catalog.E(catalog.CodeNotFound, "feedback was not found")
	}
	if expected < 1 || r.Revision != expected {
		return Record{}, catalog.E(catalog.CodePreconditionFailed, "feedback revision does not match")
	}
	if r.State == "withdrawn" || r.State == "removed" {
		return Record{}, catalog.E(catalog.CodeConflict, "feedback is terminal")
	}
	switch action {
	case "edit":
		if !ValidCategory(input.Category) || !validText(input.Comment, true) {
			return Record{}, catalog.E(catalog.CodeInvalidArgument, "feedback input is invalid")
		}
		r.Category = input.Category
		r.Comment = input.Comment
		r.State = "open"
		r.Response = ""
	case "address":
		if !validText(response, false) {
			return Record{}, catalog.E(catalog.CodeInvalidArgument, "feedback response is invalid")
		}
		r.State = "addressed"
		r.Response = response
	case "reopen":
		r.State = "open"
		r.Response = ""
	case "withdraw":
		r.State = "withdrawn"
		r.Comment = ""
		r.Response = ""
	case "remove":
		r.State = "removed"
		r.Comment = ""
		r.Response = ""
	default:
		return Record{}, catalog.E(catalog.CodeInvalidArgument, "feedback action is invalid")
	}
	r.Revision++
	// Retained state is the time floor across replica skew or clock corrections.
	// Revision fencing still rejects concurrent changes under the store's lock.
	r.UpdatedAt = catalog.TransitionTime(s.clock.Now(), r.UpdatedAt)
	return s.save(ctx, r, expected, action)
}

// save constructs safe closed audit facts and delegates the atomic commit.
func (s *Service) save(ctx context.Context, r Record, expected int64, action string) (Record, error) {
	a, _ := identity.FromContext(ctx)
	id, err := s.ids.New(r.UpdatedAt)
	if err != nil {
		return Record{}, catalog.Wrap(catalog.CodeInternal, "create feedback audit identifier", err)
	}
	execution := audit.ExecutionFrom(ctx)
	if execution.AuthorizationDigest == "" || len(identity.RecordedPermissionChecks(ctx)) > 0 {
		execution.AuthorizationDigest = identity.DecisionDigest(ctx)
	}
	evidence, err := audit.NewRecord(id, r.TenantID, audit.Principal{ID: a.MembershipID, Type: audit.PrincipalCatalogMembership, ActorType: string(a.ActorType), RealmID: a.RealmID}, "feedback."+action, "feedback", r.ID, "", execution, map[string]any{"revision": r.Revision, "state": r.State}, r.UpdatedAt)
	if err != nil {
		return Record{}, catalog.Wrap(catalog.CodeInternal, "construct feedback audit", err)
	}
	return s.store.Save(ctx, r, expected, evidence)
}
