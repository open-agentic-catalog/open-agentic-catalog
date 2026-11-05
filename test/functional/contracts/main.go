// Command contracts checks representative responses from the owned Compose API.
// It is test tooling, not part of either shipped binary.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	serviceapi "github.com/open-agentic-catalog/open-agentic-catalog/api"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

const maximumResponse = 8 << 20

// Never wrap untrusted bodies, headers, URLs or validator errors in diagnostics.
var (
	errTransport = errors.New("contract request failed")
	errLimit     = errors.New("contract response limit exceeded")
	errContract  = errors.New("contract response mismatch")
)

type exchange struct {
	status int
	header http.Header
	body   []byte
}

// localClient accepts only the owned HTTP loopback origin and disables proxies
// and redirects. It must not be reused for resource-declared destinations.
func localClient(base string) (*http.Client, error) {
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.ForceQuery {
		return nil, errors.New("contract driver requires the owned loopback API")
	}
	// This client is deliberately restricted to the invocation-owned loopback
	// listener. It is not a transport for external content or federation URLs.
	return &http.Client{
		Timeout:       10 * time.Second,
		Transport:     &http.Transport{Proxy: nil, MaxResponseHeaderBytes: 64 << 10, DisableKeepAlives: true},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return errTransport },
	}, nil
}

// request sends a fixture GET with optional authentication and cache validation.
// The caller must supply the origin accepted by localClient and a reviewed path.
func request(ctx context.Context, client *http.Client, base, path, token, etag string) (exchange, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return exchange{}, errTransport
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	return send(client, req)
}

// send captures a bounded response and closes its body on every read path.
// Transport failures use fixed errors rather than exposing private exchange data.
func send(client *http.Client, req *http.Request) (exchange, error) {
	response, err := client.Do(req)
	if err != nil {
		return exchange{}, errTransport
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maximumResponse+1))
	if err != nil {
		return exchange{}, errTransport
	}
	if len(raw) > maximumResponse {
		return exchange{}, errLimit
	}
	return exchange{response.StatusCode, response.Header.Clone(), raw}, nil
}

// loadContract accepts only the embedded API bytes and resolves local references.
// Served-contract drift and schema errors share a content-free failure result.
func loadContract(ctx context.Context, raw []byte) (*openapi3.T, error) {
	if !bytes.Equal(raw, serviceapi.ControlV1) {
		return nil, errContract
	}
	loader := openapi3.NewLoader()
	loader.Context = ctx
	loader.IsExternalRefsAllowed = false
	document, err := loader.LoadFromData(raw)
	if err != nil || document.Validate(ctx) != nil {
		return nil, errContract
	}
	return document, nil
}

// validate applies the response contract for a GET at the reviewed path template.
func validate(document *openapi3.T, path string, result exchange, expected int) error {
	return validateMethod(document, http.MethodGet, path, result, expected)
}

// validateMethod checks status, media type, body shape and problem correlation.
// path is an OpenAPI template, not an arbitrary concrete request URL. Callers
// retain responsibility for workflow semantics and request-side assertions.
func validateMethod(document *openapi3.T, method, path string, result exchange, expected int) error {
	if result.status != expected || len(result.body) > maximumResponse {
		return errContract
	}
	item := document.Paths.Value(path)
	if item == nil {
		return errContract
	}
	operation := item.Operations()[method]
	if operation == nil || operation.Responses == nil {
		return errContract
	}
	response := operation.Responses.Status(result.status)
	if response == nil {
		response = operation.Responses.Default()
	}
	if response == nil || response.Value == nil {
		return errContract
	}
	if result.status == http.StatusNotModified || result.status == http.StatusNoContent || len(response.Value.Content) == 0 {
		if len(result.body) != 0 {
			return errContract
		}
		return nil
	}
	mediaType, _, err := mime.ParseMediaType(result.header.Get("Content-Type"))
	if err != nil {
		return errContract
	}
	media := response.Value.Content.Get(mediaType)
	if media == nil || media.Schema == nil || media.Schema.Value == nil {
		return errContract
	}
	var value any = string(result.body)
	if mediaType == "application/json" || strings.HasSuffix(mediaType, "+json") {
		if jsondoc.Validate(result.body) != nil || json.Unmarshal(result.body, &value) != nil {
			return errContract
		}
	}
	if media.Schema.Value.VisitJSON(value, openapi3.EnableJSONSchema2020()) != nil {
		return errContract
	}
	if mediaType == "application/problem+json" {
		problem, ok := value.(map[string]any)
		if !ok || problem["status"] != float64(result.status) || problem["requestId"] == "" || result.header.Get("X-Request-ID") == "" || result.header.Get("Cache-Control") != "no-store" {
			return errContract
		}
		// The replayed body retains the original execution's request ID; the
		// header identifies this delivery attempt. This is the documented API
		// convention, not permission to rewrite a retained command response.
		if result.header.Get("Idempotency-Replayed") != "true" && problem["requestId"] != result.header.Get("X-Request-ID") {
			return errContract
		}
	}
	return nil
}

