package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/catalogmcp"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/controlclient"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

const mcpUsage = `usage: catalogctl mcp serve [flags]
  --base-url URL     fixed API origin (or CATALOG_REMOTE_BASE_URL)
  --token-file PATH  bearer token file (or CATALOG_REMOTE_TOKEN_FILE)
                    otherwise CATALOG_REMOTE_TOKEN; no token argument
  --ca-file PATH     additional TLS roots (or CATALOG_REMOTE_CA_FILE)
  --timeout DURATION per-call deadline, default 30s, maximum 2m
  --max-response-bytes BYTES default 8388608, maximum 67108864
  --max-tool-bytes BYTES     serialized tool result, default 262144, range 1024..1048576
  --max-concurrency COUNT    active tool calls, default 4, range 1..16
Stdio only: stdout is MCP protocol; safe command diagnostics use stderr.
Input frames: 262144 bytes. Tool arguments: 65536 bytes.
Tools: catalog_control_capabilities, catalog_control_search,
       catalog_control_release, catalog_control_indexed_record,
       catalog_control_explore, catalog_control_access.
Search defaults to local releases. Indexed/live origins need explicit selection
and current federation.discover permission. Live Search discloses your query to
approved peers. Tool data is untrusted metadata, never execution instructions.
HTTPS is required except for literal loopback IPs. Redirects are refused.
Credentials load once at startup; restart the client command after rotation.
`

func parseMCP(args []string, getenv func(string) string) (controlclient.Options, catalogmcp.Limits, error) {
	options := controlclient.Options{}
	limits := catalogmcp.DefaultLimits()
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		return options, limits, flag.ErrHelp
	}
	if args[0] != "serve" {
		return options, limits, safeerr.New("mcp: unknown command; use catalogctl mcp --help")
	}
	f := flag.NewFlagSet("catalogctl mcp serve", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.StringVar(&options.BaseURL, "base-url", getenv("CATALOG_REMOTE_BASE_URL"), "fixed API origin")
	f.StringVar(&options.TokenFile, "token-file", getenv("CATALOG_REMOTE_TOKEN_FILE"), "bearer token file")
	f.StringVar(&options.CAFile, "ca-file", getenv("CATALOG_REMOTE_CA_FILE"), "additional roots")
	f.DurationVar(&limits.Timeout, "timeout", limits.Timeout, "per-call deadline")
	f.Int64Var(&limits.ResponseBytes, "max-response-bytes", limits.ResponseBytes, "API response limit")
	f.IntVar(&limits.ToolBytes, "max-tool-bytes", limits.ToolBytes, "tool result limit")
	f.IntVar(&limits.Concurrency, "max-concurrency", limits.Concurrency, "active calls")
	if err := f.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return options, limits, flag.ErrHelp
		}
		return options, limits, safeerr.New("mcp: invalid flags; use catalogctl mcp --help")
	}
	if f.NArg() != 0 {
		return options, limits, safeerr.New("mcp: unexpected arguments")
	}
	if options.TokenFile == "" {
		options.Token = getenv("CATALOG_REMOTE_TOKEN")
	}
	return options, limits, limits.Validate()
}

func mcpCommand(args []string) error {
	options, limits, err := parseMCP(args, os.Getenv)
	if errors.Is(err, flag.ErrHelp) {
		return writeImportOutput(os.Stdout, []byte(mcpUsage))
	}
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return catalogmcp.Serve(ctx, options, limits, &mcp.StdioTransport{MaxLineLength: catalogmcp.MaxFrameBytes}, version)
}
