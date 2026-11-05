package buildinfo

import (
	"strings"
	"testing"
)

func TestBoundedIdentity(t *testing.T) {
	for _, state := range []string{"clean", "modified", "unknown", "", "private\nvalue"} {
		want := state
		if state == "" || state == "private\nvalue" {
			want = "unknown"
		}
		got := New("0.1.0-rc.1.dev+gabcdef", strings.Repeat("a", 40), state)
		if got.SourceState != want || got.Version != "0.1.0-rc.1.dev+gabcdef" || got.Commit != strings.Repeat("a", 40) {
			t.Fatalf("unexpected identity: %+v", got)
		}
	}
	for _, bad := range []string{"", "secret\nvalue", strings.Repeat("x", 129), "$(id)", "x/../file"} {
		if got := New(bad, bad, "unknown"); got.Version != "dev" || got.Commit != "unknown" {
			t.Fatalf("unbounded identity admitted: %+v", got)
		}
	}
	if got := New(strings.Repeat("x", 128), "fixture", "unknown"); len(got.Version) != 128 {
		t.Fatal("valid upper boundary rejected")
	}
}