// validateInput is a single bounded private exchange supplied by the existing
// Python journey clients. Bodies travel on stdin, never arguments or disk.
type validateInput struct {
	Method  string      `json:"method"`
	Path    string      `json:"path"`
	Status  int         `json:"status"`
	Headers http.Header `json:"headers"`
	Body    []byte      `json:"body"`
}

// validateStdin requires a passed contract result, not merely a valid observation.
func validateStdin(ctx context.Context, input io.Reader) error {
	observation, err := observeStdin(ctx, input)
	if err != nil || observation.Contract != "passed" {
		return errContract
	}
	return nil
}

// observation contains only contract-owned identity and bounded outcome fields.
// It never retains a request path, headers or response content.
type observation struct {
	OperationID string `json:"operationId"`
	Status      int    `json:"status"`
	Contract    string `json:"contract"`
}

// observeStdin projects a private exchange onto safe operation/outcome fields.
// A schema mismatch is a failed observation; malformed input is an error, and a
// transport failure has status zero with no headers or body to validate.
func observeStdin(ctx context.Context, input io.Reader) (observation, error) {
	value, err := decodeInput(input)
	if err != nil {
		return observation{}, err
	}
	document, err := loadContract(ctx, serviceapi.ControlV1)
	if err != nil {
		return observation{}, err
	}
	template, operation, err := matchOperation(document, value.Method, value.Path)
	if err != nil {
		return observation{}, err
	}
	result := observation{OperationID: operation.OperationID, Status: value.Status, Contract: "not-checked"}
	if value.Status == 0 {
		if len(value.Body) != 0 || len(value.Headers) != 0 {
			return observation{}, errContract
		}
		return result, nil
	}
	result.Contract = "passed"
	if validateMethod(document, value.Method, template, exchange{value.Status, value.Headers, value.Body}, value.Status) != nil {
		result.Contract = "failed"
	}
	return result, nil
}

// decodeInput strictly decodes the bounded stdin envelope and canonicalizes
// headers before route matching or schema validation sees the exchange.
func decodeInput(input io.Reader) (validateInput, error) {
	const maximumInput = 12 << 20 // base64 response plus bounded headers
	raw, err := io.ReadAll(io.LimitReader(input, maximumInput+1))
	if err != nil || len(raw) > maximumInput || jsondoc.Validate(raw) != nil {
		return validateInput{}, errContract
	}
	var value validateInput
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil {
		return validateInput{}, errContract
	}
	if len(value.Method) > 16 || len(value.Path) > 8192 || !strings.HasPrefix(value.Path, "/") || strings.ContainsAny(value.Path, "\r\n#") || (value.Status != 0 && (value.Status < 100 || value.Status > 599)) {
		return validateInput{}, errContract
	}
	canonical := make(http.Header, len(value.Headers))
	headerBytes := 0
	for name, values := range value.Headers {
		key := http.CanonicalHeaderKey(name)
		if _, exists := canonical[key]; exists || key == "" || len(values) == 0 {
			return validateInput{}, errContract
		}
		for _, item := range values {
			headerBytes += len(name) + len(item)
			if headerBytes > 64<<10 || strings.ContainsAny(name+item, "\r\n") {
				return validateInput{}, errContract
			}
		}
		canonical[key] = values
	}
	value.Headers = canonical
	return value, nil
}

// matchOperation maps a concrete path to one contract template and HTTP method;
// query values are not part of the retained operation identity.
func matchOperation(document *openapi3.T, method, rawPath string) (string, *openapi3.Operation, error) {
	path, _, _ := strings.Cut(rawPath, "?")
	// Prefer literal routes, then the most specific reviewed template. Matching
	// never fetches a schema or interprets a caller-supplied regular expression.
	paths := document.Paths.InMatchingOrder()
	sort.SliceStable(paths, func(i, j int) bool { return strings.Count(paths[i], "{") < strings.Count(paths[j], "{") })
	for _, template := range paths {
		pattern := regexp.QuoteMeta(template)
		pattern = regexp.MustCompile(`\\\{[^{}]+\\\}`).ReplaceAllString(pattern, `[^/]+`)
		if regexp.MustCompile("^" + pattern + "$").MatchString(path) {
			operation := document.Paths.Value(template).Operations()[method]
			if operation == nil || operation.OperationID == "" {
				return "", nil, errContract
			}
			return template, operation, nil
		}
	}
	return "", nil, errContract
}

// run executes the representative exchange checks without emitting observations.
func run(ctx context.Context, base, documentID string) error {
	return runObserved(ctx, base, documentID, func(observation, int) error { return nil })
}

