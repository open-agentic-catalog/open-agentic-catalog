package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type capture struct {
	Document   json.RawMessage `json:"-"`
	Digest     string          `json:"sha256"`
	Source     string          `json:"source"`
	CapturedAt time.Time       `json:"capturedAt"`
	Pages      int             `json:"pages"`
	Complete   bool            `json:"complete"`
	Reason     string          `json:"reason,omitempty"`
}

type collector interface {
	Collect(context.Context, mapping) (capture, error)
}
type mcpCollector struct {
	catalogTokenFile string
	now              func() time.Time
}

func localAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsValid() && !ip.IsUnspecified() && (ip.IsPrivate() || ip.IsLoopback())
}

func localDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errors.New("invalid_local_endpoint")
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return nil, errors.New("local_endpoint_unavailable")
	}
	for _, ip := range ips {
		if !localAddress(ip) {
			return nil, errors.New("nonlocal_mcp_address")
		}
	}
	var last error
	for _, ip := range ips {
		conn, err := (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		last = err
	}
	return nil, last
}

// mcpBoundary restricts the native SDK to one configured endpoint and captures
// exact result JSON before SDK decoding. It never follows metadata URLs.
type mcpBoundary struct {
	next             http.RoundTripper
	mapping          mapping
	catalogTokenFile string
	mu               sync.Mutex
	bytes            int
	pages            [][]json.RawMessage
	cursors          []string
}

func (b *mcpBoundary) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.String() != b.mapping.Endpoint || req.Method != http.MethodPost && req.Method != http.MethodDelete {
		return nil, errors.New("mcp_request_refused")
	}
	var id json.RawMessage
	method := ""
	if req.Method == http.MethodPost {
		raw, err := io.ReadAll(io.LimitReader(req.Body, 64<<10+1))
		_ = req.Body.Close()
		if err != nil || len(raw) > 64<<10 || uniqueJSON(raw) != nil {
			return nil, errors.New("mcp_request_refused")
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(raw, &request) != nil {
			return nil, errors.New("mcp_request_refused")
		}
		method, id = request.Method, request.ID
		switch method {
		case "initialize", "notifications/initialized", "tools/list", "notifications/cancelled":
		default:
			return nil, errors.New("mcp_method_refused")
		}
		req = req.Clone(req.Context())
		req.Body = io.NopCloser(bytes.NewReader(raw))
		req.ContentLength = int64(len(raw))
	}
	token, err := readToken(req.Context(), b.mapping.TokenFile)
	if err != nil {
		return nil, errors.New("mcp_credential_unavailable")
	}
	catalogToken, err := readToken(req.Context(), b.catalogTokenFile)
	if err != nil || token == catalogToken {
		return nil, errors.New("mcp_credential_refused")
	}
	req = req.Clone(req.Context())
	req.Header = req.Header.Clone()
	req.Header.Del("Cookie")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := b.next.RoundTrip(req)
	if err != nil {
		var invalid *tls.CertificateVerificationError
		if errors.As(err, &invalid) {
			return nil, failed("mcp_tls_failed", false)
		}
		return nil, failed("mcp_transport_failed", true)
	}
	body := resp.Body
	defer func() { _ = body.Close() }()
	// Streamable HTTP permits servers to refuse session termination with 405.
	terminationUnsupported := req.Method == http.MethodDelete && resp.StatusCode == http.StatusMethodNotAllowed
	if (resp.StatusCode < 200 || resp.StatusCode >= 300) && !terminationUnsupported {
		return nil, failed("mcp_http_failed", resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests)
	}
	if resp.Header.Get("Content-Encoding") != "" {
		return nil, errors.New("mcp_encoding_refused")
	}
	if len(id) == 0 || req.Method == http.MethodDelete {
		n, err := io.Copy(io.Discard, io.LimitReader(resp.Body, 4097))
		if err != nil || n > 4096 {
			return nil, errors.New("mcp_cleanup_failed")
		}
		b.mu.Lock()
		b.bytes += int(n)
		overBudget := b.bytes > captureLimit
		b.mu.Unlock()
		if overBudget {
			return nil, errors.New("mcp_byte_limit")
		}
		resp.Body = io.NopCloser(bytes.NewReader(nil))
		return resp, nil
	}
	b.mu.Lock()
	remaining := captureLimit - b.bytes
	b.mu.Unlock()
	if remaining <= 0 {
		return nil, errors.New("mcp_byte_limit")
	}
	media, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		return nil, errors.New("mcp_response_refused")
	}
	var wire, payload []byte
	switch media {
	case "application/json":
		wire, err = io.ReadAll(io.LimitReader(resp.Body, int64(remaining)+1))
		payload = wire
	case "text/event-stream":
		wire, payload, err = finiteEvent(resp.Body, remaining, id)
	default:
		return nil, errors.New("mcp_media_type_refused")
	}
	if err != nil || len(wire) > remaining || uniqueJSON(payload) != nil {
		return nil, errors.New("invalid_mcp_response")
	}
	var envelope struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}
	if json.Unmarshal(payload, &envelope) != nil || envelope.JSONRPC != "2.0" || !bytes.Equal(envelope.ID, id) || len(envelope.Error) > 0 || len(envelope.Result) == 0 {
		return nil, errors.New("mcp_rpc_failed")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.bytes += len(wire)
	if method == "tools/list" {
		var members map[string]json.RawMessage
		if json.Unmarshal(envelope.Result, &members) != nil || members == nil {
			return nil, errors.New("invalid_mcp_page")
		}
		if raw, ok := members["nextCursor"]; ok {
			if len(raw) == 0 || raw[0] != '"' {
				return nil, errors.New("invalid_mcp_cursor")
			}
		}
		var page struct {
			Tools      []json.RawMessage `json:"tools"`
			NextCursor *string           `json:"nextCursor"`
		}
		if json.Unmarshal(envelope.Result, &page) != nil || page.Tools == nil || page.NextCursor != nil && (*page.NextCursor == "" || len(*page.NextCursor) > 4096) {
			return nil, errors.New("invalid_mcp_page")
		}
		b.pages = append(b.pages, page.Tools)
		cursor := ""
		if page.NextCursor != nil {
			cursor = *page.NextCursor
		}
		b.cursors = append(b.cursors, cursor)
	}
	resp.Body = io.NopCloser(bytes.NewReader(wire))
	resp.ContentLength = int64(len(wire))
	return resp, nil
}

