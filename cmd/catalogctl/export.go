package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

const exportUsage = `usage: catalogctl export --release-id UUID --output-dir PATH [flags]
` + `usage: catalogctl export --format draft --version-revision-id UUID [--listing-revision-id UUID] --output-dir PATH [flags]
` + `usage: catalogctl export preview --query search|browse --output-dir PATH [flags]
` + `usage: catalogctl export batch --release-id UUID [--release-id UUID ...] --output-dir PATH [flags]
` + `usage: catalogctl export batch --selection-file PATH --output-dir PATH [flags]
  --representation-id UUID  native/client formats; required with several representations
  --format FORMAT          native (default), draft, mcp-vscode, mcp-claude-code, mcp-cursor
  --remote N               client formats only; 1-based original remotes[] position
                            required with several remotes
  --version-revision-id UUID exact revision for draft export, instead of --release-id
  --listing-revision-id UUID optional matching metadata with --version-revision-id
  --base-url URL            API origin (or CATALOG_REMOTE_BASE_URL)
  --token-file PATH         bearer token file (or CATALOG_REMOTE_TOKEN_FILE)
                            otherwise use CATALOG_REMOTE_TOKEN; no token argument
  --login-file PATH         explicit human login file; excludes bearer sources
  --issuer-ca-file PATH     issuer roots for login-file refresh
  --ca-file PATH            additional TLS roots (or CATALOG_REMOTE_CA_FILE)
  --timeout DURATION        whole export deadline, default 30s, maximum 2m
  --max-response-bytes N    JSON response limit, default 8388608, maximum 67108864
  --max-content-bytes N     document or draft file limit, default 8388608, maximum 67108864
  preview: --text TEXT --kind KIND (repeatable for Search) --tag TAG (repeatable)
           --namespace UUID --publisher VALUE --visibility private|tenant|public
           --license VALUE --category VALUE (repeatable; Search only)
           --limit 1-100 (default 25) --max-pages 1-100 (default 10)
  preview/batch: --max-records 1-64 (default 64)
                 --max-total-bytes N (default 67108864, maximum 268435456)
Preview reads local results only and writes selection.json before any download.
Batch freezes selected release IDs, saves each in its UUID directory and writes
batch.json with per-release outcomes. Failed/denied items exit unsuccessfully;
successful earlier files remain. Missing batch.json means an interrupted batch.
An incomplete preview stays incomplete in the batch report. These independent
reads are not a point-in-time backup. Selection files contain private identities.
The output directory must not exist; its parent must exist without symlinks.
Existing paths are never overwritten. Files are private and export.json is the
completion marker. native.json holds inline JSON; native.bin holds exact retained
bytes. The sidecar distinguishes their checksums from the recorded document digest.
Native format includes external package references and inspection data.
Draft mode saves draft.json and a completion sidecar. Release selection includes
its exact Listing metadata. Revision selection includes metadata only when selected.
Draft mode rejects representation selectors, including those in selection files.
The result reports browser text-size compatibility separately from Save readiness.
Draft Skill packages need separate content; Plugin references retain their checksum.
Client formats export one literal HTTPS remote as mcp.json (VS Code/Cursor) or
.mcp.json (Claude Code). Place or merge it yourself in .vscode/mcp.json,
.cursor/mcp.json or .mcp.json respectively. Headers are omitted; export.json
lists their names and remaining authentication setup. Client formats require
an eligible local MCP release and cannot export packages or unresolved templates.
Review private output before sharing. Loading it in a client can connect to the
remote server. Export does not edit settings or contact discovered endpoints.
No external URL is fetched, archive reconstructed, resource installed or code run.
The API origin requires HTTPS except for literal loopback IPs. Redirects are refused.
`

type exportOptions struct {
	remote                 remoteOptions
	releaseID              string
	representationID       string
	outputDir              string
	maxContentBytes        int64
	selection              *exportSelectionItem
	format                 string
	versionRevisionID      string
	listingRevisionID      string
	representationSelected bool
	remotePosition         int
	remoteSelected         bool
}

type exportResult struct {
	Format            int    `json:"format"`
	ReleaseID         string `json:"releaseId"`
	RepresentationID  string `json:"representationId"`
	VersionRevisionID string `json:"versionRevisionId,omitempty"`
	Disposition       string `json:"disposition"`
	BrowserCompatible *bool  `json:"browserCompatible,omitempty"`
}

func exportCommand(arguments []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runExport(ctx, arguments, os.Getenv, os.Stdout)
}

