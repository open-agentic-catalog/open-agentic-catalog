package demodata

import "github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"

// Representation resolves the default manifest's exact input, including the selected local archive origin.
func Representation(sample Sample, archiveOrigin string) catalog.RepresentationInput {
	for _, item := range DefaultPlan("expanded", archiveOrigin).Resources {
		if item.Resource.Key == sample.Key {
			return item.Representation
		}
	}
	return catalog.RepresentationInput{}
}
