//go:build functional

// SDK and editor consumers share reviewed source inputs, not runtime packages.
package clientcompat_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// reviewedCase keeps source-derived expectations independent of API responses.
type reviewedCase struct {
	Name           string            `json:"name"`
	Kind           catalog.Kind      `json:"kind"`
	Identifier     string            `json:"identifier"`
	ReleaseID      string            `json:"releaseId"`
	Representation map[string]any    `json:"representation"`
	Data           any               `json:"data,omitempty"`
	Text           string            `json:"text,omitempty"`
	Description    string            `json:"description,omitempty"`
	FileCount      int               `json:"fileCount,omitempty"`
	Documents      map[string]string `json:"documents,omitempty"`
}

type reviewedCorpus struct {
	Origin string         `json:"origin"`
	Cases  []reviewedCase `json:"cases"`
}

// reviewedBytes reads only inert files copied into the invocation-owned module.
func reviewedBytes(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata/upstream", filepath.FromSlash(name)))
	must(t, err)
	return b
}

// reviewedZIP preserves complete package bytes and modes without extraction.
func reviewedZIP(t *testing.T, directory, prefix string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	w := zip.NewWriter(&buffer)
	root := filepath.Join("testdata/upstream", filepath.FromSlash(directory))
	must(t, filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		h := &zip.FileHeader{Name: prefix + filepath.ToSlash(relative), Method: zip.Store}
		h.SetMode(info.Mode())
		member, err := w.CreateHeader(h)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(name)
		if err == nil {
			_, err = member.Write(b)
		}
		return err
	}))
	must(t, w.Close())
	return buffer.Bytes()
}

// reviewedArchive accepts just the owned Plugin archive through an injected port.
type reviewedArchive struct {
	body []byte
	t    *testing.T
	now  time.Time
}

const reviewedPluginURL = "https://packages.example.com/reviewed-context7.zip"

// PolicyDigest binds acquisition to this fixed test-only archive policy.
func (reviewedArchive) PolicyDigest() string { return "sha256:" + strings.Repeat("b", 64) }

// FetchExternalRepresentation supplies owned bytes and refuses every other URL.
func (a reviewedArchive) FetchExternalRepresentation(_ context.Context, request catalog.ExternalRepresentationFetchRequest) (catalog.ExternalRepresentationFetchResult, error) {
	if request.URL != reviewedPluginURL {
		a.t.Errorf("unexpected resource acquisition: %s", request.URL)
		return catalog.ExternalRepresentationFetchResult{}, fmt.Errorf("unowned corpus destination")
	}
	return catalog.ExternalRepresentationFetchResult{RequestedURL: request.URL, FinalURL: request.URL, Status: 200, MediaType: catalog.PluginMediaZIP, Body: bytes.Clone(a.body), ContentDigest: fmt.Sprintf("sha256:%x", sha256.Sum256(a.body)), PolicyID: "reviewed-corpus", PolicyRevision: 1, PolicyDigest: a.PolicyDigest(), RetrievedAt: a.now}, nil
}

