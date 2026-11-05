// Package web hosts the optional Catalog client. It has no database access;
// authentication and API forwarding use fixed operator-configured destinations.
package web

import (
	"encoding/hex"
	"io"
	"net"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/diagnostics"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

// Config contains non-secret startup settings. Credentials are mounted files.
type Config struct {
	LogLevel             string   `json:"logLevel"`
	LogFormat            string   `json:"logFormat"`
	Listen               string   `json:"listen"`
	Environment          string   `json:"environment"`
	Origin               string   `json:"origin"`
	BasePath             string   `json:"basePath"`
	Assets               string   `json:"assets"`
	Upstream             string   `json:"upstream"`
	APICAFile            string   `json:"apiCAFile"`
	CookieName           string   `json:"cookieName"`
	KeysFile             string   `json:"keysFile"`
	SessionSeconds       int      `json:"sessionSeconds"`
	RequestBytes         int64    `json:"requestBytes"`
	ResponseBytes        int64    `json:"responseBytes"`
	TimeoutSeconds       int      `json:"timeoutSeconds"`
	AuthMode             string   `json:"authMode"`
	DevelopmentUsersFile string   `json:"developmentUsersFile"`
	Issuer               string   `json:"issuer"`
	IssuerCAFile         string   `json:"issuerCAFile"`
	ClientID             string   `json:"clientId"`
	ClientSecretFile     string   `json:"clientSecretFile"`
	Scopes               []string `json:"scopes,omitempty"`
	DocumentationURL     string   `json:"documentationUrl"`
}

// keyPair is one mounted signing/encryption pair; order selects the active writer.
type keyPair struct {
	Hash  string `json:"hash"`
	Block string `json:"block"`
}

// developmentUser binds a browser-visible alias to a server-only development token.
type developmentUser struct {
	Alias          string `json:"alias"`
	Label          string `json:"label"`
	Description    string `json:"description,omitempty"`
	ProfileGroup   string `json:"profileGroup,omitempty"`
	Elevated       bool   `json:"elevated,omitempty"`
	SeparateTenant bool   `json:"separateTenant,omitempty"`
	Token          string `json:"token"`
}

var identifier = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)
var basePathPattern = regexp.MustCompile(`^(?:/[a-zA-Z0-9_-]+)+$`)

// LoadConfig reads strict, bounded JSON without loading an environment file.
func LoadConfig(filename string) (Config, error) {
	c := Config{LogLevel: "info", LogFormat: "text"}
	if err := readJSONFile(filename, &c); err != nil {
		return c, safeerr.New("UI configuration is not valid bounded JSON")
	}
	return c, c.validate()
}

