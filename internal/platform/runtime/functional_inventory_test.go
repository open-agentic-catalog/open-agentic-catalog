package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

// This inventory classifies evidence, not executable scenarios or test results.
// Keep it test-only: no product code or runtime configuration consumes it.
type functionalInventory struct {
	FormatVersion int                  `json:"formatVersion"`
	Scenarios     []functionalScenario `json:"scenarios"`
}

type functionalScenario struct {
	ID          string               `json:"id"`
	Disposition string               `json:"disposition"`
	Status      string               `json:"status"`
	Surfaces    []string             `json:"surfaces"`
	Evidence    []functionalEvidence `json:"evidence"`
	Reason      string               `json:"reason"`
	Success     string               `json:"success"`
	Rejection   string               `json:"rejection"`
	Recovery    string               `json:"recovery"`
	Gaps        []string             `json:"gaps"`
}

type functionalEvidence struct {
	Path   string `json:"path"`
	Symbol string `json:"symbol,omitempty"`
}

func checkFunctionalInventory(t *testing.T, repository fs.FS) {
	t.Helper()
	raw, err := fs.ReadFile(repository, "test/functional/coverage.json")
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := decodeFunctionalInventory(raw)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := discoverFunctionalSurfaces(repository)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateFunctionalInventory(inventory, expected, repository); err != nil {
		t.Fatal(err)
	}
	counts := make(map[string]int)
	for _, scenario := range inventory.Scenarios {
		counts[scenario.Status] += len(scenario.Surfaces)
	}
	t.Logf("classified %d surfaces in %d scenarios: %v (not a runtime pass report)", len(expected), len(inventory.Scenarios), counts)
}

func decodeFunctionalInventory(raw []byte) (functionalInventory, error) {
	var inventory functionalInventory
	if len(raw) > 1<<20 {
		return inventory, fmt.Errorf("functional inventory exceeds 1 MiB")
	}
	if err := jsondoc.Validate(raw); err != nil {
		return inventory, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&inventory); err != nil {
		return inventory, err
	}
	if inventory.FormatVersion != 1 || len(inventory.Scenarios) == 0 {
		return inventory, fmt.Errorf("functional inventory requires formatVersion 1 and scenarios")
	}
	return inventory, nil
}

var functionalScenarioID = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)

func validateFunctionalInventory(inventory functionalInventory, expected map[string]struct{}, repository fs.FS) error {
	seen, ids := make(map[string]struct{}), make(map[string]struct{})
	for _, scenario := range inventory.Scenarios {
		if !functionalScenarioID.MatchString(scenario.ID) {
			return fmt.Errorf("invalid scenario ID %q", scenario.ID)
		}
		if _, duplicate := ids[scenario.ID]; duplicate {
			return fmt.Errorf("duplicate scenario %s", scenario.ID)
		}
		ids[scenario.ID] = struct{}{}
		if !slices.Contains([]string{"compose", "minikube", "conditional", "development-only", "lower-level-only"}, scenario.Disposition) || !slices.Contains([]string{"covered", "partial", "missing", "not-applicable"}, scenario.Status) {
			return fmt.Errorf("%s has an invalid disposition or status", scenario.ID)
		}
		if (scenario.Disposition == "conditional") != (scenario.Status == "not-applicable") {
			return fmt.Errorf("%s must reserve not-applicable for conditional capabilities", scenario.ID)
		}
		if scenario.Disposition == "lower-level-only" && scenario.Status != "covered" {
			return fmt.Errorf("%s cannot use lower-level-only to hide a coverage gap", scenario.ID)
		}
		if (scenario.Status == "covered") != (len(scenario.Gaps) == 0) {
			return fmt.Errorf("%s must record gaps unless covered, and covered cannot retain gaps", scenario.ID)
		}
		for _, text := range append([]string{scenario.Reason, scenario.Success, scenario.Rejection, scenario.Recovery}, scenario.Gaps...) {
			if strings.TrimSpace(text) == "" {
				return fmt.Errorf("%s has an empty reason, obligation, or gap", scenario.ID)
			}
		}
		if len(scenario.Surfaces) == 0 || len(scenario.Evidence) == 0 {
			return fmt.Errorf("%s requires surfaces and existing evidence", scenario.ID)
		}
		if scenario.Disposition == "lower-level-only" && !slices.ContainsFunc(scenario.Evidence, func(evidence functionalEvidence) bool {
			return strings.HasSuffix(evidence.Path, "_test.go") && strings.HasPrefix(evidence.Symbol, "Test")
		}) {
			return fmt.Errorf("%s requires a named lower-level test", scenario.ID)
		}
		for _, surface := range scenario.Surfaces {
			if _, found := expected[surface]; !found {
				return fmt.Errorf("%s contains unknown surface %s", scenario.ID, surface)
			}
			if _, duplicate := seen[surface]; duplicate {
				return fmt.Errorf("surface %s is classified twice", surface)
			}
			seen[surface] = struct{}{}
		}
		for _, evidence := range scenario.Evidence {
			if err := validateFunctionalEvidence(repository, evidence); err != nil {
				return fmt.Errorf("%s: %w", scenario.ID, err)
			}
		}
	}
	if missing := sortedSetDifference(expected, seen); len(missing) != 0 {
		return fmt.Errorf("unclassified shipped surfaces: %s", strings.Join(missing, ", "))
	}
	return nil
}

