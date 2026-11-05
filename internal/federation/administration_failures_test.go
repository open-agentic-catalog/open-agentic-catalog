package federation

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

type administrationIDs struct {
	calls, failAt int
	err           error
}

func (g *administrationIDs) New(time.Time) (string, error) {
	g.calls++
	if g.calls == g.failAt {
		return "", g.err
	}
	return fmt.Sprintf("00000000-0000-7000-8000-%012d", 100+g.calls), nil
}

// Failure cases reuse a valid indexed-source configuration. No inspection,
// preview, or administrative write in these tests may fetch upstream content.
func TestSourceAdministrationIdentifierFailuresDoNotMutate(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"connect", "preview", "update"} {
		count := 2
		if operation == "connect" {
			count = 3
		}
		for failAt := 1; failAt <= count; failAt++ {
			t.Run(fmt.Sprintf("%s/%d", operation, failAt), func(t *testing.T) {
				manager, store, c, evidence, source, input := newChangePreviewTest(t)
				inspection := Inspection{ID: "00000000-0000-7000-8000-000000000090", TenantID: source.TenantID, SourceType: source.Type, ResolvedURL: "https://another.remote.example/ard.json", ProfileID: source.ProfileID, PolicyID: source.PolicyID, PolicyRevision: source.PolicyRevision, PolicyDigest: source.PolicyDigest, CreatedAt: c.Now(), ExpiresAt: c.Now().Add(time.Minute)}
				if _, err := store.SaveInspection(t.Context(), inspection); err != nil {
					t.Fatal(err)
				}
				if operation == "update" {
					p, err := manager.PreviewChange(auditEvidenceContext(), source.ID, input)
					if err != nil {
						t.Fatal(err)
					}
					input.ChangePreviewDigest = p.Digest
				}
				beforeAudits, beforePreviews := len(evidence.audit), len(store.previews)
				ids := &administrationIDs{failAt: failAt, err: errors.New("identifier failed")}
				manager.ids = ids
				var err error
				switch operation {
				case "connect":
					_, err = manager.Connect(auditEvidenceContext(), ConnectInput{InspectionID: inspection.ID, DisplayName: "Another remote", Scope: source.Scope, Reason: "Approve this source"})
				case "preview":
					_, err = manager.PreviewChange(auditEvidenceContext(), source.ID, input)
				case "update":
					_, err = manager.UpdateSource(auditEvidenceContext(), source.ID, input, source.Revision)
				}
				if !errors.Is(err, ids.err) || ids.calls != failAt {
					t.Fatalf("error=%v calls=%d", err, ids.calls)
				}
				assertUnchangedPreviewSource(t, store, source, evidence, beforeAudits)
				if len(store.sources) != 1 || len(store.previews) != beforePreviews {
					t.Fatal("failed preparation retained a source or preview")
				}
				retained, readErr := store.GetInspection(t.Context(), source.TenantID, inspection.ID)
				if readErr != nil || !reflect.DeepEqual(retained, inspection) {
					t.Fatal("failed connection consumed its inspection")
				}
			})
		}
	}
}

func TestConnectRequiresSourceAdministratorAndExactConnectivity(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"anonymous", "network profile", "credential binding", "valid inherited connectivity"} {
		t.Run(mode, func(t *testing.T) {
			manager, store, c, evidence, source, _ := newChangePreviewTest(t)
			inspection := Inspection{ID: "00000000-0000-7000-8000-000000000090", TenantID: source.TenantID, SourceType: source.Type, ResolvedURL: "https://another.remote.example/ard.json", ProfileID: source.ProfileID, PolicyID: source.PolicyID, PolicyRevision: source.PolicyRevision, PolicyDigest: source.PolicyDigest, NetworkProfile: "approved-network", CredentialBinding: "approved-binding", CreatedAt: c.Now(), ExpiresAt: c.Now().Add(time.Minute)}
			if _, err := store.SaveInspection(t.Context(), inspection); err != nil {
				t.Fatal(err)
			}
			input := ConnectInput{InspectionID: inspection.ID, DisplayName: " Another remote ", Scope: source.Scope, Reason: "Approve this source"}
			ctx := auditEvidenceContext()
			code := catalog.CodePreconditionFailed
			switch mode {
			case "anonymous":
				ctx = context.Background()
				code = catalog.CodeForbidden
			case "network profile":
				input.NetworkProfile = "other"
			case "credential binding":
				input.CredentialBinding = "other"
			}
			got, err := manager.Connect(ctx, input)
			if mode == "valid inherited connectivity" {
				if err != nil || got.NetworkProfile != inspection.NetworkProfile || got.CredentialBinding != inspection.CredentialBinding || got.DisplayName != "Another remote" || got.UpdateMode != UpdateManual || len(store.sources) != 2 || len(evidence.audit) != 1 || len(evidence.outbox) != 1 {
					t.Fatalf("source=%+v error=%v", got, err)
				}
			} else {
				if catalog.ErrorCodeOf(err) != code || got.ID != "" || len(store.sources) != 1 {
					t.Fatalf("source=%+v error=%v", got, err)
				}
				assertUnchangedPreviewSource(t, store, source, evidence, 0)
			}
		})
	}
}

