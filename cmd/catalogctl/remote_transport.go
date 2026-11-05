package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/cliauth"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/controlclient"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

const (
	remoteMaxBody    = controlclient.MaxBody
	remoteMaxHeaders = controlclient.MaxHeaders
	remoteMaxToken   = controlclient.MaxToken
	remoteMaxCA      = controlclient.MaxCA
)

type remoteEnvelope = controlclient.Envelope
type remoteAPIError = controlclient.APIError
type remoteSession struct {
	client        *http.Client
	origin, token string
}

func (s *remoteSession) close() { s.client.CloseIdleConnections() }

// Resolve the same explicit credential choice for every remote CLI workflow.
// Inherited bearer inputs still conflict when an empty flag hides their value.
func selectRemoteCredentials(o *remoteOptions, getenv func(string) string) error {
	if o.loginFile != "" && (o.tokenFile != "" || getenv("CATALOG_REMOTE_TOKEN_FILE") != "" || getenv("CATALOG_REMOTE_TOKEN") != "") {
		return safeerr.New("remote: --login-file cannot be combined with a token file or token environment, including inherited values")
	}
	if o.issuerCAFile != "" && o.loginFile == "" {
		return safeerr.New("remote: --issuer-ca-file requires --login-file")
	}
	if o.tokenFile == "" {
		o.token = getenv("CATALOG_REMOTE_TOKEN")
	}
	return nil
}

func newRemoteSession(ctx context.Context, o remoteOptions) (*remoteSession, error) {
	if o.loginFile != "" {
		if o.tokenFile != "" || o.token != "" {
			return nil, safeerr.New("remote: login credentials cannot be combined with another bearer source")
		}
		token, err := cliauth.AccessToken(ctx, o.loginFile, o.baseURL, o.caFile, o.issuerCAFile)
		if err != nil {
			return nil, err
		}
		o.token = token
	}
	s, err := controlclient.New(ctx, controlclient.Options{BaseURL: o.baseURL, TokenFile: o.tokenFile, CAFile: o.caFile, Token: o.token, Timeout: o.timeout})
	if err != nil {
		return nil, err
	}
	return &remoteSession{client: s.Client, origin: s.Origin, token: s.Token}, nil
}
func requestRemote(ctx context.Context, o remoteOptions) (remoteEnvelope, error) {
	s, err := newRemoteSession(ctx, o)
	if err != nil {
		return remoteEnvelope{}, err
	}
	defer s.close()
	return exchangeRemote(ctx, s.client, o, s.origin, s.token)
}
func newRemoteRequest(ctx context.Context, origin, token, method, path, accept string, body io.Reader) (*http.Request, error) {
	return controlclient.NewRequest(ctx, origin, token, method, path, accept, body)
}
func remoteOrigin(v string) (string, error)            { return controlclient.Origin(v) }
func remoteReadFile(p string, n int64) ([]byte, error) { return controlclient.ReadFile(p, n) }
func remoteDiagnostic(v, t string, n int) string       { return controlclient.Diagnostic(v, t, n) }
func remoteHTTPFailure(s int, h http.Header, d []byte, t string) remoteAPIError {
	return controlclient.HTTPFailure(s, h, d, t)
}

func exchangeRemote(ctx context.Context, client *http.Client, options remoteOptions, origin, token string) (remoteEnvelope, error) {
	var result remoteEnvelope
	var err error
	method := http.MethodGet
	var body []byte
	if options.search != nil {
		method = http.MethodPost
		body, err = json.Marshal(options.search)
		if err != nil {
			return result, safeerr.New("remote: cannot encode Search request")
		}
	}
	if options.reindex != nil {
		method = http.MethodPost
		body, err = json.Marshal(options.reindex)
		if err != nil {
			return result, safeerr.New("remote: cannot encode reindex request")
		}
	}
	if options.trustRequest != nil {
		method = http.MethodPost
		// Preserve explicit empty versions and the validated command bytes.
		body = options.trustRequest
	}
	if options.body != nil {
		method = http.MethodPost
		body = options.body
	}
	expectedStatus := options.expectedStatus
	if expectedStatus == 0 && (options.reindex != nil || options.trustRequest != nil) {
		expectedStatus = http.StatusAccepted
	}
	return controlclient.Exchange(ctx, client, origin, token, controlclient.Request{Method: method, Path: options.path, Body: body, MaxResponseBytes: options.maxResponseBytes, ExpectedStatus: expectedStatus, IdempotencyKey: options.idempotencyKey, IfMatch: options.ifMatch})
}
