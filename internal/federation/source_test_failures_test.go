package federation

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
)

// A successful fetch must not become a successful source test when evidence or
// persistence fails. All responses are supplied through the fetcher port.
func TestSourceTestFailuresDoNotAdvanceReviewedState(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"audit identifier", "outbox identifier", "write failure", "transport audit identifier", "transport audit persistence", "conflict audit persistence", "changed interface audit persistence"} {
		t.Run(mode, func(t *testing.T) {
			manager, store, _, evidence, source, _ := newChangePreviewTest(t)
			outbound := fixedAuditFetcher{result: fetcher.Result{Status: 200, FinalURL: source.Endpoint, MediaType: "application/ard+json", Body: []byte(`{"entries":[]}`)}}
			sentinel := errors.New("source test dependency failed")
			ids := &administrationIDs{err: sentinel}
			switch mode {
			case "audit identifier":
				ids.failAt = 1
			case "outbox identifier":
				ids.failAt = 2
			case "write failure":
				manager.store = rejectingSourceUpdateStore{Store: store, err: sentinel}
			case "transport audit identifier":
				outbound.err, ids.failAt = fetcher.ErrPolicyDenied, 1
			case "transport audit persistence":
				outbound.err, evidence.err = fetcher.ErrPolicyDenied, sentinel
			case "conflict audit persistence":
				manager.store = rejectingSourceUpdateStore{Store: store, err: catalog.E(catalog.CodePreconditionFailed, "concurrent source update")}
				evidence.err = sentinel
			case "changed interface audit persistence":
				outbound.result.FinalURL = "https://changed.example/ard.json"
				evidence.err = sentinel
			}
			manager.fetcher, manager.ids = outbound, ids
			result, err := manager.TestSourceAtRevision(auditEvidenceContext(), source.ID, source.Revision)
			if !errors.Is(err, sentinel) || result.SourceID != "" || catalog.ErrorPreservesEvidence(err) {
				t.Fatalf("failure reported success or retained evidence: result=%+v error=%v", result, err)
			}
			if ids.failAt > 0 && ids.calls != ids.failAt {
				t.Fatalf("identifier calls=%d want=%d", ids.calls, ids.failAt)
			}
			assertUnchangedPreviewSource(t, store, source, evidence, 0)
		})
	}
}

func TestSourceTestRegistryProbePreservesPolicyAndInterfaceBinding(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"success", "List unavailable", "changed policy", "policy failure audit unavailable"} {
		t.Run(mode, func(t *testing.T) {
			m, store, _, evidence, source, _ := newChangePreviewTest(t)
			const base = "https://registry.example/ard/v1/"
			source.Type, source.RegistrySearchURL, source.RegistryListURL = SourceRegistry, base+"search", base+"agents"
			var err error
			source, err = m.normalizeAndValidateSource(t.Context(), source)
			if err != nil {
				t.Fatal(err)
			}
			store.sources[source.ID] = source
			manifest := `{"entries":[{"identifier":"urn:air:registry.example:registry:main","displayName":"Registry","type":"application/ai-registry+json","url":"` + base + `","representativeQueries":["Find agents","Browse agents"]}]}`
			probeURL := source.RegistryListURL + "?pageSize=1"
			probe := fetcher.Result{Status: 200, FinalURL: probeURL, MediaType: "application/json", Body: []byte(`{"items":[],"total":0}`)}
			sentinel := errors.New("audit persistence unavailable")
			switch mode {
			case "List unavailable":
				probe.Status = 503
			case "changed policy", "policy failure audit unavailable":
				probe.PolicyDigest = "sha256:" + strings.Repeat("e", 64)
				if mode == "policy failure audit unavailable" {
					evidence.err = sentinel
				}
			}
			outbound := &routingFetcher{results: map[string]fetcher.Result{
				"GET " + source.Endpoint: federationResultAt(source.Endpoint, manifest),
				"GET " + probeURL:        probe,
			}}
			m.fetcher = outbound
			writes := &decisionSourceStore{Store: store}
			m.store = writes
			result, err := m.TestSourceAtRevision(auditEvidenceContext(), source.ID, source.Revision)
			switch mode {
			case "success":
				if err != nil || result.Status != "succeeded" || result.SourceRevision != source.Revision+1 || !result.Capabilities["search"] || !result.Capabilities["list"] || !result.Capabilities["enumerable"] || writes.writes != 1 || len(evidence.audit) != 1 || len(evidence.outbox) != 1 {
					t.Fatalf("test=%+v error=%v writes=%d evidence=%+v", result, err, writes.writes, evidence)
				}
				retained, readErr := store.GetSource(t.Context(), source.TenantID, source.ID)
				want := source
				want.Revision++
				want.UpdatedAt = result.TestedAt
				if readErr != nil || !reflect.DeepEqual(retained, want) {
					t.Fatalf("verification changed reviewed configuration: %+v, %v", retained, readErr)
				}
			case "List unavailable":
				if err != nil || result.Status != "failed" || !strings.Contains(result.SafeDiagnostic, "interface changed") || result.Capabilities["list"] || writes.writes != 0 {
					t.Fatalf("unverified List approved: %+v, %v writes=%d", result, err, writes.writes)
				}
				assertUnchangedPreviewSource(t, store, source, evidence, 1)
			case "changed policy":
				if catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || !catalog.ErrorPreservesEvidence(err) || result.SourceID != "" || writes.writes != 0 {
					t.Fatalf("mixed policy accepted: %+v, %v writes=%d", result, err, writes.writes)
				}
				assertUnchangedPreviewSource(t, store, source, evidence, 1)
			case "policy failure audit unavailable":
				if !errors.Is(err, sentinel) || catalog.ErrorPreservesEvidence(err) || result.SourceID != "" || writes.writes != 0 {
					t.Fatalf("unretained policy failure: %+v, %v writes=%d", result, err, writes.writes)
				}
				assertUnchangedPreviewSource(t, store, source, evidence, 0)
			}
			if mode == "List unavailable" || mode == "changed policy" {
				record := evidence.audit[0]
				facts := auditFacts(t, record)
				if record.Action != audit.ActionSourceTested || record.TenantID != source.TenantID || record.TargetID != source.ID || record.BeforeDigest == "" || record.BeforeDigest != record.AfterDigest || facts["stateChanged"] != false || fmt.Sprint(facts["beforeRevision"]) != fmt.Sprint(source.Revision) || fmt.Sprint(facts["afterRevision"]) != fmt.Sprint(source.Revision) {
					t.Fatalf("failed probe audit changed state or lost attribution: %+v", record)
				}
			}
			if len(outbound.requests) != 2 || outbound.requests[0].URL != source.Endpoint || outbound.requests[1].URL != probeURL {
				t.Fatalf("unexpected discovery contacts: %+v", outbound.requests)
			}
			for _, request := range outbound.requests {
				if request.TenantID != source.TenantID {
					t.Fatalf("fetch lost tenant binding: %+v", request)
				}
			}
			if request := outbound.requests[1]; request.MaxResponseBytes != 1<<20 || !request.RejectOriginChanges || !request.RedactQueryInErrors {
				t.Fatalf("List probe lost its safety bounds: %+v", request)
			}
		})
	}
}
