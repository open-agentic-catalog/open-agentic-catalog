package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	testTenant     = "00000000-0000-7000-8000-000000000001"
	testMembership = "00000000-0000-7000-8000-000000000002"
	testRealm      = "00000000-0000-7000-8000-000000000003"
)

func TestExecutionContextActionRegistryAndDigestHelpers(t *testing.T) {
	if execution := ExecutionFrom(context.Background()); execution != (Execution{}) {
		t.Fatalf("empty execution = %#v", execution)
	}
	plainContext := context.Background()
	if WithJobTrigger(plainContext, "manual") != plainContext {
		t.Fatal("a Job trigger was added without Job execution context")
	}
	initial := Execution{RequestID: "request-1", AuthorizationDigest: "sha256:" + strings.Repeat("a", 64)}
	ctx := WithExecution(context.Background(), initial)
	commandID := "00000000-0000-7000-8000-000000000004"
	withCommand := ExecutionFrom(WithCommandID(ctx, commandID))
	if withCommand.RequestID != initial.RequestID || withCommand.AuthorizationDigest != initial.AuthorizationDigest || withCommand.CommandID != commandID || ExecutionFrom(ctx).CommandID != "" {
		t.Fatalf("execution context = %#v / original %#v", withCommand, ExecutionFrom(ctx))
	}
	job := JobExecution{ID: "00000000-0000-7000-8000-000000000005", Kind: "source.sync", Attempt: 2, Initiator: testMembership, Trigger: "manual"}
	jobContext := WithJobExecution(ctx, job)
	storedJob, ok := JobExecutionFrom(jobContext)
	if !ok || storedJob != job {
		t.Fatalf("Job execution context = %#v, %t", storedJob, ok)
	}
	if _, ok := JobExecutionFrom(ctx); ok {
		t.Fatal("Job execution mutated the parent context")
	}
	automaticContext := WithJobTrigger(jobContext, "automatic")
	automaticJob, ok := JobExecutionFrom(automaticContext)
	if !ok || automaticJob.Trigger != "automatic" || automaticJob.ID != job.ID || automaticJob.Kind != job.Kind || automaticJob.Attempt != job.Attempt || automaticJob.Initiator != job.Initiator || storedJob.Trigger != "manual" {
		t.Fatalf("updated Job context = %#v / original %#v", automaticJob, storedJob)
	}
	actions := Actions()
	if len(actions) != len(actionRules) || !slices.IsSorted(actions) {
		t.Fatalf("actions = %#v", actions)
	}
	for _, action := range actions {
		if !RegisteredAction(action) {
			t.Fatalf("listed action %q is not registered", action)
		}
	}
	if DigestBytes(nil) != "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("empty digest = %q", DigestBytes(nil))
	}
	if got := (&RangeTooLargeError{Reason: "row limit exceeded"}).Error(); !strings.Contains(got, "row limit exceeded") {
		t.Fatalf("range error = %q", got)
	}

}

func TestNewRecordValidatesStableAttributionAndReason(t *testing.T) {
	at := time.Date(2026, time.September, 3, 12, 0, 0, 123456789, time.FixedZone("source", 3600))
	execution := Execution{RequestID: "request-1", AuthorizationDigest: "sha256:" + strings.Repeat("a", 64), CommandID: "00000000-0000-7000-8000-000000000004"}
	record, err := NewRecord("00000000-0000-7000-8000-000000000005", testTenant, Principal{ID: testMembership, Type: PrincipalCatalogMembership, ActorType: "human", RealmID: testRealm}, "namespace.updated", "namespace", testTenant, "  Reviewed recovery range  ", execution, map[string]any{"count": json.Number("9007199254740993")}, at)
	if err != nil {
		t.Fatal(err)
	}
	if record.FormatVersion == nil || *record.FormatVersion != 1 || record.Actor != testMembership || record.Reason != "Reviewed recovery range" || record.RequestID != execution.RequestID || record.Details["principalType"] != PrincipalCatalogMembership || record.At.Location() != time.UTC || record.At.Nanosecond()%1000 != 0 {
		t.Fatalf("record = %#v", record)
	}
	facts, ok := record.Details["facts"].(map[string]any)
	if !ok {
		t.Fatalf("facts = %#v", record.Details["facts"])
	}
	if facts["count"] != json.Number("9007199254740993") {
		t.Fatalf("lossless facts = %#v", facts)
	}
	if err := ValidateNewRecord(record); err != nil {
		t.Fatal(err)
	}
	wrongTargetType := cloneRecord(t, record)
	wrongTargetType.TargetType = "publisher"
	if err := ValidateNewRecord(wrongTargetType); err == nil {
		t.Fatal("existing Catalog action accepted another target type")
	}
	wrongTargetID := cloneRecord(t, record)
	wrongTargetID.TargetID = "not-a-uuid"
	if err := ValidateNewRecord(wrongTargetID); err == nil {
		t.Fatal("existing Catalog action accepted a non-UUID target")
	}
}

func TestRecordValidationRejectsUnsafeShapes(t *testing.T) {
	at := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	valid, err := NewRecord("00000000-0000-7000-8000-000000000005", testTenant, Principal{ID: testMembership, Type: PrincipalCatalogMembership, ActorType: "service", RealmID: testRealm}, "namespace.updated", "namespace", testTenant, "reviewed", Execution{}, nil, at)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*Record){
		"legacy version":   func(record *Record) { record.FormatVersion = nil },
		"unknown version":  func(record *Record) { value := 2; record.FormatVersion = &value },
		"invalid event ID": func(record *Record) { record.ID = "not-a-uuid" },
		"empty actor":      func(record *Record) { record.Actor = "" },
		"unknown action":   func(record *Record) { record.Action = "audit.unknown" },
		"empty target type": func(record *Record) {
			record.TargetType = ""
		},
		"empty target ID": func(record *Record) { record.TargetID = "" },
		"unnormalized reason": func(record *Record) {
			record.Reason = " reviewed "
		},
		"invalid digest":  func(record *Record) { record.AfterDigest = "sha256:no" },
		"oversized actor": func(record *Record) { record.Actor = strings.Repeat("a", MaxActorBytes+1) },
		"bad request ID":  func(record *Record) { record.RequestID = "line\nbreak" },
		"zero occurrence": func(record *Record) { record.At = time.Time{} },
		"non-UTC occurrence": func(record *Record) {
			record.At = record.At.In(time.FixedZone("offset", 3600))
		},
		"sub-microsecond occurrence": func(record *Record) {
			record.At = record.At.Add(time.Nanosecond)
		},
		"missing details": func(record *Record) { record.Details = nil },
		"unknown detail":  func(record *Record) { record.Details["token"] = "secret" },
		"missing realm":   func(record *Record) { delete(record.Details, "realmId") },
		"wrong principal": func(record *Record) { record.Details["principalType"] = "external_subject" },
		"database field on member": func(record *Record) {
			record.Details["currentUser"] = "catalog_owner"
		},
		"controlled fact": func(record *Record) {
			record.Details["facts"] = map[string]any{"outcome": "accepted\nthen changed"}
		},
		"controlled fact name": func(record *Record) {
			record.Details["facts"] = map[string]any{"bad\nname": "accepted"}
		},
		"wrong authorization type": func(record *Record) {
			record.Details["authorizationDigest"] = true
		},
		"wrong command type": func(record *Record) { record.Details["commandId"] = 42 },
		"facts are not object": func(record *Record) {
			record.Details["facts"] = []any{"value"}
		},
		"too many array facts": func(record *Record) {
			record.Details["facts"] = map[string]any{"values": make([]any, MaxDetailsArrayEntries+1)}
		},
		"invalid UTF-8 fact": func(record *Record) {
			record.Details["facts"] = map[string]any{"value": string([]byte{0xff})}
		},
		"unsupported detail value": func(record *Record) {
			record.Details["facts"] = map[string]any{"value": func() {}}
		},
		"too many facts": func(record *Record) {
			facts := map[string]any{}
			for i := 0; i <= MaxDetailsMembers; i++ {
				facts[fmt.Sprint(i)] = i
			}
			record.Details["facts"] = facts
		},
		"too deeply nested": func(record *Record) {
			value := any("end")
			for range MaxDetailsDepth + 1 {
				value = map[string]any{"next": value}
			}
			record.Details["facts"] = map[string]any{"root": value}
		},
	}
	serviceRecord, err := NewRecord("00000000-0000-7000-8000-000000000006", testTenant, Principal{ID: "catalog-worker", Type: PrincipalServiceExecutor}, "namespace.created", "namespace", testTenant, "", Execution{}, nil, at)
	if err != nil {
		t.Fatal(err)
	}
	serviceRecord.Details["realmId"] = testRealm
	if err := ValidateNewRecord(serviceRecord); err == nil {
		t.Fatal("service principal accepted a Catalog-membership realm")
	}
	databaseRecord, err := NewRecord("00000000-0000-7000-8000-000000000007", testTenant, Principal{ID: "catalog_migrator", Type: PrincipalDatabaseLogin}, "namespace.created", "namespace", testTenant, "", Execution{CurrentUser: "catalog_owner", OperatorLabel: "operator-one"}, nil, at)
	if err != nil {
		t.Fatal(err)
	}
	databaseRecord.Details["authorizationDigest"] = "sha256:" + strings.Repeat("a", 64)
	if err := ValidateNewRecord(databaseRecord); err == nil {
		t.Fatal("database principal accepted HTTP authorization attribution")
	}
	invalidService := cloneRecord(t, serviceRecord)
	delete(invalidService.Details, "realmId")
	invalidService.Actor = "unsafe service"
	if err := ValidateNewRecord(invalidService); err == nil {
		t.Fatal("service principal accepted an unsafe identifier")
	}
	missingDatabaseAuthority := cloneRecord(t, databaseRecord)
	delete(missingDatabaseAuthority.Details, "authorizationDigest")
	delete(missingDatabaseAuthority.Details, "currentUser")
	if err := ValidateNewRecord(missingDatabaseAuthority); err == nil {
		t.Fatal("database principal without effective authority was accepted")
	}
	invalidOperator := cloneRecord(t, databaseRecord)
	delete(invalidOperator.Details, "authorizationDigest")
	invalidOperator.Details["operatorLabel"] = "operator\nlabel"
	if err := ValidateNewRecord(invalidOperator); err == nil {
		t.Fatal("database principal accepted an unsafe operator label")
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			copyRecord := cloneRecord(t, valid)
			mutate(&copyRecord)
			if err := ValidateNewRecord(copyRecord); err == nil {
				t.Fatalf("unsafe record accepted: %#v", copyRecord)
			}
		})
	}
	for name, reason := range map[string]string{"empty": "", "control": "bad\nreason", "too long": strings.Repeat("x", MaxReasonRunes+1)} {
		t.Run("reason "+name, func(t *testing.T) {
			if _, err := NormalizeReason("audit.exported", reason); err == nil {
				t.Fatalf("unsafe reason %q accepted", name)
			}
		})
	}
	if _, err := NormalizeReason("namespace.created", ""); err != nil {
		t.Fatalf("optional empty reason rejected: %v", err)
	}
	if _, err := NormalizeRequiredReason(" \t"); err == nil {
		t.Fatal("an empty required reason was accepted")
	}
	type structuredFact struct {
		State string `json:"state"`
	}
	structured := cloneRecord(t, valid)
	structured.Details["facts"] = map[string]any{"values": []any{nil, true, json.Number("1"), structuredFact{State: "safe"}}}
	if err := ValidateNewRecord(structured); err != nil {
		t.Fatalf("bounded structured facts rejected: %v", err)
	}
	if _, err := NewRecord("00000000-0000-7000-8000-000000000008", testTenant, Principal{ID: "catalog-worker", Type: PrincipalServiceExecutor}, "namespace.created", "namespace", testTenant, "", Execution{}, map[string]any{"unsupported": func() {}}, at); err == nil {
		t.Fatal("unencodable facts were accepted")
	}
}

