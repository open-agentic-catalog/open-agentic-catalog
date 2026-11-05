// Package diagnostics projects private failures at command and HTTP boundaries.
package diagnostics

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

// Report writes one terminal diagnostic without calling Error on a private cause.
// command and code are fixed application vocabulary, never argument values.
func Report(output io.Writer, command, code string, err error, service bool) {
	if errors.Is(err, safeerr.ErrPanic) {
		code = "INTERNAL_PANIC"
	}
	message := safeerr.Message(err)
	if service {
		errorProcess(err, command).Logger(NewLogger(output, "error", errorFormat(err))).Error(message, "event", "process.failed", "component", "process", "command", command, "stage", safeerr.Stage(err), "error_code", code, "error_type", fmt.Sprintf("%T", err))
	} else {
		_, _ = fmt.Fprintf(output, "%s: %s: %s: %s\n", command, safeerr.Stage(err), code, message)
	}
}

// Flags suppresses flag's implicit raw-value diagnostics. Parse retains errors
// internally and prints only declared help, which contains no supplied values.
func Flags(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Usage = func() {
		// Parse renders safe help without private defaults or argument values.
	}
	return flags
}

// Parse admits arguments without letting flag print user-controlled text.
func Parse(flags *flag.FlagSet, arguments []string) error {
	flags.SetOutput(io.Discard)
	err := flags.Parse(arguments)
	if errors.Is(err, flag.ErrHelp) {
		_, _ = fmt.Fprintf(os.Stdout, "Usage of %s:\n", flags.Name())
		flags.VisitAll(func(value *flag.Flag) {
			_, _ = fmt.Fprintf(os.Stdout, "  -%s\n    %s\n", value.Name, value.Usage)
		})
	}
	return safeerr.Wrap(err, "invalid flags; use "+flags.Name()+" --help")
}

// HTTPErrorLog replaces net/http's raw transport/panic fallback with one fixed
// ERROR signal. The writer does not parse or retain private standard-library text.
func HTTPErrorLog(logger *slog.Logger) *log.Logger {
	return log.New(httpErrors{logger}, "", 0)
}

type httpErrors struct{ logger *slog.Logger }

func (w httpErrors) Write(raw []byte) (int, error) {
	w.logger.Error("HTTP listener diagnostic", "event", "http.listener_error", "component", "http")
	return len(raw), nil
}

// RecoverHTTP contains request failures at hosts without the product API's
// transactional recovery middleware. Abort rather than append to a possibly
// committed response; net/http handles ErrAbortHandler without logging it again.
func RecoverHTTP(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			if err, ok := recovered.(error); !ok || !errors.Is(err, http.ErrAbortHandler) {
				logger.Error("HTTP handler panic", "event", "http.panic", "component", "http")
			}
			panic(http.ErrAbortHandler)
		}()
		next.ServeHTTP(w, r)
	})
}
