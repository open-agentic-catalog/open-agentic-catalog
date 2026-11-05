// Container probes use a literal loopback-only client so the scratch image needs
// no shell or external HTTP utility to inspect process health.

package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	diagnostic "github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/diagnostics"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

const healthProbeTimeout = 2500 * time.Millisecond

// probe is a deliberately separate outbound boundary for container-local
// health checks. It accepts only literal loopback HTTP endpoints, fixed probe
// paths, bounded responses, and no redirects, credentials, or proxy.
func probe(arguments []string) error {
	flags := diagnostic.Flags("catalogctl probe")
	endpoint := flags.String("url", "", "loopback worker or API health endpoint")
	expectedStatus := flags.Int("expect-status", http.StatusOK, "expected HTTP status (200 or 503)")
	if err := diagnostic.Parse(flags, arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *endpoint == "" {
		return safeerr.New("usage: catalogctl probe --url http://127.0.0.1:<port>/<startupz|livez|readyz> [--expect-status <200|503>]")
	}
	if *expectedStatus != http.StatusOK && *expectedStatus != http.StatusServiceUnavailable {
		return safeerr.New("health probe expected status must be 200 or 503")
	}
	parsed, err := url.Parse(*endpoint)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.RawPath != "" || parsed.Fragment != "" || parsed.Host == "" {
		return safeerr.New("probe URL must be an absolute loopback HTTP URL without credentials, query, or fragment")
	}
	address := net.ParseIP(parsed.Hostname())
	port, portErr := strconv.ParseUint(parsed.Port(), 10, 16)
	if address == nil || !address.IsLoopback() || portErr != nil || port == 0 {
		return safeerr.New("probe URL must contain a loopback IP address and non-zero numeric port")
	}
	switch parsed.Path {
	case "/startupz", "/livez", "/readyz":
	default:
		return safeerr.New("probe URL path must be /startupz, /livez, or /readyz")
	}
	ctx, cancel := context.WithTimeout(context.Background(), healthProbeTimeout)
	defer cancel()
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: healthProbeTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return safeerr.New("health probe redirects are forbidden") }}
	return requestHealthProbe(ctx, client, parsed.String(), *expectedStatus)
}

// requestHealthProbe validates one bounded response using the loopback client
// configured by probe. It neither retries nor interprets the response body.
func requestHealthProbe(ctx context.Context, client *http.Client, endpoint string, expectedStatus int) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build health probe request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("health probe failed: %w", err)
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 4097))
	closeErr := response.Body.Close()
	if readErr != nil {
		return fmt.Errorf("read health probe response: %w", readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close health probe response: %w", closeErr)
	}
	if len(body) > 4096 {
		return safeerr.New("health probe response exceeded 4 KiB")
	}
	if response.StatusCode != expectedStatus {
		return fmt.Errorf("health probe returned HTTP %d; expected %d", response.StatusCode, expectedStatus)
	}
	return nil
}
