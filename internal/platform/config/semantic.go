// Semantic configuration binds query and indexing work to one deployment-owned
// embedding profile. It contains no secret values and never selects a model per request.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/netip"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

// Semantic describes the one optional embedding endpoint and bounded work policy.
// ProfileRevision changes when an operator changes otherwise unobservable model weights.
type Semantic struct {
	Enabled                bool     `json:"enabled"`
	Endpoint               string   `json:"endpoint,omitempty"`
	Model                  string   `json:"model,omitempty"`
	ResponseModel          string   `json:"responseModel,omitempty"`
	ProfileRevision        string   `json:"profileRevision,omitempty"`
	Dimensions             int      `json:"dimensions,omitempty"`
	RequestedDimensions    int      `json:"requestedDimensions,omitempty"`
	QueryPrefix            string   `json:"queryPrefix,omitempty"`
	DocumentPrefix         string   `json:"documentPrefix,omitempty"`
	SimilarityCutoff       float64  `json:"similarityCutoff"`
	TokenFile              string   `json:"tokenFile,omitempty"`
	CAFile                 string   `json:"caFile,omitempty"`
	AllowHTTP              bool     `json:"allowHttp"`
	AllowedAddressPrefixes []string `json:"allowedAddressPrefixes,omitempty"`
	ExcludedTenants        []string `json:"excludedTenants,omitempty"`
	QueryTimeout           Duration `json:"queryTimeout"`
	BatchTimeout           Duration `json:"batchTimeout"`
	QueryConcurrency       int      `json:"queryConcurrency"`
	BatchConcurrency       int      `json:"batchConcurrency"`
	BatchSize              int      `json:"batchSize"`
	MaxInputBytes          int      `json:"maxInputBytes"`
}

// SemanticDefaults supplies bounded work settings without choosing a provider.
func SemanticDefaults() Semantic {
	return Semantic{SimilarityCutoff: 0.5, QueryTimeout: Duration{2 * time.Second}, BatchTimeout: Duration{10 * time.Second}, QueryConcurrency: 4, BatchConcurrency: 1, BatchSize: 16, MaxInputBytes: 8192}
}

// Validate checks inputs without contacting the provider or loading credentials.
// Plain HTTP requires explicitly permitted private addresses and no bearer secret.
func (s Semantic) Validate() error {
	if !s.Enabled {
		return nil
	}
	u, err := url.Parse(s.Endpoint)
	if err != nil || len(s.Endpoint) > 2048 || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || u.Path == "" || path.Clean(u.Path) != u.Path || strings.ContainsAny(s.Endpoint, "\r\n\x00") {
		return safeerr.New("config: semanticSearch.endpoint must be an exact canonical URL without credentials, query or fragment")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && s.AllowHTTP) {
		return safeerr.New("config: semanticSearch requires HTTPS or explicit private HTTP")
	}
	if len(s.AllowedAddressPrefixes) > 16 {
		return safeerr.New("config: too many semantic address prefixes")
	}
	for _, raw := range s.AllowedAddressPrefixes {
		prefix, e := netip.ParsePrefix(raw)
		if e != nil || prefix != prefix.Masked() || (u.Scheme == "http" && !privateTracePrefix(prefix)) {
			return safeerr.New("config: invalid semantic address prefix")
		}
	}
	if u.Scheme == "http" && (s.TokenFile != "" || len(s.AllowedAddressPrefixes) == 0) {
		return safeerr.New("config: plaintext embeddings require explicit private prefixes and no credentials")
	}
	if !boundedSemanticText(s.Model, 256) || !boundedSemanticText(s.ProfileRevision, 128) || (s.ResponseModel != "" && !boundedSemanticText(s.ResponseModel, 256)) {
		return safeerr.New("config: semantic model and profile revision must be bounded nonempty text")
	}
	if s.Dimensions < 1 || s.Dimensions > 4096 || s.RequestedDimensions < 0 || s.RequestedDimensions > 4096 || (s.RequestedDimensions != 0 && s.RequestedDimensions != s.Dimensions) {
		return safeerr.New("config: invalid semantic dimensions")
	}
	if math.IsNaN(s.SimilarityCutoff) || math.IsInf(s.SimilarityCutoff, 0) || s.SimilarityCutoff < -1 || s.SimilarityCutoff > 1 {
		return safeerr.New("config: semantic similarity cutoff must be between -1 and 1")
	}
	if s.MaxInputBytes < 64 || s.MaxInputBytes > 32768 || len(s.QueryPrefix) > 1024 || len(s.DocumentPrefix) > 1024 || !utf8.ValidString(s.QueryPrefix) || !utf8.ValidString(s.DocumentPrefix) || strings.ContainsRune(s.QueryPrefix+s.DocumentPrefix, 0) || len(s.QueryPrefix) >= s.MaxInputBytes || len(s.DocumentPrefix)+64 > s.MaxInputBytes {
		return safeerr.New("config: invalid semantic input bounds or prefixes")
	}
	if s.QueryTimeout.Duration < 100*time.Millisecond || s.QueryTimeout.Duration > 10*time.Second || s.BatchTimeout.Duration < time.Second || s.BatchTimeout.Duration > 20*time.Second || s.QueryConcurrency < 1 || s.QueryConcurrency > 32 || s.BatchConcurrency < 1 || s.BatchConcurrency > 8 || s.BatchSize < 1 || s.BatchSize > 32 {
		return safeerr.New("config: semantic work settings are outside bounds")
	}
	if len(s.ExcludedTenants) > 256 {
		return safeerr.New("config: too many semantic tenant exclusions")
	}
	seen := make(map[string]bool, len(s.ExcludedTenants))
	for _, tenant := range s.ExcludedTenants {
		tenant = strings.ToLower(tenant)
		if !identity.ValidTenantID(tenant) || seen[tenant] {
			return safeerr.New("config: invalid or duplicate semantic tenant exclusion")
		}
		seen[tenant] = true
	}
	for _, f := range []string{s.TokenFile, s.CAFile} {
		if len(f) > 4096 || strings.ContainsAny(f, "\r\n\x00") {
			return safeerr.New("config: invalid semantic credential path")
		}
	}
	return nil
}

