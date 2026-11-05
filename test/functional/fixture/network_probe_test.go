package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"syscall"
	"testing"
)

func TestNetworkProbeOutcome(t *testing.T) {
	for _, test := range []struct {
		err   error
		want  string
		valid bool
	}{
		{nil, "connected", true}, {context.DeadlineExceeded, "timeout", true},
		{fmt.Errorf("wrapped: %w", context.DeadlineExceeded), "timeout", true},
		{syscall.ECONNREFUSED, "", false}, {context.Canceled, "", false}, {errors.New("private sentinel"), "", false},
	} {
		got, err := networkProbeOutcome(test.err)
		if got != test.want || (err == nil) != test.valid {
			t.Fatalf("unexpected result: %q, %v", got, err)
		}
	}
}

func TestNetworkProbeRejectsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"tcp"}, {"unknown", "host"}, {"tcp", "example.com:80"}, {"tcp", "127.0.0.1:0"}, {"tcp", "invalid"}} {
		var output bytes.Buffer
		if err := networkProbe(args, &output); err == nil || output.Len() != 0 {
			t.Fatalf("invalid probe accepted: %v", args)
		}
	}
}
