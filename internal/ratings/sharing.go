package ratings

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"net/url"
	"time"
)

// Summary contains aggregate consented opinions only, never an individual ID.
type Summary struct {
	Reporter       string    `json:"reporter"`
	Identifier     string    `json:"identifier"`
	VersionPresent bool      `json:"versionPresent"`
	Version        string    `json:"version,omitempty"`
	Minimum        int       `json:"minimum"`
	Maximum        int       `json:"maximum"`
	Count          int64     `json:"count"`
	Sum            int64     `json:"sum"`
	MeasuredAt     time.Time `json:"measuredAt"`
}

func (s Summary) Average() float64 { return (Aggregate{Count: s.Count, Sum: s.Sum}).Average() }

// DecodeSummary omits malformed optional summaries without rejecting the entry.
// The enclosing admitted publisher and advertised version bind each claim.
func DecodeSummary(raw []byte, reporter, identifier string, present bool, version string) *Summary {
	if len(raw) == 0 || len(raw) > 2048 || jsondoc.Validate(raw) != nil {
		return nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil
	}
	for _, name := range []string{"reporter", "identifier", "versionPresent", "minimum", "maximum", "count", "sum", "measuredAt"} {
		if len(fields[name]) == 0 {
			return nil
		}
	}
	for _, value := range fields {
		if bytes.Equal(value, []byte("null")) {
			return nil
		}
	}
	var s Summary
	if jsondoc.Decode(raw, &s) != nil {
		return nil
	}
	u, err := url.Parse(s.Reporter)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || s.Reporter != reporter || s.Identifier != identifier || s.VersionPresent != present || s.Version != version || s.Minimum != 1 || s.Maximum != 5 || s.Count < 1 || !validTotals(s.Count, s.Sum) || s.MeasuredAt.IsZero() || s.MeasuredAt.Location() != time.UTC {
		return nil
	}
	return &s
}
func (s *Service) Sharing(ctx context.Context, host, audience string) (Sharing, error) {
	ctx = identity.WithPermissionContext(ctx)
	a, err := identity.RequirePermission(ctx, "ratings.sharing.manage", "")
	if err != nil {
		return Sharing{}, catalog.PermissionError("rating sharing management is not permitted", err)
	}
	if !validSharingScope(host, audience) {
		return Sharing{}, catalog.E(catalog.CodeInvalidArgument, "rating sharing scope is invalid")
	}
	r, err := s.store.GetSharing(ctx, a.TenantID, host, audience)
	if catalog.ErrorCodeOf(err) == catalog.CodeNotFound {
		return Sharing{TenantID: a.TenantID, Host: host, Audience: audience}, nil
	}
	return r, err
}
func (s *Service) SetSharing(ctx context.Context, host, audience string, enabled bool, expected int64, reason string) (Sharing, error) {
	ctx = identity.WithPermissionContext(ctx)
	old, err := s.Sharing(ctx, host, audience)
	if err != nil {
		return Sharing{}, err
	}
	if expected < 0 || expected != old.Revision {
		return Sharing{}, catalog.E(catalog.CodePreconditionFailed, "rating sharing revision does not match")
	}
	now := catalog.TransitionTime(s.clock.Now(), old.UpdatedAt)
	r := old
	r.Enabled = enabled
	r.Revision++
	r.UpdatedAt = now
	if r.ID == "" {
		r.ID, err = s.ids.New(now)
		if err != nil {
			return Sharing{}, err
		}
	}
	a, _ := identity.FromContext(ctx)
	id, err := s.ids.New(now)
	if err != nil {
		return Sharing{}, err
	}
	execution := audit.ExecutionFrom(ctx)
	execution.AuthorizationDigest = identity.DecisionDigest(ctx)
	evidence, err := audit.NewRecord(id, a.TenantID, audit.Principal{ID: a.MembershipID, Type: audit.PrincipalCatalogMembership, ActorType: string(a.ActorType), RealmID: a.RealmID}, "rating.sharing", "rating_sharing", r.ID, reason, execution, map[string]any{"revision": r.Revision, "enabled": r.Enabled}, now)
	if err != nil {
		return Sharing{}, catalog.Wrap(catalog.CodeInvalidArgument, "rating sharing reason or audit is invalid", err)
	}
	return s.store.SaveSharing(ctx, r, expected, evidence)
}
func validSharingScope(host, audience string) bool {
	u, err := url.Parse("https://" + host)
	return err == nil && u.Host == host && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == "" && len(host) > 0 && len(host) <= 253 && (audience == "anonymous" || audience == "authenticated")
}
func ValidateSharingSave(old, r Sharing, expected int64, e audit.Record) error {
	if old.Revision != expected {
		return catalog.E(catalog.CodePreconditionFailed, "rating sharing revision does not match")
	}
	facts, err := json.Marshal(e.Details["facts"])
	if err != nil {
		return catalog.E(catalog.CodeInvalidArgument, "rating sharing audit facts are invalid")
	}
	want, err := json.Marshal(map[string]any{"revision": r.Revision, "enabled": r.Enabled})
	if err != nil {
		return catalog.E(catalog.CodeInvalidArgument, "rating sharing audit facts are invalid")
	}
	if expected < 0 || !identity.ValidTenantID(r.ID) || !identity.ValidTenantID(r.TenantID) || !validSharingScope(r.Host, r.Audience) || r.Revision != expected+1 || r.UpdatedAt.IsZero() || expected > 0 && (old.ID != r.ID || old.TenantID != r.TenantID || old.Host != r.Host || old.Audience != r.Audience || r.UpdatedAt.Before(old.UpdatedAt)) || e.Action != "rating.sharing" || e.TargetType != "rating_sharing" || e.TargetID != r.ID || e.TenantID != r.TenantID || !e.At.Equal(r.UpdatedAt) || !bytes.Equal(facts, want) || audit.ValidateNewRecord(e) != nil {
		return catalog.E(catalog.CodeInvalidArgument, "rating sharing record or audit is invalid")
	}
	return nil
}
