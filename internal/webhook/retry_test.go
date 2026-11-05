package webhook

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
)

type advancingSender struct {
	clock  *clock.Fake
	delay  time.Duration
	result SendResult
}

func (s advancingSender) Send(context.Context, SendRequest) (SendResult, error) {
	s.clock.Advance(s.delay)
	return s.result, nil
}

func TestRetryDelayStartsAfterTheResponse(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	cipher, _ := NewAESGCM(keyBytes(3))
	encrypted, _ := cipher.Encrypt([]byte("whsec_" + strings.Repeat("a", 43)))
	endpoint := Endpoint{ID: "endpoint", SecretCiphertext: encrypted, SecretVersion: 1}
	event := catalog.OutboxEvent{ID: "00000000-0000-7000-8000-000000000010", TenantID: "00000000-0000-7000-8000-000000000001", Type: catalog.CanonicalEventType("resource.created"), Subject: "00000000-0000-7000-8000-000000000011", Data: testPublicReference(t, "resource", "00000000-0000-7000-8000-000000000011"), CreatedAt: now}
	for _, tc := range []struct {
		name    string
		ceiling time.Duration
		hint    string
		want    time.Duration
	}{
		{"receiver minimum", time.Hour, "120", 2 * time.Minute},
		{"HTTP date minimum", time.Hour, now.Add(130 * time.Second).Format(http.TimeFormat), 2 * time.Minute},
		{"receiver capped", 5500 * time.Millisecond, "120", 5500 * time.Millisecond},
		{"receiver within cap", 5500 * time.Millisecond, "5", 5 * time.Second},
		{"backoff exceeds hint", 5500 * time.Millisecond, "1", 3062125 * time.Microsecond},
		{"malformed hint", 5500 * time.Millisecond, "invalid", 3062125 * time.Microsecond},
		{"no hint", 5500 * time.Millisecond, "", 3062125 * time.Microsecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			serviceClock := clock.NewFake(now)
			sender := advancingSender{clock: serviceClock, delay: 10 * time.Second, result: SendResult{Status: 429, RetryAfter: tc.hint}}
			// The retained delivery's second attempt uses the capped window.
			got := deliver(context.Background(), sender, cipher, endpoint, event, Delivery{ID: "delivery", Attempt: 1}, 3, serviceClock, tc.ceiling)
			if got.NextAttemptAt == nil || !got.NextAttemptAt.Equal(now.Add(10*time.Second+tc.want)) || !got.UpdatedAt.Equal(now.Add(10*time.Second)) {
				t.Fatalf("wrong completion-based retry: %#v", got)
			}
			sender.delay = -time.Second
			sender.result = SendResult{Status: 204}
			got = deliver(context.Background(), sender, cipher, endpoint, event, got, 3, serviceClock, tc.ceiling)
			if got.UpdatedAt.Before(now.Add(10*time.Second)) || got.State != DeliverySucceeded || got.NextAttemptAt != nil {
				t.Fatalf("backwards clock or success changed delivery state: %#v", got)
			}
		})
	}
}

func TestRetryAfterGrammarAndBounds(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	for input, want := range map[string]time.Duration{
		"": 0, "0": 0, " 120 ": 2 * time.Minute, "+1": 0, "-1": 0,
		"1.5": 0, "1e3": 0, "120, 240": 0, "junk": 0, "3601": time.Hour,
		strings.Repeat("9", 100): time.Hour, strings.Repeat("9", 128): time.Hour, strings.Repeat("9", 129): 0,
		"9223372037": time.Hour, // Valid int64 seconds must be capped before duration multiplication overflows.
		now.Add(-time.Second).Format(http.TimeFormat):    0,
		now.Format(http.TimeFormat):                      0,
		now.Add(2 * time.Minute).Format(http.TimeFormat): 2 * time.Minute,
		now.Add(2 * time.Hour).Format(http.TimeFormat):   time.Hour,
	} {
		t.Run(input, func(t *testing.T) {
			if got := retryAfterDelay(input, now, time.Hour); got != want {
				t.Fatalf("delay = %s, want %s", got, want)
			}
		})
	}
	for _, format := range []string{time.RFC850, time.ANSIC} {
		if got := retryAfterDelay(now.Add(time.Minute).Format(format), now, time.Hour); got != time.Minute {
			t.Fatalf("legacy HTTP date %s = %s", format, got)
		}
	}
	// Configuration permits a fractional-second ceiling; an integer hint just
	// below it must retain its own duration, while the next second is capped.
	ceiling := 5500 * time.Millisecond
	for input, want := range map[string]time.Duration{"5": 5 * time.Second, "6": ceiling} {
		if got := retryAfterDelay(input, now, ceiling); got != want {
			t.Fatalf("fractional ceiling for %s: %s, want %s", input, got, want)
		}
	}
}

