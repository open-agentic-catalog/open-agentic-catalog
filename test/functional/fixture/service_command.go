package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

type serviceCommandInput struct {
	Address   string `json:"address"`
	Token     string `json:"token"`
	Operation string `json:"operation"`
	Path      string `json:"path"`
	Key       string `json:"key"`
	Process   string `json:"process"`
	Namespace *struct {
		Slug        string `json:"slug"`
		DisplayName string `json:"displayName"`
	} `json:"namespace"`
}
type serviceCommandResult struct {
	Outcome    string `json:"outcome"`
	InstanceID string `json:"instanceId,omitempty"`
	Status     int    `json:"status,omitempty"`
	Digest     string `json:"digest,omitempty"`
	ETag       string `json:"etag,omitempty"`
	Replayed   bool   `json:"replayed,omitempty"`
	Code       string `json:"code,omitempty"`
}

func validateServiceCommand(in serviceCommandInput) error {
	address, err := netip.ParseAddrPort(in.Address)
	if err != nil || !address.Addr().IsPrivate() || address.Port() != 8080 || in.Token == "" || len(in.Token) > 8192 || strings.ContainsAny(in.Token, "\r\n") {
		return errors.New("invalid Service command address or token")
	}
	switch in.Operation {
	case "read":
		valid := false
		for _, prefix := range []string{"/api/v1/jobs/", "/api/v1/namespaces/"} {
			if strings.HasPrefix(in.Path, prefix) && probeProcessID(strings.TrimPrefix(in.Path, prefix)) {
				valid = true
			}
		}
		if !valid || in.Namespace != nil || in.Key != "" || in.Process != "" {
			return errors.New("invalid Service read")
		}
	case "create", "discard":
		if in.Path != "" || in.Namespace == nil || len(in.Key) < 1 || len(in.Key) > 128 || strings.ContainsAny(in.Key, "\r\n") || (in.Operation == "discard" && !probeProcessID(in.Process)) || (in.Operation == "create" && in.Process != "") {
			return errors.New("invalid Service command")
		}
		if len(in.Namespace.Slug) < 1 || len(in.Namespace.Slug) > 80 || len(in.Namespace.DisplayName) < 1 || len(in.Namespace.DisplayName) > 120 {
			return errors.New("invalid Service namespace")
		}
		for _, c := range in.Namespace.Slug {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return errors.New("invalid Service namespace slug")
			}
		}
	default:
		return errors.New("invalid Service operation")
	}
	return nil
}

// serviceCommand uses one raw HTTP/1 connection for process identification and
// the selected operation. It has no transport retry, proxy or redirect path.
func serviceCommand(input io.Reader, output io.Writer) error {
	var in serviceCommandInput
	decoder := json.NewDecoder(io.LimitReader(input, 16<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("invalid Service command input")
	}
	if err := validateServiceCommand(in); err != nil {
		return err
	}
	result := executeServiceCommand(in)
	return json.NewEncoder(output).Encode(result)
}

func executeServiceCommand(in serviceCommandInput) serviceCommandResult {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", in.Address)
	if err != nil {
		return serviceCommandError(err)
	}
	defer func() { _ = conn.Close() }()
	deadline, _ := ctx.Deadline()
	if conn.SetDeadline(deadline) != nil {
		return serviceCommandResult{Outcome: "transport_error"}
	}
	return serviceCommandConnection(conn, in)
}

func serviceCommandError(err error) serviceCommandResult {
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return serviceCommandResult{Outcome: "timeout"}
	}
	return serviceCommandResult{Outcome: "transport_error"}
}

func serviceCommandConnection(conn net.Conn, in serviceCommandInput) serviceCommandResult {
	reader := bufio.NewReader(conn)
	response, err := serviceWireRequest(conn, reader, in, "GET", "/api/v1/service-information", nil)
	if err != nil {
		return serviceCommandError(err)
	}
	identity := serviceInformationOutcome(response.StatusCode, io.LimitReader(response.Body, 16<<10))
	_ = response.Body.Close()
	if identity.Outcome != "ok" {
		return serviceCommandResult{Outcome: "invalid_identity"}
	}
	if in.Operation == "discard" && identity.InstanceID != in.Process {
		return serviceCommandResult{Outcome: "not_selected", InstanceID: identity.InstanceID}
	}
	method, path := http.MethodGet, in.Path
	var body []byte
	if in.Operation != "read" {
		method, path = http.MethodPost, "/api/v1/namespaces"
		body, err = json.Marshal(in.Namespace)
		if err != nil {
			return serviceCommandResult{Outcome: "invalid_response"}
		}
	}
	response, err = serviceWireRequest(conn, reader, in, method, path, body)
	if err != nil {
		return serviceCommandError(err)
	}
	if in.Operation == "discard" && response.StatusCode == http.StatusCreated {
		// The fixture observes commitment, then closes the connection and
		// withholds the entire command receipt from its caller.
		_ = conn.Close()
		_ = response.Body.Close()
		return serviceCommandResult{Outcome: "response_discarded", InstanceID: identity.InstanceID}
	}
	result := projectServiceResponse(response)
	result.InstanceID = identity.InstanceID
	return result
}

func serviceWireRequest(conn net.Conn, reader *bufio.Reader, in serviceCommandInput, method, path string, body []byte) (*http.Response, error) {
	request, err := http.NewRequestWithContext(context.Background(), method, "http://"+in.Address+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+in.Token)
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", in.Key)
	}
	if err := request.Write(conn); err != nil {
		return nil, err
	}
	return http.ReadResponse(reader, request)
}

// Only hashes, fixed codes and bounded public process IDs leave the client.
func projectServiceResponse(response *http.Response) serviceCommandResult {
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		return serviceCommandError(err)
	}
	if len(body) > 1<<20 {
		return serviceCommandResult{Outcome: "invalid_response"}
	}
	etag := response.Header.Get("ETag")
	if etag != "" {
		text := strings.TrimSuffix(strings.TrimPrefix(etag, `"rev-`), `"`)
		revision, parseErr := strconv.ParseUint(text, 10, 64)
		if parseErr != nil || revision == 0 || etag != `"rev-`+strconv.FormatUint(revision, 10)+`"` {
			return serviceCommandResult{Outcome: "invalid_response"}
		}
	}
	digest := sha256.Sum256(body)
	result := serviceCommandResult{Outcome: "ok", Status: response.StatusCode, Digest: hex.EncodeToString(digest[:]), ETag: etag, Replayed: response.Header.Get("Idempotency-Replayed") == "true"}
	if response.StatusCode >= 400 {
		var problem struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(body, &problem) == nil {
			switch problem.Code {
			case "NOT_FOUND", "FORBIDDEN", "REQUEST_DEADLINE_EXCEEDED", "DEPENDENCY_UNAVAILABLE":
				result.Code = problem.Code
			}
		}
	}
	return result
}
