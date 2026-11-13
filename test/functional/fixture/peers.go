// Peer fixtures expose a fixed Catalog API through test TLS for federation journeys.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

const peerResponseLimit = 4 << 20

type peerStatusWriter struct {
	http.ResponseWriter
	status int
	count  func(int)
}

func (w *peerStatusWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.count(status)
	w.ResponseWriter.WriteHeader(status)
}
func (w *peerStatusWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}
func (w *peerStatusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// peerHandler terminates test TLS and forwards actual Catalog bytes to two
// fixed Compose services, never a caller-selected URL. This fixture-only
// transport is not a product external-content fetch boundary.
func peerHandler(transport http.RoundTripper) http.Handler {
	return routedPeerHandler(transport, map[string]string{"peer-a.test:8443": "api-a:8080", "private-a.test:8443": "api-a:8080", "peer-b.test:8443": "api:8080"})
}

func kubernetesPeerRoutes(primary, secondary string) (map[string]string, error) {
	valid := regexp.MustCompile(`^[a-z]([-a-z0-9]{0,61}[a-z0-9])?$`)
	if !valid.MatchString(primary) || !valid.MatchString(secondary) || primary == secondary {
		return nil, errors.New("peer namespaces must be distinct DNS labels")
	}
	return map[string]string{"peer-a." + primary + ".svc.cluster.local:8443": "catalog." + secondary + ".svc.cluster.local:8080", "peer-b." + primary + ".svc.cluster.local:8443": "catalog." + primary + ".svc.cluster.local:8080"}, nil
}

func routedPeerHandler(transport http.RoundTripper, routes map[string]string) http.Handler {
	var mutex sync.Mutex
	counts := map[string]int{}
	pages := map[string]int{}
	failContinuations := false
	failInitial := false
	failOffline := false
	failManifestJSON := false
	pageCap := 0
	peerHost := func(host string) bool {
		_, ok := routes[host]
		return ok
	}

	proxy := &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(request *httputil.ProxyRequest) {
			backend := routes[request.In.Host]
			request.SetURL(&url.URL{Scheme: "http", Host: backend})
			request.Out.Host = request.In.Host
			for _, name := range []string{"Forwarded", "X-Forwarded-Host", "X-Original-Host", "X-Forwarded-For", "X-Forwarded-Proto"} {
				request.Out.Header.Del(name)
			}
		},
		ModifyResponse: func(response *http.Response) error {
			original := response.Body
			defer func() { _ = original.Close() }()
			body, err := io.ReadAll(io.LimitReader(response.Body, peerResponseLimit+1))
			if err != nil || len(body) > peerResponseLimit {
				return errors.New("peer response exceeds fixture bounds")
			}
			response.Body = io.NopCloser(bytes.NewReader(body))
			return nil
		},
		ErrorLog: log.New(io.Discard, "", 0),
		ErrorHandler: func(writer http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(writer, "Catalog peer unavailable", http.StatusBadGateway)
		},
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		// Controls are reachable only through the owned fixture origin. They
		// retain closed page classes and counts, never query text or tokens.
		if !peerHost(request.Host) && !request.URL.IsAbs() && request.URL.RawQuery == "" {
			if request.Method == http.MethodPost && request.URL.Path == "/v1/embeddings" {
				peerEmbeddings(writer, request)
				return
			}
			if request.Method == http.MethodGet && request.URL.Path == "/peer-pages" {
				mutex.Lock()
				defer mutex.Unlock()
				writeJSON(writer, http.StatusOK, pages)
				return
			}
			if request.Method == http.MethodPost && request.URL.Path == "/peer-fault" {
				raw, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, 1024))
				_ = request.Body.Close()
				var control struct {
					Enabled   *bool `json:"enabled"`
					Offline   bool  `json:"offline,omitempty"`
					Malformed bool  `json:"malformed,omitempty"`
					Initial   bool  `json:"initial,omitempty"`
					PageSize  *int  `json:"pageSize,omitempty"`
				}
				if err != nil || jsondoc.Decode(raw, &control) != nil || control.Enabled == nil || control.PageSize != nil && (*control.PageSize < 0 || *control.PageSize > 50) {
					http.Error(writer, "Invalid peer fault control", http.StatusBadRequest)
					return
				}
				mutex.Lock()
				failContinuations = *control.Enabled
				failInitial = control.Initial
				failOffline = *control.Enabled && control.Offline
				failManifestJSON = *control.Enabled && control.Malformed
				if control.PageSize != nil {
					pageCap = *control.PageSize
				}
				mutex.Unlock()
				writeJSON(writer, http.StatusOK, control)
				return
			}
		}
		if request.Method == http.MethodGet && request.URL.Path == "/healthz" && !peerHost(request.Host) {
			writer.WriteHeader(http.StatusOK)
			return
		}
		if request.Method == http.MethodGet && request.URL.Path == "/peer-stats" && !peerHost(request.Host) && !request.URL.IsAbs() {
			mutex.Lock()
			defer mutex.Unlock()
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(counts)
			return
		}
		allowedHost := peerHost(request.Host)
		allowedRoute := request.Method == http.MethodGet && (request.URL.Path == "/.well-known/ard.json" || request.URL.Path == "/.well-known/ai-catalog.json" || request.URL.Path == "/ard/v1/agents") || request.Method == http.MethodPost && (request.URL.Path == "/ard/v1/search" || request.URL.Path == "/ard/v1/explore")
		if !allowedHost || !allowedRoute || request.URL.IsAbs() {
			http.NotFound(writer, request)
			return
		}
		ctx, cancel := context.WithTimeout(request.Context(), 4*time.Second)
		defer cancel()
		request.Body = http.MaxBytesReader(writer, request.Body, 64<<10)
		mode := "read"
		page := "initial"
		if request.Method == http.MethodPost {
			raw, err := io.ReadAll(request.Body)
			_ = request.Body.Close()
			if err != nil {
				http.Error(writer, "Peer request exceeds fixture bounds", http.StatusRequestEntityTooLarge)
				return
			}
			request.Body = io.NopCloser(bytes.NewReader(raw))
			var envelope struct {
				Federation string `json:"federation"`
				PageToken  string `json:"pageToken"`
			}
			_ = json.Unmarshal(raw, &envelope)
			mode = "other"
			if envelope.Federation == "none" || envelope.Federation == "auto" || envelope.Federation == "referrals" {
				mode = envelope.Federation
			}
			if envelope.PageToken != "" {
				page = "continuation"
			}
			// Model a peer with a smaller page ceiling. The actual Catalog
			// still creates every result and signed continuation; no response
			// is fabricated. Apply the same ceiling to continuation requests.
			mutex.Lock()
			ceiling := pageCap
			mutex.Unlock()
			if ceiling > 0 && strings.HasPrefix(request.Host, "peer-a.") && request.URL.Path == "/ard/v1/search" {
				var fields map[string]json.RawMessage
				if json.Unmarshal(raw, &fields) != nil || fields == nil {
					http.Error(writer, "Invalid peer page input", http.StatusBadRequest)
					return
				}
				var size int
				if json.Unmarshal(fields["pageSize"], &size) == nil && size > ceiling {
					fields["pageSize"] = json.RawMessage(strconv.Itoa(ceiling))
				}
				raw, _ = json.Marshal(fields)
				request.Body = io.NopCloser(bytes.NewReader(raw))
				request.ContentLength = int64(len(raw))
			}
		}
		response := &peerStatusWriter{ResponseWriter: writer, count: func(status int) {
			// Count before publishing response headers so a subsequent stats read
			// cannot race with the completed discovery request's accounting.
			key := request.Host + " " + request.Method + " " + request.URL.Path + " " + mode + " " + strconv.Itoa(status)
			mutex.Lock()
			counts[key]++
			if request.Method == http.MethodPost && request.URL.Path == "/ard/v1/search" {
				pages[request.Host+" "+page+" "+strconv.Itoa(status)]++
			}
			mutex.Unlock()
		}}
		mutex.Lock()
		fail := failOffline && strings.HasPrefix(request.Host, "peer-a.") || failContinuations && strings.HasPrefix(request.Host, "peer-a.") && request.URL.Path == "/ard/v1/search" && (page == "continuation" || failInitial)
		malformed := failManifestJSON && strings.HasPrefix(request.Host, "peer-a.") && request.URL.Path == "/.well-known/ai-catalog.json"
		mutex.Unlock()
		if fail {
			http.Error(response, "Catalog peer continuation unavailable", http.StatusServiceUnavailable)
			return
		}
		if malformed {
			response.Header().Set("Content-Type", "application/ai-catalog+json")
			_, _ = response.Write([]byte(`{"entries":`))
			return
		}
		proxy.ServeHTTP(response, request.WithContext(ctx))
		if response.status == 0 {
			response.WriteHeader(http.StatusOK)
		}
	})
}

// Fixed vectors qualify pagination and equal-rank ordering, not model relevance.
// Only the fixture origin exposes this endpoint; peer discovery cannot reach it.
func peerEmbeddings(writer http.ResponseWriter, request *http.Request) {
	defer func() { _ = request.Body.Close() }()
	raw, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, 256<<10))
	var input struct {
		Model    string   `json:"model"`
		Input    []string `json:"input"`
		Encoding string   `json:"encoding_format"`
	}
	if err != nil || jsondoc.Decode(raw, &input) != nil || input.Model != "peer-pagination" || input.Encoding != "float" || len(input.Input) == 0 || len(input.Input) > 16 || request.Header.Get("Authorization") != "" {
		http.Error(writer, "Invalid embedding fixture input", http.StatusBadRequest)
		return
	}
	data := make([]map[string]any, len(input.Input))
	for index, text := range input.Input {
		if len(text) > 8192 {
			http.Error(writer, "Embedding fixture input too large", http.StatusRequestEntityTooLarge)
			return
		}
		data[index] = map[string]any{"index": index, "embedding": []int{1, 0, 0}}
	}
	writeJSON(writer, http.StatusOK, map[string]any{"model": input.Model, "data": data})
}
