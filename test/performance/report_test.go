// Unit tests exercise isolated validation and controlled dependencies.
package main

import (
	"context"
	"errors"
	"testing"
)

func TestPerformanceHTTPFailureNeverRetainsProblemText(t *testing.T) {
	for _, item := range []struct{ body, want string }{
		{`{"code":"INVALID_ARGUMENT","detail":"catalog cursor is stale"}`, "400-stale-generation"},
		{`{"code":"INVALID_ARGUMENT","detail":"live search cursor is invalid or expired; restart the search"}`, "400-invalid-live-cursor"},
		{`{"code":"INVALID_ARGUMENT","detail":"live search cursor is invalid or expired; restart the search","instance":"private-sentinel"}`, "400-invalid-live-cursor"},
		{`{"code":"FORBIDDEN","detail":"live search cursor is invalid or expired; restart the search"}`, "400"},
		{`{"code":"INVALID_ARGUMENT","detail":"live search cursor is invalid or expired; restart the search private-sentinel"}`, "400"},
		{`{"code":"INVALID_ARGUMENT","detail":"private-sentinel","detail":"live search cursor is invalid or expired; restart the search"}`, "400"},
		{`{"code":"INVALID_ARGUMENT","detail":"private document"}`, "400"},
		{`{"code":"INVALID_ARGUMENT","code":"INVALID_ARGUMENT","detail":"catalog cursor is stale"}`, "400"},
		{`not json`, "400"},
	} {
		if got := classifyHTTPFailure(400, []byte(item.body)); string(got) != item.want || got.Error() != "status" {
			t.Fatalf("classification=%q", string(got))
		}
	}
	if got := classifyHTTPFailure(503, []byte(`{"code":"INVALID_ARGUMENT","detail":"live search cursor is invalid or expired; restart the search"}`)); string(got) != "503" {
		t.Fatal("cursor detail changed a different HTTP status")
	}
}

func TestPerformanceTransportErrorClassification(t *testing.T) {
	for _, item := range []struct {
		err  error
		want requestError
	}{
		{context.Canceled, "canceled"}, {context.DeadlineExceeded, "timeout"}, {errors.New("private dial destination"), "transport"},
	} {
		if got := classifyRequest(item.err); got != item.want {
			t.Fatalf("classification=%s", got)
		}
	}
}
