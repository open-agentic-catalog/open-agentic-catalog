package runtime

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

type dependencyReview struct {
	version      string
	goVersion    string
	license      string
	purpose      string
	resourceInfo string
}

func TestGoLicenseTargetContract(t *testing.T) {
	t.Parallel()

	repository := openTestRepository(t)
	makefile := string(readTestFile(t, repository, "Makefile"))
	requireTextFragments(t, makefile, []string{
		"MODULE_PATH := $(shell awk '/^module / { print $$2; exit }' go.mod)",
		"GO_LICENSES_VERSION := v1.6.0",
		"GO_LICENSE_ALLOWLIST := Apache-2.0,MIT,BSD-2-Clause,BSD-3-Clause",
		"license-check:",
		"test/packaging/go_licenses.py --go \"$(GO)\" --toolchain \"$(GO_TOOLCHAIN)\"",
		"--version \"$(GO_LICENSES_VERSION)\" --module \"$(MODULE_PATH)\" --allowed \"$(GO_LICENSE_ALLOWLIST)\"",
		"$(addprefix --source-root ,$(GO_SOURCE_PACKAGES))",
		"lint: fmt-check module-check license-check",
	})
	requireTextFragments(t, string(readTestFile(t, repository, "test/packaging/go_licenses.py")), []string{
		`RAPID = "pgregory.net/rapid"`,
		`RAPID_VERSION = "v1.3.0"`,
		`TAGS = "functional,integration,helm,minikube"`,
		`"--include_tests"`,
	})
}

func TestParseDirectGoModuleRequirements(t *testing.T) {
	t.Parallel()

	content := `module example.invalid/service

go 1.27.0

require example.invalid/inline v1.2.3

require (
	example.invalid/direct v2.0.0
	example.invalid/ignored v3.0.0 // indirect
)
`
	got := parseDirectGoModuleRequirements(t, content)
	want := map[string]string{
		"example.invalid/direct": "v2.0.0",
		"example.invalid/inline": "v1.2.3",
	}
	if len(got) != len(want) {
		t.Fatalf("parsed %d direct requirements, want %d: %#v", len(got), len(want), got)
	}
	for module, version := range want {
		if got[module] != version {
			t.Errorf("requirement %s has version %q, want %q", module, got[module], version)
		}
	}
}

func checkDirectDependencyInventory(t *testing.T, repository *os.Root) {
	t.Helper()

	requirements := parseDirectGoModuleRequirements(t, string(readTestFile(t, repository, "go.mod")))
	inventory := string(readTestFile(t, repository, "documentation/src/content/docs/reference/dependencies.md"))
	requireTextFragments(t, inventory, []string{
		"## License policy",
		"`go.mod` remains authoritative for selected versions.",
	})
	reviews := parseDependencyReviews(t, inventory)
	allowed := parseMakeLicenseAllowlist(t, string(readTestFile(t, repository, "Makefile")))

	if len(reviews) != len(requirements) {
		t.Errorf("dependency inventory has %d rows for %d direct go.mod requirements", len(reviews), len(requirements))
	}
	for module, version := range requirements {
		review, exists := reviews[module]
		if !exists {
			t.Errorf("documentation/src/content/docs/reference/dependencies.md is missing direct module %s", module)
			continue
		}
		if review.version != version {
			t.Errorf("dependency inventory records %s at %s, go.mod selects %s", module, review.version, version)
		}
		approved := approvedInventoryLicense(review.license, allowed)
		// Runtime exclusion and replacement rejection are enforced by license-check.
		rapidTestException := module == "pgregory.net/rapid" && version == "v1.3.0" && review.license == "MPL-2.0"
		if !approved && !rapidTestException {
			t.Errorf("dependency inventory records unapproved license %s for %s", review.license, module)
		}
		if review.goVersion == "" || review.purpose == "" || review.resourceInfo == "" {
			t.Errorf("dependency inventory has an incomplete review for %s", module)
		}
	}
	for module := range reviews {
		if _, exists := requirements[module]; !exists {
			t.Errorf("dependency inventory contains %s, which is not a direct go.mod requirement", module)
		}
	}
}

// Every license in a conjunctive inventory declaration must already be approved.
// This does not widen the module scanner's license policy or its Rapid exception.
func approvedInventoryLicense(value string, allowed map[string]struct{}) bool {
	for _, license := range strings.Split(value, " AND ") {
		if _, ok := allowed[license]; !ok {
			return false
		}
	}
	return true
}

