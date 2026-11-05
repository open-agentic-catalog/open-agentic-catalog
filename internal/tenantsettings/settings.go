// Package tenantsettings owns bounded tenant policy, not deployment configuration.
package tenantsettings

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

const Action = "tenant.settings.changed"

// Duration uses the same units as deployment durations without importing their loader.
type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }
func (d *Duration) UnmarshalJSON(raw []byte) error {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil || utf8.RuneCountInString(text) < 2 || utf8.RuneCountInString(text) > 64 {
		return errors.New("interval must be a duration string")
	}
	value, err := time.ParseDuration(text)
	if err != nil {
		return errors.New("interval must be a duration string")
	}
	*d = Duration(value)
	return nil
}

type Overrides struct {
	NewJobMaxAttempts           *int      `json:"newJobMaxAttempts,omitempty"`
	AutomaticSourceSyncEnabled  *bool     `json:"automaticSourceSyncEnabled,omitempty"`
	AutomaticSourceSyncInterval *Duration `json:"automaticSourceSyncInterval,omitempty"`
}
type Policy struct {
	NewJobMaxAttempts           int      `json:"newJobMaxAttempts"`
	AutomaticSourceSyncEnabled  bool     `json:"automaticSourceSyncEnabled"`
	AutomaticSourceSyncInterval Duration `json:"automaticSourceSyncInterval"`
}
type Record struct {
	TenantID  string     `json:"tenantId"`
	Revision  int64      `json:"revision"`
	Overrides Overrides  `json:"overrides"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
}
type View struct {
	Record
	DeploymentDefaults Policy `json:"deploymentDefaults"`
	Effective          Policy `json:"effective"`
}

func (o Overrides) Validate() error {
	if o.NewJobMaxAttempts != nil && (*o.NewJobMaxAttempts < 1 || *o.NewJobMaxAttempts > 20) {
		return catalog.E(catalog.CodeInvalidArgument, "new Job maximum attempts must be between one and twenty")
	}
	if o.AutomaticSourceSyncInterval != nil && (time.Duration(*o.AutomaticSourceSyncInterval) < time.Minute || time.Duration(*o.AutomaticSourceSyncInterval) > 24*time.Hour) {
		return catalog.E(catalog.CodeInvalidArgument, "automatic source interval must be between one minute and twenty-four hours")
	}
	return nil
}
func (o Overrides) Resolve(defaults Policy) Policy {
	if o.NewJobMaxAttempts != nil {
		defaults.NewJobMaxAttempts = *o.NewJobMaxAttempts
	}
	if o.AutomaticSourceSyncEnabled != nil {
		defaults.AutomaticSourceSyncEnabled = *o.AutomaticSourceSyncEnabled
	}
	if o.AutomaticSourceSyncInterval != nil {
		defaults.AutomaticSourceSyncInterval = *o.AutomaticSourceSyncInterval
	}
	return defaults
}

// ETag binds the saved revision and responding process's safe defaults.
func ETag(r Record, defaults Policy) string {
	raw, err := json.Marshal(struct {
		Record   Record `json:"record"`
		Defaults Policy `json:"defaults"`
	}{r, defaults})
	if err != nil {
		return ""
	} // Fail closed if a future policy encoder becomes fallible.
	sum := sha256.Sum256(raw)
	return `"settings-` + hex.EncodeToString(sum[:]) + `"`
}

type Store interface {
	Get(context.Context, string) (Record, error)
	Save(context.Context, Record, Policy, string, audit.Record) (Record, error)
}
type Service struct {
	store    Store
	defaults Policy
	clock    catalog.Clock
	ids      catalog.IDGenerator
}

func New(store Store, interval time.Duration, maxAttempts int, clock catalog.Clock, ids catalog.IDGenerator) (*Service, error) {
	if maxAttempts < 1 || maxAttempts > 20 || store == nil || clock == nil || ids == nil || interval < time.Minute || interval > 24*time.Hour {
		return nil, errors.New("tenant settings dependencies or defaults are invalid")
	}
	return &Service{store: store, defaults: Policy{AutomaticSourceSyncEnabled: true, AutomaticSourceSyncInterval: Duration(interval), NewJobMaxAttempts: maxAttempts}, clock: clock, ids: ids}, nil
}
func (s *Service) Get(ctx context.Context) (View, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := identity.RequirePermission(ctx, "settings.read", "")
	if err != nil {
		return View{}, catalog.PermissionError("tenant settings read is not permitted", err)
	}
	r, err := s.store.Get(ctx, actor.TenantID)
	return s.view(r), err
}
func (s *Service) view(r Record) View { return View{r, s.defaults, r.Overrides.Resolve(s.defaults)} }
func (s *Service) Tag(v View) string  { return ETag(v.Record, s.defaults) }
func (s *Service) Save(ctx context.Context, overrides Overrides, tag, reason string) (View, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := identity.RequirePermission(ctx, "settings.manage", "")
	if err != nil {
		return View{}, catalog.PermissionError("tenant settings management is not permitted", err)
	}
	if err = overrides.Validate(); err != nil {
		return View{}, err
	}
	old, err := s.Get(ctx)
	if err != nil {
		return View{}, err
	}
	if tag != s.Tag(old) {
		return View{}, catalog.E(catalog.CodePreconditionFailed, "If-Match must contain the current settings ETag")
	}
	if old.Revision == math.MaxInt64 {
		return View{}, catalog.E(catalog.CodeConflict, "tenant settings revision is exhausted")
	}
	previous := time.Time{}
	if old.UpdatedAt != nil {
		previous = *old.UpdatedAt
	}
	now := catalog.TransitionTime(s.clock.Now(), previous).UTC().Truncate(time.Microsecond)
	next := Record{TenantID: actor.TenantID, Revision: old.Revision + 1, Overrides: cloneOverrides(overrides), UpdatedAt: &now}
	id, err := s.ids.New(now)
	if err != nil {
		return View{}, err
	}
	execution := audit.ExecutionFrom(ctx)
	execution.AuthorizationDigest = identity.DecisionDigest(ctx)
	evidence, err := audit.NewRecord(id, actor.TenantID, audit.Principal{ID: actor.MembershipID, Type: audit.PrincipalCatalogMembership, ActorType: string(actor.ActorType), RealmID: actor.RealmID}, Action, "tenant_settings", actor.TenantID, reason, execution, changeFacts(old.Record, next), now)
	if err != nil {
		return View{}, catalog.Wrap(catalog.CodeInvalidArgument, "tenant settings reason or evidence is invalid", err)
	}
	saved, err := s.store.Save(ctx, next, s.defaults, tag, evidence)
	return s.view(saved), err
}
func changeFacts(before, after Record) map[string]any {
	facts := map[string]any{"beforeRevision": before.Revision, "afterRevision": after.Revision}
	for prefix, o := range map[string]Overrides{"before": before.Overrides, "after": after.Overrides} {
		facts[prefix+"AttemptsSet"] = o.NewJobMaxAttempts != nil
		if o.NewJobMaxAttempts != nil {
			facts[prefix+"Attempts"] = *o.NewJobMaxAttempts
		}
		facts[prefix+"EnabledSet"] = o.AutomaticSourceSyncEnabled != nil
		facts[prefix+"IntervalSet"] = o.AutomaticSourceSyncInterval != nil
		if o.AutomaticSourceSyncEnabled != nil {
			facts[prefix+"Enabled"] = *o.AutomaticSourceSyncEnabled
		}
		if o.AutomaticSourceSyncInterval != nil {
			facts[prefix+"Interval"] = time.Duration(*o.AutomaticSourceSyncInterval).String()
		}
	}
	return facts
}

// ValidateSave binds each persisted transition to its exact prior policy and audit.
func ValidateSave(old, next Record, defaults Policy, tag string, evidence audit.Record) error {
	if tag != ETag(old, defaults) {
		return catalog.E(catalog.CodePreconditionFailed, "tenant settings changed; read the current policy")
	}
	if err := next.Overrides.Validate(); err != nil {
		return err
	}
	facts, err := json.Marshal(changeFacts(old, next))
	if err != nil {
		return err
	}
	actual, marshalErr := json.Marshal(evidence.Details["facts"])
	if marshalErr != nil {
		return catalog.E(catalog.CodeInvalidArgument, "tenant settings audit facts are invalid")
	}
	if !identity.ValidTenantID(next.TenantID) || old.TenantID != next.TenantID || old.Revision < 0 || old.Revision == math.MaxInt64 || next.Revision != old.Revision+1 || next.UpdatedAt == nil || next.UpdatedAt.IsZero() || old.UpdatedAt != nil && next.UpdatedAt.Before(*old.UpdatedAt) || evidence.Action != Action || evidence.TargetType != "tenant_settings" || evidence.TargetID != next.TenantID || evidence.TenantID != next.TenantID || !evidence.At.Equal(*next.UpdatedAt) || !bytes.Equal(actual, facts) || audit.ValidateNewRecord(evidence) != nil {
		return catalog.E(catalog.CodeInvalidArgument, "tenant settings transition or evidence is invalid")
	}
	return nil
}
func cloneOverrides(o Overrides) Overrides {
	if o.NewJobMaxAttempts != nil {
		value := *o.NewJobMaxAttempts
		o.NewJobMaxAttempts = &value
	}
	if o.AutomaticSourceSyncEnabled != nil {
		v := *o.AutomaticSourceSyncEnabled
		o.AutomaticSourceSyncEnabled = &v
	}
	if o.AutomaticSourceSyncInterval != nil {
		v := *o.AutomaticSourceSyncInterval
		o.AutomaticSourceSyncInterval = &v
	}
	return o
}
func clone(r Record) Record {
	r.Overrides = cloneOverrides(r.Overrides)
	if r.UpdatedAt != nil {
		v := *r.UpdatedAt
		r.UpdatedAt = &v
	}
	return r
}

type EvidenceSink interface {
	AppendAuditEvent(context.Context, audit.Record) error
}
type MemoryStore struct {
	mu       sync.RWMutex
	records  map[string]Record
	evidence EvidenceSink
}

func NewMemoryStore(sink EvidenceSink) *MemoryStore {
	return &MemoryStore{records: make(map[string]Record), evidence: sink}
}
func (m *MemoryStore) Get(ctx context.Context, tenant string) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.get(tenant), nil
}
func (m *MemoryStore) get(tenant string) Record {
	r, ok := m.records[tenant]
	if !ok {
		return Record{TenantID: tenant}
	}
	return clone(r)
}
func (m *MemoryStore) Save(ctx context.Context, r Record, defaults Policy, tag string, evidence audit.Record) (Record, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var saved Record
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var err error
			saved, err = m.Save(ctx, r, defaults, tag, evidence)
			return err
		})
		return saved, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	if err := ValidateSave(m.get(r.TenantID), r, defaults, tag, evidence); err != nil {
		return Record{}, err
	}
	if m.evidence == nil {
		return Record{}, catalog.E(catalog.CodeInternal, "tenant settings audit sink is unavailable")
	}
	if err := m.evidence.AppendAuditEvent(ctx, evidence); err != nil {
		return Record{}, err
	}
	m.records[r.TenantID] = clone(r)
	return clone(r), nil
}

// WithAutomaticSourcePolicy fences an exact tenant's memory admission against saves.
func (m *MemoryStore) WithAutomaticSourcePolicy(ctx context.Context, tenant string, fallback time.Duration, attempts int, admit func(bool, time.Duration, int) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	policy := m.get(tenant).Overrides.Resolve(Policy{AutomaticSourceSyncEnabled: true, AutomaticSourceSyncInterval: Duration(fallback), NewJobMaxAttempts: attempts})
	return admit(policy.AutomaticSourceSyncEnabled, time.Duration(policy.AutomaticSourceSyncInterval), policy.NewJobMaxAttempts)
}

// WithNewJobAttemptLimit retains the settings read fence through a memory enqueue.
func (m *MemoryStore) WithNewJobAttemptLimit(ctx context.Context, tenant string, fallback int, admit func(int) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	value := fallback
	if override := m.get(tenant).Overrides.NewJobMaxAttempts; override != nil {
		value = *override
	}
	return admit(value)
}
