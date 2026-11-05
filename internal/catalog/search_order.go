package catalog

import jsonv2 "encoding/json/v2"

// UnmarshalJSON distinguishes an omitted default from an explicitly invalid
// order, including within saved criteria. The Go zero value remains the default.
func (order *SearchOrder) UnmarshalJSON(document []byte) error {
	var value string
	if err := jsonv2.Unmarshal(document, &value); err != nil {
		return E(CodeInvalidArgument, "catalog search order is invalid")
	}
	switch SearchOrder(value) {
	case SearchOrderNameAsc, SearchOrderNameDesc, SearchOrderKindAsc, SearchOrderKindDesc, SearchOrderPublisherAsc, SearchOrderPublisherDesc:
		*order = SearchOrder(value)
		return nil
	}
	return E(CodeInvalidArgument, "catalog search order is invalid")
}
