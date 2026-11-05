package fetcher

import (
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// RetryAfterDelay parses delta-seconds or an HTTP date relative to receipt time.
// Source and embedding Jobs ignore malformed, past or excessive hints; accepted
// delays are at most 24 hours. Webhook hints use a separate configured-cap policy.
func RetryAfterDelay(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return 0
	}
	if seconds, err := strconv.ParseUint(value, 10, 32); err == nil && seconds <= uint64((24*time.Hour)/time.Second) {
		return time.Duration(seconds) * time.Second
	}
	if deadline, err := http.ParseTime(value); err == nil {
		delay := deadline.Sub(now)
		if delay > 0 && delay <= 24*time.Hour {
			return delay
		}
	}
	return 0
}