// newReviewedCorpus governs all six inputs in a separate disposable Catalog.
// It supplies expected fields from source before admission, never from readback.
func newReviewedCorpus(t *testing.T) (*fixture, reviewedCorpus) {
	t.Helper()
	archive := reviewedArchive{body: reviewedZIP(t, "context7/plugin", ""), t: t}
	f := newFixtureWithFetcher(t, &archive)
	archive.now = f.now
	actor := f.actor("admin")
	ns, err := f.service.CreateNamespace(actor, catalog.CreateNamespaceInput{Slug: "reviewed-upstream", DisplayName: "Reviewed upstream corpus"})
	must(t, err)
	pub, err := f.service.CreatePublisher(actor, catalog.CreatePublisherInput{DisplayName: "Corpus fixture", Domains: []string{"catalog.example.com"}})
	must(t, err)
	_, err = f.service.ApprovePublisherPublicationDomain(actor, pub.ID, "catalog.example.com", "Owned metadata fixture", pub.Revision)
	must(t, err)
	handoff := &catalog.SkillHandoff{Git: &catalog.GitHandoffReference{Repository: "https://github.com/openai/skills", Commit: "49f948faa9258a0c61caceaf225e179651397431", Directory: "skills/.curated/jupyter-notebook"}}
	inputs := []struct {
		name, version, profile string
		kind                   catalog.Kind
		input                  catalog.RepresentationInput
	}{
		{"researcher", "1.0.0", adapter.ProfileA2AAgentCard, catalog.KindAgent, catalog.RepresentationInput{MediaType: adapter.MediaA2AAgentCard, Data: reviewedBytes(t, "a2a-dotnet/researcher-card.json")}},
		{"jupyter-notebook", "1.0.0", adapter.ProfileAgentSkills, catalog.KindSkill, catalog.RepresentationInput{MediaType: adapter.MediaSkillZIP, PackageData: reviewedZIP(t, "openai/jupyter-notebook", "jupyter-notebook/"), Handoff: handoff}},
		{"hf-cli", "1.0.0", adapter.ProfileAgentSkills, catalog.KindSkill, catalog.RepresentationInput{MediaType: adapter.MediaSkillMarkdownARD, Content: string(reviewedBytes(t, "huggingface/hf-cli/SKILL.md"))}},
		{"playwright", "0.0.85", adapter.ProfileMCPRegistry, catalog.KindMCPServer, catalog.RepresentationInput{MediaType: adapter.MediaMCPRegistry, Data: reviewedBytes(t, "playwright/server.json")}},
		{"context7-registry", "4.0.3", adapter.ProfileMCPRegistry, catalog.KindMCPServer, catalog.RepresentationInput{MediaType: adapter.MediaMCPRegistry, Data: reviewedBytes(t, "context7/server.json")}},
		{"context7-plugin", "1.0.2", catalog.PluginProfile, catalog.KindPlugin, catalog.RepresentationInput{MediaType: catalog.PluginMediaZIP, URL: reviewedPluginURL}},
	}
	result := reviewedCorpus{Origin: f.server.URL}
	for _, tc := range inputs {
		expected := reviewedCase{Name: tc.name, Kind: tc.kind, Identifier: "urn:air:catalog.example.com:reviewed:" + tc.name, Representation: map[string]any{"role": "primary", "mediaType": tc.input.MediaType, "profileId": tc.profile, "validationState": "valid"}}
		source := tc.input.PackageData
		if tc.input.Data != nil {
			must(t, json.Unmarshal(tc.input.Data, &expected.Data))
			source, err = json.Marshal(expected.Data)
			must(t, err)
		}
		if tc.input.Content != "" {
			expected.Text = tc.input.Content
			source = []byte(expected.Text)
			line := strings.Split(expected.Text, "\n")[2]
			expected.Description, err = strconv.Unquote(strings.TrimPrefix(line, "description: "))
			must(t, err)
		}
		if tc.kind == catalog.KindPlugin {
			source = archive.body
			expected.FileCount = 5
			expected.Documents = map[string]string{}
			for _, path := range []string{"plugin.json", "mcp.json", "skills/context7-mcp/SKILL.md"} {
				expected.Documents[path] = string(reviewedBytes(t, "context7/plugin/"+path))
			}
		}
		if tc.name == "jupyter-notebook" {
			expected.FileCount = 12
		}
		expected.Representation["contentDigest"] = fmt.Sprintf("sha256:%x", sha256.Sum256(source))
		expected.Representation["size"] = float64(len(source))
		resource, createErr := f.service.CreateResource(actor, catalog.CreateResourceInput{NamespaceID: ns.ID, Kind: tc.kind, CanonicalIdentifier: expected.Identifier})
		must(t, createErr)
		input := tc.input
		input.Role = catalog.RepresentationPrimary
		_, revision, evidence, versionErr := f.service.CreateVersion(actor, resource.ID, catalog.CreateVersionInput{Version: tc.version, Representations: []catalog.RepresentationInput{input}})
		must(t, versionErr)
		if evidence.State != catalog.EvidenceComplete {
			t.Fatalf("reviewed %s failed admission: %+v", tc.name, evidence.Findings)
		}
		listing, listingErr := f.service.CreateListing(actor, catalog.CreateListingInput{NamespaceID: ns.ID, ResourceID: resource.ID})
		must(t, listingErr)
		listing, _, err = f.service.CreateListingRevision(actor, listing.ID, catalog.CreateListingRevisionInput{VersionRevisionID: revision.ID, DisplayName: "Reviewed " + tc.name, Description: "Inert upstream metadata", PublisherID: pub.ID, Visibility: catalog.VisibilityTenant, Tags: []string{"reviewed-upstream"}, RepresentativeQueries: []string{"find reviewed metadata", "inspect upstream definition"}}, listing.Revision)
		must(t, err)
		submission, submitErr := f.service.Submit(actor, listing.ID)
		must(t, submitErr)
		_, _, err = f.service.DecideSubmission(f.actor("reviewer"), submission.ID, "approve", "Metadata only", submission.Revision)
		must(t, err)
		_, release, _, publishErr := f.service.Publish(actor, listing.ID, catalog.PublishInput{SubmissionID: submission.ID}, listing.Revision)
		must(t, publishErr)
		expected.ReleaseID = release.ID
		result.Cases = append(result.Cases, expected)
	}
	return f, result
}

