// Retry scheduling combines a reviewed response classification, deterministic
// per-delivery jitter and a bounded receiver Retry-After hint.

package webhook

import (
	"crypto/sha256"
	"encoding/binary"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Retry only responses for which another identical notification can reasonably
// succeed. Redirects are never followed and authentication/configuration errors
// require operator action, not another identical request.
func retryableStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooEarly ||
		status == http.StatusTooManyRequests || status >= 500 && status <= 599 &&
		status != http.StatusNotImplemented && status != http.StatusHTTPVersionNotSupported
}

// Stable per-delivery/attempt jitter spreads retries without a mutable random
// source or nondeterministic tests. Cap the window before sampling so low
// configured ceilings preserve dispersion. UUID delivery IDs provide the spread.
func jitteredBackoff(deliveryID string, attempt int, ceiling time.Duration) time.Duration {
	upper := min(webhookBackoff(attempt), ceiling)
	sum := sha256.Sum256([]byte(deliveryID + ":" + strconv.Itoa(attempt)))
	return upper/2 + (upper/2)*time.Duration(binary.BigEndian.Uint32(sum[:4])%10001)/10000
}

// retryAfterDelay accepts delta-seconds or a future HTTP date, clamps the delay
// to ceiling, and ignores malformed or past hints.
func retryAfterDelay(value string, now time.Time, ceiling time.Duration) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return 0
	}
	digits := true
	for _, character := range value {
		if character < '0' || character > '9' {
			digits = false
			break
		}
	}
	if digits {
		seconds, err := strconv.ParseInt(value, 10, 64)
		if err != nil || seconds > int64(ceiling/time.Second) {
			// A syntactically valid but overflowing delay still exceeds the cap.
			return ceiling
		}
		return min(time.Duration(seconds)*time.Second, ceiling)
	}
	when, err := http.ParseTime(value)
	if err != nil || !when.After(now) {
		return 0
	}
	return min(when.Sub(now), ceiling)
}