// boundedSemanticText prevents control characters and misleading blank identities.
func boundedSemanticText(value string, limit int) bool {
	return len(value) > 0 && len(value) <= limit && utf8.ValidString(value) && strings.TrimSpace(value) == value && strings.IndexFunc(value, unicode.IsControl) < 0
}

// PermitsTenant applies deployment exclusions before any model call or vector read.
func (s Semantic) PermitsTenant(tenant string) bool {
	return s.Enabled && identity.ValidTenantID(tenant) && !slices.ContainsFunc(s.ExcludedTenants, func(excluded string) bool { return strings.EqualFold(excluded, tenant) })
}

// EmbeddingProfile binds vector-compatible inputs without endpoint or secrets.
// Transport rotation and ranking-only changes must not force a vector rebuild.
func (s Semantic) EmbeddingProfile() string {
	raw, err := json.Marshal(struct {
		Model          string `json:"model"`
		Revision       string `json:"revision"`
		QueryPrefix    string `json:"queryPrefix"`
		DocumentPrefix string `json:"documentPrefix"`
		Template       string `json:"template"`
		Dimensions     int    `json:"dimensions"`
		MaxInputBytes  int    `json:"maxInputBytes"`
	}{s.Model, s.ProfileRevision, s.QueryPrefix, s.DocumentPrefix, catalog.SemanticTemplate + "-l2", s.Dimensions, s.MaxInputBytes})
	if err != nil {
		return "" // The closed projection contains only JSON-encodable strings and integers.
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// RankingProfile binds continuation to vector inputs, lexical rules and cutoff.
// Transport-only changes do not invalidate a compatible continuation.
func (s Semantic) RankingProfile() string {
	sum := sha256.Sum256([]byte("hybrid-rrf60-v1\x00" + catalog.CoreSearchProfile + "\x00" + s.EmbeddingProfile() + "\x00" + strconv.FormatFloat(s.SimilarityCutoff, 'g', -1, 64)))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// applySemantic overlays one closed JSON configuration group onto bounded defaults.
func applySemantic(c *Config, raw string) error {
	if len(raw) > 32768 || !strings.HasPrefix(strings.TrimSpace(raw), "{") || jsondoc.Validate([]byte(raw)) != nil {
		return safeerr.New("semantic override must be bounded duplicate-free JSON")
	}
	var value any
	if json.Unmarshal([]byte(raw), &value) != nil || containsNull(value) {
		return safeerr.New("semantic override must not contain null values")
	}
	if err := jsondoc.Decode([]byte(raw), &c.SemanticSearch); err != nil {
		return safeerr.New("semantic override contains invalid or unknown settings")
	}
	return nil
}
