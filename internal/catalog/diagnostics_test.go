package catalog

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestFindingCollectorCountAndEncodedBoundary(t *testing.T) {
	limits := DiagnosticLimits{MaxFindings: 2, MaxDiagnosticBytes: 1024}
	first := Finding{Code: "WARNING", Severity: SeverityWarning, Message: "First warning"}
	report := NewFindingCollector(limits)
	for range 2 {
		if !report.Add(first) || report.Exhausted() {
			t.Fatal("exact finding count rejected")
		}
	}
	if report.Add(Finding{Code: "LATE_ERROR", Severity: SeverityError, Message: "must not disappear as success"}) {
		t.Fatal("over-limit finding accepted")
	}
	assertDiagnosticReport(t, report, limits, true)
	if report.Findings()[0] != first {
		t.Fatal("bounded prefix changed")
	}
	before := append([]Finding(nil), report.Findings()...)
	report.Add(first)
	if !reflect.DeepEqual(before, report.Findings()) {
		t.Fatal("exhausted report changed")
	}

	limits.MaxFindings = 128
	value := Finding{Code: "EXACT", Severity: SeverityInfo}
	base, err := json.Marshal([]Finding{value})
	if err != nil {
		t.Fatal(err)
	}
	// An empty message is still encoded: fill the remaining array budget exactly.
	value.Message = strings.Repeat("x", limits.MaxDiagnosticBytes-len(base))
	report = NewFindingCollector(limits)
	if !report.Add(value) {
		t.Fatal("exact byte boundary rejected")
	}
	raw, err := json.Marshal(report.Findings())
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != limits.MaxDiagnosticBytes {
		t.Fatalf("fixture bytes=%d", len(raw))
	}
	value.Message += "x"
	report = NewFindingCollector(limits)
	report.Add(value)
	assertDiagnosticReport(t, report, limits, true)

	report = NewFindingCollector(limits)
	value.Message = strings.Repeat("\x01", 128)
	if !report.Add(value) || report.Add(value) {
		t.Fatal("JSON escaping was not included in byte accounting")
	}
	assertDiagnosticReport(t, report, limits, true)
}

func TestFindingCollectorFieldsPrefixesAndInputOwnership(t *testing.T) {
	limits := DefaultDiagnosticLimits()
	for name, finding := range map[string]Finding{
		"path":                {Path: strings.Repeat("p", MaxFindingPathBytes+1)},
		"message":             {Message: strings.Repeat("m", MaxFindingMessageBytes+1)},
		"multibyte message":   {Message: strings.Repeat("\u754c", MaxFindingMessageBytes/3+1)},
		"code":                {Code: strings.Repeat("C", 129)},
		"upstream exhaustion": {Code: DiagnosticBudgetExhausted, Severity: SeverityWarning},
	} {
		t.Run(name, func(t *testing.T) {
			report := NewFindingCollector(limits)
			report.Add(finding)
			assertDiagnosticReport(t, report, limits, true)
		})
	}
	input := []Finding{{Code: "ORIGINAL", Severity: SeverityInfo, Path: strings.Repeat("x", MaxFindingPathBytes), Message: "unchanged"}}
	original := input[0]
	report := NewFindingCollector(limits)
	report.AddAll("", input)
	assertDiagnosticReport(t, report, limits, false)
	report = NewFindingCollector(limits)
	report.AddAll("/representations/15", input)
	assertDiagnosticReport(t, report, limits, true)
	if input[0] != original {
		t.Fatal("immutable input was changed")
	}
	for _, invalid := range []DiagnosticLimits{{}, {MaxFindings: 513, MaxDiagnosticBytes: 1024}, {MaxFindings: 1, MaxDiagnosticBytes: 1023}, {MaxFindings: -1, MaxDiagnosticBytes: 262145}} {
		if invalid.Validate() == nil {
			t.Fatalf("invalid budget accepted: %+v", invalid)
		}
		assertDiagnosticReport(t, NewFindingCollector(invalid), limits, true)
	}
}

func TestFindingCollectorEvictsEnoughTailForMarker(t *testing.T) {
	limits := DiagnosticLimits{MaxFindings: 128, MaxDiagnosticBytes: 1024}
	// Twenty short findings fit, but replacing only the last one would not
	// leave room for the explicit marker. Count alone does not exhaust this.
	finding := Finding{Code: "W", Severity: SeverityWarning}
	input := make([]Finding, 20)
	for i := range input {
		input[i] = finding
	}
	encoded, err := json.Marshal(input)
	if err != nil || len(encoded) > limits.MaxDiagnosticBytes {
		t.Fatalf("invalid byte-boundary fixture: bytes=%d error=%v", len(encoded), err)
	}
	report := NewFindingCollector(limits)
	report.AddAll("", input)
	assertDiagnosticReport(t, report, limits, false)
	if report.Add(Finding{Code: "ERROR", Severity: SeverityError, Message: strings.Repeat("x", 1024)}) {
		t.Fatal("over-budget late error accepted")
	}
	assertDiagnosticReport(t, report, limits, true)
	if len(report.Findings()) >= len(input) {
		t.Fatal("fixture did not require eviction of multiple findings")
	}
	for _, retained := range report.Findings()[:len(report.Findings())-1] {
		if retained != finding {
			t.Fatal("retained prefix changed")
		}
	}
	before := append([]Finding(nil), report.Findings()...)
	report.AddAll("/later", input)
	if !reflect.DeepEqual(before, report.Findings()) {
		t.Fatal("later aggregation changed the exhausted report")
	}
}

func TestFindingCollectorExactFieldAndPrefixBoundaries(t *testing.T) {
	limits := DefaultDiagnosticLimits()
	finding := Finding{Code: strings.Repeat("c", 128), Severity: SeverityWarning, Path: strings.Repeat("p", MaxFindingPathBytes), Message: strings.Repeat("\u754c", 682) + "ab"}
	report := NewFindingCollector(limits)
	if !report.Add(finding) || !reflect.DeepEqual(report.Findings(), []Finding{finding}) {
		t.Fatal("exact UTF-8 field limits were rejected or rewritten")
	}
	assertDiagnosticReport(t, report, limits, false)
	const prefix = "/representations/15"
	finding.Path = strings.Repeat("p", MaxFindingPathBytes-len(prefix))
	for _, suffix := range []string{"", "p"} {
		report = NewFindingCollector(limits)
		input := finding
		input.Path += suffix
		report.AddAll(prefix, []Finding{input})
		assertDiagnosticReport(t, report, limits, suffix != "")
		if suffix == "" && report.Findings()[0].Path != prefix+finding.Path {
			t.Fatal("exact aggregate path changed")
		}
	}
}

func assertDiagnosticReport(t *testing.T, report *FindingCollector, limits DiagnosticLimits, exhausted bool) {
	t.Helper()
	raw, err := json.Marshal(report.Findings())
	if err != nil || len(raw) > limits.MaxDiagnosticBytes || len(report.Findings()) > limits.MaxFindings || report.Exhausted() != exhausted {
		t.Fatalf("report count=%d bytes=%d exhausted=%t error=%v", len(report.Findings()), len(raw), report.Exhausted(), err)
	}
	markers := 0
	for _, f := range report.Findings() {
		if f.Code == DiagnosticBudgetExhausted {
			markers++
			if f.Severity != SeverityError {
				t.Fatal("exhaustion did not block validation")
			}
		}
	}
	if exhausted && markers != 1 || !exhausted && markers != 0 {
		t.Fatalf("exhaustion markers=%d", markers)
	}
}
