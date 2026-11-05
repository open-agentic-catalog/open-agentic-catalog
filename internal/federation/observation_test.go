package federation

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

func TestObservationNormalizationSeparatesLexicalAndTypedVersionIdentity(t *testing.T) {
	identifier := "urn:air:source.example:default:agent"
	left := sourceEntry{Identifier: identifier, DisplayName: "Example Agent", Description: "Same meaning", Type: "application/a2a-agent-card+json", Data: json.RawMessage(`{"name":"Example","sequence":9007199254740993,"capabilities":{}}`), Tags: []string{"search", "agent"}, RepresentativeQueries: []string{"Find   example", "Use example"}, Version: "1", VersionPresent: true, Metadata: json.RawMessage(`{"count":9007199254740993,"source":"test"}`), UnknownProperties: map[string]any{"extension": map[string]any{"enabled": true, "sequence": json.Number("9007199254740993")}}}
	right := left
	right.Data = json.RawMessage("{\n  \"capabilities\": {}, \"sequence\": 9007199254740993, \"name\": \"Example\"\n}")
	right.Metadata = json.RawMessage(`{"source":"test","count":9007199254740993}`)
	right.UnknownProperties = map[string]any{"extension": map[string]any{"sequence": json.Number("9007199254740993"), "enabled": true}}
	right.Tags = []string{"agent", "search"}
	right.RepresentativeQueries = []string{"Use example", "Find example"}
	leftBytes, leftDigest := normalizeObservationEntry(left, identifier, catalog.KindAgent)
	rightBytes, rightDigest := normalizeObservationEntry(right, identifier, catalog.KindAgent)
	if leftDigest == "" || leftDigest != rightDigest || string(leftBytes) != string(rightBytes) {
		t.Fatalf("formatting-only normalization differs: left %q %s right %q %s", leftBytes, leftDigest, rightBytes, rightDigest)
	}
	var normalized normalizedObservationEntry
	if err := json.Unmarshal(leftBytes, &normalized); err != nil || !bytes.Contains(normalized.Data, []byte(`9007199254740993`)) || !bytes.Contains(normalized.Metadata, []byte(`9007199254740993`)) {
		t.Fatalf("normalized evidence changed exact JSON numbers: %s, %v", leftBytes, err)
	}
	unversioned := left
	unversioned.Version = ""
	unversioned.VersionPresent = false
	_, unversionedDigest := normalizeObservationEntry(unversioned, identifier, catalog.KindAgent)
	if unversionedDigest == leftDigest || sourceScopedObservationKey(identifier, "", false, "", "", "") == sourceScopedObservationKey(identifier, "", true, "", "", "") {
		t.Fatal("typed version presence was collapsed during observation identity")
	}
}

func TestObservationParsingPreservesVersionPresenceAndUnknownFields(t *testing.T) {
	document, err := parseDocumentWithContextInputs([]byte(`{"entries":[{"identifier":"urn:air:source.example:default:agent","displayName":"Agent","type":"application/a2a-agent-card+json","data":{"name":"Agent"},"version":"","x-extension":{"count":9007199254740993}}]}`), "application/ard+json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(document.entries) != 1 || !document.entries[0].VersionPresent || document.entries[0].Version != "" {
		t.Fatalf("typed version presence was not preserved: %#v", document.entries)
	}
	value, ok := document.entries[0].UnknownProperties["x-extension"].(map[string]any)
	if !ok || value["count"] != json.Number("9007199254740993") {
		t.Fatalf("unknown extension was not preserved without numeric loss: %#v", document.entries[0].UnknownProperties)
	}
	withoutVersion, err := parseDocumentWithContextInputs([]byte(`{"entries":[{"identifier":"urn:air:source.example:default:agent","displayName":"Agent","type":"application/a2a-agent-card+json","data":{"name":"Agent"}}]}`), "application/ard+json", nil)
	if err != nil || len(withoutVersion.entries) != 1 || withoutVersion.entries[0].VersionPresent {
		t.Fatalf("absent version was not preserved: %#v, %v", withoutVersion.entries, err)
	}
}

