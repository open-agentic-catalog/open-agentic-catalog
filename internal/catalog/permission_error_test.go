package catalog

import (
	"errors"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"testing"
)

func TestPermissionErrorPreservesDependencyClassification(t *testing.T) {
	for _, trial := range []struct {
		err  error
		want ErrorCode
	}{
		{identity.E(identity.CodeDependency, "private policy dependency"), CodeDependencyUnavailable},
		{E(CodeDependencyUnavailable, "private repository dependency"), CodeDependencyUnavailable},
		{identity.E(identity.CodeForbidden, "refused"), CodeForbidden},
		{identity.E(identity.CodeUnauthenticated, "inactive"), CodeForbidden},
	} {
		err := PermissionError("current permission admission failed", trial.err)
		if ErrorCodeOf(err) != trial.want || !errors.Is(err, trial.err) {
			t.Fatal("permission classification/cause lost", err)
		}
	}
}
