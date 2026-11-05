package publication

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/events"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

type recordingPublicationEvidence struct {
	audit  []audit.Record
	outbox []catalog.OutboxEvent
	err    error
}

func (s *recordingPublicationEvidence) AppendEvidence(_ context.Context, record audit.Record, outbox *catalog.OutboxEvent) error {
	if s.err != nil {
		return s.err
	}
	s.audit = append(s.audit, record)
	if outbox != nil {
		s.outbox = append(s.outbox, *outbox)
	}
	return nil
}

func TestPublicationLifecycleSeparatesAuditOnlyCandidateFromPublicTransitions(t *testing.T) {
	t.Parallel()

	const tenantID = "00000000-0000-7000-8000-000000000001"
	record := eligiblePublicationRecord()
	record.Resource.TenantID = tenantID
	evidence := &recordingPublicationEvidence{}
	store := NewMemoryStore(evidence)
	// Keep a sub-microsecond remainder: a backward clock must not let the
	// microsecond activation floor precede the immutable creation timestamp.
	serviceClock := &mutablePublicationClock{now: time.Date(2026, time.September, 3, 12, 0, 0, 999, time.UTC)}
	manager, err := NewManager(Options{
		Records:                     publicationRecordSource{records: []catalog.CatalogRecord{record}},
		Store:                       store,
		Clock:                       serviceClock,
		IDs:                         id.Generator{},
		BaseURL:                     "https://catalog.example.com",
		PublisherDomain:             "catalog.example.com",
		ARDProfile:                  "ard-test",
		AICatalogProfile:            "ai-catalog-test",
		CursorKey:                   []byte("01234567890123456789012345678901"),
		Host:                        "catalog.example.com",
		Audience:                    AudienceAnonymous,
		Evidence:                    evidence,
		AcknowledgeOutboxAtCreation: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	actor := identity.Actor{TenantID: tenantID, Subject: "external-subject", Roles: []identity.Role{identity.RolePublisher}, RealmID: "00000000-0000-7000-8000-000000000002", MembershipID: "00000000-0000-7000-8000-000000000003", ActorType: identity.ActorHuman}
	ctx := identityfixture.WithActor(context.Background(), actor)

	candidate, err := manager.BuildCandidate(ctx, tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence.audit) != 1 || len(evidence.outbox) != 0 || evidence.audit[0].Action != audit.ActionPublicationCandidateCreated {
		t.Fatalf("candidate evidence = audit %#v outbox %#v", evidence.audit, evidence.outbox)
	}
	serviceClock.now = serviceClock.now.Add(-2 * time.Second)
	active, err := manager.Activate(ctx, tenantID, candidate.ID, "Activate the reviewed candidate")
	if err != nil || active.State != SnapshotActive {
		t.Fatalf("activation = %#v, %v", active, err)
	}
	if err := ValidateSnapshotIntegrity(active); err != nil {
		t.Fatalf("activated snapshot cannot be read or rolled back: %v", err)
	}
	if active.ActivatedAt == nil || !active.ActivatedAt.Equal(candidate.CreatedAt) || !evidence.audit[1].At.Equal(*active.ActivatedAt) || !evidence.outbox[0].CreatedAt.Equal(*active.ActivatedAt) {
		t.Fatal("clock backstep made activation precede creation or disagree with evidence")
	}
	workerJobID := "00000000-0000-7000-8000-000000000004"
	workerCtx := audit.WithJobExecution(ctx, audit.JobExecution{ID: workerJobID, Kind: "publication.generate", Attempt: 1, Initiator: actor.MembershipID, Trigger: "manual"})
	replacement, err := manager.BuildCandidate(workerCtx, tenantID)
	if err != nil {
		t.Fatal(err)
	}
	active, err = manager.Activate(ctx, tenantID, replacement.ID, "Activate the replacement candidate")
	if err != nil || active.ID != replacement.ID || active.State != SnapshotActive {
		t.Fatalf("replacement activation = %#v, %v", active, err)
	}
	active, err = manager.Rollback(ctx, tenantID, candidate.ID, "Restore the previous reviewed snapshot")
	if err != nil || active.ID != candidate.ID || active.State != SnapshotActive {
		t.Fatalf("rollback = %#v, %v", active, err)
	}
	if active.ActivatedAt == nil || !active.ActivatedAt.Equal(candidate.CreatedAt.Add(time.Microsecond)) || !evidence.audit[4].At.Equal(*active.ActivatedAt) || !evidence.outbox[2].CreatedAt.Equal(*active.ActivatedAt) {
		t.Fatal("rollback did not advance retained activation time with matching evidence")
	}
	retired, err := manager.Unpublish(ctx, tenantID, "Withdraw the reviewed publication")
	if err != nil || retired.State != SnapshotRetired {
		t.Fatalf("unpublish = %#v, %v", retired, err)
	}
	wantActions := []string{audit.ActionPublicationCandidateCreated, audit.ActionPublicationActivated, audit.ActionPublicationCandidateCreated, audit.ActionPublicationActivated, audit.ActionPublicationRolledBack, audit.ActionPublicationUnpublished}
	if len(evidence.audit) != len(wantActions) || len(evidence.outbox) != 4 {
		t.Fatalf("publication transition evidence = audit %#v outbox %#v", evidence.audit, evidence.outbox)
	}
	for index, action := range wantActions {
		if evidence.audit[index].Action != action {
			t.Fatalf("publication audit action %d = %q, want %q", index, evidence.audit[index].Action, action)
		}
	}
	workerAudit := evidence.audit[2]
	workerFacts, _ := workerAudit.Details["facts"].(map[string]any)
	if workerAudit.Actor != audit.PrincipalIDCatalogWorker || workerFacts["trigger"] != "manual" || workerFacts["jobId"] != workerJobID || workerFacts["initiator"] != actor.MembershipID {
		t.Fatalf("worker publication attribution = %#v", workerAudit)
	}
	rollbackFacts, _ := evidence.audit[4].Details["facts"].(map[string]any)
	if rollbackFacts["replacedSnapshotId"] != replacement.ID {
		t.Fatalf("publication rollback facts = %#v", rollbackFacts)
	}
	wantTargets := []string{candidate.ID, replacement.ID, candidate.ID, candidate.ID}
	for index, outbox := range evidence.outbox {
		var reference events.NotificationReference
		if err := json.Unmarshal(outbox.Data, &reference); err != nil || reference.Host != "catalog.example.com" || reference.TargetType != "publication_snapshot" || reference.TargetID != wantTargets[index] || outbox.PublishedAt == nil {
			t.Fatalf("publication outbox reference = %#v, %v", outbox, err)
		}
	}
	encoded, err := json.Marshal(evidence.audit)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), actor.Subject) {
		t.Fatalf("external identity subject leaked into publication audit: %s", encoded)
	}
}

