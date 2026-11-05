package runtime

import (
	"encoding/json"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

func TestFunctionalInventoryRejectsDriftAndFalseCompletion(t *testing.T) {
	repository := openTestRepository(t)
	raw := readTestFile(t, repository, "test/functional/coverage.json")
	expected, err := discoverFunctionalSurfaces(repository.FS())
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*functionalInventory){
		"missing surface": func(i *functionalInventory) { i.Scenarios[0].Surfaces = i.Scenarios[0].Surfaces[1:] },
		"duplicate surface": func(i *functionalInventory) {
			i.Scenarios[0].Surfaces = append(i.Scenarios[0].Surfaces, i.Scenarios[0].Surfaces[0])
		},
		"obsolete operation":    func(i *functionalInventory) { i.Scenarios[0].Surfaces[0] = "http:removedOperation" },
		"duplicate scenario":    func(i *functionalInventory) { i.Scenarios[1].ID = i.Scenarios[0].ID },
		"invalid scenario name": func(i *functionalInventory) { i.Scenarios[0].ID = "../scenario" },
		"invalid disposition":   func(i *functionalInventory) { i.Scenarios[0].Disposition = "probably-covered" },
		"invalid status":        func(i *functionalInventory) { i.Scenarios[0].Status = "maybe" },
		"unconditional skip":    func(i *functionalInventory) { i.Scenarios[0].Status = "not-applicable" },
		"lower-level gap": func(i *functionalInventory) {
			i.Scenarios[0].Disposition = "lower-level-only"
			i.Scenarios[0].Status = "partial"
			i.Scenarios[0].Gaps = []string{"Unproven composition boundary"}
		},
		"false completion": func(i *functionalInventory) {
			i.Scenarios[0].Status = "covered"
			i.Scenarios[0].Gaps = []string{"Unproven composition boundary"}
		},
		"hidden gap": func(i *functionalInventory) {
			i.Scenarios[0].Status = "partial"
			i.Scenarios[0].Gaps = nil
		},
		"empty gap":          func(i *functionalInventory) { i.Scenarios[0].Gaps = []string{" "} },
		"missing reason":     func(i *functionalInventory) { i.Scenarios[0].Reason = "" },
		"missing success":    func(i *functionalInventory) { i.Scenarios[0].Success = "" },
		"missing rejection":  func(i *functionalInventory) { i.Scenarios[0].Rejection = "" },
		"missing recovery":   func(i *functionalInventory) { i.Scenarios[0].Recovery = "" },
		"empty surfaces":     func(i *functionalInventory) { i.Scenarios[0].Surfaces = nil },
		"missing evidence":   func(i *functionalInventory) { i.Scenarios[0].Evidence = nil },
		"missing file":       func(i *functionalInventory) { i.Scenarios[0].Evidence[0].Path = "absent.go" },
		"outside repository": func(i *functionalInventory) { i.Scenarios[0].Evidence[0].Path = "../private.go" },
		"directory evidence": func(i *functionalInventory) { i.Scenarios[0].Evidence[0].Path = "internal" },
		"stale function": func(i *functionalInventory) {
			i.Scenarios[0].Evidence[0] = functionalEvidence{Path: "internal/platform/httpserver/self_test.go", Symbol: "TestRemoved"}
		},
		"non-Go function": func(i *functionalInventory) {
			i.Scenarios[0].Evidence[0] = functionalEvidence{Path: "Makefile", Symbol: "TestSelfAccessWithoutProductRoles"}
		},
		"unnamed lower-level test": func(i *functionalInventory) {
			i.Scenarios[0].Disposition = "lower-level-only"
			i.Scenarios[0].Status = "covered"
			i.Scenarios[0].Gaps = nil
			i.Scenarios[0].Evidence = []functionalEvidence{{Path: "Makefile"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			inventory, err := decodeFunctionalInventory(raw)
			if err != nil {
				t.Fatal(err)
			}
			change(&inventory)
			err = validateFunctionalInventory(inventory, expected, repository.FS())
			if err == nil {
				t.Fatal("invalid inventory was accepted")
			}
			if name == "stale function" && !strings.Contains(err.Error(), "TestRemoved is absent") || name == "non-Go function" && !strings.Contains(err.Error(), "symbols require Go source") {
				t.Fatalf("evidence mutation failed for an unrelated reason: %v", err)
			}
		})
	}
	for _, surface := range []string{"http:newOperation", "worker:GET /new-status", "catalogd:new-role", "catalogctl:repair new-command"} {
		t.Run(surface, func(t *testing.T) {
			inventory, err := decodeFunctionalInventory(raw)
			if err != nil {
				t.Fatal(err)
			}
			expected[surface] = struct{}{}
			defer delete(expected, surface)
			if err := validateFunctionalInventory(inventory, expected, repository.FS()); err == nil || !strings.Contains(err.Error(), surface) {
				t.Fatalf("new surface was not identified: %v", err)
			}
		})
	}
}

func TestFunctionalInventoryDecodingIsClosed(t *testing.T) {
	for _, raw := range []string{
		`{`, `{} null`, `{"formatVersion":1,"formatVersion":1,"scenarios":[]}`,
		`{"formatVersion":1,"scenarios":[],"unknown":true}`,
		`{"formatVersion":2,"scenarios":[]}`, `{"formatVersion":1,"scenarios":[]}`,
		`{"formatVersion":1,"scenarios":[{"surprise":true}]}`,
		strings.Repeat(" ", 1<<20+1),
	} {
		if _, err := decodeFunctionalInventory([]byte(raw)); err == nil {
			t.Fatal("invalid inventory JSON was accepted")
		}
	}
}

func TestFunctionalInventoryCommandDiscovery(t *testing.T) {
	repository := fstest.MapFS{
		"cmd/catalogctl/main.go": {Data: []byte(`package main
const usage = "usage: catalogctl <repair|version> [arguments]"
func repair() { _ = "usage: catalogctl repair <plan|apply> --tenant <uuid>" }
func version() { _ = "usage: catalogctl version" }
// usage: catalogctl comment-only
`)},
		"cmd/catalogctl/helper_test.go": {Data: []byte(`package main; const ignored = "usage: catalogctl fake"`)},
	}
	commands, err := discoverInventoryCommands(repository, "catalogctl")
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 4 {
		t.Fatalf("unexpected commands: %v", commands)
	}
	for _, name := range []string{"help", "version", "repair plan", "repair apply"} {
		if _, exists := commands[name]; !exists {
			t.Fatalf("missing command %s", name)
		}
	}
	repository["cmd/catalogctl/export.go"] = &fstest.MapFile{Data: []byte(`package main
const usage = "usage: catalogctl export --release-id UUID [flags]" + "usage: catalogctl export <preview|batch> [flags]"
`)}
	commands, err = discoverInventoryCommands(repository, "catalogctl")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"export", "export preview", "export batch"} {
		if _, exists := commands[name]; !exists {
			t.Fatalf("lost runnable parent or child %s", name)
		}
	}
	if _, exists := commands["repair"]; exists {
		t.Fatal("aggregate summary became a runnable leaf")
	}
	repository["cmd/catalogctl/extra.go"] = &fstest.MapFile{Data: []byte(`package main; func extra(arguments []string) { if arguments[0] == "undocumented" {} }`)}
	if _, err := discoverInventoryCommands(repository, "catalogctl"); err == nil || !strings.Contains(err.Error(), "undocumented") {
		t.Fatalf("undocumented dispatch accepted: %v", err)
	}
	for _, grammar := range []string{"", "[flags]", "<missing-end", "<a||b>"} {
		if _, err := expandInventoryUsage(grammar); err == nil {
			t.Fatalf("invalid command grammar %q was accepted", grammar)
		}
	}
}