// validate checks deployment URLs, resource bounds and mutually exclusive login modes.
func (c Config) validate() error {
	if err := diagnostics.ValidateLogging(c.LogLevel, c.LogFormat); err != nil {
		return err
	}
	if c.Environment != "development" && c.Environment != "production" {
		return safeerr.New("UI environment must be development or production")
	}
	origin, err := fixedURL(c.Origin)
	if err != nil || origin.Path != "" {
		return safeerr.New("UI origin must be an exact HTTP(S) origin without a path")
	}
	local := origin.Hostname() == "localhost" || net.ParseIP(origin.Hostname()).IsLoopback()
	if origin.Scheme != "https" && (c.Environment != "development" || !local) {
		return safeerr.New("UI HTTP origin requires loopback development")
	}
	if c.BasePath != "" && (!basePathPattern.MatchString(c.BasePath) || path.Clean(c.BasePath) != c.BasePath) {
		return safeerr.New("UI basePath must be empty or a canonical path without trailing slash")
	}
	if c.BasePath == "/docs" || strings.HasPrefix(c.BasePath, "/docs/") {
		return safeerr.New("UI basePath must not use the reserved /docs route")
	}
	upstream, err := fixedURL(c.Upstream)
	if err != nil || upstream.Path != "" {
		return safeerr.New("UI upstream must be a fixed HTTP(S) origin")
	}
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return safeerr.New("UI listen must contain host and port")
	}
	if !identifier.MatchString(c.CookieName) || c.KeysFile == "" || c.Assets == "" {
		return safeerr.New("UI cookieName, keysFile and assets are required")
	}
	if c.SessionSeconds < 60 || c.SessionSeconds > 28800 || c.TimeoutSeconds < 1 || c.TimeoutSeconds > 300 {
		return safeerr.New("UI session must be 60..28800 seconds and timeout 1..300 seconds")
	}
	if c.RequestBytes < 1 || c.RequestBytes > 64<<20 || c.ResponseBytes < 1 || c.ResponseBytes > 64<<20 {
		return safeerr.New("UI request and response bounds must be 1..67108864 bytes")
	}
	if c.DocumentationURL != "" {
		if u, err := fixedURL(c.DocumentationURL); err != nil || u.Scheme != "https" {
			return safeerr.New("UI documentationUrl must use HTTPS")
		}
	}
	switch c.AuthMode {
	case "development":
		if c.Environment != "development" || !local || c.DevelopmentUsersFile == "" || c.Issuer != "" || c.ClientID != "" || c.ClientSecretFile != "" || c.IssuerCAFile != "" || len(c.Scopes) != 0 {
			return safeerr.New("UI development login requires loopback development and no OIDC settings")
		}
	case "oidc":
		issuer, err := fixedURL(c.Issuer)
		if err != nil || (issuer.Scheme != "https" && c.Environment != "development") || c.ClientID == "" || c.ClientSecretFile == "" || c.DevelopmentUsersFile != "" {
			return safeerr.New("UI OIDC requires issuer, client and secret file, with no development users")
		}
		if len(c.Scopes) > 16 {
			return safeerr.New("too many UI OIDC scopes")
		}
		seen := map[string]bool{}
		for _, scope := range c.Scopes {
			if scope == "offline_access" || scope == "openid" || scope == "" || len(scope) > 128 || strings.ContainsAny(scope, " \t\r\n") || seen[scope] {
				return safeerr.New("invalid UI OIDC scope")
			}
			seen[scope] = true
		}
	default:
		return safeerr.New("UI authMode must be development or oidc")
	}
	return nil
}

// fixedURL parses a credential-free HTTP(S) URL. Callers enforce origin-only paths.
func fixedURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, safeerr.New("invalid fixed URL")
	}
	return u, nil
}

// readFile bounds mounted regular-file inputs both before and during the read.
func readFile(filename string, limit int64) ([]byte, error) {
	f, err := os.Open(filename) // #nosec G304 -- Operator-configured mounted input, never a browser-selected path; regular-file and byte bounds follow.
	if err != nil {
		return nil, safeerr.New("cannot open UI input")
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, safeerr.New("UI input must be a bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, safeerr.New("cannot read bounded UI input")
	}
	return data, nil
}

// readJSONFile decodes a mounted JSON input within the shared 64 KiB ceiling.
func readJSONFile(filename string, out any) error {
	raw, err := readFile(filename, 64<<10)
	if err != nil {
		return err
	}
	return strictJSON(raw, out)
}

// strictJSON rejects duplicate keys, trailing values and unknown control fields.
func strictJSON(raw []byte, out any) error {
	if err := jsondoc.Validate(raw); err != nil {
		return safeerr.New("invalid JSON")
	}
	return jsondoc.Decode(raw, out)
}

// decodeKey requires hex-encoded key material of the exact cryptographic size.
func decodeKey(raw string, size int) ([]byte, error) {
	key, err := hex.DecodeString(raw)
	if err != nil || len(key) != size {
		return nil, safeerr.New("UI session keys require 64-byte hash and 32-byte encryption keys in hex")
	}
	return key, nil
}
