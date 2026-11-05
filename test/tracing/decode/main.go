// Command decode checks the owned OTLP capture on stdin and emits safe causal fields.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/open-agentic-catalog/open-agentic-catalog/test/tracing"
)

// main bounds stdin before privacy validation and emits only decoded causal fields.
// Invalid input or output failure produces a nonzero status for the calling harness.
func main() {
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, (24<<20)+1))
	if err != nil {
		fmt.Fprintln(os.Stderr, "trace input failed")
		os.Exit(1)
	}
	spans, err := tracing.Decode(raw)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(spans); err != nil {
		os.Exit(1)
	}
}
