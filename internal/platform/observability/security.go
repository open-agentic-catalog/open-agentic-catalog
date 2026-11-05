// Security counters classify API refusal codes without identity or request data.
package observability

import (
	"bytes"
	"fmt"
)

func (r *Registry) ObserveSecurityRefusal(reason string) {
	index := 0
	switch reason {
	case "unauthenticated":
	case "forbidden":
		index = 1
	default:
		return
	}
	r.mu.Lock()
	r.securityRefusals[index]++
	r.mu.Unlock()
}

func (r *Registry) writeSecurityMetrics(output *bytes.Buffer) {
	r.mu.Lock()
	counts := r.securityRefusals
	r.mu.Unlock()
	const name = "open_agentic_catalog_security_refusals_total"
	writeMetricHeader(output, name, "Final API unauthenticated/forbidden problem responses; not unique identities or all 401/403 statuses.", "counter")
	for i, reason := range []string{"unauthenticated", "forbidden"} {
		fmt.Fprintf(output, "%s{reason=%q} %d\n", name, reason, counts[i])
	}
}
