package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Observations retain fixed protocol fields and synthetic credential labels,
// never token bytes, arguments, queries or response documents.
type contextForgeEvent struct {
	Path       string `json:"path"`
	Method     string `json:"method"`
	Tool       string `json:"tool,omitempty"`
	Protocol   string `json:"protocol,omitempty"`
	Negotiated string `json:"negotiated,omitempty"`
	Credential string `json:"credential"`
	TestKey    bool   `json:"testKey,omitempty"`
	Status     int    `json:"status"`
}

type contextForgeObservations struct {
	mu       sync.Mutex
	labels   map[string]string
	events   []contextForgeEvent
	overflow int
}

type contextForgeResponse struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
	probe  bool
}

func (w *contextForgeResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *contextForgeResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *contextForgeResponse) Write(raw []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.probe && w.body.Len()+len(raw) <= 16384 {
		_, _ = w.body.Write(raw)
	}
	return w.ResponseWriter.Write(raw)
}

func contextForgeObserve(directory string) (*contextForgeObservations, error) {
	raw, err := os.ReadFile(filepath.Join(directory, "token-labels.json")) // #nosec G304 -- invocation-owned public digest input.
	if err != nil {
		return nil, err
	}
	o := &contextForgeObservations{labels: map[string]string{}, events: []contextForgeEvent{}}
	if err := json.Unmarshal(raw, &o.labels); err != nil {
		return nil, err
	}
	return o, nil
}

func (o *contextForgeObservations) observe(next http.Handler, path string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "owned request exceeds its bound", http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var envelope struct {
			Method string `json:"method"`
			Params struct {
				Name            string `json:"name"`
				ProtocolVersion string `json:"protocolVersion"`
			} `json:"params"`
		}
		_ = json.Unmarshal(raw, &envelope)
		method := envelope.Method
		switch method {
		case "initialize", "server/discover", "notifications/initialized", "tools/list", "tools/call", "resources/list", "resources/read", "prompts/list", "prompts/get", "ping":
		default:
			method = "other"
		}
		tool := envelope.Params.Name
		switch tool {
		case "catalog_control_capabilities", "catalog_control_search", "catalog_control_release", "catalog_control_indexed_record", "catalog_control_explore", "catalog_control_access", "echo":
		default:
			tool = ""
		}
		protocol := r.Header.Get("Mcp-Protocol-Version")
		if protocol == "" {
			protocol = envelope.Params.ProtocolVersion
		}
		if protocol != "2025-11-25" && protocol != "2026-07-28" {
			protocol = ""
		}
		credential := "none"
		if bearer := r.Header.Get("Authorization"); len(r.Header.Values("Authorization")) != 0 {
			sum := sha256.Sum256([]byte(strings.TrimPrefix(bearer, "Bearer ")))
			credential = o.labels[hex.EncodeToString(sum[:])]
			if credential == "" {
				credential = "other"
			}
		}
		testKey := path == "everything" && r.Header.Get("X-Mcp-Test-Key") != ""
		response := &contextForgeResponse{ResponseWriter: w, probe: method == "initialize" || method == "server/discover"}
		next.ServeHTTP(response, r)
		var result struct {
			Result struct {
				ProtocolVersion string `json:"protocolVersion"`
			} `json:"result"`
		}
		_ = json.Unmarshal(response.body.Bytes(), &result)
		negotiated := result.Result.ProtocolVersion
		if negotiated != "2025-11-25" && negotiated != "2026-07-28" {
			negotiated = ""
		}
		o.add(contextForgeEvent{Path: path, Method: method, Tool: tool, Protocol: protocol, Negotiated: negotiated, Credential: credential, TestKey: testKey, Status: response.status})
	})
}

func (o *contextForgeObservations) add(event contextForgeEvent) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.events) < 1024 {
		o.events = append(o.events, event)
	} else {
		o.overflow++
	}
}

func (o *contextForgeObservations) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(struct {
		Events   []contextForgeEvent `json:"events"`
		Overflow int                 `json:"overflow"`
	}{o.events, o.overflow}); err != nil {
		return
	}
}