// reviewedManifest passes public source expectations by a private local file.
func reviewedManifest(t *testing.T) string {
	t.Helper()
	_, corpus := newReviewedCorpus(t)
	b, err := json.Marshal(corpus)
	must(t, err)
	name := filepath.Join(t.TempDir(), "reviewed-corpus.json")
	must(t, os.WriteFile(name, b, 0600))
	return name
}

// checkReviewedRecord compares decoded SDK projections with independent inputs.
func checkReviewedRecord(t *testing.T, expected reviewedCase, record map[string]any) {
	t.Helper()
	resource := record["resource"].(map[string]any)
	if resource["kind"] != string(expected.Kind) || resource["canonicalIdentifier"] != expected.Identifier || record["release"].(map[string]any)["id"] != expected.ReleaseID {
		t.Fatalf("reviewed resource identity changed: %s", expected.Name)
	}
	reps := record["versionRevision"].(map[string]any)["representations"].([]any)
	if len(reps) != 1 {
		t.Fatal("reviewed representation count changed")
	}
	rep := reps[0].(map[string]any)
	for key, value := range expected.Representation {
		if !reflect.DeepEqual(rep[key], value) {
			t.Fatalf("%s %s changed: got %#v, want %#v", expected.Name, key, rep[key], value)
		}
	}
	if expected.Data != nil && !reflect.DeepEqual(rep["data"], expected.Data) {
		t.Fatalf("reviewed native JSON changed: %s", expected.Name)
	}
	if expected.Description != "" && rep["normalizedFacts"].(map[string]any)["description"] != expected.Description {
		t.Fatal("long Skill description changed")
	}
	if expected.FileCount > 0 {
		key := "package"
		if expected.Kind == catalog.KindPlugin {
			key = "plugin"
		}
		if len(rep[key].(map[string]any)["files"].([]any)) != expected.FileCount {
			t.Fatal("reviewed package inventory changed")
		}
	}
	if expected.Documents != nil {
		got := map[string]string{}
		documents := rep["plugin"].(map[string]any)["documents"].([]any)
		if len(documents) != len(expected.Documents) {
			t.Fatal("retained Plugin document count changed")
		}
		for _, item := range documents {
			doc := item.(map[string]any)
			got[doc["path"].(string)] = doc["content"].(string)
		}
		if !reflect.DeepEqual(got, expected.Documents) {
			t.Fatal("retained Plugin documents changed")
		}
	}
}
