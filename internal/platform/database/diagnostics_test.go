package database

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestRetainedDiagnosticsDecodeWithoutNewAdmissionLimits(t *testing.T) {
	// Historical reads preserve even reports larger than the current hard
	// admission count. Only the SQL projection's exact sentinel denies a read.
	want := make([]catalog.Finding, 513)
	for i := range want {
		want[i] = catalog.Finding{Code: "LEGACY_WARNING", Severity: catalog.SeverityWarning, Message: "diagnostics_read_limit"}
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got []catalog.Finding
	if err := unmarshalRetainedFindings(raw, &got); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("historical findings changed: count=%d error=%v", len(got), err)
	}
	for _, tc := range []struct {
		raw  string
		code catalog.ErrorCode
	}{
		{`"diagnostics_read_limit"`, catalog.CodeWorkLimitExceeded},
		{`"different sentinel"`, catalog.CodeInternal},
		{`[{`, catalog.CodeInternal},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			var findings []catalog.Finding
			if err := unmarshalRetainedFindings([]byte(tc.raw), &findings); catalog.ErrorCodeOf(err) != tc.code {
				t.Fatalf("error=%v, want %s", err, tc.code)
			}
		})
	}
}
