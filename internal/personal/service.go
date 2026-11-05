package personal

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"reflect"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type Service struct {
	store     Store
	authority Authority
	clock     catalog.Clock
	ids       catalog.IDGenerator
}

func New(store Store, authority Authority, clock catalog.Clock, ids catalog.IDGenerator) (*Service, error) {
	if store == nil || authority == nil || clock == nil || ids == nil {
		return nil, errors.New("personal library dependencies are required")
	}
	return &Service{store, authority, clock, ids}, nil
}
func Actor(ctx context.Context) (identity.Actor, error) {
	a, ok := identity.FromContext(ctx)
	if !ok || a.ActorType != identity.ActorHuman || !identity.ValidTenantID(a.MembershipID) || !identity.ValidTenantID(a.RealmID) {
		return a, catalog.E(catalog.CodeForbidden, "personal library requires a human membership")
	}
	if _, err := identity.RequirePermission(ctx, "library.own", ""); err != nil {
		return a, catalog.PermissionError("personal library access is not permitted", err)
	}
	return a, nil
}

// ValidateLabel preserves exact spelling; significant spaces are never normalized.
func ValidateLabel(label string) error {
	if !utf8.ValidString(label) || utf8.RuneCountInString(label) > MaxLabelRunes || strings.TrimSpace(label) == "" || strings.IndexFunc(label, unicode.IsControl) >= 0 {
		return catalog.E(catalog.CodeInvalidArgument, "saved label requires 1 to 120 characters without controls")
	}
	return nil
}
func Validate(in Input) error {
	if err := ValidateLabel(in.Label); err != nil {
		return err
	}
	if in.Type == "search" && in.Search != nil && in.Reference == nil {
		return catalog.ValidateSavedSearch(in.Search.Query())
	}
	if in.Type != "resource" || in.Reference == nil || in.Search != nil {
		return catalog.E(catalog.CodeInvalidArgument, "select one saved resource or search")
	}
	r := *in.Reference
	if r.Origin == "local" && r.Kind == "" && identity.ValidTenantID(r.ReleaseID) && identity.ValidTenantID(r.VersionRevisionID) && r.SourceID == "" && r.PresentationGeneration == 0 && r.Identifier == "" && r.VersionPresent == nil && r.Version == "" {
		return nil
	}
	if r.Origin == "foreign" && r.Kind.Valid() && r.ReleaseID == "" && r.VersionRevisionID == "" && identity.ValidTenantID(r.SourceID) && r.PresentationGeneration > 0 && r.VersionPresent != nil && (*r.VersionPresent || r.Version == "") && utf8.ValidString(r.Version) && utf8.RuneCountInString(r.Version) <= 128 && !strings.ContainsAny(r.Version, "\x00\r\n") {
		parsed, err := catalog.ParseAIRIdentifier(r.Identifier)
		if err == nil && parsed.String() == r.Identifier && utf8.RuneCountInString(r.Identifier) <= 512 {
			return nil
		}
	}
	return catalog.E(catalog.CodeInvalidArgument, "saved resource identity is invalid")
}
func (s *Service) Get(ctx context.Context, id string) (Item, error) {
	a, err := Actor(ctx)
	if err != nil {
		return Item{}, err
	}
	if !identity.ValidTenantID(id) {
		return Item{}, catalog.E(catalog.CodeInvalidArgument, "saved item ID is invalid")
	}
	return s.store.Get(ctx, a, id)
}
func (s *Service) List(ctx context.Context, q ListRequest) ([]Item, error) {
	a, err := Actor(ctx)
	if err != nil {
		return nil, err
	}
	if q.Type != "" && q.Type != "resource" && q.Type != "search" || q.Limit < 1 || q.Limit > 100 || q.AfterID != "" && !identity.ValidTenantID(q.AfterID) {
		return nil, catalog.E(catalog.CodeInvalidArgument, "saved item page is invalid")
	}
	if q.Label != nil {
		if err := ValidateLabel(*q.Label); err != nil {
			return nil, err
		}
	}
	q.Limit++
	return s.store.List(ctx, a, q)
}
func (s *Service) Save(ctx context.Context, id string, expected int64, in Input, remove bool) (Item, error) {
	ctx = identity.WithPermissionContext(ctx)
	a, err := Actor(ctx)
	if err != nil {
		return Item{}, err
	}
	var old Item
	if id != "" {
		old, err = s.Get(ctx, id)
		if err != nil {
			return Item{}, err
		}
		if expected < 1 || old.Revision != expected {
			return Item{}, catalog.E(catalog.CodePreconditionFailed, "saved item revision changed")
		}
	} else if expected != 0 || remove {
		return Item{}, catalog.E(catalog.CodeInvalidArgument, "saved item command is invalid")
	}
	if !remove {
		if err := Validate(in); err != nil {
			return Item{}, err
		}
		bytes, err := json.Marshal(in)
		if err != nil || len(bytes) > MaxBodyBytes {
			return Item{}, catalog.E(catalog.CodeInvalidArgument, "saved item exceeds the body limit")
		}
		if in.Reference != nil && !reflect.DeepEqual(in.Reference, old.Reference) {
			if err := s.authority.Admit(ctx, *in.Reference); err != nil {
				return Item{}, err
			}
		}
	}
	now := s.clock.Now().UTC().Truncate(time.Microsecond)
	if now.Before(old.UpdatedAt) {
		return Item{}, catalog.E(catalog.CodeConflict, "saved item transition time is before its retained update")
	}
	action := "personal.item.created"
	if id == "" {
		id, err = s.ids.New(now)
		if err != nil {
			return Item{}, catalog.Wrap(catalog.CodeInternal, "allocate saved item", err)
		}
	} else {
		action = "personal.item.updated"
	}
	item := Item{Input: in, ID: id, Revision: expected + 1, CreatedAt: old.CreatedAt, UpdatedAt: now}
	if expected == 0 {
		item.CreatedAt = now
	}
	if remove {
		item = old
		item.UpdatedAt = now
		item.Revision++
		action = "personal.item.removed"
	}
	auditID, err := s.ids.New(now)
	if err != nil {
		return Item{}, catalog.Wrap(catalog.CodeInternal, "allocate saved item evidence", err)
	}
	execution := audit.ExecutionFrom(ctx)
	execution.AuthorizationDigest = identity.DecisionDigest(ctx)
	evidence, err := audit.NewRecord(auditID, a.TenantID, audit.Principal{ID: a.MembershipID, Type: audit.PrincipalCatalogMembership, ActorType: string(a.ActorType), RealmID: a.RealmID}, action, "personal_item", id, "", execution, map[string]any{"revision": item.Revision}, now)
	if err != nil {
		return Item{}, catalog.Wrap(catalog.CodeInternal, "construct saved item evidence", err)
	}
	return s.store.Save(ctx, a, item, expected, remove, evidence)
}
