package runtime

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

func TestWithdrawalPreviewBoundsTenantHostsBeforeAnySnapshotRead(t *testing.T) {
	coordinator := &publicationWithdrawalCoordinator{managers: map[string]publicationBinding{}}
	for i := 0; i <= publication.MaxWithdrawalPreviewHosts; i++ {
		// Nil managers prove refusal happens before even the first hydration.
		coordinator.managers[fmt.Sprintf("host-%03d.example", i)] = publicationBinding{tenantID: "tenant"}
	}
	if rows, err := coordinator.PreviewPublicationRelease(t.Context(), "tenant", "release"); catalog.ErrorCodeOf(err) != catalog.CodeWorkLimitExceeded || rows != nil {
		t.Fatal(rows, err)
	}
	if rows, err := coordinator.PreviewPublicationRelease(t.Context(), "other", "release"); err != nil || len(rows) != 0 {
		t.Fatal("foreign configured hosts leaked", rows, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if rows, err := coordinator.PreviewPublicationRelease(ctx, "tenant", "release"); !errors.Is(err, context.Canceled) || rows != nil {
		t.Fatal(rows, err)
	}
}
