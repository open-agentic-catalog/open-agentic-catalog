package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"net/http"
	"reflect"
	"strconv"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

func downloadExportArtifact(ctx context.Context, session *remoteSession, representation exportRepresentation, limit int64) ([]byte, error) {
	if representation.Size > limit {
		return nil, safeerr.New("export: recorded document size exceeds the configured content byte limit")
	}
	request, err := newRemoteRequest(ctx, session.origin, session.token, http.MethodGet, "/api/v1/artifacts/"+representation.ArtifactID+"/content", representation.MediaType, nil)
	if err != nil {
		return nil, safeerr.New("export: cannot construct artifact request")
	}
	response, err := session.client.Do(request)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
			return nil, safeerr.New("export: artifact request cancelled or deadline exceeded")
		}
		return nil, safeerr.New("export: artifact request failed; check connectivity and TLS trust")
	}
	defer func() { _ = response.Body.Close() }()
	requestID := remoteDiagnostic(response.Header.Get("X-Request-ID"), session.token, 256)
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return nil, remoteAPIError{Status: response.StatusCode, Code: "redirect_refused", Detail: "API redirects are not followed", RequestID: requestID}
	}
	if response.StatusCode != http.StatusOK {
		// Keep denial and safe problem codes useful without ever printing binary
		// error pages or interpreting an unsuccessful body as exported content.
		raw, readErr := io.ReadAll(io.LimitReader(response.Body, remoteMaxBody+1))
		if ctx.Err() != nil || errors.Is(readErr, context.DeadlineExceeded) {
			return nil, safeerr.New("export: artifact request cancelled or deadline exceeded")
		}
		if readErr != nil || len(raw) > remoteMaxBody {
			raw = nil
		}
		return nil, remoteHTTPFailure(response.StatusCode, response.Header, raw, session.token)
	}
	fail := remoteAPIError{Status: response.StatusCode, Code: "invalid_response", Detail: "artifact headers, media type, size or checksum disagree with the selected representation", RequestID: requestID}
	for _, name := range []string{"Content-Type", "Content-Digest", "Content-Length"} {
		if len(response.Header.Values(name)) != 1 {
			return nil, fail
		}
	}
	media, parameters, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	expectedMedia, expectedParameters, expectedErr := mime.ParseMediaType(representation.MediaType)
	length, lengthErr := strconv.ParseInt(response.Header.Get("Content-Length"), 10, 64)
	if mediaErr != nil || expectedErr != nil || media != expectedMedia || !reflect.DeepEqual(parameters, expectedParameters) || lengthErr != nil || length != representation.Size || response.ContentLength != length || response.Header.Get("Content-Encoding") != "" || response.Uncompressed {
		return nil, fail
	}
	// Read one extra byte even when the peer promises an exact length. Injected
	// transports and malformed streams must not bypass the retained size check.
	raw, err := io.ReadAll(io.LimitReader(response.Body, representation.Size+1))
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
		return nil, safeerr.New("export: artifact request cancelled or deadline exceeded")
	}
	if err != nil || int64(len(raw)) != representation.Size || exportChecksum(raw) != representation.ContentDigest {
		return nil, fail
	}
	digest := sha256.Sum256(raw)
	if response.Header.Get("Content-Digest") != "sha-256=:"+base64.StdEncoding.EncodeToString(digest[:])+":" {
		return nil, fail
	}
	return raw, nil
}
