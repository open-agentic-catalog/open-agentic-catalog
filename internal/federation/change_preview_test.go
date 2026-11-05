package federation

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

// Preview tests use a normalized indexed source and real evidence persistence;
// preparing or applying its configuration must never fetch upstream content.
func newChangePreviewTest(t *testing.T) (*Manager, *MemoryStore, *clock.Fake, *recordingFederationEvidence, Source, PatchInput) {
	t.Helper()
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	fakeClock := clock.NewFake(now)
	evidence := &recordingFederationEvidence{}
	store := NewMemoryStore(evidence)
	outbound := &routingFetcher{results: nil}
	manager, err := NewManager(store, outbound, fakeClock, id.Generator{}, WithEvidenceSink(evidence, true))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if len(outbound.requests) != 0 {
			t.Error("source configuration change contacted an upstream destination")
		}
	})
	source := Source{
		ID: "00000000-0000-7000-8000-000000000020", TenantID: auditEvidenceTenantID,
		DisplayName: "Remote", Type: SourceARDManifest, Endpoint: "https://catalog.remote.example/ard.json",
		Status: SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalIndexed,
		Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateManual,
		PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: federationTestPolicyDigest,
		Revision: 1, CreatedBy: auditEvidenceMembershipID, CreatedAt: now, UpdatedAt: now,
	}
	source, err = manager.normalizeAndValidateSource(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateSource(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	name := "Reviewed remote"
	return manager, store, fakeClock, evidence, source, PatchInput{Intent: ChangeUpdate, DisplayName: &name, Reason: "Apply the reviewed configuration"}
}

func assertUnchangedPreviewSource(t *testing.T, store *MemoryStore, source Source, evidence *recordingFederationEvidence, audits int) {
	t.Helper()
	stored, err := store.GetSource(t.Context(), source.TenantID, source.ID)
	if err != nil || !reflect.DeepEqual(stored, source) {
		t.Fatalf("rejected change modified source: %#v, %v", stored, err)
	}
	if len(evidence.audit) != audits || len(evidence.outbox) != 0 {
		t.Fatalf("rejected change emitted evidence: audits=%d outbox=%d", len(evidence.audit), len(evidence.outbox))
	}
}

func TestSourceChangePreviewExpiryBoundary(t *testing.T) {
	for _, intent := range []ChangeIntent{ChangeUpdate, ChangeRemove} {
		for _, offset := range []time.Duration{-time.Microsecond, 0, time.Microsecond} {
			t.Run(string(intent)+"/"+offset.String(), func(t *testing.T) {
				manager, store, fakeClock, evidence, source, input := newChangePreviewTest(t)
				if intent == ChangeRemove {
					input = PatchInput{Intent: ChangeRemove}
				}
				preview, err := manager.PreviewChange(auditEvidenceContext(), source.ID, input)
				if err != nil {
					t.Fatal(err)
				}
				if !preview.ExpiresAt.Equal(fakeClock.Now().Add(10*time.Minute)) || len(evidence.audit) != 1 || evidence.audit[0].Action != audit.ActionSourceChangePreviewCreated || len(evidence.outbox) != 0 {
					t.Fatal("preview expiry or audit-only evidence changed")
				}
				fakeClock.Advance(preview.ExpiresAt.Sub(fakeClock.Now()) + offset)
				var changed Source
				if intent == ChangeUpdate {
					input.ChangePreviewDigest = preview.Digest
					changed, err = manager.UpdateSource(auditEvidenceContext(), source.ID, input, source.Revision)
				} else {
					changed, err = manager.SetStatus(auditEvidenceContext(), source.ID, SourceRemoved, source.Revision, StatusChangeInput{Reason: "Remove the reviewed source", ChangePreviewDigest: preview.Digest})
				}
				if offset >= 0 {
					if catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed || changed.ID != "" {
						t.Fatalf("expired preview applied: %#v, %v", changed, err)
					}
					assertUnchangedPreviewSource(t, store, source, evidence, 1)
					return
				}
				if err != nil || changed.Revision != source.Revision+1 || len(evidence.audit) != 2 || len(evidence.outbox) != 1 {
					t.Fatalf("valid preview did not commit with evidence: %#v, %v", changed, err)
				}
				if intent == ChangeUpdate && changed.DisplayName != *input.DisplayName || intent == ChangeRemove && changed.Status != SourceRemoved {
					t.Fatalf("wrong source change: %#v", changed)
				}
				// GetSource deliberately hides removed sources. Check the retained
				// row directly so removal must persist its tombstone as well.
				stored, ok := store.sources[source.ID]
				if !ok || !reflect.DeepEqual(stored, changed) {
					t.Fatalf("successful change was not persisted: %#v", stored)
				}
			})
		}
	}
}

func TestSourceUpdateRequiresExactlyReviewedPreview(t *testing.T) {
	for name, mutate := range map[string]func(*ChangePreview){
		"other tenant":        func(p *ChangePreview) { p.TenantID = "00000000-0000-7000-8000-000000000099" },
		"other source":        func(p *ChangePreview) { p.SourceID = "00000000-0000-7000-8000-000000000099" },
		"other revision":      func(p *ChangePreview) { p.SourceRevision++ },
		"removal intent":      func(p *ChangePreview) { p.Intent = ChangeRemove },
		"other configuration": func(p *ChangePreview) { p.ConfigurationDigest = "sha256:" + strings.Repeat("e", 64) },
		"changed count":       func(p *ChangePreview) { p.CurrentMatched++ },
	} {
		t.Run(name, func(t *testing.T) {
			manager, store, _, evidence, source, input := newChangePreviewTest(t)
			preview, err := manager.PreviewChange(auditEvidenceContext(), source.ID, input)
			if err != nil {
				t.Fatal(err)
			}
			input.ChangePreviewDigest = preview.Digest
			// Simulate retained evidence for a different review. The manager must
			// verify each binding rather than trusting possession of a digest alone.
			mutate(&preview)
			store.previews[preview.Digest] = preview
			changed, err := manager.UpdateSource(auditEvidenceContext(), source.ID, input, source.Revision)
			if catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed || changed.ID != "" {
				t.Fatalf("mismatched review applied: %#v, %v", changed, err)
			}
			assertUnchangedPreviewSource(t, store, source, evidence, 1)
		})
	}
}

type observationChangeAtCommitStore struct {
	Store
	beforeCommit func()
	calls        int
}

func (s *observationChangeAtCommitStore) UpdateSource(ctx context.Context, source Source, expectedRevision int64, evidence ...MutationEvidence) (Source, error) {
	s.calls++
	s.beforeCommit()
	return s.Store.UpdateSource(ctx, source, expectedRevision, evidence...)
}

func TestSourceChangeRejectsReplacedObservationsWithUnchangedCount(t *testing.T) {
	for _, intent := range []ChangeIntent{ChangeUpdate, ChangeRemove} {
		for _, atCommit := range []bool{false, true} {
			phase := "before validation"
			if atCommit {
				phase = "after validation before commit"
			}
			t.Run(string(intent)+"/"+phase, func(t *testing.T) {
				manager, store, _, evidence, source, input := newChangePreviewTest(t)
				observation := Observation{
					ID: "00000000-0000-7000-8000-000000000030", TenantID: source.TenantID, SourceID: source.ID,
					ExactEntryDigest: "sha256:" + strings.Repeat("a", 64), Freshness: FreshCurrent,
					RawEntry: []byte(`{"identifier":"urn:air:remote.example:agent:one","type":"application/a2a-agent-card+json"}`),
				}
				store.observations[observation.ID] = observation
				if intent == ChangeRemove {
					input = PatchInput{Intent: ChangeRemove}
				}
				preview, err := manager.PreviewChange(auditEvidenceContext(), source.ID, input)
				if err != nil || preview.CurrentMatched != 1 {
					t.Fatalf("initial preview = %#v, %v", preview, err)
				}
				replacement := observation
				replacement.ID = "00000000-0000-7000-8000-000000000031"
				replace := func() {
					store.mu.Lock()
					defer store.mu.Unlock()
					delete(store.observations, observation.ID)
					store.observations[replacement.ID] = replacement
				}
				interleaved := &observationChangeAtCommitStore{Store: store, beforeCommit: replace}
				if atCommit {
					manager.store = interleaved
				} else {
					replace()
				}
				var changed Source
				if intent == ChangeUpdate {
					input.ChangePreviewDigest = preview.Digest
					changed, err = manager.UpdateSource(auditEvidenceContext(), source.ID, input, source.Revision)
				} else {
					changed, err = manager.SetStatus(auditEvidenceContext(), source.ID, SourceRemoved, source.Revision, StatusChangeInput{Reason: "Remove the reviewed source", ChangePreviewDigest: preview.Digest})
				}
				if catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed || changed.ID != "" {
					t.Fatalf("changed observation membership was accepted: %#v, %v", changed, err)
				}
				if atCommit && interleaved.calls != 1 {
					t.Fatal("test did not reach the commit-time guard")
				}
				assertUnchangedPreviewSource(t, store, source, evidence, 1)
				current, overflow, err := store.ListCurrentObservations(t.Context(), source.TenantID, source.ID, 2)
				if err != nil || overflow || len(current) != 1 || current[0].ID != replacement.ID || current[0].Freshness != FreshCurrent {
					t.Fatalf("rejected change damaged the replacement observation: %#v, %v", current, err)
				}
			})
		}
	}
}

// Only the bounded selection calls are overridden. All persistence and audit
// rollback assertions still use the real memory store.
type previewSelectionStore struct {
	Store
	t         *testing.T
	tenantID  string
	sourceID  string
	failList  bool
	overflow  bool
	err       error
	setCalls  int
	listCalls int
}

func (s *previewSelectionStore) CurrentObservationSet(ctx context.Context, tenantID, sourceID string, limit int) (ObservationSet, bool, error) {
	s.setCalls++
	if tenantID != s.tenantID || sourceID != s.sourceID || limit != MaxSynchronizationEntries {
		s.t.Fatal("preview lost its tenant, source or bounded selection limit")
	}
	if !s.failList {
		return ObservationSet{}, s.overflow, s.err
	}
	return s.Store.CurrentObservationSet(ctx, tenantID, sourceID, limit)
}

func (s *previewSelectionStore) ListCurrentObservations(_ context.Context, tenantID, sourceID string, limit int) ([]Observation, bool, error) {
	s.listCalls++
	if tenantID != s.tenantID || sourceID != s.sourceID || limit != MaxSynchronizationEntries {
		s.t.Fatal("preview lost its tenant, source or bounded observation limit")
	}
	return nil, s.overflow, s.err
}

func TestSourcePreviewSelectionFailuresDoNotPersistPreview(t *testing.T) {
	for _, failList := range []bool{false, true} {
		for _, overflow := range []bool{false, true} {
			name := "membership"
			if failList {
				name = "observations"
			}
			if overflow {
				name += "/overflow"
			} else {
				name += "/dependency failure"
			}
			t.Run(name, func(t *testing.T) {
				manager, store, _, evidence, source, input := newChangePreviewTest(t)
				selection := &previewSelectionStore{Store: store, t: t, tenantID: source.TenantID, sourceID: source.ID, failList: failList, overflow: overflow}
				if !overflow {
					selection.err = errors.New("observation selection unavailable")
				}
				manager.store = selection
				preview, err := manager.PreviewChange(auditEvidenceContext(), source.ID, input)
				if err == nil || preview.ID != "" || overflow && catalog.ErrorCodeOf(err) != catalog.CodeWorkLimitExceeded || !overflow && !errors.Is(err, selection.err) {
					t.Fatalf("incomplete selection produced a preview: %#v, %v", preview, err)
				}
				wantLists := 0
				if failList {
					wantLists = 1
				}
				if selection.setCalls != 1 || selection.listCalls != wantLists || len(store.previews) != 0 {
					t.Fatal("failed selection continued work or retained a preview")
				}
				assertUnchangedPreviewSource(t, store, source, evidence, 0)
			})
		}
	}
}

func TestSourcePreviewAndUpdateRollBackWhenAuditFails(t *testing.T) {
	for _, applying := range []bool{false, true} {
		name := "preview"
		if applying {
			name = "update"
		}
		t.Run(name, func(t *testing.T) {
			manager, store, _, evidence, source, input := newChangePreviewTest(t)
			ctx := auditEvidenceContext()
			if applying {
				preview, err := manager.PreviewChange(ctx, source.ID, input)
				if err != nil {
					t.Fatal(err)
				}
				input.ChangePreviewDigest = preview.Digest
			}
			beforeAudits, beforePreviews := len(evidence.audit), len(store.previews)
			failure := errors.New("audit persistence unavailable")
			evidence.err = failure
			if applying {
				changed, err := manager.UpdateSource(ctx, source.ID, input, source.Revision)
				if !errors.Is(err, failure) || changed.ID != "" {
					t.Fatalf("update survived audit failure: %#v, %v", changed, err)
				}
			} else {
				preview, err := manager.PreviewChange(ctx, source.ID, input)
				if !errors.Is(err, failure) || preview.ID != "" {
					t.Fatalf("preview survived audit failure: %#v, %v", preview, err)
				}
			}
			if len(store.previews) != beforePreviews {
				t.Fatal("failed audit changed retained previews")
			}
			assertUnchangedPreviewSource(t, store, source, evidence, beforeAudits)
			// A transient audit outage must not leave a half-written command that
			// prevents the same reviewed change from succeeding after recovery.
			evidence.err = nil
			if applying {
				changed, err := manager.UpdateSource(ctx, source.ID, input, source.Revision)
				if err != nil || changed.Revision != source.Revision+1 || changed.DisplayName != *input.DisplayName {
					t.Fatalf("update could not recover: %#v, %v", changed, err)
				}
				if len(evidence.audit) != beforeAudits+1 || len(evidence.outbox) != 1 {
					t.Fatal("recovered update did not retain exactly one audit and notification")
				}
				stored, err := store.GetSource(ctx, source.TenantID, source.ID)
				if err != nil || !reflect.DeepEqual(stored, changed) {
					t.Fatalf("recovered update was not persisted: %#v, %v", stored, err)
				}
			} else {
				preview, err := manager.PreviewChange(ctx, source.ID, input)
				if err != nil || len(store.previews) != 1 || len(evidence.audit) != 1 || len(evidence.outbox) != 0 {
					t.Fatalf("preview could not recover atomically: %#v, %v", preview, err)
				}
				retained, err := store.GetChangePreview(ctx, source.TenantID, preview.Digest)
				if err != nil || retained != preview {
					t.Fatalf("recovered preview was not retained: %#v, %v", retained, err)
				}
			}
		})
	}
}