func TestClosedFactRecordAndEncoderBoundariesFailSafely(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	digest := "sha256:" + strings.Repeat("a", 64)
	targetID := "00000000-0000-7000-8000-000000000010"
	principal := Principal{ID: testMembership, Type: PrincipalCatalogMembership, ActorType: "human", RealmID: testRealm}
	facts := map[string]any{"eventCount": 0, "eventsDigest": digest}
	record, err := NewRecord("00000000-0000-7000-8000-000000000011", testTenant, principal, ActionAuditEventsRead, "audit_event_page", targetID, "", Execution{}, facts, at)
	if err != nil {
		t.Fatal(err)
	}

	for name, candidate := range map[string]map[string]any{
		"unknown": {"eventCount": 0, "eventsDigest": digest, "private": true},
		"missing": {"eventCount": 0},
		"type":    {"eventCount": "zero", "eventsDigest": digest},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateActionFacts(ActionAuditEventsRead, candidate); err == nil {
				t.Fatal("invalid closed facts were accepted")
			}
		})
	}
	withoutFacts := cloneRecord(t, record)
	delete(withoutFacts.Details, "facts")
	if err := ValidateNewRecord(withoutFacts); err == nil {
		t.Fatal("action-specific facts were optional")
	}

	wrongType := cloneRecord(t, record)
	wrongType.TargetType = "audit_range"
	if err := ValidateNewRecord(wrongType); err == nil {
		t.Fatal("action-specific target type was not enforced")
	}
	wrongTarget := cloneRecord(t, record)
	wrongTarget.TargetID = "not-a-uuid"
	if err := ValidateNewRecord(wrongTarget); err == nil {
		t.Fatal("action-specific target identifier was not enforced")
	}
	invalidRecord := cloneRecord(t, record)
	invalidRecord.Action = "audit.unknown"
	if _, err := EncodeRecords([]Record{invalidRecord}); err == nil {
		t.Fatal("invalid version-1 record was encoded")
	}
	legacy := Record{ID: targetID, Details: map[string]any{"unsupported": func() {}}, At: at}
	if _, err := EncodeRecords([]Record{legacy}); err == nil {
		t.Fatal("unencodable legacy record was accepted")
	}
	members := 0
	if err := validateValue(make(chan int), 0, &members); err == nil {
		t.Fatal("unsupported fact value was accepted")
	}
	members = 0
	if err := validateValue([]any{make(chan int)}, 0, &members); err == nil {
		t.Fatal("unsupported nested fact value was accepted")
	}
	if empty, err := Facts(nil); err != nil || len(empty) != 0 {
		t.Fatalf("empty typed facts = %#v, %v", empty, err)
	}
	for _, value := range []any{[]int{1}, make(chan int)} {
		if _, err := Facts(value); err == nil {
			t.Fatalf("non-object or unencodable typed facts accepted: %T", value)
		}
	}
	typed, err := Facts(struct {
		Count int64 `json:"count"`
	}{Count: 9007199254740993})
	if err != nil || typed["count"] != json.Number("9007199254740993") {
		t.Fatalf("typed facts lost integer precision: %#v, %v", typed, err)
	}
}