func TestInventoryConjunctiveLicenses(t *testing.T) {
	allowed := map[string]struct{}{"Apache-2.0": {}, "MIT": {}}
	for _, value := range []string{"Apache-2.0", "MIT", "Apache-2.0 AND MIT"} {
		if !approvedInventoryLicense(value, allowed) {
			t.Errorf("approved licenses refused: %q", value)
		}
	}
	for _, value := range []string{"", "MIT AND GPL-3.0", "MIT AND ", "MIT OR GPL-3.0", "MIT,Apache-2.0", "MPL-2.0"} {
		if approvedInventoryLicense(value, allowed) {
			t.Errorf("unapproved or malformed licenses accepted: %q", value)
		}
	}
}

func parseDirectGoModuleRequirements(t *testing.T, content string) map[string]string {
	t.Helper()

	requirements := make(map[string]string)
	inBlock := false
	scanner := bufio.NewScanner(strings.NewReader(content))
	for line := 1; scanner.Scan(); line++ {
		trimmed := strings.TrimSpace(scanner.Text())
		if trimmed == "require (" {
			if inBlock {
				t.Fatalf("go.mod line %d starts a nested require block", line)
			}
			inBlock = true
			continue
		}
		if inBlock && trimmed == ")" {
			inBlock = false
			continue
		}
		if !inBlock {
			if !strings.HasPrefix(trimmed, "require ") {
				continue
			}
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "require "))
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		declaration, comment, _ := strings.Cut(trimmed, "//")
		if strings.Contains(comment, "indirect") {
			continue
		}
		fields := strings.Fields(declaration)
		if len(fields) != 2 {
			t.Errorf("go.mod line %d has an unrecognized direct requirement %q", line, trimmed)
			continue
		}
		if prior, exists := requirements[fields[0]]; exists {
			t.Errorf("go.mod declares direct module %s more than once at %s and %s", fields[0], prior, fields[1])
		}
		requirements[fields[0]] = fields[1]
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan go.mod: %v", err)
	}
	if inBlock {
		t.Error("go.mod has an unterminated require block")
	}
	if len(requirements) == 0 {
		t.Fatal("go.mod has no direct requirements")
	}
	return requirements
}

func parseDependencyReviews(t *testing.T, content string) map[string]dependencyReview {
	t.Helper()

	reviews := make(map[string]dependencyReview)
	for lineNumber, line := range strings.Split(content, "\n") {
		cells := strings.Split(line, "|")
		if len(cells) != 8 || !strings.HasPrefix(strings.TrimSpace(cells[1]), "`") {
			continue
		}
		module := markdownCodeCell(t, lineNumber+1, cells[1])
		review := dependencyReview{
			version:      markdownCodeCell(t, lineNumber+1, cells[2]),
			goVersion:    markdownCodeCell(t, lineNumber+1, cells[3]),
			license:      markdownCodeCell(t, lineNumber+1, cells[4]),
			purpose:      strings.TrimSpace(cells[5]),
			resourceInfo: strings.TrimSpace(cells[6]),
		}
		if _, exists := reviews[module]; exists {
			t.Errorf("documentation/src/content/docs/reference/dependencies.md lists %s more than once", module)
		}
		reviews[module] = review
	}
	if len(reviews) == 0 {
		t.Fatal("documentation/src/content/docs/reference/dependencies.md has no direct module rows")
	}
	return reviews
}

func markdownCodeCell(t *testing.T, line int, cell string) string {
	t.Helper()

	value := strings.TrimSpace(cell)
	if len(value) < 2 || value[0] != '`' || value[len(value)-1] != '`' {
		t.Errorf("documentation/src/content/docs/reference/dependencies.md line %d has non-code inventory cell %q", line, value)
		return ""
	}
	return value[1 : len(value)-1]
}

func parseMakeLicenseAllowlist(t *testing.T, content string) map[string]struct{} {
	t.Helper()

	const prefix = "GO_LICENSE_ALLOWLIST := "
	var value string
	for _, line := range strings.Split(content, "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		if value != "" {
			t.Fatal("Makefile defines GO_LICENSE_ALLOWLIST more than once")
		}
		value = strings.TrimSpace(strings.TrimPrefix(line, prefix))
	}
	if value == "" {
		t.Fatal("Makefile does not define GO_LICENSE_ALLOWLIST")
	}
	allowed := make(map[string]struct{})
	for _, license := range strings.Split(value, ",") {
		license = strings.TrimSpace(license)
		if license == "" {
			t.Fatal("Makefile GO_LICENSE_ALLOWLIST contains an empty identifier")
		}
		if _, exists := allowed[license]; exists {
			t.Errorf("Makefile GO_LICENSE_ALLOWLIST repeats %s", license)
		}
		allowed[license] = struct{}{}
	}
	return allowed
}
