package overview

import (
	"testing"
	"time"
)

func TestCountAndScope(t *testing.T) {
	for _, trial := range []struct {
		rows, value int
		limited     bool
	}{{0, 0, false}, {10000, 10000, false}, {10001, 10000, true}, {20000, 10000, true}} {
		if got := Count(trial.rows); got.Value != trial.value || got.Limited != trial.limited {
			t.Fatal(got, trial)
		}
	}
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.FixedZone("fixture", 3600))
	value := NewScope("tenant", at)
	if value.TenantID != "tenant" || value.Scope != "current_access" || value.SelectionLimit != 10000 || value.ObservedAt.Location() != time.UTC || !value.ObservedAt.Equal(at) {
		t.Fatal(value)
	}
}
