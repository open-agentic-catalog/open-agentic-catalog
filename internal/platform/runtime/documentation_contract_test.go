package runtime

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/database"
	"github.com/open-agentic-catalog/open-agentic-catalog/schemas"
)

// Only task families belong in the tracker index. ADRs, advisories, hashes and
// curve names use separate identifier schemes, including longer numeric parts.
const backlogIDPattern = `(?:ARD|B|C|D|F|I|N|O|Q|R|S|X)-[0-9]{2,}`

var (
	markdownLinkPattern  = regexp.MustCompile(`\[[^]\n]*\]\(([^)\s]+)`)
	htmlIDPattern        = regexp.MustCompile(`\bid="([^"]+)"`)
	backlogDefinition    = regexp.MustCompile(`(?m)^- \[[ xX]\] \*\*(` + backlogIDPattern + `) -`)
	backlogReference     = regexp.MustCompile(`\b` + backlogIDPattern + `\b`)
	adrIndexLink         = regexp.MustCompile(`^\[([0-9]{4})\]\(([^)]+)\)$`)
	adrHeading           = regexp.MustCompile(`(?m)^title: "?ADR-([0-9]{4}):`)
	adrStatus            = regexp.MustCompile(`(?m)^- Status: ([A-Za-z]+)$`)
	skippedMarkdownTrees = map[string]struct{}{`.git`: {}, `.tools`: {}, `.playwright-cli`: {}, `bin`: {}, `coverage`: {}, `objects`: {}, `schemas/upstream`: {}, `samples/upstream`: {}, `specs`: {}, `output`: {}, `documentation/node_modules`: {}, `documentation/.astro`: {}, `documentation/dist`: {}, `documentation/src/content/docs/generated`: {}, `catalog-ui/node_modules`: {}, `catalog-ui/dist`: {}, `test/validation/node_modules`: {}, `test/interoperability/mcp/node_modules`: {}, `test/validation/.tools`: {}, `test/sdk/.tools`: {}, `examples/langgraph/.venv`: {}, `sdk/typescript/node_modules`: {}, `sdk/rust/target`: {}, `test/sdk/rust/consumer/target`: {}, `extensions/vscode/node_modules`: {}, `extensions/vscode/.tools`: {}, `extensions/vscode/dist`: {}, `test/vscode/.tools`: {}}
)

type indexedADR struct {
	file   string
	status string
}

