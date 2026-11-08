package runtime

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"strconv"
	"strings"
	"testing"
)

const serviceModulePath = "github.com/open-agentic-catalog/open-agentic-catalog"

type architectureFinding struct {
	line    int
	message string
}

type dependencyOwnership struct {
	importPrefix string
	owners       []string
}

var domainPackageDirectories = []string{
	"internal/adapter",
	"internal/artwork",
	"internal/audit",
	"internal/catalog",
	"internal/draftfile",
	"internal/events",
	"internal/federation",
	"internal/feedback",
	"internal/housekeeping",
	"internal/identity",
	"internal/jobs",
	"internal/jsondoc",
	"internal/overview",
	"internal/personal",
	"internal/publication",
	"internal/ratings",
	"internal/safeerr",
	"internal/standards",
	"internal/tenantsettings",
	"internal/webhook",
}

var architectureSourceDirectories = []string{"api", "cmd", "internal", "migrations", "schemas", "catalog-ui"}

var providerDependencies = []dependencyOwnership{
	{importPrefix: "github.com/coreos/go-oidc/v3", owners: []string{"internal/platform/authn", "internal/platform/cliauth", "catalog-ui/internal/web"}},
	{importPrefix: "github.com/jackc/pgx/v5", owners: []string{"internal/platform/database", "internal/platform/runtime"}},
}

func TestArchitectureContracts(t *testing.T) {
	repository := openTestRepository(t)
	// A new first-party root needs an explicit ownership decision. Platform
	// composition, generated protocol bindings and composed test journeys are
	// deliberately outside the domain-to-platform dependency rule.
	excluded := map[string]string{
		"internal/a2apb":       "generated pinned protocol bindings",
		"internal/integration": "composed application test journeys",
		"internal/platform":    "concrete adapters and composition",
	}
	entries, err := fs.ReadDir(repository.FS(), "internal")
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		directory := "internal/" + entry.Name()
		seen[directory] = true
		if !isDomainDirectory(directory) && excluded[directory] == "" {
			t.Errorf("%s needs an explicit architecture ownership disposition", directory)
		}
	}
	protected := make(map[string]bool)
	for _, directory := range domainPackageDirectories {
		if !seen[directory] || protected[directory] || excluded[directory] != "" {
			t.Errorf("invalid, duplicate or excluded protected domain %s", directory)
		}
		protected[directory] = true
	}
	moduleFile := string(readTestFile(t, repository, "go.mod"))
	if !strings.HasPrefix(moduleFile, "module "+serviceModulePath+"\n") {
		t.Fatalf("architecture analyzer module path %q does not match go.mod", serviceModulePath)
	}
	// Check every PostgreSQL integration file, not just the suite's first file.
	integrationFiles, err := fs.Glob(repository.FS(), "internal/platform/database/*_integration_test.go")
	if err != nil || len(integrationFiles) == 0 {
		t.Fatalf("find PostgreSQL integration tests: %v (files: %d)", err, len(integrationFiles))
	}
	for _, filename := range integrationFiles {
		content := string(readTestFile(t, repository, filename))
		if !strings.HasPrefix(content, "//go:build integration\n\n") {
			t.Errorf("%s must remain excluded from dependency-free default test runs", filename)
		}
	}
	err = fs.WalkDir(repository.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if name == "." {
				return nil
			}
			if name == "schemas/upstream" || name == "catalog-ui/node_modules" || name == "catalog-ui/dist" {
				return fs.SkipDir
			}
			if isOwnedDirectory(name, architectureSourceDirectories) {
				return nil
			}
			return fs.SkipDir
		}
		if !entry.Type().IsRegular() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}

		content, err := repository.ReadFile(name)
		if err != nil {
			return err
		}
		findings, err := analyzeArchitectureFile(name, content)
		if err != nil {
			return err
		}
		for _, finding := range findings {
			t.Errorf("%s:%d: %s", name, finding.line, finding.message)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("inspect repository architecture: %v", err)
	}
}

