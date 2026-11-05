// Public document units check retained identity, cache policy and artifact
// eligibility through controlled snapshot/document reads.
package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

func TestPublicSnapshotDocumentProjection(t *testing.T) {
	t.Parallel()
	for _, mode := range []DiscoveryMode{DiscoveryAnonymous, DiscoveryAuthenticated} {
		for _, family := range []string{"ARD", "AI Catalog"} {
			for _, condition := range []string{"unconditional", "etag", "modified", "etag precedence", "old date", "invalid date"} {
				t.Run(fmt.Sprintf("%s/%s/%s", mode, family, condition), func(t *testing.T) {
					snapshot, _ := publicFixtureSnapshot(t, mode)
					ctx := t.Context()
					calls := 0
					s := publicProjectionServer(t, mode, func(c context.Context, tenant string) (publication.Snapshot, error) {
						calls++
						if !samePublicProjectionContext(c, ctx, mode) || tenant != projectionTenant {
							t.Fatal("snapshot read binding")
						}
						return snapshot, nil
					})
					path, media, etag, digest := "/.well-known/ard.json", "application/json", snapshot.ETag, snapshot.ARDContentDigest
					var expected any = snapshot.ARD
					handler := s.handleARDManifest
					if family == "AI Catalog" {
						path = "/.well-known/ai-catalog.json"
						media = "application/ai-catalog+json"
						etag = snapshot.AICatalogETag
						digest = snapshot.AIContentDigest
						expected = snapshot.AICatalog
						handler = s.handleAICatalog
					}
					r := httptest.NewRequestWithContext(ctx, http.MethodGet, path, nil)
					r.Host = publicFixtureHost
					r.Header.Set("Authorization", "Bearer reader")
					status := 200
					switch condition {
					case "etag":
						r.Header.Set("If-None-Match", "W/"+etag)
						status = 304
					case "modified":
						r.Header.Set("If-Modified-Since", projectionTime.Format(http.TimeFormat))
						status = 304
					case "etag precedence":
						r.Header.Set("If-None-Match", `"different"`)
						r.Header.Set("If-Modified-Since", projectionTime.Add(time.Hour).Format(http.TimeFormat))
					case "old date":
						r.Header.Set("If-Modified-Since", snapshot.CreatedAt.Format(http.TimeFormat))
					case "invalid date":
						r.Header.Set("If-Modified-Since", "invalid")
					}
					w := httptest.NewRecorder()
					w.Header().Add("Vary", "origin, accept-encoding")
					w.Header().Add("Vary", "Origin, ")
					handler(w, r)
					cache, vary := "public, no-cache, no-transform", "Accept-Encoding, Origin"
					if mode == DiscoveryAuthenticated {
						cache = "private, no-cache, no-transform"
						vary = "Accept-Encoding, Authorization, Origin"
					}
					if w.Code != status || calls != 1 || w.Header().Get("Content-Type") != media || w.Header().Get("ETag") != etag || w.Header().Get("Content-Digest") != digest || w.Header().Get("Last-Modified") != projectionTime.Format(http.TimeFormat) || w.Header().Get("Cache-Control") != cache || w.Header().Get("Vary") != vary || w.Header().Get("Link") != publicDiscoveryLinks() {
						t.Fatalf("document response=%d headers=%v body=%s reads=%d", w.Code, w.Header(), w.Body, calls)
					}
					if status == 304 {
						if w.Body.Len() != 0 {
							t.Fatal("conditional document has body")
						}
						return
					}
					raw, err := json.Marshal(expected)
					if err != nil {
						t.Fatal(err)
					}
					if w.Body.String() != string(raw) || digestHeader(w.Body.Bytes()) != digest || strings.Contains(w.Body.String(), "private-fixture") {
						t.Fatalf("retained document mismatch: %s", w.Body)
					}
				})
			}
		}
	}
	// The helper also supports a candidate's creation time; active reads require
	// activation evidence and must not use an invalid active fixture for this arm.
	candidate := publication.Snapshot{CreatedAt: projectionTime}
	if got := snapshotModifiedAt(candidate); !got.Equal(projectionTime) {
		t.Fatalf("creation fallback=%v", got)
	}
}
func TestPublicSnapshotErrors(t *testing.T) {
	t.Parallel()
	for _, family := range []string{"ARD", "AI Catalog"} {
		for _, tc := range []struct {
			name   string
			err    error
			status int
			code   catalog.ErrorCode
		}{
			{"missing", catalog.E(catalog.CodeNotFound, "private-fixture-missing"), 404, catalog.CodeNotFound},
			{"unready", catalog.E(catalog.CodePublicationNotReady, "private-fixture-unready"), 503, catalog.CodePublicationNotReady},
			{"unreadable", errors.New("private-fixture-database"), 503, catalog.CodeDependencyUnavailable},
		} {
			t.Run(family+"/"+tc.name, func(t *testing.T) {
				calls := 0
				s := publicProjectionServer(t, DiscoveryAnonymous, func(ctx context.Context, tenant string) (publication.Snapshot, error) {
					calls++
					if ctx != t.Context() || tenant != projectionTenant {
						t.Fatal("failed snapshot binding")
					}
					return publication.Snapshot{}, tc.err
				})
				r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/.well-known/ard.json", nil)
				r.Host = publicFixtureHost
				w := httptest.NewRecorder()
				if family == "ARD" {
					s.handleARDManifest(w, r)
				} else {
					s.handleAICatalog(w, r)
				}
				requireARDProjectionProblem(t, w, tc.status, tc.code)
				if calls != 1 || w.Header().Get("ETag") != "" {
					t.Fatal("failure emitted document identity")
				}
			})
		}
	}
}
func TestPublicArtifactProjection(t *testing.T) {
	t.Parallel()
	for _, mode := range []DiscoveryMode{DiscoveryAnonymous, DiscoveryAuthenticated} {
		for _, scenario := range []string{"document", "conditional", "not allowlisted", "snapshot unavailable", "document unavailable"} {
			t.Run(fmt.Sprintf("%s/%s", mode, scenario), func(t *testing.T) {
				snapshot, document := publicFixtureSnapshot(t, mode)
				ctx := t.Context()
				reads, documents := 0, 0
				s := publicProjectionServer(t, mode, func(c context.Context, tenant string) (publication.Snapshot, error) {
					reads++
					if !samePublicProjectionContext(c, ctx, mode) || tenant != projectionTenant {
						t.Fatal("artifact snapshot binding")
					}
					if scenario == "snapshot unavailable" {
						return publication.Snapshot{}, errors.New("private-fixture-snapshot")
					}
					return snapshot, nil
				})
				s.catalog = projectionCatalog(t, publicDocumentRepository{document: func(c context.Context, tenant, key string) (catalog.DocumentBlob, error) {
					documents++
					if !samePublicProjectionContext(c, ctx, mode) || tenant != projectionTenant || key != document.ID {
						t.Fatal("artifact document binding")
					}
					if scenario == "document unavailable" {
						return catalog.DocumentBlob{}, catalog.E(catalog.CodeDependencyUnavailable, "private-fixture-document")
					}
					return document, nil
				}}, nil)
				artifactID := document.ID
				if scenario == "not allowlisted" {
					artifactID = strings.Repeat("0", 64)
				}
				r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/public/v1/artifacts/"+artifactID, nil)
				r.Host = publicFixtureHost
				r.SetPathValue("artifactId", artifactID)
				r.Header.Set("Authorization", "Bearer reader")
				if scenario == "conditional" {
					r.Header.Set("If-None-Match", `W/"`+document.ID+`"`)
				}
				w := httptest.NewRecorder()
				s.handlePublicArtifact(w, r)
				if reads != 1 {
					t.Fatalf("snapshot reads=%d", reads)
				}
				if scenario == "not allowlisted" || scenario == "snapshot unavailable" {
					requireARDProjectionProblem(t, w, 404, catalog.CodeNotFound)
					if documents != 0 {
						t.Fatal("artifact read before eligibility")
					}
					return
				}
				if documents != 1 {
					t.Fatalf("document reads=%d", documents)
				}
				if scenario == "document unavailable" {
					requireARDProjectionProblem(t, w, 503, catalog.CodeDependencyUnavailable)
					return
				}
				cache, vary := "public, max-age=60, must-revalidate", ""
				if mode == DiscoveryAuthenticated {
					cache = "private, max-age=60, must-revalidate"
					vary = "Authorization"
				}
				if w.Header().Get("ETag") != `"`+document.ID+`"` || w.Header().Get("Cache-Control") != cache || w.Header().Get("Vary") != vary {
					t.Fatalf("artifact cache=%v", w.Header())
				}
				if scenario == "conditional" {
					if w.Code != 304 || w.Body.Len() != 0 {
						t.Fatal("conditional artifact body")
					}
					return
				}
				media := "text/markdown"
				if w.Code != 200 || w.Body.String() != string(document.Content) || w.Header().Get("Content-Length") != strconv.Itoa(len(document.Content)) || w.Header().Get("Content-Type") != media || w.Header().Get("Content-Digest") != digestHeader(document.Content) || w.Header().Get("Content-Disposition") != `attachment; filename="`+document.ID+`"` {
					t.Fatalf("artifact response=%d headers=%v body=%s", w.Code, w.Header(), w.Body)
				}
			})
		}
	}
}
