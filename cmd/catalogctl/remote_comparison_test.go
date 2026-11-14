package main

import (
	"net/url"
	"strings"
	"testing"
)

func TestRemoteSourceComparisonArguments(t *testing.T) {
	t.Parallel()
	const before = "00000000-0000-7000-8000-000000000001"
	const after = "00000000-0000-7000-8000-000000000002"
	base := []string{"source-compare", "--before-snapshot-id", before, "--after-snapshot-id", after}
	for _, extra := range [][]string{nil, {"--view", "all", "--limit", "100", "--cursor", "opaque+/=& café"}} {
		args := append(append([]string(nil), base...), extra...)
		options, err := parseRemote(args, remoteTestEnvironment("", ""))
		if err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(options.path)
		if err != nil || u.Path != "/api/v1/federation/snapshots/"+before+"/comparison" || u.Query().Get("otherSnapshotId") != after {
			t.Fatalf("selection changed: %s %v", options.path, err)
		}
		q := u.Query()
		if len(extra) == 0 {
			if q.Get("view") != "changes" || q.Get("limit") != "25" || q.Has("cursor") || len(q) != 3 {
				t.Fatal("incorrect defaults", q)
			}
		} else if q.Get("view") != "all" || q.Get("limit") != "100" || q.Get("cursor") != "opaque+/=& café" || len(q) != 4 {
			t.Fatal("query values changed", q)
		}
	}
	for _, extra := range [][]string{
		{"--before-snapshot-id", "private-invalid"}, {"--after-snapshot-id", ""},
		{"--view", "private-invalid"}, {"--limit", "0"}, {"--limit", "101"},
		{"--cursor", strings.Repeat("x", 2049)}, {"--cursor", "\xff"}, {"--id", before},
	} {
		_, err := parseRemote(append(append([]string(nil), base...), extra...), remoteTestEnvironment("", ""))
		if err == nil || strings.Contains(err.Error(), "private-invalid") {
			t.Fatalf("accepted or disclosed invalid arguments: %v", err)
		}
	}
	for _, args := range [][]string{{"source-compare"}, {"source-compare", "--before-snapshot-id", before}, {"source-compare", "--after-snapshot-id", after}} {
		if _, err := parseRemote(args, remoteTestEnvironment("", "")); err == nil {
			t.Fatal("missing selection accepted")
		}
	}
}