func TestSourceChangePreparationGuardsDoNotPersist(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"preview", "update"} {
		for _, mode := range []string{"empty patch", "empty name", "no change", "connectivity change", "invalid retrieval mode", "missing source", "stale revision", "missing reason", "removal with patch"} {
			if operation == "preview" && (mode == "stale revision" || mode == "missing reason") || operation == "update" && mode == "removal with patch" {
				continue
			}
			t.Run(operation+"/"+mode, func(t *testing.T) {
				manager, store, _, evidence, source, input := newChangePreviewTest(t)
				beforeAudits, beforePreviews := 0, 0
				if operation == "update" {
					preview, err := manager.PreviewChange(auditEvidenceContext(), source.ID, input)
					if err != nil {
						t.Fatal(err)
					}
					input.ChangePreviewDigest = preview.Digest
					beforeAudits, beforePreviews = 1, 1
				}
				sourceID, revision := source.ID, source.Revision
				code := catalog.CodeInvalidArgument
				switch mode {
				case "empty patch":
					input.DisplayName = nil
				case "empty name":
					name := ""
					input.DisplayName = &name
				case "no change":
					input.DisplayName = &source.DisplayName
				case "connectivity change":
					network := "other-network"
					input.NetworkProfile = &network
					code = catalog.CodePreconditionFailed
				case "invalid retrieval mode":
					value := RetrievalMode("unknown")
					input.RetrievalMode = &value
				case "missing source":
					sourceID = "missing"
					code = catalog.CodeNotFound
				case "stale revision":
					revision++
					code = catalog.CodePreconditionFailed
				case "missing reason":
					input.Reason = ""
				case "removal with patch":
					input.Intent = ChangeRemove
				}
				var err error
				if operation == "preview" {
					_, err = manager.PreviewChange(auditEvidenceContext(), sourceID, input)
				} else {
					_, err = manager.UpdateSource(auditEvidenceContext(), sourceID, input, revision)
				}
				if catalog.ErrorCodeOf(err) != code {
					t.Fatalf("error=%v want=%s", err, code)
				}
				assertUnchangedPreviewSource(t, store, source, evidence, beforeAudits)
				if len(store.previews) != beforePreviews {
					t.Fatal("rejected preparation retained a preview")
				}
			})
		}
	}
}

func TestSourceUpdateSelectionFailuresPreserveReviewedConfiguration(t *testing.T) {
	t.Parallel()
	for _, overflow := range []bool{false, true} {
		t.Run(fmt.Sprintf("overflow=%t", overflow), func(t *testing.T) {
			manager, store, _, evidence, source, input := newChangePreviewTest(t)
			preview, err := manager.PreviewChange(auditEvidenceContext(), source.ID, input)
			if err != nil {
				t.Fatal(err)
			}
			input.ChangePreviewDigest = preview.Digest
			selection := &previewSelectionStore{Store: store, t: t, tenantID: source.TenantID, sourceID: source.ID, overflow: overflow}
			if !overflow {
				selection.err = errors.New("membership read failed")
			}
			manager.store = selection
			_, err = manager.UpdateSource(auditEvidenceContext(), source.ID, input, source.Revision)
			if overflow {
				if catalog.ErrorCodeOf(err) != catalog.CodeWorkLimitExceeded {
					t.Fatalf("error=%v", err)
				}
			} else if !errors.Is(err, selection.err) {
				t.Fatalf("error=%v", err)
			}
			if selection.setCalls != 1 || selection.listCalls != 0 {
				t.Fatal("update hydrated observations or retried failed selection")
			}
			assertUnchangedPreviewSource(t, store, source, evidence, 1)
		})
	}
}
