// Remote commands parse explicit API-client inputs and emit bounded JSON envelopes
// without loading the service's runtime configuration or database credentials.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/controlclient"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

const remoteUsage = `usage: catalogctl remote <capabilities|search|record|job|source-compare|reindex|trust-request|trust-result> [flags]
  --base-url URL     API origin (or CATALOG_REMOTE_BASE_URL)
  --token-file PATH  bearer token file (or CATALOG_REMOTE_TOKEN_FILE)
                    otherwise use CATALOG_REMOTE_TOKEN; no token argument
  --login-file PATH  explicit human login file; excludes all bearer sources
  --issuer-ca-file PATH additional issuer roots for login-file refresh
  --ca-file PATH     additional TLS roots (or CATALOG_REMOTE_CA_FILE)
  --timeout DURATION whole request deadline, default 30s, maximum 2m
  record/job: --id UUID (record requires the ListingRelease ID)
  record: --max-response-bytes BYTES (default 8388608, maximum 67108864)
  source-compare: --before-snapshot-id UUID --after-snapshot-id UUID
                  --view changes|all (default changes) --limit 1-100 (default 25)
                  --cursor VALUE; reads one page of saved source membership
  search: --text TEXT --kind KIND (repeatable) --tag TAG (repeatable)
          --license VALUE --category VALUE (repeatable)
          --origin local|indexed|live (repeatable) --namespace UUID (local only)
          --publisher VALUE --visibility private|tenant|public
          --limit 1-100 (default 25) --cursor VALUE
  reindex: --reason TEXT --idempotency-key KEY [--snapshot-id UUID]
           requires operations.reindex and the configured OAuth scope; returns a Job to inspect with remote job
  trust-request: --source-id UUID --request-file JSON --idempotency-key KEY
  trust-result: --source-id UUID --id JOB_UUID
Flags follow the command. Each API operation is sent once; login-file refresh
can add issuer and identity checks. Output is a JSON envelope containing data
and meta.requestId, plus meta.etag when present.
HTTPS is required except for literal loopback IPs. Redirects are refused.
`

type remoteOptions struct {
	body             json.RawMessage
	expectedStatus   int
	ifMatch          string
	maxResponseBytes int64
	baseURL          string
	tokenFile        string
	loginFile        string
	issuerCAFile     string
	caFile           string
	token            string
	timeout          time.Duration
	path             string
	search           *remoteSearch
	trustRequest     json.RawMessage
	reindex          *remoteReindex
	idempotencyKey   string
}

// remoteReindex is the focused administrative command, not a provider selector.
type remoteReindex struct {
	Reason     string `json:"reason"`
	SnapshotID string `json:"snapshotId,omitempty"`
}

// remoteSearch mirrors only the selected request, not the extensible response
// models. Contract tests bind this small handwritten slice to Control OpenAPI.
type remoteSearch = controlclient.Search

// remote cancels the single remote operation on process interruption or termination.
func remote(arguments []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runRemote(ctx, arguments, os.Getenv, os.Stdout)
}

// runRemote never loads a runtime configuration or database identity. Its
// injected environment and output keep CLI behavior testable without globals.
func runRemote(ctx context.Context, arguments []string, getenv func(string) string, output io.Writer) error {
	options, err := parseRemote(arguments, getenv)
	if errors.Is(err, flag.ErrHelp) {
		_, err = io.WriteString(output, remoteUsage)
		return err
	}
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, options.timeout)
	defer cancel()
	result, err := requestRemote(ctx, options)
	if err != nil {
		return err
	}
	encoded, err := remoteJSON(result)
	if err != nil {
		return safeerr.New("remote: cannot encode response")
	}
	encoded = append(encoded, '\n')
	if count, err := output.Write(encoded); err != nil || count != len(encoded) {
		return safeerr.New("remote: cannot write output")
	}
	return nil
}

