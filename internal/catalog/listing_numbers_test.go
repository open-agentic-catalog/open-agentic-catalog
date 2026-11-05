package catalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"testing"
)

func TestListingRevisionCopyPreservesExtensionNumbers(t *testing.T) {
	original := ListingRevision{Extensions: map[string]any{"integer": json.Number("9007199254740993"), "decimal": json.Number("0.123456789012345678901"), "exponent": json.Number("1e999"), "nested": map[string]any{"null": nil, "ordinary": true}}}
	copied := clone(original)
	for _, key := range []string{"integer", "decimal", "exponent"} {
		if copied.Extensions[key] != original.Extensions[key] {
			t.Fatalf("retained %s changed", key)
		}
	}
	copiedMap, copiedOK := copied.Extensions["nested"].(map[string]any)
	originalMap, originalOK := original.Extensions["nested"].(map[string]any)
	if !copiedOK || !originalOK {
		t.Fatal("nested extension maps missing")
	}
	copiedMap["ordinary"] = false
	if originalMap["ordinary"] != true {
		t.Fatal("copy aliases original extensions")
	}
	nested := clone(struct{ Listing ListingRevision }{Listing: original})
	if nested.Listing.Extensions["exponent"] != json.Number("1e999") {
		t.Fatal("nested retained listing copy changed numeric values")
	}
}

func TestListingDigestKeepsExistingNumericEncodings(t *testing.T) {
	for _, number := range []float64{0, 100, 1.2, 0.1, 1e20, 1e21, 0.000001, 1e-7, -1.2e-8} {
		values := map[string]any{"number": number}
		established, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		normalized, err := json.Marshal(listingDigestExtensions(values))
		if err != nil || !bytes.Equal(established, normalized) {
			t.Fatalf("existing numeric digest spelling changed: %s -> %s (%v)", established, normalized, err)
		}
	}
}

func TestListingDigestPreservesDecimalValuesAcrossJSONBNotation(t *testing.T) {
	for _, pair := range [][2]string{
		{"1e999", "1" + strings.Repeat("0", 999)},
		{"1.230e-5", "0.00001230"},
		{"-0.000e+10", "0.000"},
		{"9007199254740993", "9007199254740993.00"},
		{"-0.123456789012345678901", "-123456789012345678901e-21"},
	} {
		original := ListingRevision{Extensions: map[string]any{"nested": []any{json.Number(pair[0]), "unchanged", nil, true}}}
		var err error
		original.Digest, err = digestJSON(revisionDigestInput(original))
		if err != nil {
			t.Fatal(err)
		}
		retained := clone(original)
		retainedValues, ok := retained.Extensions["nested"].([]any)
		if !ok {
			t.Fatal("nested extension array missing")
		}
		retainedValues[0] = json.Number(pair[1])
		if err := ValidateListingRevisionDigest(retained); err != nil {
			t.Fatalf("equivalent %s and %s differ: %v", pair[0], pair[1], err)
		}
		retainedValues[0] = json.Number("2")
		if err := ValidateListingRevisionDigest(retained); err == nil {
			t.Fatal("changed numeric value preserved digest")
		}
		originalValues, ok := original.Extensions["nested"].([]any)
		if !ok || originalValues[0] != json.Number(pair[0]) {
			t.Fatal("digest normalization changed retained input")
		}
	}
	for _, bad := range []any{json.Number("invalid"), json.Number("1e999999999999"), func() {}} {
		if _, err := digestJSON(revisionDigestInput(ListingRevision{Extensions: map[string]any{"bad": bad}})); err == nil {
			t.Fatal("invalid or unsupported numeric input accepted")
		}
	}
}

func TestListingDigestNormalizationPreservesExactValue(t *testing.T) {
	for _, input := range []string{"0", "-0.000e+10", "1e-6", "1e-7", "1e20", "1e21", "1e999", "-1.230e-999", "9007199254740993", "-0.123456789012345678901", "123456789012345678901.23456789"} {
		t.Run(input, func(t *testing.T) {
			raw, err := json.Marshal(listingDigestExtensions{"number": json.Number(input)})
			if err != nil {
				t.Fatal(err)
			}
			var result map[string]json.Number
			if err := json.Unmarshal(raw, &result); err != nil {
				t.Fatal(err)
			}
			// Rational arithmetic is independent of the digest's decimal formatter.
			want, wantOK := new(big.Rat).SetString(input)
			got, gotOK := new(big.Rat).SetString(result["number"].String())
			if !wantOK || !gotOK || want.Cmp(got) != 0 {
				t.Fatalf("decimal value changed: %s -> %s", input, result["number"])
			}
		})
	}
}

func TestListingUnsupportedNumberStopsBeforeWrite(t *testing.T) {
	f, repository := lifecycleFailureFixture(t, "revise listing")
	input := CreateListingRevisionInput{VersionRevisionID: "revision", PublisherID: "publisher", DisplayName: "Example", Description: "A governed listing", Visibility: VisibilityTenant, Extensions: map[string]any{"number": json.Number("1e999999999999")}}
	_, _, err := f.service.CreateListingRevision(f.adminContext(), lifecycleListingID, input, 1)
	if ErrorCodeOf(err) != CodeInvalidArgument || len(repository.writes) != 0 {
		t.Fatalf("unsupported extension number must fail before repository write: %v", err)
	}
	// This fixture returns a sentinel at its write port. An otherwise identical
	// supported number must reach that port, proving the negative guard matters.
	input.Extensions["number"] = json.Number("1e999")
	_, _, err = f.service.CreateListingRevision(f.adminContext(), lifecycleListingID, input, 1)
	if !errors.Is(err, repository.err) || len(repository.writes) != 1 {
		t.Fatalf("supported extension number did not reach the write port: %v", err)
	}
}
