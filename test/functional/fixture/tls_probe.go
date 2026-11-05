package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/netip"
	"sync/atomic"
	"time"
)

type tlsProbeResult struct {
	Stage    string `json:"stage"`
	Outcome  string `json:"outcome"`
	Protocol string `json:"protocol,omitempty"`
}

// tlsProbe accepts only an owned private fixture address and public CA on stdin.
// It reports transport stages, never errors, peer documents or credentials.
func tlsProbe(args []string, input io.Reader, output io.Writer) error {
	if len(args) != 2 || (args[0] != "h2" && args[0] != "http1") {
		return errors.New("select TLS probe protocol and private address")
	}
	address, err := netip.ParseAddrPort(args[1])
	if err != nil || !address.Addr().IsPrivate() || address.Port() != 8443 {
		return errors.New("invalid TLS probe destination")
	}
	ca, err := io.ReadAll(io.LimitReader(input, (16<<10)+1))
	if err != nil || len(ca) > 16<<10 {
		return errors.New("invalid TLS probe CA")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return errors.New("invalid TLS probe CA")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return json.NewEncoder(output).Encode(fixtureTLSProbe(ctx, args[1], roots, args[0] == "h2"))
}

func fixtureTLSProbe(ctx context.Context, address string, roots *x509.CertPool, h2 bool) tlsProbeResult {
	var stage atomic.Uint32
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, ForceAttemptHTTP2: h2, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: "federation-fixture"}, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	trace := &httptrace.ClientTrace{TLSHandshakeStart: func() { stage.Store(1) }, TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
		if err == nil {
			stage.Store(2)
		}
	}, GotFirstResponseByte: func() { stage.Store(3) }}
	request, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodGet, "https://federation-fixture:8443/.well-known/ard.json", nil)
	if err != nil {
		return tlsProbeResult{Stage: "request", Outcome: "error"}
	}
	response, err := client.Do(request)
	if err != nil {
		outcome := "error"
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			outcome = "timeout"
		}
		return tlsProbeResult{Stage: [...]string{"tcp", "tls", "headers", "body"}[min(stage.Load(), 3)], Outcome: outcome}
	}
	defer func() { _ = response.Body.Close() }()
	size, err := io.Copy(io.Discard, io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || response.StatusCode != http.StatusOK || size > 1<<20 {
		return tlsProbeResult{Stage: "body", Outcome: "error", Protocol: response.Proto}
	}
	return tlsProbeResult{Stage: "complete", Outcome: "connected", Protocol: response.Proto}
}