func TestArchitectureAnalyzer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		filename    string
		source      string
		wantMessage string
	}{
		{
			name:        "domain cannot import database adapter",
			filename:    "internal/catalog/bad.go",
			source:      `package catalog; import _ "github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/database"`,
			wantMessage: "domain package cannot import platform implementation",
		},
		{
			name:     "federation can import hardened fetcher",
			filename: "internal/federation/good.go",
			source:   `package federation; import _ "github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"`,
		},
		{
			name:        "provider dependency stays with owner",
			filename:    "internal/catalog/bad.go",
			source:      `package catalog; import _ "github.com/jackc/pgx/v5"`,
			wantMessage: "provider dependency may only be imported by",
		},
		{
			name:        "raw outbound client is rejected",
			filename:    "internal/publication/bad.go",
			source:      `package publication; import "net/http"; var client = &http.Client{}`,
			wantMessage: "construct outbound HTTP through internal/platform/fetcher",
		},
		{
			name:     "shared Control client owns its transport",
			filename: "internal/platform/controlclient/client.go",
			source:   `package controlclient; import "net/http"; var client = &http.Client{}`,
		},
		{
			name:        "remote command parser cannot construct HTTP",
			filename:    "cmd/catalogctl/remote.go",
			source:      `package main; import "net/http"; var client = &http.Client{}`,
			wantMessage: "construct outbound HTTP through internal/platform/fetcher",
		},
		{
			name:        "raw outbound transport allocation is rejected",
			filename:    "internal/publication/bad.go",
			source:      `package publication; import "net/http"; var transport = new(http.Transport)`,
			wantMessage: "construct Transport",
		},
		{
			name:        "package-level HTTP call is rejected",
			filename:    "internal/publication/bad.go",
			source:      `package publication; import "net/http"; func fetch() { _, _ = http.Get("https://example.com") }`,
			wantMessage: "call http.Get",
		},
		{
			name:        "aliased request construction is rejected",
			filename:    "internal/publication/bad.go",
			source:      `package publication; import web "net/http"; func request() { _, _ = web.NewRequest("GET", "https://example.com", nil) }`,
			wantMessage: "call http.NewRequest",
		},
		{
			name:        "default HTTP client is rejected",
			filename:    "internal/publication/bad.go",
			source:      `package publication; import "net/http"; var client = http.DefaultClient`,
			wantMessage: "use http.DefaultClient",
		},
		{
			name:        "dot import cannot bypass enforcement",
			filename:    "internal/publication/bad.go",
			source:      `package publication; import . "net/http"`,
			wantMessage: "dot-imported net/http",
		},
		{
			name:     "fetcher owns outbound client construction",
			filename: "internal/platform/fetcher/good.go",
			source:   `package fetcher; import "net/http"; var client = &http.Client{}`,
		},
		{
			name:     "OIDC verifier owns bounded identity-provider client",
			filename: "internal/platform/authn/good.go",
			source:   `package authn; import "net/http"; var client = &http.Client{}`,
		},
		{
			name:     "loopback smoke client is explicit exception",
			filename: "cmd/catalogctl/smoke.go",
			source:   `package main; import "net/http"; var client = &http.Client{}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			findings, err := analyzeArchitectureFile(test.filename, []byte(test.source))
			if err != nil {
				t.Fatal(err)
			}
			if test.wantMessage == "" {
				if len(findings) != 0 {
					t.Fatalf("unexpected findings: %#v", findings)
				}
				return
			}
			for _, finding := range findings {
				if strings.Contains(finding.message, test.wantMessage) {
					return
				}
			}
			t.Fatalf("findings %#v do not contain %q", findings, test.wantMessage)
		})
	}
}

func TestArchitectureDomainOwnership(t *testing.T) {
	t.Parallel()
	// Literal controls are independent of the list consumed by the analyzer:
	// removing a protected root must break its own denial control.
	for _, root := range []string{
		"adapter", "artwork", "audit", "catalog", "draftfile", "events",
		"federation", "feedback", "housekeeping", "identity", "jobs", "jsondoc",
		"overview", "personal", "publication", "ratings", "safeerr", "standards",
		"tenantsettings", "webhook",
	} {
		t.Run(root, func(t *testing.T) {
			for _, suffix := range []string{"/bad.go", "/nested/bad.go"} {
				findings, err := analyzeArchitectureFile("internal/"+root+suffix,
					[]byte(`package fixture; import _ "`+serviceModulePath+`/internal/platform/database"`))
				if err != nil || len(findings) != 1 || !strings.Contains(findings[0].message, "domain package cannot import platform implementation") {
					t.Fatalf("%s%s: findings=%v err=%v", root, suffix, findings, err)
				}
			}
			// A dependency-neutral domain edge is legitimate.
			findings, err := analyzeArchitectureFile("internal/"+root+"/good.go",
				[]byte(`package fixture; import _ "`+serviceModulePath+`/internal/catalog"`))
			if err != nil || len(findings) != 0 {
				t.Fatalf("allowed domain edge: findings=%v err=%v", findings, err)
			}
		})
	}
	for _, filename := range []string{
		"internal/platform/runtime/wire.go", "internal/integration/journey.go",
		"internal/a2apb/binding.go", "internal/catalogneighbor/file.go",
	} {
		findings, err := analyzeArchitectureFile(filename,
			[]byte(`package fixture; import _ "`+serviceModulePath+`/internal/platform/database"`))
		if err != nil || len(findings) != 0 {
			t.Fatalf("unprotected neighbor %s: findings=%v err=%v", filename, findings, err)
		}
	}
	for _, test := range []struct {
		file, imported string
		allowed        bool
	}{
		{"internal/federation/fetch.go", "internal/platform/fetcher", true},
		{"internal/federation/sub/fetch.go", "internal/platform/fetcher", false},
		{"internal/federation/fetch.go", "internal/platform/fetcher/neighbor", false},
		{"internal/catalog/fetch.go", "internal/platform/fetcher", false},
	} {
		findings, err := analyzeArchitectureFile(test.file,
			[]byte(`package fixture; import _ "`+serviceModulePath+`/`+test.imported+`"`))
		if err != nil || (len(findings) == 0) != test.allowed {
			t.Fatalf("exact federation exception %s -> %s: findings=%v err=%v", test.file, test.imported, findings, err)
		}
	}
}

func analyzeArchitectureFile(filename string, content []byte) ([]architectureFinding, error) {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, filename, content, 0)
	if err != nil {
		return nil, err
	}

	directory := path.Dir(filename)
	findings := make([]architectureFinding, 0)
	httpAliases := make(map[string]struct{})
	for _, specification := range file.Imports {
		importPath, err := strconv.Unquote(specification.Path.Value)
		if err != nil {
			return nil, err
		}
		line := fileSet.Position(specification.Pos()).Line
		if isDomainDirectory(directory) && strings.HasPrefix(importPath, serviceModulePath+"/internal/platform/") && (directory != "internal/federation" || importPath != serviceModulePath+"/internal/platform/fetcher") {
			findings = append(findings, architectureFinding{line: line, message: "domain package cannot import platform implementation " + importPath})
		}
		for _, dependency := range providerDependencies {
			if hasImportPrefix(importPath, dependency.importPrefix) && !isOwnedDirectory(directory, dependency.owners) {
				findings = append(findings, architectureFinding{line: line, message: dependency.importPrefix + " provider dependency may only be imported by " + strings.Join(dependency.owners, " or ")})
			}
		}
		if importPath != "net/http" {
			continue
		}
		alias := path.Base(importPath)
		if specification.Name != nil {
			alias = specification.Name.Name
		}
		if alias == "." && !allowsHTTPConstruction(filename) {
			findings = append(findings, architectureFinding{line: line, message: "dot-imported net/http bypasses outbound construction enforcement"})
			continue
		}
		httpAliases[alias] = struct{}{}
	}

	if allowsHTTPConstruction(filename) || len(httpAliases) == 0 {
		return findings, nil
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch expression := node.(type) {
		case *ast.CompositeLit:
			if member, ok := httpMember(expression.Type, httpAliases); ok && (member == "Client" || member == "Transport") {
				findings = append(findings, outboundHTTPFinding(fileSet, expression.Pos(), "construct "+member))
			}
		case *ast.CallExpr:
			if identifier, ok := expression.Fun.(*ast.Ident); ok && identifier.Name == "new" && len(expression.Args) == 1 {
				if member, ok := httpMember(expression.Args[0], httpAliases); ok && (member == "Client" || member == "Transport") {
					findings = append(findings, outboundHTTPFinding(fileSet, expression.Pos(), "construct "+member))
				}
			}
			if member, ok := httpMember(expression.Fun, httpAliases); ok && isHTTPConstructionFunction(member) {
				findings = append(findings, outboundHTTPFinding(fileSet, expression.Pos(), "call http."+member))
			}
		case *ast.SelectorExpr:
			if member, ok := httpMember(expression, httpAliases); ok && (member == "DefaultClient" || member == "DefaultTransport") {
				findings = append(findings, outboundHTTPFinding(fileSet, expression.Pos(), "use http."+member))
			}
		}
		return true
	})
	return findings, nil
}

func isDomainDirectory(directory string) bool {
	return isOwnedDirectory(directory, domainPackageDirectories)
}

func isOwnedDirectory(directory string, owners []string) bool {
	for _, owner := range owners {
		if directory == owner || strings.HasPrefix(directory, owner+"/") {
			return true
		}
	}
	return false
}

func hasImportPrefix(importPath, prefix string) bool {
	return importPath == prefix || strings.HasPrefix(importPath, prefix+"/")
}

func allowsHTTPConstruction(filename string) bool {
	directory := path.Dir(filename)
	return directory == "internal/platform/fetcher" || strings.HasPrefix(directory, "internal/platform/fetcher/") || directory == "internal/platform/authn" || strings.HasPrefix(directory, "internal/platform/authn/") || filename == "cmd/catalogctl/smoke.go" || filename == "cmd/catalogctl/probe.go" || filename == "internal/platform/controlclient/client.go" || filename == "catalog-ui/internal/web/transport.go"
}

func httpMember(expression ast.Expr, aliases map[string]struct{}) (string, bool) {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	identifier, ok := selector.X.(*ast.Ident)
	if !ok {
		return "", false
	}
	if _, ok := aliases[identifier.Name]; !ok {
		return "", false
	}
	return selector.Sel.Name, true
}

func isHTTPConstructionFunction(name string) bool {
	switch name {
	case "Get", "Head", "NewRequest", "NewRequestWithContext", "Post", "PostForm":
		return true
	default:
		return false
	}
}

func outboundHTTPFinding(fileSet *token.FileSet, position token.Pos, operation string) architectureFinding {
	return architectureFinding{line: fileSet.Position(position).Line, message: operation + ": construct outbound HTTP through internal/platform/fetcher"}
}
