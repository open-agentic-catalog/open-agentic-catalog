package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

const maxResources = 32
const maxPages = 8
const captureLimit = 1 << 20
const reportLimit = 4 << 20
const protocolVersion = "2025-11-25"

type mapping struct {
	ResourceID  string `json:"resourceId"`
	NamespaceID string `json:"namespaceId"`
	Endpoint    string `json:"endpoint"`
	TokenFile   string `json:"tokenFile"`
	CAFile      string `json:"caFile,omitempty"`
}
type configuration struct {
	Resources []mapping `json:"resources"`
}

func sha(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

func privateFile(name string, limit int64) ([]byte, error) {
	// #nosec G304 G703 -- An operator selects each local input. No-follow, type,
	// permission and byte checks apply before its content is used.
	f, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("private_input_unavailable")
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > limit {
		return nil, errors.New("private_input_unavailable")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || len(raw) == 0 || int64(len(raw)) > limit {
		return nil, errors.New("private_input_unavailable")
	}
	return raw, nil
}

func readToken(ctx context.Context, name string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	raw, err := privateFile(name, 16384)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", errors.New("invalid_credential")
	}
	for _, c := range []byte(token) {
		if c <= 32 || c >= 127 {
			return "", errors.New("invalid_credential")
		}
	}
	return token, ctx.Err()
}

func validateEndpoint(raw string) error {
	u, err := url.Parse(raw)
	// The offline runner accepts at most 1024 characters in its source label.
	if err != nil || u == nil || len(raw) > 1024 || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") || u.Opaque != "" || u.RawPath != "" || u.Path == "" || path.Clean(u.Path) != u.Path || strings.HasSuffix(u.Host, ":") {
		return errors.New("invalid_mcp_endpoint")
	}
	return nil
}

func loadConfiguration(name string) (configuration, string, error) {
	raw, err := privateFile(name, 64<<10)
	if err != nil || uniqueJSON(raw) != nil {
		return configuration{}, "", errors.New("invalid_mapping")
	}
	if _, err = fields(raw, "resources"); err != nil {
		return configuration{}, "", errors.New("invalid_mapping")
	}
	var c configuration
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || len(c.Resources) < 1 || len(c.Resources) > maxResources {
		return c, "", errors.New("invalid_mapping")
	}
	var top struct {
		Resources []json.RawMessage `json:"resources"`
	}
	_ = json.Unmarshal(raw, &top)
	seen, spaces := map[string]bool{}, map[string]bool{}
	for i, m := range c.Resources {
		if _, err = fields(top.Resources[i], "resourceId", "namespaceId", "endpoint", "tokenFile", "caFile"); err != nil {
			return c, "", errors.New("invalid_mapping")
		}
		if !uuidPattern.MatchString(m.ResourceID) || !uuidPattern.MatchString(m.NamespaceID) || seen[m.ResourceID] || !filepath.IsAbs(m.TokenFile) || m.CAFile != "" && !filepath.IsAbs(m.CAFile) {
			return c, "", errors.New("invalid_mapping")
		}
		if err = validateEndpoint(m.Endpoint); err != nil {
			return c, "", err
		}
		seen[m.ResourceID] = true
		spaces[m.NamespaceID] = true
	}
	if len(spaces) > 8 {
		return c, "", errors.New("mapping_namespace_limit")
	}
	sort.Slice(c.Resources, func(i, j int) bool { return c.Resources[i].ResourceID < c.Resources[j].ResourceID })
	canonical, err := json.Marshal(c)
	if err != nil {
		return c, "", errors.New("invalid_mapping")
	}
	return c, sha(canonical), nil
}

func roots(name string) (*x509.CertPool, error) {
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	if name == "" {
		return pool, nil
	}
	raw, err := privateFile(name, 256<<10)
	if err != nil {
		return nil, errors.New("invalid_ca_file")
	}
	if !pool.AppendCertsFromPEM(raw) {
		return nil, errors.New("invalid_ca_file")
	}
	return pool, nil
}

type failure struct {
	code      string
	transient bool
}

func (e *failure) Error() string               { return e.code }
func failed(code string, transient bool) error { return &failure{code: code, transient: transient} }
func errorCode(err error) string {
	var f *failure
	if errors.As(err, &f) {
		return f.code
	}
	return "receiver_work_failed"
}