func parseExport(arguments []string, getenv func(string) string) (exportOptions, error) {
	var options exportOptions
	flags := flag.NewFlagSet("catalogctl export", flag.ContinueOnError)
	// Never echo supplied flags or environment-derived credentials in help/errors.
	flags.SetOutput(io.Discard)
	flags.StringVar(&options.releaseID, "release-id", "", "exact ListingRelease UUID")
	flags.StringVar(&options.representationID, "representation-id", "", "representation UUID")
	flags.StringVar(&options.format, "format", "native", "native, draft or an MCP client format")
	flags.IntVar(&options.remotePosition, "remote", 0, "original 1-based remotes position")
	flags.StringVar(&options.versionRevisionID, "version-revision-id", "", "exact revision for draft export")
	flags.StringVar(&options.listingRevisionID, "listing-revision-id", "", "optional exact Listing metadata")
	flags.StringVar(&options.outputDir, "output-dir", "", "new private directory")
	flags.StringVar(&options.remote.baseURL, "base-url", getenv("CATALOG_REMOTE_BASE_URL"), "API origin")
	flags.StringVar(&options.remote.tokenFile, "token-file", getenv("CATALOG_REMOTE_TOKEN_FILE"), "token file")
	flags.StringVar(&options.remote.loginFile, "login-file", "", "explicit human login credentials")
	flags.StringVar(&options.remote.issuerCAFile, "issuer-ca-file", "", "issuer roots for refresh")
	flags.StringVar(&options.remote.caFile, "ca-file", getenv("CATALOG_REMOTE_CA_FILE"), "additional roots")
	flags.DurationVar(&options.remote.timeout, "timeout", 30*time.Second, "whole export deadline")
	flags.Int64Var(&options.remote.maxResponseBytes, "max-response-bytes", remoteMaxBody, "record byte limit")
	flags.Int64Var(&options.maxContentBytes, "max-content-bytes", remoteMaxBody, "document byte limit")
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return options, flag.ErrHelp
		}
		return options, safeerr.New("export: invalid flags; use catalogctl export --help")
	}
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "remote" {
			options.remoteSelected = true
		}
		if f.Name == "representation-id" {
			options.representationSelected = true
		}
	})
	if flags.NArg() != 0 || options.outputDir == "" {
		return options, safeerr.New("export: require a release UUID, output directory and optional representation UUID")
	}
	if err := validateExportFormat(options); err != nil {
		return options, err
	}
	options.releaseID = strings.ToLower(options.releaseID)
	options.representationID = strings.ToLower(options.representationID)
	options.versionRevisionID = strings.ToLower(options.versionRevisionID)
	options.listingRevisionID = strings.ToLower(options.listingRevisionID)
	if options.remote.timeout <= 0 || options.remote.timeout > 2*time.Minute || options.remote.maxResponseBytes < 1 || options.remote.maxResponseBytes > 64<<20 || options.maxContentBytes < 1 || options.maxContentBytes > 64<<20 {
		return options, safeerr.New("export: timeout must be within (0, 2m] and byte limits within 1..67108864")
	}
	if err := selectRemoteCredentials(&options.remote, getenv); err != nil {
		return options, err
	}
	options.remote.path = "/api/v1/catalog/resources/" + options.releaseID
	return options, nil
}

func runExport(ctx context.Context, arguments []string, getenv func(string) string, output io.Writer) error {
	if len(arguments) > 0 && (arguments[0] == "preview" || arguments[0] == "batch") {
		return runExportBatch(ctx, arguments, getenv, output)
	}
	options, err := parseExport(arguments, getenv)
	if errors.Is(err, flag.ErrHelp) {
		var n int
		n, err = io.WriteString(output, exportUsage)
		if err == nil && n != len(exportUsage) {
			err = io.ErrShortWrite
		}
		return safeerr.Wrap(err, "export: cannot write help")
	}
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, options.remote.timeout)
	defer cancel()
	parent, name, err := openExportParent(options.outputDir)
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	session, err := newRemoteSession(ctx, options.remote)
	if err != nil {
		return err
	}
	defer session.close()
	manifest, content, err := prepareExport(ctx, session, options)
	if err != nil {
		return err
	}
	if err := writeExport(ctx, parent, name, manifest, content); err != nil {
		return err
	}
	// Never echo native documents, package references or caller paths to stdout.
	resultValue := exportResult{Format: 1, ReleaseID: manifest.ReleaseID, RepresentationID: manifest.RepresentationID, Disposition: manifest.Disposition, BrowserCompatible: manifest.BrowserCompatible}
	if options.format == "draft" {
		resultValue.VersionRevisionID = manifest.VersionRevisionID
	}
	result, err := json.Marshal(resultValue)
	if err != nil {
		return safeerr.New("export: cannot encode result; completed files remain available")
	}
	result = append(result, '\n')
	if n, err := output.Write(result); err != nil || n != len(result) {
		return safeerr.New("export: cannot write result; completed files remain available")
	}
	return nil
}
