// Federation decision tests reject invalid transitions before contact or persistence.
package federation

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// Record reads and mutation attempts separately from retained state so a store
// rejection cannot hide a missing preparation guard.
type decisionSourceStore struct {
	Store
	reads  [][2]string
	err    error
	writes int
}

func (s *decisionSourceStore) GetSource(ctx context.Context, tenant, id string) (Source, error) {
	s.reads = append(s.reads, [2]string{tenant, id})
	if s.err != nil {
		return Source{}, s.err
	}
	return s.Store.GetSource(ctx, tenant, id)
}

func (s *decisionSourceStore) UpdateSource(ctx context.Context, source Source, revision int64, evidence ...MutationEvidence) (Source, error) {
	s.writes++
	return s.Store.UpdateSource(ctx, source, revision, evidence...)
}

func TestFederationDecisionLiveSelectionRejectsBeforeContact(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"ordinary reason", "empty ID", "padded ID", "duplicate IDs", "missing source", "lookup failure"} {
		t.Run(mode, func(t *testing.T) {
			m, store, _, evidence, source, _ := newChangePreviewTest(t)
			reads := &decisionSourceStore{Store: store}
			m.store = reads
			request := LiveSearchRequest{Text: "private query", Administrative: true, Reason: "Diagnose selected source", SourceIDs: []string{source.ID}}
			wantCode, wantReads := catalog.CodeInvalidArgument, 0
			sentinel := errors.New("source selection failed")
			switch mode {
			case "ordinary reason":
				request.Administrative = false
				request.SourceIDs = nil
				request.DiscoveryHost, request.Audience = "catalog.example.com", AudienceAnonymous
			case "empty ID":
				request.SourceIDs = []string{""}
			case "padded ID":
				request.SourceIDs = []string{" " + source.ID}
			case "duplicate IDs":
				request.SourceIDs = []string{source.ID, source.ID}
			case "missing source":
				reads.err = catalog.E(catalog.CodeNotFound, "private source details")
				wantCode, wantReads = catalog.CodeSourcePolicyDenied, 1
			case "lookup failure":
				reads.err, wantReads = sentinel, 1
			}
			result, err := m.LiveSearch(auditEvidenceContext(), request)
			if mode == "lookup failure" {
				if !errors.Is(err, sentinel) {
					t.Fatalf("lost lookup cause: %v", err)
				}
			} else if catalog.ErrorCodeOf(err) != wantCode {
				t.Fatalf("error=%v; want %s", err, wantCode)
			}
			if !reflect.DeepEqual(result, LiveSearchResponse{}) || catalog.ErrorPreservesEvidence(err) || reads.writes != 0 || len(reads.reads) != wantReads {
				t.Fatalf("result=%+v reads=%v writes=%d error=%v", result, reads.reads, reads.writes, err)
			}
			if mode == "missing source" && strings.Contains(err.Error(), "private source details") {
				t.Fatal("missing source disclosed private lookup details")
			}
			if wantReads == 1 && reads.reads[0] != [2]string{source.TenantID, source.ID} {
				t.Fatalf("selection lost tenant binding: %v", reads.reads)
			}
			assertUnchangedPreviewSource(t, store, source, evidence, 0)
		})
	}
}