func TestDocumentationContracts(t *testing.T) {
	repository := openTestRepository(t)
	markdownFiles := serviceMarkdownFiles(t, repository.FS())

	t.Run("functional surface inventory", func(t *testing.T) {
		checkFunctionalInventory(t, repository.FS())
	})

	t.Run("manual image source inventory", func(t *testing.T) {
		var entries []struct {
			Source string `json:"source"`
		}
		if err := json.Unmarshal(readTestFile(t, repository, "documentation/content-map.json"), &entries); err != nil {
			t.Fatal(err)
		}
		_, stage, ok := strings.Cut(string(readTestFile(t, repository, "catalog-ui/Containerfile")), " AS documentation\n")
		if !ok {
			t.Fatal("documentation image stage is missing")
		}
		stage, _, _ = strings.Cut(stage, "\nFROM ")
		for _, entry := range entries {
			if !documentationCopyIncludes(stage, entry.Source) {
				t.Errorf("published manual source %s is missing from the documentation image COPY inputs", entry.Source)
			}
		}
	})

	t.Run("ASCII and local links", func(t *testing.T) {
		for _, name := range markdownFiles {
			content := readTestFile(t, repository, name)
			for offset, value := range content {
				if value >= 0x80 {
					t.Errorf("%s:%d contains a non-ASCII byte", name, lineNumber(content, offset))
					break
				}
			}
			for _, match := range markdownLinkPattern.FindAllSubmatchIndex(content, -1) {
				rawTarget := string(content[match[2]:match[3]])
				checkLocalMarkdownLink(t, repository.FS(), name, lineNumber(content, match[0]), rawTarget)
			}
		}
	})

	t.Run("architecture contract references", func(t *testing.T) {
		// Functional inventory audits the source operations; docs-site-check verifies
		// every rendered OpenAPI operation instead of a second handwritten route list.
		content := string(readTestFile(t, repository, "documentation/src/content/docs/architecture/api-surfaces.md"))
		for _, reference := range []string{"api/openapi/control-v1.yaml", "reference/api-conventions.md"} {
			if !strings.Contains(content, reference) {
				t.Errorf("architecture API overview omits canonical reference %s", reference)
			}
		}
	})

	t.Run("architecture diagram sources", func(t *testing.T) {
		for _, name := range []string{
			"ANSAssessment", "BrowserSession", "CatalogMCPConnections", "CatalogOverview", "CommandPath",
			"CommandRepair", "Components", "ComposeDeployment", "ContextForgeFlows", "CoordinatedUpgrade",
			"DatabaseConnections", "Deployment", "DomainModel", "EmbeddingBoundary", "Events",
			"Federation", "FederationPeers", "FeedbackLifecycle", "Identity", "JobLifecycle",
			"MonitoringDeployment", "NativeImportRecovery", "OutboundChecks", "PluginDelivery", "Publication",
			"PublisherDNSVerification", "ReleaseVisibility", "RestoreVerification", "RevisionLifecycle", "ScopedAccess",
			"SearchContinuation", "SemanticGeneration", "SourceIngestion", "SystemContext", "TelemetryPaths",
			"TestingPyramid", "TraceCausality", "WebhookOutcomes", "WebhookReceiver", "WelcomeDeployment",
		} {
			file := "documentation/src/components/architecture/" + name + ".astro"
			content := readTestFile(t, repository, file)
			for _, value := range content {
				if value >= 0x80 {
					t.Errorf("%s contains non-ASCII source", file)
					break
				}
			}
			if !bytes.Contains(content, []byte("<svg")) || !bytes.Contains(content, []byte(`role="img"`)) {
				t.Errorf("%s must contain an accessible SVG", file)
			}
			ids := make(map[string]struct{})
			for _, match := range htmlIDPattern.FindAllSubmatch(content, -1) {
				id := string(match[1])
				if _, duplicate := ids[id]; duplicate {
					t.Errorf("%s repeats id %s", file, id)
				}
				ids[id] = struct{}{}
			}
		}
	})

	t.Run("backlog identifiers", func(t *testing.T) {
		// Task IDs remain unique when their full entries move between trackers.
		for _, obsolete := range []string{"todo.md", "done.md"} {
			if _, err := fs.Stat(repository.FS(), obsolete); err == nil {
				t.Errorf("obsolete root tracker %s must not duplicate todo/", obsolete)
			}
		}
		definitions, problems := indexLocalBacklog(repository.FS())
		for _, problem := range problems {
			t.Error(problem)
		}
		if definitions == nil {
			return // Local trackers are not distributed with the source.
		}
		for _, name := range markdownFiles {
			for _, problem := range checkBacklogReferences(name, string(readTestFile(t, repository, name)), definitions) {
				t.Error(problem)
			}
		}
	})

	t.Run("ADR index parity", func(t *testing.T) {
		index := parseADRIndex(t, string(readTestFile(t, repository, "documentation/src/content/docs/architecture/decisions/index.md")))
		files, err := fs.Glob(repository.FS(), "documentation/src/content/docs/architecture/decisions/[0-9][0-9][0-9][0-9]-*.md")
		if err != nil {
			t.Fatal(err)
		}
		if len(files) != len(index) {
			t.Errorf("ADR index has %d entries for %d record files", len(index), len(files))
		}
		for _, name := range files {
			content := string(readTestFile(t, repository, name))
			heading := adrHeading.FindStringSubmatch(content)
			status := adrStatus.FindStringSubmatch(content)
			if len(heading) != 2 || len(status) != 2 {
				t.Errorf("%s is missing its ADR heading or status", name)
				continue
			}
			entry, exists := index[heading[1]]
			if !exists {
				t.Errorf("%s is absent from documentation/src/content/docs/architecture/decisions/index.md", name)
				continue
			}
			if entry.file != path.Base(name) {
				t.Errorf("ADR-%s index target is %s, want %s", heading[1], entry.file, path.Base(name))
			}
			if entry.status != status[1] {
				t.Errorf("ADR-%s index status is %s, record status is %s", heading[1], entry.status, status[1])
			}
		}
	})

	t.Run("architecture migration inventory", func(t *testing.T) {
		files, err := fs.Glob(repository.FS(), "migrations/[0-9][0-9][0-9][0-9][0-9][0-9]_*.sql")
		if err != nil {
			t.Fatal(err)
		}
		if len(files) == 0 {
			t.Fatal("migrations contains no numbered SQL migrations")
		}
		architecture := string(readTestFile(t, repository, "documentation/src/content/docs/architecture/persistence.md"))
		versions := make([]string, 0, len(files))
		for _, name := range files {
			version, _, found := strings.Cut(path.Base(name), "_")
			if !found {
				t.Fatalf("numbered migration %s has no description separator", name)
			}
			versions = append(versions, version)
			if !strings.Contains(architecture, "### "+version+" - ") {
				t.Errorf("documentation/src/content/docs/architecture/persistence.md does not list migration %s", version)
			}
		}
		wantRange := "Migrations " + versions[0] + "-" + versions[len(versions)-1]
		if !strings.Contains(architecture, wantRange) {
			t.Errorf("documentation/src/content/docs/architecture/persistence.md does not report current range %q", wantRange)
		}
		chartGuide := string(readTestFile(t, repository, "deployments/helm/open-agentic-catalog/README.md"))
		if !strings.Contains(chartGuide, "schema runs through migration "+versions[len(versions)-1]) {
			t.Error("Helm chart guide does not report the current migration")
		}
	})

	t.Run("runtime privilege manifest inventory", func(t *testing.T) {
		version := strconv.Itoa(database.RuntimePrivilegeManifestVersion())
		roles := string(readTestFile(t, repository, "documentation/src/content/docs/operations/database-roles.mdx"))
		if !strings.Contains(roles, "Version "+version+" of the manifest") {
			t.Errorf("documentation/src/content/docs/operations/database-roles.mdx does not report current runtime privilege manifest v%s", version)
		}
		architecture := string(readTestFile(t, repository, "documentation/src/content/docs/architecture/persistence.md"))
		if !strings.Contains(architecture, "exact runtime privilege manifest v"+version) {
			t.Errorf("documentation/src/content/docs/architecture/persistence.md does not report current runtime privilege manifest v%s", version)
		}
		chartGuide := string(readTestFile(t, repository, "deployments/helm/open-agentic-catalog/README.md"))
		if !strings.Contains(chartGuide, "exact privilege manifest v"+version+".") {
			t.Errorf("Helm chart guide does not report current privilege manifest v%s", version)
		}
	})

	t.Run("profile inventory", func(t *testing.T) {
		profiles := string(readTestFile(t, repository, "schemas/PROFILES.md"))
		for _, profile := range schemas.Profiles {
			if !strings.Contains(profiles, "`"+profile.ID+"`") {
				t.Errorf("schemas/PROFILES.md does not list profile %s", profile.ID)
			}
			if !strings.Contains(profiles, "`"+profile.Commit+"`") {
				t.Errorf("schemas/PROFILES.md does not list commit %s for %s", profile.Commit, profile.ID)
			}
		}
	})

	t.Run("direct dependency inventory", func(t *testing.T) {
		checkDirectDependencyInventory(t, repository)
	})

	t.Run("workflow defaults", func(t *testing.T) {
		makefile := string(readTestFile(t, repository, "Makefile"))
		environment := string(readTestFile(t, repository, ".env.example"))
		for _, name := range []string{
			"CATALOG_CONFIG_FILE",
			"CATALOG_CONFIG_ROLE",
			"CATALOG_POSTGRES_CONFIG_FILE",
			"CATALOG_CONTAINER_CONFIG_FILE",
			"CATALOG_MIGRATION_DATABASE_DSN",
			"COMPOSE",
			"LOCAL_MONITORING",
			"LOCAL_UI",
			"LOCAL_WEBHOOKS",
			"UI_IMAGE",
			"CATALOG_UI_PORT",
			"MONITORING_RELEASE",
			"MONITORING_COMPONENT",
			"MONITORING_PORT",
			"CATALOG_MONITORING_PROMETHEUS_PORT",
			"CATALOG_MONITORING_GRAFANA_PORT",
			"CATALOG_MONITORING_RETENTION_HOURS",
			"CATALOG_MONITORING_RETENTION_MB",
			"IMAGE",
			"ARTIFACT_OUTPUT",
			"CHART_VERSION",
			"APP_VERSION",
			"COVERAGE_MIN",
			"CORE_COVERAGE_MIN",
			"CRITICAL_PACKAGE_COVERAGE_MIN",
			"POSTGRES_COVERAGE_MIN",
			"FUZZ_TIME",
			"ARD_MANIFEST_URL",
			"ARD_REGISTRY_URL",
		} {
			makeValue := requiredAssignmentValue(t, makefile, name+" ?=")
			environmentValue := requiredEnvironmentExampleValue(t, environment, name)
			if makeValue != environmentValue {
				t.Errorf(".env.example sets %s=%q, Makefile default is %q", name, environmentValue, makeValue)
			}
		}
	})

	t.Run("monitoring selection has no ambient default", func(t *testing.T) {
		makefile := string(readTestFile(t, repository, "Makefile"))
		environment := string(readTestFile(t, repository, ".env.example"))
		for _, name := range []string{"MONITORING_CONTEXT", "MONITORING_NAMESPACE", "MONITORING_VALUES"} {
			if !strings.Contains(makefile, name+" ?=\n") || !strings.Contains(environment, name+"=\n") {
				t.Errorf("%s must require explicit selection", name)
			}
		}
	})

	t.Run("runtime environment reference", func(t *testing.T) {
		reference := string(readTestFile(t, repository, "documentation/src/content/docs/reference/configuration.mdx"))
		for _, name := range config.EnvironmentVariables() {
			if !strings.Contains(reference, "`"+name+"`") {
				t.Errorf("documentation/src/content/docs/reference/configuration.mdx omits runtime environment variable %s", name)
			}
		}
	})
}

