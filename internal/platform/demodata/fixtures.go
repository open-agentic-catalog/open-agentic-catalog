// Package demodata validates inert sample authoring inputs; it never installs or invokes resources.
package demodata

import (
	"encoding/json"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/demoarchive"
	"slices"
	"strings"
	"sync"
)

const TenantID = demoarchive.TenantID

// Sample specifies a published example from the embedded default manifest.
type Sample struct {
	Key, Name, Description, Namespace string
	Kind                              catalog.Kind
}

// DefaultPlan loads the one checked-in inventory. Invalid build inputs are a developer error.
var defaultPlans = sync.OnceValue(func() map[string]Plan {
	result := map[string]Plan{}
	for _, profile := range []string{"minimal", "expanded"} {
		plan, err := Load("", profile, "")
		if err != nil {
			panic("invalid embedded sample manifest")
		}
		result[profile] = plan
	}
	return result
})

func DefaultPlan(profile, origin string) Plan {
	base, ok := defaultPlans()[profile]
	if !ok {
		panic("invalid default sample profile")
	}
	// Return independently owned values, including nested representation bodies and package bytes.
	raw, err := json.Marshal(base)
	if err != nil {
		panic("invalid default sample plan")
	}
	var plan Plan
	if json.Unmarshal(raw, &plan) != nil {
		panic("invalid default sample plan")
	}
	for i := range plan.Resources {
		item := &plan.Resources[i]
		item.Archive = slices.Clone(base.Resources[i].Archive)
		item.Representation.Data = slices.Clone(base.Resources[i].Representation.Data)
		if len(item.Archive) > 0 {
			item.Representation.URL = strings.TrimSuffix(origin, "/") + "/" + item.Resource.Key + ".zip"
		}
	}
	// Resolve the same plan identity through the shared canonical algorithm.
	canonicalizePlanDigest(&plan)
	return plan
}

func Samples() []Sample {
	plan := DefaultPlan("expanded", "")
	result := make([]Sample, 0, len(plan.Resources))
	// Preserve the former public inventory order; the manifest itself owns that order.
	// Profiles own command order, not Samples' grouping.
	for _, kind := range []catalog.Kind{catalog.KindAgent, catalog.KindMCPServer, catalog.KindSkill, catalog.KindPlugin} {
		for _, item := range plan.Resources {
			r := item.Resource
			if r.State == "published" && r.Kind == kind {
				result = append(result, Sample{r.Key, r.Name, r.Description, r.Namespace, r.Kind})
			}
		}
	}
	return result
}

// Archive returns exact default bytes only for a selected manifest Plugin path.
func Archive(path string) ([]byte, string, bool) {
	for _, item := range DefaultPlan("expanded", "").Resources {
		if len(item.Archive) > 0 && path == "/"+item.Resource.Key+".zip" {
			return slices.Clone(item.Archive), item.BodyDigest, true
		}
	}
	return nil, "", false
}
