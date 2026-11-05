//go:build functional

// Remote reindex tests keep mutation-specific validation separate from read-only commands.
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRemoteReindexCommand verifies explicit reason/key, one POST and unchanged Job output.
func TestRemoteReindexCommand(t *testing.T) {
	const snapshot = "00000000-0000-7000-8000-000000000099"
	for _, public := range []bool{false, true} {
		t.Run(map[bool]string{false: "control", true: "public"}[public], func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/catalog:reindex" || r.Header.Get("Idempotency-Key") != "reindex-test-key" || r.Header.Get("Authorization") != "Bearer test-token" {
					t.Error("incorrect command binding")
				}
				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["reason"] != "Rebuild derived search" || public && body["snapshotId"] != snapshot || !public && len(body) != 1 {
					t.Errorf("body=%v", body)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusAccepted)
				_, _ = io.WriteString(w, `{"id":"`+snapshot+`","kind":"semantic.reindex","state":"queued"}`)
			}))
			defer server.Close()
			args := []string{"reindex", "--reason", "Rebuild derived search", "--idempotency-key", "reindex-test-key"}
			if public {
				args = append(args, "--snapshot-id", snapshot)
			}
			var output bytes.Buffer
			if err := runRemote(t.Context(), args, remoteTestEnvironment(server.URL, "test-token"), &output); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || !strings.Contains(output.String(), `"kind":"semantic.reindex"`) {
				t.Fatalf("calls=%d output=%s", calls, &output)
			}
		})
	}
	for _, args := range [][]string{
		{"reindex"}, {"reindex", "--reason", "reason"}, {"reindex", "--reason", " ", "--idempotency-key", "valid-key"},
		{"reindex", "--reason", "reason", "--idempotency-key", "bad key"},
		{"reindex", "--reason", "reason", "--idempotency-key", "valid-key", "--snapshot-id", "not-a-uuid"},
		{"search", "--reason", "not-a-read"},
	} {
		if _, err := parseRemote(args, remoteTestEnvironment("", "")); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
}