func TestFederationDecisionSourceTestPreconditions(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"unauthorized", "missing source", "read failure", "zero revision", "stale revision", "invalid configuration"} {
		t.Run(mode, func(t *testing.T) {
			m, store, _, evidence, source, _ := newChangePreviewTest(t)
			reads := &decisionSourceStore{Store: store}
			m.store = reads
			ctx, revision := auditEvidenceContext(), source.Revision
			wantCode, wantReads := catalog.CodePreconditionFailed, 1
			sentinel := errors.New("source read failed")
			switch mode {
			case "unauthorized":
				ctx, wantCode, wantReads = context.Background(), catalog.CodeForbidden, 0
			case "missing source":
				reads.err, wantCode = catalog.E(catalog.CodeNotFound, "source missing"), catalog.CodeNotFound
			case "read failure":
				reads.err = sentinel
			case "zero revision":
				revision = 0
			case "stale revision":
				revision++
			case "invalid configuration":
				source.Scope.Kinds = []catalog.Kind{"unsupported"}
				store.sources[source.ID] = source
				wantCode = catalog.CodeInvalidArgument
			}
			result, err := m.TestSourceAtRevision(ctx, source.ID, revision)
			if mode == "read failure" {
				if !errors.Is(err, sentinel) {
					t.Fatalf("lost read cause: %v", err)
				}
			} else if catalog.ErrorCodeOf(err) != wantCode {
				t.Fatalf("error=%v; want %s", err, wantCode)
			}
			if result.SourceID != "" || catalog.ErrorPreservesEvidence(err) || reads.writes != 0 || len(reads.reads) != wantReads {
				t.Fatalf("result=%+v reads=%v writes=%d error=%v", result, reads.reads, reads.writes, err)
			}
			if wantReads == 1 && reads.reads[0] != [2]string{source.TenantID, source.ID} {
				t.Fatalf("source test lost tenant binding: %v", reads.reads)
			}
			assertUnchangedPreviewSource(t, store, source, evidence, 0)
		})
	}
}

func TestFederationDecisionStatusPreparationDoesNotWrite(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"ambiguous input", "missing reason", "missing source", "already active", "missing removal preview", "removal selection failure", "removal selection overflow", "audit ID", "outbox ID"} {
		t.Run(mode, func(t *testing.T) {
			m, store, _, evidence, source, _ := newChangePreviewTest(t)
			spy := &decisionSourceStore{Store: store}
			m.store = spy
			status := SourcePaused
			inputs := []StatusChangeInput{{Reason: "Apply reviewed source status"}}
			wantCode := catalog.CodeInvalidArgument
			wantAudits := 0
			sentinel := errors.New("status preparation failed")
			var wantError error
			var ids *administrationIDs
			switch mode {
			case "ambiguous input":
				inputs = append(inputs, inputs[0])
			case "missing reason":
				inputs = nil
			case "missing source":
				spy.err, wantCode = catalog.E(catalog.CodeNotFound, "missing source"), catalog.CodeNotFound
			case "already active":
				status, wantCode = SourceActive, catalog.CodeInvalidTransition
			case "missing removal preview":
				status, wantCode = SourceRemoved, catalog.CodePreconditionFailed
			case "removal selection failure", "removal selection overflow":
				preview, err := m.PreviewChange(auditEvidenceContext(), source.ID, PatchInput{Intent: ChangeRemove})
				if err != nil {
					t.Fatal(err)
				}
				inputs[0].ChangePreviewDigest = preview.Digest
				status, wantAudits = SourceRemoved, 1
				selection := &previewSelectionStore{Store: spy, t: t, tenantID: source.TenantID, sourceID: source.ID}
				m.store = selection
				if mode == "removal selection failure" {
					selection.err, wantError = sentinel, sentinel
				} else {
					selection.overflow, wantCode = true, catalog.CodeWorkLimitExceeded
				}
			case "audit ID", "outbox ID":
				failAt := 1
				if mode == "outbox ID" {
					failAt = 2
				}
				ids = &administrationIDs{failAt: failAt, err: sentinel}
				m.ids = ids
				wantError = sentinel
			}
			got, err := m.SetStatus(auditEvidenceContext(), source.ID, status, source.Revision, inputs...)
			if wantError != nil {
				if !errors.Is(err, wantError) {
					t.Fatalf("lost preparation cause: %v", err)
				}
			} else if catalog.ErrorCodeOf(err) != wantCode {
				t.Fatalf("error=%v; want %s", err, wantCode)
			}
			if got.ID != "" || spy.writes != 0 {
				t.Fatalf("source=%+v writes=%d", got, spy.writes)
			}
			if ids != nil && ids.calls != ids.failAt {
				t.Fatalf("identifier calls=%d; want %d", ids.calls, ids.failAt)
			}
			assertUnchangedPreviewSource(t, store, source, evidence, wantAudits)
		})
	}
}