func TestCoveredActionFactsHaveClosedSemanticContracts(t *testing.T) {
	at := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	digest := "sha256:" + strings.Repeat("a", 64)
	otherDigest := "sha256:" + strings.Repeat("b", 64)
	targetID := "00000000-0000-7000-8000-000000000010"
	otherID := "00000000-0000-7000-8000-000000000011"
	principal := Principal{ID: testMembership, Type: PrincipalCatalogMembership, ActorType: "human", RealmID: testRealm}
	tests := []struct {
		action     string
		targetType string
		targetID   string
		reason     string
		facts      map[string]any
	}{
		{ActionAuditEventsRead, "audit_event_page", targetID, "", map[string]any{"eventCount": 0, "eventsDigest": digest}},
		{ActionAuditExported, "audit_range", targetID, "export evidence", map[string]any{"rowCount": 1, "eventsDigest": digest, "occurredFrom": at.Add(-time.Hour).Format(time.RFC3339Nano), "occurredBefore": at.Format(time.RFC3339Nano)}},
		{ActionIdempotencyReservationReleased, "idempotency_reservation", targetID, "release interrupted command", map[string]any{"reservationId": targetID, "protocolVersion": 2}},
		{ActionSourceInspected, "source_inspection", targetID, "", map[string]any{"outcome": "succeeded", "originClass": "named_private:catalog-net", "policyDigest": digest, "configurationDigest": otherDigest, "inspectionDigest": digest, "expiresAt": at.Add(time.Hour).Format(time.RFC3339Nano)}},
		{ActionSourceChangePreviewCreated, "source_change_preview", targetID, "", map[string]any{"sourceId": otherID, "sourceRevision": 1, "intent": "remove", "configurationDigest": digest, "observationSetDigest": otherDigest, "previewDigest": digest, "currentMatched": 2, "proposedMatched": 0, "excludedObservations": 2, "sendsQueriesUpstream": false, "expiresAt": at.Add(time.Hour).Format(time.RFC3339Nano)}},
		{ActionSourceConnected, "federation_source", targetID, "approve source", map[string]any{"inspectionId": otherID, "sourceRevision": 1, "configurationDigest": digest, "retrievalMode": "hybrid", "updateMode": "automatic", "policyDigest": otherDigest}},
		{ActionSourceRevisionActivated, "federation_source", targetID, "approve revision", map[string]any{"previewId": otherID, "sourceRevision": 2, "beforeConfigurationDigest": digest, "afterConfigurationDigest": otherDigest, "retrievalMode": "indexed", "updateMode": "manual_review", "policyDigest": digest, "trigger": "reviewed_update"}},
		{ActionSourceTested, "federation_source", targetID, "", map[string]any{"beforeRevision": 1, "afterRevision": 2, "outcome": "succeeded", "stateChanged": true, "httpClass": "2xx", "policyDigest": digest, "bodyDigest": otherDigest, "capabilities": []string{"list", "search"}}},
		{ActionSourcePaused, "federation_source", targetID, "pause source", map[string]any{"beforeState": "active", "afterState": "paused", "beforeRevision": 2, "afterRevision": 3}},
		{ActionSourceResumed, "federation_source", targetID, "resume source", map[string]any{"beforeState": "paused", "afterState": "active", "beforeRevision": 3, "afterRevision": 4}},
		{ActionSourceRemoved, "federation_source", targetID, "remove source", map[string]any{"beforeState": "active", "afterState": "removed", "beforeRevision": 4, "afterRevision": 5, "previewId": otherID, "previewDigest": digest, "observationSetDigest": otherDigest}},
		{ActionSourceSyncRequested, "federation_source", targetID, "", map[string]any{"sourceRevision": 5, "jobId": otherID, "trigger": "manual", "deduplicated": false}},
		{ActionSourceSyncCompleted, "source_snapshot", targetID, "", map[string]any{"sourceId": otherID, "sourceRevision": 5, "state": "complete", "policyDigest": digest, "memberCount": 3, "added": 1, "updated": 2, "withdrawn": 0, "jobId": targetID, "jobKind": "source.sync", "jobAttempt": 1, "initiator": InitiatorSourceMaintainer, "trigger": "automatic"}},
		{ActionSourceSyncFailed, "source_snapshot", targetID, "", map[string]any{"sourceId": otherID, "sourceRevision": 5, "state": "failed", "policyDigest": digest, "memberCount": 0, "added": 0, "updated": 0, "withdrawn": 0, "failureClass": "dependency", "jobId": targetID, "jobKind": "source.sync", "jobAttempt": 2, "trigger": "manual"}},
		{ActionPublicationCandidateCreated, "publication_snapshot", targetID, "", map[string]any{"host": "catalog.example.com", "audience": "anonymous", "generation": 1, "ardProfile": "ard-v1", "aiCatalogProfile": "ai-v1", "ardDigest": digest, "aiCatalogDigest": otherDigest, "entryCount": 2, "exclusionCount": 1, "trigger": "direct"}},
		{ActionPublicationCandidateCreated, "publication_snapshot", targetID, "", map[string]any{"host": "catalog.example.com", "audience": "anonymous", "generation": 1, "ardProfile": "ard-v1", "aiCatalogProfile": "ai-v1", "ardDigest": digest, "aiCatalogDigest": otherDigest, "entryCount": 2, "exclusionCount": 1, "trigger": "manual", "jobId": targetID, "jobKind": "publication.generate", "jobAttempt": 1, "initiator": otherID}},
		{ActionPublicationActivated, "publication_snapshot", targetID, "activate candidate", map[string]any{"host": "catalog.example.com", "generation": 1, "ardProfile": "ard-v1", "aiCatalogProfile": "ai-v1", "ardDigest": digest, "aiCatalogDigest": otherDigest}},
		{ActionPublicationRolledBack, "publication_snapshot", targetID, "restore candidate", map[string]any{"host": "catalog.example.com", "generation": 1, "replacedSnapshotId": otherID, "ardProfile": "ard-v1", "aiCatalogProfile": "ai-v1", "ardDigest": digest, "aiCatalogDigest": otherDigest}},
		{ActionPublicationUnpublished, "publication_snapshot", targetID, "", map[string]any{"host": "catalog.example.com", "generation": 1, "cause": "release_revoked", "originatingAction": "listing_release.revoked", "originatingTargetId": otherID, "ardDigest": digest, "aiCatalogDigest": otherDigest}},
		{ActionJobCancellationRequested, "job", targetID, "cancel work", map[string]any{"kind": "source.sync", "beforeState": "queued", "afterState": "cancelled", "cancellationRequested": false}},
		{ActionJobRetried, "job", targetID, "retry work", map[string]any{"kind": "source.sync", "beforeState": "failed", "afterState": "queued", "parentJobId": targetID, "replacementJobId": otherID, "failureClass": "dependency"}},
		{ActionSearchProjectionRebuilt, "catalog_search_projection", targetID, "repair search", map[string]any{"selectedFirstId": targetID, "selectedLastId": otherID, "processed": 2, "hasMore": false, "beforeGeneration": 5, "afterGeneration": 7}},
		{ActionSearchProjectionRebuilt, "catalog_search_projection", targetID, "legacy repair evidence", map[string]any{"selectedFirstId": targetID, "selectedLastId": otherID, "processed": 2, "hasMore": false, "beforeGeneration": 5, "afterGeneration": 7}},
		{ActionWebhookSecretsReencrypted, "webhook_secret_batch", targetID, "replace encryption key", map[string]any{"firstId": targetID, "lastId": otherID, "changed": 1, "verified": 2}},
		{ActionVersionRevisionRead, "version_revision", targetID, "", map[string]any{"contentDigest": digest}},
		{ActionCatalogDocumentRead, "catalog_document", strings.Repeat("c", 64), "", map[string]any{"contentDigest": digest, "sizeBytes": 10}},
		{ActionSourceInspectionRead, "source_inspection", targetID, "", map[string]any{"contentDigest": digest}},
		{ActionRawObservationRead, "observation", targetID, "", map[string]any{"contentDigest": digest, "sizeBytes": 12}},
		{ActionFederationLiveSearchDiagnostic, "federation_live_search", targetID, "investigate source", map[string]any{"contactedSourceIds": []string{targetID, otherID}, "outcome": "partial", "resultCount": 1, "diagnosticCount": 1, "partial": true, "policyDigests": []string{digest, otherDigest}}},
	}
	for _, testCase := range tests {
		t.Run(testCase.action, func(t *testing.T) {
			if _, err := NewRecord(targetID, testTenant, principal, testCase.action, testCase.targetType, testCase.targetID, testCase.reason, Execution{}, testCase.facts, at); err != nil {
				t.Fatalf("valid action facts rejected: %v", err)
			}
		})
	}

	rejections := []struct {
		name       string
		action     string
		targetType string
		targetID   string
		reason     string
		facts      map[string]any
	}{
		{"negative audit page count", ActionAuditEventsRead, "audit_event_page", targetID, "", map[string]any{"eventCount": -1, "eventsDigest": digest}},
		{"reversed audit export range", ActionAuditExported, "audit_range", targetID, "export evidence", map[string]any{"rowCount": 1, "eventsDigest": digest, "occurredFrom": at.Format(time.RFC3339Nano), "occurredBefore": at.Add(-time.Hour).Format(time.RFC3339Nano)}},
		{"oversized audit export count", ActionAuditExported, "audit_range", targetID, "export evidence", map[string]any{"rowCount": MaxExportRows + 1, "eventsDigest": digest, "occurredFrom": at.Add(-time.Hour).Format(time.RFC3339Nano), "occurredBefore": at.Format(time.RFC3339Nano)}},
		{"repair protocol", ActionIdempotencyReservationReleased, "idempotency_reservation", targetID, "repair", map[string]any{"reservationId": targetID, "protocolVersion": 1}},
		{"inspection completion", ActionSourceInspected, "source_inspection", targetID, "", map[string]any{"outcome": "transport_error", "originClass": "public", "policyDigest": digest, "configurationDigest": digest, "inspectionDigest": digest, "expiresAt": at.Format(time.RFC3339Nano)}},
		{"connected source revision", ActionSourceConnected, "federation_source", targetID, "connect source", map[string]any{"inspectionId": otherID, "sourceRevision": 2, "configurationDigest": digest, "retrievalMode": "indexed", "updateMode": "manual_review", "policyDigest": otherDigest}},
		{"unchanged source configuration", ActionSourceRevisionActivated, "federation_source", targetID, "update source", map[string]any{"previewId": otherID, "sourceRevision": 2, "beforeConfigurationDigest": digest, "afterConfigurationDigest": digest, "retrievalMode": "indexed", "updateMode": "manual_review", "policyDigest": otherDigest, "trigger": "reviewed_update"}},
		{"removal counts", ActionSourceChangePreviewCreated, "source_change_preview", targetID, "", map[string]any{"sourceId": otherID, "sourceRevision": 1, "intent": "remove", "configurationDigest": digest, "observationSetDigest": digest, "previewDigest": digest, "currentMatched": 2, "proposedMatched": 1, "excludedObservations": 1, "sendsQueriesUpstream": false, "expiresAt": at.Format(time.RFC3339Nano)}},
		{"update count difference", ActionSourceChangePreviewCreated, "source_change_preview", targetID, "", map[string]any{"sourceId": otherID, "sourceRevision": 1, "intent": "update", "configurationDigest": digest, "observationSetDigest": digest, "previewDigest": digest, "currentMatched": 2, "proposedMatched": 1, "excludedObservations": 0, "sendsQueriesUpstream": false, "expiresAt": at.Format(time.RFC3339Nano)}},
		{"update proposed count overflow", ActionSourceChangePreviewCreated, "source_change_preview", targetID, "", map[string]any{"sourceId": otherID, "sourceRevision": 1, "intent": "update", "configurationDigest": digest, "observationSetDigest": digest, "previewDigest": digest, "currentMatched": 1, "proposedMatched": 2, "excludedObservations": 0, "sendsQueriesUpstream": false, "expiresAt": at.Format(time.RFC3339Nano)}},
		{"candidate attribution", ActionPublicationCandidateCreated, "publication_snapshot", targetID, "", map[string]any{"host": "catalog.example.com", "audience": "anonymous", "generation": 1, "ardProfile": "ard-v1", "aiCatalogProfile": "ai-v1", "ardDigest": digest, "aiCatalogDigest": digest, "entryCount": 0, "exclusionCount": 0, "trigger": "manual", "jobId": otherID}},
		{"candidate missing initiator", ActionPublicationCandidateCreated, "publication_snapshot", targetID, "", map[string]any{"host": "catalog.example.com", "audience": "anonymous", "generation": 1, "ardProfile": "ard-v1", "aiCatalogProfile": "ai-v1", "ardDigest": digest, "aiCatalogDigest": digest, "entryCount": 0, "exclusionCount": 0, "trigger": "manual", "jobId": otherID, "jobKind": "publication.generate", "jobAttempt": 1}},
		{"successful test without state change", ActionSourceTested, "federation_source", targetID, "", map[string]any{"beforeRevision": 1, "afterRevision": 1, "outcome": "succeeded", "stateChanged": false, "httpClass": "2xx", "policyDigest": digest, "bodyDigest": otherDigest}},
		{"failed test with state change", ActionSourceTested, "federation_source", targetID, "", map[string]any{"beforeRevision": 1, "afterRevision": 2, "outcome": "http_error", "stateChanged": true, "httpClass": "5xx", "policyDigest": digest, "bodyDigest": otherDigest}},
		{"completed sync failure class", ActionSourceSyncCompleted, "source_snapshot", targetID, "", map[string]any{"sourceId": otherID, "sourceRevision": 1, "state": "complete", "policyDigest": digest, "memberCount": 0, "added": 0, "updated": 0, "withdrawn": 0, "failureClass": "dependency", "jobId": targetID, "jobKind": "source.sync", "jobAttempt": 1, "initiator": InitiatorSourceMaintainer, "trigger": "automatic"}},
		{"sync changes exceed membership", ActionSourceSyncCompleted, "source_snapshot", targetID, "", map[string]any{"sourceId": otherID, "sourceRevision": 1, "state": "complete", "policyDigest": digest, "memberCount": 1, "added": 1, "updated": 1, "withdrawn": 0, "jobId": targetID, "jobKind": "source.sync", "jobAttempt": 1, "initiator": InitiatorSourceMaintainer, "trigger": "automatic"}},
		{"automatic sync without initiator", ActionSourceSyncCompleted, "source_snapshot", targetID, "", map[string]any{"sourceId": otherID, "sourceRevision": 1, "state": "complete", "policyDigest": digest, "memberCount": 0, "added": 0, "updated": 0, "withdrawn": 0, "jobId": targetID, "jobKind": "source.sync", "jobAttempt": 1, "trigger": "automatic"}},
		{"automatic sync with membership initiator", ActionSourceSyncCompleted, "source_snapshot", targetID, "", map[string]any{"sourceId": otherID, "sourceRevision": 1, "state": "complete", "policyDigest": digest, "memberCount": 0, "added": 0, "updated": 0, "withdrawn": 0, "jobId": targetID, "jobKind": "source.sync", "jobAttempt": 1, "initiator": otherID, "trigger": "automatic"}},
		{"failed sync missing class", ActionSourceSyncFailed, "source_snapshot", targetID, "", map[string]any{"sourceId": otherID, "sourceRevision": 1, "state": "failed", "policyDigest": digest, "memberCount": 0, "added": 0, "updated": 0, "withdrawn": 0, "jobId": targetID, "jobKind": "source.sync", "jobAttempt": 1, "initiator": InitiatorSourceMaintainer, "trigger": "automatic"}},
		{"failed sync unknown class", ActionSourceSyncFailed, "source_snapshot", targetID, "", map[string]any{"sourceId": otherID, "sourceRevision": 1, "state": "failed", "policyDigest": digest, "memberCount": 0, "added": 0, "updated": 0, "withdrawn": 0, "failureClass": "future", "jobId": targetID, "jobKind": "source.sync", "jobAttempt": 1, "initiator": InitiatorSourceMaintainer, "trigger": "automatic"}},
		{"withdrawal digests", ActionPublicationUnpublished, "publication_snapshot", targetID, "", map[string]any{"host": "catalog.example.com", "generation": 1, "cause": "release_revoked", "originatingAction": "listing_release.revoked", "originatingTargetId": otherID, "ardDigest": digest}},
		{"withdrawal cause action mismatch", ActionPublicationUnpublished, "publication_snapshot", targetID, "", map[string]any{"host": "catalog.example.com", "generation": 1, "cause": "release_revoked", "originatingAction": "listing_release.retired", "originatingTargetId": otherID}},
		{"cancel state", ActionJobCancellationRequested, "job", targetID, "cancel", map[string]any{"kind": "source.sync", "beforeState": "running", "afterState": "running", "cancellationRequested": false}},
		{"cancel failure class", ActionJobCancellationRequested, "job", targetID, "cancel", map[string]any{"kind": "source.sync", "beforeState": "failed", "afterState": "failed", "cancellationRequested": false, "failureClass": "unsafe_future_class"}},
		{"retry identity", ActionJobRetried, "job", targetID, "retry", map[string]any{"kind": "source.sync", "beforeState": "failed", "afterState": "queued", "parentJobId": targetID, "replacementJobId": targetID}},
		{"retry failure class", ActionJobRetried, "job", targetID, "retry", map[string]any{"kind": "source.sync", "beforeState": "failed", "afterState": "queued", "parentJobId": targetID, "replacementJobId": otherID, "failureClass": "unsafe_future_class"}},
		{"reversed projection IDs", ActionSearchProjectionRebuilt, "catalog_search_projection", targetID, "repair", map[string]any{"selectedFirstId": otherID, "selectedLastId": targetID, "processed": 2, "hasMore": false, "beforeGeneration": 5, "afterGeneration": 7}},
		{"projection completion shapes", ActionSearchProjectionRebuilt, "catalog_search_projection", targetID, "repair", map[string]any{"selectedFirstId": targetID, "selectedLastId": otherID, "processed": 2, "remaining": 0, "hasMore": false, "beforeGeneration": 5, "afterGeneration": 7}},
		{"negative byte count", ActionCatalogDocumentRead, "catalog_document", strings.Repeat("c", 64), "", map[string]any{"contentDigest": digest, "sizeBytes": -1}},
		{"unordered live sources", ActionFederationLiveSearchDiagnostic, "federation_live_search", targetID, "investigate source", map[string]any{"contactedSourceIds": []string{otherID, targetID}, "outcome": "partial", "resultCount": 1, "diagnosticCount": 1, "partial": true, "policyDigests": []string{digest, otherDigest}}},
		{"partial live outcome without partial response", ActionFederationLiveSearchDiagnostic, "federation_live_search", targetID, "investigate source", map[string]any{"contactedSourceIds": []string{targetID}, "outcome": "partial", "resultCount": 1, "diagnosticCount": 1, "partial": false, "policyDigests": []string{digest}}},
		{"successful live outcome with partial response", ActionFederationLiveSearchDiagnostic, "federation_live_search", targetID, "investigate source", map[string]any{"contactedSourceIds": []string{targetID}, "outcome": "succeeded", "resultCount": 1, "diagnosticCount": 1, "partial": true, "policyDigests": []string{digest}}},
	}
	for _, testCase := range rejections {
		t.Run("reject "+testCase.name, func(t *testing.T) {
			if _, err := NewRecord(targetID, testTenant, principal, testCase.action, testCase.targetType, testCase.targetID, testCase.reason, Execution{}, testCase.facts, at); err == nil {
				t.Fatal("semantically inconsistent action facts were accepted")
			}
		})
	}
}

