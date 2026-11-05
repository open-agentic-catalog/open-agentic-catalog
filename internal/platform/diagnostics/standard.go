package diagnostics

import (
	"log"
	"log/slog"
	"os"
	"sync/atomic"
	"time"
)

// Only command entry points install this process-wide fallback. Library/test
// logger construction leaves the standard logger and slog defaults untouched.
var standardFallback atomic.Pointer[standardOutput]

// ProtectStandardLog contains unstructured dependency diagnostics, including
// net/http's unsolicited-response bytes. It does not redirect process stderr.
// Startup binds the validated process logger later; bootstrap uses safe stderr.
func ProtectStandardLog(command string) {
	output := &standardOutput{}
	output.logger.Store((Process{Service: command}).Logger(NewLogger(os.Stderr, "warn", "text")))
	standardFallback.Store(output)
	log.SetOutput(output)
	log.SetPrefix("")
	log.SetFlags(0)
}

type standardOutput struct {
	logger  atomic.Pointer[slog.Logger]
	warning Warning
}

func (s *standardOutput) Write(raw []byte) (int, error) {
	s.warning.Warn(time.Now(), s.logger.Load(), "dependency emitted a diagnostic", "event", "dependency.diagnostic", "component", "dependency")
	return len(raw), nil
}
