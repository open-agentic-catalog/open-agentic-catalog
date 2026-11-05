package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestValidationDiagnosticConfiguration(t *testing.T) {
	configuration := validTestConfig()
	if configuration.Validation != catalog.DefaultDiagnosticLimits() {
		t.Fatal("diagnostic defaults differ")
	}
	for _, input := range []catalog.DiagnosticLimits{
		{MaxFindings: 0, MaxDiagnosticBytes: 65536},
		{MaxFindings: -1, MaxDiagnosticBytes: 65536},
		{MaxFindings: 513, MaxDiagnosticBytes: 65536},
		{MaxFindings: 128, MaxDiagnosticBytes: 0},
		{MaxFindings: 128, MaxDiagnosticBytes: 1023},
		{MaxFindings: 128, MaxDiagnosticBytes: 262145},
	} {
		configuration.Validation = input
		if err := configuration.Validate(); err == nil || !strings.Contains(err.Error(), "validation") {
			t.Fatalf("invalid diagnostic policy %+v: %v", input, err)
		}
	}
	for _, input := range []catalog.DiagnosticLimits{{MaxFindings: 1, MaxDiagnosticBytes: 1024}, {MaxFindings: 512, MaxDiagnosticBytes: 262144}} {
		configuration.Validation = input
		if err := configuration.Validate(); err != nil {
			t.Fatalf("valid boundary %+v: %v", input, err)
		}
		raw, err := json.Marshal(configuration.Report())
		if err != nil {
			t.Fatal(err)
		}
		var report struct {
			Validation catalog.DiagnosticLimits `json:"validation"`
		}
		if err := json.Unmarshal(raw, &report); err != nil || report.Validation != input {
			t.Fatalf("effective report lost budgets: %s, %v", raw, err)
		}
	}
}

func TestValidationDiagnosticConfigurationLoading(t *testing.T) {
	clearRuntimeEnvironment(t)
	for _, input := range []string{
		`{"validation":{"maxFindings":0}}`,
		`{"validation":{"maxDiagnosticBytes":0}}`,
		`{"validation":{"maxFindings":null}}`,
		`{"validation":{"maxFindings":"32"}}`,
		`{"validation":{"maxFindings":32,"maxFindings":64}}`,
		`{"validation":{"unknown":1}}`,
	} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadFor(path, RoleMigration); err == nil {
			t.Fatalf("invalid diagnostic JSON accepted: %s", input)
		}
	}
	for _, name := range []string{"CATALOG_VALIDATION_MAX_FINDINGS", "CATALOG_VALIDATION_MAX_DIAGNOSTIC_BYTES"} {
		for _, value := range []string{"", "-1", "0", "262145", "abc"} {
			t.Run(name+"="+value, func(t *testing.T) {
				t.Setenv(name, value)
				if _, err := LoadFor("", RoleMigration); err == nil {
					t.Fatal("invalid diagnostic override accepted")
				}
			})
		}
	}
}
