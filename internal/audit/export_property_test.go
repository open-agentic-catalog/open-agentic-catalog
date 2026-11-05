package audit

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"testing"
	"time"

	"pgregory.net/rapid"
)

func propertyAuditJSON(t *rapid.T, value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal generated audit value: %v", err)
	}
	return raw
}

func TestPropertyAuditExport(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		from := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(rapid.IntRange(0, 86400).Draw(t, "time offset")) * time.Second)
		before := from.Add(100 * time.Microsecond)
		generatedAt := before.Add(time.Duration(rapid.IntRange(0, 100).Draw(t, "generation delay")) * time.Microsecond)
		query := Query{OccurredFrom: &from, OccurredBefore: &before}
		actor := fmt.Sprintf("abcdefab-0000-7000-8000-%012x", rapid.Uint64Range(1, 65535).Draw(t, "actor"))
		target := fmt.Sprintf("00000000-0000-7000-8000-%012x", rapid.Uint64Range(1, 65535).Draw(t, "target"))
		request := rapid.StringMatching(`[a-z0-9]{1,16}`).Draw(t, "request")
		if rapid.Bool().Draw(t, "actor filter") {
			query.Actor = actor
		}
		if rapid.Bool().Draw(t, "action filter") {
			query.Action = "namespace.created"
		}
		if rapid.Bool().Draw(t, "request filter") {
			query.RequestID = request
		}
		if rapid.Bool().Draw(t, "target filter") {
			query.TargetType, query.TargetID = "namespace", target
		}
		count := rapid.IntRange(0, 20).Draw(t, "row count")
		records := make([]Record, 0, count)
		at := from
		for i := range count {
			// Zero increments create ties. Strictly increasing IDs break those ties;
			// microsecond steps keep normalization from collapsing distinct positions.
			at = at.Add(time.Duration(rapid.IntRange(0, 3).Draw(t, "time step")) * time.Microsecond)
			facts := map[string]any{
				"large":   json.Number(fmt.Sprint(int64(9007199254740993) + rapid.Int64Range(0, 999).Draw(t, "large integer"))),
				"decimal": json.Number(fmt.Sprintf("%d.%04d", rapid.IntRange(0, 99).Draw(t, "whole"), rapid.IntRange(0, 9999).Draw(t, "fraction"))),
				"label":   rapid.StringMatching(`[a-z]{1,12}`).Draw(t, "fact label"),
				"flag":    rapid.Bool().Draw(t, "fact flag"),
			}
			record := currentTestRecord(fmt.Sprintf("00000000-0000-7000-8000-%012x", i+1), at, facts)
			record.Actor, record.TargetID, record.RequestID = actor, target, request
			records = append(records, record)
		}
		// This model uses only the documented JSON encoding and SHA-256, not the
		// production record encoder, digest or manifest comparison helpers.
		events := propertyAuditJSON(t, records)
		sum := sha256.Sum256(events)
		want := Manifest{
			EncodingProfile: ExportEncodingProfile, TenantID: testTenant,
			Filters: Filters{Actor: query.Actor, Action: query.Action, RequestID: query.RequestID, TargetType: query.TargetType, TargetID: query.TargetID, OccurredFrom: from, OccurredBefore: before},
			Cutoff:  before, GeneratedAt: generatedAt, RowCount: count, EventsDigest: fmt.Sprintf("sha256:%x", sum),
		}
		if count > 0 {
			want.First = &Position{OccurredAt: records[0].At, ID: records[0].ID}
			want.Last = &Position{OccurredAt: records[count-1].At, ID: records[count-1].ID}
		}
		exported, raw, err := BuildExport(testTenant, query, before, generatedAt, records)
		if err != nil || exported.FormatVersion != ExportFormatV1 || !reflect.DeepEqual(exported.Manifest, want) || !bytes.Equal(exported.RawEvents, events) {
			t.Fatalf("export differs from input model: %+v, %v; want %+v", exported, err, want)
		}
		_, repeated, err := BuildExport(testTenant, query, before, generatedAt, records)
		if err != nil || !bytes.Equal(raw, repeated) {
			t.Fatalf("export encoding is not deterministic: %v", err)
		}
		verified, report, err := VerifyExport(raw)
		if err != nil || !report.Valid || report.Mode != "file" || report.RowCount != count || report.EventsDigest != want.EventsDigest || !reflect.DeepEqual(verified, exported) {
			t.Fatalf("verify model: %+v, %+v, %v", verified, report, err)
		}
		if count == 0 {
			return
		}
		// Changing a retained fact remains valid current-format data. Its new export
		// must verify, but pairing it with the original manifest must fail.
		changed := slices.Clone(records)
		changed[0].Details = maps.Clone(records[0].Details)
		originalFacts, ok := records[0].Details["facts"].(map[string]any)
		if !ok {
			t.Fatal("generated record lost its facts")
		}
		facts := maps.Clone(originalFacts)
		label, ok := facts["label"].(string)
		if !ok {
			t.Fatal("generated record lost its label")
		}
		facts["label"] = label + "x"
		changed[0].Details["facts"] = facts
		rebuilt, changedRaw, err := BuildExport(testTenant, query, before, generatedAt, changed)
		if err != nil {
			t.Fatalf("tampered fact is not structurally valid: %v", err)
		}
		if _, _, err := VerifyExport(changedRaw); err != nil {
			t.Fatalf("valid changed export did not verify: %v", err)
		}
		stale := exported
		stale.RawEvents = rebuilt.RawEvents
		if _, _, err := VerifyExport(propertyAuditJSON(t, stale)); err == nil {
			t.Fatal("changed facts accepted with original manifest")
		}
		reject := func(label string, q Query, bad []Record) {
			if _, _, err := BuildExport(testTenant, q, before, generatedAt, bad); err == nil {
				t.Fatalf("built invalid %s selection", label)
			}
			wire := exported
			wire.RawEvents = propertyAuditJSON(t, bad)
			wire.Manifest.Filters = Filters{Actor: q.Actor, Action: q.Action, RequestID: q.RequestID, TargetType: q.TargetType, TargetID: q.TargetID, OccurredFrom: *q.OccurredFrom, OccurredBefore: *q.OccurredBefore}
			if _, _, err := VerifyExport(propertyAuditJSON(t, wire)); err == nil {
				t.Fatalf("verified invalid %s selection", label)
			}
		}
		bad := slices.Clone(records)
		bad[0].TenantID = "00000000-0000-7000-8000-000000000099"
		reject("tenant", query, bad)
		bad = slices.Clone(records)
		bad[0].At = from.Add(-time.Microsecond)
		reject("before range", query, bad)
		bad = slices.Clone(records)
		bad[count-1].At = before
		reject("exclusive upper bound", query, bad)
		bad = slices.Clone(records)
		if count > 1 {
			bad[0], bad[1] = bad[1], bad[0]
		} else {
			bad = append(bad, bad[0])
		}
		reject("order", query, bad)
		for _, filter := range []string{"actor", "action", "request", "target type", "target ID"} {
			mismatch := query
			switch filter {
			case "actor":
				mismatch.Actor = "different-actor"
			case "action":
				mismatch.Action = "namespace.updated"
			case "request":
				mismatch.RequestID = request + "x"
			case "target type":
				mismatch.TargetType, mismatch.TargetID = "resource", target
			case "target ID":
				mismatch.TargetType, mismatch.TargetID = "namespace", "abcdefab-0000-7000-8000-000000000099"
			}
			reject(filter, mismatch, records)
		}
	})
}