// The maintained image uses literal COPY inputs. Match a file or its copied
// parent directory; do not infer availability from the developer's checkout.
func documentationCopyIncludes(stage, source string) bool {
	for _, line := range strings.Split(stage, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] != "COPY" {
			continue
		}
		for _, input := range fields[1 : len(fields)-1] {
			if strings.HasPrefix(input, "--") {
				continue
			}
			input = path.Clean(input)
			if source == input || strings.HasPrefix(source, input+"/") {
				return true
			}
		}
	}
	return false
}

func TestDocumentationImageSourceSelection(t *testing.T) {
	const source = "examples/new-consumer/README.md"
	for _, tc := range []struct {
		stage string
		want  bool
	}{
		{"COPY examples/older-consumer/README.md /src/examples/older-consumer/README.md", false},
		{"COPY examples/new-consumer/README.md /src/examples/new-consumer/README.md", true},
		{"COPY examples /src/examples", true},
		{"COPY examples/new-consumer-other /src/examples/new-consumer-other", false},
		{"COPY --chown=1000:1000 examples/new-consumer/README.md /src/examples/new-consumer/README.md", true},
	} {
		if got := documentationCopyIncludes(tc.stage, source); got != tc.want {
			t.Errorf("source selection = %t, want %t", got, tc.want)
		}
	}
}

