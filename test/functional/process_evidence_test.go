package functional_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

// Retain only a bounded tail in memory. Callers export fixed classifications,
// never the captured records or a dependency's error text.
type processEvidenceWriter struct {
	mu   sync.Mutex
	tail []byte
}

func (w *processEvidenceWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	const limit = 65536
	w.tail = append(w.tail, p...)
	if len(w.tail) > limit {
		w.tail = append([]byte(nil), w.tail[len(w.tail)-limit:]...)
	}
	return len(p), nil
}

func (w *processEvidenceWriter) startupFailure() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for line := range bytes.SplitSeq(w.tail, []byte("\n")) {
		var v struct {
			Event string `json:"event"`
			Stage string `json:"stage"`
		}
		if jsondoc.Validate(line) == nil && json.Unmarshal(line, &v) == nil && v.Event == "process.failed" && v.Stage == "startup" {
			return true
		}
	}
	return false
}

func cachedPoolerLogin(err *pgconn.PgError) bool {
	return err != nil && strings.HasPrefix(err.Message, "server login has been failing, cached error:") && strings.HasSuffix(err.Message, "(server_login_retry)")
}

func processPostgresState(err *pgconn.PgError) string {
	if err != nil {
		switch err.Code {
		case "08P01", "28P01", "28000", "57P01", "57P03":
			return err.Code
		}
	}
	return "other"
}

func processAssignmentProblem(raw []byte) (string, bool) {
	var p struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	if jsondoc.Validate(raw) != nil || json.Unmarshal(raw, &p) != nil {
		return "unknown", false
	}
	switch p.Code {
	case "CONFLICT", "PRECONDITION_FAILED", "FORBIDDEN", "IDEMPOTENCY_KEY_REUSED":
		return p.Code, p.Code == "CONFLICT" && p.Detail == "assignment cannot be revoked"
	default:
		return "unknown", false
	}
}

func processRequestID(value string) string {
	if len(value) != 36 {
		return "unavailable"
	}
	for i, c := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return "unavailable"
			}
		} else if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return "unavailable"
		}
	}
	return value
}

func TestProcessAssignmentEvidenceRetainsOnlyFixedFields(t *testing.T) {
	for _, item := range []struct {
		raw, code string
		refused   bool
	}{
		{`{"code":"CONFLICT","detail":"assignment cannot be revoked","private":"private-sentinel"}`, "CONFLICT", true},
		{`{"code":"PRECONDITION_FAILED","detail":"private-sentinel"}`, "PRECONDITION_FAILED", false},
		{`{"code":"private-sentinel","detail":"private-sentinel"}`, "unknown", false},
		{`{"code":"CONFLICT","code":"CONFLICT"}`, "unknown", false},
		{`not JSON`, "unknown", false},
	} {
		code, refused := processAssignmentProblem([]byte(item.raw))
		if code != item.code || refused != item.refused {
			t.Fatal("unsafe or incorrect problem projection")
		}
	}
	const requestID = "00000000-0000-7000-8000-000000000001"
	if processRequestID(requestID) != requestID {
		t.Fatal("lost bounded request identifier")
	}
	for _, value := range []string{"private-sentinel", "", requestID + "\nprivate-sentinel", strings.Repeat("z", 36)} {
		if processRequestID(value) != "unavailable" {
			t.Fatal("retained an unsafe request identifier")
		}
	}
}

func TestProcessEvidenceIsBoundedAndRejectsMalformedRecords(t *testing.T) {
	var w processEvidenceWriter
	_, _ = w.Write([]byte(strings.Repeat("private-sentinel", 10000)))
	_, _ = w.Write([]byte("\n{\"event\":\"process.failed\",\"stage\":\"startup\",\"private\":\"private-sentinel\"}\n"))
	if len(w.tail) > 65536 || !w.startupFailure() {
		t.Fatal("bounded tail lost the terminal classification")
	}
	for _, raw := range []string{`{"event":"other","stage":"startup"}`, `{"event":"process.failed","stage":"shutdown"}`, `{"event":"other","event":"process.failed","stage":"startup"}`, `not JSON`} {
		var other processEvidenceWriter
		_, _ = other.Write([]byte(raw))
		if other.startupFailure() {
			t.Fatal("unrelated or malformed record classified as startup failure")
		}
	}
}

func TestCachedPoolerLoginClassification(t *testing.T) {
	for _, v := range []struct {
		message string
		want    bool
	}{
		{"server login has been failing, cached error: connect failed (server_login_retry)", true},
		{"password authentication failed for private-sentinel", false},
		{"server login has been failing, cached error: private-sentinel", false},
	} {
		if cachedPoolerLogin(&pgconn.PgError{Message: v.message}) != v.want {
			t.Fatal("incorrect fixed pooler classification")
		}
	}
	if cachedPoolerLogin(nil) {
		t.Fatal("nil error classified as cached failure")
	}
	if processPostgresState(&pgconn.PgError{Code: "private-sentinel"}) != "other" || processPostgresState(nil) != "other" || processPostgresState(&pgconn.PgError{Code: "08P01"}) != "08P01" {
		t.Fatal("unsafe or incorrect SQL-state projection")
	}
}
