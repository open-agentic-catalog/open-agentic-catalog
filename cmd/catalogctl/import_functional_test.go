//go:build functional

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/samples"
)

// Keep process execution outside the unit selection. Exercise the real
// dispatcher, safe stderr, exit status and separation from runtime credentials.
func TestImportPreviewCLIProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	binary := filepath.Join(canonicalTempDir(t), "catalogctl")
	build := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-o", binary, ".") // #nosec G204,G702 -- fixed build and invocation-owned executable.
	if raw, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", raw, err)
	}
	run := func(t *testing.T, args []string, success, hasReport bool) importPreview {
		t.Helper()
		command := exec.CommandContext(ctx, binary, args...) // #nosec G204,G702 -- owned binary and synthetic local arguments.
		command.Env = append(os.Environ(), "CATALOG_CONFIG_FILE=/missing/PRIVATE_SENTINEL", "CATALOG_DATABASE_DSN=postgres://user:PRIVATE_SENTINEL@invalid", "CATALOG_REMOTE_TOKEN_FILE=/missing/PRIVATE_SENTINEL", "CATALOG_REMOTE_BASE_URL=https://invalid.example.com")
		var output, diagnostic bytes.Buffer
		command.Stdout, command.Stderr = &output, &diagnostic
		err := command.Run()
		if (err == nil) != success {
			t.Fatalf("exit %v stdout %s stderr %s", err, output.Bytes(), diagnostic.Bytes())
		}
		if !success {
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 {
				t.Fatalf("expected command failure status 1, got %v", err)
			}
		}
		var batch importBatchPreview
		var report importPreview
		if hasReport {
			if json.Unmarshal(output.Bytes(), &batch) != nil || len(batch.Resources) != 1 {
				t.Fatal("missing batch preview")
			}
			report = batch.Resources[0]
			if report.LocallyValid != success || len(report.Commands) != 4 || report.ServerLimitsChecked || len(report.PendingServerChecks) == 0 {
				t.Fatal("missing or incorrect preview report", output.String())
			}
		} else if output.Len() != 0 {
			t.Fatal("unexpected stdout", output.String())
		}
		if strings.Contains(output.String()+diagnostic.String(), "PRIVATE_SENTINEL") || success && diagnostic.Len() != 0 || !success && diagnostic.Len() == 0 {
			t.Fatal("unsafe or incorrectly separated process diagnostics")
		}
		return report
	}
	dir, _ := importTestInput(t)
	filename := writeImportTestFile(t, dir, "import.yaml", []byte(importTestYAML), 0600)
	for _, test := range []struct {
		name    string
		args    []string
		native  string
		success bool
		report  bool
	}{
		{"valid local input", []string{"import", "preview", "--file", filename}, `{"name":"Example","framework":"private","large":9007199254740993}`, true, true},
		{"invalid native findings", []string{"import", "preview", "--file", filename}, `{"name":"PRIVATE_SENTINEL"}`, false, true},
		{"ambiguous native input", []string{"import", "preview", "--file", filename}, `{"name":"PRIVATE_SENTINEL","name":"shadowed"}`, false, false},
		{"missing file", []string{"import", "preview", "--file", dir + "/PRIVATE_SENTINEL"}, `{}`, false, false},
		{"apply requires receipt", []string{"import", "apply", "--file", filename}, `{}`, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			writeImportTestFile(t, dir, "agent.json", []byte(test.native), 0600)
			run(t, test.args, test.success, test.report)
		})
	}
	for _, test := range []struct {
		kind     catalog.Kind
		setup    func(*testing.T, string, *importResource)
		profiles []string
	}{
		{catalog.KindAgent, func(t *testing.T, dir string, r *importResource) {
			t.Helper()
			r.Version = "1.0.0"
			r.Representations = []importRepresentation{{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaA2AAgentCard, DataFile: "agent.json"}}
			copyImportSample(t, dir, "order-agent.json", "agent.json")
		}, []string{adapter.ProfileA2AAgentCard}},
		{catalog.KindMCPServer, func(t *testing.T, dir string, r *importResource) {
			t.Helper()
			r.Version = "2.0.0"
			r.Representations = []importRepresentation{
				{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaMCPServerCard, DataFile: "card.json"},
				{Role: catalog.RepresentationDistribution, MediaType: adapter.MediaMCPRegistry, DataFile: "registry.json"},
			}
			copyImportSample(t, dir, "everything.json", "card.json")
			copyImportSample(t, dir, "everything.registry.json", "registry.json")
		}, []string{adapter.ProfileMCPServerCard, adapter.ProfileMCPRegistry}},
		{catalog.KindSkill, func(t *testing.T, dir string, r *importResource) {
			t.Helper()
			r.Representations = []importRepresentation{{Role: catalog.RepresentationPrimary, MediaType: catalog.SkillPackageMediaZIP, PackageDirectory: "assets/safe-skill", Handoff: &catalog.SkillHandoff{OCI: &catalog.OCIHandoffReference{Reference: "registry.example.com/safe-skill@sha256:" + strings.Repeat("a", 64)}}}}
			writeImportTestFile(t, dir, "assets/safe-skill/SKILL.md", []byte("---\nname: safe-skill\ndescription: Helps with local examples\n---\nInstructions\n"), 0600)
			writeImportTestFile(t, dir, "assets/safe-skill/scripts/run.sh", []byte("#!/bin/sh\nexit 1\n"), 0700)
		}, []string{adapter.ProfileAgentSkills}},
		{catalog.KindPlugin, func(_ *testing.T, _ string, r *importResource) {
			r.Representations = []importRepresentation{{Role: catalog.RepresentationPrimary, MediaType: catalog.PluginMediaZIP, URL: "https://archives.example.com/plugin.zip"}}
		}, []string{catalog.PluginProfile}},
	} {
		t.Run(string(test.kind), func(t *testing.T) {
			dir, manifest := importTestInput(t)
			manifest.Resources[0].Kind = test.kind
			test.setup(t, dir, &manifest.Resources[0])
			raw, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			filename := writeImportTestFile(t, dir, "import.yaml", raw, 0600)
			report := run(t, []string{"import", "preview", "--file", filename}, true, true)
			if report.Kind != test.kind || len(report.Representations) != len(test.profiles) {
				t.Fatal("resource kind or representation collection changed")
			}
			for index, profile := range test.profiles {
				evidence := report.Representations[index]
				want := "checked"
				if test.kind == catalog.KindPlugin {
					want = "pending-server-retrieval"
				}
				if evidence.ProfileID != profile || evidence.LocalValidation != want {
					t.Fatalf("incorrect native evidence: %#v", evidence)
				}
			}
			if test.kind == catalog.KindSkill && (report.Representations[0].PackageFiles != 2 || report.Representations[0].ExecutableFiles != 1) {
				t.Fatal("compiled preview lost complete Skill inventory or executable evidence")
			}
			if test.kind == catalog.KindMCPServer && (report.Representations[0].Role != catalog.RepresentationPrimary || report.Representations[1].Role != catalog.RepresentationDistribution) {
				t.Fatal("compiled preview changed MCP document roles")
			}
			if test.kind == catalog.KindPlugin && len(report.Assets) != 0 {
				t.Fatal("compiled URL preview unexpectedly prepared local assets")
			}
		})
	}
}

func copyImportSample(t *testing.T, directory, sample, name string) {
	t.Helper()
	raw, err := samples.Files.ReadFile("cards/" + sample)
	if err != nil {
		t.Fatal(err)
	}
	writeImportTestFile(t, directory, name, raw, 0600)
}