func TestReasonNormalizationUsesUnicodeCodePoints(t *testing.T) {
	trimmed, err := NormalizeReason("audit.exported", "\u2003reviewed recovery\u3000")
	if err != nil || trimmed != "reviewed recovery" {
		t.Fatalf("Unicode whitespace normalization = %q, %v", trimmed, err)
	}
	maximum := strings.Repeat("é", MaxReasonRunes)
	if normalized, err := NormalizeRequiredReason(maximum); err != nil || normalized != maximum {
		t.Fatalf("maximum Unicode reason rejected: length=%d, err=%v", len([]rune(normalized)), err)
	}
	for name, reason := range map[string]string{
		"too many code points": strings.Repeat("界", MaxReasonRunes+1),
		"embedded control":     "review\u0000reason",
		"invalid UTF-8":        string([]byte{'r', 0xff}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NormalizeRequiredReason(reason); err == nil {
				t.Fatal("invalid reason was accepted")
			}
		})
	}
}

func TestFactKindsAndIntegerNormalizationRejectAmbiguousValues(t *testing.T) {
	t.Parallel()

	uuid := "00000000-0000-7000-8000-000000000010"
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, testCase := range []struct {
		name  string
		value any
		kind  factKind
		valid bool
	}{
		{name: "text", value: "bounded", kind: factText, valid: true},
		{name: "text type", value: 1, kind: factText},
		{name: "text control", value: "line\nbreak", kind: factText},
		{name: "UUID", value: uuid, kind: factUUID, valid: true},
		{name: "UUID syntax", value: "uuid", kind: factUUID},
		{name: "digest", value: digest, kind: factDigest, valid: true},
		{name: "digest syntax", value: "sha256:no", kind: factDigest},
		{name: "JSON integer", value: json.Number("7"), kind: factInteger, valid: true},
		{name: "JSON decimal", value: json.Number("7.5"), kind: factInteger},
		{name: "boolean", value: true, kind: factBoolean, valid: true},
		{name: "boolean type", value: "true", kind: factBoolean},
		{name: "text array", value: []any{"one", "two"}, kind: factTextArray, valid: true},
		{name: "array type", value: []string{"one"}, kind: factTextArray},
		{name: "array bound", value: make([]any, 33), kind: factTextArray},
		{name: "array item type", value: []any{1}, kind: factTextArray},
		{name: "UUID array", value: []any{uuid}, kind: factUUIDArray, valid: true},
		{name: "UUID array syntax", value: []any{"uuid"}, kind: factUUIDArray},
		{name: "digest array", value: []any{digest}, kind: factDigestArray, valid: true},
		{name: "digest array syntax", value: []any{"sha256:no"}, kind: factDigestArray},
		{name: "unknown kind", value: "value", kind: factKind(255)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateFactKind("tested", testCase.value, testCase.kind)
			if (err == nil) != testCase.valid {
				t.Fatalf("validation error = %v, valid = %t", err, testCase.valid)
			}
		})
	}

	for _, testCase := range []struct {
		name  string
		value any
		want  int64
		valid bool
	}{
		{name: "JSON number", value: json.Number("-7"), want: -7, valid: true},
		{name: "invalid JSON number", value: json.Number("1.1")},
		{name: "int", value: int(1), want: 1, valid: true},
		{name: "int8", value: int8(2), want: 2, valid: true},
		{name: "int16", value: int16(3), want: 3, valid: true},
		{name: "int32", value: int32(4), want: 4, valid: true},
		{name: "int64", value: int64(5), want: 5, valid: true},
		{name: "uint", value: uint(6), want: 6, valid: true},
		{name: "uint8", value: uint8(7), want: 7, valid: true},
		{name: "uint16", value: uint16(8), want: 8, valid: true},
		{name: "uint32", value: uint32(9), want: 9, valid: true},
		{name: "uint64", value: uint64(10), want: 10, valid: true},
		{name: "uint64 overflow", value: uint64(math.MaxInt64) + 1},
		{name: "unsupported", value: float64(1)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := integerFact(testCase.value)
			if ok != testCase.valid || got != testCase.want {
				t.Fatalf("integerFact(%T(%v)) = %d, %t; want %d, %t", testCase.value, testCase.value, got, ok, testCase.want, testCase.valid)
			}
		})
	}

	for _, value := range []string{"", strings.Repeat("a", 64), "Upper", "-prefix", "suffix-", "has.dot"} {
		if safeProfileName(value) {
			t.Fatalf("unsafe profile name %q was accepted", value)
		}
	}
	for _, value := range []string{"a", "private-1", "9-network"} {
		if !safeProfileName(value) {
			t.Fatalf("safe profile name %q was rejected", value)
		}
	}
}

