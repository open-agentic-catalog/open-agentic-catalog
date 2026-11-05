package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/fxamacker/cbor/v2"
)

var evidencePath = regexp.MustCompile(`^/v1/agents/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/(receipt|status-token)$`)

// checkpointGate pauses one request before any upstream checkpoint read. Only
// the owned supervisor can release it; cancellation and timeout fail closed.
type checkpointGate struct {
	mu      sync.Mutex
	state   string
	release chan struct{}
}

func newCheckpointGate() *checkpointGate {
	return &checkpointGate{state: "armed", release: make(chan struct{})}
}

func (g *checkpointGate) status() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.state
}

func (g *checkpointGate) finish(state string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state != "waiting" && (state != "aborted" || g.state != "armed") {
		return false
	}
	g.state = state
	close(g.release)
	return true
}

func (g *checkpointGate) wait(ctx context.Context) bool {
	g.mu.Lock()
	if g.state != "armed" {
		g.mu.Unlock()
		return false
	}
	g.state = "waiting"
	g.mu.Unlock()
	bounded, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	select {
	case <-g.release:
	case <-bounded.Done():
		state := "expired"
		if ctx.Err() != nil {
			state = "cancelled"
		}
		g.finish(state)
	}
	return bounded.Err() == nil && g.status() == "released"
}

func evidenceRoute(path string) bool {
	return path == "/checkpoint" || path == "/v1/log/checkpoint/history" || evidencePath.MatchString(path)
}

// evidenceEdge is selected only by the explicit local trust scenario. Native
// responses stay byte-exact unless one named corruption control is selected.
func evidenceEdge(discovery http.Handler, upstream string, client *http.Client) (http.Handler, error) {
	native, err := routeHandler(upstream, false, true, client)
	if err != nil {
		return nil, err
	}
	var mutex sync.Mutex
	mode := "native"
	var gate *checkpointGate
	requests := make([]map[string]any, 0, 512)
	overflow := false
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_qualification/evidence" {
			mutex.Lock()
			defer mutex.Unlock()
			if r.Method == http.MethodPost {
				var input struct {
					Mode string `json:"mode"`
				}
				decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
				decoder.DisallowUnknownFields()
				if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || !slices.Contains([]string{"native", "unsupported-framework", "receipt-signature", "receipt-root", "checkpoint", "missing-status", "gone-status", "unavailable", "delay", "history-gate", "history-missing", "history-corrupt", "release-checkpoint"}, input.Mode) || input.Mode == "" {
					http.Error(w, "invalid evidence control", http.StatusBadRequest)
					return
				}
				if input.Mode == "release-checkpoint" {
					if gate == nil || !gate.finish("released") {
						http.Error(w, "checkpoint is not waiting", http.StatusConflict)
						return
					}
				} else {
					if gate != nil {
						gate.finish("aborted")
					}
					mode, gate = input.Mode, nil
					if strings.HasPrefix(mode, "history-") {
						gate = newCheckpointGate()
					}
				}
			} else if r.Method != http.MethodGet {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			state := "none"
			if gate != nil {
				state = gate.status()
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"mode": mode, "requests": requests, "overflow": overflow, "checkpointGate": state})
			return
		}
		if !evidenceRoute(r.URL.Path) {
			mutex.Lock()
			selected := mode
			mutex.Unlock()
			if selected == "unsupported-framework" && r.URL.Path == "/v1/search" {
				result := httptest.NewRecorder()
				discovery.ServeHTTP(result, r)
				body := result.Body.Bytes()
				if result.Code == http.StatusOK {
					body = unsupportedFramework(body)
				}
				for key, values := range result.Header() {
					w.Header()[key] = values
				}
				w.WriteHeader(result.Code)
				_, _ = w.Write(body)
				return
			}
			discovery.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		mutex.Lock()
		selected := mode
		selectedGate := gate
		row := map[string]any{"path": r.URL.RequestURI(), "mode": selected, "authorizationPresent": r.Header.Get("Authorization") != "", "cookiePresent": r.Header.Get("Cookie") != ""}
		if len(requests) == cap(requests) {
			overflow = true
		} else {
			requests = append(requests, row)
		}
		mutex.Unlock()
		if selectedGate != nil && r.URL.Path == "/checkpoint" && !selectedGate.wait(r.Context()) {
			http.Error(w, "owned checkpoint gate refused", http.StatusServiceUnavailable)
			return
		}
		if selected == "history-missing" && r.URL.Path == "/v1/log/checkpoint/history" {
			mutex.Lock()
			row["status"] = http.StatusNotFound
			mutex.Unlock()
			http.Error(w, "owned history unavailable", http.StatusNotFound)
			return
		}
		if selected == "delay" {
			timer := time.NewTimer(6 * time.Second)
			defer timer.Stop()
			select {
			case <-r.Context().Done():
				return
			case <-timer.C:
			}
		}
		if selected == "unavailable" || strings.HasSuffix(r.URL.Path, "/status-token") && (selected == "missing-status" || selected == "gone-status") {
			status := http.StatusServiceUnavailable
			if selected == "missing-status" {
				status = http.StatusNotFound
			}
			if selected == "gone-status" {
				status = http.StatusGone
			}
			http.Error(w, "owned evidence fault", status)
			return
		}
		result := httptest.NewRecorder()
		native.ServeHTTP(result, r)
		body := bytes.Clone(result.Body.Bytes())
		nativeHash := fmt.Sprintf("%x", sha256.Sum256(body))
		if result.Code == http.StatusOK {
			switch {
			case selected == "receipt-signature" && strings.HasSuffix(r.URL.Path, "/receipt") && len(body) > 0:
				body[len(body)-1] ^= 1
			case selected == "receipt-root" && strings.HasSuffix(r.URL.Path, "/receipt"):
				body = corruptProofRoot(body)
			case selected == "checkpoint" && r.URL.Path == "/checkpoint":
				body = bytes.Replace(body, []byte("owned-ans"), []byte("wrong-log"), 1)
			case selected == "history-corrupt" && r.URL.Path == "/v1/log/checkpoint/history":
				body = corruptHistorySignature(body)
			}
		}
		mutex.Lock()
		row["status"], row["nativeSHA256"], row["responseSHA256"] = result.Code, nativeHash, fmt.Sprintf("%x", sha256.Sum256(body))
		mutex.Unlock()
		for k, values := range result.Header() {
			w.Header()[k] = values
		}
		w.WriteHeader(result.Code)
		_, _ = w.Write(body)
	}), nil
}

