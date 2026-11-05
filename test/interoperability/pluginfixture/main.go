// The Plugin qualification observer wraps the existing prepared archive handler.
package main

import (
	"crypto/tls"
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/demodata"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/demofixture"
)

type observation struct {
	Method        string `json:"method"`
	Path          string `json:"path"`
	Authorization bool   `json:"authorization"`
	Cookie        bool   `json:"cookie"`
}

func main() {
	directory := flag.String("directory", "", "owned prepared input")
	flag.Parse()
	files, err := demodata.ReadPrepared(*directory)
	if err != nil {
		os.Exit(1)
	}
	handler := demofixture.PreparedHandler(files)
	var mu sync.Mutex
	rows := make([]observation, 0)
	observed := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/observations" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(rows); err != nil {
				return
			}
			return
		}
		if len(rows) >= 64 {
			http.Error(w, "observation limit", http.StatusServiceUnavailable)
			return
		}
		rows = append(rows, observation{Method: r.Method, Path: r.URL.Path, Authorization: r.Header.Get("Authorization") != "", Cookie: r.Header.Get("Cookie") != ""})
		handler.ServeHTTP(w, r)
	})
	server := &http.Server{Addr: "127.0.0.1:9443", Handler: observed, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 10 * time.Second, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	if server.ListenAndServeTLS(filepath.Join(*directory, "cert.pem"), filepath.Join(*directory, "key.pem")) != nil {
		os.Exit(1)
	}
}
