//go:build functional

package main

import (
	"bytes"
	"net"
	"testing"
)

func TestNetworkProbeLiveAndClosedListener(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	address := listener.Addr().String()
	var output bytes.Buffer
	if err := networkProbe([]string{"tcp", address}, &output); err != nil || output.String() != "connected\n" {
		t.Fatalf("live control: %q, %v", output.String(), err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := networkProbe([]string{"tcp", address}, &output); err == nil || output.Len() != 0 {
		t.Fatal("closed listener was accepted as a policy denial")
	}
}