func TestSafetyWithdrawalRoutesEvidenceThroughTrustedBoundary(t *testing.T) {
	t.Parallel()

	const tenantID = "00000000-0000-7000-8000-000000000011"
	record := eligiblePublicationRecord()
	record.Resource.TenantID = tenantID
	evidence := &recordingPublicationEvidence{}
	store := NewMemoryStore(evidence)
	serviceClock := &mutablePublicationClock{now: time.Date(2026, time.September, 3, 13, 0, 0, 0, time.UTC)}
	manager, err := NewManager(Options{
		Records:                     publicationRecordSource{records: []catalog.CatalogRecord{record}},
		Store:                       store,
		Clock:                       serviceClock,
		IDs:                         id.Generator{},
		BaseURL:                     "https://catalog.example.com",
		PublisherDomain:             "catalog.example.com",
		ARDProfile:                  "ard-test",
		AICatalogProfile:            "ai-catalog-test",
		CursorKey:                   []byte("01234567890123456789012345678901"),
		Host:                        "catalog.example.com",
		Audience:                    AudienceAnonymous,
		Evidence:                    evidence,
		AcknowledgeOutboxAtCreation: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	actor := identity.Actor{TenantID: tenantID, Subject: "fixture", Roles: []identity.Role{identity.RolePublisher}, RealmID: "00000000-0000-7000-8000-000000000012", MembershipID: "00000000-0000-7000-8000-000000000013", ActorType: identity.ActorHuman}
	ctx := identityfixture.WithActor(context.Background(), actor)
	candidate, err := manager.BuildCandidate(ctx, tenantID)
	if err != nil {
		t.Fatal(err)
	}
	active, err := manager.Activate(ctx, tenantID, candidate.ID, "Activate the reviewed candidate")
	if err != nil {
		t.Fatal(err)
	}

	corrupt := active
	corrupt.Host = "INVALID HOST"
	corrupt.TenantID = "00000000-0000-7000-8000-000000000099"
	store.mu.Lock()
	store.snapshots[memorySnapshotKey(tenantID, active.ID)] = corrupt
	store.mu.Unlock()
	retired := corrupt
	retired.State = SnapshotRetired

	changed, err := manager.WithdrawDomain(ctx, tenantID, "unrelated.example.com", WithdrawalCause{Cause: "publisher_domain_revoked", OriginatingAction: "publisher.publication_domain_revoked", OriginatingTargetID: "00000000-0000-7000-8000-000000000014"})
	if err != nil || !changed {
		t.Fatalf("safety withdrawal = %t, %v", changed, err)
	}
	withdrawal := evidence.audit[len(evidence.audit)-1]
	wantBefore, err := snapshotStateDigest(corrupt)
	if err != nil {
		t.Fatal(err)
	}
	wantAfter, err := snapshotStateDigest(retired)
	if err != nil {
		t.Fatal(err)
	}
	if withdrawal.TenantID != tenantID || withdrawal.BeforeDigest != wantBefore || withdrawal.AfterDigest != wantAfter {
		t.Fatalf("withdrawal evidence boundary or digests = %#v", withdrawal)
	}
	facts, ok := withdrawal.Details["facts"].(map[string]any)
	if !ok || facts["host"] != "catalog.example.com" || facts["ardDigest"] != nil || facts["aiCatalogDigest"] != nil {
		t.Fatalf("withdrawal facts = %#v", withdrawal.Details["facts"])
	}
	outbox := evidence.outbox[len(evidence.outbox)-1]
	var reference events.NotificationReference
	if err := json.Unmarshal(outbox.Data, &reference); err != nil || outbox.TenantID != tenantID || reference.Host != "catalog.example.com" {
		t.Fatalf("withdrawal notification = %#v, %#v, %v", outbox, reference, err)
	}
}

func TestPublicationMemoryStoreRollsBackWhenEvidenceAppendFails(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("audit unavailable")
	store := NewMemoryStore(&recordingPublicationEvidence{err: sentinel})
	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	candidate := Snapshot{ID: "00000000-0000-7000-8000-000000000096", TenantID: "00000000-0000-7000-8000-000000000097", Generation: 1, State: SnapshotCandidate, CreatedAt: now}
	evidenceFor := func(action string) MutationEvidence {
		return currentMutationTestEvidence(t, candidate.TenantID, candidate.ID, action, now)
	}
	if saved, err := store.SaveCandidate(context.Background(), candidate, evidenceFor(audit.ActionPublicationCandidateCreated)); !errors.Is(err, sentinel) || saved.ID != "" || len(store.snapshots) != 0 {
		t.Fatalf("failed candidate append changed state: %#v snapshots %#v err %v", saved, store.snapshots, err)
	}

	store.snapshots[memorySnapshotKey(candidate.TenantID, candidate.ID)] = candidate
	if activated, err := store.Activate(publicationContext(context.Background(), candidate.TenantID), candidate.TenantID, candidate.ID, now, evidenceFor(audit.ActionPublicationActivated)); !errors.Is(err, sentinel) || activated.ID != "" {
		t.Fatalf("failed activation = %#v, %v", activated, err)
	}
	retained, err := store.Get(publicationContext(context.Background(), candidate.TenantID), candidate.TenantID, candidate.ID)
	if err != nil || retained.State != SnapshotCandidate {
		t.Fatalf("failed activation changed candidate: %#v, %v", retained, err)
	}
	if _, err := store.Active(context.Background(), candidate.TenantID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("failed activation installed an active pointer: %v", err)
	}

	active := candidate
	active.State = SnapshotActive
	store.snapshots[memorySnapshotKey(active.TenantID, active.ID)] = active
	store.active[active.TenantID] = active.ID
	if retired, err := store.Deactivate(context.Background(), active.TenantID, active.ID, now, evidenceFor(audit.ActionPublicationUnpublished)); !errors.Is(err, sentinel) || retired.ID != "" {
		t.Fatalf("failed withdrawal = %#v, %v", retired, err)
	}
	retained, err = store.Active(context.Background(), active.TenantID)
	if err != nil || retained.State != SnapshotActive || retained.ID != active.ID {
		t.Fatalf("failed withdrawal changed active publication: %#v, %v", retained, err)
	}
}

func TestPublicationAuditConstructionFailsClosedAtAttributionAndEventBoundaries(t *testing.T) {
	t.Parallel()

	const tenantID = "00000000-0000-7000-8000-000000000001"
	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	activatedAt := now.Add(time.Second)
	digest := strings.Repeat("a", 64)
	snapshot := Snapshot{
		ID:               "00000000-0000-7000-8000-000000000010",
		TenantID:         tenantID,
		Host:             "catalog.example.com",
		Audience:         AudienceAnonymous,
		Generation:       1,
		State:            SnapshotCandidate,
		ARDProfile:       "ard-test",
		AICatalogProfile: "ai-catalog-test",
		ETag:             "\"" + digest + "\"",
		AICatalogETag:    "\"" + digest + "\"",
		CreatedAt:        now,
		ActivatedAt:      &activatedAt,
	}
	actor := identity.Actor{TenantID: tenantID, RealmID: "00000000-0000-7000-8000-000000000002", MembershipID: "00000000-0000-7000-8000-000000000003", ActorType: identity.ActorHuman}
	ctx := identityfixture.WithActor(context.Background(), actor)
	sink := &recordingPublicationEvidence{}

	if evidence, err := (&Manager{}).candidateEvidence(ctx, snapshot); err != nil || evidence.Audit.ID != "" {
		t.Fatalf("disabled publication evidence = %#v, %v", evidence, err)
	}
	manager := &Manager{ids: id.Generator{}, evidence: sink, host: snapshot.Host, clock: &mutablePublicationClock{now: now}, acknowledgeOutboxAtCreation: true}
	unactivated := snapshot
	unactivated.ActivatedAt = nil
	if _, err := manager.transitionEvidence(ctx, unactivated, audit.ActionPublicationActivated, "Activate candidate", ""); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("transition without activation time returned %v", err)
	}
	if _, err := manager.transitionEvidence(ctx, snapshot, audit.ActionPublicationUnpublished, "Wrong transition", ""); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("unrelated transition action returned %v", err)
	}
	for _, job := range []audit.JobExecution{
		{ID: "not-a-uuid", Kind: "publication.generate", Attempt: 1, Trigger: "manual"},
		{ID: "00000000-0000-7000-8000-000000000020", Kind: "other", Attempt: 1, Trigger: "manual"},
		{ID: "00000000-0000-7000-8000-000000000020", Kind: "publication.generate", Attempt: 0, Trigger: "manual"},
		{ID: "00000000-0000-7000-8000-000000000020", Kind: "publication.generate", Attempt: 1, Trigger: "automatic"},
	} {
		if _, err := manager.candidateEvidence(audit.WithJobExecution(ctx, job), snapshot); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
			t.Fatalf("invalid publication Job attribution %#v returned %v", job, err)
		}
	}
	untrustedInitiator := audit.WithJobExecution(ctx, audit.JobExecution{ID: "00000000-0000-7000-8000-000000000020", Kind: "publication.generate", Attempt: 1, Initiator: "external-subject", Trigger: "manual"})
	if _, err := manager.candidateEvidence(untrustedInitiator, snapshot); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("untrusted publication initiator returned %v", err)
	}

	if _, err := manager.withdrawalEvidence(ctx, tenantID, snapshot, "", WithdrawalCause{}, false); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("incomplete withdrawal cause returned %v", err)
	}
	manager.evidence = nil
	if evidence, err := manager.withdrawalEvidence(ctx, tenantID, snapshot, "", WithdrawalCause{Cause: "explicit_unpublish", OriginatingAction: "publication.unpublish", OriginatingTargetID: snapshot.ID}, false); err != nil || evidence.Audit.ID != "" {
		t.Fatalf("disabled withdrawal evidence = %#v, %v", evidence, err)
	}
	manager.evidence = sink

	if record, err := manager.auditRecord(ctx, tenantID, audit.ActionPublicationCandidateCreated, snapshot.ID, "", func() {}, now); err == nil || record.ID != "" {
		t.Fatalf("unencodable publication facts = %#v, %v", record, err)
	}
	manager.ids = id.Generator{Reader: bytes.NewReader(nil)}
	if record, err := manager.auditRecord(ctx, tenantID, audit.ActionPublicationCandidateCreated, snapshot.ID, "", candidateFacts{}, now); err == nil || record.ID != "" {
		t.Fatalf("publication audit identifier failure = %#v, %v", record, err)
	}
	manager.ids = id.Generator{}
	if record, err := manager.auditRecord(context.Background(), tenantID, audit.ActionPublicationCandidateCreated, snapshot.ID, "", candidateFacts{}, now); catalog.ErrorCodeOf(err) != catalog.CodeForbidden || record.ID != "" {
		t.Fatalf("missing publication actor = %#v, %v", record, err)
	}

	record, err := manager.auditRecord(ctx, tenantID, audit.ActionPublicationActivated, snapshot.ID, "Activate candidate", transitionFacts{Host: snapshot.Host, Generation: 1, ARDProfile: snapshot.ARDProfile, AICatalogProfile: snapshot.AICatalogProfile, ARDDigest: snapshotARDDigest(snapshot), AICatalogDigest: snapshotAICatalogDigest(snapshot)}, activatedAt)
	if err != nil {
		t.Fatal(err)
	}
	invalid := record
	invalid.Action = audit.ActionPublicationRolledBack
	invalid.Reason = ""
	if _, err := manager.publicEvidence(t.Context(), invalid, "publication.activated", snapshot, "sha256:"+digest); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("invalid publication audit record returned %v", err)
	}
	manager.ids = id.Generator{Reader: bytes.NewReader(nil)}
	if _, err := manager.publicEvidence(t.Context(), record, "publication.activated", snapshot, "sha256:"+digest); err == nil {
		t.Fatal("publication outbox identifier failure was accepted")
	}
	manager.ids = id.Generator{}
	if _, err := manager.publicEvidence(t.Context(), record, "internal.unknown", snapshot, "sha256:"+digest); err == nil {
		t.Fatal("unregistered publication event was accepted")
	}
	if etagDigest("\""+digest+"\"") != "sha256:"+digest || etagDigest("\"short\"") != "" {
		t.Fatal("publication ETag digest normalization is inconsistent")
	}
}

