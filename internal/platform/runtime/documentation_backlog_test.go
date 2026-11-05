// Tracker checks preserve task identity and open/closed placement during moves.
package runtime

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// indexLocalBacklog checks the complete tracker set when the optional local
// directory exists. An absent directory does not disable other document checks.
func indexLocalBacklog(filesystem fs.FS) (map[string]string, []string) {
	info, err := fs.Stat(filesystem, "todo")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, []string{fmt.Sprintf("read local tracker directory: %v", err)}
	}
	if !info.IsDir() {
		return nil, []string{"todo must be a directory"}
	}
	definitions := make(map[string]string)
	var problems []string
	for _, file := range []string{"todo/todo.md", "todo/deferred.md", "todo/done.md"} {
		content, err := fs.ReadFile(filesystem, file)
		if err != nil {
			problems = append(problems, fmt.Sprintf("read local tracker %s: %v", file, err))
			continue
		}
		problems = append(problems, indexBacklogTracker(file, string(content), definitions)...)
	}
	if len(problems) == 0 && len(definitions) == 0 {
		problems = append(problems, "local trackers define no backlog identifiers")
	}
	return definitions, problems
}

func TestLocalBacklogOptional(t *testing.T) {
	complete := fstest.MapFS{
		"todo/todo.md":     {Data: []byte("- [ ] **N-100 - Active.**")},
		"todo/deferred.md": {Data: []byte("- [ ] **N-101 - Deferred.**")},
		"todo/done.md":     {Data: []byte("- [x] **N-102 - Closed.**")},
	}
	for _, tc := range []struct {
		name        string
		filesystem  fs.FS
		definitions int
		problem     string
	}{
		{"absent", fstest.MapFS{}, 0, ""},
		{"complete", complete, 3, ""},
		{"no closed tasks", fstest.MapFS{"todo/todo.md": complete["todo/todo.md"], "todo/deferred.md": {}, "todo/done.md": {}}, 1, ""},
		{"empty", fstest.MapFS{"todo/todo.md": {}, "todo/deferred.md": {}, "todo/done.md": {}}, 0, "no backlog identifiers"},
		{"partial", fstest.MapFS{"todo/todo.md": complete["todo/todo.md"]}, 1, "read local tracker todo/deferred.md"},
		{"duplicate", fstest.MapFS{"todo/todo.md": complete["todo/todo.md"], "todo/deferred.md": complete["todo/todo.md"], "todo/done.md": {}}, 1, "repeats backlog identifier N-100"},
		{"wrong placement", fstest.MapFS{"todo/todo.md": complete["todo/done.md"], "todo/deferred.md": {}, "todo/done.md": {}}, 1, "move N-102 to the correct tracker"},
		{"not a directory", fstest.MapFS{"todo": {}}, 0, "todo must be a directory"},
		{"unreadable directory", deniedBacklogFS{complete, "todo"}, 0, "read local tracker directory"},
		{"unreadable tracker", deniedBacklogFS{complete, "todo/deferred.md"}, 2, "read local tracker todo/deferred.md"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			definitions, problems := indexLocalBacklog(tc.filesystem)
			if len(definitions) != tc.definitions {
				t.Fatalf("definitions = %v, want %d", definitions, tc.definitions)
			}
			if tc.problem == "" {
				if len(problems) != 0 {
					t.Fatal(problems)
				}
			} else if !strings.Contains(strings.Join(problems, "\n"), tc.problem) {
				t.Fatalf("problems = %v, want %q", problems, tc.problem)
			}
			if tc.name == "absent" && definitions != nil {
				t.Fatalf("absent tracker directory returned definitions: %v", definitions)
			}
		})
	}
}

// Permission failures remain errors, rather than making present trackers optional.
type deniedBacklogFS struct {
	fs.FS
	denied string
}

func (f deniedBacklogFS) Open(name string) (fs.File, error) {
	if name == f.denied {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrPermission}
	}
	return f.FS.Open(name)
}

// indexBacklogTracker adds definitions to a shared index and reports duplicate
// identities or checkbox states inconsistent with the destination tracker.
func indexBacklogTracker(file, content string, definitions map[string]string) []string {
	var problems []string
	for _, match := range backlogDefinition.FindAllStringSubmatch(content, -1) {
		if previous, exists := definitions[match[1]]; exists {
			problems = append(problems, fmt.Sprintf("%s repeats backlog identifier %s defined in %s", file, match[1], previous))
		} else {
			definitions[match[1]] = file
		}
		if open := strings.HasPrefix(match[0], "- [ ]"); open != (file != "todo/done.md") {
			problems = append(problems, fmt.Sprintf("%s: move %s to the correct tracker; active/deferred tasks are open and done tasks are closed", file, match[1]))
		}
	}
	return problems
}

func checkBacklogReferences(file, content string, definitions map[string]string) []string {
	var problems []string
	for _, reference := range backlogReference.FindAllString(content, -1) {
		if _, exists := definitions[reference]; !exists {
			problems = append(problems, fmt.Sprintf("%s references undefined backlog identifier %s", file, reference))
		}
	}
	return problems
}

// TestBacklogTrackerPlacement covers both open destinations, either closed-box
// spelling, references without definitions and duplication within/across files.
func TestBacklogTrackerPlacement(t *testing.T) {
	for _, id := range []string{"I-01", "N-143", "N-1000"} {
		t.Run(id, func(t *testing.T) {
			testBacklogTrackerPlacement(t, id)
		})
	}
}