func TestDocumentationSourceTreeBoundaries(t *testing.T) {
	repository := fstest.MapFS{
		"documentation/README.md":                                  &fstest.MapFile{},
		"documentation/src/content/docs/guide.md":                  &fstest.MapFile{},
		"documentation/src/content/docs/generated/spec.md":         &fstest.MapFile{},
		".tools/agntcy/source/README.md":                           &fstest.MapFile{},
		".tools/dev/lib/third-party/README.md":                     &fstest.MapFile{},
		"documentation/node_modules/tool/README.md":                &fstest.MapFile{},
		"test/validation/node_modules/tool/README.md":              &fstest.MapFile{},
		"test/interoperability/mcp/node_modules/tool/README.md":    &fstest.MapFile{},
		"test/interoperability/mcp/README.md":                      &fstest.MapFile{},
		"test/validation/.tools/README.md":                         &fstest.MapFile{},
		"extensions/vscode/.tools/node/README.md":                  &fstest.MapFile{},
		"extensions/vscode/node_modules/example/README.md":         &fstest.MapFile{},
		"extensions/vscode/dist/README.md":                         &fstest.MapFile{},
		"test/vscode/.tools/host/README.md":                        &fstest.MapFile{},
		"sdk/typescript/node_modules/example/README.md":            &fstest.MapFile{},
		"sdk/rust/target/doc/README.md":                            &fstest.MapFile{},
		"test/sdk/rust/consumer/target/generated/README.md":        &fstest.MapFile{},
		"test/sdk/.tools/python/lib/site-packages/LICENSE.md":      &fstest.MapFile{},
		"examples/langgraph/.venv/lib/site-packages/LICENSE.md":    &fstest.MapFile{},
		"documentation/.astro/cache.md":                            &fstest.MapFile{},
		"documentation/dist/generated.md":                          &fstest.MapFile{},
		"todo/records/testing.md":                                  &fstest.MapFile{},
		"documentation/src/content/docs/architecture/overview.mdx": &fstest.MapFile{},
		"todo/implementation-status.md":                            &fstest.MapFile{},
		"specs/ard-spec/README.md":                                 &fstest.MapFile{},
		"output/private.md":                                        &fstest.MapFile{},
	}
	got := serviceMarkdownFiles(t, repository)
	want := []string{"documentation/README.md", "documentation/src/content/docs/architecture/overview.mdx", "documentation/src/content/docs/guide.md", "test/interoperability/mcp/README.md", "todo/implementation-status.md", "todo/records/testing.md"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("documentation source audit boundary: got %v, want %v", got, want)
	}
}

