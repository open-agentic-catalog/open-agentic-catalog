package main

import (
	"net/http"
	"strconv"
	"sync"
	"time"
)

// The owned pressure phase has twelve distinct source URLs and a four-second
// fail-safe, below its five-second fetch deadline. No production route uses it.
type pressureFixture struct {
	mu      sync.Mutex
	release chan struct{}
	expires time.Time
	active  int
	peak    int
	expired bool
}

func registerPressureFixture(mux *http.ServeMux) {
	p := &pressureFixture{}
	mux.HandleFunc("POST /pressure/arm", p.arm)
	mux.HandleFunc("POST /pressure/release", p.open)
	mux.HandleFunc("GET /pressure/state", p.state)
	mux.HandleFunc("GET /pressure/{source}/.well-known/ard.json", p.manifest)
	mux.HandleFunc("GET /pressure/{source}/ard/v1/agents", p.list)
}

func pressureSource(r *http.Request) bool {
	id, err := strconv.Atoi(r.PathValue("source"))
	return err == nil && id >= 0 && id < 12 && strconv.Itoa(id) == r.PathValue("source")
}

func (p *pressureFixture) manifest(w http.ResponseWriter, r *http.Request) {
	if !pressureSource(r) {
		http.NotFound(w, r)
		return
	}
	id := r.PathValue("source")
	writeJSON(w, http.StatusOK, map[string]any{"entries": []any{map[string]any{
		"identifier":  "urn:air:functional.example:registry:pressure-" + id,
		"displayName": "Pressure Registry " + id,
		"type":        "application/ai-registry+json",
		"url":         fixtureOrigin + "/pressure/" + id + "/ard/v1/",
	}}})
}

func (p *pressureFixture) arm(w http.ResponseWriter, _ *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.release != nil || p.active != 0 {
		w.WriteHeader(http.StatusConflict)
		return
	}
	p.release = make(chan struct{})
	p.expires = time.Now().Add(4 * time.Second)
	p.peak, p.expired = 0, false
	w.WriteHeader(http.StatusNoContent)
}

func (p *pressureFixture) open(w http.ResponseWriter, _ *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.release != nil {
		p.expired = p.expired || !time.Now().Before(p.expires)
		close(p.release)
		p.release = nil
	}
	w.WriteHeader(http.StatusNoContent)
}

func (p *pressureFixture) state(w http.ResponseWriter, _ *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"active": p.active, "peak": p.peak, "expired": p.expired, "held": p.release != nil})
}

func (p *pressureFixture) list(w http.ResponseWriter, r *http.Request) {
	if !pressureSource(r) {
		http.NotFound(w, r)
		return
	}
	if size := r.URL.Query().Get("pageSize"); size != "1" && size != "100" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	release, expires := p.release, p.expires
	if release != nil {
		p.active++
		p.peak = max(p.peak, p.active)
	}
	p.mu.Unlock()
	if release != nil {
		defer func() { p.mu.Lock(); p.active--; p.mu.Unlock() }()
		timer := time.NewTimer(time.Until(expires))
		defer timer.Stop()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		case <-timer.C:
			p.mu.Lock()
			p.expired = true
			p.mu.Unlock()
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": []any{entryOne()}, "total": 1})
}
