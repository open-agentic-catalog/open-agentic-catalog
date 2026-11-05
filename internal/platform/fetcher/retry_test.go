package fetcher

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRetryAfterDelay(t *testing.T) {
	now := time.Date(2026, 9, 1, 17, 0, 0, 0, time.UTC)
	for value, expected := range map[string]time.Duration{
		"120":                            2 * time.Minute,
		strings.Repeat("0", 125) + "120": 2 * time.Minute,
		strings.Repeat("0", 126) + "120": 0,
		"0":                              0,
		"+120":                           0,
		"4294967296":                     0,
		"999999999999999999999999":       0,
		"private-response":               0,
		"1\x0020":                        0,
		now.Add(5 * time.Minute).Format(http.TimeFormat):            5 * time.Minute,
		now.Add(24 * time.Hour).Format(http.TimeFormat):             24 * time.Hour,
		now.Add(24*time.Hour + time.Second).Format(http.TimeFormat): 0,
		now.Add(-time.Second).Format(http.TimeFormat):               0,
		"86400":                  24 * time.Hour,
		"":                       0,
		strings.Repeat("1", 129): 0,
		"1\n20":                  0,
		"1.5":                    0,
		"86401":                  0,
		"-1":                     0,
	} {
		if actual := RetryAfterDelay(value, now); actual != expected {
			t.Errorf("Retry-After %q = %v, want %v", value, actual, expected)
		}
	}
	if got := RetryAfterDelay(" 120 \t", now); got != 2*time.Minute {
		t.Fatalf("optional surrounding whitespace changed delay: %s", got)
	}
	// HTTP dates are measured from receipt, including subsecond elapsed time.
	if got := RetryAfterDelay(now.Add(time.Minute).Format(http.TimeFormat), now.Add(250*time.Millisecond)); got != 59750*time.Millisecond {
		t.Fatalf("receipt time lost: %s", got)
	}
}