func TestFunctionalInventoryReflectsSourceSurfaces(t *testing.T) {
	repository := openTestRepository(t)
	fixture := make(fstest.MapFS)
	for _, pattern := range []string{"api/openapi/*.yaml", "internal/platform/operations/server.go", "cmd/catalogctl/*.go", "cmd/catalogd/*.go"} {
		files, err := fs.Glob(repository.FS(), pattern)
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			fixture[file] = &fstest.MapFile{Data: readTestFile(t, repository, file)}
		}
	}
	base, err := discoverFunctionalSurfaces(fixture)
	if err != nil {
		t.Fatal(err)
	}
	fixture["api/openapi/control-v1.yaml"].Data = []byte(strings.Replace(string(fixture["api/openapi/control-v1.yaml"].Data), "operationId: getLiveness", "operationId: newLiveness", 1))
	fixture["internal/platform/operations/server.go"].Data = []byte(strings.Replace(string(fixture["internal/platform/operations/server.go"].Data), `"GET /startupz"`, `"GET /new-startup"`, 1))
	fixture["cmd/catalogctl/new.go"] = &fstest.MapFile{Data: []byte(`package main; const usage = "usage: catalogctl repair new-command [flags]"`)}
	fixture["cmd/catalogd/new.go"] = &fstest.MapFile{Data: []byte(`package main; const usage = "usage: catalogd new-role [flags]"`)}
	fixture["internal/platform/operations/new.go"] = &fstest.MapFile{Data: []byte(`package operations; func extra() { router.HandleFunc("GET /extra", handler) }`)}
	changed, err := discoverFunctionalSurfaces(fixture)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"http:newLiveness", "worker:GET /new-startup", "worker:GET /extra", "catalogctl:repair new-command", "catalogd:new-role"} {
		if _, found := changed[key]; !found {
			t.Fatalf("source change was not discovered: %s", key)
		}
		if _, found := base[key]; found {
			t.Fatalf("fixture did not add a new surface: %s", key)
		}
	}
	fixture["internal/platform/operations/new.go"].Data = []byte(`package operations; func extra() { router.HandleFunc(dynamicPattern, handler) }`)
	if _, err := discoverFunctionalSurfaces(fixture); err == nil || !strings.Contains(err.Error(), "literal pattern") {
		t.Fatalf("dynamic worker registration bypassed the audit: %v", err)
	}
	// Ensure a normal JSON round trip cannot lose required declarations.
	raw := readTestFile(t, repository, "test/functional/coverage.json")
	inventory, err := decodeFunctionalInventory(raw)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeFunctionalInventory(encoded); err != nil {
		t.Fatal(err)
	}
}
