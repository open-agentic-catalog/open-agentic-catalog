package main

import (
	"errors"
	"flag"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

func TestMCPDispatcherRefusesUnsafeFlags(t *testing.T) {
	err := runArgs([]string{"mcp", "serve", "--timeout", "PRIVATE_SENTINEL"})
	if err == nil || err.Error() != "mcp: invalid flags; use catalogctl mcp --help" || safeerr.Stage(err) != "mcp" {
		t.Fatalf("unexpected command diagnostic: %v", err)
	}
}

func TestMCPExplicitInputsAndSafeFlags(t *testing.T) {
	getenv := func(name string) string {
		return map[string]string{"CATALOG_REMOTE_BASE_URL": "https://catalog.test", "CATALOG_REMOTE_TOKEN": "test-token", "CATALOG_REMOTE_TOKEN_FILE": "mounted-token", "CATALOG_REMOTE_CA_FILE": "private-ca", "CATALOG_CONFIG": "/do-not-load"}[name]
	}
	options, limits, err := parseMCP([]string{"serve", "--timeout", "1s", "--max-concurrency", "2"}, getenv)
	if err != nil || options.BaseURL != "https://catalog.test" || options.TokenFile != "mounted-token" || options.Token != "" || options.CAFile != "private-ca" || limits.Timeout != time.Second || limits.Concurrency != 2 {
		t.Fatal(options, limits, err)
	}
	for _, args := range [][]string{{}, {"--help"}, {"serve", "--help"}} {
		if _, _, err := parseMCP(args, getenv); !errors.Is(err, flag.ErrHelp) {
			t.Fatal("missing help", err)
		}
	}
	for _, args := range [][]string{{"other"}, {"serve", "--token", "PRIVATE_SENTINEL"}, {"serve", "--timeout", "PRIVATE_SENTINEL"}, {"serve", "extra"}, {"serve", "--max-tool-bytes", "0"}, {"serve", "--max-concurrency", "17"}} {
		if _, _, err := parseMCP(args, getenv); err == nil || strings.Contains(err.Error(), "PRIVATE_SENTINEL") {
			t.Fatal("unsafe flags", args, err)
		}
	}
	options, _, err = parseMCP([]string{"serve"}, func(name string) string {
		if name == "CATALOG_REMOTE_TOKEN" {
			return "test-token"
		}
		return ""
	})
	if err != nil || options.Token != "test-token" {
		t.Fatal("environment token ignored", err)
	}
}
