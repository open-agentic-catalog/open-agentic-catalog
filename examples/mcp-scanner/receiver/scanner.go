package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

type scannerIdentity struct {
	Version             string `json:"version"`
	ImageID             string `json:"imageId"`
	RulesDigest         string `json:"rulesSha256"`
	ConfigurationDigest string `json:"configurationSha256"`
}

func (i scannerIdentity) valid() bool {
	return i.Version != "" && len(i.Version) <= 64 && len(i.ImageID) == 71 && digestPattern.MatchString(i.ImageID) && hashPattern.MatchString(i.RulesDigest) && hashPattern.MatchString(i.ConfigurationDigest)
}
func (i scannerIdentity) key() string {
	return sha([]byte(i.Version + "\x00" + i.ImageID + "\x00" + i.RulesDigest + "\x00" + i.ConfigurationDigest))
}

type scanner interface {
	Identity(context.Context) (scannerIdentity, error)
	Scan(context.Context, capture, scannerIdentity) (json.RawMessage, error)
}

type pythonScanner struct{ python, runner string }
type boundedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		b.overflow = true
		return 0, errors.New("scanner_output_limit")
	}
	return b.Buffer.Write(p)
}

func (s *pythonScanner) command(ctx context.Context, args ...string) ([]byte, int, error) {
	// #nosec G204 G702 -- Both executable paths are explicit operator inputs.
	// Only fixed subcommands and invocation-owned input paths are appended.
	cmd := exec.CommandContext(ctx, s.python, append([]string{"-I", "-B", s.runner}, args...)...)
	cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "PYTHONDONTWRITEBYTECODE=1", "PYTHONNOUSERSITE=1", "LANG=C.UTF-8"}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = time.Minute
	out := &boundedBuffer{limit: reportLimit}
	diagnostics := &boundedBuffer{limit: 64 << 10}
	cmd.Stdout = out
	cmd.Stderr = diagnostics
	err := cmd.Run()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return nil, 2, failed("scanner_process_unavailable", true)
		}
		code = exit.ExitCode()
	}
	if out.overflow || diagnostics.overflow {
		return nil, 2, failed("scanner_output_limit", false)
	}
	return out.Bytes(), code, nil
}

func (s *pythonScanner) Identity(ctx context.Context) (scannerIdentity, error) {
	raw, code, err := s.command(ctx, "identity")
	var i scannerIdentity
	if err != nil || code != 0 || uniqueJSON(raw) != nil || json.Unmarshal(raw, &i) != nil || !i.valid() {
		return i, failed("scanner_tools_unavailable", false)
	}
	return i, nil
}

