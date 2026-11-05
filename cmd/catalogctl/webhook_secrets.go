// Webhook secret maintenance parses explicit tenant, key-file and batch inputs
// before invoking schema-owner re-encryption.

package main

import (
	"context"
	"encoding/base64"
	"io"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/database"
	diagnostic "github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/diagnostics"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

// repairWebhookSecrets requires stopped-runtime attestation and a bounded tenant batch before re-encrypting mounted-key secrets.
func repairWebhookSecrets(arguments []string) error {
	const usage = "usage: catalogctl repair webhook-secrets --runtimes-stopped --tenant <uuid> --reason <text> --old-key-file <path> --new-key-file <path> [--after <uuid>] [--limit <1-100>] [--config <path>]"
	flags := diagnostic.Flags("catalogctl repair webhook-secrets")
	configPath := flags.String("config", "", "optional JSON configuration baseline")
	tenantID := flags.String("tenant", "", "tenant whose retained endpoint secrets are verified")
	afterID := flags.String("after", "", "last committed endpoint UUID; omit to verify from the beginning")
	reason := flags.String("reason", "", "required operator reason recorded in audit")
	oldFile := flags.String("old-key-file", "", "base64 source encryption key file")
	newFile := flags.String("new-key-file", "", "base64 replacement encryption key file")
	stopped := flags.Bool("runtimes-stopped", false, "attest all API and worker processes remain stopped until maintenance completes")
	limit := flags.Int("limit", 100, "maximum endpoints verified in this atomic batch")
	if err := diagnostic.Parse(flags, arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || !*stopped || *oldFile == "" || *newFile == "" || database.ValidateWebhookReencryptInput(*tenantID, *afterID, *reason, *limit) != nil {
		return safeerr.New(usage)
	}
	oldKey, err := readWebhookMaintenanceKey(*oldFile)
	if err != nil {
		return err
	}
	defer clear(oldKey)
	newKey, err := readWebhookMaintenanceKey(*newFile)
	if err != nil {
		return err
	}
	defer clear(newKey)
	configuration, err := config.LoadFor(*configPath, config.RoleMigration)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := database.Open(ctx, configuration.Database.DSN, databasePoolConfig(configuration.Database))
	if err != nil {
		return err
	}
	defer pool.Close()
	result, err := database.ReencryptWebhookSecrets(ctx, pool, *tenantID, *afterID, *reason, *limit, oldKey, newKey)
	if err != nil {
		return err
	}
	return printJSON(result)
}

// readWebhookMaintenanceKey reads a bounded regular file containing exactly one base64-encoded 32-byte key and clears temporary bytes.
func readWebhookMaintenanceKey(path string) ([]byte, error) {
	// Nonblocking open lets the regular-file check reject a FIFO without waiting
	// for a writer, including when a mounted path changes during maintenance.
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0) // #nosec G304 -- Operator-selected input; regular-file and byte checks follow.
	if err != nil {
		return nil, safeerr.New("webhook maintenance key file could not be opened")
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1024 {
		return nil, safeerr.New("webhook maintenance key must be a regular file of at most 1024 bytes")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 1025))
	defer clear(raw)
	if err != nil || len(raw) > 1024 {
		return nil, safeerr.New("webhook maintenance key file could not be read within its bound")
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(key) != 32 {
		clear(key)
		return nil, safeerr.New("webhook maintenance key file must contain base64 for exactly 32 bytes")
	}
	return key, nil
}