func TestPublicationMutationEvidenceIsBoundToItsExactSnapshot(t *testing.T) {
	t.Parallel()

	tenantID := "00000000-0000-7000-8000-000000000001"
	targetID := "00000000-0000-7000-8000-000000000010"
	otherID := "00000000-0000-7000-8000-000000000011"
	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	digest := "sha256:" + strings.Repeat("a", 64)
	host := "catalog.example.com"
	principal := audit.Principal{ID: "00000000-0000-7000-8000-000000000003", Type: audit.PrincipalCatalogMembership, ActorType: "human", RealmID: "00000000-0000-7000-8000-000000000002"}
	recordFor := func(idValue, action, reason string, facts map[string]any) audit.Record {
		record, err := audit.NewRecord(idValue, tenantID, principal, action, "publication_snapshot", targetID, reason, audit.Execution{}, facts, now)
		if err != nil {
			t.Fatal(err)
		}
		record.AfterDigest = digest
		if err := audit.ValidateNewRecord(record); err != nil {
			t.Fatal(err)
		}
		return record
	}
	outboxFor := func(idValue, publicAction string, record audit.Record) *catalog.OutboxEvent {
		data, err := events.MarshalReferenceWithHost(publicAction, record.TargetType, targetID, record.AfterDigest, host)
		if err != nil {
			t.Fatal(err)
		}
		return &catalog.OutboxEvent{ID: idValue, TenantID: tenantID, Type: events.CanonicalType(publicAction), Subject: targetID, Data: data, CreatedAt: record.At}
	}
	base := MutationEvidence{
		Audit:  audit.Record{ID: "00000000-0000-7000-8000-000000000012", TenantID: tenantID, TargetID: targetID},
		Outbox: &catalog.OutboxEvent{TenantID: tenantID, Subject: targetID},
	}
	transitionFacts := map[string]any{"host": host, "generation": 1, "ardProfile": "ard-v1", "aiCatalogProfile": "ai-catalog-v1", "ardDigest": digest, "aiCatalogDigest": digest}
	activatedRecord := recordFor("00000000-0000-7000-8000-000000000012", audit.ActionPublicationActivated, "Activate reviewed publication", transitionFacts)
	versioned := MutationEvidence{Audit: activatedRecord, Outbox: outboxFor("00000000-0000-7000-8000-000000000013", "publication.activated", activatedRecord)}
	rolledBackRecord := recordFor("00000000-0000-7000-8000-000000000014", audit.ActionPublicationRolledBack, "Restore reviewed publication", transitionFacts)
	rolledBack := MutationEvidence{Audit: rolledBackRecord, Outbox: outboxFor("00000000-0000-7000-8000-000000000015", "publication.activated", rolledBackRecord)}
	candidateRecord := recordFor("00000000-0000-7000-8000-000000000016", audit.ActionPublicationCandidateCreated, "", map[string]any{"host": host, "audience": "anonymous", "generation": 1, "ardProfile": "ard-v1", "aiCatalogProfile": "ai-catalog-v1", "ardDigest": digest, "aiCatalogDigest": digest, "entryCount": 0, "exclusionCount": 0, "trigger": "direct"})
	candidate := MutationEvidence{Audit: candidateRecord}
	wrongType := *versioned.Outbox
	wrongType.Type = catalog.CanonicalEventType(audit.ActionPublicationUnpublished)
	wrongData := *versioned.Outbox
	wrongData.Data = json.RawMessage(`{"action":"publication.activated","targetType":"publication_snapshot","targetId":"` + targetID + `","digest":"` + digest + `","host":"other.example.com"}`)
	wrongTime := *versioned.Outbox
	wrongTime.CreatedAt = now.Add(time.Second)
	wrongPublishedTime := *versioned.Outbox
	publishedAt := now.Add(time.Second)
	wrongPublishedTime.PublishedAt = &publishedAt
	internal := *versioned.Outbox
	internal.Internal = true
	leased := *versioned.Outbox
	leased.LeaseOwner = "worker"
	for _, testCase := range []struct {
		name     string
		evidence []MutationEvidence
		valid    bool
	}{
		{name: "absent", valid: true},
		{name: "empty placeholder", evidence: []MutationEvidence{{}}, valid: true},
		{name: "unversioned bundle", evidence: []MutationEvidence{base}},
		{name: "valid versioned bundle", evidence: []MutationEvidence{versioned}, valid: true},
		{name: "valid rollback mapping", evidence: []MutationEvidence{rolledBack}, valid: true},
		{name: "valid audit-only candidate", evidence: []MutationEvidence{candidate}, valid: true},
		{name: "ambiguous", evidence: []MutationEvidence{{}, {}}},
		{name: "orphan notification", evidence: []MutationEvidence{{Outbox: &catalog.OutboxEvent{TenantID: tenantID, Subject: targetID}}}},
		{name: "audit tenant", evidence: []MutationEvidence{{Audit: audit.Record{ID: base.Audit.ID, TenantID: otherID, TargetID: targetID}}}},
		{name: "audit target", evidence: []MutationEvidence{{Audit: audit.Record{ID: base.Audit.ID, TenantID: tenantID, TargetID: otherID}}}},
		{name: "notification tenant", evidence: []MutationEvidence{{Audit: base.Audit, Outbox: &catalog.OutboxEvent{TenantID: otherID, Subject: targetID}}}},
		{name: "notification target", evidence: []MutationEvidence{{Audit: base.Audit, Outbox: &catalog.OutboxEvent{TenantID: tenantID, Subject: otherID}}}},
		{name: "notification type", evidence: []MutationEvidence{{Audit: versioned.Audit, Outbox: &wrongType}}},
		{name: "notification data", evidence: []MutationEvidence{{Audit: versioned.Audit, Outbox: &wrongData}}},
		{name: "notification time", evidence: []MutationEvidence{{Audit: versioned.Audit, Outbox: &wrongTime}}},
		{name: "notification published time", evidence: []MutationEvidence{{Audit: versioned.Audit, Outbox: &wrongPublishedTime}}},
		{name: "internal notification", evidence: []MutationEvidence{{Audit: versioned.Audit, Outbox: &internal}}},
		{name: "leased notification", evidence: []MutationEvidence{{Audit: versioned.Audit, Outbox: &leased}}},
		{name: "missing required notification", evidence: []MutationEvidence{{Audit: versioned.Audit}}},
		{name: "notification for audit-only action", evidence: []MutationEvidence{{Audit: candidate.Audit, Outbox: versioned.Outbox}}},
		{name: "unrelated audit action", evidence: []MutationEvidence{{Audit: audit.Record{FormatVersion: activatedRecord.FormatVersion, ID: base.Audit.ID, TenantID: tenantID, Action: "namespace.created", TargetID: targetID}}}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			err := ValidateMutationEvidence(tenantID, targetID, testCase.evidence)
			if (err == nil) != testCase.valid {
				t.Fatalf("evidence validation = %v, valid = %t", err, testCase.valid)
			}
		})
	}

	for action, publicAction := range map[string]string{
		audit.ActionPublicationActivated:   "publication.activated",
		audit.ActionPublicationRolledBack:  "publication.activated",
		audit.ActionPublicationUnpublished: "publication.unpublished",
	} {
		if got := publicationNotificationType(action); got != catalog.CanonicalEventType(publicAction) {
			t.Errorf("notification type for %q = %q", action, got)
		}
		if got := publicationNotificationAction(action); got != publicAction {
			t.Errorf("notification action for %q = %q", action, got)
		}
	}
	if publicationNotificationType(audit.ActionPublicationCandidateCreated) != "" || publicationNotificationType("namespace.created") != "" || publicationNotificationAction(audit.ActionPublicationCandidateCreated) != "" {
		t.Fatal("an audit-only or unrelated action entered the publication notification classification")
	}
	if err := ValidateMutationEvidence(tenantID, targetID, []MutationEvidence{rolledBack}, audit.ActionPublicationActivated); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("rollback evidence was accepted for an activation store operation: %v", err)
	}
}

