// Command mcpfixture provides test-only TLS and admission around unmodified Everything.
// It forwards MCP bytes; it does not implement an MCP server.
package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

const backend = "http://127.0.0.1:3001"
const operations = "127.0.0.1:9444"
const maxBody = 64 * 1024

type initialization struct {
	Protocol     string          `json:"protocolVersion"`
	Capabilities json.RawMessage `json:"capabilities"`
}

type observations struct {
	HTTP          map[string]int   `json:"http"`
	Requests      int              `json:"requests"`
	Forwarded     int              `json:"forwarded"`
	Denied        int              `json:"denied"`
	Authorization int              `json:"authorization"`
	OtherTools    int              `json:"otherTools"`
	Methods       map[string]int   `json:"methods"`
	Initialize    []initialization `json:"initialize"`
	EmptyRoots    int              `json:"emptyRoots"`
}

type fixture struct {
	mu           sync.Mutex
	seen         observations
	key          string
	proxy        *httputil.ReverseProxy
	transport    *http.Transport
	sessionLimit int
}

func newFixture(key string, target *url.URL) *fixture {
	f := &fixture{key: key, seen: observations{Methods: make(map[string]int), Initialize: []initialization{}, HTTP: make(map[string]int)}}
	f.transport = &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext, ResponseHeaderTimeout: 10 * time.Second, MaxConnsPerHost: 16, IdleConnTimeout: 10 * time.Second}
	f.proxy = &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Host = target.Host
			r.Out.Header.Del("X-Mcp-Test-Key")
			r.Out.Header.Del("Authorization")
		},
		Transport:     f.transport,
		FlushInterval: -1,
		ErrorLog:      log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "fixture upstream unavailable", http.StatusBadGateway)
		},
	}
	return f
}

func (f *fixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.seen.Requests++
	if r.Header.Get("Authorization") != "" {
		f.seen.Authorization++
	}
	f.mu.Unlock()
	if r.URL.Path != "/mcp" || r.URL.RawQuery != "" || (r.Method != http.MethodPost && r.Method != http.MethodGet && r.Method != http.MethodDelete) {
		http.Error(w, "fixture route unavailable", http.StatusNotFound)
		return
	}
	if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Mcp-Test-Key")), []byte(f.key)) != 1 {
		f.mu.Lock()
		f.seen.Denied++
		f.mu.Unlock()
		http.Error(w, "fixture key required", http.StatusUnauthorized)
		return
	}
	if r.Method == http.MethodPost {
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
		if err != nil {
			http.Error(w, "fixture body limit", http.StatusRequestEntityTooLarge)
			return
		}
		var msg struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(data, &msg) != nil {
			http.Error(w, "fixture JSON required", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		method := msg.Method
		switch method {
		case "initialize", "notifications/initialized", "logging/setLevel", "tools/list", "tools/call", "notifications/cancelled":
		case "":
			method = "response"
		default:
			method = "other"
		}
		f.seen.Methods[method]++
		if method == "initialize" && f.sessionLimit > 0 && f.seen.Methods[method] > f.sessionLimit {
			f.mu.Unlock()
			http.Error(w, "restart demo peer to clear bounded sessions", http.StatusTooManyRequests)
			return
		}
		if method == "initialize" && len(f.seen.Initialize) < 16 {
			// Retain only capabilities and protocol, never arbitrary client identity/metadata.
			var params initialization
			if json.Unmarshal(msg.Params, &params) == nil {
				f.seen.Initialize = append(f.seen.Initialize, params)
			}
		}
		forbidden := false
		if method == "tools/call" {
			var params struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(msg.Params, &params) != nil || params.Name != "echo" {
				f.seen.OtherTools++
				forbidden = true
			}
		}
		if method == "response" {
			var result struct {
				Roots []json.RawMessage `json:"roots"`
			}
			if json.Unmarshal(msg.Result, &result) == nil && result.Roots != nil && len(result.Roots) == 0 {
				f.seen.EmptyRoots++
			}
		}
		f.mu.Unlock()
		if forbidden {
			http.Error(w, "fixture permits only echo", http.StatusForbidden)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(data))
	}
	f.mu.Lock()
	f.seen.Forwarded++
	f.seen.HTTP[r.Method]++
	f.mu.Unlock()
	f.proxy.ServeHTTP(w, r)
}

func (f *fixture) status(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(f.seen); err != nil {
		return
	}
}

func serve(ctx context.Context) error { return serveAddress(ctx, "127.0.0.1:9443") }

// serveAddress is shared deployment glue; observations stay bounded in both modes.
func serveAddress(ctx context.Context, address string) error {
	const directory = "/private"
	key, err := os.ReadFile("/private/key")
	if err != nil {
		return err
	}
	if len(key) < 32 {
		return errors.New("fixture key too short")
	}
	target, err := url.Parse(backend)
	if err != nil {
		return err
	}
	f := newFixture(string(key), target)
	if address == "0.0.0.0:9443" {
		f.sessionLimit = 256
	}
	defer f.transport.CloseIdleConnections()
	api := &http.Server{Addr: address, Handler: f, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 16 * 1024, ErrorLog: log.New(io.Discard, "", 0), TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	status := &http.Server{Addr: operations, Handler: http.HandlerFunc(f.status), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, MaxHeaderBytes: 1024}
	errorsOut := make(chan error, 2)
	go func() { errorsOut <- api.ListenAndServeTLS(directory+"/server.pem", directory+"/server.key") }()
	go func() { errorsOut <- status.ListenAndServe() }()
	var result error
	select {
	case <-ctx.Done():
	case result = <-errorsOut:
	}
	// Close cancels long-lived SSE and does not wait indefinitely for a client.
	_ = api.Close()
	_ = status.Close()
	if errors.Is(result, http.ErrServerClosed) {
		return nil
	}
	return result
}

func probe(ctx context.Context) error {
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", "127.0.0.1:3001")
	if err != nil {
		return err
	}
	_ = conn.Close()
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+operations+"/", nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return errors.New("fixture status unavailable")
	}
	_, err = io.Copy(os.Stdout, io.LimitReader(response.Body, 64*1024))
	return err
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	var err error
	switch {
	case len(os.Args) == 4 && os.Args[1] == "prepare-stdio":
		err = prepareStdio(os.Args[2], os.Args[3])
	case len(os.Args) == 2 && os.Args[1] == "serve-stdio":
		err = serveStdio(ctx)
	case len(os.Args) == 2 && os.Args[1] == "serve-matrix":
		err = serveStdioMode(ctx, true)
	case len(os.Args) == 2 && os.Args[1] == "probe":
		err = probe(ctx)
	case len(os.Args) == 2 && os.Args[1] == "catalog-request":
		err = catalogCommand(ctx)
	case len(os.Args) == 3 && os.Args[1] == "certificates":
		err = certificates(os.Args[2])
	case len(os.Args) == 2 && os.Args[1] == "serve-demo":
		err = serveAddress(ctx, "0.0.0.0:9443")
	case len(os.Args) == 2 && os.Args[1] == "serve":
		err = serve(ctx)
	default:
		err = errors.New("usage: mcpfixture serve | probe")
	}
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "MCP fixture failed")
		os.Exit(1)
	}
}
