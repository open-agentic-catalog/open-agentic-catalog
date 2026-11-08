package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

func scannerUnitFinding() map[string]any {
	return map[string]any{"analyzer": "YARA", "severity": "HIGH", "summary": "Synthetic finding", "threat_category": "tool_poisoning", "details": map[string]any{"tool_name": "sum", "content_type": "description"}}
}

func scannerUnitError() map[string]any {
	return map[string]any{"analyzer": "YARA", "content_type": "description", "message": "Synthetic analyzer refusal"}
}

func scannerUnitOnlyObject(t *testing.T, value any) map[string]any {
	t.Helper()
	items, ok := value.([]any)
	if !ok || len(items) != 1 {
		t.Fatal("fixture must contain exactly one entry", value)
	}
	object, ok := items[0].(map[string]any)
	if !ok {
		t.Fatal("fixture entry must be an object", items)
	}
	return object
}

// Each outcome has a valid, independently checked tool result before a test
// changes one condition. No scanner process or captured endpoint is contacted.
func scannerUnitReport(t *testing.T, outcome string) (capture, map[string]any, map[string]any, int) {
	t.Helper()
	c := captureFixture()
	if outcome == "partial" {
		c.Complete = false
		c.Document = json.RawMessage(`{"tools":[{"name":"sum","description":"Add two integers.","inputSchema":{"type":"object","properties":{}}}],"nextCursor":"next-page"}`)
		c.Digest = sha(c.Document)
	}
	var report map[string]any
	if err := json.Unmarshal(reportFixture(c, testIdentity), &report); err != nil {
		t.Fatal(err)
	}
	results, ok := report["results"].([]any)
	if !ok || len(results) != 1 {
		t.Fatal("fixture must contain exactly one tool result", report)
	}
	tool, ok := results[0].(map[string]any)
	if !ok {
		t.Fatal("fixture must contain an object result", results)
	}
	code := 2
	switch outcome {
	case "completed":
		code = 0
	case "finding":
		tool["findings"] = []any{scannerUnitFinding()}
		code = 1
	case "partial":
		report["completedTools"] = 0
		tool["status"], tool["analyzer_errors"] = "partial", []any{scannerUnitError()}
	case "failed":
		report["status"], report["completedTools"] = "failed", 0
		tool["status"], tool["analyzers"], tool["analyzer_errors"] = "failed", []any{}, []any{scannerUnitError()}
	case "skipped":
		report["status"], report["completedTools"] = "skipped", 0
		tool["status"], tool["analyzers"] = "skipped", []any{}
	default:
		t.Fatal("unknown unit fixture outcome", outcome)
	}
	if err := validateScan(fixtureJSON(report), c, testIdentity, code); err != nil {
		t.Fatal("valid outcome control", outcome, err)
	}
	return c, report, tool, code
}

func requireScannerUnitFailure(t *testing.T, err error, code string) {
	t.Helper()
	var result *failure
	if !errors.As(err, &result) || result.code != code || result.transient {
		t.Fatalf("want permanent %s, got %#v (%v)", code, result, err)
	}
}

func TestScannerAcceptsAttributedToolOutcomes(t *testing.T) {
	for _, outcome := range []string{"completed", "finding", "partial", "failed", "skipped"} {
		t.Run(outcome, func(t *testing.T) { _, _, _, _ = scannerUnitReport(t, outcome) })
	}
	for _, severity := range []string{"HIGH", "MEDIUM", "LOW", "INFO", "SAFE", "UNKNOWN"} {
		t.Run(severity, func(t *testing.T) {
			c, report, tool, code := scannerUnitReport(t, "finding")
			findings, ok := tool["findings"].([]any)
			if !ok || len(findings) != 1 {
				t.Fatal("fixture must contain one finding", tool)
			}
			finding, ok := findings[0].(map[string]any)
			if !ok {
				t.Fatal("fixture finding must be an object", findings)
			}
			finding["severity"] = severity
			details, ok := finding["details"].(map[string]any)
			if !ok {
				t.Fatal("fixture finding must have attributed details", finding)
			}
			details["content_type"] = "parameters"
			if err := validateScan(fixtureJSON(report), c, testIdentity, code); err != nil {
				t.Fatal("supported severity and parameter attribution refused", err)
			}
		})
	}
	t.Run("parameter analyzer error", func(t *testing.T) {
		c, report, tool, code := scannerUnitReport(t, "partial")
		scannerUnitOnlyObject(t, tool["analyzer_errors"])["content_type"] = "parameters"
		if err := validateScan(fixtureJSON(report), c, testIdentity, code); err != nil {
			t.Fatal("supported parameter error attribution refused", err)
		}
	})
}

func TestScannerRejectsInconsistentToolOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, outcome, failure string
		change                 func(map[string]any, map[string]any)
	}{
		{"unknown report status", "completed", "invalid_scanner_outcome", func(r, _ map[string]any) { r["status"] = "pending" }},
		{"unknown cleanup", "completed", "invalid_scanner_outcome", func(r, _ map[string]any) { r["cleanup"] = "unknown" }},
		{"failed cleanup", "completed", "incomplete_scanner_report", func(r, _ map[string]any) { r["cleanup"] = "failed" }},
		{"missing result collection", "completed", "invalid_scanner_outcome", func(r, _ map[string]any) { delete(r, "results") }},
		{"unknown tool", "completed", "scanner_tool_mismatch", func(_, tool map[string]any) { tool["tool_name"] = "other-tool" }},
		{"duplicate tool", "completed", "scanner_tool_mismatch", func(r, tool map[string]any) { r["results"] = []any{tool, tool} }},
		{"missing analyzers", "completed", "incomplete_scanner_report", func(_, tool map[string]any) { delete(tool, "analyzers") }},
		{"missing errors", "completed", "incomplete_scanner_report", func(_, tool map[string]any) { delete(tool, "analyzer_errors") }},
		{"missing findings", "completed", "incomplete_scanner_report", func(_, tool map[string]any) { delete(tool, "findings") }},
		{"unselected analyzer", "completed", "unselected_scanner_analyzer", func(_, tool map[string]any) { tool["analyzers"] = []any{"OTHER"} }},
		{"additional analyzer", "completed", "unselected_scanner_analyzer", func(_, tool map[string]any) { tool["analyzers"] = []any{"YARA", "OTHER"} }},
		{"unknown tool outcome", "completed", "invalid_scanner_outcome", func(_, tool map[string]any) { tool["status"] = "pending" }},
		{"completed without analyzer", "completed", "incomplete_scanner_report", func(_, tool map[string]any) { tool["analyzers"] = []any{} }},
		{"completed with error", "completed", "incomplete_scanner_report", func(_, tool map[string]any) { tool["analyzer_errors"] = []any{scannerUnitError()} }},
		{"partial without analyzer", "partial", "incomplete_scanner_report", func(_, tool map[string]any) { tool["analyzers"] = []any{} }},
		{"partial without error", "partial", "incomplete_scanner_report", func(_, tool map[string]any) { tool["analyzer_errors"] = []any{} }},
		{"failed with selected analyzer", "failed", "incomplete_scanner_report", func(_, tool map[string]any) { tool["analyzers"] = []any{"YARA"} }},
		{"failed without error", "failed", "incomplete_scanner_report", func(_, tool map[string]any) { tool["analyzer_errors"] = []any{} }},
		{"skipped with selected analyzer", "skipped", "incomplete_scanner_report", func(_, tool map[string]any) { tool["analyzers"] = []any{"YARA"} }},
		{"skipped with error", "skipped", "incomplete_scanner_report", func(_, tool map[string]any) { tool["analyzer_errors"] = []any{scannerUnitError()} }},
		{"skipped with finding", "skipped", "incomplete_scanner_report", func(_, tool map[string]any) { tool["findings"] = []any{scannerUnitFinding()} }},
		{"wrong completion count", "completed", "incomplete_scanner_report", func(r, _ map[string]any) { r["completedTools"] = 0 }},
		{"missing tool result", "completed", "incomplete_scanner_report", func(r, _ map[string]any) { r["results"], r["completedTools"] = []any{}, 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, report, tool, code := scannerUnitReport(t, tc.outcome)
			tc.change(report, tool)
			requireScannerUnitFailure(t, validateScan(fixtureJSON(report), c, testIdentity, code), tc.failure)
		})
	}
}

