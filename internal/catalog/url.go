// Retained URL checks reject unsafe reference syntax and known credential query
// names; network authorization remains the outbound fetcher's responsibility.

package catalog

import (
	"net/url"
	"strconv"
	"strings"
)

// ValidExternalHTTPSURL checks the syntax of a retained HTTPS reference and
// rejects user information and recognized secret query keys. It does not resolve
// DNS or authorize outbound access; a fetch still requires the hardened boundary.
func ValidExternalHTTPSURL(value string) bool {
	return validExternalHTTPSURL(value, false)
}

// ValidDocumentationHTTPSURL additionally permits a document fragment.
func ValidDocumentationHTTPSURL(value string) bool {
	return validExternalHTTPSURL(value, true)
}

// validExternalHTTPSURL checks bounded HTTPS syntax and credential-query
// exclusions with a caller-selected fragment policy.
func validExternalHTTPSURL(value string, allowFragment bool) bool {
	if value == "" || len(value) > 2048 || strings.ContainsAny(value, "\r\n\x00") {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Hostname() == "" || parsed.User != nil || parsed.Opaque != "" || !allowFragment && parsed.Fragment != "" {
		return false
	}
	for _, character := range parsed.Hostname() {
		if character > 127 || character == '%' {
			return false
		}
	}
	if port := parsed.Port(); port != "" {
		value, parseErr := strconv.ParseUint(port, 10, 16)
		if parseErr != nil || value == 0 {
			return false
		}
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return false
	}
	for name := range query {
		if secretURLQueryParameter(name) {
			return false
		}
	}
	return true
}

// secretURLQueryParameter reports whether a normalized query name is a
// recognized credential or signature field.
func secretURLQueryParameter(name string) bool {
	normalized := strings.NewReplacer("-", "", "_", "").Replace(strings.ToLower(name))
	switch normalized {
	case "accesstoken", "apikey", "authorization", "credential", "password", "secret", "sig", "signature", "token", "xamzcredential", "xamzsecuritytoken", "xamzsignature":
		return true
	default:
		return false
	}
}
