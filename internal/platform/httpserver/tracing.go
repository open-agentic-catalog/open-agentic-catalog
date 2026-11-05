// HTTP tracing records approved route patterns and private causal context while
// excluding operational traffic and raw request data.

package httpserver

import (
	"net/http"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// traceHTTP surrounds product middleware, including rejections, without tracing
// health/scrape traffic or recording raw URL paths, headers or response bodies.
func traceHTTP(tracer catalog.Tracer, router *http.ServeMux, next http.Handler) http.Handler {
	if tracer == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pattern := router.Handler(r)
		route := safeLogRoute(pattern)
		switch route {
		case "/livez", "/readyz", "/startupz", "/metrics", "/status", "/version":
			next.ServeHTTP(w, r)
			return
		}
		parent := incomingTrace(r.Header)
		ctx, end := tracer.Start(r.Context(), "HTTP "+safeLogMethod(r.Method)+" "+route, catalog.TraceServer, parent)
		tracked := &statusWriter{ResponseWriter: w}
		outcome := "error"
		defer func() { end(outcome) }()
		next.ServeHTTP(tracked, r.WithContext(ctx))
		outcome = "ok"
		if tracked.status >= 500 {
			outcome = "5xx"
		} else if tracked.status >= 400 {
			outcome = "4xx"
		}
	})
}