func TestPublicationStoreRestoresBothSidesOfFailedPointerTransitions(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("audit unavailable")
	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	tenantID := "00000000-0000-7000-8000-000000000021"
	priorID := "00000000-0000-7000-8000-000000000022"
	targetID := "00000000-0000-7000-8000-000000000023"
	evidenceFor := func(idValue, action string) MutationEvidence {
		return currentMutationTestEvidence(t, tenantID, idValue, action, now)
	}

	t.Run("activation", func(t *testing.T) {
		store := NewMemoryStore(&recordingPublicationEvidence{err: sentinel})
		prior := Snapshot{ID: priorID, TenantID: tenantID, Generation: 1, State: SnapshotActive, CreatedAt: now}
		target := Snapshot{ID: targetID, TenantID: tenantID, Generation: 2, State: SnapshotCandidate, CreatedAt: now}
		store.snapshots[memorySnapshotKey(tenantID, priorID)] = prior
		store.snapshots[memorySnapshotKey(tenantID, targetID)] = target
		store.active[tenantID] = priorID
		if result, err := store.Activate(publicationContext(context.Background(), tenantID), tenantID, targetID, now, evidenceFor(targetID, audit.ActionPublicationActivated)); !errors.Is(err, sentinel) || result.ID != "" {
			t.Fatalf("failed activation = %#v, %v", result, err)
		}
		if store.active[tenantID] != priorID || store.snapshots[memorySnapshotKey(tenantID, priorID)].State != SnapshotActive || store.snapshots[memorySnapshotKey(tenantID, targetID)].State != SnapshotCandidate {
			t.Fatalf("failed activation changed publication pointer: active %q snapshots %#v", store.active[tenantID], store.snapshots)
		}
	})

	for _, withPrior := range []bool{false, true} {
		t.Run(fmt.Sprintf("rollback prior %t", withPrior), func(t *testing.T) {
			store := NewMemoryStore(&recordingPublicationEvidence{err: sentinel})
			target := Snapshot{ID: targetID, TenantID: tenantID, Generation: 1, State: SnapshotRetired, CreatedAt: now}
			store.snapshots[memorySnapshotKey(tenantID, targetID)] = target
			if withPrior {
				prior := Snapshot{ID: priorID, TenantID: tenantID, Generation: 2, State: SnapshotActive, CreatedAt: now}
				store.snapshots[memorySnapshotKey(tenantID, priorID)] = prior
				store.active[tenantID] = priorID
			}
			if result, err := store.Rollback(publicationContext(context.Background(), tenantID), tenantID, targetID, now, evidenceFor(targetID, audit.ActionPublicationRolledBack)); !errors.Is(err, sentinel) || result.ID != "" {
				t.Fatalf("failed rollback = %#v, %v", result, err)
			}
			if store.snapshots[memorySnapshotKey(tenantID, targetID)].State != SnapshotRetired {
				t.Fatalf("failed rollback activated target: %#v", store.snapshots)
			}
			if withPrior {
				if store.active[tenantID] != priorID || store.snapshots[memorySnapshotKey(tenantID, priorID)].State != SnapshotActive {
					t.Fatalf("failed rollback changed prior active snapshot: active %q snapshots %#v", store.active[tenantID], store.snapshots)
				}
			} else if store.active[tenantID] != "" {
				t.Fatalf("failed rollback created an active pointer: %q", store.active[tenantID])
			}
		})
	}
}

