package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sync"
)

// recordSearch observes local test traffic only. It never changes peer response bytes.
func recordSearch(next http.Handler) http.Handler {
	var mutex sync.Mutex
	requests := make([]map[string]any, 0, 512)
	overflow := false
	evidenceRequests := 0
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/_qualification/requests" {
			mutex.Lock()
			defer mutex.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"requests": requests, "overflow": overflow, "evidenceRequests": evidenceRequests})
			return
		}
		if evidenceRoute(r.URL.Path) {
			mutex.Lock()
			evidenceRequests++
			mutex.Unlock()
		}
		if r.Method == http.MethodPost && r.URL.Path == "/v1/search" {
			raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, requestLimit))
			if err != nil {
				http.Error(w, "request limit", http.StatusRequestEntityTooLarge)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
			mutex.Lock()
			if len(requests) == cap(requests) {
				overflow = true
			} else if json.Valid(raw) {
				requests = append(requests, map[string]any{"body": json.RawMessage(raw), "authorizationPresent": r.Header.Get("Authorization") != ""})
			}
			mutex.Unlock()
		}
		next.ServeHTTP(w, r)
	})
}
