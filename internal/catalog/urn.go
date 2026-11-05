// AIR identifiers normalize publisher domains while preserving the validated
// namespace and resource-name segments used for canonical resource identity.

package catalog

import (
	"fmt"
	"net"
	"strings"
	"unicode/utf8"
)

type AIRIdentifier struct {
	Publisher string   `json:"publisher"`
	Namespace []string `json:"namespace"`
	Name      string   `json:"name"`
}

// ParseAIRIdentifier validates a bounded urn:air value and normalizes the
// publisher domain. Namespace and name segments retain their case.
func ParseAIRIdentifier(value string) (AIRIdentifier, error) {
	if len(value) > 512 || !utf8.ValidString(value) {
		return AIRIdentifier{}, E(CodeInvalidArgument, "identifier is invalid or too long")
	}
	parts := strings.Split(value, ":")
	if len(parts) < 4 || !strings.EqualFold(parts[0], "urn") || !strings.EqualFold(parts[1], "air") {
		return AIRIdentifier{}, E(CodeInvalidArgument, "identifier must use urn:air")
	}
	publisher := strings.ToLower(strings.TrimSuffix(parts[2], "."))
	if publisher == "" || net.ParseIP(publisher) != nil || !validDomain(publisher) {
		return AIRIdentifier{}, E(CodeInvalidArgument, "identifier publisher must be a normalized domain")
	}
	segments := parts[3:]
	for _, segment := range segments {
		if !validAIRSegment(segment) {
			return AIRIdentifier{}, E(CodeInvalidArgument, "identifier contains an invalid segment")
		}
	}
	return AIRIdentifier{Publisher: publisher, Namespace: segments[:len(segments)-1], Name: segments[len(segments)-1]}, nil
}

// String returns the urn:air spelling of the identifier with a lowercase
// publisher domain; it does not revalidate fields.
func (id AIRIdentifier) String() string {
	segments := append(append([]string{}, id.Namespace...), id.Name)
	return fmt.Sprintf("urn:air:%s:%s", strings.ToLower(id.Publisher), strings.Join(segments, ":"))
}

// validAIRSegment reports whether a nonempty segment fits the ASCII identifier
// alphabet and excludes dot traversal segments.
func validAIRSegment(value string) bool {
	if value == "" || value == "." || value == ".." || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}

// validDomain reports whether a lowercase dotted DNS name satisfies the
// publisher label and total-length bounds.
func validDomain(value string) bool {
	if len(value) > 253 || !strings.Contains(value, ".") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
				continue
			}
			return false
		}
	}
	return true
}
