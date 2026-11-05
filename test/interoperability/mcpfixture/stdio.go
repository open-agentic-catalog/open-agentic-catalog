package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/demodata"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/demofixture"
)

const stdioOrigin = "https://127.0.0.1:9443"

func prepareStdio(directory, privateIP string) error {
	ip := net.ParseIP(privateIP)
	if ip == nil || ip.To4() == nil || !ip.IsPrivate() {
		return errors.New("expected an owned private IPv4 address")
	}
	if err := certificatesForIP(directory, ip); err != nil {
		return err
	}
	return demodata.Prepare(directory, demodata.DefaultPlan("expanded", stdioOrigin))
}

// stdioManifest supplies fixed metadata without following resource URLs.
// The empty version is an invalid control; version absence is valid.
func stdioManifest() map[string]any {
	entries := make([]any, 0, 4)
	for _, version := range []any{nil, "", "1.0.0", "2.0.0"} {
		entry := map[string]any{"identifier": "urn:air:catalog.example.com:server:stdio-indexed", "displayName": "Owned indexed MCP metadata", "type": "application/mcp-server-card+json", "url": stdioOrigin + "/never-invoke", "description": "Inert independent-client metadata", "tags": []string{"stdio-indexed"}}
		if version != nil {
			entry["version"] = version
		}
		entries = append(entries, entry)
	}
	return map[string]any{"specVersion": "1.0", "entries": entries}
}

func stdioHandler(archives http.Handler) http.Handler {
	var mu sync.Mutex
	counts := map[string]int{"manifests": 0, "archives": 0, "unexpected": 0, "credentials": 0}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			counts["credentials"]++
			http.Error(w, "No credentials allowed", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet || r.URL.RawQuery != "" {
			counts["unexpected"]++
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		switch r.URL.Path {
		case "/observations":
			_ = json.NewEncoder(w).Encode(counts)
		case "/.well-known/ard.json":
			counts["manifests"]++
			_ = json.NewEncoder(w).Encode(stdioManifest())
		case "/never-invoke":
			counts["unexpected"]++
			http.Error(w, "Resource execution is forbidden", http.StatusForbidden)
		default:
			counts["archives"]++
			archives.ServeHTTP(w, r)
		}
	})
}

func serveStdio(ctx context.Context) error { return serveStdioMode(ctx, false) }

func serveStdioMode(ctx context.Context, matrix bool) error {
	files, err := demodata.ReadPrepared("/private")
	if err != nil {
		return err
	}
	handler := stdioHandler(demofixture.PreparedHandler(files))
	if matrix {
		handler = matrixHandler(demofixture.PreparedHandler(files), "/private/outage")
	}
	server := &http.Server{Addr: "0.0.0.0:9443", Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 8192, ErrorLog: log.New(io.Discard, "", 0), TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	done := stopStdio(ctx, server)
	err = server.ListenAndServeTLS(filepath.Join("/private", "server.pem"), filepath.Join("/private", "server.key"))
	if errors.Is(err, http.ErrServerClosed) {
		return <-done
	}
	return err
}

// Keep the listener lifetime under the command's signal context, as for the
// existing Everything wrapper. Shutdown drains before the process succeeds.
func stopStdio(ctx context.Context, server *http.Server) <-chan error {
	// A startup failure can return before the signal owner cancels ctx.
	// Buffer the result so that cleanup never waits for a departed receiver.
	done := make(chan error, 1)
	go func() {
		defer close(done)
		<-ctx.Done()
		bounded, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		done <- server.Shutdown(bounded)
	}()
	return done
}
