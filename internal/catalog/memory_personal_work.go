package catalog

import (
	"context"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"sort"
)

type workOwner struct{ Tenant, Member, Realm string }

func workAttributionKey(kind, id, revision string) string { return kind + ":" + id + ":" + revision }

// indexWorkAudit records only exact retained transition attribution while locked.
func (m *MemoryRepository) indexWorkAudit(e AuditEvent) {
	switch e.Action {
	case "resource.created", "resource_version.created", "resource_version.revised", "catalog_listing.revised", "review_submission.submitted":
	default:
		return
	}
	if e.FormatVersion == nil || *e.FormatVersion != audit.RecordFormatV1 {
		return
	}
	if e.Details["principalType"] != "catalog_membership" || e.Details["actorType"] != "human" {
		return
	}
	realm, _ := e.Details["realmId"].(string)
	if !identity.ValidTenantID(realm) || !identity.ValidTenantID(e.Actor) {
		return
	}
	revision := ""
	if facts, ok := e.Details["facts"].(map[string]any); ok {
		revision, _ = facts["revisionId"].(string)
	}
	if (e.TargetType == "resource_version" || e.TargetType == "catalog_listing") && revision == "" {
		return
	}
	if m.workOwners == nil {
		m.workOwners = map[string]workOwner{}
	}
	m.workOwners[workAttributionKey(e.TargetType, e.TargetID, revision)] = workOwner{e.TenantID, e.Actor, realm}
}
func (m *MemoryRepository) ListPersonalWork(ctx context.Context, tenant string, q PersonalWorkRequest) ([]PersonalWorkItem, error) {
	if identity.NeedsCollectionAdmission(ctx) {
		return identity.WithCollectionRead(ctx, func(c context.Context) ([]PersonalWorkItem, error) { return m.ListPersonalWork(c, tenant, q) })
	}
	if err := identity.RequireCollectionTenant(ctx, tenant); err != nil {
		return nil, err
	}
	a, _ := identity.FromContext(ctx)
	owner := workOwner{tenant, a.MembershipID, a.RealmID}
	m.mu.RLock()
	defer m.mu.RUnlock()
	owns := func(kind, id, revision string) bool {
		return m.workOwners[workAttributionKey(kind, id, revision)] == owner
	}
	rows := []PersonalWorkItem{}
	add := func(item PersonalWorkItem, r Resource, revision ListingRevision) error {
		if r.TenantID != tenant || r.DeletedAt != nil || item.ID <= q.AfterID {
			return nil
		}
		history, err := identity.CollectionAllows(ctx, "resource.read_history", r.NamespaceID)
		if err != nil {
			return err
		}
		meta, err := identity.CollectionAllows(ctx, "resource.read", r.NamespaceID)
		if err != nil {
			return err
		}
		content, err := identity.CollectionAllows(ctx, "resource.read_content", r.NamespaceID)
		if err != nil {
			return err
		}
		if item.Type == "resource" && !meta || item.Type != "resource" && !history {
			return nil
		}
		item.ResourceID, item.NamespaceID, item.Kind = r.ID, r.NamespaceID, r.Kind
		if meta {
			item.CanonicalIdentifier = r.CanonicalIdentifier
		}
		if content {
			item.DisplayName = revision.DisplayName
		}
		rows = append(rows, item)
		return nil
	}
	if q.Queue == "drafts" {
		for _, r := range m.resources {
			if !owns("resource", r.ID, "") {
				continue
			}
			hasVersion := false
			for _, v := range m.versions {
				if v.TenantID == tenant && v.ResourceID == r.ID {
					hasVersion = true
					break
				}
			}
			if !hasVersion {
				if err := add(PersonalWorkItem{ID: "resource:" + r.ID, Type: "resource", State: "empty_resource"}, r, ListingRevision{}); err != nil {
					return nil, err
				}
			}
		}
		for _, v := range m.versions {
			if v.TenantID != tenant || v.CandidateRevisionID == "" || v.SealedRevisionID != "" || !owns("resource_version", v.ID, v.CandidateRevisionID) {
				continue
			}
			if err := add(PersonalWorkItem{ID: "version:" + v.ID, Type: "version", State: "unsealed_version", VersionID: v.ID, VersionRevisionID: v.CandidateRevisionID}, m.resources[v.ResourceID], ListingRevision{}); err != nil {
				return nil, err
			}
		}
		for _, l := range m.listings {
			if l.TenantID != tenant || l.CandidateRevisionID == "" || !owns("catalog_listing", l.ID, l.CandidateRevisionID) {
				continue
			}
			submitted := false
			for _, s := range m.submissions {
				if s.TenantID == tenant && s.ListingID == l.ID && s.ListingRevisionID == l.CandidateRevisionID {
					submitted = true
					break
				}
			}
			if submitted {
				continue
			}
			rev := m.listingRevisions[l.CandidateRevisionID]
			if err := add(PersonalWorkItem{ID: "candidate:" + l.ID, Type: "candidate", State: "unsubmitted_candidate", ListingID: l.ID, ListingRevisionID: rev.ID, VersionRevisionID: rev.VersionRevisionID, CurrentCandidateRevisionID: rev.ID}, m.resources[l.ResourceID], rev); err != nil {
				return nil, err
			}
		}
	} else {
		for _, s := range m.submissions {
			if s.TenantID != tenant || string(s.State) != q.Queue || !owns("review_submission", s.ID, "") {
				continue
			}
			excluded := false
			if q.Queue == "approved" {
				excluded = m.submissionReleasedLocked(tenant, s.ID)
			}
			if q.Queue == "changes_required" {
				for _, n := range m.submissions {
					if n.TenantID == tenant && n.ListingID == s.ListingID && (n.SubmittedAt.After(s.SubmittedAt) || n.SubmittedAt.Equal(s.SubmittedAt) && n.ID > s.ID) {
						excluded = true
						break
					}
				}
			}
			if excluded {
				continue
			}
			l := m.listings[s.ListingID]
			rev := m.listingRevisions[s.ListingRevisionID]
			if err := add(PersonalWorkItem{ID: "submission:" + s.ID, Type: "submission", State: string(s.State), ListingID: l.ID, ListingRevisionID: s.ListingRevisionID, VersionRevisionID: s.VersionRevisionID, SubmissionID: s.ID, CurrentCandidateRevisionID: l.CandidateRevisionID}, m.resources[l.ResourceID], rev); err != nil {
				return nil, err
			}
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	if len(rows) > q.Limit {
		rows = rows[:q.Limit]
	}
	return rows, nil
}
