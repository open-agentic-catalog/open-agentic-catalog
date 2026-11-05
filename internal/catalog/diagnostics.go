// Diagnostic budgets bound newly generated validation findings independently
// of retained documents, immutable evidence reads and command replay.
package catalog

import "encoding/json"

const (
	MaxValidationFindings        = 512
	MaxValidationDiagnosticBytes = 256 << 10
	MaxFindingPathBytes          = 1024
	MaxFindingMessageBytes       = 2048
	DiagnosticBudgetExhausted    = "VALIDATION_DIAGNOSTICS_EXHAUSTED"
)

// DiagnosticLimits apply to each representation and separately to the complete
// revision and evidence finding arrays. Encoded budgets include array syntax.
type DiagnosticLimits struct {
	MaxFindings        int `json:"maxFindings"`
	MaxDiagnosticBytes int `json:"maxDiagnosticBytes"`
}

// DefaultDiagnosticLimits allow useful reports without input-driven growth.
func DefaultDiagnosticLimits() DiagnosticLimits {
	return DiagnosticLimits{MaxFindings: 128, MaxDiagnosticBytes: 64 << 10}
}

// Validate rejects budgets which cannot retain an explicit exhaustion finding.
func (l DiagnosticLimits) Validate() error {
	if l.MaxFindings < 1 || l.MaxFindings > MaxValidationFindings || l.MaxDiagnosticBytes < 1024 || l.MaxDiagnosticBytes > MaxValidationDiagnosticBytes {
		return E(CodeInvalidArgument, "validation budgets require 1-512 findings and 1024-262144 diagnostic bytes")
	}
	return nil
}

// FindingCollector stops accepting diagnostics after exhaustion. It never
// returns a truncated successful report or modifies an input finding.
type FindingCollector struct {
	limits    DiagnosticLimits
	findings  []Finding
	sizes     []int
	bytes     int
	exhausted bool
}

// NewFindingCollector creates an empty bounded report. Invalid limits fail
// closed; configuration and service construction report them before validation.
func NewFindingCollector(limits DiagnosticLimits) *FindingCollector {
	c := &FindingCollector{limits: limits, bytes: 2, findings: []Finding{}}
	if limits.Validate() != nil {
		c.limits = DefaultDiagnosticLimits()
		c.exhaust()
	}
	return c
}

// Add retains one complete finding or an explicit error marking exhaustion.
// False means the caller can stop constructing further diagnostics.
func (c *FindingCollector) Add(f Finding) bool {
	if c.exhausted {
		return false
	}
	if f.Code == DiagnosticBudgetExhausted || len(f.Code) > 128 || len(f.Path) > MaxFindingPathBytes || len(f.Message) > MaxFindingMessageBytes {
		c.exhaust()
		return false
	}
	raw, err := json.Marshal(f)
	if err != nil {
		c.exhaust()
		return false
	}
	cost := len(raw)
	if len(c.findings) > 0 {
		cost++
	}
	if len(c.findings) >= c.limits.MaxFindings || cost > c.limits.MaxDiagnosticBytes-c.bytes {
		c.exhaust()
		return false
	}
	c.findings = append(c.findings, f)
	c.sizes = append(c.sizes, cost)
	c.bytes += cost
	return true
}

// AddAll adds a bounded prefix of an existing report without copying its tail.
func (c *FindingCollector) AddAll(prefix string, findings []Finding) {
	for _, f := range findings {
		if c.exhausted {
			return
		}
		if len(prefix)+len(f.Path) > MaxFindingPathBytes {
			c.exhaust()
			return
		}
		f.Path = prefix + f.Path
		if !c.Add(f) {
			return
		}
	}
}

// Findings returns the completed report; callers must stop adding before use.
func (c *FindingCollector) Findings() []Finding { return c.findings }

// Exhausted reports whether further validation diagnostics would be incomplete.
func (c *FindingCollector) Exhausted() bool { return c.exhausted }

func (c *FindingCollector) exhaust() {
	f := Finding{Code: DiagnosticBudgetExhausted, Severity: SeverityError, Message: "Validation diagnostics exceed the configured count, byte or field limit; validation is incomplete."}
	raw, _ := json.Marshal(f) //nolint:errchkjson // Fixed string-only marker has no fallible marshaler.
	for len(c.findings) > 0 && (len(c.findings) >= c.limits.MaxFindings || c.bytes+len(raw)+1 > c.limits.MaxDiagnosticBytes) {
		last := len(c.findings) - 1
		c.bytes -= c.sizes[last]
		c.findings = c.findings[:last]
		c.sizes = c.sizes[:last]
	}
	c.findings = append(c.findings, f)
	c.exhausted = true
}
