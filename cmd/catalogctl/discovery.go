// Anonymous publication commands never load runtime configuration or credentials.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/controlclient"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

const discoveryUsage = `usage: catalogctl discovery <manifest|list> [flags]
  --base-url URL    required anonymous publication origin; no environment default
  --ca-file PATH    additional TLS roots; maximum 1 MiB
  --timeout DURATION whole request deadline, default 30s, maximum 2m
  list: --page-size 1-100 (default 20) --page-token VALUE
        --filter EXPRESSION (maximum 2048 bytes)
        --order-by displayName|updatedAt (optional DESC)
Flags follow the command. Each invocation makes one manifest or List request.
Output is a JSON envelope with data and meta.requestId, plus meta.etag when present.
List data.pageToken is exact opaque continuation, including on an empty page.
Keep the same origin, filter, order and page size when continuing explicitly.
Responses are limited to 8 MiB. HTTPS is required except for literal loopback IPs.
Redirects, authentication flags, Search and automatic traversal are unsupported.
Ambient credentials, login caches, runtime settings and proxy variables are ignored.
Returned locations remain metadata; no entries, artifacts or private records are fetched.
`

type discoveryOptions struct {
	baseURL, caFile, path string
	timeout               time.Duration
	pageSize              int
}

func discovery(arguments []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runDiscovery(ctx, arguments, os.Stdout)
}

func runDiscovery(ctx context.Context, arguments []string, output io.Writer) error {
	options, err := parseDiscovery(arguments)
	if errors.Is(err, flag.ErrHelp) {
		_, err = io.WriteString(output, discoveryUsage)
		return err
	}
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, options.timeout)
	defer cancel()
	session, err := controlclient.NewAnonymous(ctx, options.baseURL, options.caFile, options.timeout)
	if err != nil {
		return err
	}
	defer session.Close()
	result, err := exchangeDiscovery(ctx, session.Client, session.Origin, options)
	if err != nil {
		return err
	}
	encoded, err := controlclient.JSON(result)
	if err != nil {
		return safeerr.New("discovery: cannot encode response")
	}
	encoded = append(encoded, '\n')
	if count, err := output.Write(encoded); err != nil || count != len(encoded) {
		return safeerr.New("discovery: cannot write output")
	}
	return nil
}

// parseDiscovery accepts no authentication flags and reads no environment or files.
// The API owns filter grammar; the CLI bounds it and preserves it without rewriting.
func parseDiscovery(arguments []string) (discoveryOptions, error) {
	var options discoveryOptions
	if len(arguments) == 0 || arguments[0] == "help" || arguments[0] == "--help" || arguments[0] == "-h" {
		return options, flag.ErrHelp
	}
	action := arguments[0]
	if action != "manifest" && action != "list" {
		return options, safeerr.New("discovery: unknown command; use catalogctl discovery --help")
	}
	flags := flag.NewFlagSet("catalogctl discovery", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&options.baseURL, "base-url", "", "required origin")
	flags.StringVar(&options.caFile, "ca-file", "", "additional TLS roots")
	flags.DurationVar(&options.timeout, "timeout", 30*time.Second, "whole request deadline")
	var filter, order, token string
	if action == "list" {
		flags.IntVar(&options.pageSize, "page-size", 20, "entries per page")
		flags.StringVar(&filter, "filter", "", "ARD List expression")
		flags.StringVar(&order, "order-by", "", "ARD List ordering")
		flags.StringVar(&token, "page-token", "", "exact opaque continuation")
	}
	if err := flags.Parse(arguments[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return options, flag.ErrHelp
		}
		return options, safeerr.New("discovery: invalid flags; use catalogctl discovery --help")
	}
	if flags.NArg() != 0 || options.timeout <= 0 || options.timeout > 2*time.Minute {
		return options, safeerr.New("discovery: unexpected arguments or invalid timeout (maximum 2m)")
	}
	origin, err := controlclient.Origin(options.baseURL)
	if err != nil {
		return options, err
	}
	options.baseURL = origin
	options.path = "/.well-known/ard.json"
	if action == "list" {
		bounded := func(value string, limit int) bool {
			return len(value) <= limit && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
		}
		if options.pageSize < 1 || options.pageSize > 100 || !bounded(filter, 2048) || !bounded(token, 4096) {
			return options, safeerr.New("discovery: invalid List page size, filter or token")
		}
		switch order {
		case "", "displayName", "displayName DESC", "updatedAt", "updatedAt DESC":
		default:
			return options, safeerr.New("discovery: invalid List ordering")
		}
		query := url.Values{"pageSize": {strconv.Itoa(options.pageSize)}}
		for key, value := range map[string]string{"filter": filter, "orderBy": order, "pageToken": token} {
			if value != "" {
				query.Set(key, value)
			}
		}
		options.path = "/ard/v1/agents?" + query.Encode()
	}
	return options, nil
}

