// Package demoarchive defines the exact local archive identities shared by preparation and runtime policy.
// It has no sample inventory, execution, network client or production artifact authority.
package demoarchive

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

const TenantID = "00000000-0000-7000-8000-000000000001"
const MaxArchiveBytes = 64 << 10
const MaxAllowlistBytes = 32 << 10
const MaxArchives = 64

var pathPattern = regexp.MustCompile(`^/[a-z0-9]+(?:-[a-z0-9]+)*\.zip$`)
var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

type Entry struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Size   int    `json:"size"`
}
type Allowlist struct {
	Format   int     `json:"format"`
	TenantID string  `json:"tenantId"`
	Entries  []Entry `json:"entries"`
}

func (a Allowlist) Validate() error {
	if a.Format != 1 || a.TenantID != TenantID || a.Entries == nil || len(a.Entries) > MaxArchives {
		return errors.New("invalid development archive allowlist")
	}
	previous := ""
	for _, e := range a.Entries {
		if len(e.Path) > 70 || !pathPattern.MatchString(e.Path) || !digestPattern.MatchString(e.Digest) || e.Size < 1 || e.Size > MaxArchiveBytes || e.Path <= previous {
			return errors.New("invalid development archive identity")
		}
		previous = e.Path
	}
	return nil
}
func Decode(raw []byte) (Allowlist, error) {
	var a Allowlist
	if len(raw) > MaxAllowlistBytes || jsondoc.Decode(raw, &a) != nil {
		return a, errors.New("invalid development archive allowlist")
	}
	return a, a.Validate()
}
func (a Allowlist) Digest() string {
	raw, err := json.Marshal(a)
	if err != nil {
		panic("invalid development archive identity")
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
}
func ValidateOrigin(origin string) error {
	u, err := url.Parse(origin)
	if err != nil || len(origin) > 256 || u.Scheme != "https" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || strings.ContainsAny(origin, "\r\n\x00") {
		return errors.New("invalid local archive origin")
	}
	if u.Port() != "" {
		port, e := strconv.Atoi(u.Port())
		if e != nil || port < 1 || port > 65535 {
			return errors.New("invalid local archive port")
		}
	}
	ip, e := netip.ParseAddr(u.Hostname())
	if e != nil || ip.Is4In6() || ip.Zone() != "" || !(ip.IsPrivate() || ip.IsLoopback()) || ip.String() != u.Hostname() {
		return errors.New("invalid local archive address")
	}
	return nil
}
