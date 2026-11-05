package config

import (
	"testing"
	"time"
)

func TestWebhookRetryCeilingValidation(t *testing.T) {
	for _, value := range []time.Duration{0, time.Second, 5 * time.Second, time.Minute, time.Hour, time.Hour + time.Nanosecond} {
		t.Run(value.String(), func(t *testing.T) {
			cfg := validTestConfig()
			cfg.Webhooks.Enabled = true
			cfg.Webhooks.RetryCeiling = Duration{Duration: value}
			err := cfg.Validate()
			valid := value >= 5*time.Second && value <= time.Hour
			if (err == nil) != valid {
				t.Fatalf("validation = %v, valid=%v", err, valid)
			}
		})
	}
}