// runObserved checks representative reads and request envelopes on the owned API.
// It reports each completed observation before later workflow checks can fail.
func runObserved(ctx context.Context, base, documentID string, observe func(observation, int) error) error {
	client, err := localClient(base)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	document, err := loadContract(ctx, serviceapi.ControlV1)
	if err != nil {
		return err
	}
	read := func(path, token, etag string, expected int) (exchange, error) {
		template, operation, err := matchOperation(document, http.MethodGet, path)
		if err != nil {
			return exchange{}, err
		}
		result, err := request(ctx, client, base, path, token, etag)
		if err != nil {
			if observe(observation{operation.OperationID, 0, "not-checked"}, expected) != nil {
				return exchange{}, errContract
			}
			return exchange{}, err
		}
		state := "passed"
		if validate(document, template, result, result.status) != nil {
			state = "failed"
		}
		if observe(observation{operation.OperationID, result.status, state}, expected) != nil {
			return exchange{}, errContract
		}
		return result, validate(document, template, result, expected)
	}
	contract, err := read("/openapi/control-v1.yaml", "", "", 200)
	if err != nil {
		return err
	}
	if !bytes.Equal(contract.body, serviceapi.ControlV1) {
		return errContract
	}
	// Fixed representative bindings, not an all-operation schema coverage claim.
	for _, check := range []struct {
		path, token string
		status      int
	}{
		{"/livez", "", 200}, {"/readyz", "", 200}, {"/version", "", 200}, {"/metrics", "", 200},
		{"/api/v1/service-information", "local-admin", 200}, {"/api/v1/service-information", "", 401},
		{"/api/v1/service-information", "invalid-functional-token", 401}, {"/api/v1/service-information?limit=1", "local-admin", 400},
		{"/api/v1/capabilities", "local-admin", 200}, {"/api/v1/capabilities", "", 401},
		{"/api/v1/capabilities", "invalid-functional-token", 401},
		{"/api/v1/namespaces?limit=1", "local-admin", 200},
		{"/api/v1/namespaces?limit=invalid", "local-admin", 400},
	} {
		if _, err := read(check.path, check.token, "", check.status); err != nil {
			return err
		}
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9_-]{1,150}$`).MatchString(documentID) {
		return errors.New("invalid owned document identifier")
	}
	artifactPath := "/public/v1/artifacts/" + documentID
	artifact, err := read(artifactPath, "", "", 200)
	if err != nil {
		return err
	}
	if !bytes.Contains(artifact.body, []byte("Inspect the order evidence")) || artifact.header.Get("ETag") != `"`+documentID+`"` {
		return errContract
	}
	conditional, err := read(artifactPath, "", artifact.header.Get("ETag"), 304)
	if err != nil {
		return err
	}
	if conditional.header.Get("ETag") != artifact.header.Get("ETag") {
		return errContract
	}
	return requestEnvelopes(ctx, client, base, document, observe)
}

// main selects offline stdin/operation modes or bounded live fixture checks and
// exposes only safe driver results to its supervising process.
func main() {
	stdin := flag.Bool("validate-stdin", false, "validate one bounded private journey response")
	observe := flag.Bool("observe-stdin", false, "return one safe operation observation")
	operations := flag.Bool("operations", false, "print the authoritative operation ID denominator")
	representative := flag.Bool("report", false, "emit bounded representative observations")
	base := flag.String("base-url", "", "invocation-owned loopback API")
	document := flag.String("document-id", "", "published smoke Skill document")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	var err error
	switch {
	case *operations:
		var ids []string
		ids, err = operationIDs(ctx)
		if err == nil {
			err = json.NewEncoder(os.Stdout).Encode(ids)
		}
	case *observe:
		var value observation
		value, err = observeStdin(ctx, os.Stdin)
		if err == nil {
			err = json.NewEncoder(os.Stdout).Encode(value)
		}
	case *representative:
		err = runObserved(ctx, *base, *document, func(value observation, expected int) error {
			// A bounded set of representative exchanges, consumed privately by
			// the Python wrapper even when a later behavior assertion fails.
			return json.NewEncoder(os.Stdout).Encode(struct {
				observation
				Expected int `json:"expected"`
			}{value, expected})
		})
	case *stdin:
		err = validateStdin(ctx, os.Stdin)
	default:
		err = run(ctx, *base, *document)
	}
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if !*stdin && !*observe && !*operations && !*representative {
		fmt.Println("Representative response contracts passed; smoke internals and remaining bindings are not claimed.")
	}
}

// operationIDs returns the sorted, unique contract denominator and rejects missing
// or duplicate identities instead of silently changing coverage accounting.
func operationIDs(ctx context.Context) ([]string, error) {
	document, err := loadContract(ctx, serviceapi.ControlV1)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0)
	seen := make(map[string]bool)
	for _, item := range document.Paths.Map() {
		for _, operation := range item.Operations() {
			if operation.OperationID == "" || seen[operation.OperationID] {
				return nil, errContract
			}
			seen[operation.OperationID] = true
			ids = append(ids, operation.OperationID)
		}
	}
	sort.Strings(ids)
	return ids, nil
}
