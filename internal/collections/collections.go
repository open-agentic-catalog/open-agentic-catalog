// Package collections owns namespace-scoped curation of current local Listings.
// Membership grants no authority over its targets or public discovery feeds.
package collections

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

const (
	MaxName                 = 160
	MaxDescription          = 2000
	MaxBodyBytes            = 32 << 10
	MaxMemberBodyBytes      = 1024
	MaxMembers              = 200
	MaxNamespaceCollections = 100
	MaxTenantCollections    = 1000
	MaxMemberText           = 256
	MaxMemberKinds          = 4
)

type Collection struct {
	ID          string    `json:"id"`
	NamespaceID string    `json:"namespaceId"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Revision    int64     `json:"revision"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type Input struct {
	NamespaceID string `json:"namespaceId"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type Patch struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
}

type ListRequest struct {
	NamespaceID, AfterID string
	Limit                int
}
type MemberRequest struct {
	AfterID, Generation string
	Text                string
	Kinds               []catalog.Kind
	Revision            int64
	Limit               int
}
type MemberPage struct {
	Collection Collection
	Generation string
	Items      []catalog.CatalogSummary
}

type Operation string

const (
	Create Operation = "created"
	Update Operation = "updated"
	Delete Operation = "deleted"
	Add    Operation = "member_added"
	Remove Operation = "member_removed"
)

// Store owns coherent policy/data selection and atomic audited mutations.
type Store interface {
	Get(context.Context, identity.Actor, string) (Collection, error)
	List(context.Context, identity.Actor, ListRequest) ([]Collection, error)
	Target(context.Context, identity.Actor, string) (string, error)
	Members(context.Context, identity.Actor, string, MemberRequest) (MemberPage, error)
	Save(context.Context, identity.Actor, Collection, int64, Operation, string, audit.Record) (Collection, error)
}

type Service struct {
	store Store
	clock catalog.Clock
	ids   catalog.IDGenerator
}

func New(store Store, clock catalog.Clock, ids catalog.IDGenerator) (*Service, error) {
	if store == nil || clock == nil || ids == nil {
		return nil, errors.New("collection dependencies are required")
	}
	return &Service{store, clock, ids}, nil
}

func Normalize(name, description string) (string, string, error) {
	name, description = strings.TrimSpace(name), strings.TrimSpace(description)
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > MaxName || strings.IndexFunc(name, func(r rune) bool { return unicode.IsControl(r) || r == '\u2028' || r == '\u2029' }) >= 0 {
		return "", "", catalog.E(catalog.CodeInvalidArgument, "collection name requires 1 to 160 characters on one line")
	}
	if !utf8.ValidString(description) || utf8.RuneCountInString(description) > MaxDescription || strings.IndexFunc(description, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' }) >= 0 {
		return "", "", catalog.E(catalog.CodeInvalidArgument, "collection description permits at most 2000 characters without controls except line breaks and tabs")
	}
	return name, description, nil
}

func Actor(ctx context.Context) (identity.Actor, error) {
	a, ok := identity.FromContext(ctx)
	if !ok || identity.ValidatePermissionActor(a) != nil {
		return a, catalog.E(catalog.CodeForbidden, "a current Catalog membership is required")
	}
	return a, nil
}
func require(ctx context.Context, permission identity.Permission, namespace string) error {
	_, err := identity.RequirePermission(ctx, permission, namespace)
	if err != nil {
		return catalog.PermissionError("collection access is not permitted", err)
	}
	return nil
}
func readContext(ctx context.Context) (context.Context, identity.Actor, error) {
	bound, a, err := identity.WithCollectionContext(ctx)
	if err != nil {
		return bound, a, catalog.PermissionError("current collection authority is required", err)
	}
	return bound, a, nil
}
func (s *Service) Get(ctx context.Context, id string) (Collection, error) {
	id = strings.ToLower(id)
	if !identity.ValidTenantID(id) {
		return Collection{}, catalog.E(catalog.CodeInvalidArgument, "collection ID is invalid")
	}
	ctx, a, err := readContext(ctx)
	if err != nil {
		return Collection{}, err
	}
	item, err := s.store.Get(ctx, a, id)
	if err != nil {
		return Collection{}, err
	}
	if err := require(ctx, "collection.read", item.NamespaceID); err != nil {
		return Collection{}, err
	}
	return item, nil
}
func (s *Service) List(ctx context.Context, q ListRequest) ([]Collection, error) {
	q.NamespaceID, q.AfterID = strings.ToLower(q.NamespaceID), strings.ToLower(q.AfterID)
	if q.Limit < 1 || q.Limit > 100 || q.NamespaceID != "" && !identity.ValidTenantID(q.NamespaceID) || q.AfterID != "" && !identity.ValidTenantID(q.AfterID) {
		return nil, catalog.E(catalog.CodeInvalidArgument, "collection page is invalid")
	}
	ctx, a, err := readContext(ctx)
	if err != nil {
		return nil, err
	}
	q.Limit++
	return s.store.List(ctx, a, q)
}

// Members also supplies the current snapshot binding before cursor admission.
// A zero limit returns no members and only the parent/generation binding.
func (s *Service) Members(ctx context.Context, id string, q MemberRequest) (MemberPage, error) {
	var err error
	q.Text, q.Kinds, err = NormalizeMemberFilters(q.Text, q.Kinds)
	if err != nil {
		return MemberPage{}, err
	}
	id, q.AfterID = strings.ToLower(id), strings.ToLower(q.AfterID)
	if !identity.ValidTenantID(id) || q.Limit < 0 || q.Limit > 100 || q.AfterID != "" && !identity.ValidTenantID(q.AfterID) || q.Revision < 0 {
		return MemberPage{}, catalog.E(catalog.CodeInvalidArgument, "collection member page is invalid")
	}
	ctx, a, err := readContext(ctx)
	if err != nil {
		return MemberPage{}, err
	}
	if q.Limit > 0 {
		q.Limit++
	}
	return s.store.Members(ctx, a, id, q)
}

// NormalizeMemberFilters bounds inputs and gives cursors and both stores one
// canonical selection. Empty/whitespace text and empty kinds mean ordinary browse.
func NormalizeMemberFilters(text string, kinds []catalog.Kind) (string, []catalog.Kind, error) {
	if !utf8.ValidString(text) || utf8.RuneCountInString(text) > MaxMemberText || strings.IndexFunc(text, func(r rune) bool { return unicode.IsControl(r) && !unicode.IsSpace(r) }) >= 0 || len(kinds) > MaxMemberKinds {
		return "", nil, catalog.E(catalog.CodeInvalidArgument, "collection filters permit at most 256 text characters and four kinds")
	}
	for _, kind := range kinds {
		if !kind.Valid() {
			return "", nil, catalog.E(catalog.CodeInvalidArgument, "collection kind must be agent, mcp_server, skill or plugin")
		}
	}
	kinds = slices.Clone(kinds)
	slices.Sort(kinds)
	return strings.ToLower(strings.Join(strings.Fields(text), " ")), slices.Compact(kinds), nil
}

func matchesMember(item catalog.CatalogSummary, q MemberRequest) bool {
	if len(q.Kinds) > 0 && !slices.Contains(q.Kinds, item.Resource.Kind) {
		return false
	}
	return q.Text == "" || catalog.CoreLexicalScore(catalog.SearchDocument{DisplayName: item.ListingRevision.DisplayName, Description: item.ListingRevision.Description}, q.Text) > 0
}
func (s *Service) Create(ctx context.Context, in Input) (Collection, error) {
	in.NamespaceID = strings.ToLower(in.NamespaceID)
	ctx = identity.WithPermissionContext(ctx)
	a, err := Actor(ctx)
	if err != nil {
		return Collection{}, err
	}
	if !identity.ValidTenantID(in.NamespaceID) {
		return Collection{}, catalog.E(catalog.CodeInvalidArgument, "owner namespace ID is invalid")
	}
	if err := require(ctx, "collection.manage", in.NamespaceID); err != nil {
		return Collection{}, err
	}
	if err := require(ctx, "collection.read", in.NamespaceID); err != nil {
		return Collection{}, err
	}
	name, description, err := Normalize(in.Name, in.Description)
	if err != nil {
		return Collection{}, err
	}
	now := s.clock.Now().UTC().Truncate(time.Microsecond)
	id, err := s.ids.New(now)
	if err != nil {
		return Collection{}, catalog.Wrap(catalog.CodeInternal, "allocate collection", err)
	}
	item := Collection{ID: id, NamespaceID: in.NamespaceID, Name: name, Description: description, Revision: 1, CreatedAt: now, UpdatedAt: now}
	return s.save(ctx, a, item, 0, Create, "")
}
func (s *Service) Change(ctx context.Context, id string, expected int64, op Operation, patch Patch, listingID string) (Collection, error) {
	id, listingID = strings.ToLower(id), strings.ToLower(listingID)
	ctx = identity.WithPermissionContext(ctx)
	a, err := Actor(ctx)
	if err != nil {
		return Collection{}, err
	}
	if expected < 1 || expected == 1<<63-1 {
		return Collection{}, catalog.E(catalog.CodeInvalidArgument, "collection revision is invalid")
	}
	old, err := s.Get(ctx, id)
	if err != nil {
		return Collection{}, err
	}
	if err := require(ctx, "collection.manage", old.NamespaceID); err != nil {
		return Collection{}, err
	}
	if old.Revision != expected {
		return Collection{}, catalog.E(catalog.CodePreconditionFailed, "collection changed; refresh before editing")
	}
	switch op {
	case Create:
		return Collection{}, catalog.E(catalog.CodeInvalidArgument, "use collection creation for a new aggregate")
	case Update:
		if patch.Name == nil && patch.Description == nil || listingID != "" {
			return Collection{}, catalog.E(catalog.CodeInvalidArgument, "select collection metadata to update")
		}
		if patch.Name != nil {
			old.Name = *patch.Name
		}
		if patch.Description != nil {
			old.Description = *patch.Description
		}
		old.Name, old.Description, err = Normalize(old.Name, old.Description)
		if err != nil {
			return Collection{}, err
		}
	case Add, Remove:
		if !identity.ValidTenantID(listingID) || patch.Name != nil || patch.Description != nil {
			return Collection{}, catalog.E(catalog.CodeInvalidArgument, "member command requires one local Listing ID")
		}
		if op == Add {
			namespace, lookupErr := s.store.Target(ctx, a, listingID)
			if lookupErr != nil {
				if catalog.ErrorCodeOf(lookupErr) == catalog.CodeNotFound {
					return Collection{}, catalog.E(catalog.CodeNotFound, "eligible local Listing was not found")
				}
				return Collection{}, lookupErr
			}
			if err := require(ctx, "resource.read", namespace); err != nil {
				if catalog.ErrorCodeOf(err) == catalog.CodeForbidden {
					return Collection{}, catalog.E(catalog.CodeNotFound, "eligible local Listing was not found")
				}
				return Collection{}, err
			}
		}
	case Delete:
		if listingID != "" || patch.Name != nil || patch.Description != nil {
			return Collection{}, catalog.E(catalog.CodeInvalidArgument, "collection deletion does not accept metadata")
		}
	default:
		return Collection{}, catalog.E(catalog.CodeInvalidArgument, "collection command is invalid")
	}
	old.Revision++
	old.UpdatedAt = s.clock.Now().UTC().Truncate(time.Microsecond)
	return s.save(ctx, a, old, expected, op, listingID)
}
func (s *Service) save(ctx context.Context, a identity.Actor, item Collection, expected int64, op Operation, listingID string) (Collection, error) {
	evidenceID, err := s.ids.New(item.UpdatedAt)
	if err != nil {
		return Collection{}, catalog.Wrap(catalog.CodeInternal, "allocate collection evidence", err)
	}
	execution := audit.ExecutionFrom(ctx)
	execution.AuthorizationDigest = identity.DecisionDigest(ctx)
	facts := map[string]any{"revision": item.Revision, "namespaceId": item.NamespaceID}
	if listingID != "" {
		facts["listingId"] = listingID
	}
	evidence, err := audit.NewRecord(evidenceID, a.TenantID, audit.Principal{ID: a.MembershipID, Type: audit.PrincipalCatalogMembership, ActorType: string(a.ActorType), RealmID: a.RealmID}, "collection."+string(op), "collection", item.ID, "", execution, facts, item.UpdatedAt)
	if err != nil {
		return Collection{}, catalog.Wrap(catalog.CodeInternal, "construct collection evidence", err)
	}
	return s.store.Save(ctx, a, item, expected, op, listingID, evidence)
}

// ValidateSave binds a repository write to its aggregate and exact audit record.
func ValidateSave(a identity.Actor, old, next Collection, expected int64, op Operation, listingID string, evidence audit.Record) error {
	if old.Revision != expected {
		return catalog.E(catalog.CodePreconditionFailed, "collection changed; refresh before editing")
	}
	if expected < 0 || next.Revision < 1 || next.Revision != expected+1 || !identity.ValidTenantID(next.ID) || !identity.ValidTenantID(next.NamespaceID) || next.CreatedAt.IsZero() || next.UpdatedAt.Before(next.CreatedAt) || expected > 0 && (next.ID != old.ID || next.NamespaceID != old.NamespaceID || !next.CreatedAt.Equal(old.CreatedAt) || next.UpdatedAt.Before(old.UpdatedAt)) {
		return catalog.E(catalog.CodeConflict, "collection transition is invalid")
	}
	name, description, err := Normalize(next.Name, next.Description)
	if err != nil {
		return err
	}
	if name != next.Name || description != next.Description {
		return catalog.E(catalog.CodeInvalidArgument, "collection text is not normalized")
	}
	if expected == 0 && op != Create || expected > 0 && op != Update && op != Delete && op != Add && op != Remove || (op == Add || op == Remove) != (listingID != "") {
		return catalog.E(catalog.CodeInvalidArgument, "collection operation is invalid")
	}
	if op != Create && op != Update && (old.Name != next.Name || old.Description != next.Description) {
		return catalog.E(catalog.CodeConflict, "membership cannot change collection metadata")
	}
	if err := audit.ValidateNewRecord(evidence); err != nil {
		return catalog.Wrap(catalog.CodeInternal, "invalid collection evidence", err)
	}
	facts, ok := evidence.Details["facts"].(map[string]any)
	expectedFacts := map[string]any{"revision": next.Revision, "namespaceId": next.NamespaceID}
	if listingID != "" {
		expectedFacts["listingId"] = listingID
	}
	actualJSON, actualErr := json.Marshal(facts)
	wantedJSON, wantedErr := json.Marshal(expectedFacts)
	if !ok || actualErr != nil || wantedErr != nil || string(actualJSON) != string(wantedJSON) || evidence.TenantID != a.TenantID || evidence.Actor != a.MembershipID || evidence.TargetType != "collection" || evidence.TargetID != next.ID || evidence.Action != "collection."+string(op) || !evidence.At.Equal(next.UpdatedAt) || evidence.Details["realmId"] != a.RealmID || evidence.Details["actorType"] != string(a.ActorType) || evidence.Details["principalType"] != "catalog_membership" {
		return catalog.E(catalog.CodeInternal, "collection evidence does not match command")
	}
	return nil
}

func CheckSnapshot(item Collection, generation string, q MemberRequest) error {
	if q.Revision != 0 && item.Revision != q.Revision || q.Generation != "" && generation != q.Generation {
		return catalog.E(catalog.CodeInvalidArgument, "collection contents changed; restart the read")
	}
	return nil
}
