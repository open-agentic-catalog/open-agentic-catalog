package ratings

import (
	"context"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"sort"
	"sync"
)

// MemoryStore commits each opinion, its rollup and audit under one write lock.
type MemoryStore struct {
	mu       sync.Mutex
	records  map[string]Record
	owners   map[string]string
	totals   map[string]Aggregate
	sharing  map[string]Sharing
	evidence EvidenceSink
}

func NewMemoryStore(evidence EvidenceSink) *MemoryStore {
	return &MemoryStore{records: map[string]Record{}, owners: map[string]string{}, totals: map[string]Aggregate{}, sharing: map[string]Sharing{}, evidence: evidence}
}
func memoryDo[T any](ctx context.Context, m *MemoryStore, work func(context.Context) (T, error)) (T, error) {
	var result T
	if identity.NeedsPermissionAdmission(ctx) {
		err := identity.WithCurrentPermissionAdmission(ctx, func(admitted context.Context) error {
			var err error
			result, err = memoryDo(admitted, m, work)
			return err
		})
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return work(ctx)
}
func (m *MemoryStore) Get(ctx context.Context, tenant, id string) (Record, error) {
	return memoryDo(ctx, m, func(ctx context.Context) (Record, error) {
		r, ok := m.records[tenant+"/"+id]
		if !ok {
			return r, catalog.E(catalog.CodeNotFound, "rating was not found")
		}
		return r, nil
	})
}
func (m *MemoryStore) Own(ctx context.Context, tenant string, target Target, owner string) (Record, error) {
	return memoryDo(ctx, m, func(ctx context.Context) (Record, error) {
		id := m.owners[tenant+"/"+Key(target)+"/"+owner]
		r, ok := m.records[tenant+"/"+id]
		if !ok {
			return r, catalog.E(catalog.CodeNotFound, "rating was not found")
		}
		return r, nil
	})
}
func (m *MemoryStore) List(ctx context.Context, tenant string, q ListInput) ([]Record, error) {
	return memoryDo(ctx, m, func(ctx context.Context) ([]Record, error) {
		if q.Limit < 1 || q.Limit > 101 {
			return nil, catalog.E(catalog.CodeInvalidArgument, "rating page is invalid")
		}
		out := []Record{}
		for _, r := range m.records {
			if r.TenantID == tenant && r.ID > q.AfterID && (q.State == "" || q.State == r.State) && (q.PrincipalKey == "" || q.PrincipalKey == r.PrincipalKey) {
				out = append(out, r)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		return out[:min(q.Limit, len(out))], nil
	})
}
func (m *MemoryStore) Save(ctx context.Context, r Record, expected int64, _ Admission, e audit.Record) (Record, error) {
	return memoryDo(ctx, m, func(ctx context.Context) (Record, error) {
		old := m.records[r.TenantID+"/"+r.ID]
		if err := ValidateSave(old, r, expected, e); err != nil {
			return Record{}, err
		}
		owner := r.TenantID + "/" + Key(r.Target) + "/" + r.PrincipalKey
		if id := m.owners[owner]; id != "" && id != r.ID {
			return Record{}, catalog.E(catalog.CodePreconditionFailed, "rating already exists; refresh it")
		}
		totalKey := r.TenantID + "/" + Key(r.Target)
		aggregate := adjust(m.totals[totalKey], old, -1)
		aggregate = adjust(aggregate, r, 1)
		if !aggregate.Valid() {
			return Record{}, catalog.E(catalog.CodeInternal, "rating totals are invalid")
		}
		if m.evidence == nil {
			return Record{}, catalog.E(catalog.CodeInternal, "rating audit is unavailable")
		}
		if err := m.evidence.AppendAuditEvent(ctx, e); err != nil {
			return Record{}, err
		}
		m.records[r.TenantID+"/"+r.ID] = r
		m.owners[owner] = r.ID
		m.totals[totalKey] = aggregate
		return r, nil
	})
}
func adjust(a Aggregate, r Record, sign int64) Aggregate {
	if r.State == "active" {
		a.Count += sign
		a.Sum += sign * int64(r.Score)
		if r.Consent {
			a.SharedCount += sign
			a.SharedSum += sign * int64(r.Score)
		}
	}
	return a
}
func (m *MemoryStore) Aggregates(ctx context.Context, tenant string, targets []Target) (map[string]Aggregate, error) {
	return memoryDo(ctx, m, func(ctx context.Context) (map[string]Aggregate, error) {
		if len(targets) > catalog.MaxPublicationRecords {
			return nil, catalog.E(catalog.CodeWorkLimitExceeded, "rating target batch is too large")
		}
		out := map[string]Aggregate{}
		for _, t := range targets {
			if err := ValidateTarget(t); err != nil {
				return nil, err
			}
			key := Key(t)
			out[key] = m.totals[tenant+"/"+key]
		}
		return out, nil
	})
}
func (m *MemoryStore) GetSharing(ctx context.Context, tenant, host, audience string) (Sharing, error) {
	return memoryDo(ctx, m, func(ctx context.Context) (Sharing, error) {
		r, ok := m.sharing[tenant+"/"+host+"/"+audience]
		if !ok {
			return r, catalog.E(catalog.CodeNotFound, "rating sharing was not found")
		}
		return r, nil
	})
}
func (m *MemoryStore) SaveSharing(ctx context.Context, r Sharing, expected int64, e audit.Record) (Sharing, error) {
	return memoryDo(ctx, m, func(ctx context.Context) (Sharing, error) {
		key := r.TenantID + "/" + r.Host + "/" + r.Audience
		old := m.sharing[key]
		if err := ValidateSharingSave(old, r, expected, e); err != nil {
			return Sharing{}, err
		}
		if m.evidence == nil {
			return Sharing{}, catalog.E(catalog.CodeInternal, "rating audit is unavailable")
		}
		if err := m.evidence.AppendAuditEvent(ctx, e); err != nil {
			return Sharing{}, err
		}
		m.sharing[key] = r
		return r, nil
	})
}
