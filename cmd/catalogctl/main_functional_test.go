//go:build functional

// Functional tests exercise real process, network or application composition boundaries.
package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoopbackHealthProbeIsBounded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/livez":
			writer.WriteHeader(http.StatusOK)
		case "/readyz":
			writer.WriteHeader(http.StatusServiceUnavailable)
		case "/startupz":
			_, _ = writer.Write([]byte(strings.Repeat("x", 4097)))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	if err := probe([]string{"--url", server.URL + "/livez"}); err != nil {
		t.Fatalf("live probe failed: %v", err)
	}
	if err := probe([]string{"--url", server.URL + "/readyz", "--expect-status", "503"}); err != nil {
		t.Fatalf("not-ready probe failed: %v", err)
	}
	if err := probe([]string{"--url", server.URL + "/readyz"}); err == nil {
		t.Fatal("probe accepted an unexpected status")
	}
	if err := probe([]string{"--url", server.URL + "/startupz"}); err == nil {
		t.Fatal("probe accepted an oversized response")
	}
	redirectServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, server.URL+"/livez", http.StatusTemporaryRedirect)
	}))
	defer redirectServer.Close()
	if err := probe([]string{"--url", redirectServer.URL + "/livez"}); err == nil {
		t.Fatal("probe followed a redirect")
	}
}

func TestLocalOperatorCommandsAndArgumentFailures(t *testing.T) {
	path := localOperatorTestConfig(t)

	for _, arguments := range [][]string{
		{"profiles", "verify"},
		{"config", "validate", "--config", path, "--role", "api"},
		{"diagnostics", "--config", path, "--role", "worker"},
		{"bootstrap", "--config", path, "--namespace", "coverage"},
	} {
		if err := runArgs(arguments); err != nil {
			t.Fatalf("runArgs(%v) returned %v", arguments, err)
		}
	}
}
