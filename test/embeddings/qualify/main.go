// Command qualify exercises the production Go embedding client against a
// fixture-owned endpoint. The launcher supplies only synthetic metadata.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/embeddings"
)

// main accepts bounded fixture configuration on stdin, never command-line secrets.
func main() {
	if err := run(os.Stdin, os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run verifies batch ordering, stable repeated queries, normalization and actual
// provider overlength rejection. It reports no vectors or input text.
func run(input io.Reader, output io.Writer) error {
	settings := config.SemanticDefaults()
	decoder := json.NewDecoder(io.LimitReader(input, 32769))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return errors.New("invalid embedding fixture configuration")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || !settings.Enabled {
		return errors.New("expected one enabled embedding fixture configuration")
	}
	overlengthQuery := strings.Repeat("word ", 600)
	// Local byte rejection shares the provider's error category. Establish
	// admission before any inference so it cannot pass the provider control.
	if len(settings.QueryPrefix)+len(overlengthQuery) > settings.MaxInputBytes {
		return errors.New("overlength probe exceeds configured input byte limit")
	}
	client, err := embeddings.New(settings)
	if err != nil {
		return err
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	texts := []string{"Search public documentation for a software library.", "Translate a short sentence into French.", "Inspect a synthetic weather forecast."}
	started := time.Now()
	batch, err := client.Documents(ctx, texts)
	if err != nil {
		return fmt.Errorf("embedding batch: %w", err)
	}
	if len(batch) != len(texts) {
		return errors.New("embedding batch count mismatch")
	}
	for i, text := range texts {
		single, err := client.Documents(ctx, []string{text})
		if err != nil {
			return fmt.Errorf("embedding single: %w", err)
		}
		if !sameVector(batch[i], single[0]) {
			return errors.New("embedding batch/single ordering or stability mismatch")
		}
	}
	first, err := client.Query(ctx, texts[0])
	if err != nil {
		return fmt.Errorf("embedding query: %w", err)
	}
	second, err := client.Query(ctx, texts[0])
	if err != nil || !sameVector(first, second) {
		return errors.New("embedding repeated query is unstable")
	}
	// This fixed text exceeds both pinned profiles' contexts and fits the
	// checked byte ceiling; the provider must reject it, not truncate it.
	if _, err = client.Query(ctx, overlengthQuery); !errors.Is(err, embeddings.ErrRejected) {
		return errors.New("embedding provider did not reject overlength input")
	}
	return json.NewEncoder(output).Encode(struct {
		Dimensions         int   `json:"dimensions"`
		BatchOrdering      bool  `json:"batchOrdering"`
		RepeatStable       bool  `json:"repeatStable"`
		OverlengthRejected bool  `json:"overlengthRejected"`
		ElapsedMillis      int64 `json:"elapsedMillis"`
	}{settings.Dimensions, true, true, true, time.Since(started).Milliseconds()})
}

// sameVector requires finite unit vectors and stable components within the
// checked-in profiles' numeric tolerance, not merely matching dimensions.
func sameVector(a, b []float32) bool {
	if len(a) == 0 || len(a) != len(b) {
		return false
	}
	var normA, normB float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		if math.IsNaN(x) || math.IsNaN(y) || math.IsInf(x, 0) || math.IsInf(y, 0) || math.Abs(x-y) > 1e-5 {
			return false
		}
		normA += x * x
		normB += y * y
	}
	return math.Abs(normA-1) < 1e-5 && math.Abs(normB-1) < 1e-5
}
