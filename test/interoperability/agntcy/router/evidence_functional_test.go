//go:build functional

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestCheckpointGateThroughOwnedTLS(t *testing.T) {
	for _, cancelRequest := range []bool{false, true} {
		t.Run(map[bool]string{false: "release", true: "cancel"}[cancelRequest], func(t *testing.T) {
			var calls atomic.Int32
			const checkpoint = "owned-ans\n4\nexact-native-root\n\n— exact-native-signature\n"
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/checkpoint" {
					t.Error("wrong upstream path")
				}
				w.Header().Set("Content-Type", "text/plain")
				_, _ = io.WriteString(w, checkpoint)
			}))
			defer upstream.Close()
			edge, err := evidenceEdge(http.NotFoundHandler(), upstream.URL, upstream.Client())
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewTLSServer(edge)
			defer server.Close()
			client := server.Client()
			client.Timeout = 2 * time.Second
			control := func(mode string, expected int) map[string]any {
				t.Helper()
				method := http.MethodGet
				var body io.Reader
				if mode != "" {
					method, body = http.MethodPost, bytes.NewBufferString(`{"mode":"`+mode+`"}`)
				}
				request, e := http.NewRequestWithContext(t.Context(), method, server.URL+"/_qualification/evidence", body)
				if e != nil {
					t.Fatal(e)
				}
				response, e := client.Do(request)
				if e != nil {
					t.Fatal(e)
				}
				defer func() { _ = response.Body.Close() }()
				if response.StatusCode != expected {
					t.Fatal(response.StatusCode)
				}
				var value map[string]any
				if expected == 200 && json.NewDecoder(response.Body).Decode(&value) != nil {
					t.Fatal("invalid control response")
				}
				return value
			}
			control("release-checkpoint", 409)
			control("history-gate", 200)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				request, e := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/checkpoint", nil)
				if e == nil {
					var response *http.Response
					response, e = client.Do(request)
					if e == nil {
						defer func() { _ = response.Body.Close() }()
						var raw []byte
						raw, e = io.ReadAll(response.Body)
						if e == nil && (response.StatusCode != http.StatusOK || string(raw) != checkpoint) {
							e = io.ErrUnexpectedEOF
						}
					}
				}
				done <- e
			}()
			deadline := time.Now().Add(time.Second)
			for control("", 200)["checkpointGate"] != "waiting" {
				if time.Now().After(deadline) {
					t.Fatal("request did not reach gate")
				}
				time.Sleep(time.Millisecond)
			}
			if calls.Load() != 0 {
				t.Fatal("checkpoint fetched before release")
			}
			if cancelRequest {
				cancel()
			} else {
				control("release-checkpoint", 200)
			}
			if e := <-done; (e != nil) != cancelRequest {
				t.Fatal("wrong request outcome", e)
			}
			control("native", 200)
			if calls.Load() != map[bool]int32{false: 1, true: 0}[cancelRequest] {
				t.Fatal("unexpected upstream requests")
			}
		})
	}
}