// exchangeDiscovery makes one fixed publication read and never copies credentials.
func exchangeDiscovery(ctx context.Context, client *http.Client, origin string, options discoveryOptions) (remoteEnvelope, error) {
	var result remoteEnvelope
	request, err := controlclient.NewAnonymousRequest(ctx, origin, options.path)
	if err != nil {
		return result, safeerr.New("discovery: cannot construct request")
	}
	response, err := client.Do(request) // #nosec G704 -- Explicit operator origin and code-selected manifest/List path; no redirects, proxies or credentials.
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
			return result, safeerr.New("discovery: request cancelled or deadline exceeded")
		}
		return result, safeerr.New("discovery: request failed; check publication origin, connectivity and TLS trust")
	}
	defer func() { _ = response.Body.Close() }()
	failure := remoteAPIError{Status: response.StatusCode, Code: "invalid_response", Detail: "invalid or oversized publication response"}
	for _, name := range []string{"X-Request-ID", "ETag"} {
		value := response.Header.Get(name)
		if len(response.Header.Values(name)) > 1 || !utf8.ValidString(value) || len(value) > 4096 || strings.ContainsAny(value, "\r\n\x00") {
			return result, failure
		}
	}
	failure.RequestID = controlclient.Diagnostic(response.Header.Get("X-Request-ID"), "", 256)
	raw, err := io.ReadAll(io.LimitReader(response.Body, controlclient.MaxBody+1))
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
		return result, safeerr.New("discovery: request cancelled or deadline exceeded")
	}
	if err != nil || len(raw) > controlclient.MaxBody {
		return result, failure
	}
	media, params, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if len(response.Header.Values("Content-Type")) != 1 || mediaErr != nil || media != "application/json" || len(params) > 1 || len(params) == 1 && !strings.EqualFold(params["charset"], "utf-8") || !utf8.Valid(raw) || jsondoc.Validate(raw) != nil || !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		return result, failure
	}
	if response.StatusCode != http.StatusOK {
		// Preserve finite ARD refusal codes, not untrusted server messages or URLs.
		var problem struct {
			Code string `json:"errorCode"`
		}
		_ = json.Unmarshal(raw, &problem)
		failure.Code, failure.Detail = "http_error", "publication request refused; check host admission, active publication and continuation"
		switch problem.Code {
		case "NOT_FOUND", "UNAUTHENTICATED", "FORBIDDEN", "INVALID_ARGUMENT", "PUBLICATION_NOT_READY", "REQUEST_DEADLINE_EXCEEDED", "WORK_LIMIT_EXCEEDED", "DEPENDENCY_UNAVAILABLE", "INTERNAL":
			failure.Code = problem.Code
		}
		return result, failure
	}
	if !validDiscoveryData(raw, options.pageSize) {
		return result, failure
	}
	result.Data = raw
	result.Meta.RequestID = response.Header.Get("X-Request-ID")
	result.Meta.ETag = response.Header.Get("ETag")
	return result, nil
}

// validDiscoveryData checks the consumed shape without interpreting metadata URLs
// or rejecting extensible standards fields. The byte ceiling bounds all metadata.
func validDiscoveryData(raw []byte, pageSize int) bool {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return false
	}
	key := "entries"
	if pageSize != 0 {
		key = "items"
	}
	var entries []map[string]json.RawMessage
	if json.Unmarshal(object[key], &entries) != nil || entries == nil || pageSize > 0 && len(entries) > pageSize {
		return false
	}
	for _, entry := range entries {
		for _, key := range []string{"identifier", "displayName", "type"} {
			var value string
			if len(entry[key]) == 0 || entry[key][0] != '"' || json.Unmarshal(entry[key], &value) != nil {
				return false
			}
		}
		_, location := entry["url"]
		_, data := entry["data"]
		if !location && !data {
			return false
		}
		if location {
			var value string
			if len(entry["url"]) == 0 || entry["url"][0] != '"' || json.Unmarshal(entry["url"], &value) != nil {
				return false
			}
		}
		if data && !bytes.HasPrefix(bytes.TrimSpace(entry["data"]), []byte("{")) {
			return false
		}
	}
	if pageSize > 0 {
		var total int
		if bytes.Equal(object["total"], []byte("null")) || json.Unmarshal(object["total"], &total) != nil || total < len(entries) {
			return false
		}
		if rawToken, ok := object["pageToken"]; ok {
			var token string
			if bytes.Equal(rawToken, []byte("null")) || json.Unmarshal(rawToken, &token) != nil || len(token) > 4096 || strings.ContainsRune(token, 0) {
				return false
			}
		}
	}
	return true
}
