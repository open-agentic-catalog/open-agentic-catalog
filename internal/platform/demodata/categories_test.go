package demodata

import (
	"reflect"
	"strings"
	"testing"
)

func TestCategoriesBoundsAndManifestBinding(t *testing.T) {
	m, assets := manifestFixture(t)
	before, err := fixtureDecode(t, m, assets)
	if err != nil {
		t.Fatal(err)
	}
	categories := make([]string, 16)
	for i := range categories {
		categories[i] = strings.Repeat(string(rune(0x1f600+i)), 80)
	}
	categories[0] = "data, tools"
	m.Resources[0].Listing.Categories = categories
	after, err := fixtureDecode(t, m, assets)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after.Resources[0].Resource.Listing.Categories, categories) {
		t.Fatal("category spelling or literal comma changed")
	}
	if after.Digest == before.Digest || after.Resources[0].BodyDigest != before.Resources[0].BodyDigest {
		t.Fatal("listing category changes must bind the manifest without rewriting native evidence")
	}
}
