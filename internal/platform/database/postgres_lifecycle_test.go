package database

import (
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestPostgresReleaseTransitionMatrixMatchesDomain(t *testing.T) {
	for _, from := range []catalog.ReleaseState{catalog.ReleasePublished, catalog.ReleaseDeprecated} {
		if !validTransition(from, catalog.ReleaseSuperseded) {
			t.Fatalf("%s release could not be explicitly superseded", from)
		}
	}
	if validTransition(catalog.ReleaseRetired, catalog.ReleaseSuperseded) || validTransition(catalog.ReleaseRevoked, catalog.ReleaseSuperseded) {
		t.Fatal("terminal release could be superseded")
	}
}
