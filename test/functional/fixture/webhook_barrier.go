package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

// One invocation-local selection is sufficient for the crash control. Only
// verified signed requests enter its bounded evidence; bodies and keys stay out.
type webhookBarrier struct {
	EndpointID string           `json:"endpointId"`
	Source     string           `json:"source"`
	EventID    string           `json:"eventId"`
	Released   bool             `json:"released"`
	Attempts   []barrierAttempt `json:"attempts"`
	release    chan struct{}
	expires    time.Time
}

type barrierAttempt struct {
	Digest    string `json:"digest"`
	Duplicate bool   `json:"duplicate"`
	Outcome   string `json:"outcome"`
}

func (r *webhookReceiver) barrierControl(w http.ResponseWriter, req *http.Request) {
	var input struct {
		Action     string `json:"action"`
		EndpointID string `json:"endpointId"`
		Source     string `json:"source"`
		EventID    string `json:"eventId"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, req.Body, 4096))
	decoder.DisallowUnknownFields()
	var extra any
	if decoder.Decode(&input) != nil || decoder.Decode(&extra) != io.EOF ||
		(input.Action != "arm" && input.Action != "release") || len(input.EndpointID) != 36 ||
		len(input.Source) == 0 || len(input.Source) > 1024 || len(input.EventID) != 36 {
		http.Error(w, "invalid barrier", http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if input.Action == "arm" {
		control, exists := r.endpoints[input.EndpointID]
		if !exists || control.Status != http.StatusNoContent || control.DelayMillis != 0 || r.barrier != nil {
			http.Error(w, "barrier unavailable", http.StatusConflict)
			return
		}
		r.barrier = &webhookBarrier{EndpointID: input.EndpointID, Source: input.Source, EventID: input.EventID,
			Attempts: []barrierAttempt{}, release: make(chan struct{}), expires: time.Now().Add(30 * time.Second)}
	} else {
		b := r.barrier
		if b == nil || b.EndpointID != input.EndpointID || b.Source != input.Source || b.EventID != input.EventID {
			http.Error(w, "barrier mismatch", http.StatusConflict)
			return
		}
		if !b.Released {
			close(b.release)
			b.Released = true
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (r *webhookReceiver) barrierReport(w http.ResponseWriter, _ *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	writeJSON(w, http.StatusOK, r.barrier)
}

// Called under mu, after signature verification. A mismatched event cannot
// satisfy this control even when aggregate duplicate/activity counters increase.
func (r *webhookReceiver) barrierBegin(endpoint, source, event string, body []byte, duplicate bool) (*webhookBarrier, int) {
	b := r.barrier
	if b == nil || b.EndpointID != endpoint || b.Source != source || b.EventID != event {
		return nil, -1
	}
	if len(b.Attempts) >= 8 {
		return b, -1
	}
	digest := sha256.Sum256(body)
	b.Attempts = append(b.Attempts, barrierAttempt{Digest: hex.EncodeToString(digest[:]), Duplicate: duplicate, Outcome: "blocked"})
	return b, len(b.Attempts) - 1
}

// Release, disconnection and timeout are independently bounded exits. In
// particular, cleanup does not depend on a killed sender reading a response.
func (r *webhookReceiver) barrierWait(ctx context.Context, b *webhookBarrier, index int) bool {
	outcome := "released"
	select {
	case <-b.release:
		// Once released, later attempts no longer belong to the timed barrier.
		// A single select with an expired timer would choose randomly here.
	default:
		timer := time.NewTimer(time.Until(b.expires))
		defer timer.Stop()
		select {
		case <-b.release:
		case <-ctx.Done():
			outcome = "disconnected"
		case <-timer.C:
			outcome = "expired"
		}
	}
	r.mu.Lock()
	b.Attempts[index].Outcome = outcome
	r.mu.Unlock()
	return outcome == "released"
}
