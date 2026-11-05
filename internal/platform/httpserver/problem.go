// Problem responses translate internal failures into stable RFC 9457 details and
// mark failed atomic encoding paths rollback-only.

package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/idempotency"
)

// Problem is the closed RFC 9457 response exposed by the control API. Internal
// causes are mapped to stable safe codes before this type is encoded.
type Problem struct {
	Type      string            `json:"type"`
	Title     string            `json:"title"`
	Status    int               `json:"status"`
	Detail    string            `json:"detail"`
	Instance  string            `json:"instance,omitempty"`
	Code      catalog.ErrorCode `json:"code"`
	RequestID string            `json:"requestId"`
	Errors    []catalog.Finding `json:"errors,omitempty"`
}

type ardProblem struct {
	ErrorCode catalog.ErrorCode `json:"errorCode"`
	Message   string            `json:"message"`
}

var (
	internalProblemFallback = []byte(`{"type":"https://catalog.example.com/problems/internal","title":"Internal error","status":500,"detail":"The service could not complete the request. Use the request ID to inspect safe diagnostics.","code":"INTERNAL","requestId":""}`)
	internalARDFallback     = []byte(`{"errorCode":"INTERNAL","message":"The service could not complete the request. Use the request ID to inspect safe diagnostics."}`)
)

// routerErrorHandler converts unmatched router failures to the selected API problem format while preserving allowed methods.
func routerErrorHandler(router *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if _, pattern := router.Handler(request); pattern != "" {
			router.ServeHTTP(writer, request)
			return
		}

		capture := newResponseCapture()
		router.ServeHTTP(capture, request)
		switch capture.status {
		case http.StatusBadRequest:
			writeRouterProblem(writer, request, catalog.E(catalog.CodeInvalidArgument, "the request target is invalid"), capture.header.Get("Allow"))
		case http.StatusNotFound:
			writeRouterProblem(writer, request, catalog.E(catalog.CodeNotFound, "route was not found"), capture.header.Get("Allow"))
		case http.StatusMethodNotAllowed:
			writeRouterProblem(writer, request, catalog.E(catalog.CodeMethodNotAllowed, "method is not allowed for this route"), capture.header.Get("Allow"))
		default:
			copyHeaders(writer.Header(), capture.header)
			status := capture.status
			if status == 0 {
				status = http.StatusOK
			}
			writer.WriteHeader(status)
			_, _ = writer.Write(capture.body.Bytes())
		}
	})
}

// writeRouterProblem preserves Allow and chooses control or discovery error encoding by route family.
func writeRouterProblem(writer http.ResponseWriter, request *http.Request, err error, allow string) {
	if allow != "" {
		writer.Header().Set("Allow", allow)
	}
	if usesARDProblem(request.URL.Path) {
		writeARDProblem(writer, request, err)
		return
	}
	writeProblem(writer, request, err)
}

// usesARDProblem identifies discovery route families that use the ARD error envelope.
func usesARDProblem(path string) bool {
	switch path {
	case "/.well-known/ard.json", "/.well-known/ai-catalog.json", "/ard/v1", "/public/v1":
		return true
	default:
		return strings.HasPrefix(path, "/ard/v1/") || strings.HasPrefix(path, "/public/v1/")
	}
}

// writeProblem marks non-evidence-preserving command failures for rollback and emits a correlated control problem.
func writeProblem(writer http.ResponseWriter, request *http.Request, err error) {
	if !catalog.ErrorPreservesEvidence(err) {
		idempotency.AbortCommand(request.Context(), err)
	}
	code := catalog.ErrorCodeOf(err)
	if errors.Is(err, context.DeadlineExceeded) || request.Context().Err() == context.DeadlineExceeded {
		code = catalog.CodeRequestDeadlineExceeded
		err = catalog.E(code, "the request exceeded the service work deadline")
	}
	status, title := problemStatus(code)
	detail := safeDetail(err, code)
	problem := Problem{Type: "https://catalog.example.com/problems/" + strings.ToLower(strings.ReplaceAll(string(code), "_", "-")), Title: title, Status: status, Detail: detail, Instance: request.URL.Path, Code: code, RequestID: requestID(request.Context())}
	raw, marshalErr := json.Marshal(problem)
	if marshalErr != nil {
		status = http.StatusInternalServerError
		raw = internalProblemFallback
	}
	// Only this code-owned safe validation renderer can mark an early error
	// replayable without successful private-disclosure checks.
	if code == catalog.CodeInvalidArgument && status == http.StatusBadRequest && marshalErr == nil {
		markValidationProblem(writer)
	}
	prepareErrorResponse(writer.Header())
	observeProblem(request, code, status, err)
	_ = writeEncodedJSON(writer, status, "application/problem+json", raw)
}

