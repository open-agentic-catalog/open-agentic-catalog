// JSON-LD unit tests exercise canonicalization, query-context rejection and the
// remote-context budget using static documents and a controlled fetch function.
package standards

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// TestNormalizeARDEntryPreservesIdentityAndTrust checks canonical @id spelling,
// lower-case trust evidence and exact numeric spelling without altering input.
func TestNormalizeARDEntryPreservesIdentityAndTrust(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"@context":"https://agenticresourcediscovery.org/context/v1","@id":"urn:air:example.com:default:item","identifier":"urn:air:example.com:default:item","displayName":"Item","type":"application/json","data":{"sequence":9007199254740993,"nested":{ "z": 2, "a": 1 }},"trustManifest":{"identity":"did:web:example.com","trustSchema":{"future":{"sequence":9007199254740993}},"future":{"@context":"https://never-fetch.example/context","sequence":9007199254740993,"empty":null}}}`)
	before := bytes.Clone(raw)
	normalized, err := NormalizeARDEntry(raw, func(uri string) ([]byte, error) {
		t.Errorf("embedded base context unexpectedly fetched: %s", uri)
		return nil, errors.New("unexpected fetch")
	})
	if err != nil || !bytes.Equal(raw, before) {
		t.Fatalf("normalization error=%v, input changed=%t", err, !bytes.Equal(raw, before))
	}
	if err := ValidateARDEntry(normalized); err != nil {
		t.Fatalf("normalized entry is invalid: %v", err)
	}
	var got, want map[string]any
	for _, document := range []struct {
		raw    []byte
		target *map[string]any
	}{{normalized, &got}, {before, &want}} {
		decoder := json.NewDecoder(bytes.NewReader(document.raw))
		decoder.UseNumber()
		if err := decoder.Decode(document.target); err != nil {
			t.Fatal(err)
		}
	}
	delete(want, "@context")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized evidence=%s, want unchanged fields without @context", normalized)
	}
}

// TestResolveARDTermRejectsInvalidContexts requires unsupported context mappings
// to fail without returning a partially resolved filter or using a remote loader.
func TestResolveARDTermRejectsInvalidContexts(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, term, want string
		context          any
	}{
		{"remote context", "kind", "resolve ARD query context:", "https://contexts.example.com/query"},
		{"invalid context mapping", "kind", "resolve ARD query context:", map[string]any{"kind": map[string]any{"@id": 42}}},
		{"node identity", "id", "did not resolve to one node", nil},
		{"aliased identity", "alias", "did not resolve to one node", map[string]any{"alias": "@id"}},
		{"empty leading term", ".field", "did not resolve to one node", nil},
		{"foreign vocabulary", "alias", "not supported by this Registry", map[string]any{"alias": "https://other.example.com/term"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveARDTerm(tc.term, tc.context)
			if err == nil || got != "" || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("term=%q error=%v, want empty result and %q", got, err, tc.want)
			}
		})
	}
}

// TestContextLoaderCountsFailedAttempts prevents failed or malformed contexts
// from resetting the remote budget; the pinned base context remains local.
func TestContextLoaderCountsFailedAttempts(t *testing.T) {
	t.Parallel()
	base, err := loadBaseContext()
	if err != nil {
		t.Fatal(err)
	}
	fetchErr := errors.New("controlled fetch failure")
	for _, malformed := range []bool{false, true} {
		t.Run(fmt.Sprintf("malformed=%t", malformed), func(t *testing.T) {
			calls := 0
			loader := &boundedContextLoader{baseContext: base, fetch: func(uri string) ([]byte, error) {
				calls++
				if uri != fmt.Sprintf("https://contexts.example.com/%d", calls) {
					t.Fatalf("unexpected context request: %s", uri)
				}
				if malformed {
					return []byte(`{"@context":{},"@context":{}}`), nil
				}
				return nil, fetchErr
			}}
			if document, err := loader.LoadDocument(ARDBaseContext); err != nil || document == nil {
				t.Fatalf("pinned context before remote attempts: document=%#v error=%v", document, err)
			}
			for i := 1; i <= maxRemoteContexts; i++ {
				document, err := loader.LoadDocument(fmt.Sprintf("https://contexts.example.com/%d", i))
				if document != nil || err == nil || (!malformed && !errors.Is(err, fetchErr)) || (malformed && !strings.Contains(err.Error(), "not unambiguous JSON")) {
					t.Fatalf("attempt %d: document=%#v error=%v", i, document, err)
				}
			}
			if document, err := loader.LoadDocument("https://contexts.example.com/over-budget"); document != nil || err == nil || !strings.Contains(err.Error(), "budget was exhausted") {
				t.Fatalf("over budget: document=%#v error=%v", document, err)
			}
			if document, err := loader.LoadDocument(ARDBaseContext); err != nil || document == nil || document.DocumentURL != ARDBaseContext || !reflect.DeepEqual(document.Document, base) {
				t.Fatalf("pinned context after exhaustion: document=%#v error=%v", document, err)
			}
			if calls != maxRemoteContexts {
				t.Fatalf("fetches=%d, want %d", calls, maxRemoteContexts)
			}
		})
	}
}
