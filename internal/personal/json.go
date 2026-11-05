package personal

import (
	"encoding/json"
	"errors"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

// Reference is a closed discriminated control object. Presence of a field from
// the other origin is invalid even when its value is empty or zero.
func (r *Reference) UnmarshalJSON(raw []byte) error {
	type plain Reference
	var value plain
	if err := jsondoc.Decode(raw, &value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	allowed := map[string]bool{"origin": true}
	switch value.Origin {
	case "local":
		allowed["releaseId"], allowed["versionRevisionId"] = true, true
	case "foreign":
		for _, key := range []string{"kind", "sourceId", "presentationGeneration", "identifier", "versionPresent", "version"} {
			allowed[key] = true
		}
	default:
		return errors.New("saved reference origin is invalid")
	}
	for field := range fields {
		if !allowed[field] {
			return errors.New("saved reference contains a field from another origin")
		}
	}
	*r = Reference(value)
	return nil
}
