package catalog

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

type revalidationRepository struct {
	*lifecycleReadFaults
	runs        []CreateValidationRunParams
	writeErr    error
	releases    []string
	overflow    bool
	releaseErr  error
	selections  int
	documentErr error
}

func (r *revalidationRepository) CreateValidationRun(_ context.Context, p CreateValidationRunParams) (EvidenceSet, error) {
	r.runs = append(r.runs, p)
	if p.TenantID != r.listing.TenantID || p.VersionRevisionID != r.revision.ID {
		return EvidenceSet{}, errors.New("lost validation binding")
	}
	if r.writeErr != nil {
		return EvidenceSet{}, r.writeErr
	}
	return p.Evidence, nil
}

func (r *revalidationRepository) PublishedReleaseIDsByVersionRevision(_ context.Context, tenant, revision string, limit int) ([]string, bool, error) {
	r.selections++
	if tenant != r.listing.TenantID || revision != r.revision.ID || limit != MaxPublicationAffectedReleases {
		return nil, false, errors.New("unbounded or incorrectly scoped release selection")
	}
	return r.releases, r.overflow, r.releaseErr
}

func (r *revalidationRepository) GetDocument(_ context.Context, tenant, id string) (DocumentBlob, error) {
	if tenant != r.listing.TenantID || id != strings.TrimPrefix(r.revision.Representations[0].ContentDigest, "sha256:") {
		return DocumentBlob{}, errors.New("lost document binding")
	}
	return DocumentBlob{}, r.documentErr
}

type revalidationWithdrawer struct {
	PublicationWithdrawer
	repo   *revalidationRepository
	calls  []string
	failAt int
	err    error
}

func (w *revalidationWithdrawer) WithdrawPublicationRelease(_ context.Context, tenant, release, action, target, cause string) ([]string, error) {
	if tenant != w.repo.listing.TenantID || target != w.repo.revision.ID || action != "version_revision.validated" || cause != "external_representation_revalidation_blocked" || len(w.repo.runs) != 1 {
		return nil, errors.New("withdrawal preceded evidence or lost attribution")
	}
	w.calls = append(w.calls, release)
	if len(w.calls) == w.failAt {
		return nil, w.err
	}
	return []string{"catalog.example.com"}, nil
}

func revalidationFailureFixture(t *testing.T) (guardrailFixture, *revalidationRepository, *revalidationWithdrawer) {
	t.Helper()
	f, reads := lifecycleFailureFixture(t, "revalidate")
	reads.version.ResourceID = reads.resource.ID
	reads.resource.NamespaceID = "namespace"
	reads.resource.Kind = KindAgent
	reads.revision.ID = "00000000-0000-7000-8000-000000000030"
	reads.revision.Representations = []Representation{{ID: "representation", Role: RepresentationPrimary, MediaType: "application/json", ProfileID: "test-profile", Data: []byte(`{}`), ContentDigest: digestBytes([]byte(`{}`)), Size: 2, ValidationState: "valid", NormalizedFacts: map[string]any{}, UnknownProperties: map[string]any{}}}
	var err error
	reads.revision.ContentSetDigest, err = contentSetDigest(reads.revision.Representations)
	if err != nil {
		t.Fatal(err)
	}
	r := &revalidationRepository{lifecycleReadFaults: reads}
	w := &revalidationWithdrawer{repo: r, err: errors.New("withdrawal failed")}
	f.service.repo = r
	f.service.publicationWithdrawer = w
	return f, r, w
}