func TestActionFactSemanticsRejectInvalidTransitionsAndVocabularies(t *testing.T) {
	t.Parallel()

	uuid := "00000000-0000-7000-8000-000000000010"
	otherUUID := "00000000-0000-7000-8000-000000000011"
	digest := "sha256:" + strings.Repeat("a", 64)
	otherDigest := "sha256:" + strings.Repeat("b", 64)
	at := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	timestamp := at.Format(time.RFC3339Nano)
	later := at.Add(time.Hour).Format(time.RFC3339Nano)

	// These two cases concern the closed envelope, not the second-layer
	// transition rules below. Supply every unrelated required fact so each
	// rejection proves the intended field rule.
	for _, mode := range []string{"missing hasMore", "obsolete remaining"} {
		t.Run(mode, func(t *testing.T) {
			facts := map[string]any{"processed": 1, "hasMore": false, "beforeGeneration": 0, "afterGeneration": 1, "selectedFirstId": uuid, "selectedLastId": otherUUID}
			if err := validateActionFacts(ActionSearchProjectionRebuilt, facts); err != nil {
				t.Fatalf("valid repair facts: %v", err)
			}
			want := `"hasMore" is required`
			if mode == "missing hasMore" {
				delete(facts, "hasMore")
			} else {
				facts["remaining"] = -1
				want = `"remaining" is not allowed`
			}
			if err := validateActionFacts(ActionSearchProjectionRebuilt, facts); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("repair field validation: %v, want %s", err, want)
			}
		})
	}

	tests := []struct {
		name   string
		action string
		facts  map[string]any
	}{
		{name: "negative export count", action: ActionAuditExported, facts: map[string]any{"rowCount": -1, "occurredFrom": timestamp, "occurredBefore": later}},
		{name: "invalid export start", action: ActionAuditExported, facts: map[string]any{"rowCount": 0, "occurredFrom": "yesterday", "occurredBefore": later}},
		{name: "invalid export end", action: ActionAuditExported, facts: map[string]any{"rowCount": 0, "occurredFrom": timestamp, "occurredBefore": "tomorrow"}},
		{name: "inspection outcome", action: ActionSourceInspected, facts: map[string]any{"outcome": "unknown", "originClass": "public"}},
		{name: "inspection profile", action: ActionSourceInspected, facts: map[string]any{"outcome": "transport_error", "originClass": "named_private:Upper"}},
		{name: "inspection expiry", action: ActionSourceInspected, facts: map[string]any{"outcome": "succeeded", "originClass": "public", "inspectionDigest": digest, "expiresAt": "soon"}},
		{name: "preview intent", action: ActionSourceChangePreviewCreated, facts: map[string]any{"intent": "replace", "sourceRevision": 1, "currentMatched": 0, "proposedMatched": 0, "excludedObservations": 0, "expiresAt": later}},
		{name: "preview revision", action: ActionSourceChangePreviewCreated, facts: map[string]any{"intent": "update", "sourceRevision": 0, "currentMatched": 0, "proposedMatched": 0, "excludedObservations": 0, "expiresAt": later}},
		{name: "preview negative count", action: ActionSourceChangePreviewCreated, facts: map[string]any{"intent": "update", "sourceRevision": 1, "currentMatched": -1, "proposedMatched": 0, "excludedObservations": 0, "expiresAt": later}},
		{name: "connection retrieval mode", action: ActionSourceConnected, facts: map[string]any{"sourceRevision": 1, "retrievalMode": "other", "updateMode": "automatic"}},
		{name: "connection update mode", action: ActionSourceConnected, facts: map[string]any{"sourceRevision": 1, "retrievalMode": "indexed", "updateMode": "other"}},
		{name: "revision number", action: ActionSourceRevisionActivated, facts: map[string]any{"sourceRevision": 0, "retrievalMode": "indexed", "updateMode": "automatic", "beforeConfigurationDigest": digest, "afterConfigurationDigest": otherDigest, "trigger": "reviewed_update"}},
		{name: "revision retrieval mode", action: ActionSourceRevisionActivated, facts: map[string]any{"sourceRevision": 2, "retrievalMode": "other", "updateMode": "automatic", "beforeConfigurationDigest": digest, "afterConfigurationDigest": otherDigest, "trigger": "reviewed_update"}},
		{name: "revision update mode", action: ActionSourceRevisionActivated, facts: map[string]any{"sourceRevision": 2, "retrievalMode": "indexed", "updateMode": "other", "beforeConfigurationDigest": digest, "afterConfigurationDigest": otherDigest, "trigger": "reviewed_update"}},
		{name: "revision trigger", action: ActionSourceRevisionActivated, facts: map[string]any{"sourceRevision": 2, "retrievalMode": "indexed", "updateMode": "automatic", "beforeConfigurationDigest": digest, "afterConfigurationDigest": otherDigest, "trigger": "automatic"}},
		{name: "source test outcome", action: ActionSourceTested, facts: map[string]any{"outcome": "unknown", "beforeRevision": 1, "afterRevision": 1, "stateChanged": false}},
		{name: "source test revision bound", action: ActionSourceTested, facts: map[string]any{"outcome": "transport_error", "beforeRevision": 0, "afterRevision": 1, "stateChanged": false}},
		{name: "source test revision relationship", action: ActionSourceTested, facts: map[string]any{"outcome": "transport_error", "beforeRevision": 1, "afterRevision": 2, "stateChanged": false}},
		{name: "source test HTTP class", action: ActionSourceTested, facts: map[string]any{"outcome": "transport_error", "beforeRevision": 1, "afterRevision": 1, "stateChanged": false, "httpClass": "700"}},
		{name: "lifecycle revision bound", action: ActionSourcePaused, facts: map[string]any{"beforeRevision": 0, "afterRevision": 1, "beforeState": "active", "afterState": "paused"}},
		{name: "lifecycle revision relationship", action: ActionSourcePaused, facts: map[string]any{"beforeRevision": 1, "afterRevision": 3, "beforeState": "active", "afterState": "paused"}},
		{name: "lifecycle vocabulary", action: ActionSourcePaused, facts: map[string]any{"beforeRevision": 1, "afterRevision": 2, "beforeState": "removed", "afterState": "paused"}},
		{name: "lifecycle state relationship", action: ActionSourcePaused, facts: map[string]any{"beforeRevision": 1, "afterRevision": 2, "beforeState": "active", "afterState": "active"}},
		{name: "sync request revision", action: ActionSourceSyncRequested, facts: map[string]any{"sourceRevision": 0, "trigger": "manual"}},
		{name: "sync request trigger", action: ActionSourceSyncRequested, facts: map[string]any{"sourceRevision": 1, "trigger": "automatic"}},
		{name: "sync attempt", action: ActionSourceSyncCompleted, facts: map[string]any{"sourceRevision": 1, "jobAttempt": 0, "memberCount": 0, "added": 0, "updated": 0, "withdrawn": 0, "jobKind": "source.sync", "trigger": "manual", "state": "complete"}},
		{name: "sync negative count", action: ActionSourceSyncCompleted, facts: map[string]any{"sourceRevision": 1, "jobAttempt": 1, "memberCount": -1, "added": 0, "updated": 0, "withdrawn": 0, "jobKind": "source.sync", "trigger": "manual", "state": "complete"}},
		{name: "sync Job kind", action: ActionSourceSyncCompleted, facts: map[string]any{"sourceRevision": 1, "jobAttempt": 1, "memberCount": 0, "added": 0, "updated": 0, "withdrawn": 0, "jobKind": "other", "trigger": "manual", "state": "complete"}},
		{name: "sync trigger", action: ActionSourceSyncCompleted, facts: map[string]any{"sourceRevision": 1, "jobAttempt": 1, "memberCount": 0, "added": 0, "updated": 0, "withdrawn": 0, "jobKind": "source.sync", "trigger": "scheduled", "state": "complete"}},
		{name: "sync initiator", action: ActionSourceSyncCompleted, facts: map[string]any{"sourceRevision": 1, "jobAttempt": 1, "memberCount": 0, "added": 0, "updated": 0, "withdrawn": 0, "jobKind": "source.sync", "trigger": "manual", "initiator": "external-subject", "state": "complete"}},
		{name: "manual sync maintainer", action: ActionSourceSyncCompleted, facts: map[string]any{"sourceRevision": 1, "jobAttempt": 1, "memberCount": 0, "added": 0, "updated": 0, "withdrawn": 0, "jobKind": "source.sync", "trigger": "manual", "initiator": InitiatorSourceMaintainer, "state": "complete"}},
		{name: "sync state", action: ActionSourceSyncCompleted, facts: map[string]any{"sourceRevision": 1, "jobAttempt": 1, "memberCount": 0, "added": 0, "updated": 0, "withdrawn": 0, "jobKind": "source.sync", "trigger": "manual", "state": "failed"}},
		{name: "candidate generation", action: ActionPublicationCandidateCreated, facts: map[string]any{"generation": 0, "entryCount": 0, "exclusionCount": 0, "audience": "anonymous", "trigger": "direct"}},
		{name: "candidate count", action: ActionPublicationCandidateCreated, facts: map[string]any{"generation": 1, "entryCount": -1, "exclusionCount": 0, "audience": "anonymous", "trigger": "direct"}},
		{name: "candidate audience", action: ActionPublicationCandidateCreated, facts: map[string]any{"generation": 1, "entryCount": 0, "exclusionCount": 0, "audience": "private", "trigger": "direct"}},
		{name: "candidate trigger", action: ActionPublicationCandidateCreated, facts: map[string]any{"generation": 1, "entryCount": 0, "exclusionCount": 0, "audience": "anonymous", "trigger": "automatic"}},
		{name: "direct candidate attribution", action: ActionPublicationCandidateCreated, facts: map[string]any{"generation": 1, "entryCount": 0, "exclusionCount": 0, "audience": "anonymous", "trigger": "direct", "jobId": uuid}},
		{name: "withdrawal generation", action: ActionPublicationUnpublished, facts: map[string]any{"generation": 0, "cause": "explicit_unpublish", "originatingAction": "publication.unpublish"}},
		{name: "withdrawal cause", action: ActionPublicationUnpublished, facts: map[string]any{"generation": 1, "cause": "other", "originatingAction": "publication.unpublish"}},
		{name: "cancellation before state", action: ActionJobCancellationRequested, facts: map[string]any{"beforeState": "other", "afterState": "cancelled", "cancellationRequested": false}},
		{name: "cancellation after state", action: ActionJobCancellationRequested, facts: map[string]any{"beforeState": "queued", "afterState": "other", "cancellationRequested": false}},
		{name: "queued cancellation relationship", action: ActionJobCancellationRequested, facts: map[string]any{"beforeState": "queued", "afterState": "cancelled", "cancellationRequested": true}},
		{name: "terminal cancellation relationship", action: ActionJobCancellationRequested, facts: map[string]any{"beforeState": "succeeded", "afterState": "failed", "cancellationRequested": false}},
		{name: "repair processed count", action: ActionSearchProjectionRebuilt, facts: map[string]any{"processed": 0, "hasMore": false, "beforeGeneration": 0, "afterGeneration": 1, "selectedFirstId": uuid, "selectedLastId": otherUUID}},
		{name: "repair negative prior generation", action: ActionSearchProjectionRebuilt, facts: map[string]any{"processed": 1, "hasMore": false, "beforeGeneration": -1, "afterGeneration": 1, "selectedFirstId": uuid, "selectedLastId": otherUUID}},
		{name: "repair generation", action: ActionSearchProjectionRebuilt, facts: map[string]any{"processed": 2, "hasMore": false, "beforeGeneration": 1, "afterGeneration": 2, "selectedFirstId": uuid, "selectedLastId": otherUUID}},
		{name: "live outcome", action: ActionFederationLiveSearchDiagnostic, facts: map[string]any{"outcome": "unknown", "resultCount": 0, "diagnosticCount": 0, "partial": false}},
		{name: "live count", action: ActionFederationLiveSearchDiagnostic, facts: map[string]any{"outcome": "succeeded", "resultCount": -1, "diagnosticCount": 0, "partial": false}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			// Exercise relationships after field-shape validation. These focused
			// fixtures omit unrelated required fields; NewRecord contract tests
			// above cover the complete envelopes and their field validation.
			if err := validateActionFactSemantics(testCase.action, testCase.facts); err == nil {
				t.Fatal("invalid action facts were accepted")
			}
		})
	}
}