func validateFunctionalEvidence(repository fs.FS, evidence functionalEvidence) error {
	if !fs.ValidPath(evidence.Path) || evidence.Path == "." {
		return fmt.Errorf("invalid evidence path %q", evidence.Path)
	}
	info, err := fs.Stat(repository, evidence.Path)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("evidence file %s is absent or not regular", evidence.Path)
	}
	if evidence.Symbol == "" {
		return nil
	}
	if !strings.HasSuffix(evidence.Path, ".go") {
		return fmt.Errorf("evidence symbols require Go source: %s", evidence.Path)
	}
	source, err := parseInventoryGoFile(repository, evidence.Path)
	if err != nil {
		return err
	}
	for _, declaration := range source.Decls {
		if function, ok := declaration.(*ast.FuncDecl); ok && function.Name.Name == evidence.Symbol {
			return nil
		}
	}
	return fmt.Errorf("evidence function %s is absent from %s", evidence.Symbol, evidence.Path)
}

func parseInventoryGoFile(repository fs.FS, filename string) (*ast.File, error) {
	raw, err := fs.ReadFile(repository, filename)
	if err != nil {
		return nil, err
	}
	return parser.ParseFile(token.NewFileSet(), filename, raw, 0)
}

func discoverFunctionalSurfaces(repository fs.FS) (map[string]struct{}, error) {
	expected := make(map[string]struct{})
	raw, err := fs.ReadFile(repository, "api/openapi/control-v1.yaml")
	if err != nil {
		return nil, err
	}
	document, err := openapi3.NewLoader().LoadFromData(raw)
	if err != nil {
		return nil, err
	}
	for _, item := range document.Paths.Map() {
		for _, operation := range item.Operations() {
			if operation.OperationID == "" {
				return nil, fmt.Errorf("OpenAPI operation has no operationId")
			}
			key := "http:" + operation.OperationID
			if _, duplicate := expected[key]; duplicate {
				return nil, fmt.Errorf("duplicate OpenAPI operationId %s", key)
			}
			expected[key] = struct{}{}
		}
	}
	operationFiles, err := fs.Glob(repository, "internal/platform/operations/*.go")
	if err != nil {
		return nil, err
	}
	for _, filename := range operationFiles {
		if strings.HasSuffix(filename, "_test.go") {
			continue
		}
		operations, err := parseInventoryGoFile(repository, filename)
		if err != nil {
			return nil, err
		}
		if err := discoverWorkerInventoryRoutes(operations, expected); err != nil {
			return nil, err
		}
	}
	for _, binary := range []string{"catalogd", "catalogctl"} {
		commands, err := discoverInventoryCommands(repository, binary)
		if err != nil {
			return nil, err
		}
		for command := range commands {
			expected[binary+":"+command] = struct{}{}
		}
	}
	return expected, nil
}

func discoverWorkerInventoryRoutes(operations *ast.File, expected map[string]struct{}) error {
	var routeErr error
	ast.Inspect(operations, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "HandleFunc" && selector.Sel.Name != "Handle" {
			return true
		}
		if len(call.Args) == 0 {
			routeErr = fmt.Errorf("worker route lacks a literal pattern")
			return false
		}
		literal, ok := call.Args[0].(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			routeErr = fmt.Errorf("worker route must expose its literal pattern to the inventory")
			return false
		}
		pattern, _ := strconv.Unquote(literal.Value)
		if _, duplicate := expected["worker:"+pattern]; duplicate {
			routeErr = fmt.Errorf("duplicate worker route %s", pattern)
		}
		expected["worker:"+pattern] = struct{}{}
		return true
	})
	return routeErr
}

