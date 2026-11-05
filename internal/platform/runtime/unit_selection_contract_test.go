package runtime

import (
	"errors"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// This direct-call guard supplements fixture review; it is not a call graph or
// a sandbox for indirect transport calls through application interfaces.
func TestUnitSelectionContracts(t *testing.T) {
	repository := openTestRepository(t)
	makefile := string(readTestFile(t, repository, "Makefile"))
	var roots []string
	for line := range strings.SplitSeq(makefile, "\n") {
		if strings.HasPrefix(line, "GO_SOURCE_DIRS := ") {
			roots = strings.Fields(strings.TrimPrefix(line, "GO_SOURCE_DIRS := "))
		}
	}
	if len(roots) == 0 {
		t.Fatal("owned Go roots missing from Makefile")
	}
	fuzzTargets := map[string]bool{}
	for _, tags := range [][]string{nil, {"minikube"}} {
		context := build.Default
		context.BuildTags = tags
		for _, root := range roots {
			err := fs.WalkDir(repository.FS(), root, func(name string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if !entry.IsDir() {
					return nil
				}
				if strings.HasPrefix(entry.Name(), ".") || slices.Contains([]string{"node_modules", "testdata", "upstream", "generated", "vendor"}, entry.Name()) {
					return fs.SkipDir
				}
				if name != root {
					if _, err := repository.Stat(path.Join(name, "go.mod")); err == nil {
						return fs.SkipDir
					}
				}
				pkg, err := context.ImportDir(filepath.Join(repository.Name(), filepath.FromSlash(name)), 0)
				var noGo *build.NoGoError
				if errors.As(err, &noGo) {
					return nil
				}
				if err != nil {
					return err
				}
				for _, filename := range append(slices.Clone(pkg.TestGoFiles), pkg.XTestGoFiles...) {
					filename = path.Join(name, filename)
					content := readTestFile(t, repository, filename)
					findings, err := analyzeUnitTestFile(filename, content)
					if err != nil {
						return err
					}
					for _, finding := range findings {
						t.Errorf("%s:%d (%v): %s", filename, finding.line, tags, finding.message)
					}
					file, err := parser.ParseFile(token.NewFileSet(), filename, content, 0)
					if err != nil {
						return err
					}
					for _, declaration := range file.Decls {
						function, ok := declaration.(*ast.FuncDecl)
						if ok && function.Recv == nil && strings.HasPrefix(function.Name.Name, "Fuzz") {
							fuzzTargets[function.Name.Name] = true
							want := "$(GO) test ./" + name + " -run '^$$' -fuzz " + function.Name.Name + " -fuzztime=$(FUZZ_TIME)"
							if !strings.Contains(makefile, want) {
								t.Errorf("bounded campaign missing: %s", want)
							}
						}
					}
				}
				// The sole coverage exemption must remain embed-only.
				if name == "migrations" {
					for _, filename := range pkg.GoFiles {
						file, err := parser.ParseFile(token.NewFileSet(), filename, readTestFile(t, repository, path.Join(name, filename)), 0)
						if err != nil {
							return err
						}
						ast.Inspect(file, func(node ast.Node) bool {
							switch node.(type) {
							case *ast.FuncDecl, *ast.FuncLit:
								t.Error("migrations gained executable functions; remove its zero-statement coverage exemption")
							}
							return true
						})
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(fuzzTargets) == 0 {
		t.Fatal("no fuzz targets were inspected")
	}
}

func analyzeUnitTestFile(filename string, content []byte) ([]architectureFinding, error) {
	positions := token.NewFileSet()
	file, err := parser.ParseFile(positions, filename, content, 0)
	if err != nil {
		return nil, err
	}
	forbidden := map[string][]string{
		"net/http/httptest":              {"NewServer", "NewTLSServer", "NewUnstartedServer"},
		"net":                            {"Listen", "ListenPacket", "ListenTCP", "ListenUDP", "ListenUnix", "Dial", "DialTimeout", "LookupHost", "LookupIP", "LookupAddr", "LookupCNAME", "LookupMX", "LookupNS", "LookupTXT", "LookupSRV"},
		"crypto/tls":                     {"Listen", "Dial", "DialWithDialer"},
		"net/http":                       {"ListenAndServe", "ListenAndServeTLS", "Serve", "ServeTLS", "Get", "Head", "Post", "PostForm"},
		"os":                             {"StartProcess"},
		"syscall":                        {"Exec", "ForkExec", "StartProcess"},
		"github.com/jackc/pgx/v5":        {"Connect", "ConnectConfig"},
		"github.com/jackc/pgx/v5/pgconn": {"Connect", "ConnectConfig"},
		serviceModulePath + "/internal/platform/database": {"Open", "OpenRuntime", "OpenRuntimeObserved"},
	}
	aliases := map[string]string{}
	var findings []architectureFinding
	add := func(node ast.Node, message string) {
		findings = append(findings, architectureFinding{line: positions.Position(node.Pos()).Line, message: message})
	}
	for _, specification := range file.Imports {
		name, err := strconv.Unquote(specification.Path.Value)
		if err != nil {
			return nil, err
		}
		alias := path.Base(name)
		if name == "github.com/jackc/pgx/v5" {
			alias = "pgx"
		}
		if specification.Name != nil {
			alias = specification.Name.Name
		}
		if alias == "." && (forbidden[name] != nil || name == "os/exec") {
			add(specification, "dot import prevents unit boundary inspection: "+name)
		}
		aliases[alias] = name
	}
	command := func(expression ast.Expr) bool {
		if address, ok := expression.(*ast.UnaryExpr); ok && address.Op == token.AND {
			expression = address.X
		}
		if literal, ok := expression.(*ast.CompositeLit); ok {
			selector, ok := literal.Type.(*ast.SelectorExpr)
			if !ok {
				return false
			}
			identifier, ok := selector.X.(*ast.Ident)
			return ok && identifier.Obj == nil && aliases[identifier.Name] == "os/exec" && selector.Sel.Name == "Cmd"
		}
		call, ok := expression.(*ast.CallExpr)
		if !ok {
			return false
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		identifier, ok := selector.X.(*ast.Ident)
		return ok && identifier.Obj == nil && aliases[identifier.Name] == "os/exec" && (selector.Sel.Name == "Command" || selector.Sel.Name == "CommandContext")
	}
	commands := map[token.Pos]bool{}
	ast.Inspect(file, func(node ast.Node) bool {
		if assignment, ok := node.(*ast.AssignStmt); ok {
			for i, value := range assignment.Rhs {
				if i < len(assignment.Lhs) && command(value) {
					if identifier, ok := assignment.Lhs[i].(*ast.Ident); ok && identifier.Obj != nil {
						commands[identifier.Obj.Pos()] = true
					}
				}
			}
		}
		if value, ok := node.(*ast.ValueSpec); ok {
			for i, expression := range value.Values {
				if i < len(value.Names) && value.Names[i].Obj != nil && command(expression) {
					commands[value.Names[i].Obj.Pos()] = true
				}
			}
		}
		return true
	})
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		identifier, named := selector.X.(*ast.Ident)
		if named && identifier.Obj == nil && slices.Contains(forbidden[aliases[identifier.Name]], selector.Sel.Name) {
			add(call, "real boundary requires functional selection: "+aliases[identifier.Name]+"."+selector.Sel.Name)
		}
		if slices.Contains([]string{"Start", "Run", "Output", "CombinedOutput"}, selector.Sel.Name) && (command(selector.X) || named && identifier.Obj != nil && commands[identifier.Obj.Pos()]) {
			add(call, "child process requires functional selection")
		}
		return true
	})
	return findings, nil
}

func TestUnitSelectionAnalyzer(t *testing.T) {
	for _, item := range []struct {
		name, source string
		want         int
	}{
		{"aliased initializer", `package p; import h "net/http/httptest"; var server = h.NewUnstartedServer(nil)`, 1},
		{"cleanup", `package p; import "net"; func test() { defer net.Listen("tcp", "") }`, 1},
		{"child", `package p; import e "os/exec"; func test() { c := e.Command("x"); c.Run() }`, 1},
		{"direct child", `package p; import "os/exec"; func init() { exec.Command("x").CombinedOutput() }`, 1},
		{"global child", `package p; import "os/exec"; var c = exec.Command("x"); func test() { c.Start() }`, 1},
		{"literal child", `package p; import "os/exec"; func test() { c := &exec.Cmd{}; c.Output() }`, 1},
		{"database connection", `package p; import "github.com/jackc/pgx/v5"; func test() { pgx.Connect(nil, "") }`, 1},
		{"discarded command", `package p; import "os/exec"; var _ = exec.Command("x"); func test() { _ = exec.Command("x") }`, 0},
		{"dot import", `package p; import . "net/http/httptest"`, 1},
		{"recorder and inert command", `package p; import "net/http/httptest"; import "os/exec"; func test() { httptest.NewRecorder(); exec.Command("x") }`, 0},
		{"fake and shadowed name", `package p; import "net"; func test() { net := fake(); net.Listen(); fake().Run() }`, 0},
	} {
		t.Run(item.name, func(t *testing.T) {
			findings, err := analyzeUnitTestFile("fixture_test.go", []byte(item.source))
			if err != nil || len(findings) != item.want {
				t.Fatalf("findings = %v, error = %v; want %d", findings, err, item.want)
			}
		})
	}
	if _, err := analyzeUnitTestFile("invalid.go", []byte("not Go")); err == nil {
		t.Fatal("malformed source accepted")
	}
}
