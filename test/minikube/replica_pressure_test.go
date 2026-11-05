//go:build minikube

package main

import (
	"context"
	"errors"
	"math"
	"testing"
	"testing/synctest"
	"time"
)

func TestPressureQueueRejectsMissingOrUnconstrainedEvidence(t *testing.T) {
	good := pressureQueue{Total: 12, Running: 3, Queued: 9, Owners: 3, Age: 1, Connections: 48, OrdinaryCapacity: 97}
	if !validPressureQueue(good) {
		t.Fatal("valid bounded observation rejected")
	}
	for _, change := range []func(*pressureQueue){
		func(q *pressureQueue) { q.Total = 11 }, func(q *pressureQueue) { q.Running = 2 }, func(q *pressureQueue) { q.Running = 4 }, func(q *pressureQueue) { q.Queued = 0 }, func(q *pressureQueue) { q.Queued = 8 }, func(q *pressureQueue) { q.Owners = 2 }, func(q *pressureQueue) { q.Age = -1 }, func(q *pressureQueue) { q.Age = 120 }, func(q *pressureQueue) { q.Age = math.NaN() }, func(q *pressureQueue) { q.Connections = 0 }, func(q *pressureQueue) { q.Connections = 49 }, func(q *pressureQueue) { q.OrdinaryCapacity = 49 },
	} {
		bad := good
		change(&bad)
		if validPressureQueue(bad) {
			t.Fatalf("invalid observation accepted: %+v", bad)
		}
	}
}

func TestPressureReadsRequireTheExpectedIdentity(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   bool
	}{
		{200, `{"tenantId":"00000000-0000-7000-8000-000000000001","subject":"oidc-admin","actorType":"human"}`, true},
		{200, `{"tenantId":"00000000-0000-7000-8000-000000000002","subject":"oidc-admin","actorType":"human"}`, false},
		{200, `{"tenantId":"00000000-0000-7000-8000-000000000001","subject":"other","actorType":"human"}`, false},
		{200, `{"tenantId":"00000000-0000-7000-8000-000000000001","subject":"oidc-admin","actorType":"service"}`, false},
		{401, `{}`, false}, {200, `{}`, false}, {200, `invalid`, false},
	} {
		if validPressureSelf(httpResult{Status: tc.status, Body: []byte(tc.body)}) != tc.want {
			t.Fatal("pressure read identity validation", tc)
		}
	}
}

func TestPressureCancellationIncludesClientReturnInDeadline(t *testing.T) {
	for _, tc := range []struct {
		name     string
		delay    time.Duration
		response error
		timeout  bool
		bad      bool
	}{
		{"prompt cancellation", 100 * time.Millisecond, context.Canceled, false, false},
		{"slow cancellation", 4 * time.Second, context.Canceled, true, false},
		{"deadline boundary", 3 * time.Second, context.Canceled, true, false},
		{"unexpected success", 0, nil, false, true},
		{"wrong error", 0, errors.New("other"), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
				defer cancel()
				result := make(chan error, 1)
				called := false
				producerDone := make(chan struct{})
				defer func() {
					if called {
						<-producerDone
					}
				}()
				stopRequest := func() {
					called = true
					go func() { defer close(producerDone); time.Sleep(tc.delay); result <- tc.response }()
				}
				started := time.Now()
				err := cancelPressureRequest(ctx, stopRequest, result)
				if !called {
					t.Fatal("client cancellation was not requested")
				}
				if tc.timeout {
					if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) != 3*time.Second {
						t.Fatalf("client wait exceeded the shared deadline: elapsed=%s error=%v", time.Since(started), err)
					}
				} else if (err != nil) != tc.bad {
					t.Fatalf("incorrect cancellation classification: %v", err)
				}
			})
		})
	}
}
