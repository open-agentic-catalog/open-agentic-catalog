// Command router provides fixed local schema routing and byte-preserving HTTPS for Directory tests.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"
	"time"
)

const requestLimit, responseLimit = 2 << 20, 4 << 20

// handler admits only the selected local fixture routes; it never interprets peer documents.
func handler(upstream string, schema bool, client *http.Client) (http.Handler, error) {
	return routeHandler(upstream, schema, false, client)
}

func routeHandler(upstream string, schema, evidence bool, client *http.Client) (http.Handler, error) {
	base, err := url.Parse(upstream)
	if err != nil || base.Scheme != "http" || base.Hostname() == "" || base.User != nil || base.Path != "" || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("fixture requires a credential-free fixed HTTP upstream origin")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		route := r.URL.Path
		if path.Clean(route) != route || r.URL.RawPath != "" {
			http.NotFound(w, r)
			return
		}
		switch {
		case evidence:
			if r.Method != http.MethodGet || !evidenceRoute(route) {
				http.NotFound(w, r)
				return
			}
		case schema:
			if !strings.HasPrefix(route, "/api/1.0.0/") || (r.Method != http.MethodGet && r.Method != http.MethodPost) {
				http.NotFound(w, r)
				return
			}
			route = "/api/" + strings.TrimPrefix(route, "/api/1.0.0/")
		default:
			if !((r.Method == http.MethodGet && (route == "/" || route == "/.well-known/ard.json" || route == "/.well-known/ai-catalog.json" || route == "/v1/agents" || strings.HasPrefix(route, "/v1/agents/"))) || (r.Method == http.MethodPost && (route == "/v1/search" || route == "/v1/explore"))) {
				http.NotFound(w, r)
				return
			}
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, requestLimit))
		if err != nil {
			http.Error(w, "request limit", http.StatusRequestEntityTooLarge)
			return
		}
		target := *base
		target.Path, target.RawQuery = route, r.URL.RawQuery
		request, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), bytes.NewReader(raw))
		if err != nil {
			http.Error(w, "request rejected", http.StatusBadRequest)
			return
		}
		for _, name := range []string{"Content-Type", "Accept", "If-None-Match"} {
			if value := r.Header.Get(name); value != "" {
				request.Header.Set(name, value)
			}
		}
		response, err := client.Do(request) // #nosec G704 -- authority is the fixed operator origin; callers select only allowlisted paths and query strings.
		if err != nil {
			http.Error(w, "upstream unavailable", http.StatusBadGateway)
			return
		}
		defer func() { _ = response.Body.Close() }()
		body, err := io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
		if err != nil || len(body) > responseLimit {
			http.Error(w, "response limit", http.StatusBadGateway)
			return
		}
		for _, name := range []string{"Content-Type", "ETag", "Cache-Control", "Link"} {
			for _, value := range response.Header.Values(name) {
				w.Header().Add(name, value)
			}
		}
		w.WriteHeader(response.StatusCode)
		_, _ = w.Write(body)
	}), nil
}

// main runs one fixed upstream with bounded HTTP I/O and owned signal cleanup.
func main() {
	listen := flag.String("listen", ":8080", "fixture listen address")
	upstream := flag.String("upstream", "", "fixed namespace-local HTTP origin")
	schema := flag.Bool("schema", false, "route only schema version 1.0.0")
	record := flag.Bool("record-requests", false, "record bounded local Search requests for qualification")
	evidence := flag.String("evidence-upstream", "", "optional fixed owned ANS TL origin; enables evidence controls")
	certificate := flag.String("cert", "", "owned TLS certificate")
	key := flag.String("key", "", "owned TLS key")
	flag.Parse()
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	selected, err := handler(*upstream, *schema, client)
	if err != nil || flag.NArg() != 0 || (*certificate == "") != (*key == "") {
		log.Fatal("invalid fixture settings")
	}
	if *record {
		selected = recordSearch(selected)
	}
	if *evidence != "" {
		selected, err = evidenceEdge(selected, *evidence, client)
		if err != nil || *schema {
			log.Fatal("invalid owned evidence settings")
		}
	}
	server := &http.Server{Addr: *listen, Handler: selected, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 8 * time.Second, WriteTimeout: 8 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 16 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if *certificate != "" {
		err = server.ListenAndServeTLS(*certificate, *key)
	} else {
		err = server.ListenAndServe()
	}
	stop()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal("fixture listener failed")
	}
}