func (s *pythonScanner) Scan(ctx context.Context, c capture, identity scannerIdentity) (json.RawMessage, error) {
	dir, err := os.MkdirTemp("", "catalog-mcp-receiver-scan-")
	if err != nil {
		return nil, failed("scanner_input_failed", true)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	input, output := filepath.Join(dir, "capture.json"), filepath.Join(dir, "report.json")
	if err = os.WriteFile(input, c.Document, 0600); err != nil {
		return nil, failed("scanner_input_failed", true)
	}
	_, code, err := s.command(ctx, "scan", "--input", input, "--output", output, "--source", c.Source, "--captured-at", c.CapturedAt.Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	raw, err := privateFile(output, reportLimit)
	if err != nil {
		return nil, failed("scanner_report_missing", false)
	}
	if err = validateScan(raw, c, identity, code); err != nil {
		return nil, err
	}
	return raw, nil
}

func validateScan(raw []byte, c capture, i scannerIdentity, code int) error {
	if len(raw) > reportLimit || uniqueJSON(raw) != nil {
		return failed("invalid_scanner_report", false)
	}
	var r struct {
		SchemaVersion int    `json:"schemaVersion"`
		Status        string `json:"status"`
		Capture       struct {
			Input  string `json:"inputSha256"`
			Source string `json:"source"`
			At     string `json:"capturedAt"`
			Next   *bool  `json:"hasNextCursor"`
			Scope  string `json:"scope"`
		} `json:"capture"`
		Scanner struct {
			Version string `json:"version"`
			Image   string `json:"imageId"`
		} `json:"scanner"`
		Rules     string          `json:"rulesSha256"`
		Config    string          `json:"configurationSha256"`
		Context   string          `json:"contextSha256"`
		Caller    json.RawMessage `json:"callerContext"`
		Planned   *int            `json:"plannedTools"`
		Completed *int            `json:"completedTools"`
		Cleanup   string          `json:"cleanup"`
		Results   []struct {
			Tool      string            `json:"tool_name"`
			Status    string            `json:"status"`
			Analyzers []string          `json:"analyzers"`
			Errors    []json.RawMessage `json:"analyzer_errors"`
			Findings  []json.RawMessage `json:"findings"`
		} `json:"results"`
	}
	var captureDocument struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if json.Unmarshal(c.Document, &captureDocument) != nil || json.Unmarshal(raw, &r) != nil {
		return failed("invalid_scanner_report", false)
	}
	var caller map[string]json.RawMessage
	if json.Unmarshal(r.Caller, &caller) != nil || caller == nil || len(caller) != 0 {
		return failed("scanner_context_mismatch", false)
	}
	if r.SchemaVersion != 1 || r.Capture.Input != c.Digest || r.Capture.Source != c.Source || r.Capture.At != c.CapturedAt.Format(time.RFC3339Nano) || r.Capture.Next == nil || *r.Capture.Next == c.Complete || r.Capture.Scope != "supplied_page" || r.Scanner.Version != i.Version || r.Scanner.Image != i.ImageID || r.Rules != i.RulesDigest || r.Config != i.ConfigurationDigest || r.Context != sha([]byte("{}")) || r.Planned == nil || *r.Planned != len(captureDocument.Tools) || r.Completed == nil || *r.Completed < 0 || *r.Completed > *r.Planned {
		return failed("scanner_identity_mismatch", false)
	}
	if r.Status != "completed" && r.Status != "partial" && r.Status != "failed" && r.Status != "skipped" || r.Cleanup != "passed" && r.Cleanup != "failed" {
		return failed("invalid_scanner_outcome", false)
	}
	if r.Status == "completed" && *r.Planned == 0 {
		return failed("empty_scanner_work", false)
	}
	if r.Results == nil {
		return failed("invalid_scanner_outcome", false)
	}
	wanted := map[string]bool{}
	for _, tool := range captureDocument.Tools {
		wanted[tool.Name] = true
	}
	completed, findings := 0, 0
	for _, item := range r.Results {
		if !wanted[item.Tool] {
			return failed("scanner_tool_mismatch", false)
		}
		delete(wanted, item.Tool)
		if item.Analyzers == nil || item.Errors == nil || item.Findings == nil {
			return failed("incomplete_scanner_report", false)
		}
		selected := len(item.Analyzers) == 1 && item.Analyzers[0] == "YARA"
		if len(item.Analyzers) > 0 && !selected {
			return failed("unselected_scanner_analyzer", false)
		}
		switch item.Status {
		case "completed":
			if !selected || len(item.Errors) > 0 {
				return failed("incomplete_scanner_report", false)
			}
			completed++
		case "partial":
			if !selected || len(item.Errors) == 0 {
				return failed("incomplete_scanner_report", false)
			}
		case "failed":
			if selected || len(item.Errors) == 0 {
				return failed("incomplete_scanner_report", false)
			}
		case "skipped":
			if selected || len(item.Errors) > 0 || len(item.Findings) > 0 {
				return failed("incomplete_scanner_report", false)
			}
		default:
			return failed("invalid_scanner_outcome", false)
		}
		for _, problem := range item.Errors {
			var value struct {
				Analyzer string  `json:"analyzer"`
				Content  string  `json:"content_type"`
				Message  *string `json:"message"`
			}
			if json.Unmarshal(problem, &value) != nil || value.Analyzer != "YARA" || value.Content != "description" && value.Content != "parameters" || value.Message == nil {
				return failed("invalid_scanner_error", false)
			}
		}
		for _, finding := range item.Findings {
			var value struct {
				Analyzer string  `json:"analyzer"`
				Severity string  `json:"severity"`
				Summary  *string `json:"summary"`
				Category *string `json:"threat_category"`
				Details  struct {
					Tool    string `json:"tool_name"`
					Content string `json:"content_type"`
				} `json:"details"`
			}
			if json.Unmarshal(finding, &value) != nil || value.Analyzer != "YARA" || value.Details.Tool != item.Tool || value.Details.Content != "description" && value.Details.Content != "parameters" || value.Summary == nil || value.Category == nil {
				return failed("invalid_scanner_finding", false)
			}
			switch value.Severity {
			case "HIGH", "MEDIUM", "LOW", "INFO", "SAFE", "UNKNOWN":
			default:
				return failed("invalid_scanner_finding", false)
			}
		}
		findings += len(item.Findings)
	}
	if len(r.Results) > 0 && completed != *r.Completed {
		return failed("incomplete_scanner_report", false)
	}
	if r.Status == "completed" || r.Status == "partial" {
		if r.Cleanup != "passed" || len(r.Results) != *r.Planned {
			return failed("incomplete_scanner_report", false)
		}
		if completed != *r.Completed {
			return failed("incomplete_scanner_report", false)
		}
		if r.Status == "completed" && (completed != *r.Planned || !c.Complete || findings == 0 && code != 0 || findings > 0 && code != 1) {
			return failed("scanner_exit_mismatch", false)
		}
	}
	if r.Status != "completed" && code != 2 {
		return failed("scanner_exit_mismatch", false)
	}
	return nil
}

var _ io.Writer = (*boundedBuffer)(nil)