// finiteEvent retains native SSE bytes through the matching JSON-RPC response.
// The SDK still owns event/message decoding and protocol behavior.
func finiteEvent(body io.Reader, limit int, id json.RawMessage) ([]byte, []byte, error) {
	r := bufio.NewReader(io.LimitReader(body, int64(limit)+1))
	var wire, data []byte
	for len(wire) <= limit {
		line, err := r.ReadBytes('\n')
		wire = append(wire, line...)
		if len(wire) > limit {
			return nil, nil, errors.New("mcp_byte_limit")
		}
		trim := bytes.TrimRight(line, "\r\n")
		if bytes.HasPrefix(trim, []byte("data:")) {
			part := bytes.TrimPrefix(trim, []byte("data:"))
			if len(part) > 0 && part[0] == ' ' {
				part = part[1:]
			}
			if len(data) > 0 {
				data = append(data, '\n')
			}
			data = append(data, part...)
		}
		if len(trim) == 0 && len(data) > 0 {
			if uniqueJSON(data) != nil {
				return nil, nil, errors.New("invalid_mcp_event")
			}
			var e struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if json.Unmarshal(data, &e) != nil || e.Method != "" || !bytes.Equal(e.ID, id) {
				return nil, nil, errors.New("unexpected_mcp_event")
			}
			return wire, data, nil
		}
		if err != nil {
			return nil, nil, errors.New("incomplete_mcp_event")
		}
	}
	return nil, nil, errors.New("mcp_byte_limit")
}

func (c *mcpCollector) Collect(parent context.Context, m mapping) (out capture, err error) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	if err = validateEndpoint(m.Endpoint); err != nil {
		return out, failed("invalid_mcp_endpoint", false)
	}
	ca, err := roots(m.CAFile)
	if err != nil {
		return out, failed("mcp_trust_unavailable", false)
	}
	transport := &http.Transport{Proxy: nil, DialContext: localDial, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: ca}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 16 << 10, DisableCompression: true, IdleConnTimeout: 5 * time.Second, MaxConnsPerHost: 1}
	defer transport.CloseIdleConnections()
	boundary := &mcpBoundary{next: transport, mapping: m, catalogTokenFile: c.catalogTokenFile}
	client := &http.Client{Transport: boundary, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	sdkClient := mcp.NewClient(&mcp.Implementation{Name: "catalog-mcp-scanner", Version: "0.1.0"}, &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}, MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true}, Logger: slog.New(slog.DiscardHandler)})
	session, err := sdkClient.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: m.Endpoint, HTTPClient: client, MaxRetries: -1, DisableStandaloneSSE: true, MaxEventSize: captureLimit}, &mcp.ClientSessionOptions{ProtocolVersion: protocolVersion})
	if err != nil {
		return out, collectionFailure(err, "mcp_setup_failed")
	}
	defer func() {
		if closeErr := session.Close(); closeErr != nil && err == nil {
			err = collectionFailure(closeErr, "mcp_session_cleanup_failed")
		}
	}()
	if session.InitializeResult().ProtocolVersion != protocolVersion {
		return out, failed("mcp_protocol_mismatch", false)
	}
	tools := []json.RawMessage{}
	seen := map[string]bool{}
	names := map[string]bool{}
	cursor := ""
	out = capture{Source: m.Endpoint, CapturedAt: c.now().UTC()}
	for page := 0; page < maxPages; page++ {
		if _, err = session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor}); err != nil {
			return out, collectionFailure(err, "mcp_tools_read_failed")
		}
		if len(boundary.pages) != page+1 {
			return out, failed("mcp_page_binding_failed", false)
		}
		for _, raw := range boundary.pages[page] {
			var t struct {
				Name        string                     `json:"name"`
				InputSchema map[string]json.RawMessage `json:"inputSchema"`
			}
			if json.Unmarshal(raw, &t) != nil || !toolNamePattern.MatchString(t.Name) || names[t.Name] || string(t.InputSchema["type"]) != `"object"` {
				return out, failed("invalid_mcp_tool", false)
			}
			if len(tools) == 128 {
				return out, failed("mcp_tool_limit", false)
			}
			names[t.Name] = true
			tools = append(tools, raw)
		}
		out.Pages = page + 1
		cursor = boundary.cursors[page]
		if cursor == "" {
			out.Complete = true
			break
		}
		if seen[cursor] {
			out.Reason = "repeated_cursor"
			break
		}
		seen[cursor] = true
	}
	if !out.Complete && out.Reason == "" {
		out.Reason = "page_limit"
	}
	doc := map[string]any{"tools": tools}
	if !out.Complete {
		doc["nextCursor"] = cursor
	}
	raw, marshalErr := json.Marshal(doc)
	if marshalErr != nil || len(raw) > captureLimit {
		return out, failed("mcp_capture_limit", false)
	}
	out.Document = raw
	out.Digest = sha(raw)
	return out, nil
}

func collectionFailure(err error, code string) error {
	var cause *failure
	if errors.As(err, &cause) {
		return cause
	}
	return failed(code, errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled))
}