func TestRetryClassificationAndServerDelay(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	cipher, _ := NewAESGCM(keyBytes(7))
	secret, _ := cipher.Encrypt([]byte("whsec_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))
	endpoint := Endpoint{ID: "endpoint", SecretCiphertext: secret, SecretVersion: 1}
	event := catalog.OutboxEvent{ID: "00000000-0000-7000-8000-000000000010", TenantID: "00000000-0000-7000-8000-000000000001", Type: catalog.CanonicalEventType("resource.created"), Subject: "00000000-0000-7000-8000-000000000011", Data: testPublicReference(t, "resource", "00000000-0000-7000-8000-000000000011"), CreatedAt: now}
	for status := 200; status <= 599; status++ {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			delivery := Delivery{ID: "delivery", State: DeliveryPending}
			sender := &recordingSender{results: []SendResult{{Status: status, RetryAfter: "120"}}}
			got := deliver(context.Background(), sender, cipher, endpoint, event, delivery, 3, clock.NewFake(now), time.Hour)
			retry := status == 408 || status == 425 || status == 429 || status >= 500 && status != 501 && status != 505
			switch {
			case status < 300:
				if got.State != DeliverySucceeded || got.NextAttemptAt != nil {
					t.Fatalf("success = %#v", got)
				}
			case retry:
				if got.State != DeliveryWaiting || got.NextAttemptAt == nil || !got.NextAttemptAt.Equal(now.Add(2*time.Minute)) {
					t.Fatalf("retry = %#v", got)
				}
			default:
				if got.State != DeliveryDeadLetter || got.NextAttemptAt != nil {
					t.Fatalf("terminal = %#v", got)
				}
			}
		})
	}
}

func TestRetryJitterIsStableDispersedAndBounded(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	windows := []time.Duration{5 * time.Second, 15 * time.Second, 45 * time.Second, 135 * time.Second, 405 * time.Second, 1215 * time.Second}
	for _, ceiling := range []time.Duration{5 * time.Second, 5500*time.Millisecond + time.Nanosecond, time.Minute, time.Hour} {
		t.Run(ceiling.String(), func(t *testing.T) {
			for attempt := 1; attempt < 20; attempt++ {
				upper := time.Hour
				if attempt <= len(windows) {
					upper = windows[attempt-1]
				}
				upper = min(upper, ceiling)
				delays := make(map[time.Duration]bool)
				for id := 0; id < 100; id++ {
					input := Delivery{ID: strconv.Itoa(id), Attempt: attempt}
					got := failedDelivery(input, 20, now, "Unavailable.", ceiling)
					if got.State != DeliveryWaiting || got.NextAttemptAt == nil || got.ID != input.ID || got.Attempt != attempt || got.SafeError != "Unavailable." {
						t.Fatalf("retry lost delivery state: %#v", got)
					}
					delay := got.NextAttemptAt.Sub(now)
					repeated := failedDelivery(input, 20, now, "Unavailable.", ceiling)
					if delay < upper/2 || delay > upper || repeated.NextAttemptAt == nil || !got.NextAttemptAt.Equal(*repeated.NextAttemptAt) {
						t.Fatalf("attempt %d, ID %s: jitter %s outside [%s, %s] or unstable", attempt, input.ID, delay, upper/2, upper)
					}
					delays[delay] = true
				}
				// Fixed IDs and hashing make this a deterministic dispersion check.
				if len(delays) < 50 {
					t.Fatalf("attempt %d: only %d distinct retry delays", attempt, len(delays))
				}
			}
		})
	}
}

func TestRetryJitterWindowEndpoints(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	// These fixed delivery IDs map to the bottom and top SHA-256 jitter buckets
	// at attempt two. Expected delays are literal contract values, not computed
	// with the production backoff helper.
	for _, tc := range []struct {
		id      string
		ceiling time.Duration
		want    time.Duration
	}{
		{"14360", 5 * time.Second, 2500 * time.Millisecond},
		{"1738", 5 * time.Second, 5 * time.Second},
		{"14360", 5500 * time.Millisecond, 2750 * time.Millisecond},
		{"1738", 5500 * time.Millisecond, 5500 * time.Millisecond},
		{"14360", time.Hour, 7500 * time.Millisecond},
		{"1738", time.Hour, 15 * time.Second},
	} {
		got := failedDelivery(Delivery{ID: tc.id, Attempt: 2}, 3, now, "Unavailable.", tc.ceiling)
		if got.NextAttemptAt == nil || got.NextAttemptAt.Sub(now) != tc.want {
			t.Fatalf("ID %s, ceiling %s: got %#v, want delay %s", tc.id, tc.ceiling, got, tc.want)
		}
	}
}

func TestRetryCeilingAndExhaustion(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	for _, ceiling := range []time.Duration{5 * time.Second, time.Minute, time.Hour} {
		got := failedDelivery(Delivery{ID: "delivery", Attempt: 19}, 20, now, "Unavailable.", ceiling)
		if got.NextAttemptAt == nil || got.NextAttemptAt.Sub(now) > ceiling {
			t.Fatalf("uncapped backoff: %#v", got)
		}
		if got := retryAfterDelay("999999999999999999999999999", now, ceiling); got != ceiling {
			t.Fatalf("uncapped server delay: %s", got)
		}
	}
	for _, limit := range []int{1, 20} {
		previous := now.Add(time.Minute)
		got := failedDelivery(Delivery{ID: "delivery", Attempt: limit, NextAttemptAt: &previous}, limit, now, "Unavailable.", time.Hour)
		if got.State != DeliveryDeadLetter || got.NextAttemptAt != nil || got.Attempt != limit {
			t.Fatalf("exhausted delivery: %#v", got)
		}
	}
}
