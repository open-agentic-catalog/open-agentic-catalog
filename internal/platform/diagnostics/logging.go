// Logging construction is shared by the independent backend and UI hosts.
package diagnostics

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

// ValidateLogging admits only the documented startup vocabulary, without echoing input.
func ValidateLogging(level, format string) error {
	switch level {
	case "debug", "info", "warn", "error":
	default:
		return safeerr.New("logLevel must be debug, info, warn, or error")
	}
	if format != "text" && format != "json" {
		return safeerr.New("logFormat must be text or json")
	}
	return nil
}

// NewLogger constructs a synchronous standard handler from validated settings.
// It neither owns the writer nor changes the process-global default logger.
func NewLogger(output io.Writer, level, format string) *slog.Logger {
	threshold := slog.LevelInfo
	switch level {
	case "debug":
		threshold = slog.LevelDebug
	case "warn":
		threshold = slog.LevelWarn
	case "error":
		threshold = slog.LevelError
	}
	options := &slog.HandlerOptions{Level: threshold, ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
		if len(groups) == 0 && a.Key == slog.TimeKey {
			a.Value = slog.TimeValue(a.Value.Time().UTC())
		}
		return a
	}}
	if format == "json" {
		return slog.New(slog.NewJSONHandler(output, options))
	}
	return slog.New(slog.NewTextHandler(output, options))
}

// Process contains only non-secret process identity, independent of tracing.
// Collector-provided deployment/instance identity is deliberately not inferred.
type Process struct {
	Service, Role, Environment, Version string
}

// Logger derives a child without mutating the caller's handler or level policy.
func (p Process) Logger(logger *slog.Logger) *slog.Logger {
	return logger.With("service", logToken(p.Service), "role", logToken(p.Role), "environment", logToken(p.Environment), "version", logToken(p.Version))
}

// Startup reports selected configuration, not the behavior of an injected handler.
// Values are bounded deployment/build tokens; private configuration is never serialized.
func Startup(logger *slog.Logger, commit, level, format string, injected bool) {
	if fallback := standardFallback.Load(); fallback != nil {
		fallback.logger.Store(logger)
	}
	source := "configured"
	if injected {
		source = "injected"
	}
	logger.Info("logging configured", "event", "process.logging_configured", "component", "process", "commit", logToken(commit), "log_level", level, "log_format", format, "logger_source", source)
}

func logToken(value string) string {
	if value == "" || len(value) > 128 || strings.ContainsFunc(value, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._+-", r))
	}) {
		return "unknown"
	}
	return value
}

type formattedError struct {
	error
	format  string
	process Process
}

func (e formattedError) Unwrap() error { return e.error }

// WithFormat carries trusted startup formatting to the terminal stderr reporter.
// It preserves error identity and private causes; nil remains nil.
func WithFormat(err error, format string, process Process) error {
	if err == nil {
		return nil
	}
	return formattedError{err, format, process}
}

func errorProcess(err error, command string) Process {
	var formatted formattedError
	if errors.As(err, &formatted) {
		return formatted.process
	}
	return Process{Service: command}
}

func errorFormat(err error) string {
	var formatted formattedError
	if errors.As(err, &formatted) && formatted.format == "json" {
		return "json"
	}
	return "text"
}

// Warning is one fixed event/component's repeat window. Each owning call site
// allocates its own state; no request, identity or destination can create keys.
// Emission happens outside the lock. Filtered logs never affect owning metrics.
type Warning struct {
	mu         sync.Mutex
	last       time.Time
	seen       bool
	suppressed uint64
}

func (w *Warning) Warn(now time.Time, logger *slog.Logger, message string, attributes ...any) {
	w.mu.Lock()
	if w.seen && now.Sub(w.last) < time.Minute {
		w.suppressed++
		w.mu.Unlock()
		return
	}
	suppressed := w.suppressed
	w.last, w.seen, w.suppressed = now, true, 0
	w.mu.Unlock()
	logger.Warn(message, append(attributes, "suppressed", suppressed)...)
}
