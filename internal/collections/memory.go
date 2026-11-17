package collections

import (
	"context"
	"slices"
	"sort"
	"sync"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// LocalCatalog is implemented by the composed in-memory Catalog repository.
// PostgreSQL performs equivalent selection within its own tenant transaction.
type LocalCatalog interface {
	CollectionEntries(context.Context, string, []string) ([]catalog.CatalogSummary, string, error)
	GetNamespace(context.Context, string, string) (catalog.Namespace, error)
	AppendAuditEvent(context.Context, audit.Record) error
}
type MemoryStore struct {
	mu      sync.Mutex
	source  LocalCatalog
	items   map[string]map[string]Collection
	members map[string]map[string]map[string]bool
}

func NewMemoryStore(source LocalCatalog) *MemoryStore {
	return &MemoryStore{source: source, items: map[string]map[string]Collection{}, members: map[string]map[string]map[string]bool{}}
}
func boundActor(ctx context.Context, a identity.Actor) error {
	current, err := Actor(ctx)
	if err != nil {
		return err
	}
	if current.TenantID != a.TenantID || current.CommandPrincipal() != a.CommandPrincipal() {
		return catalog.E(catalog.CodeForbidden, "collection principal does not match")
	}
	return ctx.Err()
}
func (m *MemoryStore) get(ctx context.Context, a identity.Actor, id string) (Collection, error) {
	item, ok := m.items[a.TenantID][id]
	if !ok {
		return Collection{}, catalog.E(catalog.CodeNotFound, "collection was not found")
	}
	allowed, err := identity.CollectionAllows(ctx, "collection.read", item.NamespaceID)
	if err != nil {
		return Collection{}, err
	}
	if !allowed {
		return Collection{}, catalog.E(catalog.CodeNotFound, "collection was not found")
	}
	return item, nil
}
func (m *MemoryStore) Get(ctx context.Context, a identity.Actor, id string) (Collection, error) {
	if identity.NeedsReadAdmission(ctx) {
		return identity.WithReadAdmission(ctx, func(bound context.Context) (Collection, error) { return m.Get(bound, a, id) })
	}
	if err := boundActor(ctx, a); err != nil {
		return Collection{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.get(ctx, a, id)
}
func (m *MemoryStore) List(ctx context.Context, a identity.Actor, q ListRequest) ([]Collection, error) {
	if identity.NeedsReadAdmission(ctx) {
		return identity.WithReadAdmission(ctx, func(bound context.Context) ([]Collection, error) { return m.List(bound, a, q) })
	}
	if err := boundActor(ctx, a); err != nil {
		return nil, err
	}
	if q.Limit < 1 || q.Limit > 101 {
		return nil, catalog.E(catalog.CodeInvalidArgument, "collection page is invalid")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	items := []Collection{}
	for _, item := range m.items[a.TenantID] {
		if item.ID <= q.AfterID || q.NamespaceID != "" && item.NamespaceID != q.NamespaceID {
			continue
		}
		allowed, err := identity.CollectionAllows(ctx, "collection.read", item.NamespaceID)
		if err != nil {
			return nil, err
		}
		if allowed {
			items = append(items, item)
		}
	}
	slices.SortFunc(items, func(a, b Collection) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return items[:min(q.Limit, len(items))], nil
}
func (m *MemoryStore) Target(ctx context.Context, a identity.Actor, id string) (string, error) {
	if err := boundActor(ctx, a); err != nil {
		return "", err
	}
	items, _, err := m.source.CollectionEntries(ctx, a.TenantID, []string{id})
	if err != nil {
		return "", err
	}
	if len(items) != 1 {
		return "", catalog.E(catalog.CodeNotFound, "eligible local Listing was not found")
	}
	return items[0].Listing.NamespaceID, nil
}
func (m *MemoryStore) Members(ctx context.Context, a identity.Actor, id string, q MemberRequest) (MemberPage, error) {
	if identity.NeedsReadAdmission(ctx) {
		return identity.WithReadAdmission(ctx, func(bound context.Context) (MemberPage, error) { return m.Members(bound, a, id, q) })
	}
	if err := boundActor(ctx, a); err != nil {
		return MemberPage{}, err
	}
	if q.Limit < 0 || q.Limit > 101 {
		return MemberPage{}, catalog.E(catalog.CodeInvalidArgument, "collection member page is invalid")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	item, err := m.get(ctx, a, id)
	if err != nil {
		return MemberPage{}, err
	}
	ids := make([]string, 0, len(m.members[a.TenantID][id]))
	for target := range m.members[a.TenantID][id] {
		ids = append(ids, target)
	}
	records, generation, err := m.source.CollectionEntries(ctx, a.TenantID, ids)
	if err != nil {
		return MemberPage{}, err
	}
	if err := CheckSnapshot(item, generation, q); err != nil {
		return MemberPage{}, err
	}
	out := MemberPage{Collection: item, Generation: generation, Items: []catalog.CatalogSummary{}}
	sort.Slice(records, func(i, j int) bool { return records[i].Listing.ID < records[j].Listing.ID })
	for _, r := range records {
		if r.Listing.ID > q.AfterID && len(out.Items) < q.Limit {
			out.Items = append(out.Items, r)
		}
	}
	return out, nil
}
func (m *MemoryStore) Save(ctx context.Context, a identity.Actor, next Collection, expected int64, op Operation, listingID string, e audit.Record) (Collection, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var out Collection
		err := identity.WithCurrentPermissionAdmission(ctx, func(bound context.Context) error {
			var err error
			out, err = m.Save(bound, a, next, expected, op, listingID, e)
			return err
		})
		return out, err
	}
	if err := boundActor(ctx, a); err != nil {
		return Collection{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	old := m.items[a.TenantID][next.ID]
	if err := ValidateSave(a, old, next, expected, op, listingID, e); err != nil {
		return Collection{}, err
	}
	if op == Create {
		if _, err := m.source.GetNamespace(ctx, a.TenantID, next.NamespaceID); err != nil {
			return Collection{}, err
		}
		count := 0
		for _, item := range m.items[a.TenantID] {
			if item.NamespaceID == next.NamespaceID {
				count++
			}
		}
		if count >= MaxNamespaceCollections || len(m.items[a.TenantID]) >= MaxTenantCollections {
			return Collection{}, catalog.E(catalog.CodeConflict, "collection capacity is reached; remove a collection or contact an administrator")
		}
	}
	members := m.members[a.TenantID][next.ID]
	if op == Add {
		namespace, err := m.Target(ctx, a, listingID)
		if err != nil {
			return Collection{}, err
		}
		if err := require(ctx, "resource.read", namespace); err != nil {
			return Collection{}, err
		}
		if members[listingID] {
			return Collection{}, catalog.E(catalog.CodeConflict, "Listing is already a member")
		}
		if len(members) >= MaxMembers {
			return Collection{}, catalog.E(catalog.CodeConflict, "collection capacity is reached; remove a member first")
		}
	}
	if op == Remove && !members[listingID] {
		return Collection{}, catalog.E(catalog.CodeNotFound, "collection member was not found")
	}
	if err := m.source.AppendAuditEvent(ctx, e); err != nil {
		return Collection{}, err
	}
	if m.items[a.TenantID] == nil {
		m.items[a.TenantID] = map[string]Collection{}
		m.members[a.TenantID] = map[string]map[string]bool{}
	}
	if op == Delete {
		delete(m.items[a.TenantID], next.ID)
		delete(m.members[a.TenantID], next.ID)
		return next, nil
	}
	m.items[a.TenantID][next.ID] = next
	if members == nil {
		members = map[string]bool{}
		m.members[a.TenantID][next.ID] = members
	}
	if op == Add {
		members[listingID] = true
	}
	if op == Remove {
		delete(members, listingID)
	}
	return next, nil
}