func TestRevalidationStopsAtDependencyAndIdentifierFailures(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"success", "revision", "version", "resource", "document", "evidence id", "audit id", "outbox id", "persist"} {
		t.Run(mode, func(t *testing.T) {
			f, r, w := revalidationFailureFixture(t)
			sentinel := errors.New("validation dependency failed")
			wantReads := []string{"revision", "version", "resource"}
			wantRuns := 0
			switch mode {
			case "revision", "version", "resource":
				r.fail = mode
				r.err = sentinel
				for i, s := range wantReads {
					if s == mode {
						wantReads = wantReads[:i+1]
						break
					}
				}
			case "document":
				r.revision.Representations[0].Data = nil
				r.revision.Representations[0].ArtifactID = strings.TrimPrefix(r.revision.Representations[0].ContentDigest, "sha256:")
				r.documentErr = sentinel
				var err error
				r.revision.ContentSetDigest, err = contentSetDigest(r.revision.Representations)
				if err != nil {
					t.Fatal(err)
				}
			case "evidence id", "audit id", "outbox id":
				failAt := map[string]int{"evidence id": 1, "audit id": 2, "outbox id": 3}[mode]
				calls := 0
				f.service.ids = auditIDFunc(func(time.Time) (string, error) {
					calls++
					if calls == failAt {
						return "", sentinel
					}
					return fmt.Sprintf("00000000-0000-7000-8000-%012d", calls), nil
				})
			case "persist":
				r.writeErr = sentinel
				wantRuns = 1
			case "success":
				wantRuns = 1
			}
			revision, evidence, namespaceID, err := f.service.RevalidateVersionRevision(f.adminContext(), r.revision.ID)
			if mode == "success" {
				if err != nil || namespaceID != r.resource.NamespaceID || evidence.State != EvidenceComplete || revision.EvidenceSetID != evidence.ID || evidence.SubjectDigest != r.revision.ContentSetDigest {
					t.Fatalf("revision=%+v evidence=%+v error=%v", revision, evidence, err)
				}
			} else if !errors.Is(err, sentinel) || revision.ID != "" || evidence.ID != "" || namespaceID != "" {
				t.Fatalf("error=%v revision=%+v evidence=%+v", err, revision, evidence)
			}
			if !reflect.DeepEqual(r.reads, wantReads) || len(r.runs) != wantRuns || r.selections != 0 || len(w.calls) != 0 {
				t.Fatalf("reads=%v runs=%d selections=%d withdrawals=%v", r.reads, len(r.runs), r.selections, w.calls)
			}
		})
	}
}

func TestBlockedRevalidationBoundsAndStopsSafetyWithdrawal(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"no published releases", "success", "selection failure", "overflow", "first failure", "second failure", "no withdrawer"} {
		t.Run(mode, func(t *testing.T) {
			f, r, w := revalidationFailureFixture(t)
			// An unbound external reference is blocked without making a fetch.
			r.revision.Representations[0].Data = nil
			r.revision.Representations[0].URL = "https://example.com/agent.json"
			var err error
			r.revision.ContentSetDigest, err = contentSetDigest(r.revision.Representations)
			if err != nil {
				t.Fatal(err)
			}
			r.releases = []string{"release-a", "release-b", "release-c"}
			wantCalls := r.releases
			wantSelections := 1
			switch mode {
			case "no published releases":
				r.releases = nil
				wantCalls = nil
			case "selection failure":
				r.releaseErr = w.err
				wantCalls = nil
			case "overflow":
				r.overflow = true
				wantCalls = nil
			case "first failure":
				w.failAt = 1
				wantCalls = wantCalls[:1]
			case "second failure":
				w.failAt = 2
				wantCalls = wantCalls[:2]
			case "no withdrawer":
				f.service.publicationWithdrawer = nil
				wantCalls = nil
				wantSelections = 0
			}
			revision, evidence, namespaceID, err := f.service.RevalidateVersionRevision(f.adminContext(), r.revision.ID)
			switch mode {
			case "overflow":
				if ErrorCodeOf(err) != CodeWorkLimitExceeded {
					t.Fatalf("error=%v", err)
				}
			case "selection failure", "first failure", "second failure":
				if !errors.Is(err, w.err) {
					t.Fatalf("error=%v", err)
				}
			default:
				if err != nil || namespaceID != r.resource.NamespaceID || evidence.State != EvidenceBlocked || revision.EvidenceSetID != evidence.ID {
					t.Fatalf("error=%v evidence=%+v", err, evidence)
				}
			}
			if len(r.runs) != 1 || r.runs[0].Evidence.State != EvidenceBlocked || r.selections != wantSelections || !reflect.DeepEqual(w.calls, wantCalls) || len(f.fetcher.requests) != 0 {
				t.Fatalf("runs=%+v selections=%d withdrawals=%v", r.runs, r.selections, w.calls)
			}
			if err != nil && (revision.ID != "" || evidence.ID != "" || namespaceID != "") {
				t.Fatal("failed withdrawal returned successful revalidation")
			}
		})
	}
}