func serviceMarkdownFiles(t *testing.T, repository fs.FS) []string {
	t.Helper()
	var files []string
	err := fs.WalkDir(repository, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if _, skip := skippedMarkdownTrees[name]; skip || strings.HasPrefix(path.Base(name), "objects-") {
				return fs.SkipDir
			}
			return nil
		}
		if path.Ext(name) == ".md" || path.Ext(name) == ".mdx" {
			files = append(files, name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	return files
}

func checkLocalMarkdownLink(t *testing.T, repository fs.FS, name string, line int, rawTarget string) {
	t.Helper()
	target := strings.Trim(rawTarget, "<>")
	parsed, err := url.Parse(target)
	if err != nil {
		t.Errorf("%s:%d has invalid link %q: %v", name, line, rawTarget, err)
		return
	}
	if parsed.Scheme != "" || parsed.Host != "" || parsed.Path == "" {
		return
	}
	decoded, err := url.PathUnescape(parsed.Path)
	if err != nil {
		t.Errorf("%s:%d has invalid escaped link %q: %v", name, line, rawTarget, err)
		return
	}
	if strings.HasPrefix(decoded, "/") {
		t.Errorf("%s:%d uses absolute local link %q", name, line, rawTarget)
		return
	}
	resolved := path.Clean(path.Join(path.Dir(name), decoded))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		t.Errorf("%s:%d link %q escapes the repository", name, line, rawTarget)
		return
	}
	if _, err := fs.Stat(repository, resolved); err != nil {
		t.Errorf("%s:%d link %q resolves to missing %s", name, line, rawTarget, resolved)
	}
}

func parseADRIndex(t *testing.T, content string) map[string]indexedADR {
	t.Helper()
	entries := make(map[string]indexedADR)
	for _, line := range strings.Split(content, "\n") {
		cells := strings.Split(line, "|")
		if len(cells) < 5 {
			continue
		}
		match := adrIndexLink.FindStringSubmatch(strings.TrimSpace(cells[1]))
		if len(match) != 3 {
			continue
		}
		if _, exists := entries[match[1]]; exists {
			t.Errorf("documentation/src/content/docs/architecture/decisions/index.md indexes ADR-%s more than once", match[1])
		}
		entries[match[1]] = indexedADR{file: match[2], status: strings.TrimSpace(cells[3])}
	}
	if len(entries) == 0 {
		t.Fatal("documentation/src/content/docs/architecture/decisions/index.md has no indexed ADRs")
	}
	return entries
}

func lineNumber(content []byte, offset int) int {
	return bytes.Count(content[:offset], []byte("\n")) + 1
}

func sortedSetDifference(left, right map[string]struct{}) []string {
	values := make([]string, 0)
	for value := range left {
		if _, exists := right[value]; !exists {
			values = append(values, value)
		}
	}
	sort.Strings(values)
	return values
}

func requiredAssignmentValue(t *testing.T, content, prefix string) string {
	t.Helper()
	for _, line := range strings.Split(content, "\n") {
		if value, found := strings.CutPrefix(strings.TrimSpace(line), prefix); found {
			value = strings.TrimSpace(value)
			if value == "" {
				t.Fatalf("%s has an empty value", prefix)
			}
			return value
		}
	}
	t.Fatalf("missing assignment %s", prefix)
	return ""
}

func requiredEnvironmentExampleValue(t *testing.T, content, name string) string {
	t.Helper()
	prefix := name + "="
	for _, line := range strings.Split(content, "\n") {
		candidate := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "#"))
		if value, found := strings.CutPrefix(candidate, prefix); found {
			value = strings.TrimSpace(value)
			if value == "" {
				t.Fatalf(".env.example has an empty value for %s", name)
			}
			return value
		}
	}
	t.Fatalf(".env.example does not assign %s", name)
	return ""
}
