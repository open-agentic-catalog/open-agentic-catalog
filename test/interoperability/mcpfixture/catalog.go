package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// catalogRequest is a test-only bridge into the isolated namespace. Its target
// is fixed; MCP metadata never supplies an API destination or a command.
type catalogRequest struct {
	Path   string          `json:"path"`
	Method string          `json:"method"`
	Token  string          `json:"token"`
	Body   json.RawMessage `json:"body"`
	ETag   string          `json:"etag"`
	Key    string          `json:"key"`
}

type catalogResponse struct {
	Status int             `json:"status"`
	ETag   string          `json:"etag"`
	Body   json.RawMessage `json:"body"`
}

func catalogExchange(ctx context.Context, client *http.Client, input catalogRequest) (catalogResponse, error) {
	var result catalogResponse
	u, err := url.Parse(input.Path)
	if err != nil || u.IsAbs() || u.Host != "" || u.Fragment != "" || strings.Contains(input.Path, "\\") ||
		!(strings.HasPrefix(u.Path, "/api/v1/") || input.Path == "/.well-known/ard.json") ||
		(input.Method != http.MethodGet && input.Method != http.MethodPost) {
		return result, errors.New("unsupported Catalog fixture request")
	}
	var body io.Reader
	if input.Method == http.MethodPost {
		body = bytes.NewReader(input.Body)
	}
	request, err := http.NewRequestWithContext(ctx, input.Method, "http://127.0.0.1:8080"+input.Path, body)
	if err != nil {
		return result, err
	}
	request.Header.Set("Accept", "application/json")
	if input.Token != "" {
		request.Header.Set("Authorization", "Bearer "+input.Token)
	}
	if input.Method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", input.Key)
	}
	if input.ETag != "" {
		request.Header.Set("If-Match", input.ETag)
	}
	response, err := client.Do(request)
	if err != nil {
		return result, err
	}
	defer func() { _ = response.Body.Close() }()
	const limit = 2 * 1024 * 1024
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || len(raw) > limit || !json.Valid(raw) {
		return result, errors.New("invalid or oversized Catalog fixture response")
	}
	return catalogResponse{Status: response.StatusCode, ETag: response.Header.Get("ETag"), Body: raw}, nil
}

func catalogCommand(ctx context.Context) error {
	raw, err := os.ReadFile("/catalog/request.json")
	if err != nil || len(raw) > 1024*1024 {
		return errors.New("unavailable or oversized Catalog fixture input")
	}
	var input catalogRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return err
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return errors.New("trailing Catalog fixture input")
	}
	client := catalogClient()
	defer client.CloseIdleConnections()
	response, err := catalogExchange(ctx, client, input)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(response)
}

func catalogClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
}