func currentMutationTestEvidence(t *testing.T, tenantID, targetID, action string, at time.Time) MutationEvidence {
	t.Helper()
	digest := "sha256:" + strings.Repeat("a", 64)
	host := "catalog.example.com"
	facts := map[string]any{"host": host, "generation": 1, "ardProfile": "ard-v1", "aiCatalogProfile": "ai-catalog-v1", "ardDigest": digest, "aiCatalogDigest": digest}
	if action == audit.ActionPublicationCandidateCreated {
		facts["audience"] = "anonymous"
		facts["entryCount"] = 0
		facts["exclusionCount"] = 0
		facts["trigger"] = "direct"
	}
	if action == audit.ActionPublicationUnpublished {
		facts = map[string]any{"host": host, "generation": 1, "cause": "explicit_unpublish", "originatingAction": "publication.unpublish", "originatingTargetId": targetID}
	}
	reason := "reviewed publication transition"
	if action == audit.ActionPublicationCandidateCreated {
		reason = ""
	}
	record, err := audit.NewRecord("00000000-0000-7000-8000-000000000098", tenantID, audit.Principal{ID: "catalog-api", Type: audit.PrincipalServiceExecutor}, action, "publication_snapshot", targetID, reason, audit.Execution{}, facts, at)
	if err != nil {
		t.Fatal(err)
	}
	record.AfterDigest = digest
	result := MutationEvidence{Audit: record}
	if action != audit.ActionPublicationCandidateCreated {
		publicAction := action
		if action == audit.ActionPublicationRolledBack {
			publicAction = audit.ActionPublicationActivated
		}
		data, err := events.MarshalReferenceWithHost(publicAction, record.TargetType, targetID, digest, host)
		if err != nil {
			t.Fatal(err)
		}
		result.Outbox = &catalog.OutboxEvent{ID: "00000000-0000-7000-8000-000000000099", TenantID: tenantID, Type: events.CanonicalType(publicAction), Subject: targetID, Data: data, CreatedAt: at}
	}
	if err := ValidateMutationEvidence(tenantID, targetID, []MutationEvidence{result}); err != nil {
		t.Fatal(err)
	}
	return result
}
