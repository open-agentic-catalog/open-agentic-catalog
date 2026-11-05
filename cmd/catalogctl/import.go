// Native import prepares Control API requests offline and applies drafts through
// explicit origin-bound remote inputs. It never loads service configuration.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

const importUsage = `usage: catalogctl import preview --file PATH [flags]
  --file PATH                reusable draft JSON, or format: 1 native manifest
  --namespace-id UUID        destination Namespace for a new Resource
  --resource-id UUID         exact existing Resource for a new Version
  --publisher-id UUID        destination Publisher for included Listing metadata
  --listing-id UUID          exact existing Listing; otherwise check zero or one
  --content-only             omit supplied Listing metadata
  --package-file N=PATH      Skill archive for representation N (1-based)
  --package-directory N=PATH Skill directory for representation N (ZIP only)
Destination/package flags apply only to reusable files. Package paths are
relative to the draft file's directory. Native manifests retain batch support.
  --max-request-bytes BYTES   encoded request ceiling, default 2097152, maximum 67108864
  --timeout DURATION         whole preview deadline, default 2m, maximum 10m
Preview reads confined local files and uses pinned native validators. It makes
no network requests or writes. JSON output omits native documents and archives.
Remote content, permissions, identities and server limits remain unchecked.
` + importRemoteUsage

const importRemoteUsage = `usage: catalogctl import <apply|resume> --file PATH --state-file PATH [flags]
  --base-url URL             API origin (or CATALOG_REMOTE_BASE_URL)
  --token-file PATH          bearer file (or CATALOG_REMOTE_TOKEN_FILE)
                             otherwise use CATALOG_REMOTE_TOKEN; no token flag
  --login-file PATH          explicit human login file; excludes bearer sources
  --issuer-ca-file PATH      issuer roots for login-file refresh
  --ca-file PATH              additional TLS roots (or CATALOG_REMOTE_CA_FILE)
  --max-response-bytes BYTES  default 8388608, maximum 67108864
Apply requires a new state file in a private directory. Resume requires the
unchanged input file and assets. Both create drafts only; no automatic retries,
review, release, publication, installation or execution. Preserve the receipt.

`

func importCommand(arguments []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runImport(ctx, arguments, os.Stdout)
}

func runImport(ctx context.Context, arguments []string, output io.Writer) error {
	if len(arguments) == 1 && (arguments[0] == "--help" || arguments[0] == "-h" || arguments[0] == "help") {
		return writeImportOutput(output, []byte(importUsage))
	}
	if len(arguments) == 0 || (arguments[0] != "preview" && arguments[0] != "apply" && arguments[0] != "resume") {
		return safeerr.New("use catalogctl import <preview|apply|resume> --file PATH; see --help")
	}
	flags := flag.NewFlagSet("import "+arguments[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	filename := flags.String("file", "", "local native manifest or reusable JSON draft")
	var draft importDraftOptions
	flags.StringVar(&draft.NamespaceID, "namespace-id", "", "destination Namespace")
	flags.StringVar(&draft.ResourceID, "resource-id", "", "exact Resource for a new Version")
	flags.StringVar(&draft.PublisherID, "publisher-id", "", "destination Publisher")
	flags.StringVar(&draft.ListingID, "listing-id", "", "exact destination Listing")
	flags.BoolVar(&draft.ContentOnly, "content-only", false, "omit Listing metadata")
	flags.Var(&draft.PackageFiles, "package-file", "representation index and relative archive path")
	flags.Var(&draft.PackageDirectories, "package-directory", "representation index and relative directory")
	requestLimit := flags.Int("max-request-bytes", 2<<20, "encoded request limit")
	timeout := flags.Duration("timeout", 2*time.Minute, "whole preview deadline")
	var options remoteOptions
	var stateFile string
	if arguments[0] != "preview" {
		flags.StringVar(&stateFile, "state-file", "", "private recovery receipt")
		flags.StringVar(&options.baseURL, "base-url", os.Getenv("CATALOG_REMOTE_BASE_URL"), "API origin")
		flags.StringVar(&options.tokenFile, "token-file", os.Getenv("CATALOG_REMOTE_TOKEN_FILE"), "token file")
		flags.StringVar(&options.loginFile, "login-file", "", "explicit human login credentials")
		flags.StringVar(&options.issuerCAFile, "issuer-ca-file", "", "issuer roots for refresh")
		flags.StringVar(&options.caFile, "ca-file", os.Getenv("CATALOG_REMOTE_CA_FILE"), "additional TLS roots")
		flags.Int64Var(&options.maxResponseBytes, "max-response-bytes", remoteMaxBody, "response byte limit")
	}
	if err := flags.Parse(arguments[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return writeImportOutput(output, []byte(importUsage))
		}
		return safeerr.Wrap(err, "invalid import flags; use --help")
	}
	if arguments[0] != "preview" {
		if err := selectRemoteCredentials(&options, os.Getenv); err != nil {
			return err
		}
	}
	if flags.NArg() != 0 || *filename == "" || *requestLimit < 1 || *requestLimit > importMaxPreparedBytes || *timeout <= 0 || *timeout > 10*time.Minute {
		return safeerr.New("import requires --file, a request limit of 1-67108864 bytes and a timeout greater than zero and at most 10m")
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	plan, err := prepareImportInput(ctx, *filename, *requestLimit, draft)
	if err != nil {
		return err
	}
	if arguments[0] != "preview" {
		if stateFile == "" || options.maxResponseBytes < 1 || options.maxResponseBytes > 64<<20 {
			return safeerr.New("import: apply/resume requires --state-file and a response limit of 1-67108864 bytes")
		}
		options.timeout = *timeout
		report, applyErr := applyImport(ctx, plan, options, stateFile, arguments[0] == "resume")
		encoded, err := encodeImportReport(report)
		if err != nil {
			return safeerr.New("import: cannot encode outcome")
		}
		if err = writeImportOutput(output, append(encoded, '\n')); err != nil {
			return err
		}
		return applyErr
	}
	encoded, err := encodeImportReport(plan.Preview)
	if err != nil {
		return safeerr.Wrap(err, "cannot encode import preview")
	}
	if err := writeImportOutput(output, append(encoded, '\n')); err != nil {
		return err
	}
	if !plan.Preview.LocallyValid {
		return safeerr.New("import preview found local validation errors; inspect the JSON findings")
	}
	return nil
}

func writeImportOutput(output io.Writer, body []byte) error {
	n, err := output.Write(body)
	if err == nil && n != len(body) {
		err = io.ErrShortWrite
	}
	return safeerr.Wrap(err, "cannot write import output")
}

// Output is bounded independently of request, response and receipt budgets.
func encodeImportReport(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil || len(raw)+1 > importMaxReportBytes {
		return nil, safeerr.New("import report exceeds its 2 MiB encoded limit; preserve existing progress")
	}
	return raw, nil
}