func TestARDIngestionPreservesTrustManifestSpellingAndTerminalIdentifier(t *testing.T) {
	entry := `{"identifier":"urn:air:source.example:terminal-agent","displayName":"Terminal Agent","type":"application/a2a-agent-card+json","data":{"name":"Terminal Agent"},"representativeQueries":["Find the terminal agent","Show the terminal agent"],"TrustManifest":{"identity":"did:web:source.example"}}`
	document, err := parseDocumentWithContextInputs([]byte(`{"entries":[`+entry+`]}`), "application/ard+json", nil)
	if err != nil || len(document.entries) != 1 {
		t.Fatalf("terminal schema-cased entry = %#v, %v", document.entries, err)
	}
	parsed := document.entries[0]
	if string(parsed.Raw) != entry || parsed.Identifier != "urn:air:source.example:terminal-agent" {
		t.Fatalf("raw entry or terminal identifier changed: %#v", parsed)
	}
	if _, ok := parsed.UnknownProperties["trustManifest"]; !ok {
		t.Fatalf("normalized lower-case Trust Manifest missing: %#v", parsed.UnknownProperties)
	}
	if _, ok := parsed.UnknownProperties["TrustManifest"]; ok {
		t.Fatalf("schema-cased Trust Manifest leaked into normalized projection: %#v", parsed.UnknownProperties)
	}
	ambiguous, err := parseDocumentWithContextInputs([]byte(`{"entries":[{"identifier":"urn:air:source.example:terminal-agent","displayName":"Terminal Agent","type":"application/a2a-agent-card+json","data":{},"trustManifest":{"identity":"did:web:source.example"},"TrustManifest":{"identity":"did:web:source.example"}}]}`), "application/ard+json", nil)
	if err != nil || len(ambiguous.entries) != 1 || !hasFinding(ambiguous.entries[0].StructuralFindings, "ARD_JSONLD_EXPANSION_FAILED") {
		t.Fatalf("ambiguous Trust Manifest was not retained as invalid evidence: %#v, %v", ambiguous.entries, err)
	}
	ambiguousObservation, err := (&Manager{ids: id.Generator{}}).observation("tenant", Source{ID: "source", TenantID: "tenant", ProfileID: "ard-0.91-aa3e598", Revision: 1}, "snapshot", ambiguous.entries[0], 0, time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC))
	if err != nil || ambiguousObservation.Compatibility.Structural != "invalid" || ambiguousObservation.NormalizedEntryDigest != "" {
		t.Fatalf("ambiguous Trust Manifest entered a compatible projection: %#v, %v", ambiguousObservation, err)
	}
	source := Source{ID: "source", TenantID: "tenant", ProfileID: "ard-0.91-aa3e598", Revision: 1}
	observation, err := (&Manager{ids: id.Generator{}}).observation("tenant", source, "snapshot", parsed, 0, time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC))
	if err != nil || observation.Identifier != parsed.Identifier || observation.Compatibility.Structural != "valid" || observation.NormalizedEntryDigest == "" {
		t.Fatalf("terminal observation = %#v, %v", observation, err)
	}
	for _, spelling := range []string{"trustManifest", "TrustManifest"} {
		for name, trust := range map[string]string{
			"attestation URI type": `{"identity":"did:web:source.example","attestations":[{"type":"custom","uri":17}]}`,
			"signature type":       `{"identity":"did:web:source.example","signature":{}}`,
			"framework type":       `{"identity":"did:web:source.example","trustSchema":[]}`,
			"publisher mismatch":   `{"identity":"https://unrelated.example"}`,
		} {
			t.Run(spelling+"/"+name, func(t *testing.T) {
				raw := strings.Replace(entry, `"TrustManifest":{"identity":"did:web:source.example"}`, `"`+spelling+`":`+trust, 1)
				document, err := parseDocumentWithContextInputs([]byte(`{"entries":[`+raw+`]}`), "application/ard+json", nil)
				if err != nil || len(document.entries) != 1 {
					t.Fatalf("invalid trust evidence was lost: entries=%+v err=%v", document.entries, err)
				}
				retained := document.entries[0]
				if string(retained.Raw) != raw || !hasFinding(retained.StructuralFindings, "ARD_ENTRY_SCHEMA_INVALID") {
					t.Fatalf("invalid trust lacked original bytes or schema finding: %+v", retained)
				}
				observation, err := (&Manager{ids: id.Generator{}}).observation("tenant", source, "snapshot", retained, 0, time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC))
				if err != nil || observation.Compatibility.Structural != "invalid" || observation.NormalizedEntryDigest != "" || len(observation.NormalizedEntry) != 0 || observation.SearchDisplayName != "" || string(observation.RawEntry) != raw || observation.ExactEntryDigest == "" {
					t.Fatalf("invalid trust entered a search projection or lost retained evidence: %+v err=%v", observation, err)
				}
			})
		}
	}
}

func TestCurrentWriterEntryLocationIsCanonical(t *testing.T) {
	for _, value := range []string{"/entries/0", "/entries/42", "/items/0", "/items/42"} {
		if !validEntryLocation(value) {
			t.Fatalf("valid location %q was rejected", value)
		}
	}
	for _, value := range []string{"", "/entries/", "/entries/00", "/entries/-1", "/entries/1/child", "/items/", "/items/00", "/items/-1", "/items/1/child", "/other/1", "/legacy/observations/id", " /entries/1", "/entries/1\x00", "/items/1\x7f", "/entries/1\n2"} {
		if validEntryLocation(value) {
			t.Fatalf("invalid location %q was accepted", value)
		}
	}
}

