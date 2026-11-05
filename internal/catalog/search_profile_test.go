package catalog

import "testing"

func TestCoreLexicalSearchProfileGivesVendorIdentifiersExactPrecedence(t *testing.T) {
	t.Parallel()

	document := SearchDocument{Identifier: "vendor.example/agents/invoice-resolution", DisplayName: "Unrelated name"}
	if got := CoreLexicalScore(document, document.Identifier); got != 100 {
		t.Fatalf("vendor identifier exact score = %d, want 100", got)
	}
	if got := CatalogSearchExactIdentifier("URN:AIR:CATALOG.EXAMPLE.COM:finance:invoice-resolution"); got != "urn:air:catalog.example.com:finance:invoice-resolution" {
		t.Fatalf("canonical AIR search identifier = %q", got)
	}
}
