package tenantsettings

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

const tenant = "00000000-0000-7000-8000-000000000001"

type sink struct {
	records []audit.Record
	err     error
}

func (s *sink) AppendAuditEvent(_ context.Context, r audit.Record) error {
	if s.err != nil {
		return s.err
	}
	s.records = append(s.records, r)
	return nil
}
func settingsFixture(t *testing.T) (*Service, *MemoryStore, *sink, context.Context) {
	t.Helper()
	access, err := identity.NewMemoryStore([]identity.Seed{{TenantID: tenant, Issuer: "https://issuer.example", Subject: "admin", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}}})
	if err != nil {
		t.Fatal(err)
	}
	a, err := access.ResolvePrincipal(t.Context(), identity.Principal{TenantID: tenant, Issuer: "https://issuer.example", Subject: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identity.WithActor(t.Context(), identity.BindPermissionStore(a, access))
	evidence := &sink{}
	store := NewMemoryStore(evidence)
	service, err := New(store, 15*time.Minute, 5, clock.NewFake(time.Date(2026, 10, 13, 14, 0, 0, 0, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	return service, store, evidence, ctx
}
func TestSettingsInheritanceSaveResetAndAuditRollback(t *testing.T) {
	s, m, e, ctx := settingsFixture(t)
	initial, err := s.Get(ctx)
	if err != nil || initial.Revision != 0 || initial.Effective.AutomaticSourceSyncInterval != Duration(15*time.Minute) || !initial.Effective.AutomaticSourceSyncEnabled || len(m.records) != 0 {
		t.Fatalf("initial %#v %v", initial, err)
	}
	off := false
	interval := Duration(90*time.Second + 500*time.Millisecond)
	input := Overrides{AutomaticSourceSyncEnabled: &off, AutomaticSourceSyncInterval: &interval}
	saved, err := s.Save(ctx, input, s.Tag(initial), "Pause automatic synchronization")
	if err != nil || saved.Revision != 1 || saved.Effective.AutomaticSourceSyncEnabled || saved.Effective.AutomaticSourceSyncInterval != interval || len(e.records) != 1 {
		t.Fatalf("save %#v %v", saved, err)
	}
	off = true
	interval = Duration(time.Hour)
	read, err := s.Get(ctx)
	if err != nil || read.Effective.AutomaticSourceSyncEnabled || read.Effective.AutomaticSourceSyncInterval != Duration(90500*time.Millisecond) {
		t.Fatal("input alias", read, err)
	}
	*read.Overrides.AutomaticSourceSyncEnabled = true
	readAgain, _ := s.Get(ctx)
	if readAgain.Effective.AutomaticSourceSyncEnabled {
		t.Fatal("read leaked mutable pointer")
	}
	oldTag := s.Tag(initial)
	if _, err = s.Save(ctx, Overrides{}, oldTag, "Stale save"); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatal(err)
	}
	// The locked store must reject a request that passed an earlier service read.
	if _, err = m.Save(ctx, saved.Record, s.defaults, oldTag, e.records[0]); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed || len(e.records) != 1 {
		t.Fatal("stale store admission changed policy or audit", err)
	}
	e.err = errors.New("audit unavailable")
	if _, err = s.Save(ctx, Overrides{}, s.Tag(saved), "Reset defaults"); !errors.Is(err, e.err) {
		t.Fatal(err)
	}
	after, _ := s.Get(ctx)
	if after.Revision != 1 || after.Effective.AutomaticSourceSyncEnabled || len(e.records) != 1 {
		t.Fatal("failed audit changed policy")
	}
	e.err = nil
	reset, err := s.Save(ctx, Overrides{}, s.Tag(saved), "Reset defaults")
	if err != nil || reset.Revision != 2 || !reset.Effective.AutomaticSourceSyncEnabled || reset.Overrides.AutomaticSourceSyncEnabled != nil || len(e.records) != 2 {
		t.Fatalf("reset %#v %v", reset, err)
	}
	facts, ok := e.records[1].Details["facts"].(map[string]any)
	if !ok || facts["beforeEnabledSet"] != true {
		t.Fatal("audit cannot reconstruct prior override")
	}
	replacement, err := New(m, time.Hour, 5, s.clock, s.ids)
	if err != nil {
		t.Fatal(err)
	}
	inherited, _ := replacement.Get(ctx)
	if inherited.Effective.AutomaticSourceSyncInterval != Duration(time.Hour) || replacement.Tag(inherited) == s.Tag(reset) {
		t.Fatal("defaults missing from validator")
	}
	if _, err = replacement.Save(ctx, Overrides{}, s.Tag(reset), "Old deployment validator"); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatal(err)
	}
}
func TestSettingsStrictValuesAndBounds(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		valid bool
	}{
		{`{}`, true}, {`{"automaticSourceSyncEnabled":false}`, true}, {`{"automaticSourceSyncInterval":"1m"}`, true}, {`{"automaticSourceSyncInterval":"24h"}`, true}, {`{"automaticSourceSyncInterval":"90.5s"}`, true},
		{`{"automaticSourceSyncEnabled":null}`, false}, {`{"automaticSourceSyncInterval":null}`, false}, {`{"automaticSourceSyncInterval":15}`, false}, {`{"automaticSourceSyncInterval":""}`, false}, {`{"automaticSourceSyncInterval":"soon"}`, false}, {`{"automaticSourceSyncInterval":"59s"}`, false}, {`{"automaticSourceSyncInterval":"24h1ns"}`, false}, {`{"automaticSourceSyncInterval":"-1m"}`, false}, {`{"newJobMaxAttempts":5}`, true}, {`{"AutomaticSourceSyncEnabled":true}`, false}, {`{"automaticSourceSyncEnabled":true,"automaticSourceSyncEnabled":false}`, false},
		{`{"automaticSourceSyncInterval":"1m0s` + strings.Repeat(`0\u00b5s`, 20) + `"}`, true},
		{`{"automaticSourceSyncInterval":"1m0s` + strings.Repeat(`0\u03bcs`, 20) + `"}`, true},
		{`{"automaticSourceSyncInterval":"1m` + strings.Repeat(`0\u00b5s`, 21) + `"}`, false},
		{`{"automaticSourceSyncInterval":"1m` + strings.Repeat(`0\u03bcs`, 21) + `"}`, false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			var o Overrides
			err := jsondoc.Decode([]byte(tc.raw), &o)
			if err == nil {
				err = o.Validate()
			}
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%t err=%v", tc.valid, err)
			}
		})
	}
	var d Duration
	if json.Unmarshal([]byte(`"`+strings.Repeat("0", 65)+`1m"`), &d) == nil {
		t.Fatal("accepted overlong duration")
	}
	raw, err := json.Marshal(Duration(90500 * time.Millisecond))
	if err != nil || string(raw) != `"1m30.5s"` {
		t.Fatal(string(raw), err)
	}
}
func TestSettingsRejectInvalidDependenciesAuthorityAndTransitions(t *testing.T) {
	s, m, e, ctx := settingsFixture(t)
	for _, interval := range []time.Duration{0, time.Minute - time.Nanosecond, 24*time.Hour + time.Nanosecond} {
		if _, err := New(m, interval, 5, s.clock, s.ids); err == nil {
			t.Fatal("invalid interval", interval)
		}
	}
	if _, err := New(nil, time.Minute, 5, s.clock, s.ids); err == nil {
		t.Fatal("nil store")
	}
	if _, err := New(m, time.Minute, 5, nil, s.ids); err == nil {
		t.Fatal("nil clock")
	}
	if _, err := New(m, time.Minute, 5, s.clock, nil); err == nil {
		t.Fatal("nil IDs")
	}
	if _, err := s.Get(t.Context()); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatal(err)
	}
	if _, err := s.Save(t.Context(), Overrides{}, "", "Denied"); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatal(err)
	}
	initial, _ := s.Get(ctx)
	if _, err := s.Save(ctx, Overrides{}, s.Tag(initial), ""); err == nil {
		t.Fatal("missing reason")
	}
	short := Duration(time.Second)
	if _, err := s.Save(ctx, Overrides{AutomaticSourceSyncInterval: &short}, s.Tag(initial), "Invalid interval"); err == nil {
		t.Fatal("invalid bound")
	}
	saved, err := s.Save(ctx, Overrides{}, s.Tag(initial), "Record inheritance")
	if err != nil {
		t.Fatal(err)
	}
	record := e.records[0]
	for _, change := range []func(*Record, *audit.Record){
		func(r *Record, _ *audit.Record) { r.TenantID = "bad" }, func(r *Record, _ *audit.Record) { r.Revision = 3 }, func(r *Record, _ *audit.Record) { r.UpdatedAt = nil }, func(_ *Record, a *audit.Record) { a.TargetID = "00000000-0000-7000-8000-000000000002" }, func(_ *Record, a *audit.Record) { a.Action = "rating.sharing" }, func(_ *Record, a *audit.Record) { a.Details = map[string]any{"facts": map[string]any{}} }, func(_ *Record, a *audit.Record) { a.Details = map[string]any{"facts": make(chan int)} },
	} {
		r := clone(saved.Record)
		a := record
		change(&r, &a)
		if ValidateSave(initial.Record, r, s.defaults, s.Tag(initial), a) == nil {
			t.Fatal("invalid binding accepted")
		}
	}
	m.mu.Lock()
	r := saved.Record
	r.Revision = math.MaxInt64
	m.records[tenant] = r
	m.mu.Unlock()
	view, _ := s.Get(ctx)
	if _, err := s.Save(ctx, Overrides{}, s.Tag(view), "Overflow"); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := m.Get(cancelled, tenant); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := m.Save(cancelled, saved.Record, s.defaults, s.Tag(initial), record); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := m.WithAutomaticSourcePolicy(cancelled, tenant, time.Minute, 5, func(bool, time.Duration, int) error { t.Fatal("cancelled callback"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	missing := NewMemoryStore(nil)
	if _, err := missing.Save(ctx, saved.Record, s.defaults, s.Tag(initial), record); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatal(err)
	}
}
func TestSettingsConcurrentFirstSaveAndAtomicMemoryPolicy(t *testing.T) {
	s, m, e, ctx := settingsFixture(t)
	initial, _ := s.Get(ctx)
	// Use valid independently bound requests; only one can consume revision zero.
	var wg sync.WaitGroup
	outcomes := make(chan error, 2)
	for range 2 {
		wg.Go(func() { _, err := s.Save(ctx, Overrides{}, s.Tag(initial), "Concurrent first save"); outcomes <- err })
	}
	wg.Wait()
	close(outcomes)
	success, conflict := 0, 0
	for err := range outcomes {
		switch {
		case err == nil:
			success++
		case catalog.ErrorCodeOf(err) == catalog.CodePreconditionFailed:
			conflict++
		default:
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 || len(e.records) != 1 {
		t.Fatal(success, conflict, len(e.records))
	}
	err := m.WithAutomaticSourcePolicy(ctx, tenant, time.Hour, 5, func(enabled bool, interval time.Duration, _ int) error {
		if !enabled || interval != time.Hour {
			t.Fatal(enabled, interval)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	other, _ := m.Get(ctx, "00000000-0000-7000-8000-000000000002")
	if !reflect.DeepEqual(other, Record{TenantID: "00000000-0000-7000-8000-000000000002"}) {
		t.Fatal(other)
	}
}

func TestNewJobAttemptsBoundsInheritanceAndPointerIsolation(t *testing.T) {
	s, m, e, ctx := settingsFixture(t)
	for _, n := range []int{0, 21, -1} {
		if (Overrides{NewJobMaxAttempts: &n}).Validate() == nil {
			t.Fatal("invalid attempts", n)
		}
		if _, err := New(m, time.Minute, n, s.clock, s.ids); err == nil {
			t.Fatal("invalid fallback", n)
		}
	}
	for _, n := range []int{1, 20} {
		if (Overrides{NewJobMaxAttempts: &n}).Validate() != nil {
			t.Fatal("valid attempts", n)
		}
	}
	v, _ := s.Get(ctx)
	n := 3
	next, err := s.Save(ctx, Overrides{NewJobMaxAttempts: &n}, s.Tag(v), "Change future Job budgets")
	if err != nil {
		t.Fatal(err)
	}
	n = 8
	read, _ := s.Get(ctx)
	if read.Effective.NewJobMaxAttempts != 3 {
		t.Fatal("input alias")
	}
	*read.Overrides.NewJobMaxAttempts = 9
	read, _ = s.Get(ctx)
	if read.Effective.NewJobMaxAttempts != 3 {
		t.Fatal("read alias")
	}
	if err = m.WithNewJobAttemptLimit(ctx, tenant, 5, func(got int) error {
		if got != 3 {
			t.Fatal(got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = m.WithNewJobAttemptLimit(ctx, "other", 7, func(got int) error {
		if got != 7 {
			t.Fatal(got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err = m.WithNewJobAttemptLimit(cancelled, tenant, 5, func(int) error { t.Fatal("cancelled admission"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	facts, ok := e.records[0].Details["facts"].(map[string]any)
	if !ok || facts["afterAttemptsSet"] != true {
		t.Fatal("missing override facts")
	}
	restored, err := s.Save(ctx, Overrides{}, s.Tag(next), "Inherit future Job budgets")
	if err != nil || restored.Effective.NewJobMaxAttempts != 5 {
		t.Fatal(restored, err)
	}
}
