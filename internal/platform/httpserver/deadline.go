// Request deadlines cancel domain work and emit a safe timeout response only when
// headers have not already committed.

package httpserver

import (
	"context"
	"net/http"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
)

// commitmentWriter tracks the response boundary beyond which timeout rewriting is unsafe.
type commitmentWriter struct {
	http.ResponseWriter
	committed bool
}

// WriteHeader forwards the first status and marks the response committed.
func (writer *commitmentWriter) WriteHeader(status int) {
	if writer.committed {
		return
	}
	writer.committed = true
	writer.ResponseWriter.WriteHeader(status)
}

// Write commits an implicit success status before forwarding response bytes.
func (writer *commitmentWriter) Write(value []byte) (int, error) {
	if !writer.committed {
		writer.WriteHeader(http.StatusOK)
	}
	return writer.ResponseWriter.Write(value)
}

// Unwrap lets http.ResponseController retain access to optional interfaces on
// the underlying writer without the deadline layer claiming to implement them.
func (writer *commitmentWriter) Unwrap() http.ResponseWriter { return writer.ResponseWriter }

// productWorkDeadline cancels routed product work at its deadline and emits a timeout only before response commitment.
func productWorkDeadline(router *http.ServeMux, timeout time.Duration, metrics *observability.Registry, next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if !usesProductWorkDeadline(router, request) {
			next.ServeHTTP(response, request)
			return
		}
		ctx, cancel := context.WithTimeout(request.Context(), timeout)
		defer cancel()
		writer := &commitmentWriter{ResponseWriter: response}
		next.ServeHTTP(writer, request.WithContext(ctx))
		if ctx.Err() != context.DeadlineExceeded {
			return
		}
		if metrics != nil {
			metrics.RecordRequestDeadline()
		}
		if writer.committed {
			return
		}
		err := catalog.E(catalog.CodeRequestDeadlineExceeded, "the request exceeded the service work deadline")
		if usesARDProblem(request.URL.Path) {
			writeARDProblem(writer, request, err)
			return
		}
		writeProblem(writer, request, err)
	})
}

// usesProductWorkDeadline selects registered product/discovery routes while excluding health, contract and preflight reads.
func usesProductWorkDeadline(router *http.ServeMux, request *http.Request) bool {
	if request.Method == http.MethodOptions {
		return false
	}
	switch request.URL.Path {
	case "/livez", "/readyz", "/version", "/metrics", "/openapi/control-v1.yaml":
		return false
	}
	_, pattern := router.Handler(request)
	if pattern == "" {
		return false
	}
	return len(request.URL.Path) >= len("/api/v1/") && request.URL.Path[:len("/api/v1/")] == "/api/v1/" || usesARDProblem(request.URL.Path) || request.URL.Path == "/.well-known/ard.json" || request.URL.Path == "/.well-known/ai-catalog.json"
}
