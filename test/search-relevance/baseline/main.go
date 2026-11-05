// Command baseline evaluates both owning lexical scorers without a service or
// database. Its JSON matrix is consumed by the semantic relevance harness.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

// input carries only public fixture metadata and query text, never credentials.
type input struct {
	Documents []struct {
		ID          string   `json:"id"`
		Identifier  string   `json:"identifier"`
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Tags        []string `json:"tags"`
		Queries     []string `json:"representativeQueries"`
	} `json:"documents"`
	Queries []struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	} `json:"queries"`
}

// run emits unrounded integer scores; the caller applies explicit fixture
// eligibility and the respective surface's documented deterministic tie order.
func run(reader io.Reader, writer io.Writer) error {
	data, err := io.ReadAll(io.LimitReader(reader, (2<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 2<<20 {
		return fmt.Errorf("fixture input exceeds 2 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var request input
	if err := decoder.Decode(&request); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("expected one JSON input")
	}
	if len(request.Documents) == 0 || len(request.Documents) > 200 || len(request.Queries) == 0 || len(request.Queries) > 200 {
		return fmt.Errorf("fixture must have 1..200 documents and queries")
	}
	result := map[string]map[string]map[string]int{"control": {}, "public": {}}
	seen := map[string]bool{}
	for _, d := range request.Documents {
		if d.ID == "" || seen[d.ID] {
			return fmt.Errorf("duplicate or empty document ID")
		}
		seen[d.ID] = true
	}
	for _, q := range request.Queries {
		if q.ID == "" || result["control"][q.ID] != nil || strings.TrimSpace(q.Text) == "" {
			return fmt.Errorf("invalid query")
		}
		result["control"][q.ID], result["public"][q.ID] = map[string]int{}, map[string]int{}
		for _, d := range request.Documents {
			result["control"][q.ID][d.ID] = catalog.CoreLexicalScore(catalog.SearchDocument{Identifier: d.Identifier, DisplayName: d.Name, Description: d.Description, Tags: d.Tags, RepresentativeQueries: d.Queries}, q.Text)
			result["public"][q.ID][d.ID] = publication.CoreLexicalScore(publication.Entry{Identifier: d.Identifier, DisplayName: d.Name, Description: d.Description, Tags: d.Tags, RepresentativeQueries: d.Queries}, strings.Join(strings.Fields(q.Text), " "))
		}
	}
	return json.NewEncoder(writer).Encode(result)
}

// main keeps diagnostics separate from machine-readable scores.
func main() {
	if err := run(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
