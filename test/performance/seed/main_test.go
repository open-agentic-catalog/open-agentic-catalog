// Unit tests exercise isolated validation and controlled dependencies.
package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	fixture "github.com/open-agentic-catalog/open-agentic-catalog/test/performance/scalefixture"
)

func TestPluginRetrievalUsesExactArchivesAndHonorsLimits(t *testing.T) {
	for _, i := range []int{3, 7, 11} {
		representation := fixture.Representation(i)
		archive, ok := fixture.PluginArchive(representation.URL)
		if !ok {
			t.Fatal("missing embedded sample")
		}
		request := catalog.ExternalRepresentationFetchRequest{URL: representation.URL, MediaType: catalog.PluginMediaZIP, MaxResponseBytes: int64(len(archive))}
		result, err := (retrieval{}).FetchExternalRepresentation(t.Context(), request)
		if err != nil || result.ContentDigest != fmt.Sprintf("sha256:%x", sha256.Sum256(archive)) || len(result.Body) != len(archive) {
			t.Fatal(result.ContentDigest, err)
		}
		result.Body[0] ^= 1
		again, err := (retrieval{}).FetchExternalRepresentation(t.Context(), request)
		if err != nil || again.Body[0] != archive[0] {
			t.Fatal("fixture bytes were mutated", err)
		}
		for _, bad := range []catalog.ExternalRepresentationFetchRequest{
			{URL: request.URL, MediaType: request.MediaType, MaxResponseBytes: int64(len(archive) - 1)},
			{URL: "https://other.example/package.zip", MediaType: request.MediaType, MaxResponseBytes: request.MaxResponseBytes},
			{URL: request.URL, MediaType: "application/json", MaxResponseBytes: request.MaxResponseBytes},
		} {
			if _, err := (retrieval{}).FetchExternalRepresentation(t.Context(), bad); err == nil {
				t.Fatal("unexpected package request accepted")
			}
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := (retrieval{}).FetchExternalRepresentation(ctx, request); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
}

func TestInvalidSeedArgumentsDoNotConnect(t *testing.T) {
	for _, args := range [][]string{nil, {"1", "1001", "60", "unused"}, {"0", "1000", "60", "unused"}, {"1", "1000", "7201", "unused"}, {"1", "1000", "60", "unused", "unknown"}, {"0", "1000", "60", "unused", "local"}, {"1", "10000", "60", "unused", "public"}} {
		if run(args) == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