func TestBuildAndVerifyExportPreserveOrderNumbersAndEmptyRanges(t *testing.T) {
	from := time.Date(2026, time.September, 3, 10, 0, 0, 0, time.UTC)
	before := from.Add(time.Hour)
	query := Query{Action: "namespace.created", OccurredFrom: &from, OccurredBefore: &before}
	records := []Record{
		currentTestRecord("00000000-0000-7000-8000-000000000010", from.Add(time.Minute), map[string]any{"large": json.Number("9007199254740993"), "decimal": json.Number("1.2300")}),
		currentTestRecord("00000000-0000-7000-8000-000000000011", from.Add(2*time.Minute), map[string]any{"z": true, "a": "ordered"}),
	}
	export, raw, err := BuildExport(testTenant, query, before, before.Add(time.Second), records)
	if err != nil {
		t.Fatal(err)
	}
	if export.Manifest.RowCount != 2 || export.Manifest.First.ID != records[0].ID || export.Manifest.Last.ID != records[1].ID || !bytes.Contains(export.RawEvents, []byte(`9007199254740993`)) || !bytes.Contains(export.RawEvents, []byte(`1.2300`)) {
		t.Fatalf("export = %#v / %s", export, raw)
	}
	verified, report, err := VerifyExport(raw)
	if err != nil || !report.Valid || report.RowCount != 2 || !bytes.Equal(verified.RawEvents, export.RawEvents) || !strings.Contains(report.AssuranceBoundary, "cannot prove") {
		t.Fatalf("verification = %#v / %#v / %v", verified, report, err)
	}
	empty, emptyRaw, err := BuildExport(testTenant, Query{OccurredFrom: &from, OccurredBefore: &before}, before, before, nil)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Manifest.RowCount != 0 || empty.Manifest.First != nil || empty.Manifest.Last != nil || string(empty.RawEvents) != "[]" {
		t.Fatalf("empty export = %#v", empty)
	}
	if _, _, err := VerifyExport(emptyRaw); err != nil {
		t.Fatalf("empty export did not verify: %v", err)
	}
}

