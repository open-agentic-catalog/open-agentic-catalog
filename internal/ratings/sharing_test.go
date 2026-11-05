package ratings

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

func TestSummaryRequiresBoundedCompletePublisherClaim(t *testing.T) {
	value := Summary{Reporter: "https://peer.example", Identifier: admission().Target.Identifier, VersionPresent: true, Version: "1.0", Minimum: 1, Maximum: 5, Count: 5, Sum: 19, MeasuredAt: time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	decode := func(raw []byte) *Summary { return DecodeSummary(raw, value.Reporter, value.Identifier, true, "1.0") }
	if decoded := decode(raw); decoded == nil || decoded.Average() != 3.8 {
		t.Fatal("valid summary rejected", string(raw))
	}
	for name, change := range map[string]func(map[string]any){
		"missing presence":    func(m map[string]any) { delete(m, "versionPresent") },
		"null presence":       func(m map[string]any) { m["versionPresent"] = nil },
		"unknown":             func(m map[string]any) { m["voterIds"] = []string{"private"} },
		"wrong-case alias":    func(m map[string]any) { m["Count"] = m["count"] },
		"different publisher": func(m map[string]any) { m["reporter"] = "https://other.example" },
		"different AIR":       func(m map[string]any) { m["identifier"] = "urn:air:peer.example:demo:other" },
		"different version":   func(m map[string]any) { m["version"] = "2" },
		"empty count":         func(m map[string]any) { m["count"] = 0 },
		"fractional count":    func(m map[string]any) { m["count"] = 5.5 },
		"sum below scale":     func(m map[string]any) { m["sum"] = 4 },
		"sum above scale":     func(m map[string]any) { m["sum"] = 26 },
		"unsafe integer":      func(m map[string]any) { m["count"] = 9007199254740992; m["sum"] = 9007199254740992 },
		"wrong scale":         func(m map[string]any) { m["maximum"] = 10 },
		"offset time":         func(m map[string]any) { m["measuredAt"] = "2026-10-08T01:00:00+01:00" },
		"missing time":        func(m map[string]any) { delete(m, "measuredAt") },
	} {
		t.Run(name, func(t *testing.T) {
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			change(m)
			bad, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			if decode(bad) != nil {
				t.Fatalf("accepted %s", bad)
			}
		})
	}
	for _, bad := range []string{"null", "[]", "{", string(raw) + ` {}`, strings.Replace(string(raw), `"count":5`, `"count":5,"count":5`, 1), strings.Repeat(" ", 2049) + string(raw)} {
		if decode([]byte(bad)) != nil {
			t.Fatalf("accepted malformed summary %q", bad)
		}
	}
	if DecodeSummary(raw, value.Reporter, value.Identifier, false, "") != nil {
		t.Fatal("versioned claim attached to unversioned target")
	}
}

func TestRatingTransitionsSurviveEarlierReplicaClock(t *testing.T) {
	service, store, _, authority, ctx := fixture(t)
	first, err := service.Submit(ctx, admission(), 4, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(store, authority, clock.NewFake(first.UpdatedAt.Add(-time.Hour)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	edited, err := second.Submit(ctx, admission(), 5, nil, first.Revision)
	if err != nil || edited.UpdatedAt.Before(first.UpdatedAt) {
		t.Fatal("replica time regression", err)
	}
	withdrawn, err := second.Change(ctx, edited.ID, edited.Revision, "withdraw", "")
	if err != nil || withdrawn.UpdatedAt.Before(edited.UpdatedAt) {
		t.Fatal("withdrawal time regression", err)
	}
	_, err = second.Change(ctx, withdrawn.ID, withdrawn.Revision, "withdraw", "")
	code(t, err, catalog.CodeConflict)
}

func TestTargetValidationKeepsCanonicalIdentityBoundaries(t *testing.T) {
	good := admission().Target
	for name, change := range map[string]func(*Target){
		"bad AIR": func(v *Target) { v.Identifier = "not-an-air" }, "missing local resource": func(v *Target) { v.ResourceID = "" },
		"local foreign mix": func(v *Target) { v.SourceID = resource }, "local absent version": func(v *Target) { v.VersionPresent = false },
		"unknown kind": func(v *Target) { v.Kind = "unknown" }, "bad version": func(v *Target) { v.Version = "\x00" },
		"oversized version": func(v *Target) { v.Version = strings.Repeat("a", 129) }, "origin": func(v *Target) { v.Origin = "other" },
	} {
		t.Run(name, func(t *testing.T) { v := good; change(&v); code(t, ValidateTarget(v), catalog.CodeInvalidArgument) })
	}
	if _, err := New(nil, nil, nil, nil); err == nil {
		t.Fatal("missing dependencies accepted")
	}
}
