//go:build functional

// Functional tests exercise real process, network or application composition boundaries.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
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
	} {
		if err := runArgs(arguments); err != nil {
			t.Fatalf("runArgs(%v) returned %v", arguments, err)
		}
	}
}

// TestBootstrapReportsCreatedDevelopmentState checks the command result, not
// merely successful composition, with a different tenant and publication domain.
func TestBootstrapReportsCreatedDevelopmentState(t *testing.T) {
	raw, err := os.ReadFile(localOperatorTestConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	var configuration config.Config
	if err := json.Unmarshal(raw, &configuration); err != nil {
		t.Fatal(err)
	}
	const tenant = "00000000-0000-7000-8000-000000000042"
	const domain = "bootstrap.publisher.test"
	configuration.Auth.DevelopmentTokens = map[string]config.DevelopmentUser{
		"bootstrap-admin": {Subject: "bootstrap-admin", TenantID: tenant, Roles: []identity.Role{identity.RoleTenantAdmin}},
	}
	configuration.Discovery.Hosts[0].TenantID = tenant
	configuration.Discovery.Hosts[0].PublisherDomain = domain
	// Keep this composed test independent of a caller's retained database inputs.
	t.Setenv("CATALOG_DATABASE_DRIVER", "memory")
	t.Setenv("CATALOG_AUTH_MODE", "development")
	output := captureCatalogctlStdout(t, func() error {
		return runArgs([]string{"bootstrap", "--config", writeCatalogctlTestConfig(t, configuration), "--namespace", "bootstrap-contract"})
	})
	var report struct {
		TenantID  string            `json:"tenantId"`
		Namespace catalog.Namespace `json:"namespace"`
		Publisher catalog.Publisher `json:"publisher"`
	}
	if err := json.Unmarshal(output, &report); err != nil {
		t.Fatalf("bootstrap did not emit one complete JSON report: %v", err)
	}
	if report.TenantID != tenant || report.Namespace.TenantID != tenant || report.Publisher.TenantID != tenant {
		t.Fatalf("bootstrap returned another tenant: %+v", report)
	}
	if !identity.ValidTenantID(report.Namespace.ID) || !identity.ValidTenantID(report.Publisher.ID) || report.Namespace.ID == report.Publisher.ID {
		t.Fatalf("bootstrap returned invalid or reused identities: %+v", report)
	}
	if report.Namespace.Slug != "bootstrap-contract" || report.Namespace.DisplayName != "Default" || report.Namespace.Revision != 1 {
		t.Fatalf("bootstrap ignored requested Namespace: %+v", report.Namespace)
	}
	if report.Publisher.DisplayName != "Example Publisher" || report.Publisher.Revision != 2 || !slices.Equal(report.Publisher.Domains, []string{domain}) || !slices.Equal(report.Publisher.ApprovedPublicationDomains, []string{domain}) {
		t.Fatalf("bootstrap did not approve the configured publication domain: %+v", report.Publisher)
	}

	// A Reader-only profile is valid, but cannot supply bootstrap authority.
	configuration.Auth.DevelopmentTokens = map[string]config.DevelopmentUser{
		"bootstrap-reader": {Subject: "bootstrap-reader", TenantID: tenant, Roles: []identity.Role{identity.RoleReader}},
	}
	var refused error
	output = captureCatalogctlStdout(t, func() error {
		refused = runArgs([]string{"bootstrap", "--config", writeCatalogctlTestConfig(t, configuration), "--namespace", "must-not-exist"})
		return nil
	})
	want := "bootstrap requires a configured development tenant administrator; production bootstrap uses the identity provisioning workflow"
	if refused == nil || refused.Error() != want || len(output) != 0 {
		t.Fatalf("bootstrap admitted a Reader or emitted a success report: error=%v output=%q", refused, output)
	}
}

// TestLoopbackHealthProbeDeadlineCancelsResponse exercises the entrypoint's
// real whole-call budget and observes server-side cancellation of an open body.
func TestLoopbackHealthProbeDeadlineCancelsResponse(t *testing.T) {
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var fastCalls, stalledCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("probe changed its method")
		}
		if r.URL.Path == "/livez" {
			fastCalls.Add(1)
			_, _ = w.Write([]byte("ready"))
			return
		}
		if r.URL.Path != "/readyz" || stalledCalls.Add(1) != 1 {
			t.Error("probe retried or changed its fixed path")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("unfinished"))
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Error(err)
		}
		close(started)
		select {
		case <-r.Context().Done():
			close(canceled)
		case <-release:
		}
	}))
	t.Cleanup(func() {
		close(release)
		server.CloseClientConnections()
		server.Close()
	})
	if err := probe([]string{"--url", server.URL + "/livez"}); err != nil || fastCalls.Load() != 1 {
		t.Fatalf("fast successful probe control: %v, requests=%d", err, fastCalls.Load())
	}
	result := make(chan error, 1)
	go func() { result <- probe([]string{"--url", server.URL + "/readyz"}) }()
	watchdog := time.NewTimer(10 * time.Second)
	defer watchdog.Stop()
	select {
	case <-started:
	case err := <-result:
		t.Fatalf("probe completed before receiving the stalled body: %v", err)
	case <-watchdog.C:
		t.Fatal("probe did not reach the owned response")
	}
	select {
	case err := <-result:
		var timeout net.Error
		if err == nil || !errors.Is(err, context.DeadlineExceeded) && !(errors.As(err, &timeout) && timeout.Timeout()) {
			t.Fatalf("stalled probe did not enforce its own deadline: %v", err)
		}
	case <-watchdog.C:
		t.Fatal("probe exceeded the independent watchdog instead of enforcing its deadline")
	}
	select {
	case <-canceled:
	case <-watchdog.C:
		t.Fatal("probe deadline did not cancel the server request")
	}
	if stalledCalls.Load() != 1 || fastCalls.Load() != 1 {
		t.Fatalf("probe repeated requests: fast=%d stalled=%d", fastCalls.Load(), stalledCalls.Load())
	}
}
