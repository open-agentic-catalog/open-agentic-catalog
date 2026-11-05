package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"testing/synctest"
	"time"
)

func pressureRequest(ctx context.Context) *http.Request {
	r := httptest.NewRequestWithContext(ctx, "GET", "https://federation-fixture:8443/pressure/0/ard/v1/agents?pageSize=100", nil)
	r.SetPathValue("source", "0")
	return r
}

func TestPressureBarrierReleaseCancellationAndExpiry(t *testing.T) {
	for _, mode := range []string{"release", "cancel", "expire"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := &pressureFixture{}
				armed := httptest.NewRecorder()
				p.arm(armed, httptest.NewRequestWithContext(t.Context(), "POST", "/pressure/arm", nil))
				if armed.Code != 204 {
					t.Fatal(armed.Code)
				}
				conflict := httptest.NewRecorder()
				p.arm(conflict, httptest.NewRequestWithContext(t.Context(), "POST", "/pressure/arm", nil))
				if conflict.Code != 409 {
					t.Fatal("overlapping barrier was admitted")
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				reply := httptest.NewRecorder()
				done := make(chan struct{})
				go func() { p.list(reply, pressureRequest(ctx)); close(done) }()
				synctest.Wait()
				p.mu.Lock()
				active, peak := p.active, p.peak
				p.mu.Unlock()
				if active != 1 || peak != 1 {
					t.Fatal("request did not positively hold a slot")
				}
				select {
				case <-done:
					t.Fatal("barrier did not block")
				default:
				}
				switch mode {
				case "release":
					p.open(httptest.NewRecorder(), nil)
				case "cancel":
					cancel()
				case "expire":
					time.Sleep(4 * time.Second)
				}
				synctest.Wait()
				<-done
				if p.active != 0 {
					t.Fatal("barrier leaked active request")
				}
				switch mode {
				case "release":
					if reply.Code != 200 || p.expired || !json.Valid(reply.Body.Bytes()) {
						t.Fatal("release failed")
					}
				case "cancel":
					if reply.Body.Len() != 0 || p.expired {
						t.Fatal("cancelled response was delivered")
					}
				case "expire":
					if reply.Code != 503 || !p.expired {
						t.Fatal("expiry was accepted as success")
					}
				}
				p.open(httptest.NewRecorder(), nil)
				recovery := httptest.NewRecorder()
				p.list(recovery, pressureRequest(t.Context()))
				if recovery.Code != 200 {
					t.Fatal("fixture did not recover")
				}
			})
		})
	}
}

func TestPressureSourcesAndRoutes(t *testing.T) {
	mux := http.NewServeMux()
	registerPressureFixture(mux)
	for i := range 12 {
		r := httptest.NewRequestWithContext(t.Context(), "GET", "/pressure/"+strconv.Itoa(i)+"/.well-known/ard.json", nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 200 || !json.Valid(w.Body.Bytes()) {
			t.Fatal("eligible distinct source rejected", i)
		}
	}
	for _, path := range []string{"/pressure/12/.well-known/ard.json", "/pressure/-1/.well-known/ard.json", "/pressure/00/.well-known/ard.json", "/pressure/invalid/ard/v1/agents?pageSize=100", "/pressure/0/ard/v1/agents?pageSize=1000"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), "GET", path, nil))
		if w.Code != 404 && w.Code != 400 {
			t.Fatal("unbounded source or page admitted", path, w.Code)
		}
	}
}
