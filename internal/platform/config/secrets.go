// Runtime key loading keeps secret values outside Config and its diagnostic
// projection, with explicit direct/file selection and production requirements.

package config

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

const maxSecretFileBytes = 64 << 10

// ValidateRuntimeSecrets checks only keys required by this role and enabled
// capabilities. Migration commands need neither runtime key. It reads current
// environment/file inputs but does not retain keys in the Config or its report.
func ValidateRuntimeSecrets(configuration Config, role Role) error {
	if !role.Valid() {
		return fmt.Errorf("config: unsupported role %q", role)
	}
	if role == RoleMigration {
		return nil
	}
	if _, err := CursorSigningKey(configuration); err != nil {
		return err
	}
	if configuration.Webhooks.Enabled {
		if _, err := WebhookEncryptionKey(configuration); err != nil {
			return err
		}
	}
	return nil
}

// CursorSigningKey decodes the mutually exclusive direct or file input.
// Production requires an injected key; non-production has a deterministic
// development fallback that must never be treated as a deployment secret.
func CursorSigningKey(configuration Config) ([]byte, error) {
	return runtimeKey(configuration, "CATALOG_CURSOR_SIGNING_KEY", "CATALOG_CURSOR_SIGNING_KEY_FILE", "cursor")
}

// WebhookEncryptionKey loads the webhook encryption key, with the
// same production requirement as CursorSigningKey. Call only when enabled;
// this function does not inspect the webhook feature flag.
func WebhookEncryptionKey(configuration Config) ([]byte, error) {
	return runtimeKey(configuration, "CATALOG_WEBHOOK_ENCRYPTION_KEY", "CATALOG_WEBHOOK_ENCRYPTION_KEY_FILE", "webhook")
}

// runtimeKey requires exactly 32 decoded bytes when configured. Only non-production
// inputs may use the deterministic service/tenant-scoped development fallback.
func runtimeKey(configuration Config, directName, fileName, purpose string) ([]byte, error) {
	encoded, exists, err := readEnvironmentSecret(directName, fileName, os.LookupEnv)
	if err != nil {
		return nil, err
	}
	if exists {
		key, decodeErr := base64.StdEncoding.DecodeString(encoded)
		if decodeErr != nil || len(key) != 32 {
			return nil, safeerr.New(fmt.Sprintf("%s or %s must provide base64 for exactly 32 bytes", directName, fileName))
		}
		return key, nil
	}
	if configuration.Environment == "production" {
		return nil, safeerr.New(fmt.Sprintf("%s or %s is required in production", directName, fileName))
	}
	scope := configuration.ServiceName
	if len(configuration.Discovery.Hosts) > 0 {
		scope += ":" + configuration.Discovery.Hosts[0].TenantID
	}
	key := sha256.Sum256([]byte("development-" + purpose + "-key:" + scope))
	return key[:], nil
}

// readEnvironmentSecret distinguishes an absent secret from malformed input and
// rejects simultaneous direct/file settings even when either value is empty.
func readEnvironmentSecret(directName, fileName string, lookup func(string) (string, bool)) (string, bool, error) {
	direct, directSet := lookup(directName)
	file, fileSet := lookup(fileName)
	if directSet && fileSet {
		return "", false, safeerr.New(fmt.Sprintf("%s and %s are mutually exclusive", directName, fileName))
	}
	if directSet {
		if err := validateSecretValue(directName, direct); err != nil {
			return "", false, err
		}
		return direct, true, nil
	}
	if !fileSet {
		return "", false, nil
	}
	if strings.TrimSpace(file) == "" {
		return "", false, safeerr.New(fmt.Sprintf("%s must not be empty when set", fileName))
	}
	value, err := readSecretFile(file)
	if err != nil {
		return "", false, safeerr.Wrap(fmt.Errorf("read %s: %w", fileName, err), "cannot read secret file selected by "+fileName)
	}
	if err := validateSecretValue(fileName, value); err != nil {
		return "", false, err
	}
	return value, true, nil
}

// readSecretFile reads at most 64 KiB and removes one terminal LF or CRLF. Remaining
// whitespace and embedded line breaks are left for the secret-value validator.
func readSecretFile(path string) (string, error) {
	file, err := os.Open(path) // #nosec G304 -- the deployment explicitly selects the mounted secret path.
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, maxSecretFileBytes+1))
	if err != nil {
		return "", err
	}
	if len(raw) > maxSecretFileBytes {
		return "", safeerr.New("secret file exceeds 64 KiB")
	}
	value := strings.TrimSuffix(string(raw), "\n")
	value = strings.TrimSuffix(value, "\r")
	return value, nil
}

// validateSecretValue accepts one bounded nonempty line and identifies failures
// by the setting name without including the secret value.
func validateSecretValue(name, value string) error {
	if value == "" {
		return safeerr.New(fmt.Sprintf("%s must not be empty when set", name))
	}
	if len(value) > maxSecretFileBytes {
		return safeerr.New(fmt.Sprintf("%s exceeds 64 KiB", name))
	}
	if strings.ContainsAny(value, "\r\n\x00") {
		return safeerr.New(fmt.Sprintf("%s must contain one non-empty line", name))
	}
	return nil
}