// writeARDProblem encodes discovery errors and maps unavailable publication to a retryable service response.
func writeARDProblem(writer http.ResponseWriter, request *http.Request, err error) {
	code := catalog.ErrorCodeOf(err)
	if errors.Is(err, context.DeadlineExceeded) {
		code = catalog.CodeRequestDeadlineExceeded
		err = catalog.E(code, "the request exceeded the service work deadline")
	}
	status, _ := problemStatus(code)
	if code == catalog.CodePublicationNotReady {
		status = http.StatusServiceUnavailable
	}
	raw, marshalErr := json.Marshal(ardProblem{ErrorCode: code, Message: safeDetail(err, code)})
	if marshalErr != nil {
		status = http.StatusInternalServerError
		raw = internalARDFallback
	}
	prepareErrorResponse(writer.Header())
	observeProblem(request, code, status, err)
	_ = writeEncodedJSON(writer, status, "application/json", raw)
}

// problemStatus maps stable domain error codes to HTTP statuses and public titles.
func problemStatus(code catalog.ErrorCode) (int, string) {
	switch code {
	case catalog.CodeUnauthenticated:
		return http.StatusUnauthorized, "Unauthenticated"
	case catalog.CodeInvalidArgument, catalog.CodeIdempotencyKeyReused:
		if code == catalog.CodeIdempotencyKeyReused {
			return http.StatusConflict, "Idempotency key reused"
		}
		return http.StatusBadRequest, "Invalid request"
	case catalog.CodeNotFound:
		return http.StatusNotFound, "Not found"
	case catalog.CodeMethodNotAllowed:
		return http.StatusMethodNotAllowed, "Method not allowed"
	case catalog.CodeConflict, catalog.CodeVersionSealed, catalog.CodeSeparationOfDuties, catalog.CodeInvalidTransition:
		return http.StatusConflict, "Conflict"
	case catalog.CodePreconditionFailed:
		return http.StatusPreconditionFailed, "Precondition failed"
	case catalog.CodeForbidden, catalog.CodeSourcePolicyDenied:
		return http.StatusForbidden, "Forbidden"
	case catalog.CodeValidationFailed, catalog.CodePublicationNotReady, catalog.CodeAuditExportRangeTooLarge, catalog.CodeWorkLimitExceeded:
		return http.StatusUnprocessableEntity, "Validation failed"
	case catalog.CodeUnsupportedProfile:
		return http.StatusUnprocessableEntity, "Unsupported profile"
	case catalog.CodeDependencyUnavailable, catalog.CodeWebhookTestBusy:
		return http.StatusServiceUnavailable, "Dependency unavailable"
	case catalog.CodeRequestDeadlineExceeded:
		return http.StatusServiceUnavailable, "Request deadline exceeded"
	case catalog.CodeInternal:
		return http.StatusInternalServerError, "Internal error"
	default:
		return http.StatusInternalServerError, "Internal error"
	}
}

// safeDetail suppresses internal/dependency causes and otherwise uses the domain's client-facing message.
func safeDetail(err error, code catalog.ErrorCode) string {
	if code == catalog.CodeInternal || code == catalog.CodeDependencyUnavailable {
		return "The service could not complete the request. Use the request ID to inspect safe diagnostics."
	}
	if err == nil {
		return "The request could not be completed."
	}
	var domainError *catalog.DomainError
	if errors.As(err, &domainError) && domainError.Message != "" {
		return domainError.Message
	}
	return err.Error()
}

