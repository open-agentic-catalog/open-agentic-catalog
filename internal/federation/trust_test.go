package federation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/standards/anstrust"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

type trustTestVerifier struct {
	enabled bool
	digest  string
}

type refusedTrustSource struct {
	TrustVerifier
	err error
}

func (v refusedTrustSource) ValidateSource(context.Context, Source) error { return v.err }

func (*trustTestVerifier) ValidateSource(context.Context, Source) error { return nil }
func (v *trustTestVerifier) Enabled() bool                              { return v.enabled }
func (v *trustTestVerifier) Policy(name string) (string, string, bool) {
	return v.digest, "owned-authority", v.enabled && name == "owned"
}
func (*trustTestVerifier) Assess(context.Context, jobs.TrustPayload, func(context.Context) error, func() time.Time) (anstrust.Bundle, anstrust.Result, error) {
	panic("selection must not acquire evidence")
}

func trustSelectionFixture(t *testing.T) (*Manager, *MemoryStore, *livePagingFetcher, Source, TrustRequest, []byte) {
	t.Helper()
	m, store, outbound, sources := newLivePagingFixture(t, 2, "trust")
	source := sources[0]
	source.TrustVerificationPolicy = "owned"
	source.Scope = Scope{Publishers: []string{"peer.example"}}
	store.sources[source.ID] = source
	if err := WithTrustVerifier(&trustTestVerifier{true, federationTestPolicyDigest})(m); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{ "identifier":"urn:air:peer.example:agents:one", "version":"1.0", "type":"application/a2a-agent-card+json", "url":"https://peer.example/card", "score":100, "source":"https://trust.example", "description":"a\u0020b" }`)
	input := TrustRequest{SourceRevision: 1, Identifier: "urn:air:peer.example:agents:one", Version: "1.0", VersionPresent: true, MediaType: "application/a2a-agent-card+json", Delivery: "url", ExactEntryDigest: audit.DigestBytes(raw), Text: "one", Reason: "Assess selected registration"}
	outbound.respond = func(_ context.Context, r fetcher.Request) (fetcher.Result, error) {
		return fetcher.Result{Status: http.StatusOK, Body: []byte(`{"results":[` + string(raw) + `]}`), MediaType: "application/json"}, nil
	}
	return m, store, outbound, source, input, raw
}
func TestTrustSelectionPreservesExactBytesAndDirectTopology(t *testing.T) {
	m, store, outbound, source, input, raw := trustSelectionFixture(t)
	source.Referrals = ReferralPolicy{Enabled: true, MaxHops: 2, ApprovedSourceIDs: []string{"00000000-0000-7000-8000-000000000099"}}
	store.sources[source.ID] = source
	p, err := m.PrepareTrustAssessment(livePagingContext(t), source.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if string(p.Entry) != string(raw) || p.ExactEntryDigest != audit.DigestBytes(raw) || len(outbound.requests) != 1 {
		t.Fatalf("exact selection: %+v calls=%d", p, len(outbound.requests))
	}
	var body struct {
		Federation string `json:"federation"`
	}
	if json.Unmarshal(outbound.requests[0].Body, &body) != nil || body.Federation != "none" {
		t.Fatalf("referrals escaped: %s", outbound.requests[0].Body)
	}
	changed := input
	var obj any
	_ = json.Unmarshal(raw, &obj)
	encoded, marshalErr := json.Marshal(obj)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	changed.ExactEntryDigest = audit.DigestBytes(encoded)
	if _, err = m.PrepareTrustAssessment(livePagingContext(t), source.ID, changed); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("re-encoded bytes selected original: %v", err)
	}
	if len(store.snapshots) != 0 || len(store.observations) != 0 {
		t.Fatal("live assessment persisted discovery state")
	}
}
func TestTrustSelectionRejectsChangedAndUnauthorizedTargets(t *testing.T) {
	for _, name := range []string{"disabled", "missing verifier", "policy", "revision", "paused", "removed", "digest", "media", "delivery", "version presence", "bad identifier", "missing text", "missing reason", "invalid observation", "mixed selection", "missing observation", "source scope", "source race", "other tenant", "reader"} {
		t.Run(name, func(t *testing.T) {
			m, store, outbound, source, input, _ := trustSelectionFixture(t)
			ctx := livePagingContext(t)
			beforeContact := true
			switch name {
			case "missing verifier":
				m.trust = nil
			case "disabled":
				policy, ok := m.trust.(*trustTestVerifier)
				if !ok {
					t.Fatal("missing fixture policy")
				}
				policy.enabled = false
			case "policy":
				source.TrustVerificationPolicy = "unknown"
			case "revision":
				input.SourceRevision++
			case "paused":
				source.Status = SourcePaused
			case "removed":
				source.Status = SourceRemoved
			case "digest":
				input.ExactEntryDigest = federationTestPolicyDigest
				beforeContact = false
			case "media":
				input.MediaType = "application/mcp-server-card+json"
				beforeContact = false
			case "delivery":
				input.Delivery = "data"
				beforeContact = false
			case "version presence":
				input.VersionPresent = false
			case "bad identifier":
				input.Identifier = "invalid"
			case "missing text":
				input.Text = ""
			case "missing reason":
				input.Reason = ""
			case "invalid observation":
				input.ObservationID, input.Text = "invalid", ""
			case "mixed selection":
				input.ObservationID = "00000000-0000-7000-8000-000000000031"
			case "missing observation":
				input.ObservationID, input.Text = "00000000-0000-7000-8000-000000000031", ""
			case "source scope":
				source.Scope = Scope{Publishers: []string{"elsewhere.example"}}
				beforeContact = false
			case "source race":
				beforeContact = false
				original := outbound.respond
				outbound.respond = func(c context.Context, r fetcher.Request) (fetcher.Result, error) {
					next := store.sources[source.ID]
					next.Revision++
					store.sources[source.ID] = next
					return original(c, r)
				}
			case "other tenant":
				ctx = identityfixture.WithActor(t.Context(), identity.Actor{TenantID: "00000000-0000-7000-8000-000000000099", Subject: "admin", Roles: []identity.Role{identity.RoleTenantAdmin}})
			case "reader":
				ctx = identityfixture.WithActor(t.Context(), identity.Actor{TenantID: source.TenantID, Subject: "reader", Roles: []identity.Role{identity.RoleReader}})
			}
			store.sources[source.ID] = source
			if _, err := m.PrepareTrustAssessment(ctx, source.ID, input); err == nil {
				t.Fatal("invalid selection admitted")
			}
			if beforeContact && len(outbound.requests) != 0 {
				t.Fatal("refusal contacted peer")
			}
		})
	}
}

type failedSnapshotEvidenceStore struct {
	Store
	err error
}

func (s failedSnapshotEvidenceStore) ObservationHasSnapshotEvidence(context.Context, string, string, string, string, int64, string) (bool, error) {
	return false, s.err
}

func TestTrustIndexedSelectionAndHistoricalInterpretation(t *testing.T) {
	m, store, outbound, source, input, raw := trustSelectionFixture(t)
	source.LastCompleteSnapshotID = "00000000-0000-7000-8000-000000000030"
	store.sources[source.ID] = source
	input.Text = ""
	input.ObservationID = "00000000-0000-7000-8000-000000000031"
	observation := Observation{ID: input.ObservationID, TenantID: source.TenantID, SourceID: source.ID, SnapshotID: source.LastCompleteSnapshotID, Identifier: input.Identifier, Version: input.Version, VersionPresent: true, MediaType: input.MediaType, Freshness: FreshCurrent, RawEntryAvailable: true, RawEntry: raw, ExactEntryDigest: input.ExactEntryDigest, Occurrence: ObservationOccurrence{}}
	store.observations[observation.ID] = observation
	occurrence := ObservationOccurrence{ObservationID: observation.ID, SourceID: source.ID, SnapshotID: source.LastCompleteSnapshotID, SourceRevision: source.Revision, ExactEntryDigest: observation.ExactEntryDigest, EvidenceComplete: true}
	store.occurrences[occurrenceKey(source.TenantID, occurrence)] = occurrence
	failure := errors.New("snapshot evidence read failed")
	m.store = failedSnapshotEvidenceStore{Store: store, err: failure}
	if _, err := m.PrepareTrustAssessment(livePagingContext(t), source.ID, input); !errors.Is(err, failure) {
		t.Fatal("lost evidence read failure", err)
	}
	m.store = store
	p, err := m.PrepareTrustAssessment(livePagingContext(t), source.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(outbound.requests) != 0 || string(p.Entry) != string(raw) {
		t.Fatal("indexed selection used network or changed bytes")
	}
	payload, marshalErr := json.Marshal(p)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	now := m.clock.Now()
	until := now.Add(time.Hour)
	assessment := jobs.TrustAssessment{SourceID: source.ID, SourceRevision: 1, PolicyName: p.PolicyName, PolicyDigest: p.PolicyDigest, ExactEntryDigest: p.ExactEntryDigest, Authority: "owned", MediaType: p.MediaType, Delivery: p.Delivery, SoftwareTrust: "not_assessed", ArtifactIntegrity: "not_assessed", Rights: "not_assessed", Evidence: anstrust.Bundle{Profile: anstrust.Profile, Entry: raw, Receipt: []byte{}, Checkpoint: []byte{}, StatusToken: []byte{}}, Assessment: anstrust.Result{Profile: anstrust.Profile, PolicyDigest: strings.Repeat("b", 64), EntryDigest: strings.TrimPrefix(p.ExactEntryDigest, "sha256:"), CheckedAt: now, ValidUntil: &until, CurrentRegistration: true, Receipt: anstrust.Check{State: "valid", Code: "valid"}, Producer: anstrust.Check{State: "valid", Code: "valid"}, Log: anstrust.Check{State: "valid", Code: "valid"}, Checkpoint: anstrust.Check{State: "valid", Code: "valid"}, Inclusion: anstrust.Check{State: "valid", Code: "valid"}, Target: anstrust.Check{State: "valid", Code: "valid"}, Status: anstrust.Check{State: "valid", Code: "valid"}}}
	result, marshalErr := json.Marshal(assessment)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	sink := &recordingFederationEvidence{}
	m.evidence = sink
	readContext := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: source.TenantID, Subject: "source-admin", MembershipID: auditEvidenceMembershipID, RealmID: auditEvidenceRealmID, ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleSourceAdmin}})
	job := jobs.Job{ID: "00000000-0000-7000-8000-000000000039", Kind: jobs.TrustKind, PayloadVersion: 1, Payload: payload, Result: result, TenantID: source.TenantID, State: jobs.StateSucceeded}
	reader := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: source.TenantID, Subject: "reader", Roles: []identity.Role{identity.RoleReader}})
	if _, err := m.ReadTrustAssessment(reader, source.ID, job); catalog.ErrorCodeOf(err) != catalog.CodeForbidden || len(sink.audit) != 0 {
		t.Fatal("Reader disclosed private evidence or generated successful-read audit", err)
	}
	ids := m.ids
	for _, name := range []string{"current", "expired", "disabled", "policy rotation", "source removed", "source revision", "negative", "pending", "other source", "malformed result", "identifier failure", "audit unavailable", "audit failure"} {
		t.Run(name, func(t *testing.T) {
			m.trust = &trustTestVerifier{true, federationTestPolicyDigest}
			m.clock = clock.NewFake(now)
			sink.err = nil
			m.evidence = sink
			m.ids = ids
			store.sources[source.ID] = source
			j := job
			key := source.ID
			j.Result = append([]byte(nil), result...)
			want := "historical"
			switch name {
			case "current":
				want = "current"
			case "expired":
				m.clock = clock.NewFake(until)
				want = "expired"
			case "disabled":
				policy, ok := m.trust.(*trustTestVerifier)
				if !ok {
					t.Fatal("missing fixture policy")
				}
				policy.enabled = false
			case "policy rotation":
				policy, ok := m.trust.(*trustTestVerifier)
				if !ok {
					t.Fatal("missing fixture policy")
				}
				policy.digest = "sha256:" + strings.Repeat("b", 64)
			case "source removed":
				s := source
				s.Status = SourceRemoved
				store.sources[source.ID] = s
			case "source revision":
				s := source
				s.Revision++
				store.sources[source.ID] = s
			case "negative":
				a := assessment
				a.Assessment.CurrentRegistration = false
				j.Result, marshalErr = json.Marshal(a)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				want = "negative_or_insufficient"
			case "pending":
				j.State = jobs.StateRunning
			case "other source":
				key = "absent"
			case "malformed result":
				j.Result = []byte(`{}`)
			case "identifier failure":
				m.ids = failingMaintenanceIDGenerator{errors.New("injected identifier failure")}
			case "audit unavailable":
				m.evidence = nil
			case "audit failure":
				sink.err = errors.New("injected audit failure")
			}
			got, err := m.ReadTrustAssessment(readContext, key, j)
			if name == "pending" || name == "other source" || name == "malformed result" || name == "identifier failure" || name == "audit unavailable" || name == "audit failure" {
				if err == nil {
					t.Fatal("unavailable result returned success")
				}
				return
			}
			if err != nil || got.Applicability != want || got.Current != (want == "current") {
				t.Fatalf("interpretation=%+v err=%v", got, err)
			}
		})
	}
	m.ids = ids
	for _, event := range sink.audit {
		if event.Action != audit.ActionSourceTrustRead || event.TargetID != job.ID || audit.ValidateNewRecord(event) != nil {
			t.Fatalf("invalid disclosure evidence: %+v", event)
		}
	}
	observation.Freshness = FreshWithdrawn
	store.observations[observation.ID] = observation
	if _, err = m.PrepareTrustAssessment(livePagingContext(t), source.ID, input); err == nil {
		t.Fatal("withdrawn indexed target admitted")
	}
}

func TestTrustIndexedSelectionRequiresRetainedCurrentSnapshotEvidence(t *testing.T) {
	for _, scenario := range []string{"current after resync", "missing", "other tenant", "other source", "other observation", "old snapshot", "old revision", "different bytes", "incomplete"} {
		t.Run(scenario, func(t *testing.T) {
			m, store, outbound, source, input, raw := trustSelectionFixture(t)
			source.LastCompleteSnapshotID = "00000000-0000-7000-8000-000000000030"
			store.sources[source.ID] = source
			input.Text = ""
			input.ObservationID = "00000000-0000-7000-8000-000000000031"
			observation := Observation{ID: input.ObservationID, TenantID: source.TenantID, SourceID: source.ID, SnapshotID: "original-snapshot", Identifier: input.Identifier, Version: input.Version, VersionPresent: true, MediaType: input.MediaType, Freshness: FreshCurrent, RawEntryAvailable: true, RawEntry: raw, ExactEntryDigest: input.ExactEntryDigest}
			store.observations[observation.ID] = observation
			occurrence := ObservationOccurrence{ObservationID: observation.ID, SourceID: source.ID, SnapshotID: source.LastCompleteSnapshotID, SourceRevision: source.Revision, ExactEntryDigest: observation.ExactEntryDigest, EvidenceComplete: true}
			tenant := source.TenantID
			switch scenario {
			case "other tenant":
				tenant = "00000000-0000-7000-8000-000000000099"
			case "other source":
				occurrence.SourceID = "other-source"
			case "other observation":
				occurrence.ObservationID = "other-observation"
			case "old snapshot":
				occurrence.SnapshotID = "original-snapshot"
			case "old revision":
				occurrence.SourceRevision++
			case "different bytes":
				occurrence.ExactEntryDigest = audit.DigestBytes([]byte("other"))
			case "incomplete":
				occurrence.EvidenceComplete = false
			}
			if scenario != "missing" {
				store.occurrences[occurrenceKey(tenant, occurrence)] = occurrence
			}
			result, err := m.PrepareTrustAssessment(livePagingContext(t), source.ID, input)
			if scenario == "current after resync" {
				if err != nil || !bytes.Equal(result.Entry, raw) {
					t.Fatal("current retained evidence rejected", err)
				}
			} else if catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
				t.Fatal("invalid evidence admitted", err)
			}
			if len(outbound.requests) != 0 {
				t.Fatal("indexed selection contacted a peer")
			}
		})
	}
}

func TestTrustLiveNativeVariantsRemainSelectable(t *testing.T) {
	m, _, outbound, source, input, raw := trustSelectionFixture(t)
	other := bytes.Replace(raw, []byte("application/a2a-agent-card+json"), []byte("application/mcp-server-card+json"), 1)
	outbound.respond = func(context.Context, fetcher.Request) (fetcher.Result, error) {
		return fetcher.Result{Status: 200, MediaType: "application/json", Body: append(append(append([]byte(`{"results":[`), raw...), ','), append(other, []byte("]}")...)...)}, nil
	}
	input.MediaType = "application/mcp-server-card+json"
	input.ExactEntryDigest = audit.DigestBytes(other)
	got, err := m.PrepareTrustAssessment(livePagingContext(t), source.ID, input)
	if err != nil || !bytes.Equal(got.Entry, other) {
		t.Fatalf("native variant lost: %v", err)
	}
}

func TestTrustRequestPreservesExplicitVersionPresence(t *testing.T) {
	base := `{"sourceRevision":1,"identifier":"urn:air:peer.example:agents:one","versionPresent":false,"mediaType":"application/a2a-agent-card+json","delivery":"url","exactEntryDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","text":"one","reason":"Assess selected registration"}`
	for _, scenario := range []string{"absent version", "present version", "indexed selection", "missing selector", "empty extra observation", "empty extra text", "missing presence", "absent version with empty field", "present empty version", "missing version", "null", "duplicate", "wrong case", "unknown", "trailing"} {
		t.Run(scenario, func(t *testing.T) {
			raw := base
			switch scenario {
			case "indexed selection":
				raw = strings.Replace(raw, `"text":"one"`, `"observationId":"00000000-0000-7000-8000-000000000031"`, 1)
			case "missing selector":
				raw = strings.Replace(raw, `"text":"one",`, "", 1)
			case "empty extra observation":
				raw = strings.Replace(raw, `"text":"one"`, `"text":"one","observationId":""`, 1)
			case "empty extra text":
				raw = strings.Replace(raw, `"text":"one"`, `"text":"","observationId":"00000000-0000-7000-8000-000000000031"`, 1)
			case "present version":
				raw = strings.Replace(raw, `"versionPresent":false`, `"versionPresent":true,"version":"1.0"`, 1)
			case "missing presence":
				raw = strings.Replace(raw, `"versionPresent":false,`, "", 1)
			case "absent version with empty field":
				raw = strings.Replace(raw, `"versionPresent":false`, `"versionPresent":false,"version":""`, 1)
			case "present empty version":
				raw = strings.Replace(raw, `"versionPresent":false`, `"versionPresent":true,"version":""`, 1)
			case "missing version":
				raw = strings.Replace(raw, `"versionPresent":false`, `"versionPresent":true`, 1)
			case "null":
				raw = strings.Replace(raw, `"versionPresent":false`, `"versionPresent":null`, 1)
			case "duplicate":
				raw = strings.Replace(raw, `"versionPresent":false`, `"versionPresent":false,"versionPresent":true`, 1)
			case "wrong case":
				raw = strings.Replace(raw, `"versionPresent"`, `"VersionPresent"`, 1)
			case "unknown":
				raw = strings.TrimSuffix(raw, "}") + `,"receipt":"client-supplied"}`
			case "trailing":
				raw += `{}`
			}
			var request TrustRequest
			err := json.Unmarshal([]byte(raw), &request)
			wantValid := scenario == "absent version" || scenario == "present version" || scenario == "present empty version" || scenario == "indexed selection"
			if (err == nil) != wantValid {
				t.Fatalf("version presence admission: %v", err)
			}
		})
	}
}

func TestTrustResultAssemblyBindsAuditAndBounds(t *testing.T) {
	for _, scenario := range []string{"result", "oversized", "identifier failure"} {
		t.Run(scenario, func(t *testing.T) {
			m, _, outbound, source, input, entry := trustSelectionFixture(t)
			p := jobs.TrustPayload{SourceID: source.ID, SourceRevision: source.Revision, SourcePolicyDigest: source.PolicyDigest, PolicyName: "owned", PolicyDigest: federationTestPolicyDigest, Identifier: input.Identifier, Version: input.Version, VersionPresent: input.VersionPresent, MediaType: input.MediaType, Delivery: input.Delivery, ExactEntryDigest: input.ExactEntryDigest, Entry: entry}
			payload, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			job := jobs.Job{ID: "00000000-0000-7000-8000-000000000039", TenantID: source.TenantID, Actor: "00000000-0000-7000-8000-000000000040", Kind: jobs.TrustKind, PayloadVersion: 1, Payload: payload, Attempt: 2}
			na := anstrust.Check{State: "not_assessed", Code: "prerequisite_failed"}
			result := anstrust.Result{Profile: anstrust.Profile, PolicyDigest: strings.Repeat("b", 64), EntryDigest: strings.TrimPrefix(p.ExactEntryDigest, "sha256:"), CheckedAt: m.clock.Now(), Receipt: na, Producer: na, Log: na, Checkpoint: na, Inclusion: na, Target: na, Status: na}
			bundle := anstrust.Bundle{Profile: anstrust.Profile, Entry: entry, Receipt: []byte{}, Checkpoint: []byte{}, StatusToken: []byte{}}
			sentinel := errors.New("injected identifier failure")
			if scenario == "oversized" {
				result.Subject = strings.Repeat("x", jobs.MaxTrustRecordBytes)
			} else if scenario == "identifier failure" {
				m.ids = failingMaintenanceIDGenerator{sentinel}
			}
			ctx := audit.WithJobExecution(t.Context(), audit.JobExecution{ID: job.ID, Kind: job.Kind, Attempt: job.Attempt, Initiator: job.Actor, Trigger: "manual"})
			got, err := m.trustAssessmentResult(ctx, job, p, bundle, result)
			if len(outbound.requests) != 0 {
				t.Fatal("result assembly performed network acquisition")
			}
			if scenario == "oversized" {
				if catalog.ErrorCodeOf(err) != catalog.CodeInternal {
					t.Fatal("oversized result was admitted", err)
				}
				return
			}
			if scenario == "identifier failure" {
				if !errors.Is(err, sentinel) {
					t.Fatal("audit identifier failure was hidden", err)
				}
				return
			}
			if err != nil || got.Authority != "owned-authority" || !bytes.Equal(got.Evidence.Entry, entry) {
				t.Fatalf("assembled result=%+v err=%v", got, err)
			}
			raw, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			if err = jobs.ValidateTrustCompletion(job, raw, got.EvidenceRecord); err != nil {
				t.Fatal("result and completion audit disagree", err)
			}
		})
	}
}

func TestTrustJobAdmissionSeparatesHistoryFromExecution(t *testing.T) {
	for _, scenario := range []string{"current", "malformed", "reader", "foreign tenant", "revision", "removed", "credentials", "credential binding", "egress changed", "egress denied", "validation dependency", "policy", "missing verifier", "lookup failure", "unclaimed execution", "malformed execution", "missing principal"} {
		t.Run(scenario, func(t *testing.T) {
			m, store, outbound, source, input, entry := trustSelectionFixture(t)
			p := jobs.TrustPayload{SourceID: source.ID, SourceRevision: source.Revision, SourcePolicyDigest: source.PolicyDigest, PolicyName: "owned", PolicyDigest: federationTestPolicyDigest, Identifier: input.Identifier, Version: input.Version, VersionPresent: input.VersionPresent, MediaType: input.MediaType, Delivery: input.Delivery, ExactEntryDigest: input.ExactEntryDigest, Entry: entry}
			payload, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			job := jobs.Job{ID: "00000000-0000-7000-8000-000000000039", TenantID: source.TenantID, Kind: jobs.TrustKind, PayloadVersion: 1, Payload: payload}
			ctx := livePagingContext(t)
			switch scenario {
			case "malformed":
				job.Payload = []byte(`{}`)
			case "reader":
				ctx = identityfixture.WithActor(t.Context(), identity.Actor{TenantID: source.TenantID, Subject: "reader", Roles: []identity.Role{identity.RoleReader}})
			case "foreign tenant":
				job.TenantID = "00000000-0000-7000-8000-000000000099"
			case "revision":
				source.Revision++
			case "removed":
				source.Status = SourceRemoved
			case "credentials":
				source.CredentialRevision = "new-generation"
			case "credential binding":
				m.trust = refusedTrustSource{m.trust, fetcher.ErrCredentialChanged}
			case "egress changed":
				m.trust = refusedTrustSource{m.trust, fetcher.ErrPolicyChanged}
			case "egress denied":
				m.trust = refusedTrustSource{m.trust, fetcher.ErrPolicyDenied}
			case "validation dependency":
				m.trust = refusedTrustSource{m.trust, catalog.E(catalog.CodeDependencyUnavailable, "injected validation outage")}
			case "policy":
				m.trust = &trustTestVerifier{true, "sha256:" + strings.Repeat("b", 64)}
			case "missing verifier":
				m.trust = nil
			case "lookup failure":
				m.store = &decisionSourceStore{Store: store, err: catalog.E(catalog.CodeDependencyUnavailable, "injected store outage")}
			}
			store.sources[source.ID] = source
			if scenario == "malformed execution" {
				job.Payload = []byte(`{}`)
				if _, err := m.ExecuteTrustAssessment(ctx, job); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
					t.Fatal("malformed work crossed execution admission", err)
				}
				return
			}
			if scenario == "missing principal" {
				if err := m.checkTrustTarget(t.Context(), p); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
					t.Fatal("missing principal crossed source admission", err)
				}
				return
			}
			if scenario == "unclaimed execution" {
				if _, err := m.ExecuteTrustAssessment(ctx, job); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
					t.Fatal("administrator context manufactured a worker claim", err)
				}
				return
			}
			readErr := m.ValidateTrustJob(ctx, job)
			wantRead := scenario != "malformed" && scenario != "reader" && scenario != "foreign tenant"
			if (readErr == nil) != wantRead {
				t.Fatal("historical disclosure used execution eligibility", readErr)
			}
			// Enqueue receives a payload selected for the current request tenant.
			if scenario == "foreign tenant" {
				ctx = identityfixture.WithActor(t.Context(), identity.Actor{TenantID: job.TenantID, Subject: "admin", Roles: []identity.Role{identity.RoleTenantAdmin}})
			}
			err = m.ValidateTrustEnqueue(ctx, job)
			if (err == nil) != (scenario == "current") || len(outbound.requests) != 0 {
				t.Fatalf("execution admission: err=%v peer calls=%d", err, len(outbound.requests))
			}
			if scenario == "credential binding" || scenario == "egress changed" || scenario == "egress denied" {
				if catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
					t.Fatal("changed source authority was not a permanent precondition refusal", err)
				}
			}
			if scenario == "validation dependency" && catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
				t.Fatal("validation dependency failure became a permanent refusal", err)
			}
		})
	}
}

func TestTrustSourcePolicySelectionAndClearing(t *testing.T) {
	m, _, _, source, _, _ := trustSelectionFixture(t)
	source.DisplayName, source.UpdateMode = "Owned peer", UpdateManual
	if err := WithDiscoveryTargets([]DiscoveryTarget{{Host: "catalog.example.com", TenantID: source.TenantID, Audience: AudienceAnonymous}})(m); err != nil {
		t.Fatal(err)
	}
	if _, err := m.normalizeAndValidateSource(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	before, err := sourceConfigurationDigest(source)
	if err != nil {
		t.Fatal(err)
	}
	emptyPolicy := ""
	patch := PatchInput{TrustVerificationPolicy: &emptyPolicy}
	if !hasSourcePatch(patch) {
		t.Fatal("policy-only patch was discarded")
	}
	updated, err := applySourcePatch(source, patch)
	if err != nil || updated.TrustVerificationPolicy != "" {
		t.Fatal("policy was not cleared", err)
	}
	after, err := sourceConfigurationDigest(updated)
	if err != nil || after == before {
		t.Fatal("source policy omitted from configuration binding", err)
	}
	source.TrustVerificationPolicy = "unknown"
	if _, err = m.normalizeAndValidateSource(t.Context(), source); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatal("enabled unknown policy admitted", err)
	}
	m.trust = &trustTestVerifier{false, federationTestPolicyDigest}
	if _, err = m.normalizeAndValidateSource(t.Context(), source); err != nil {
		t.Fatal("disabled policy reference should remain inert", err)
	}
	source.TrustVerificationPolicy = "../unsafe"
	if _, err = m.normalizeAndValidateSource(t.Context(), source); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatal("disabled malformed reference admitted", err)
	}
}
