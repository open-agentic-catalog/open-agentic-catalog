// Tracing settings describe one optional exporter, including its exact endpoint,
// bounded queue and explicit permissions for private or plaintext collector access.

package config

import (
	"math"
	"net/netip"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

// applyTracing overlays a bounded duplicate-free JSON object onto tracing defaults
// and rejects fields outside the service-owned configuration type.
func applyTracing(c *Config, raw string) error {
	if len(raw) > 16<<10 || !strings.HasPrefix(strings.TrimSpace(raw), "{") || jsondoc.Validate([]byte(raw)) != nil {
		return safeerr.New("tracing override must be bounded duplicate-free JSON")
	}
	if err := jsondoc.Decode([]byte(raw), &c.Tracing); err != nil {
		return safeerr.New("tracing override contains invalid or unknown settings")
	}
	return nil
}

// Tracing configures one optional process-owned OTLP/HTTP protobuf exporter.
// Secret values are loaded from files by catalogd, never by configuration reports.
type Tracing struct {
	Enabled                bool     `json:"enabled"`
	Endpoint               string   `json:"endpoint,omitempty"`
	Installation           string   `json:"installation,omitempty"`
	SampleRatio            float64  `json:"sampleRatio"`
	TrustRemoteSampling    bool     `json:"trustRemoteSampling"`
	AllowHTTP              bool     `json:"allowHttp"`
	AllowedAddressPrefixes []string `json:"allowedAddressPrefixes,omitempty"`
	HeadersFile            string   `json:"headersFile,omitempty"`
	CAFile                 string   `json:"caFile,omitempty"`
	ClientCertificateFile  string   `json:"clientCertificateFile,omitempty"`
	ClientKeyFile          string   `json:"clientKeyFile,omitempty"`
	QueueSize              int      `json:"queueSize"`
	BatchSize              int      `json:"batchSize"`
	ExportTimeout          Duration `json:"exportTimeout"`
}

// Validate checks enabled exporter inputs without loading credentials or trust.
// Disabled tracing needs no endpoint; plaintext export requires explicit private
// address prefixes and forbids authentication headers and client credentials.
func (t Tracing) Validate() error {
	if !t.Enabled {
		return nil
	}
	u, err := url.Parse(t.Endpoint)
	if err != nil || len(t.Endpoint) > 2048 || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(t.Endpoint, "\r\n\x00") {
		return safeerr.New("config: tracing.endpoint must be an absolute URL without credentials, query or fragment")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && t.AllowHTTP) {
		return safeerr.New("config: tracing.endpoint requires HTTPS or explicit allowHttp")
	}
	if u.RawPath != "" || (u.Path != "" && path.Clean(u.Path) != u.Path) {
		return safeerr.New("config: tracing.endpoint must use a canonical unescaped path")
	}
	if u.Scheme == "http" && (t.HeadersFile != "" || t.ClientCertificateFile != "" || t.ClientKeyFile != "" || len(t.AllowedAddressPrefixes) == 0) {
		return safeerr.New("config: plaintext tracing requires explicit private address prefixes and no credentials")
	}
	for _, raw := range t.AllowedAddressPrefixes {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil || prefix != prefix.Masked() || (u.Scheme == "http" && !privateTracePrefix(prefix)) {
			return safeerr.New("config: tracing address prefixes must be canonical and plaintext prefixes private or loopback")
		}
	}
	if len(t.AllowedAddressPrefixes) > 16 || len(t.Installation) == 0 || len(t.Installation) > 128 || strings.ContainsAny(t.Installation, "\r\n\x00") {
		return safeerr.New("config: tracing requires a bounded installation identity and at most sixteen address prefixes")
	}
	if math.IsNaN(t.SampleRatio) || math.IsInf(t.SampleRatio, 0) || t.SampleRatio < 0 || t.SampleRatio > 1 || t.QueueSize < 16 || t.QueueSize > 8192 || t.BatchSize < 1 || t.BatchSize > 512 || t.BatchSize > t.QueueSize || t.ExportTimeout.Duration < time.Second || t.ExportTimeout.Duration > 10*time.Second {
		return safeerr.New("config: tracing sampling, queue, batch or export timeout is outside its bounds")
	}
	if (t.ClientCertificateFile == "") != (t.ClientKeyFile == "") {
		return safeerr.New("config: tracing client certificate and key must be supplied together")
	}
	return nil
}

// privateTracePrefix requires the entire prefix to fit a private or loopback range.
func privateTracePrefix(prefix netip.Prefix) bool {
	for _, raw := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8", "fc00::/7", "::1/128"} {
		allowed := netip.MustParsePrefix(raw)
		if prefix.Addr().BitLen() == allowed.Addr().BitLen() && prefix.Bits() >= allowed.Bits() && allowed.Contains(prefix.Addr()) {
			return true
		}
	}
	return false
}