func TestScannerRequiresAttributedAnalyzerErrors(t *testing.T) {
	for name, change := range map[string]func(map[string]any){
		"unselected analyzer": func(e map[string]any) { e["analyzer"] = "OTHER" },
		"missing analyzer":    func(e map[string]any) { delete(e, "analyzer") },
		"unsupported content": func(e map[string]any) { e["content_type"] = "tool_result" },
		"missing content":     func(e map[string]any) { delete(e, "content_type") },
		"missing message":     func(e map[string]any) { delete(e, "message") },
		"null message":        func(e map[string]any) { e["message"] = nil },
		"wrong message type":  func(e map[string]any) { e["message"] = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			c, report, tool, code := scannerUnitReport(t, "partial")
			change(scannerUnitOnlyObject(t, tool["analyzer_errors"]))
			requireScannerUnitFailure(t, validateScan(fixtureJSON(report), c, testIdentity, code), "invalid_scanner_error")
		})
	}
}

func TestScannerRequiresAttributedFindings(t *testing.T) {
	for name, change := range map[string]func(map[string]any){
		"unselected analyzer":  func(f map[string]any) { f["analyzer"] = "OTHER" },
		"missing analyzer":     func(f map[string]any) { delete(f, "analyzer") },
		"unsupported severity": func(f map[string]any) { f["severity"] = "CRITICAL" },
		"missing severity":     func(f map[string]any) { delete(f, "severity") },
		"missing summary":      func(f map[string]any) { delete(f, "summary") },
		"null summary":         func(f map[string]any) { f["summary"] = nil },
		"wrong summary type":   func(f map[string]any) { f["summary"] = true },
		"missing category":     func(f map[string]any) { delete(f, "threat_category") },
		"missing details":      func(f map[string]any) { delete(f, "details") },
		"different tool": func(f map[string]any) {
			f["details"] = map[string]any{"tool_name": "other-tool", "content_type": "description"}
		},
		"missing tool": func(f map[string]any) { f["details"] = map[string]any{"content_type": "description"} },
		"unsupported content": func(f map[string]any) {
			f["details"] = map[string]any{"tool_name": "sum", "content_type": "tool_result"}
		},
		"missing content": func(f map[string]any) { f["details"] = map[string]any{"tool_name": "sum"} },
	} {
		t.Run(name, func(t *testing.T) {
			c, report, tool, code := scannerUnitReport(t, "finding")
			change(scannerUnitOnlyObject(t, tool["findings"]))
			requireScannerUnitFailure(t, validateScan(fixtureJSON(report), c, testIdentity, code), "invalid_scanner_finding")
		})
	}
}

func TestScannerOutcomeMustMatchExitStatus(t *testing.T) {
	for _, tc := range []struct {
		outcome string
		exit    int
	}{{"completed", 1}, {"finding", 0}, {"partial", 0}, {"failed", 1}, {"skipped", 0}} {
		t.Run(tc.outcome, func(t *testing.T) {
			c, report, _, _ := scannerUnitReport(t, tc.outcome)
			requireScannerUnitFailure(t, validateScan(fixtureJSON(report), c, testIdentity, tc.exit), "scanner_exit_mismatch")
		})
	}
}

func TestScannerRejectsMalformedReportDocuments(t *testing.T) {
	c, report, _, code := scannerUnitReport(t, "completed")
	valid := fixtureJSON(report)
	duplicate := append([]byte(`{"schemaVersion":1,`), valid[1:]...)
	if !json.Valid(duplicate) || len(duplicate) > reportLimit {
		t.Fatal("duplicate-key fixture must remain otherwise valid JSON within the byte limit")
	}
	atLimit := bytes.Clone(valid)
	atLimit = append(atLimit, bytes.Repeat([]byte(" "), reportLimit-len(atLimit))...)
	if err := validateScan(atLimit, c, testIdentity, code); err != nil {
		t.Fatal("valid report at the exact byte limit must be accepted", err)
	}
	oversized := bytes.Clone(atLimit)
	oversized = append(oversized, ' ')
	if !json.Valid(oversized) {
		t.Fatal("oversized control must remain valid JSON")
	}
	for name, raw := range map[string][]byte{
		"malformed JSON":    []byte(`{"schemaVersion":`),
		"duplicate field":   duplicate,
		"non-object report": []byte(`[]`),
		"oversized report":  oversized,
	} {
		t.Run(name, func(t *testing.T) {
			requireScannerUnitFailure(t, validateScan(raw, c, testIdentity, code), "invalid_scanner_report")
		})
	}
}