// parseRemote selects an allowlisted read route and validates explicit flags without echoing potentially sensitive bad inputs.
func parseRemote(arguments []string, getenv func(string) string) (remoteOptions, error) {
	var options remoteOptions
	if len(arguments) == 0 || arguments[0] == "help" || arguments[0] == "--help" || arguments[0] == "-h" {
		return options, flag.ErrHelp
	}
	action := arguments[0]
	if action != "capabilities" && action != "search" && action != "record" && action != "job" && action != "source-compare" && action != "reindex" && action != "trust-request" && action != "trust-result" {
		return options, safeerr.New("remote: unknown command; use catalogctl remote --help")
	}
	flags := flag.NewFlagSet("catalogctl remote", flag.ContinueOnError)
	// The flag package echoes invalid values. Suppress that output because an
	// accidental --token or URL argument may contain a secret or terminal code.
	// runRemote owns help through its supplied writer; the generic command
	// parser would also write help directly to process stdout.
	flags.SetOutput(io.Discard)
	flags.StringVar(&options.baseURL, "base-url", getenv("CATALOG_REMOTE_BASE_URL"), "API origin")
	flags.StringVar(&options.tokenFile, "token-file", getenv("CATALOG_REMOTE_TOKEN_FILE"), "token file")
	flags.StringVar(&options.loginFile, "login-file", "", "explicit human login credentials")
	flags.StringVar(&options.issuerCAFile, "issuer-ca-file", "", "issuer roots for refresh")
	flags.StringVar(&options.caFile, "ca-file", getenv("CATALOG_REMOTE_CA_FILE"), "additional roots")
	flags.DurationVar(&options.timeout, "timeout", 30*time.Second, "whole request timeout")
	options.maxResponseBytes = remoteMaxBody
	if action == "record" {
		flags.Int64Var(&options.maxResponseBytes, "max-response-bytes", remoteMaxBody, "exact record response byte limit")
	}
	var identifier, sourceID, requestFile string
	var beforeSnapshot, afterSnapshot, comparisonView, comparisonCursor string
	var comparisonLimit int
	if action == "source-compare" {
		flags.StringVar(&beforeSnapshot, "before-snapshot-id", "", "before source snapshot UUID")
		flags.StringVar(&afterSnapshot, "after-snapshot-id", "", "after source snapshot UUID")
		flags.StringVar(&comparisonView, "view", "changes", "changes or all entries")
		flags.IntVar(&comparisonLimit, "limit", 25, "page limit")
		flags.StringVar(&comparisonCursor, "cursor", "", "opaque continuation")
	}
	if action == "trust-request" || action == "trust-result" {
		flags.StringVar(&sourceID, "source-id", "", "source UUID")
		if action == "trust-request" {
			flags.StringVar(&requestFile, "request-file", "", "closed assessment request JSON")
			flags.StringVar(&options.idempotencyKey, "idempotency-key", "", "command replay key")
		} else {
			flags.StringVar(&identifier, "id", "", "assessment Job UUID")
		}
	}
	if action == "reindex" {
		options.reindex = &remoteReindex{}
		flags.StringVar(&options.reindex.Reason, "reason", "", "audit reason")
		flags.StringVar(&options.reindex.SnapshotID, "snapshot-id", "", "active public snapshot UUID")
		flags.StringVar(&options.idempotencyKey, "idempotency-key", "", "stable command retry key")
	}
	if action == "record" || action == "job" {
		flags.StringVar(&identifier, "id", "", "UUID")
	}
	if action == "search" {
		query := &remoteSearch{}
		options.search = query
		flags.StringVar(&query.Text, "text", "", "search text")
		flags.StringVar(&query.Publisher, "publisher", "", "publisher")
		flags.StringVar(&query.License, "license", "", "exact license declaration")
		flags.StringVar(&query.NamespaceID, "namespace", "", "local namespace UUID")
		flags.Func("origin", "repeatable provenance origin", func(value string) error {
			if len(query.Origins) >= 3 {
				return safeerr.New("too many origins")
			}
			query.Origins = append(query.Origins, catalog.SearchResultType(value))
			return nil
		})
		flags.Func("category", "repeatable category", func(value string) error {
			if len(query.Categories) >= 16 {
				return safeerr.New("too many categories")
			}
			query.Categories = append(query.Categories, value)
			return nil
		})
		flags.StringVar(&query.Cursor, "cursor", "", "opaque cursor")
		flags.IntVar(&query.Limit, "limit", 25, "page limit")
		flags.Func("visibility", "visibility", func(value string) error {
			query.Visibility = catalog.Visibility(value)
			return nil
		})
		flags.Func("kind", "repeatable kind", func(value string) error {
			if len(query.Kinds) >= 4 {
				return safeerr.New("too many kinds")
			}
			query.Kinds = append(query.Kinds, catalog.Kind(value))
			return nil
		})
		flags.Func("tag", "repeatable tag", func(value string) error {
			if len(query.Tags) >= 32 {
				return safeerr.New("too many tags")
			}
			query.Tags = append(query.Tags, value)
			return nil
		})
	}
	if err := flags.Parse(arguments[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return options, flag.ErrHelp
		}
		return options, safeerr.New("remote: invalid flags; use catalogctl remote --help")
	}
	if flags.NArg() != 0 || options.timeout <= 0 || options.timeout > 2*time.Minute {
		return options, safeerr.New("remote: unexpected arguments or timeout outside (0, 2m]")
	}
	if options.maxResponseBytes < 1 || options.maxResponseBytes > 64<<20 {
		return options, safeerr.New("remote: --max-response-bytes must be 1..67108864")
	}
	switch action {
	case "source-compare":
		if !identity.ValidTenantID(beforeSnapshot) || !identity.ValidTenantID(afterSnapshot) {
			return options, safeerr.New("remote: source-compare requires before and after source snapshot UUIDs")
		}
		if comparisonView != "changes" && comparisonView != "all" || comparisonLimit < 1 || comparisonLimit > 100 || len(comparisonCursor) > 2048 || !utf8.ValidString(comparisonCursor) {
			return options, safeerr.New("remote: source-compare requires view changes or all, limit 1-100 and a cursor of at most 2048 bytes")
		}
		query := url.Values{"otherSnapshotId": {afterSnapshot}, "view": {comparisonView}, "limit": {strconv.Itoa(comparisonLimit)}}
		if comparisonCursor != "" {
			query.Set("cursor", comparisonCursor)
		}
		options.path = "/api/v1/federation/snapshots/" + beforeSnapshot + "/comparison?" + query.Encode()
	case "trust-request", "trust-result":
		if !identity.ValidTenantID(sourceID) {
			return options, safeerr.New("remote: --source-id must be a UUID")
		}
		options.path = "/api/v1/federation/sources/" + sourceID + "/trust-assessments"
		if action == "trust-result" {
			if !identity.ValidTenantID(identifier) {
				return options, safeerr.New("remote: --id must be a Job UUID")
			}
			options.path += "/" + identifier
		} else {
			if len(options.idempotencyKey) < 8 || len(options.idempotencyKey) > 128 || strings.IndexFunc(options.idempotencyKey, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
				return options, safeerr.New("remote: trust-request requires an 8-128 character ASCII idempotency key")
			}
			raw, err := readTrustFile(requestFile, 64<<10)
			if err != nil {
				return options, safeerr.New("remote: cannot read bounded assessment request file")
			}
			var selection federation.TrustRequest
			if jsondoc.Decode(raw, &selection) != nil {
				return options, safeerr.New("remote: assessment request must be closed valid JSON")
			}
			options.trustRequest = raw
		}
	case "reindex":
		if strings.TrimSpace(options.reindex.Reason) == "" || len(options.reindex.Reason) > 2000 || !utf8.ValidString(options.reindex.Reason) || options.reindex.SnapshotID != "" && !identity.ValidTenantID(options.reindex.SnapshotID) || len(options.idempotencyKey) < 8 || len(options.idempotencyKey) > 128 || strings.IndexFunc(options.idempotencyKey, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
			return options, safeerr.New("remote: reindex requires a bounded reason, an 8-128 character ASCII idempotency key and an optional snapshot UUID")
		}
		options.path = "/api/v1/catalog:reindex"
	case "capabilities":
		options.path = "/api/v1/capabilities"
	case "search":
		if err := options.search.Validate(); err != nil {
			return options, err
		}
		options.path = "/api/v1/catalog/search"
	case "record", "job":
		if !identity.ValidTenantID(identifier) {
			return options, safeerr.New("remote: --id must be a UUID; record lookup requires the ListingRelease ID")
		}
		options.path = "/api/v1/catalog/resources/" + identifier
		if action == "job" {
			options.path = "/api/v1/jobs/" + identifier
		}
	}
	if err := selectRemoteCredentials(&options, getenv); err != nil {
		return options, err
	}
	return options, nil
}

func remoteJSON(value any) ([]byte, error) { return controlclient.JSON(value) }
