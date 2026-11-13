package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"sync"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

// publicationHandler serves exact publisher bytes; controls never rewrite them.
// It has no HTTP client, descriptor loader or external destination.
func publicationHandler(raw []byte) http.Handler {
	var mutex sync.Mutex
	unavailable := false
	requests, unexpected, credentialed, discovery := 0, 0, 0, 0
	statuses := map[int]int{}
	sum := sha256.Sum256(raw)
	etag := `"` + hex.EncodeToString(sum[:]) + `"`
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutex.Lock()
		defer mutex.Unlock()
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			credentialed++
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/healthz" && r.URL.RawQuery == "":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/state" && r.URL.RawQuery == "":
			writeJSON(w, http.StatusOK, map[string]any{"requests": requests, "statuses": statuses, "unexpectedRequests": unexpected, "credentialedRequests": credentialed, "discoveryProbes": discovery})
		case r.Method == http.MethodGet && (r.URL.Path == "/.well-known/ard.json" || r.URL.Path == "/.well-known/ai-catalog.json") && r.URL.RawQuery == "":
			// Inspection tries standard discovery locations after the selected
			// generic-JSON document is refused. No substitute document is served.
			discovery++
			http.NotFound(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/control" && r.URL.RawQuery == "":
			var control struct {
				Unavailable *bool `json:"unavailable"`
			}
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
			if err != nil || jsondoc.Decode(body, &control) != nil || control.Unavailable == nil {
				http.Error(w, "Invalid publication control", http.StatusBadRequest)
				return
			}
			unavailable = *control.Unavailable
			writeJSON(w, http.StatusOK, map[string]bool{"unavailable": unavailable})
		case r.Method == http.MethodGet && (r.URL.Path == "/ai-catalog.json" || r.URL.Path == "/generic/ai-catalog.json") && r.URL.RawQuery == "":
			requests++
			if requests > 128 || unavailable {
				statuses[http.StatusServiceUnavailable]++
				http.Error(w, "Owned publication unavailable", http.StatusServiceUnavailable)
				return
			}
			statuses[http.StatusOK]++
			media := "application/ai-catalog+json"
			if r.URL.Path == "/generic/ai-catalog.json" {
				media = "application/json"
			}
			w.Header().Set("Content-Type", media)
			w.Header().Set("ETag", etag)
			_, _ = w.Write(raw)
		default:
			unexpected++
			http.NotFound(w, r)
		}
	})
}
