package observability

import (
	"strings"
	"sync"
	"testing"
)

func TestSecurityRefusalCountersAreClosedAndConcurrent(t *testing.T) {
	registry := New(nil)
	var work sync.WaitGroup
	for range 32 {
		work.Go(func() {
			registry.ObserveSecurityRefusal("unauthenticated")
			registry.ObserveSecurityRefusal("forbidden")
			registry.ObserveSecurityRefusal("private-user")
		})
	}
	work.Wait()
	metrics := string(registry.Render(t.Context()))
	for _, reason := range []string{"unauthenticated", "forbidden"} {
		if !strings.Contains(metrics, `open_agentic_catalog_security_refusals_total{reason="`+reason+`"} 32`) {
			t.Fatalf("missing %s", reason)
		}
	}
	if strings.Contains(metrics, "private-user") {
		t.Fatal("unknown reason emitted")
	}
}