func TestExportRejectsBoundsOrderingTamperingAndAmbiguity(t *testing.T) {
	from := time.Date(2026, time.September, 3, 10, 0, 0, 0, time.UTC)
	before := from.Add(time.Hour)
	query := Query{OccurredFrom: &from, OccurredBefore: &before}
	first := currentTestRecord("00000000-0000-7000-8000-000000000010", from.Add(time.Minute), nil)
	second := currentTestRecord("00000000-0000-7000-8000-000000000011", from.Add(2*time.Minute), nil)
	if _, _, err := BuildExport(testTenant, query, before, before, []Record{second, first}); err == nil {
		t.Fatal("descending export records were accepted")
	}
	wrongAction := first
	wrongAction.Action = "resource.created"
	if _, _, err := BuildExport(testTenant, Query{Action: "namespace.created", OccurredFrom: &from, OccurredBefore: &before}, before, before, []Record{wrongAction}); err == nil {
		t.Fatal("record outside the exact action filter was accepted")
	}
	if _, _, err := BuildExport(testTenant, query, from.Add(time.Minute), before, nil); err == nil {
		t.Fatal("cutoff before the range end was accepted")
	}
	if _, _, err := BuildExport(testTenant, query, before, from, nil); err == nil {
		t.Fatal("generation time before the cutoff was accepted")
	}
	rows := make([]Record, MaxExportRows+1)
	for index := range rows {
		rows[index] = currentTestRecord(fmt.Sprintf("00000000-0000-7000-8000-%012d", index+1), from.Add(time.Duration(index)*time.Microsecond), nil)
	}
	maximum, maximumRaw, err := BuildExport(testTenant, query, before, before, rows[:MaxExportRows])
	if err != nil {
		t.Fatal(err)
	}
	if _, report, err := VerifyExport(maximumRaw); err != nil || report.RowCount != MaxExportRows {
		t.Fatalf("maximum row count verification: %+v, %v", report, err)
	}
	if _, err := BindTrustedManifest(maximum, "maximum-rows"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := BuildExport(testTenant, query, before, before, rows); !isRangeTooLarge(err) {
		t.Fatalf("row overflow = %v", err)
	}
	responseRows := make([]Record, 600)
	for index := range responseRows {
		responseRows[index] = currentTestRecord(fmt.Sprintf("00000000-0000-7000-8001-%012d", index+1), from.Add(time.Duration(index)*time.Microsecond), map[string]any{"value": strings.Repeat("x", 15<<10)})
	}
	if _, _, err := BuildExport(testTenant, query, before, before, responseRows); !isRangeTooLarge(err) {
		t.Fatalf("encoded response overflow = %v", err)
	}
	for index := range responseRows {
		responseRows[index].Details["facts"] = map[string]any{"value": strings.Repeat("x", 13000)}
	}
	_, boundedRaw, err := BuildExport(testTenant, query, before, before, responseRows)
	if err != nil {
		t.Fatal(err)
	}
	remaining := MaxExportResponseBytes - len(boundedRaw)
	for index := range responseRows {
		extra := remaining / len(responseRows)
		if index < remaining%len(responseRows) {
			extra++
		}
		responseRows[index].Details["facts"] = map[string]any{"value": strings.Repeat("x", 13000+extra)}
	}
	_, boundedRaw, err = BuildExport(testTenant, query, before, before, responseRows)
	if err != nil || len(boundedRaw) != MaxExportResponseBytes {
		t.Fatalf("exact export byte limit: bytes=%d, %v", len(boundedRaw), err)
	}
	if _, report, err := VerifyExport(boundedRaw); err != nil || report.RowCount != len(responseRows) {
		t.Fatalf("exact export byte limit verification: %+v, %v", report, err)
	}
	if _, _, err := VerifyExport(append(boundedRaw, ' ')); err == nil {
		t.Fatal("otherwise valid export above file byte limit was accepted")
	}
	oversized := first
	oversized.Details = map[string]any{"value": strings.Repeat("x", MaxEncodedRecordBytes)}
	if _, _, err := BuildExport(testTenant, query, before, before, []Record{oversized}); err == nil {
		t.Fatal("oversized corrupt record was accepted")
	}
	if _, _, err := DigestRecords([]Record{oversized}); err == nil {
		t.Fatal("oversized corrupt record was digested")
	}
	_, raw, err := BuildExport(testTenant, query, before, before, []Record{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := VerifyExport(raw); err != nil {
		t.Fatalf("unmodified export must verify before tampering: %v", err)
	}
	if _, _, err := BuildExport(testTenant, query, before, before, []Record{first, first}); err == nil {
		t.Fatal("duplicate export positions were accepted")
	}
	mutations := [][]byte{
		bytes.Replace(raw, []byte(`"rowCount":2`), []byte(`"rowCount":1`), 1),
		bytes.Replace(raw, []byte(`"rowCount":2,`), nil, 1),
		bytes.Replace(raw, []byte(`"eventsDigest":"sha256:`), []byte(`"eventsDigest":"sha256:f`), 1),
		bytes.Replace(raw, []byte(`"formatVersion":1`), []byte(`"formatVersion":2`), 1),
		bytes.Replace(raw, []byte(`"manifest":{`), []byte(`"manifest":{"unknown":true,`), 1),
		bytes.Replace(raw, []byte(`"rowCount":2`), []byte(`"rowCount":2,"rowCount":2`), 1),
	}
	for index, mutated := range mutations {
		if _, _, err := VerifyExport(mutated); err == nil {
			t.Fatalf("tampered export %d verified: %s", index, mutated)
		}
	}
	atExclusiveEnd := second
	atExclusiveEnd.At = before
	if _, _, err := BuildExport(testTenant, query, before, before, []Record{atExclusiveEnd}); err == nil {
		t.Fatal("record at the exclusive range end was accepted")
	}
}

func TestExportExactSelectionFilters(t *testing.T) {
	from := time.Date(2026, time.September, 3, 10, 0, 0, 0, time.UTC)
	before := from.Add(time.Hour)
	record := currentTestRecord("00000000-0000-7000-8000-000000000010", from, nil)
	record.RequestID = "request-1"
	matching := Query{Actor: record.Actor, Action: record.Action, RequestID: record.RequestID, TargetType: record.TargetType, TargetID: record.TargetID, OccurredFrom: &from, OccurredBefore: &before}
	for _, tc := range []struct {
		name     string
		change   func(*Query)
		accepted bool
	}{
		{"matching", func(*Query) {}, true},
		{"no request filter", func(q *Query) { q.RequestID = "" }, true},
		{"other actor", func(q *Query) { q.Actor = "other-actor" }, false},
		{"other request", func(q *Query) { q.RequestID = "request-2" }, false},
		{"other target type", func(q *Query) { q.TargetType = "resource" }, false},
		{"other target ID", func(q *Query) { q.TargetID = "00000000-0000-7000-8000-000000000099" }, false},
		{"below lower bound", func(q *Query) { later := from.Add(time.Microsecond); q.OccurredFrom = &later }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := matching
			tc.change(&candidate)
			_, raw, err := BuildExport(testTenant, candidate, before, before, []Record{record})
			if (err == nil) != tc.accepted {
				t.Fatalf("selection accepted=%t: %v", tc.accepted, err)
			}
			if tc.accepted {
				if _, report, err := VerifyExport(raw); err != nil || report.RowCount != 1 {
					t.Fatalf("matching export: %+v, %v", report, err)
				}
			}
		})
	}
}

func TestExportRejectsMalformedHeadersSelectionsAndEventArrays(t *testing.T) {
	from := time.Date(2026, time.September, 3, 10, 0, 0, 0, time.UTC)
	before := from.Add(time.Hour)
	query := Query{OccurredFrom: &from, OccurredBefore: &before}
	record := currentTestRecord("00000000-0000-7000-8000-000000000010", from.Add(time.Minute), nil)
	if _, _, err := BuildExport("not-a-tenant", query, before, before, nil); err == nil {
		t.Fatal("invalid export tenant was accepted")
	}
	if _, _, err := BuildExport(testTenant, Query{}, before, before, nil); err == nil {
		t.Fatal("missing export range was accepted")
	}
	if _, _, err := BuildExport(testTenant, query, time.Time{}, before, nil); err == nil {
		t.Fatal("zero export cutoff was accepted")
	}
	wrongTenant := record
	wrongTenant.TenantID = "00000000-0000-7000-8000-000000000099"
	if _, _, err := BuildExport(testTenant, query, before, before, []Record{wrongTenant}); err == nil {
		t.Fatal("cross-tenant export record was accepted")
	}
	unencodable := record
	unencodable.Details = map[string]any{"value": func() {}}
	if _, _, err := BuildExport(testTenant, query, before, before, []Record{unencodable}); err == nil {
		t.Fatal("unencodable legacy record was accepted")
	}
	sameTimeLaterID := record
	sameTimeLaterID.ID = "00000000-0000-7000-8000-000000000011"
	if _, _, err := BuildExport(testTenant, query, before, before, []Record{sameTimeLaterID, record}); err == nil {
		t.Fatal("descending IDs at the same occurrence time were accepted")
	}

	_, validRaw, err := BuildExport(testTenant, query, before, before, nil)
	if err != nil {
		t.Fatal(err)
	}
	invalid := [][]byte{
		nil,
		[]byte(`{`),
		[]byte(`{"formatVersion":1,"events":[]}`),
		[]byte(`{"formatVersion":1,"manifest":{},"events":[]}`),
		bytes.Replace(validRaw, []byte(`"events":[]`), []byte(`"events":null`), 1),
		bytes.Replace(validRaw, []byte(`"events":[]`), []byte(`"events":{}`), 1),
		bytes.Replace(validRaw, []byte(`"events":[]`), []byte(`"events":[ ]`), 1),
		bytes.Replace(validRaw, []byte(testTenant), []byte("not-a-tenant"), 1),
		append(append([]byte(nil), validRaw...), []byte(` {}`)...),
	}
	for index, raw := range invalid {
		if _, _, err := VerifyExport(raw); err == nil {
			t.Fatalf("malformed export %d verified: %s", index, raw)
		}
	}

	rows := make([]Record, MaxExportRows+1)
	for index := range rows {
		rows[index] = currentTestRecord(fmt.Sprintf("00000000-0000-7000-8002-%012d", index+1), from.Add(time.Duration(index)*time.Microsecond), nil)
	}
	events, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(validRaw, &wire); err != nil {
		t.Fatal(err)
	}
	wire["events"] = json.RawMessage(events)
	overflowRaw, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := VerifyExport(overflowRaw); err == nil {
		t.Fatal("oversized decoded event array verified")
	}
}

func TestAuditCorruptionBoundariesRejectPersistedAndFileShapes(t *testing.T) {
	t.Parallel()

	from := time.Date(2026, time.September, 3, 10, 0, 0, 0, time.UTC)
	before := from.Add(time.Hour)
	query := Query{OccurredFrom: &from, OccurredBefore: &before}
	record := currentTestRecord("00000000-0000-7000-8000-000000000010", from.Add(time.Minute), nil)
	if _, err := NormalizeQuery(Query{Action: strings.Repeat("a.", MaxActionBytes)}, false); err == nil {
		t.Fatal("oversized action filter was accepted")
	}

	oversizedV1 := record
	version := RecordFormatV1
	oversizedV1.FormatVersion = &version
	oversizedV1.Details = map[string]any{"value": strings.Repeat("x", MaxEncodedRecordBytes)}
	if _, _, err := BuildExport(testTenant, query, before, before, []Record{oversizedV1}); err == nil {
		t.Fatal("oversized persisted version-1 event was accepted")
	}
	invalidV1 := record
	invalidV1.FormatVersion = &version
	invalidV1.Action = "audit.unknown"
	invalidV1.Details = map[string]any{}
	if _, _, err := BuildExport(testTenant, query, before, before, []Record{invalidV1}); err == nil {
		t.Fatal("invalid persisted version-1 event was accepted")
	}
	if _, err := requiredObjectMember([]byte("[]"), "manifest"); err == nil {
		t.Fatal("non-object audit envelope was accepted")
	}

	_, emptyRaw, err := BuildExport(testTenant, query, before, before, nil)
	if err != nil {
		t.Fatal(err)
	}
	var emptyWire map[string]any
	if err := json.Unmarshal(emptyRaw, &emptyWire); err != nil {
		t.Fatal(err)
	}
	delete(emptyWire, "events")
	missingEvents, err := json.Marshal(emptyWire)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := VerifyExport(missingEvents); err == nil {
		t.Fatal("audit export without events was accepted")
	}
	if err := json.Unmarshal(emptyRaw, &emptyWire); err != nil {
		t.Fatal(err)
	}
	manifest, ok := emptyWire["manifest"].(map[string]any)
	if !ok {
		t.Fatalf("audit export manifest = %#v", emptyWire["manifest"])
	}
	filters, ok := manifest["filters"].(map[string]any)
	if !ok {
		t.Fatalf("audit export filters = %#v", manifest["filters"])
	}
	filters["occurredFrom"] = filters["occurredBefore"]
	invalidRange, err := json.Marshal(emptyWire)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := VerifyExport(invalidRange); err == nil {
		t.Fatal("audit export with an empty manifest range was accepted")
	}

	_, recordRaw, err := BuildExport(testTenant, query, before, before, []Record{record})
	if err != nil {
		t.Fatal(err)
	}
	var recordWire map[string]any
	if err := json.Unmarshal(recordRaw, &recordWire); err != nil {
		t.Fatal(err)
	}
	events, ok := recordWire["events"].([]any)
	if !ok || len(events) != 1 {
		t.Fatalf("audit export events = %#v", recordWire["events"])
	}
	wireRecord, ok := events[0].(map[string]any)
	if !ok {
		t.Fatalf("audit export record = %#v", events[0])
	}
	wireRecord["tenantId"] = "00000000-0000-7000-8000-000000000099"
	wrongSelection, err := json.Marshal(recordWire)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := VerifyExport(wrongSelection); err == nil {
		t.Fatal("audit export containing another tenant was accepted")
	}

	principal := Principal{ID: testMembership, Type: PrincipalCatalogMembership, ActorType: "human", RealmID: testRealm}
	valid, err := NewRecord("00000000-0000-7000-8000-000000000011", testTenant, principal, "namespace.created", "namespace", testTenant, "", Execution{}, nil, from)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Record){
		"membership principal": func(value *Record) { value.Actor = "not-a-uuid" },
		"membership actor type": func(value *Record) {
			value.Details["actorType"] = "machine"
		},
		"oversized details": func(value *Record) {
			value.Details["facts"] = map[string]any{"value": strings.Repeat("x", MaxDetailsBytes)}
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := cloneRecord(t, valid)
			mutate(&candidate)
			if err := ValidateNewRecord(candidate); err == nil {
				t.Fatal("corrupt audit record was accepted")
			}
		})
	}
	if encoded, err := EncodeRecords(nil); err != nil || string(encoded) != "[]" {
		t.Fatalf("nil audit page = %s, %v", encoded, err)
	}
}

