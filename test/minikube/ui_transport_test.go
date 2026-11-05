//go:build minikube

// Login diagnostics identify transport failures without exposing private request values.
package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"testing"
)

func TestUILoginTransportFailureKeepsOnlySafeCategory(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{"canceled", context.Canceled},
		{"deadline", context.DeadlineExceeded},
		{"timeout", &net.DNSError{Err: "private-provider-message", Name: "private-host", IsTimeout: true}},
		{"unexpected_eof", io.ErrUnexpectedEOF},
		{"eof", io.EOF},
		{"closed", net.ErrClosed},
		{"transport", errors.New("private-cookie-value")},
	} {
		t.Run(test.name, func(t *testing.T) {
			wrapped := &url.Error{Op: "Post", URL: "https://private-host/login?token=private-token", Err: errors.Join(test.err, errors.New("private-session-value"))}
			want := "packaged UI development login transport failed (" + test.name + ")"
			if got := uiLoginTransportFailure(wrapped).Error(); got != want {
				t.Fatalf("diagnostic must contain only the fixed category; got %q", got)
			}
		})
	}
}
