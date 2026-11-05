package fetcher

import "net/netip"

// PublicReferenceAllowed checks a retained HTTPS reference against the same URL
// and literal-address rules as public fetching, including operator denials. It
// performs no DNS lookup, creates no client and grants no network authority.
// Hostnames still require every resolved address to pass Router during fetching.
func PublicReferenceAllowed(value string, deniedPrefixes []netip.Prefix) bool {
	boundary := Fetcher{policy: Policy{DeniedAddressPrefixes: deniedPrefixes}}
	_, err := boundary.validateURL(value)
	return err == nil
}
