package catalog

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// UnmarshalJSON preserves arbitrary extension numbers in retained listing
// revisions, including nested CatalogRecord copies and private API readback.
// Request control admission belongs to CreateListingRevisionInput's decoder.
func (revision *ListingRevision) UnmarshalJSON(raw []byte) error {
	type retainedListingRevision ListingRevision
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	return decoder.Decode((*retainedListingRevision)(revision))
}

// PostgreSQL jsonb preserves numeric values, but changes exponent notation and
// negative zero. Hash exact decimal values independently of their spelling.
// This normalization is limited to listing digests; native evidence and other
// service digests retain their existing byte/encoding contracts.
type listingDigestExtensions map[string]any

func (extensions listingDigestExtensions) MarshalJSON() ([]byte, error) {
	raw, err := json.Marshal(map[string]any(extensions))
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := normalizeListingNumbers(&value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func normalizeListingNumbers(value *any) error {
	switch item := (*value).(type) {
	case map[string]any:
		for key, child := range item {
			if err := normalizeListingNumbers(&child); err != nil {
				return err
			}
			item[key] = child
		}
	case []any:
		for index := range item {
			if err := normalizeListingNumbers(&item[index]); err != nil {
				return err
			}
		}
	case json.Number:
		// Input has already passed encoding/json's number grammar. Never expand
		// exponent notation or convert through float64: work stays linear in the
		// input size, including very large integers and very small decimals.
		sign, mantissa := "", string(item)
		if strings.HasPrefix(mantissa, "-") {
			sign, mantissa = "-", mantissa[1:]
		}
		var exponent int64
		if index := strings.IndexAny(mantissa, "eE"); index >= 0 {
			var err error
			exponent, err = strconv.ParseInt(mantissa[index+1:], 10, 32)
			if err != nil {
				return err
			}
			mantissa = mantissa[:index]
		}
		if index := strings.IndexByte(mantissa, '.'); index >= 0 {
			exponent -= int64(len(mantissa) - index - 1)
			mantissa = mantissa[:index] + mantissa[index+1:]
		}
		mantissa = strings.TrimLeft(mantissa, "0")
		if mantissa == "" {
			*value = json.Number("0")
			return nil
		}
		coefficient := strings.TrimRight(mantissa, "0")
		exponent += int64(len(mantissa) - len(coefficient))
		// Keep the established encoding/json spelling for ordinary stored
		// numbers. Only precision and PostgreSQL notation changes are repaired.
		point := int64(len(coefficient)) + exponent
		number := coefficient
		switch {
		case point > 0 && point <= 21 && point >= int64(len(coefficient)):
			number += strings.Repeat("0", int(point)-len(coefficient))
		case point > 0 && point <= 21:
			number = coefficient[:int(point)] + "." + coefficient[int(point):]
		case point <= 0 && point > -6:
			number = "0." + strings.Repeat("0", int(-point)) + coefficient
		default:
			if len(coefficient) > 1 {
				number = coefficient[:1] + "." + coefficient[1:]
			}
			exponentText := strconv.FormatInt(point-1, 10)
			if point > 0 {
				exponentText = "+" + exponentText
			}
			number += "e" + exponentText
		}
		number = sign + number
		*value = json.Number(number)
	}
	return nil
}
