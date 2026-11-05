// Optional hybrid deployment state must not make ordinary smoke checks fail.
package main

import (
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestSmokeSearchCapabilityModes(t *testing.T) {
	for _, tc := range []struct {
		tier, profile, semantic string
		degraded, valid         bool
	}{
		{"core", catalog.CoreSearchProfile, "disabled", false, true},
		{"core", catalog.CoreSearchProfile, "disabled", true, false},
		{"hybrid", "sha256:" + strings.Repeat("a", 64), "enabled", false, true},
		{"hybrid", "sha256:" + strings.Repeat("a", 64), "enabled", true, true},
		{"hybrid", "sha256:invalid", "enabled", false, false},
		{"hybrid", catalog.CoreSearchProfile, "enabled", false, false},
		{"core", catalog.CoreSearchProfile, "unknown", false, false},
	} {
		if got := validSmokeSearchState(tc.tier, tc.profile, tc.semantic, tc.degraded); got != tc.valid {
			t.Fatalf("mode=%+v got=%v", tc, got)
		}
	}
}