// Usage strings are the CLI's existing command grammar. Read string literals,
// not comments; collapse parent summaries to documented leaf commands. Aliases
// (-h, --help and --version) share their canonical command's classification.
func discoverInventoryCommands(repository fs.FS, binary string) (map[string]struct{}, error) {
	files, err := fs.Glob(repository, "cmd/"+binary+"/*.go")
	if err != nil {
		return nil, err
	}
	commands := map[string]struct{}{"help": {}}
	explicitCommands := map[string]bool{}
	var dispatchWords []string
	for _, filename := range files {
		if strings.HasSuffix(filename, "_test.go") {
			continue
		}
		source, err := parseInventoryGoFile(repository, filename)
		if err != nil {
			return nil, err
		}
		var usageErr error
		ast.Inspect(source, func(node ast.Node) bool {
			if comparison, ok := node.(*ast.BinaryExpr); ok && (comparison.Op == token.EQL || comparison.Op == token.NEQ) {
				for _, pair := range [][2]ast.Expr{{comparison.X, comparison.Y}, {comparison.Y, comparison.X}} {
					if commandSelector(pair[0]) {
						if literal, ok := pair[1].(*ast.BasicLit); ok && literal.Kind == token.STRING {
							word, _ := strconv.Unquote(literal.Value)
							dispatchWords = append(dispatchWords, word)
						}
					}
				}
			}
			if selection, ok := node.(*ast.SwitchStmt); ok && commandSelector(selection.Tag) {
				for _, statement := range selection.Body.List {
					clause, ok := statement.(*ast.CaseClause)
					if !ok {
						continue
					}
					for _, expression := range clause.List {
						if literal, ok := expression.(*ast.BasicLit); ok && literal.Kind == token.STRING {
							word, _ := strconv.Unquote(literal.Value)
							dispatchWords = append(dispatchWords, word)
						}
					}
				}
			}
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, _ := strconv.Unquote(literal.Value)
			if grammar, found := strings.CutPrefix(value, "usage: "+binary+" "); found {
				leaves, err := expandInventoryUsage(grammar)
				if err != nil {
					usageErr = err
				}
				for _, leaf := range leaves {
					commands[leaf] = struct{}{}
				}
				// A direct usage such as `export --release-id ...` is runnable
				// even when `export preview` and `export batch` also exist.
				// Remove only aggregate parent summaries, not explicit commands.
				if len(leaves) == 1 {
					explicitCommands[leaves[0]] = true
				}
			}
			return true
		})
		if usageErr != nil {
			return nil, usageErr
		}
	}
	if len(commands) == 1 {
		return nil, fmt.Errorf("%s has no discoverable command usage", binary)
	}
	words := map[string]bool{"-h": true, "--help": true, "--version": true}
	for command := range commands {
		for _, word := range strings.Fields(command) {
			words[word] = true
		}
	}
	for _, word := range dispatchWords {
		if !words[word] {
			return nil, fmt.Errorf("%s dispatches undocumented command word %q; update its usage and inventory", binary, word)
		}
	}
	for parent := range commands {
		if explicitCommands[parent] {
			continue
		}
		for child := range commands {
			if strings.HasPrefix(child, parent+" ") {
				delete(commands, parent)
				break
			}
		}
	}
	return commands, nil
}

func commandSelector(expression ast.Expr) bool {
	if name, ok := expression.(*ast.Ident); ok {
		return name.Name == "action" || name.Name == "roleName"
	}
	index, ok := expression.(*ast.IndexExpr)
	if !ok {
		return false
	}
	name, ok := index.X.(*ast.Ident)
	return ok && name.Name == "arguments"
}

func expandInventoryUsage(grammar string) ([]string, error) {
	paths := []string{""}
	for _, word := range strings.Fields(grammar) {
		if strings.HasPrefix(word, "[") || strings.HasPrefix(word, "--") {
			break
		}
		if strings.HasPrefix(word, "<") && strings.HasSuffix(word, ">") {
			word = word[1 : len(word)-1]
		}
		var next []string
		for _, option := range strings.Split(word, "|") {
			if !functionalScenarioID.MatchString(option) {
				return nil, fmt.Errorf("unsupported CLI usage grammar %q", grammar)
			}
			for _, parent := range paths {
				next = append(next, strings.TrimSpace(parent+" "+option))
			}
		}
		paths = next
	}
	if len(paths) == 1 && paths[0] == "" {
		return nil, fmt.Errorf("empty CLI usage grammar")
	}
	return paths, nil
}
