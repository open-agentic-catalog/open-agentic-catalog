//go:build functional

package runtime

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
)

func TestRuntimeBuildsIndependentWebhookPolicyAndIgnoresDisabledFile(t *testing.T) {
	cfg := localRuntimeConfig()
	cfg.Webhooks = config.Webhooks{Enabled: true, MaxAttempts: 3, Timeout: config.Duration{Duration: time.Second}, PollInterval: config.Duration{Duration: time.Second}, RetryCeiling: config.Duration{Duration: time.Hour}, PolicyFile: filepath.Join(t.TempDir(), "policy.json")}
	policy := `{"schemaVersion":1,"policyId":"receiver-policy","revision":1,"public":{"mode":"disabled"}}`
	if err := os.WriteFile(cfg.Webhooks.PolicyFile, []byte(policy), 0600); err != nil {
		t.Fatal(err)
	}
	instance, err := Build(context.Background(), BuildOptions{ConfigPath: writeRuntimeConfig(t, cfg), Role: config.RoleAll, Version: "test", Commit: "test", Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	instance.Close()
	cfg.Webhooks.Enabled = false
	cfg.Webhooks.PolicyFile += "-missing"
	instance, err = Build(context.Background(), BuildOptions{ConfigPath: writeRuntimeConfig(t, cfg), Role: config.RoleAll, Version: "test", Commit: "test", Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal("disabled webhooks opened a policy dependency", err)
	}
	instance.Close()
}