func TestTrustedManifestRequiresMatchingRecoveryPoint(t *testing.T) {
	from := time.Date(2026, time.September, 3, 10, 0, 0, 0, time.UTC)
	before := from.Add(time.Hour)
	export, _, err := BuildExport(testTenant, Query{OccurredFrom: &from, OccurredBefore: &before}, before, before, nil)
	if err != nil {
		t.Fatal(err)
	}
	trustedRaw, err := json.Marshal(TrustedManifest{FormatVersion: 1, RecoveryPointID: "backup-2026-09-03", Manifest: export.Manifest})
	if err != nil {
		t.Fatal(err)
	}
	trusted, err := ParseTrustedManifest(trustedRaw, "backup-2026-09-03")
	if err != nil || trusted.RecoveryPointID != "backup-2026-09-03" {
		t.Fatalf("trusted manifest = %#v, %v", trusted, err)
	}
	if _, err := ParseTrustedManifest(trustedRaw, "another-point"); err == nil {
		t.Fatal("mismatched recovery point was accepted")
	}
	withoutBinding := bytes.Replace(trustedRaw, []byte(`"recoveryPointId":"backup-2026-09-03",`), nil, 1)
	if _, err := ParseTrustedManifest(withoutBinding, "backup-2026-09-03"); err == nil {
		t.Fatal("missing recovery-point binding was accepted")
	}
	if _, err := ParseTrustedManifest(trustedRaw, "backup\npoint"); err == nil {
		t.Fatal("unsafe recovery-point binding was accepted")
	}
	if _, err := BindTrustedManifest(export, "backup\npoint"); err == nil {
		t.Fatal("unsafe recovery-point identifier was bound")
	}
	invalidRaw := [][]byte{
		nil,
		[]byte(`{`),
		[]byte(`{"formatVersion":1,"recoveryPointId":"backup-2026-09-03"}`),
		bytes.Replace(trustedRaw, []byte(`"rowCount":0,`), nil, 1),
		bytes.Replace(trustedRaw, []byte(`"manifest":{`), []byte(`"manifest":{"unknown":true,`), 1),
	}
	for index, raw := range invalidRaw {
		if _, err := ParseTrustedManifest(raw, "backup-2026-09-03"); err == nil {
			t.Fatalf("malformed trusted manifest %d parsed: %s", index, raw)
		}
	}

	badRange := trusted
	badRange.Manifest.Filters.TargetType = "resource"
	if err := ValidateTrustedManifest(badRange, badRange.RecoveryPointID); err == nil {
		t.Fatal("trusted manifest with an unpaired target parsed")
	}
	badDigest := trusted
	badDigest.Manifest.EventsDigest = "sha256:no"
	if err := ValidateTrustedManifest(badDigest, badDigest.RecoveryPointID); err == nil {
		t.Fatal("trusted manifest with an invalid digest parsed")
	}
	badEmptyPositions := trusted
	badEmptyPositions.Manifest.First = &Position{OccurredAt: from, ID: testMembership}
	if err := ValidateTrustedManifest(badEmptyPositions, badEmptyPositions.RecoveryPointID); err == nil {
		t.Fatal("empty trusted manifest with a position parsed")
	}
	oneEventExport, _, err := BuildExport(testTenant, Query{OccurredFrom: &from, OccurredBefore: &before}, before, before, []Record{currentTestRecord("00000000-0000-7000-8000-000000000010", from.Add(time.Minute), nil)})
	if err != nil {
		t.Fatal(err)
	}
	oneEvent, err := BindTrustedManifest(oneEventExport, "backup-2026-09-03")
	if err != nil {
		t.Fatal(err)
	}
	padded := append(append([]byte{}, trustedRaw...), bytes.Repeat([]byte(" "), (256<<10)-len(trustedRaw))...)
	if _, err := ParseTrustedManifest(padded, trusted.RecoveryPointID); err != nil {
		t.Fatalf("exact trusted file limit: %v", err)
	}
	if _, err := ParseTrustedManifest(append(padded, ' '), trusted.RecoveryPointID); err == nil {
		t.Fatal("oversized trusted file accepted")
	}
	for _, tc := range []struct {
		name     string
		change   func(*TrustedManifest)
		accepted bool
	}{
		{"two ordered positions", func(m *TrustedManifest) {
			m.Manifest.RowCount = 2
			m.Manifest.Last.ID = "00000000-0000-7000-8000-000000000011"
		}, true},
		{"two different times", func(m *TrustedManifest) {
			m.Manifest.RowCount = 2
			m.Manifest.Last.OccurredAt = from.Add(2 * time.Minute)
			m.Manifest.Last.ID = "00000000-0000-7000-8000-000000000011"
		}, true},
		{"missing first", func(m *TrustedManifest) { m.Manifest.First = nil }, false},
		{"missing last", func(m *TrustedManifest) { m.Manifest.Last = nil }, false},
		{"invalid first ID", func(m *TrustedManifest) { m.Manifest.RowCount = 2; m.Manifest.First.ID = "000" }, false},
		{"invalid last ID", func(m *TrustedManifest) { m.Manifest.RowCount = 2; m.Manifest.Last.ID = "bad" }, false},
		{"first below range", func(m *TrustedManifest) {
			m.Manifest.RowCount = 2
			m.Manifest.First.OccurredAt = from.Add(-time.Microsecond)
		}, false},
		{"last at upper bound", func(m *TrustedManifest) { m.Manifest.RowCount = 2; m.Manifest.Last.OccurredAt = before }, false},
		{"descending times", func(m *TrustedManifest) {
			m.Manifest.RowCount = 2
			m.Manifest.First.OccurredAt = from.Add(2 * time.Minute)
		}, false},
		{"descending IDs", func(m *TrustedManifest) {
			m.Manifest.RowCount = 2
			m.Manifest.First.ID = "00000000-0000-7000-8000-000000000011"
		}, false},
		{"one row different IDs", func(m *TrustedManifest) { m.Manifest.Last.ID = "00000000-0000-7000-8000-000000000011" }, false},
		{"one row different times", func(m *TrustedManifest) { m.Manifest.Last.OccurredAt = from.Add(2 * time.Minute) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := oneEvent
			first, last := *oneEvent.Manifest.First, *oneEvent.Manifest.Last
			candidate.Manifest.First, candidate.Manifest.Last = &first, &last
			tc.change(&candidate)
			err := ValidateTrustedManifest(candidate, candidate.RecoveryPointID)
			if (err == nil) != tc.accepted {
				t.Fatalf("trusted manifest accepted=%t: %v", tc.accepted, err)
			}
		})
	}
	oneEvent.Manifest.First.ID = "not-a-uuid"
	if err := ValidateTrustedManifest(oneEvent, oneEvent.RecoveryPointID); err == nil {
		t.Fatal("trusted manifest with an invalid position parsed")
	}
}

func TestNormalizeQueryRequiresExactPairedAndHalfOpenFilters(t *testing.T) {
	from := time.Date(2026, time.September, 3, 10, 0, 0, 123456789, time.FixedZone("test", 3600))
	before := from.Add(time.Hour)
	query, err := NormalizeQuery(Query{Actor: testMembership, TargetType: "resource", TargetID: "target", OccurredFrom: &from, OccurredBefore: &before}, true)
	if err != nil || query.OccurredFrom.Location() != time.UTC || query.OccurredBefore.Location() != time.UTC || query.OccurredFrom.Nanosecond()%1000 != 0 || query.OccurredBefore.Nanosecond()%1000 != 0 {
		t.Fatalf("normalized query = %#v, %v", query, err)
	}
	for _, tc := range []struct {
		name    string
		maximum int
		set     func(*Query, string)
	}{
		{"actor", MaxActorBytes, func(q *Query, v string) { q.Actor = v }},
		{"action", MaxActionBytes, func(q *Query, v string) { q.Action = "a." + v[2:] }},
		{"request ID", MaxRequestIDBytes, func(q *Query, v string) { q.RequestID = v }},
		{"target type", MaxTargetTypeBytes, func(q *Query, v string) { q.TargetType = v; q.TargetID = "target" }},
		{"target ID", MaxTargetIDBytes, func(q *Query, v string) { q.TargetType = "resource"; q.TargetID = v }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := Query{OccurredFrom: &from, OccurredBefore: &before}
			tc.set(&candidate, strings.Repeat("a", tc.maximum))
			if _, err := NormalizeQuery(candidate, true); err != nil {
				t.Fatalf("exact filter limit: %v", err)
			}
			tc.set(&candidate, strings.Repeat("a", tc.maximum+1))
			if _, err := NormalizeQuery(candidate, true); err == nil {
				t.Fatal("oversized filter accepted")
			}
		})
	}
	invalid := []Query{
		{TargetType: "resource"},
		{TargetID: "target"},
		{OccurredFrom: &before, OccurredBefore: &from},
		{OccurredFrom: &from},
		{Action: "Bad Action", OccurredFrom: &from, OccurredBefore: &before},
		{RequestID: "bad/request", OccurredFrom: &from, OccurredBefore: &before},
		{Actor: " leading-space", OccurredFrom: &from, OccurredBefore: &before},
		{Actor: strings.Repeat("a", MaxActorBytes+1), OccurredFrom: &from, OccurredBefore: &before},
		{TargetType: strings.Repeat("t", MaxTargetTypeBytes+1), TargetID: "target", OccurredFrom: &from, OccurredBefore: &before},
	}
	for _, value := range invalid {
		if _, err := NormalizeQuery(value, true); err == nil {
			t.Fatalf("invalid query accepted: %#v", value)
		}
	}
}

func currentTestRecord(id string, at time.Time, details map[string]any) Record {
	if details == nil {
		details = map[string]any{}
	}
	version := RecordFormatV1
	return Record{FormatVersion: &version, ID: id, TenantID: testTenant, Actor: testMembership, Action: "namespace.created", TargetType: "namespace", TargetID: testTenant, Details: map[string]any{"principalType": PrincipalCatalogMembership, "actorType": "human", "realmId": testRealm, "facts": details}, At: at.UTC().Truncate(time.Microsecond)}
}

func cloneRecord(t *testing.T, record Record) Record {
	t.Helper()
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var result Record
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func isRangeTooLarge(err error) bool {
	var target *RangeTooLargeError
	return errors.As(err, &target)
}

func FuzzVerifyExport(f *testing.F) {
	from := time.Date(2026, time.September, 3, 10, 0, 0, 0, time.UTC)
	before := from.Add(time.Hour)
	_, raw, err := BuildExport(testTenant, Query{OccurredFrom: &from, OccurredBefore: &before}, before, before, []Record{currentTestRecord("00000000-0000-7000-8000-000000000010", from, map[string]any{"number": json.Number("1.00")})})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(raw)
	f.Add([]byte(`{"formatVersion":1,"manifest":{},"events":[]}`))
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > MaxExportFileBytes+1 {
			t.Skip()
		}
		_, _, _ = VerifyExport(input)
	})
}
