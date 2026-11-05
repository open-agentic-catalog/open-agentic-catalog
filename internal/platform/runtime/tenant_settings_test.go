package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/tenantsettings"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

func TestMemorySettingsJobAdmissionPreservesIntentAndFailures(t *testing.T) {
	for _, mode := range []string{"default", "explicit", "retry child", "cancelled", "store failure", "current permission", "saved default", "saved explicit", "saved retry child", "saved other tenant"} {
		t.Run(mode, func(t *testing.T) {
			probe := &trustStoreProbe{}
			store := memorySettingsJobs{Store: probe, settings: tenantsettings.NewMemoryStore(nil)}
			job := jobs.Job{TenantID: "00000000-0000-7000-8000-000000000001", AttemptLimit: 5, DefaultAttemptLimit: true}
			ctx := t.Context()
			failure := errors.New("enqueue failed")
			want := 5
			if strings.HasPrefix(mode, "saved ") {
				store.settings = tenantsettings.NewMemoryStore(catalog.NewMemoryRepository())
				service, err := tenantsettings.New(store.settings, time.Minute, 5, clock.NewFake(time.Date(2026, 10, 13, 14, 0, 0, 0, time.UTC)), id.Generator{})
				if err != nil {
					t.Fatal(err)
				}
				admin := identityfixture.WithActor(ctx, identity.Actor{TenantID: job.TenantID, Subject: "admin", MembershipID: "00000000-0000-7000-8000-000000000011", RealmID: "00000000-0000-7000-8000-000000000012", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}})
				initial, err := service.Get(admin)
				if err != nil {
					t.Fatal(err)
				}
				attempts := 2
				if _, err = service.Save(admin, tenantsettings.Overrides{NewJobMaxAttempts: &attempts}, service.Tag(initial), "Change owned admission default"); err != nil {
					t.Fatal(err)
				}
				if mode == "saved default" {
					want = 2
				}
			}
			switch mode {
			case "explicit", "saved explicit":
				job.DefaultAttemptLimit = false
			case "retry child", "saved retry child":
				job.ParentJobID = "parent"
			case "saved other tenant":
				job.TenantID = "00000000-0000-7000-8000-000000000002"
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "store failure":
				probe.err = failure
			case "current permission":
				ctx = identity.WithPermissionContext(identityfixture.WithActor(ctx, identity.Actor{TenantID: job.TenantID, Subject: "admin", Roles: []identity.Role{identity.RoleTenantAdmin}}))
				if _, err := identity.RequirePermission(ctx, "source.sync", ""); err != nil {
					t.Fatal(err)
				}
			}
			got, reused, err := store.Enqueue(ctx, job)
			switch mode {
			case "cancelled":
				if !errors.Is(err, context.Canceled) || probe.enqueues != 0 {
					t.Fatal(got, err)
				}
			case "store failure":
				if !errors.Is(err, failure) || probe.enqueues != 1 {
					t.Fatal(got, err)
				}
			default:
				if err != nil || reused || got.AttemptLimit != want || got.DefaultAttemptLimit || probe.enqueues != 1 {
					t.Fatal(got, reused, err)
				}
			}
		})
	}
}
