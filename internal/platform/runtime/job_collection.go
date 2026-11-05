package runtime

import (
	"sort"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
)

// This is deployment target selection, not permission evaluation. The caller's
// current family permissions and retained targets still select the actual rows.
func configuredJobCollectionTargets(tenantID string, bindings map[string]publicationBinding, semanticProfile string) jobs.ListTargets {
	targets := jobs.ListTargets{Enforced: true, SemanticProfile: semanticProfile}
	for host, binding := range bindings {
		if binding.tenantID == tenantID {
			targets.PublicationHosts = append(targets.PublicationHosts, host)
		}
	}
	sort.Strings(targets.PublicationHosts)
	// Preserve the existing single-deployment-host default; a multi-host Job
	// must retain an explicit host even if only one belongs to this tenant.
	if len(bindings) == 1 && len(targets.PublicationHosts) == 1 {
		targets.DefaultPublicationHost = targets.PublicationHosts[0]
	}
	return targets
}
