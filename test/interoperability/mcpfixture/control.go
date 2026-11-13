package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// controlTraffic retains finite protocol observations without bodies or credentials.
type controlTraffic struct {
	Requests   int            `json:"requests"`
	Unexpected int            `json:"unexpected"`
	Sampling   bool           `json:"sampling"`
	Methods    map[string]int `json:"methods"`
	Tools      map[string]int `json:"tools"`
	Statuses   map[string]int `json:"statuses"`
	Offered    []string       `json:"offered"`
	Negotiated []string       `json:"negotiated"`
}

type controlInitialization struct{}

// controlProxy forwards unchanged MCP bytes to the fixed owned Catalog API.
type controlProxy struct {
	mu        sync.Mutex
	traffic   controlTraffic
	proxy     *httputil.ReverseProxy
	transport *http.Transport
}

// newControlProxy provides private HTTPS without replacing Catalog's MCP server.
func newControlProxy() *controlProxy {
	f := &controlProxy{traffic: controlTraffic{Methods: map[string]int{}, Tools: map[string]int{}, Statuses: map[string]int{}, Offered: []string{}, Negotiated: []string{}}}
	f.transport = &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, ResponseHeaderTimeout: 10 * time.Second, MaxConnsPerHost: 8, IdleConnTimeout: 10 * time.Second}
	f.proxy = &httputil.ReverseProxy{
		Rewrite:   func(r *httputil.ProxyRequest) { r.SetURL(&url.URL{Scheme: "http", Host: "127.0.0.1:8080"}) },
		Transport: f.transport,
		ErrorLog:  log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "Owned Catalog unavailable", http.StatusBadGateway)
		},
		ModifyResponse: f.observeNegotiation,
	}
	return f
}

// protocolLabel keeps unexpected versions bounded and free of peer-controlled text.
func protocolLabel(value string) string {
	switch value {
	case "2025-11-25", "2026-07-28":
		return value
	default:
		return "other"
	}
}

// observeNegotiation reads an initialization result and restores the exact bytes.
func (f *controlProxy) observeNegotiation(response *http.Response) error {
	f.mu.Lock()
	status := "other"
	switch response.StatusCode {
	case http.StatusOK, http.StatusAccepted, http.StatusUnauthorized, http.StatusForbidden, http.StatusMethodNotAllowed:
		status = strconv.Itoa(response.StatusCode)
	}
	f.traffic.Statuses[status]++
	f.mu.Unlock()
	if response.Request.Context().Value(controlInitialization{}) != true {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	closeErr := response.Body.Close()
	if err != nil || closeErr != nil || len(raw) > maxBody {
		return errors.New("owned initialization response unavailable")
	}
	response.Body = io.NopCloser(bytes.NewReader(raw))
	var message struct {
		Result struct {
			Protocol string `json:"protocolVersion"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &message) == nil && message.Result.Protocol != "" {
		f.mu.Lock()
		if len(f.traffic.Negotiated) < 32 {
			f.traffic.Negotiated = append(f.traffic.Negotiated, protocolLabel(message.Result.Protocol))
		}
		f.mu.Unlock()
	}
	return nil
}

// ServeHTTP observes only finite MCP labels; every other destination is refused.
func (f *controlProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/adk-observations" && r.URL.RawQuery == "" {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(f.traffic); err != nil {
			return
		}
		return
	}
	f.mu.Lock()
	f.traffic.Requests++
	allowed := r.URL.Path == "/mcp" && r.URL.RawQuery == "" && (r.Method == http.MethodPost || r.Method == http.MethodGet || r.Method == http.MethodDelete)
	if !allowed {
		f.traffic.Unexpected++
	}
	f.mu.Unlock()
	if !allowed {
		http.Error(w, "Resource requests are forbidden", http.StatusForbidden)
		return
	}
	if r.Method == http.MethodPost {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
		if err != nil {
			http.Error(w, "Owned request bound exceeded", http.StatusRequestEntityTooLarge)
			return
		}
		var message struct {
			Method string `json:"method"`
			Params struct {
				Name         string                     `json:"name"`
				Protocol     string                     `json:"protocolVersion"`
				Capabilities map[string]json.RawMessage `json:"capabilities"`
			} `json:"params"`
		}
		_ = json.Unmarshal(raw, &message)
		method := message.Method
		switch method {
		case "initialize", "notifications/initialized", "tools/list", "tools/call", "ping", "notifications/cancelled":
		default:
			method = "other"
		}
		f.mu.Lock()
		f.traffic.Methods[method]++
		if method == "initialize" {
			_, sampling := message.Params.Capabilities["sampling"]
			f.traffic.Sampling = f.traffic.Sampling || sampling
			if len(f.traffic.Offered) < 32 {
				f.traffic.Offered = append(f.traffic.Offered, protocolLabel(message.Params.Protocol))
			}
			r = r.WithContext(context.WithValue(r.Context(), controlInitialization{}, true))
		}
		if method == "tools/call" {
			name := message.Params.Name
			switch name {
			case "catalog_control_capabilities", "catalog_control_access", "catalog_control_search", "catalog_control_release", "catalog_control_explore", "catalog_control_indexed_record":
			default:
				name = "other"
			}
			f.traffic.Tools[name]++
		}
		f.mu.Unlock()
		r.Body = io.NopCloser(bytes.NewReader(raw))
	}
	f.proxy.ServeHTTP(w, r)
}

// serveControl exposes the existing Catalog over invocation-owned private TLS.
func serveControl(ctx context.Context) error {
	f := newControlProxy()
	defer f.transport.CloseIdleConnections()
	server := &http.Server{Addr: "127.0.0.1:9555", Handler: f, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 8192, ErrorLog: log.New(io.Discard, "", 0), TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	done := stopStdio(ctx, server)
	err := server.ListenAndServeTLS(filepath.Join("/private", "server.pem"), filepath.Join("/private", "server.key"))
	if errors.Is(err, http.ErrServerClosed) {
		return <-done
	}
	return err
}