// respondJSON encodes a control response through deadline and atomic-command failure handling.
func (s *Server) respondJSON(writer http.ResponseWriter, request *http.Request, status int, value any) {
	s.respondJSONWithType(writer, request, status, "application/json; charset=utf-8", value, false)
}

// respondARDJSON encodes a successful discovery response with the selected standards media type.
func (s *Server) respondARDJSON(writer http.ResponseWriter, request *http.Request, mediaType string, value any) {
	s.respondJSONWithType(writer, request, http.StatusOK, mediaType, value, true)
}

// respondJSONWithType checks deadlines around encoding and aborts atomic command state when response encoding fails.
func (s *Server) respondJSONWithType(writer http.ResponseWriter, request *http.Request, status int, mediaType string, value any, ard bool) {
	if request.Context().Err() == context.DeadlineExceeded {
		err := catalog.E(catalog.CodeRequestDeadlineExceeded, "the request exceeded the service work deadline")
		if ard {
			writeARDProblem(writer, request, err)
		} else {
			writeProblem(writer, request, err)
		}
		return
	}
	if status == http.StatusNoContent {
		writer.Header().Del("Content-Type")
		writer.Header().Del("Content-Length")
		writer.WriteHeader(status)
		return
	}
	raw, err := json.Marshal(value)
	if err != nil {
		idempotencyAbort(request, catalog.E(catalog.CodeInternal, "response encoding failed"))
		if s.logger != nil {
			diagnosticOwned(request.Context())
			s.logger.Error("HTTP response JSON encoding failed", requestLogAttributes(request, "event", "http.encoding_failed", "component", "api", "status", status, "value_type", fmt.Sprintf("%T", value), "error_type", fmt.Sprintf("%T", err))...)
		}
		if ard {
			writeARDProblem(writer, request, catalog.E(catalog.CodeInternal, "response encoding failed"))
		} else {
			writeProblem(writer, request, catalog.E(catalog.CodeInternal, "response encoding failed"))
		}
		return
	}
	if request.Context().Err() == context.DeadlineExceeded {
		err := catalog.E(catalog.CodeRequestDeadlineExceeded, "the request exceeded the service work deadline")
		if ard {
			writeARDProblem(writer, request, err)
		} else {
			writeProblem(writer, request, err)
		}
		return
	}
	if err := writeEncodedJSON(writer, status, mediaType, raw); err != nil && s.logger != nil {
		diagnosticOwned(request.Context())
		s.logger.Debug("HTTP response write failed", requestLogAttributes(request, "event", "http.write_failed", "component", "api", "status", status, "error_type", fmt.Sprintf("%T", err))...)
	}
}

// idempotencyAbort marks the current atomic command rollback-only after an HTTP response failure.
func idempotencyAbort(request *http.Request, err error) {
	idempotency.AbortCommand(request.Context(), err)
}

// writeEncodedJSON writes exact encoded bytes with their length and detects a short body write.
func writeEncodedJSON(writer http.ResponseWriter, status int, mediaType string, raw []byte) error {
	writer.Header().Set("Content-Type", mediaType)
	writer.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	writer.WriteHeader(status)
	written, err := writer.Write(raw)
	if err != nil {
		return err
	}
	if written != len(raw) {
		return io.ErrShortWrite
	}
	return nil
}

// prepareErrorResponse removes stale success/evidence headers and disables caching before emitting an error.
func prepareErrorResponse(header http.Header) {
	for _, name := range []string{"Content-Digest", "Content-Disposition", "Content-Length", "ETag", "Last-Modified", "Link", "Location", "Server-Timing"} {
		header.Del(name)
	}
	header.Set("Cache-Control", "no-store")
}

// Preserve code-owned validation provenance through the standard writer chain.
func markValidationProblem(writer http.ResponseWriter) {
	for depth := 0; depth < 8; depth++ {
		if capture, ok := writer.(*responseCapture); ok {
			capture.validationProblem = true
			return
		}
		wrapper, ok := writer.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return
		}
		writer = wrapper.Unwrap()
	}
}
