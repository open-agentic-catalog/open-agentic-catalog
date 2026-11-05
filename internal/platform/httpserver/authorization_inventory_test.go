package httpserver

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	serviceapi "github.com/open-agentic-catalog/open-agentic-catalog/api"
)

// The target matrix is reviewed input, not a second runtime policy engine. This
// test catches unaccounted API additions and scope drift during the cutover.
func TestAuthorizationEvolutionOperationInventory(t *testing.T) {
	var matrix struct {
		Permissions map[string]struct {
			Scope     string   `json:"scope"`
			Delegable bool     `json:"delegable"`
			Requires  []string `json:"requires"`
		} `json:"permissions"`
		Builtins map[string][]string `json:"builtins"`
		Policies map[string]struct {
			ScopeSource string `json:"scopeSource"`
			Actors      string `json:"actors"`
			Disclosure  string `json:"disclosure"`
			Eligibility string `json:"eligibility"`
		} `json:"policies"`
		Operations []struct {
			OperationID            string              `json:"operationId"`
			Method                 string              `json:"method"`
			Path                   string              `json:"path"`
			Policy                 string              `json:"policy"`
			OAuthCeiling           string              `json:"oauthCeiling"`
			CurrentAuthority       string              `json:"currentAuthority"`
			Permissions            []string            `json:"permissions"`
			ConditionalPermissions map[string][]string `json:"conditionalPermissions"`
		} `json:"operations"`
	}
	raw, err := os.ReadFile("../../../test/authorization/inventory.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &matrix); err != nil {
		t.Fatal(err)
	}
	document, err := openapi3.NewLoader().LoadFromData(serviceapi.ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, row := range matrix.Operations {
		key := row.Method + " " + row.Path
		if seen[key] {
			t.Fatalf("duplicate operation %s", key)
		}
		seen[key] = true
		path := document.Paths.Value(row.Path)
		if path == nil || path.GetOperation(row.Method) == nil || path.GetOperation(row.Method).OperationID != row.OperationID {
			t.Fatalf("stale operation %s", key)
		}
		policy, ok := matrix.Policies[row.Policy]
		if !ok || policy.ScopeSource == "" || policy.Actors == "" || policy.Disclosure == "" || policy.Eligibility == "" || row.CurrentAuthority == "" {
			t.Fatalf("incomplete boundary %s", key)
		}
		if strings.HasPrefix(row.Path, "/api/v1/") && requiredOAuthScope(row.Method, row.Path) != row.OAuthCeiling {
			t.Fatalf("OAuth ceiling drift %s", key)
		}
		all := slices.Clone(row.Permissions)
		for _, permissions := range row.ConditionalPermissions {
			all = append(all, permissions...)
		}
		for _, permission := range all {
			if _, ok := matrix.Permissions[permission]; !ok {
				t.Fatalf("unknown action %s on %s", permission, key)
			}
		}
		if row.Path == "/api/v1/catalog/search" && !slices.Contains(row.ConditionalPermissions["external"], "federation.discover") {
			t.Fatal("external search needs pre-disclosure authority")
		}
	}
	for path, item := range document.Paths.Map() {
		for method := range item.Operations() {
			if !seen[method+" "+path] {
				t.Fatalf("unreviewed operation %s %s", method, path)
			}
		}
	}
	for permission, descriptor := range matrix.Permissions {
		if descriptor.Scope != "tenant" && descriptor.Scope != "namespace" {
			t.Fatalf("unknown scope for %s", permission)
		}
		for _, required := range descriptor.Requires {
			if _, ok := matrix.Permissions[required]; !ok {
				t.Fatalf("unknown dependency %s", required)
			}
		}
		if permission == "access.manage" && descriptor.Delegable {
			t.Fatal("access administration cannot be delegated through custom roles")
		}
	}
	for role, permissions := range matrix.Builtins {
		seenPermissions := map[string]bool{}
		for _, permission := range permissions {
			if seenPermissions[permission] {
				t.Fatalf("duplicate builtin action %s in %s", permission, role)
			}
			seenPermissions[permission] = true
			descriptor, ok := matrix.Permissions[permission]
			if !ok {
				t.Fatalf("unknown builtin action %s", permission)
			}
			for _, required := range descriptor.Requires {
				if !slices.Contains(permissions, required) {
					t.Fatalf("unusable builtin %s missing %s", role, required)
				}
			}
			if slices.Contains([]string{"reader", "contributor", "reviewer", "publisher"}, role) && descriptor.Scope != "namespace" {
				t.Fatalf("scoped template %s includes tenant-only permission %s", role, permission)
			}
		}
	}
	if len(matrix.Builtins["service_client"]) != 0 {
		t.Fatal("service classification must not supply authority")
	}
	if len(matrix.Builtins["tenant_admin"]) != len(matrix.Permissions) {
		t.Fatal("review administrator vocabulary explicitly")
	}
}

// Keep the exported domain caller inventory exhaustive as implementation moves
// off role arrays. Constructors, repositories and workers have separate owners.
func TestAuthorizationEvolutionDomainInventory(t *testing.T) {
	var fixture struct {
		Policies      map[string]json.RawMessage `json:"policies"`
		DomainCallers []struct {
			Package  string `json:"package"`
			Receiver string `json:"receiver"`
			Method   string `json:"method"`
			Source   string `json:"source"`
			Policy   string `json:"policy"`
		} `json:"domainCallers"`
	}
	raw, err := os.ReadFile("../../../test/authorization/inventory.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	documented := map[string]bool{}
	for _, row := range fixture.DomainCallers {
		key := row.Source + ":" + row.Receiver + "." + row.Method
		if documented[key] {
			t.Fatalf("duplicate caller %s", key)
		}
		documented[key] = true
		if _, ok := fixture.Policies[row.Policy]; !ok {
			t.Fatalf("unreviewed caller policy %s", key)
		}
	}
	observed := map[string]bool{}
	for _, pkg := range []string{"catalog", "identity", "federation", "publication", "jobs", "webhook", "feedback", "ratings", "personal"} {
		files, err := filepath.Glob("../../" + pkg + "/*.go")
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			tree, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, decl := range tree.Decls {
				function, ok := decl.(*ast.FuncDecl)
				if !ok || function.Recv == nil || !function.Name.IsExported() || function.Body == nil {
					continue
				}
				receiver := function.Recv.List[0].Type
				if pointer, ok := receiver.(*ast.StarExpr); ok {
					receiver = pointer.X
				}
				name, ok := receiver.(*ast.Ident)
				if !ok || !slices.Contains([]string{"Service", "Manager", "SearchService"}, name.Name) {
					continue
				}
				key := "internal/" + pkg + "/" + filepath.Base(file) + ":" + name.Name + "." + function.Name.Name
				observed[key] = true
				if !documented[key] {
					t.Fatalf("unreviewed domain caller %s", key)
				}
			}
		}
	}
	for key := range documented {
		if !observed[key] {
			t.Fatalf("stale domain caller %s", key)
		}
	}
}
