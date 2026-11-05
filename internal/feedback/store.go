// Memory persistence preserves the same revision and evidence boundary as SQL.
package feedback

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"sync"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// MemoryStore is service-free persistence with atomic state/evidence updates.
type MemoryStore struct {
	mu       sync.Mutex
	records  map[string]Record
	evidence EvidenceSink
	reader   ContextReader
}

// NewMemoryStore requires an audit sink for every committed mutation.
func NewMemoryStore(evidence EvidenceSink, reader ContextReader) *MemoryStore {
	return &MemoryStore{records: map[string]Record{}, evidence: evidence, reader: reader}
}

// Get never crosses tenant boundaries or discloses another tenant's existence.
func (m *MemoryStore) Get(ctx context.Context, tenant, id string) (Record, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 Record
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, operationErr = m.Get(ctx, tenant, id)
			return operationErr
		})
		return admitted0, err
	}

	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[tenant+"/"+id]
	if !ok {
		return Record{}, catalog.E(catalog.CodeNotFound, "feedback was not found")
	}
	return r, nil
}

// List filters before selecting a stable bounded page; SQL implements indexed selection.
func (m *MemoryStore) List(ctx context.Context, tenant string, q ListInput) ([]Record, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 []Record
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, operationErr = m.List(ctx, tenant, q)
			return operationErr
		})
		return admitted0, err
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if q.Limit < 1 || q.Limit > 101 {
		return nil, catalog.E(catalog.CodeInvalidArgument, "feedback page is invalid")
	}
	items := []Record{}
	for _, r := range m.records {
		if r.TenantID == tenant && r.ID > q.AfterID && (q.MembershipID == "" || r.MembershipID == q.MembershipID) && (q.ResourceID == "" || r.ResourceID == q.ResourceID) && (q.Category == "" || r.Category == q.Category) && (q.State == "" || r.State == q.State) {
			if m.reader == nil {
				return nil, catalog.E(catalog.CodeInternal, "feedback context reader is unavailable")
			}
			if _, err := m.reader.GetResource(ctx, tenant, r.ResourceID); err != nil {
				if catalog.ErrorCodeOf(err) == catalog.CodeNotFound {
					continue
				}
				return nil, err
			}
			items = append(items, r)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items[:min(len(items), q.Limit)], nil
}

// Save validates exact state/evidence before committing either in-memory effect.
func (m *MemoryStore) Save(ctx context.Context, r Record, expected int64, evidence audit.Record) (Record, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 Record
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, operationErr = m.Save(ctx, r, expected, evidence)
			return operationErr
		})
		return admitted0, err
	}

	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	current := m.records[r.TenantID+"/"+r.ID]
	if err := ValidateSave(current, r, expected, evidence); err != nil {
		return Record{}, err
	}
	if m.evidence == nil {
		return Record{}, catalog.E(catalog.CodeInternal, "feedback audit sink is unavailable")
	}
	if err := m.evidence.AppendAuditEvent(ctx, evidence); err != nil {
		return Record{}, err
	}
	m.records[r.TenantID+"/"+r.ID] = r
	return r, nil
}

// ValidateSave binds immutable context, legal transitions and safe audit facts.
// Repositories call it under their write lock, not just in the service.
func ValidateSave(old, r Record, expected int64, e audit.Record) error {
	if old.Revision != expected {
		return catalog.E(catalog.CodePreconditionFailed, "feedback revision does not match")
	}
	if !identity.ValidTenantID(r.ID) || !identity.ValidTenantID(r.TenantID) || !identity.ValidTenantID(r.MembershipID) || !identity.ValidTenantID(r.ResourceID) || !ValidCategory(r.Category) || !ValidState(r.State) || r.Revision != expected+1 || r.CreatedAt.IsZero() || r.UpdatedAt.Before(r.CreatedAt) || !validText(r.Response, false) {
		return catalog.E(catalog.CodeInvalidArgument, "feedback record is invalid")
	}
	if r.ReleaseID != "" && (!identity.ValidTenantID(r.ReleaseID) || r.VersionRevisionID == "") || r.VersionRevisionID != "" && !identity.ValidTenantID(r.VersionRevisionID) {
		return catalog.E(catalog.CodeInvalidArgument, "feedback context is invalid")
	}
	terminal := r.State == "withdrawn" || r.State == "removed"
	if terminal && (r.Comment != "" || r.Response != "") || !terminal && !validText(r.Comment, true) || r.State == "open" && r.Response != "" {
		return catalog.E(catalog.CodeInvalidArgument, "feedback text is invalid")
	}
	if expected == 0 {
		if r.State != "open" || e.Action != "feedback.created" || !r.CreatedAt.Equal(r.UpdatedAt) {
			return catalog.E(catalog.CodeConflict, "invalid feedback creation")
		}
	} else {
		if old.State == "withdrawn" || old.State == "removed" || old.ID != r.ID || old.TenantID != r.TenantID || old.MembershipID != r.MembershipID || old.ResourceID != r.ResourceID || old.ReleaseID != r.ReleaseID || old.VersionRevisionID != r.VersionRevisionID || !old.CreatedAt.Equal(r.CreatedAt) || r.UpdatedAt.Before(old.UpdatedAt) {
			return catalog.E(catalog.CodeConflict, "feedback context or terminal state cannot change")
		}
		switch e.Action {
		case "feedback.edit":
			if r.State != "open" {
				return catalog.E(catalog.CodeConflict, "feedback edit must reopen")
			}
		case "feedback.address", "feedback.reopen":
			state := "addressed"
			if e.Action == "feedback.reopen" {
				state = "open"
			}
			if r.State != state || r.Comment != old.Comment || r.Category != old.Category {
				return catalog.E(catalog.CodeConflict, "feedback response cannot rewrite the comment")
			}
		case "feedback.withdraw", "feedback.remove":
			state := "withdrawn"
			if e.Action == "feedback.remove" {
				state = "removed"
			}
			if r.State != state || r.Category != old.Category {
				return catalog.E(catalog.CodeConflict, "invalid feedback removal")
			}
		default:
			return catalog.E(catalog.CodeConflict, "invalid feedback transition")
		}
	}
	if err := audit.ValidateNewRecord(e); err != nil {
		return catalog.Wrap(catalog.CodeInternal, "invalid feedback audit", err)
	}
	facts, ok := e.Details["facts"].(map[string]any)
	actual, err := json.Marshal(facts)
	if err != nil {
		return catalog.Wrap(catalog.CodeInternal, "encode feedback audit facts", err)
	}
	want, err := json.Marshal(map[string]any{"revision": r.Revision, "state": r.State})
	if err != nil {
		return catalog.Wrap(catalog.CodeInternal, "encode expected feedback facts", err)
	}
	if !ok || e.TenantID != r.TenantID || e.TargetID != r.ID || e.TargetType != "feedback" || !e.At.Equal(r.UpdatedAt) || !bytes.Equal(actual, want) {
		return catalog.E(catalog.CodeInternal, "feedback audit does not match state")
	}
	return nil
}