func TestSnapshotReconciliationRequiresCompleteEnumeration(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	snapshot := Snapshot{ID: "00000000-0000-7000-8000-000000000020", TenantID: auditEvidenceTenantID, SourceID: "00000000-0000-7000-8000-000000000021", SourceRevision: 2,
		State: SnapshotComplete, EnumerationComplete: true, ReconcileCurrentView: true,
		PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: federationTestPolicyDigest, BodyDigest: federationTestPolicyDigest,
		StartedAt: now, CompletedAt: &now}
	if err := ValidateSnapshotCommit(snapshot, nil); err != nil {
		t.Fatalf("complete current-view reconciliation = %v", err)
	}
	if err := ValidateRetainedSnapshotIntegrity(snapshot); err != nil {
		t.Fatalf("retained complete reconciliation = %v", err)
	}
	for _, state := range []SnapshotState{SnapshotIncomplete, SnapshotFailed} {
		t.Run(string(state), func(t *testing.T) {
			candidate := snapshot
			candidate.State, candidate.EnumerationComplete, candidate.ReconcileCurrentView = state, false, false
			if err := ValidateSnapshotCommit(candidate, nil); err != nil {
				t.Fatalf("non-reconciling %s snapshot = %v", state, err)
			}
			if err := ValidateRetainedSnapshotIntegrity(candidate); err != nil {
				t.Fatalf("retained non-reconciling %s snapshot = %v", state, err)
			}
			candidate.ReconcileCurrentView = true
			if err := ValidateSnapshotCommit(candidate, nil); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument || !strings.Contains(err.Error(), "current-view reconciliation requires a complete source snapshot") {
				t.Fatalf("incomplete reconciliation = %v", err)
			}
			if err := ValidateRetainedSnapshotIntegrity(candidate); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument || !strings.Contains(err.Error(), "timing or authority evidence is invalid") {
				t.Fatalf("retained incomplete reconciliation = %v", err)
			}
		})
	}
}

func TestRetainedRawOnlyObservationCannotCarrySearchProjection(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	raw := json.RawMessage(`{"type":"application/future+json","data":{"opaque":true}}`)
	// Independently computed SHA-256 of the literal exact bytes above.
	exactDigest := "sha256:06cd766fcf518d8f2e4ba16daa4b6345c7e35d7153905e8bd87a21bf26a8e74c"
	observation := Observation{RawEntry: raw, ExactEntryDigest: exactDigest, IdentityDigest: exactDigest, FirstSeenAt: now, LastSeenAt: now}
	if err := ValidateRetainedObservationIntegrity(observation, "", false); err != nil {
		t.Fatalf("exact unsupported observation without projection = %v", err)
	}
	for name, mutate := range map[string]func(*Observation, *bool){
		"projection marked complete": func(_ *Observation, complete *bool) { *complete = true },
		"display name":               func(value *Observation, _ *bool) { value.SearchDisplayName = "Future resource" },
		"description":                func(value *Observation, _ *bool) { value.SearchDescription = "Unnormalized description" },
		"tags":                       func(value *Observation, _ *bool) { value.SearchTags = []string{"future"} },
		"queries":                    func(value *Observation, _ *bool) { value.SearchQueries = []string{"Find future"} },
		"publisher":                  func(value *Observation, _ *bool) { value.SearchPublisher = "source.example" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate, complete := observation, false
			mutate(&candidate, &complete)
			if err := ValidateRetainedObservationIntegrity(candidate, "", complete); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || !strings.Contains(err.Error(), "derived projection exists without normalized evidence") {
				t.Fatalf("orphan projection = %v", err)
			}
		})
	}
}

