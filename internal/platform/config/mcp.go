package config

import (
	"net/url"
	"os"
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

// MCP configures the API-only, stateless, read-only MCP transport.
type MCP struct {
	Enabled        bool     `json:"enabled"`
	AllowedOrigins []string `json:"allowedOrigins,omitempty"`
	MaxToolBytes   int      `json:"maxToolBytes,omitempty"`
	MaxConcurrency int      `json:"maxConcurrency,omitempty"`
}

func (c MCP) Validate(production bool) error {
	if c.MaxToolBytes < 1024 || c.MaxToolBytes > 1<<20 || c.MaxConcurrency < 1 || c.MaxConcurrency > 16 || len(c.AllowedOrigins) > 64 {
		return safeerr.New("config: mcp requires maxToolBytes 1024-1048576, maxConcurrency 1-16 and at most 64 origins")
	}
	seen := map[string]bool{}
	for _, origin := range c.AllowedOrigins {
		u, err := url.Parse(origin)
		if err != nil || !validURLHost(u) || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Scheme != "http" && u.Scheme != "https" || production && u.Scheme != "https" || origin != u.Scheme+"://"+u.Host || strings.ToLower(u.Host) != u.Host || seen[origin] {
			return safeerr.New("config: mcp.allowedOrigins requires unique exact HTTP(S) origins, HTTPS in production")
		}
		seen[origin] = true
	}
	if c.Enabled && os.Getenv("MCPGODEBUG") != "" {
		return safeerr.New("config: MCPGODEBUG overrides are unsupported with HTTP MCP; unset MCPGODEBUG")
	}
	return nil
}

// ValidatePublicOrigin reuses the HTTP public URL contract for direct server wiring.
func ValidatePublicOrigin(value string) error {
	return (Config{HTTP: HTTP{PublicBaseURL: value}}).validatePublicBaseURL()
}