func testBacklogTrackerPlacement(t *testing.T, id string) {
	t.Helper()
	wrongState := "move " + id + " to the correct tracker; active/deferred tasks are open and done tasks are closed"
	for _, tc := range []struct {
		name, file, content string
		problem             string
		wantDefinitions     int
	}{
		{"active", "todo/todo.md", "- [ ] **$ID - Work.**", "", 1},
		{"deferred", "todo/deferred.md", "- [ ] **$ID - Work.**", "", 1},
		{"closed", "todo/done.md", "- [x] **$ID - Work.**", "", 1},
		{"uppercase closed", "todo/done.md", "- [X] **$ID - Work.**", "", 1},
		{"closed in active", "todo/todo.md", "- [x] **$ID - Work.**", "todo/todo.md: " + wrongState, 1},
		{"closed in deferred", "todo/deferred.md", "- [X] **$ID - Work.**", "todo/deferred.md: " + wrongState, 1},
		{"open in closed", "todo/done.md", "- [ ] **$ID - Work.**", "todo/done.md: " + wrongState, 1},
		{"reference only", "todo/todo.md", "See $ID in another tracker.", "", 0},
		{"same file duplicate", "todo/deferred.md", "- [ ] **$ID - Work.**\n- [ ] **$ID - Duplicate.**", "todo/deferred.md repeats backlog identifier " + id + " defined in todo/deferred.md", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			definitions := make(map[string]string)
			problems := indexBacklogTracker(tc.file, strings.ReplaceAll(tc.content, "$ID", id), definitions)
			var want []string
			if tc.problem != "" {
				want = []string{tc.problem}
			}
			if !slices.Equal(problems, want) {
				t.Fatalf("problems = %v, want %v", problems, want)
			}
			if len(definitions) != tc.wantDefinitions || (tc.wantDefinitions != 0 && definitions[id] != tc.file) {
				t.Fatalf("definitions = %v, want %d entries with %s in %s", definitions, tc.wantDefinitions, id, tc.file)
			}
		})
	}
	definitions := make(map[string]string)
	for i, file := range []string{"todo/todo.md", "todo/deferred.md", "todo/done.md"} {
		box := " "
		if file == "todo/done.md" {
			box = "x"
		}
		problems := indexBacklogTracker(file, "- ["+box+"] **"+id+" - Work.**", definitions)
		var want []string
		if i != 0 {
			want = []string{file + " repeats backlog identifier " + id + " defined in todo/todo.md"}
		}
		if !slices.Equal(problems, want) || !maps.Equal(definitions, map[string]string{id: "todo/todo.md"}) {
			t.Fatalf("cross-file identity: problems=%v definitions=%v", problems, definitions)
		}
	}
}

func TestBacklogIdentifierFamilies(t *testing.T) {
	// These are tracker families, not ADRs, advisories, digests or curve names.
	for _, id := range []string{"ARD-13", "B-03", "C-09", "D-09", "F-11", "I-01", "N-143", "O-15", "Q-05", "R-07", "S-09", "X-01"} {
		t.Run(id, func(t *testing.T) {
			definitions := make(map[string]string)
			if problems := indexBacklogTracker("todo/todo.md", "- [ ] **"+id+" - Work.**", definitions); len(problems) != 0 {
				t.Fatal(problems)
			}
			if !maps.Equal(definitions, map[string]string{id: "todo/todo.md"}) {
				t.Fatalf("definitions = %v, want exact identifier %s", definitions, id)
			}
			if references := backlogReference.FindAllString(id, -1); !slices.Equal(references, []string{id}) {
				t.Fatalf("references = %v, want [%s]", references, id)
			}
		})
	}
}

func TestBacklogReferences(t *testing.T) {
	definitions := make(map[string]string)
	content := "- [ ] **I-01 - Work.**\n- [ ] **N-100 - Work.**\n- [ ] **N-1000 - Work.**"
	if problems := indexBacklogTracker("todo/todo.md", content, definitions); len(problems) != 0 {
		t.Fatal(problems)
	}
	for _, tc := range []struct {
		name, content string
		undefined     []string
	}{
		{"valid references", "[I-01](todo.md), N-100/N-1000; `N-100`. N-100's scope.", nil},
		{"undefined two digits", "See I-02.", []string{"I-02"}},
		{"undefined three digits", "See N-101.", []string{"N-101"}},
		{"undefined longer number", "See N-1001.", []string{"N-1001"}},
		{"adjacent references", "I-02,N-101/N-1001", []string{"I-02", "N-101", "N-1001"}},
		{"unrelated identifiers", "ADR-0017 CVE-2026-12345 GO-2026-1234 SHA-256 P-256 ES-256 RS-256 OTHER-12", nil},
		{"incomplete identifiers", "N-1 I-1 n-101 prefixN-101 N-101suffix N-101_suffix N-10suffix N-1001suffix", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var want []string
			for _, id := range tc.undefined {
				want = append(want, "guide.md references undefined backlog identifier "+id)
			}
			if problems := checkBacklogReferences("guide.md", tc.content, definitions); !slices.Equal(problems, want) {
				t.Fatalf("problems = %v, want %v", problems, want)
			}
		})
	}
}

func TestBacklogDefinitionExclusions(t *testing.T) {
	for _, id := range []string{"ADR-0017", "CVE-2026", "GO-2026", "SHA-256", "P-256", "ES-256", "RS-256", "OTHER-12", "N-1", "prefixN-101", "N-101suffix", "N-101_suffix"} {
		t.Run(id, func(t *testing.T) {
			definitions := make(map[string]string)
			content := "- [ ] **" + id + " - Not a task definition.**"
			if problems := indexBacklogTracker("todo/done.md", content, definitions); len(problems) != 0 || len(definitions) != 0 {
				t.Fatalf("unrelated identifier indexed: definitions=%v problems=%v", definitions, problems)
			}
		})
	}
}
