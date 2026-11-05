// Probe response units use a controlled transport without a loopback listener.
package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestHealthProbeResponseAndCleanup(t *testing.T) {
	t.Parallel()
	readFailure := errors.New("fixture read failure")
	closeFailure := errors.New("fixture close failure")
	transportFailure := errors.New("fixture transport failure")
	for _, tc := range []struct {
		name                            string
		status, expected, size          int
		readErr, closeErr, transportErr error
		want                            string
		cause                           error
	}{
		{name: "live", status: 200, expected: 200, size: 2},
		{name: "not ready", status: 503, expected: 503, size: 4096},
		{name: "unexpected status", status: 503, expected: 200, size: 2, want: "health probe returned HTTP 503; expected 200"},
		{name: "oversized", status: 200, expected: 200, size: 8192, want: "health probe response exceeded 4 KiB"},
		{name: "read failure", status: 200, expected: 200, readErr: readFailure, want: "read health probe response: fixture read failure", cause: readFailure},
		{name: "close failure", status: 200, expected: 200, size: 2, closeErr: closeFailure, want: "close health probe response: fixture close failure", cause: closeFailure},
		{name: "read and close failure", status: 200, expected: 200, readErr: readFailure, closeErr: closeFailure, want: "read health probe response: fixture read failure", cause: readFailure},
		{name: "transport failure", transportErr: transportFailure, want: "health probe failed:", cause: transportFailure},
		{name: "deadline", transportErr: context.DeadlineExceeded, want: "health probe failed:", cause: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &catalogctlTrackedBody{Reader: strings.NewReader(strings.Repeat("x", tc.size)), closeErr: tc.closeErr}
			if tc.readErr != nil {
				body.Reader = catalogctlErrorReadCloser{err: tc.readErr}
			}
			calls := 0
			client := &http.Client{Transport: catalogctlRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Context() != t.Context() || r.Method != http.MethodGet || r.URL.String() != "http://[::1]:8081/readyz" || len(r.Header) != 0 || r.Body != nil {
					t.Error("probe request changed")
				}
				if tc.transportErr != nil {
					return nil, tc.transportErr
				}
				return &http.Response{StatusCode: tc.status, Body: body}, nil
			})}
			err := requestHealthProbe(t.Context(), client, "http://[::1]:8081/readyz", tc.expected)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.HasPrefix(err.Error(), tc.want) {
				t.Fatalf("error=%v want=%s", err, tc.want)
			}
			if tc.cause != nil && !errors.Is(err, tc.cause) {
				t.Fatalf("lost cause: %v", err)
			}
			wantCloses := 1
			if tc.transportErr != nil {
				wantCloses = 0
			}
			if calls != 1 || body.closes != wantCloses || body.bytesRead > 4097 {
				t.Fatalf("calls=%d closes=%d read=%d", calls, body.closes, body.bytesRead)
			}
			if tc.name == "oversized" && body.bytesRead != 4097 {
				t.Fatal("oversize boundary was not consumed exactly")
			}
		})
	}
}
