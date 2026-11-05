package webhook

import (
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/events"
)

func testPublicReference(t *testing.T, targetType, targetID string) []byte {
	t.Helper()
	raw, err := events.MarshalReference(targetType+".created", targetType, targetID, "")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
