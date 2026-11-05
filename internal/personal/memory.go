package personal

import (
	"context"
	"encoding/json"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"slices"
	"sort"
	"strconv"
	"sync"
)

type MemoryStore struct {
	mu     sync.Mutex
	owners map[string]map[string]Item
	sink   AuditSink
}

func NewMemoryStore(sink AuditSink) *MemoryStore {
	return &MemoryStore{owners: map[string]map[string]Item{}, sink: sink}
}
func ownerKey(a identity.Actor) string { return a.TenantID + "/" + a.RealmID + "/" + a.MembershipID }

// Detach all reference and criteria fields before returning or retaining them.
func detached(i Item) Item {
	if i.Reference != nil {
		r := *i.Reference
		if r.VersionPresent != nil {
			v := *r.VersionPresent
			r.VersionPresent = &v
		}
		i.Reference = &r
	}
	if i.Search != nil {
		q := *i.Search
		q.Origins = slices.Clone(q.Origins)
		q.Kinds = slices.Clone(q.Kinds)
		q.Tags = slices.Clone(q.Tags)
		q.Categories = slices.Clone(q.Categories)
		i.Search = &q
	}
	return i
}

// Stores never accept a caller-selected owner, including an administrator.
func BoundOwner(ctx context.Context, a identity.Actor) error {
	current, ok := identity.FromContext(ctx)
	if !ok || current.ActorType != identity.ActorHuman || current.TenantID != a.TenantID || current.MembershipID != a.MembershipID || current.RealmID != a.RealmID {
		return catalog.E(catalog.CodeForbidden, "saved item owner does not match the current human")
	}
	return nil
}
func (m *MemoryStore) Get(ctx context.Context, a identity.Actor, id string) (Item, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var out Item
		err := identity.WithCurrentPermissionAdmission(ctx, func(c context.Context) error { var err error; out, err = m.Get(c, a, id); return err })
		return out, err
	}
	if err := BoundOwner(ctx, a); err != nil {
		return Item{}, err
	}
	if err := ctx.Err(); err != nil {
		return Item{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	i, ok := m.owners[ownerKey(a)][id]
	if !ok {
		return Item{}, catalog.E(catalog.CodeNotFound, "saved item was not found")
	}
	return detached(i), nil
}
func (m *MemoryStore) List(ctx context.Context, a identity.Actor, q ListRequest) ([]Item, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var out []Item
		err := identity.WithCurrentPermissionAdmission(ctx, func(c context.Context) error { var err error; out, err = m.List(c, a, q); return err })
		return out, err
	}
	if err := BoundOwner(ctx, a); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if q.Limit < 1 || q.Limit > 101 {
		return nil, catalog.E(catalog.CodeInvalidArgument, "saved page is invalid")
	}
	if q.Label != nil {
		if err := ValidateLabel(*q.Label); err != nil {
			return nil, err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	items := []Item{}
	for _, i := range m.owners[ownerKey(a)] {
		if i.ID > q.AfterID && (q.Type == "" || q.Type == i.Type) && (q.Label == nil || i.Label == *q.Label) {
			items = append(items, detached(i))
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items[:min(q.Limit, len(items))], nil
}
func ValidateSave(old, item Item, expected int64, remove bool, e audit.Record, a identity.Actor) error {
	if old.Revision != expected {
		return catalog.E(catalog.CodePreconditionFailed, "saved item revision changed")
	}
	if item.Revision != expected+1 || !identity.ValidTenantID(item.ID) || item.CreatedAt.IsZero() || item.UpdatedAt.Before(item.CreatedAt) || expected > 0 && (item.ID != old.ID || !item.CreatedAt.Equal(old.CreatedAt) || item.UpdatedAt.Before(old.UpdatedAt)) {
		return catalog.E(catalog.CodeConflict, "saved item transition is invalid")
	}
	action := "personal.item.created"
	if expected > 0 {
		action = "personal.item.updated"
	}
	if remove {
		action = "personal.item.removed"
		if expected == 0 {
			return catalog.E(catalog.CodeInvalidArgument, "saved item removal is invalid")
		}
	}
	if err := Validate(item.Input); err != nil {
		return err
	}
	if err := audit.ValidateNewRecord(e); err != nil {
		return catalog.Wrap(catalog.CodeInternal, "saved item audit is invalid", err)
	}
	facts, ok := e.Details["facts"].(map[string]any)
	expectedFact, marshalErr := json.Marshal(facts["revision"])
	if !ok || marshalErr != nil || string(expectedFact) != strconv.FormatInt(item.Revision, 10) || !e.At.Equal(item.UpdatedAt) || e.Details["actorType"] != "human" || e.Details["principalType"] != "catalog_membership" {
		return catalog.E(catalog.CodeInternal, "saved item audit facts are invalid")
	}
	if e.Actor != a.MembershipID || e.TenantID != a.TenantID || e.TargetID != item.ID || e.TargetType != "personal_item" || e.Action != action || e.Details["realmId"] != a.RealmID {
		return catalog.E(catalog.CodeInternal, "saved item audit binding is invalid")
	}
	return nil
}
func (m *MemoryStore) Save(ctx context.Context, a identity.Actor, item Item, expected int64, remove bool, e audit.Record) (Item, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var out Item
		err := identity.WithCurrentPermissionAdmission(ctx, func(c context.Context) error {
			var err error
			out, err = m.Save(c, a, item, expected, remove, e)
			return err
		})
		return out, err
	}
	if err := BoundOwner(ctx, a); err != nil {
		return Item{}, err
	}
	if err := ctx.Err(); err != nil {
		return Item{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := ownerKey(a)
	items := m.owners[key]
	if items == nil {
		items = map[string]Item{}
	}
	old := items[item.ID]
	if err := ValidateSave(old, item, expected, remove, e, a); err != nil {
		return Item{}, err
	}
	if expected == 0 && len(items) >= MaxItems {
		return Item{}, catalog.E(catalog.CodeConflict, "saved library contains 200 items; remove one first")
	}
	if err := m.sink.AppendAuditEvent(ctx, e); err != nil {
		return Item{}, err
	}
	if remove {
		delete(items, item.ID)
	} else {
		items[item.ID] = detached(item)
	}
	m.owners[key] = items
	return detached(item), nil
}
