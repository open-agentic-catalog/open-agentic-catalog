package demodata

import (
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/artwork"
	"testing"
)

func TestOptionalArtworkProfile(t *testing.T) {
	plan, err := Load("", "artwork", "https://127.0.0.1:19443")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Resources) != 1 {
		t.Fatal("optional profile must select one owned fixture")
	}
	input := plan.Resources[0].Resource.Listing.Artwork
	if input == nil || artwork.ValidateInput(*input, artwork.DefaultLimits()) != nil || input.Icon.URL != "https://127.0.0.1:19443/artwork-icon.png" || input.Screenshots[0].URL != "https://127.0.0.1:19443/artwork-screen.jpg" || input.Icon.Rights.Basis != "owned" {
		t.Fatalf("unexpected fixture: %#v", input)
	}
	for name, count := range map[string]int{"minimal": 5, "expanded": 14, "curated": 14} {
		p, e := Load("", name, "")
		if e != nil || len(p.Resources) != count {
			t.Fatalf("existing %s profile changed: %v", name, e)
		}
	}
}