func TestValidateSnapshotCommitRejectsIncompleteOrMismatchedEvidence(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	raw := json.RawMessage(`{"identifier":"urn:air:source.example:default:agent"}`)
	normalized := json.RawMessage(`{"profile":"catalog-observation-v1","identifier":"urn:air:source.example:default:agent","versionPresent":false,"version":"","kind":"agent","mediaType":"application/a2a-agent-card+json","displayName":"","description":"","url":"","data":null,"tags":null,"representativeQueries":[],"metadata":null}`)
	exactDigest := observationDigest(raw)
	normalizedDigest := observationDigest(normalized)
	snapshot := Snapshot{ID: "snapshot", TenantID: "tenant", SourceID: "source", SourceRevision: 2, State: SnapshotComplete, EnumerationComplete: true, WithdrawalAuthority: true, MemberCount: 1, StartedAt: now, CompletedAt: &now}
	observation := Observation{IdentityDigest: exactDigest,
		ID: "observation", TenantID: snapshot.TenantID, SourceID: snapshot.SourceID, SnapshotID: snapshot.ID, SourceKey: "identity", Identifier: "urn:air:source.example:default:agent", Kind: catalog.KindAgent, MediaType: "application/a2a-agent-card+json", Completeness: CompleteEntry, Compatibility: Compatibility{Structural: "valid", ProfileID: "ard-test", Support: "supported"}, RawEntry: raw, NormalizedEntry: normalized, RawEntryAvailable: true,
		ExactEntryDigest: exactDigest, NormalizedEntryDigest: normalizedDigest, NormalizationProfile: observationNormalizationProfile, SearchPublisher: "source.example", Freshness: FreshCurrent, FirstSeenAt: now, LastSeenAt: now,
		Occurrence: ObservationOccurrence{ObservationID: "observation", SourceID: snapshot.SourceID, SnapshotID: snapshot.ID, SourceRevision: snapshot.SourceRevision, RetrievalDigest: exactDigest, EntryLocation: "/entries/0", ExactEntryDigest: exactDigest, NormalizedEntryDigest: normalizedDigest, EvidenceComplete: true, ObservedAt: now},
	}
	if err := ValidateSnapshotCommit(snapshot, []Observation{observation}); err != nil {
		t.Fatalf("valid snapshot evidence returned %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Snapshot, *Observation)
	}{
		{name: "missing completion", mutate: func(value *Snapshot, _ *Observation) { value.CompletedAt = nil }},
		{name: "reversed completion", mutate: func(value *Snapshot, _ *Observation) {
			completed := now.Add(-time.Second)
			value.CompletedAt = &completed
		}},
		{name: "member count", mutate: func(value *Snapshot, _ *Observation) { value.MemberCount = 2 }},
		{name: "change count", mutate: func(value *Snapshot, _ *Observation) { value.Added = 2 }},
		{name: "complete enumeration", mutate: func(value *Snapshot, _ *Observation) { value.EnumerationComplete = false }},
		{name: "incomplete authority", mutate: func(value *Snapshot, _ *Observation) {
			value.State = SnapshotIncomplete
			value.EnumerationComplete = false
		}},
		{name: "failed enumeration", mutate: func(value *Snapshot, _ *Observation) { value.State = SnapshotFailed }},
		{name: "running", mutate: func(value *Snapshot, _ *Observation) { value.State = SnapshotRunning }},
		{name: "unknown state", mutate: func(value *Snapshot, _ *Observation) { value.State = SnapshotState("unknown") }},
		{name: "observation tenant", mutate: func(_ *Snapshot, value *Observation) { value.TenantID = "other" }},
		{name: "observation occurrence", mutate: func(_ *Snapshot, value *Observation) { value.Occurrence.ObservationID = "other" }},
		{name: "raw unavailable", mutate: func(_ *Snapshot, value *Observation) { value.RawEntryAvailable = false }},
		{name: "raw malformed", mutate: func(_ *Snapshot, value *Observation) { value.RawEntry = json.RawMessage(`{"bad":`) }},
		{name: "raw duplicate key", mutate: func(_ *Snapshot, value *Observation) {
			value.RawEntry = json.RawMessage(`{"identifier":"one","identifier":"two"}`)
			value.ExactEntryDigest = observationDigest(value.RawEntry)
			value.Occurrence.ExactEntryDigest = value.ExactEntryDigest
			value.Occurrence.RetrievalDigest = value.ExactEntryDigest
		}},
		{name: "raw digest", mutate: func(_ *Snapshot, value *Observation) {
			value.ExactEntryDigest = "sha256:" + string(bytes.Repeat([]byte{'a'}, 64))
		}},
		{name: "identity digest", mutate: func(_ *Snapshot, value *Observation) {
			value.IdentityDigest = "sha256:" + string(bytes.Repeat([]byte{'a'}, 64))
		}},
		{name: "normalization input binding", mutate: func(_ *Snapshot, value *Observation) {
			value.Occurrence.NormalizationInputDigest = "sha256:" + string(bytes.Repeat([]byte{'b'}, 64))
		}},
		{name: "retrieval digest", mutate: func(_ *Snapshot, value *Observation) { value.Occurrence.RetrievalDigest = "invalid" }},
		{name: "entry location", mutate: func(_ *Snapshot, value *Observation) { value.Occurrence.EntryLocation = "/entries/01" }},
		{name: "occurrence completeness", mutate: func(_ *Snapshot, value *Observation) { value.Occurrence.EvidenceComplete = false }},
		{name: "occurrence time", mutate: func(_ *Snapshot, value *Observation) { value.Occurrence.ObservedAt = now.Add(time.Second) }},
		{name: "normalization profile", mutate: func(_ *Snapshot, value *Observation) { value.NormalizationProfile = "unknown" }},
		{name: "normalized digest", mutate: func(_ *Snapshot, value *Observation) { value.NormalizedEntryDigest = exactDigest }},
		{name: "normalized duplicate key", mutate: func(_ *Snapshot, value *Observation) {
			value.NormalizedEntry = json.RawMessage(`{"profile":"catalog-observation-v1","profile":"catalog-observation-v1"}`)
			value.NormalizedEntryDigest = observationDigest(value.NormalizedEntry)
			value.Occurrence.NormalizedEntryDigest = value.NormalizedEntryDigest
		}},
		{name: "normalized occurrence", mutate: func(_ *Snapshot, value *Observation) { value.Occurrence.NormalizedEntryDigest = exactDigest }},
		{name: "normalized identity binding", mutate: func(_ *Snapshot, value *Observation) {
			value.NormalizedEntry = json.RawMessage(`{"profile":"catalog-observation-v1","identifier":"urn:air:other.example:default:agent","versionPresent":false,"version":"","kind":"agent","mediaType":"application/a2a-agent-card+json"}`)
			value.NormalizedEntryDigest = observationDigest(value.NormalizedEntry)
			value.Occurrence.NormalizedEntryDigest = value.NormalizedEntryDigest
		}},
		{name: "orphan normalized occurrence", mutate: func(_ *Snapshot, value *Observation) {
			value.NormalizedEntry = nil
			value.NormalizedEntryDigest = ""
			value.NormalizationProfile = ""
		}},
		{name: "first seen", mutate: func(_ *Snapshot, value *Observation) { value.FirstSeenAt = time.Time{} }},
		{name: "reversed timestamps", mutate: func(_ *Snapshot, value *Observation) { value.LastSeenAt = now.Add(-time.Second) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidateSnapshot := snapshot
			candidateObservation := observation
			test.mutate(&candidateSnapshot, &candidateObservation)
			if err := ValidateSnapshotCommit(candidateSnapshot, []Observation{candidateObservation}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
				t.Fatalf("invalid evidence returned %v", err)
			}
		})
	}
	failed := snapshot
	failed.State, failed.EnumerationComplete, failed.WithdrawalAuthority = SnapshotFailed, false, false
	failed.MemberCount = 0
	if err := ValidateSnapshotCommit(failed, nil); err != nil {
		t.Fatalf("failed snapshot without members = %v", err)
	}
	failed.MemberCount = 1
	if err := ValidateSnapshotCommit(failed, []Observation{observation}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument || !strings.Contains(err.Error(), "a failed source snapshot cannot retain enumerated members") {
		t.Fatalf("failed snapshot retaining a valid member = %v", err)
	}
	duplicateObservation := observation
	duplicateObservation.Occurrence.EntryLocation = "/entries/1"
	if err := ValidateSnapshotCommit(Snapshot{ID: snapshot.ID, TenantID: snapshot.TenantID, SourceID: snapshot.SourceID, SourceRevision: snapshot.SourceRevision, State: SnapshotComplete, EnumerationComplete: true, MemberCount: 2, StartedAt: now, CompletedAt: &now}, []Observation{observation, duplicateObservation}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("duplicate observation identifier returned %v", err)
	}
	duplicateOccurrence := observation
	duplicateOccurrence.ID = "observation-two"
	duplicateOccurrence.Occurrence.ObservationID = duplicateOccurrence.ID
	if err := ValidateSnapshotCommit(Snapshot{ID: snapshot.ID, TenantID: snapshot.TenantID, SourceID: snapshot.SourceID, SourceRevision: snapshot.SourceRevision, State: SnapshotComplete, EnumerationComplete: true, MemberCount: 2, StartedAt: now, CompletedAt: &now}, []Observation{observation, duplicateOccurrence}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("duplicate observation occurrence returned %v", err)
	}
}

func TestValidateSnapshotCommitBindsPrivateConditionalStateToSafeEvidence(t *testing.T) {
	now := time.Date(2026, 9, 1, 13, 0, 0, 0, time.UTC)
	policyDigest := "sha256:" + strings.Repeat("a", 64)
	requestKey := "sha256:" + strings.Repeat("b", 64)
	url := "https://catalog.example/.well-known/ard.json"
	body := []byte(`{"entries":[]}`)
	representationDigest := digestText(string(body))
	// #nosec G101 -- synthetic credential revision evidence, not a secret.
	snapshot := Snapshot{ID: "snapshot", TenantID: "tenant", SourceID: "source", SourceRevision: 2, State: SnapshotComplete, EnumerationComplete: true, PolicyDigest: policyDigest, CredentialRevision: "credential-4", StartedAt: now, CompletedAt: &now}
	snapshot.Retrievals = []RetrievalEvidence{{ID: "retrieval", RequestKey: requestKey, RequestedURLDigest: digestText(url), FinalURLDigest: digestText(url), ProfileID: "ard-test", Status: 200, ReceivedBody: true, RepresentationDigest: representationDigest, FetchedAt: now}}
	snapshot.RetrievalStates = []RetrievalState{{TenantID: snapshot.TenantID, SourceID: snapshot.SourceID, SourceRevision: snapshot.SourceRevision, RequestKey: requestKey, RequestedURL: url, FinalURL: url, RequestIdentityDigest: requestKey, ProfileID: "ard-test", PolicyDigest: policyDigest, CredentialRevision: snapshot.CredentialRevision, RepresentationDigest: representationDigest, MediaType: "application/ard+json", Body: body, ETag: `"catalog-v1"`, VarySelectionDigest: digestText("vary"), UpdatedAt: now}}
	if err := ValidateRetainedRetrievalState(snapshot.RetrievalStates[0], snapshot.PolicyDigest, snapshot.CredentialRevision); err != nil {
		t.Fatalf("valid retained conditional state returned %v", err)
	}
	invalidRetainedState := snapshot.RetrievalStates[0]
	invalidRetainedState.PolicyDigest = requestKey
	if err := ValidateRetainedRetrievalState(invalidRetainedState, snapshot.PolicyDigest, snapshot.CredentialRevision); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("invalid retained conditional state returned %v", err)
	}
	if err := ValidateSnapshotCommit(snapshot, nil); err != nil {
		t.Fatalf("valid conditional evidence returned %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*Snapshot)
	}{
		{name: "retrieval bound", mutate: func(value *Snapshot) { value.Retrievals = make([]RetrievalEvidence, maxSyncRequests+1) }},
		{name: "duplicate retrieval", mutate: func(value *Snapshot) { value.Retrievals = append(value.Retrievals, value.Retrievals[0]) }},
		{name: "invalid reuse semantics", mutate: func(value *Snapshot) { value.Retrievals[0].Reused = true }},
		{name: "request identity", mutate: func(value *Snapshot) { value.RetrievalStates[0].RequestIdentityDigest = policyDigest }},
		{name: "redirect origin", mutate: func(value *Snapshot) {
			value.RetrievalStates[0].FinalURL = "https://other.example/.well-known/ard.json"
		}},
		{name: "policy binding", mutate: func(value *Snapshot) { value.RetrievalStates[0].PolicyDigest = requestKey }},
		{name: "credential binding", mutate: func(value *Snapshot) { value.RetrievalStates[0].CredentialRevision = "credential-3" }},
		{name: "representation body", mutate: func(value *Snapshot) { value.RetrievalStates[0].Body = []byte(`{"entries":[1]}`) }},
		{name: "invalid validator", mutate: func(value *Snapshot) { value.RetrievalStates[0].ETag = `"bad"quote"` }},
		{name: "state on incomplete snapshot", mutate: func(value *Snapshot) { value.State = SnapshotIncomplete; value.EnumerationComplete = false }},
		{name: "received body digest", mutate: func(value *Snapshot) { value.Retrievals[0].RepresentationDigest = "" }},
		{name: "matching evidence", mutate: func(value *Snapshot) { value.Retrievals[0].FinalURLDigest = policyDigest }},
		{name: "duplicate state", mutate: func(value *Snapshot) { value.RetrievalStates = append(value.RetrievalStates, value.RetrievalStates[0]) }},
		{name: "retained and invalidated", mutate: func(value *Snapshot) { value.RetrievalInvalidations = []string{requestKey} }},
		{name: "duplicate invalidation", mutate: func(value *Snapshot) {
			value.RetrievalStates = nil
			value.RetrievalInvalidations = []string{requestKey, requestKey}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := snapshot
			candidate.Retrievals = append([]RetrievalEvidence(nil), snapshot.Retrievals...)
			candidate.RetrievalStates = append([]RetrievalState(nil), snapshot.RetrievalStates...)
			test.mutate(&candidate)
			if err := ValidateSnapshotCommit(candidate, nil); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
				t.Fatalf("invalid conditional evidence returned %v", err)
			}
		})
	}
	failed := Snapshot{ID: "failed", TenantID: "tenant", SourceID: "source", SourceRevision: 2, State: SnapshotFailed, StartedAt: now, CompletedAt: &now, RetrievalInvalidations: []string{requestKey}}
	if err := ValidateSnapshotCommit(failed, nil); err != nil {
		t.Fatalf("failed retrieval-state invalidation returned %v", err)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil || bytes.Contains(encoded, []byte(url)) || bytes.Contains(encoded, []byte(`catalog-v1`)) || !bytes.Contains(encoded, []byte(`requestedUrlDigest`)) {
		t.Fatalf("snapshot JSON exposed private conditional state: %s, %v", encoded, err)
	}
}

func TestValidateRetainedObservationIntegrity(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	identifier := "urn:air:source.example:default:agent"
	entry := sourceEntry{Identifier: identifier, Version: "1", VersionPresent: true, Type: "application/a2a-agent-card+json", DisplayName: "Example Agent", Description: "Retained observation", Tags: []string{"example"}, RepresentativeQueries: []string{"Find example"}}
	normalizedRaw, normalizedDigest := normalizeObservationEntry(entry, identifier, catalog.KindAgent)
	var normalized normalizedObservationEntry
	if err := json.Unmarshal(normalizedRaw, &normalized); err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"identifier":"urn:air:source.example:default:agent"}`)
	exactDigest := observationDigest(raw)
	observation := Observation{
		Identifier: identifier, Version: "1", VersionPresent: true, Kind: catalog.KindAgent, MediaType: entry.Type,
		RawEntry: raw, ExactEntryDigest: exactDigest, IdentityDigest: observationIdentityDigest(exactDigest, ""),
		NormalizedEntry: normalizedRaw, NormalizedEntryDigest: normalizedDigest, NormalizationProfile: observationNormalizationProfile,
		SearchDisplayName: normalized.DisplayName, SearchDescription: normalized.Description, SearchTags: normalizedSearchTags(normalized.Tags),
		SearchQueries: normalized.RepresentativeQueries, SearchPublisher: "source.example", FirstSeenAt: now, LastSeenAt: now.Add(time.Minute),
	}
	if err := ValidateRetainedObservationIntegrity(observation, "", true); err != nil {
		t.Fatalf("current observation = %v", err)
	}
	repairable := observation
	repairable.SearchDescription = ""
	if err := ValidateRetainedObservationIntegrity(repairable, "", false); err != nil {
		t.Fatalf("repairable observation = %v", err)
	}
	legacy := Observation{ExactEntryDigest: exactDigest, FirstSeenAt: now, LastSeenAt: now}
	if err := ValidateRetainedObservationIntegrity(legacy, "", false); err == nil {
		t.Fatalf("legacy observation = %v", err)
	}
	for name, mutate := range map[string]func(*Observation, *bool){
		"legacy raw only": func(value *Observation, _ *bool) {
			*value = legacy
			value.RawEntry = raw
		},
		"legacy identity only": func(value *Observation, _ *bool) {
			*value = legacy
			value.IdentityDigest = observation.IdentityDigest
		},
		"legacy normalized evidence": func(value *Observation, _ *bool) {
			*value = legacy
			value.NormalizedEntryDigest = normalizedDigest
		},
		"legacy search projection": func(value *Observation, complete *bool) {
			*value = legacy
			value.SearchDisplayName = "unexpected"
			*complete = true
		},
		"raw digest":            func(value *Observation, _ *bool) { value.ExactEntryDigest = normalizedDigest },
		"identity digest":       func(value *Observation, _ *bool) { value.IdentityDigest = normalizedDigest },
		"normalized digest":     func(value *Observation, _ *bool) { value.NormalizedEntryDigest = exactDigest },
		"normalized profile":    func(value *Observation, _ *bool) { value.NormalizationProfile = "unknown" },
		"projection mismatch":   func(value *Observation, _ *bool) { value.SearchPublisher = "other.example" },
		"reversed observation":  func(value *Observation, _ *bool) { value.LastSeenAt = now.Add(-time.Second) },
		"malformed exact bytes": func(value *Observation, _ *bool) { value.RawEntry = json.RawMessage(`{"bad":`) },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := observation
			projectionComplete := true
			mutate(&candidate, &projectionComplete)
			if err := ValidateRetainedObservationIntegrity(candidate, "", projectionComplete); err == nil {
				t.Fatal("invalid retained observation was accepted")
			}
		})
	}
}

func TestValidateRetainedOccurrenceIntegrity(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	digest := "sha256:" + strings.Repeat("a", 64)
	normalizedDigest := "sha256:" + strings.Repeat("b", 64)
	normalizationInput := "sha256:" + strings.Repeat("c", 64)
	identityDigest := observationIdentityDigest(digest, normalizationInput)
	completed := now.Add(time.Minute)
	snapshot := Snapshot{ID: "snapshot", TenantID: "tenant", SourceID: "source", SourceRevision: 2, State: SnapshotComplete, StartedAt: now.Add(-time.Minute), CompletedAt: &completed}
	observation := Observation{ID: "observation", TenantID: snapshot.TenantID, SourceID: snapshot.SourceID, ExactEntryDigest: digest, NormalizedEntryDigest: normalizedDigest, IdentityDigest: identityDigest, FirstSeenAt: now.Add(-time.Minute), LastSeenAt: completed}
	occurrence := ObservationOccurrence{ObservationID: observation.ID, SourceID: snapshot.SourceID, SnapshotID: snapshot.ID, SourceRevision: snapshot.SourceRevision, RetrievalDigest: digest, EntryLocation: "/entries/0", ExactEntryDigest: digest, NormalizedEntryDigest: normalizedDigest, NormalizationInputDigest: normalizationInput, EvidenceComplete: true, ObservedAt: now}
	if err := ValidateRetainedOccurrenceIntegrity(occurrence, observation, snapshot); err != nil {
		t.Fatalf("current occurrence = %v", err)
	}
	legacy := occurrence
	legacy.SourceRevision = 0
	legacy.EntryLocation = "/legacy/observations/" + observation.ID
	legacy.NormalizedEntryDigest = ""
	legacy.NormalizationInputDigest = ""
	legacy.EvidenceComplete = false
	legacyObservation := observation
	legacyObservation.NormalizedEntryDigest = ""
	if err := ValidateRetainedOccurrenceIntegrity(legacy, legacyObservation, Snapshot{ID: snapshot.ID, TenantID: snapshot.TenantID, SourceID: snapshot.SourceID, State: SnapshotComplete, StartedAt: snapshot.StartedAt, CompletedAt: snapshot.CompletedAt}); err == nil {
		t.Fatalf("legacy occurrence = %v", err)
	}
	for name, mutate := range map[string]func(*ObservationOccurrence, *Observation, *Snapshot){
		"observation":      func(value *ObservationOccurrence, _ *Observation, _ *Snapshot) { value.ObservationID = "other" },
		"source":           func(value *ObservationOccurrence, _ *Observation, _ *Snapshot) { value.SourceID = "other" },
		"snapshot":         func(value *ObservationOccurrence, _ *Observation, _ *Snapshot) { value.SnapshotID = "other" },
		"source revision":  func(value *ObservationOccurrence, _ *Observation, _ *Snapshot) { value.SourceRevision++ },
		"retrieval digest": func(value *ObservationOccurrence, _ *Observation, _ *Snapshot) { value.RetrievalDigest = "invalid" },
		"location":         func(value *ObservationOccurrence, _ *Observation, _ *Snapshot) { value.EntryLocation = "/entries/00" },
		"exact digest": func(value *ObservationOccurrence, _ *Observation, _ *Snapshot) {
			value.ExactEntryDigest = normalizedDigest
		},
		"normalized digest": func(value *ObservationOccurrence, _ *Observation, _ *Snapshot) { value.NormalizedEntryDigest = digest },
		"normalization input": func(value *ObservationOccurrence, _ *Observation, _ *Snapshot) {
			value.NormalizationInputDigest = digest
		},
		"incomplete": func(value *ObservationOccurrence, _ *Observation, _ *Snapshot) { value.EvidenceComplete = false },
		"before snapshot": func(value *ObservationOccurrence, _ *Observation, _ *Snapshot) {
			value.ObservedAt = now.Add(-2 * time.Minute)
		},
		"after observation": func(value *ObservationOccurrence, _ *Observation, _ *Snapshot) {
			value.ObservedAt = completed.Add(time.Second)
		},
		"failed snapshot": func(_ *ObservationOccurrence, _ *Observation, value *Snapshot) { value.State = SnapshotFailed },
	} {
		t.Run(name, func(t *testing.T) {
			candidateOccurrence := occurrence
			candidateObservation := observation
			candidateSnapshot := snapshot
			mutate(&candidateOccurrence, &candidateObservation, &candidateSnapshot)
			if err := ValidateRetainedOccurrenceIntegrity(candidateOccurrence, candidateObservation, candidateSnapshot); err == nil {
				t.Fatal("invalid occurrence was accepted")
			}
		})
	}
	invalidLegacy := legacy
	invalidLegacy.EntryLocation = "/legacy/observations/other"
	if err := ValidateRetainedOccurrenceIntegrity(invalidLegacy, legacyObservation, Snapshot{ID: snapshot.ID, TenantID: snapshot.TenantID, SourceID: snapshot.SourceID, State: SnapshotComplete, StartedAt: snapshot.StartedAt, CompletedAt: snapshot.CompletedAt}); err == nil {
		t.Fatal("invalid legacy occurrence was accepted")
	}
}

func TestSnapshotRejectsPreReleaseWithdrawalAlias(t *testing.T) {
	for _, input := range []string{`{"id":"snapshot","possiblyWithdrawn":3}`, `{"withdrawn":1,"withdrawn":2}`, `{"withdrawn":2,"unknown":true}`} {
		var snapshot Snapshot
		if err := json.Unmarshal([]byte(input), &snapshot); err == nil {
			t.Fatalf("accepted obsolete or ambiguous snapshot %s", input)
		}
	}
	var snapshot Snapshot
	if err := json.Unmarshal([]byte(`{"id":"snapshot","sourceRevision":1,"withdrawn":3}`), &snapshot); err != nil || snapshot.Withdrawn != 3 || snapshot.SourceRevision != 1 {
		t.Fatalf("current snapshot = %#v, %v", snapshot, err)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil || bytes.Contains(raw, []byte("possiblyWithdrawn")) || !bytes.Contains(raw, []byte(`"withdrawn":3`)) {
		t.Fatalf("current snapshot encoding = %s, %v", raw, err)
	}
}