// Alter only the c2sp signature. Unsigned native "valid" hints remain unchanged.
func corruptHistorySignature(raw []byte) []byte {
	var document map[string]any
	if json.Unmarshal(raw, &document) == nil {
		checkpoints, _ := document["checkpoints"].([]any)
		if len(checkpoints) == 1 {
			checkpoint, _ := checkpoints[0].(map[string]any)
			signatures, _ := checkpoint["signatures"].([]any)
			for _, item := range signatures {
				signature, _ := item.(map[string]any)
				value, _ := signature["rawSignature"].(string)
				decoded, err := base64.StdEncoding.Strict().DecodeString(value)
				if signature["signatureType"] == "c2sp" && err == nil && len(decoded) > 4 {
					decoded[len(decoded)-1] ^= 1
					signature["rawSignature"] = base64.StdEncoding.EncodeToString(decoded)
					if encoded, err := json.Marshal(document); err == nil {
						return encoded
					}
				}
			}
		}
	}
	return []byte("invalid owned history control")
}

func unsupportedFramework(raw []byte) []byte {
	var document map[string]any
	if json.Unmarshal(raw, &document) != nil {
		return []byte("invalid owned framework control")
	}
	results, _ := document["results"].([]any)
	for _, result := range results {
		row, _ := result.(map[string]any)
		trust, _ := row["trustManifest"].(map[string]any)
		if trust != nil {
			trust["trustSchema"] = map[string]any{"identifier": "https://framework.example/unsupported", "version": "1"}
		}
	}
	body, err := json.Marshal(document)
	if err != nil {
		return []byte("invalid owned framework control")
	}
	return body
}

// The root is unsigned COSE metadata. Changing it must not grant proof authority.
func corruptProofRoot(raw []byte) []byte {
	var tag cbor.RawTag
	var sign1 []cbor.RawMessage
	var unprotected map[int64]cbor.RawMessage
	var proof map[int64]cbor.RawMessage
	if cbor.Unmarshal(raw, &tag) != nil || cbor.Unmarshal(tag.Content, &sign1) != nil || len(sign1) != 4 || cbor.Unmarshal(sign1[1], &unprotected) != nil || cbor.Unmarshal(unprotected[396], &proof) != nil {
		return []byte("invalid owned proof control")
	}
	proof[-4], _ = cbor.Marshal(make([]byte, 32))
	unprotected[396], _ = cbor.Marshal(proof)
	sign1[1], _ = cbor.Marshal(unprotected)
	tag.Content, _ = cbor.Marshal(sign1)
	encoded, _ := cbor.Marshal(tag)
	return encoded
}
