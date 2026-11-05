// Package schemas embeds and verifies the exact upstream standards inputs used by the service.
package schemas

import (
	"bufio"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// Files contains the pinned standards sources and their checksum manifest.
//
//go:embed all:upstream checksums.txt
var Files embed.FS

// Profile identifies an immutable upstream input and its local bundle directory.
type Profile struct {
	ID         string `json:"id"`
	Project    string `json:"project"`
	Repository string `json:"repository"`
	Commit     string `json:"commit"`
	Path       string `json:"path"`
}

// Profiles lists the selected upstream contracts, not every service-owned adapter.
var Profiles = []Profile{
	{ID: "agent-plugins-1.0.0-1fc1b627", Project: "Agent Plugins", Repository: "agentplugins/agent-plugins-spec", Commit: "1fc1b6270e3cc492ec2d24ad7a34277c6d53b9c1", Path: "upstream/agent-plugins/agent-plugins-1.0.0-1fc1b627"},
	{ID: "ard-0.91-aa3e598", Project: "Agentic Resource Discovery", Repository: "ards-project/ard-spec", Commit: "aa3e598bb7752a9175897823234311216acfa864", Path: "upstream/ard/ard-0.91-aa3e598"},
	{ID: "ai-catalog-1.0-b062278", Project: "AI Catalog", Repository: "Agent-Card/ai-catalog", Commit: "b062278fd7f5a83202d33208f1d07c5b5eb7ccb3", Path: "upstream/ai-catalog/ai-catalog-1.0-b062278"},
	{ID: "a2a-agent-card-1.0.1-3303592", Project: "A2A", Repository: "a2aproject/A2A", Commit: "3303592588e388e62e0f69f701af531d2f4e3991", Path: "upstream/a2a/a2a-agent-card-1.0.1-3303592"},
	{ID: "mcp-registry-server-1.8.1-f52dc85", Project: "MCP Registry", Repository: "modelcontextprotocol/registry", Commit: "f52dc8525a441a3abf5fedc9912152d95af5aab1", Path: "upstream/mcp/mcp-registry-server-1.8.1-f52dc85"},
	{ID: "mcp-server-card-v1-experimental-526201b", Project: "MCP Server Card", Repository: "modelcontextprotocol/ext-server-card", Commit: "526201bbc80231daa40ffcdecfc9da4e54e5dc93", Path: "upstream/mcp/mcp-server-card-v1-experimental-526201b"},
	{ID: "agent-skills-69ef37e", Project: "Agent Skills", Repository: "agentskills/agentskills", Commit: "69ef37e9424c0a7ea9dd2293b559e43ec8176379", Path: "upstream/agent-skills/agent-skills-69ef37e"},
}

// Verification describes checksum integrity, not protocol or trust conformance.
type Verification struct {
	Status        string    `json:"status"`
	Profiles      []Profile `json:"profiles"`
	ArtifactCount int       `json:"artifactCount"`
	BundleDigest  string    `json:"bundleDigest"`
}

// VerifyBundle checks the embedded files against their embedded checksum manifest.
func VerifyBundle() (Verification, error) {
	manifest, err := Files.ReadFile("checksums.txt")
	if err != nil {
		return Verification{}, fmt.Errorf("read embedded checksum manifest: %w", err)
	}
	return Verify(Files, manifest)
}

// Verify requires an exact file/checksum match under upstream and computes an
// order-independent bundle digest. It does not retrieve or authenticate upstream.
func Verify(files fs.FS, manifest []byte) (Verification, error) {
	expected, err := parseChecksums(manifest)
	if err != nil {
		return Verification{}, err
	}
	actual := make(map[string]string)
	err = fs.WalkDir(files, "upstream", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		content, readErr := fs.ReadFile(files, path)
		if readErr != nil {
			return readErr
		}
		digest := sha256.Sum256(content)
		actual[path] = hex.EncodeToString(digest[:])
		return nil
	})
	if err != nil {
		return Verification{}, fmt.Errorf("walk embedded standards: %w", err)
	}
	if len(actual) != len(expected) {
		return Verification{}, fmt.Errorf("profile bundle artifact count mismatch: manifest=%d embedded=%d", len(expected), len(actual))
	}
	paths := make([]string, 0, len(expected))
	for path, digest := range expected {
		actualDigest, ok := actual[path]
		if !ok {
			return Verification{}, fmt.Errorf("profile bundle artifact is missing: %s", path)
		}
		if actualDigest != digest {
			return Verification{}, fmt.Errorf("profile bundle checksum mismatch: %s", path)
		}
		paths = append(paths, path)
	}
	for path := range actual {
		if _, ok := expected[path]; !ok {
			return Verification{}, fmt.Errorf("profile bundle contains unlisted artifact: %s", path)
		}
	}
	sort.Strings(paths)
	bundle := sha256.New()
	for _, path := range paths {
		_, _ = fmt.Fprintf(bundle, "%s  %s\n", expected[path], path)
	}
	profiles := append([]Profile(nil), Profiles...)
	return Verification{Status: "verified", Profiles: profiles, ArtifactCount: len(actual), BundleDigest: "sha256:" + hex.EncodeToString(bundle.Sum(nil))}, nil
}

// parseChecksums rejects empty, malformed or duplicate manifest entries and
// restricts all paths to the pinned upstream tree.
func parseChecksums(manifest []byte) (map[string]string, error) {
	if len(manifest) == 0 {
		return nil, errors.New("profile bundle checksum manifest is empty")
	}
	checksums := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(string(manifest)))
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Text()
		parts := strings.SplitN(line, "  ", 2)
		if len(parts) != 2 || len(parts[0]) != sha256.Size*2 || !strings.HasPrefix(parts[1], "upstream/") || strings.Contains(parts[1], "..") {
			return nil, fmt.Errorf("invalid checksum manifest line %d", lineNumber)
		}
		if _, err := hex.DecodeString(parts[0]); err != nil {
			return nil, fmt.Errorf("invalid checksum on manifest line %d: %w", lineNumber, err)
		}
		if _, exists := checksums[parts[1]]; exists {
			return nil, fmt.Errorf("duplicate checksum path on manifest line %d", lineNumber)
		}
		checksums[parts[1]] = parts[0]
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read checksum manifest: %w", err)
	}
	return checksums, nil
}
