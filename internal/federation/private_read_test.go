package federation

import (
	"context"
	"errors"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"reflect"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
)

func TestPrivateFederationReadsRejectMissingAuthorityAndAuditSetup(t *testing.T) {
	t.Parallel()
	const inspectionID = "00000000-0000-7000-8000-000000000010"
	const observationID = "00000000-0000-7000-8000-000000000011"
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	identifierFailure := errors.New("audit identifier unavailable")
	for _, read := range []struct {
		name string
		call func(*Manager, context.Context) (any, error)
		zero any
	}{
		{"inspection", func(m *Manager, ctx context.Context) (any, error) { return m.GetInspection(ctx, inspectionID) }, Inspection{}},
		{"raw observation", func(m *Manager, ctx context.Context) (any, error) { return m.GetRawObservation(ctx, observationID) }, Observation{}},
	} {
		t.Run(read.name, func(t *testing.T) {
			t.Parallel()
			for _, test := range []struct {
				name string
				code catalog.ErrorCode
			}{
				{"unauthenticated", catalog.CodeForbidden},
				{"reader role", catalog.CodeForbidden},
				{"other tenant", catalog.CodeNotFound},
				{"missing record", catalog.CodeNotFound},
				{"missing evidence sink", catalog.CodeDependencyUnavailable},
				{"identifier failure", catalog.CodeInternal},
				{"missing membership attribution", catalog.CodeInternal},
			} {
				t.Run(test.name, func(t *testing.T) {
					t.Parallel()
					store := NewMemoryStore()
					store.inspections[inspectionID] = Inspection{ID: inspectionID, TenantID: auditEvidenceTenantID, RequestedURL: "https://private.example/source", BodyDigest: federationTestPolicyDigest, PolicyDigest: federationTestPolicyDigest, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
					body := []byte(`{"private":"document"}`)
					store.observations[observationID] = Observation{ID: observationID, TenantID: auditEvidenceTenantID, RawEntryAvailable: true, RawEntry: body, ExactEntryDigest: audit.DigestBytes(body)}
					evidence := &recordingFederationEvidence{}
					// Read-only manager units need no fetcher or source onboarding workflow.
					manager := &Manager{store: store, clock: clock.NewFake(now), ids: &scriptedFederationAuditIDs{values: []string{"00000000-0000-7000-8000-000000000012"}}, evidence: evidence}
					ctx := auditEvidenceContext()
					actor, ok := identity.FromContext(ctx)
					if !ok {
						t.Fatal("missing fixture actor")
					}
					switch test.name {
					case "unauthenticated":
						ctx = t.Context()
					case "reader role":
						actor.Roles = []identity.Role{identity.RoleReader}
						ctx = identityfixture.WithActor(ctx, actor)
					case "other tenant":
						actor.TenantID = "00000000-0000-7000-8000-000000000099"
						ctx = identityfixture.WithActor(ctx, actor)
					case "missing record":
						delete(store.inspections, inspectionID)
						delete(store.observations, observationID)
					case "missing evidence sink":
						manager.evidence = nil
					case "identifier failure":
						manager.ids = failingMaintenanceIDGenerator{err: identifierFailure}
					case "missing membership attribution":
						actor.MembershipID = ""
						ctx = identityfixture.WithActor(ctx, actor)
					}
					got, err := read.call(manager, ctx)
					if catalog.ErrorCodeOf(err) != test.code || !reflect.DeepEqual(got, read.zero) {
						t.Fatalf("private read = %#v, %v, want empty result and %s", got, err, test.code)
					}
					if test.name == "identifier failure" && !errors.Is(err, identifierFailure) {
						t.Fatalf("identifier failure cause was lost: %v", err)
					}
					if len(evidence.audit) != 0 || len(evidence.outbox) != 0 {
						t.Fatal("rejected private read retained success evidence or a notification")
					}
				})
			}
		})
	}
}

func TestRawObservationReadRequiresRetainedExactBytes(t *testing.T) {
	t.Parallel()
	const observationID = "00000000-0000-7000-8000-000000000011"
	store := NewMemoryStore()
	store.observations[observationID] = Observation{ID: observationID, TenantID: auditEvidenceTenantID, RawEntryAvailable: false}
	// With no audit dependencies, advancing past the unavailable-bytes guard
	// would either fail with a different code or panic rather than hide a regression.
	manager := &Manager{store: store}
	got, err := manager.GetRawObservation(auditEvidenceContext(), observationID)
	if catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed || !reflect.DeepEqual(got, Observation{}) {
		t.Fatalf("unavailable raw entry = %#v, %v", got, err)
	}
}
