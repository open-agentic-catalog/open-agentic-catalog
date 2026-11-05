package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

func exportTestArtifact(raw []byte) (exportRepresentation, http.Header) {
	digest := sha256.Sum256(raw)
	return exportRepresentation{ID: exportRepID, ArtifactID: strings.TrimPrefix(exportChecksum(raw), "sha256:"), ContentDigest: exportChecksum(raw), Size: int64(len(raw)), MediaType: "text/markdown; charset=utf-8", ProfileID: "skill-profile", ValidationState: "valid"}, http.Header{
		"Content-Type":   {"text/markdown; charset=utf-8"},
		"Content-Length": {strconv.Itoa(len(raw))},
		"Content-Digest": {"sha-256=:" + base64.StdEncoding.EncodeToString(digest[:]) + ":"},
	}
}

func TestExportArtifactIntegrityBoundsAndCleanup(t *testing.T) {
	const original = "---\nname: example\n---\n# Retained document\r\nKeep these exact bytes.\n"
	for _, name := range []string{"success", "missing digest", "noncanonical digest", "duplicate digest", "wrong media", "wrong parameters", "duplicate media", "missing length", "wrong length", "duplicate length", "unknown length", "short body", "long body", "changed body", "compressed", "uncompressed", "read failure", "cancelled read", "size limit", "redirect", "denial", "private error", "error body read failure", "oversized error body", "cancelled error body", "error body deadline", "request failure", "request deadline"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			rep, header := exportTestArtifact([]byte(original))
			body := &catalogctlTrackedBody{Reader: strings.NewReader(original)}
			response := &http.Response{StatusCode: http.StatusOK, Header: header, Body: body, ContentLength: rep.Size}
			limit := rep.Size
			calls := 0
			session := &remoteSession{origin: "https://catalog.invalid", token: "fixture-token", client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: catalogctlRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.String() != "https://catalog.invalid/api/v1/artifacts/"+rep.ArtifactID+"/content" || r.Context() != ctx || r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer fixture-token" || r.Header.Get("Accept") != rep.MediaType || r.Header.Get("Cookie") != "" || r.Body != nil {
					t.Error("artifact request lost origin/credential/selection binding")
				}
				if name == "request failure" {
					return nil, errors.New("PRIVATE_SENTINEL fixture-token")
				}
				if name == "request deadline" {
					return nil, context.DeadlineExceeded
				}
				return response, nil
			})}}
			switch name {
			case "missing digest":
				header.Del("Content-Digest")
			case "noncanonical digest":
				header.Set("Content-Digest", "sha-256=:bad=:")
			case "duplicate digest":
				header.Add("Content-Digest", header.Get("Content-Digest"))
			case "wrong media":
				header.Set("Content-Type", "application/json")
			case "wrong parameters":
				header.Set("Content-Type", "text/markdown; charset=latin1")
			case "duplicate media":
				header.Add("Content-Type", header.Get("Content-Type"))
			case "missing length":
				header.Del("Content-Length")
			case "wrong length":
				header.Set("Content-Length", "1")
			case "duplicate length":
				header.Add("Content-Length", header.Get("Content-Length"))
			case "unknown length":
				response.ContentLength = -1
			case "short body":
				body.Reader = strings.NewReader(original[:len(original)-1])
			case "long body":
				body.Reader = strings.NewReader(original + "x")
			case "changed body":
				body.Reader = strings.NewReader(strings.Replace(original, "name", "Name", 1))
			case "compressed":
				header.Set("Content-Encoding", "gzip")
			case "uncompressed":
				response.Uncompressed = true
			case "read failure":
				body.Reader = exportFailReader{}
			case "cancelled read":
				body.Reader = exportCancelReader{cancel: cancel}
			case "size limit":
				limit--
			case "redirect":
				response.StatusCode = http.StatusFound
				header.Set("Location", "https://never-fetch.invalid/PRIVATE_SENTINEL")
			case "denial":
				response.StatusCode = http.StatusForbidden
				header.Set("Content-Type", "application/problem+json")
				body.Reader = strings.NewReader(`{"code":"FORBIDDEN","detail":"fixture-token unavailable"}`)
			case "private error":
				response.StatusCode = http.StatusBadGateway
				header.Set("Content-Type", "text/html")
				body.Reader = strings.NewReader("PRIVATE_SENTINEL fixture-token")
			case "error body read failure", "oversized error body", "cancelled error body", "error body deadline":
				response.StatusCode = http.StatusBadGateway
				header.Set("Content-Type", "application/problem+json")
				const problem = `{"code":"PARTIAL_PRIVATE_CODE","detail":"PRIVATE_SENTINEL fixture-token"}`
				switch name {
				case "error body read failure":
					body.Reader = io.MultiReader(strings.NewReader(problem), exportFailReader{})
				case "oversized error body":
					body.Reader = io.MultiReader(strings.NewReader(problem), strings.NewReader(strings.Repeat(" ", remoteMaxBody)))
				case "cancelled error body":
					body.Reader = exportCancelReader{cancel: cancel}
				case "error body deadline":
					body.Reader = iotest.ErrReader(context.DeadlineExceeded)
				}
			}
			raw, err := downloadExportArtifact(ctx, session, rep, limit)
			if name == "success" {
				if err != nil || string(raw) != original {
					t.Fatalf("exact bytes: %q %v", raw, err)
				}
			} else if err == nil || raw != nil || strings.Contains(safeerr.Message(err), "PRIVATE_SENTINEL") {
				t.Fatalf("unsafe acceptance/diagnostic: bytes=%q err=%v", raw, err)
			}
			var failure remoteAPIError
			if name == "error body read failure" || name == "oversized error body" {
				if !errors.As(err, &failure) || failure.Status != http.StatusBadGateway || failure.Code != "http_error" || failure.Detail != "API returned an unsuccessful response" {
					t.Fatal("incomplete or oversized problem was trusted", err)
				}
			}
			if name == "cancelled error body" || name == "error body deadline" {
				if err == nil || safeerr.Message(err) != "export: artifact request cancelled or deadline exceeded" {
					t.Fatal("error body cancellation/deadline was lost", err)
				}
			}
			if errors.As(err, &failure) {
				if strings.Contains(failure.Error(), "PRIVATE_SENTINEL") || strings.Contains(failure.Error(), "fixture-token") {
					t.Fatal("artifact error leaked private content/credential")
				}
				if name == "denial" && (failure.Status != 403 || failure.Code != "FORBIDDEN" || failure.Detail != "[redacted] unavailable") {
					t.Fatal("safe denial details lost", failure)
				}
			}
			wantCalls, wantCloses := 1, 1
			if name == "size limit" {
				wantCalls, wantCloses = 0, 0
			} else if name == "request failure" || name == "request deadline" {
				wantCloses = 0
			}
			if calls != wantCalls || body.closes != wantCloses || body.bytesRead > len(original)+1 && response.StatusCode == http.StatusOK || body.bytesRead > remoteMaxBody+1 {
				t.Fatalf("request/read/cleanup bounds: calls=%d closes=%d read=%d", calls, body.closes, body.bytesRead)
			}
		})
	}
}

type exportCancelReader struct{ cancel context.CancelFunc }

func (r exportCancelReader) Read([]byte) (int, error) {
	r.cancel()
	return 0, context.Canceled
}

type exportFailReader struct{}

func (exportFailReader) Read([]byte) (int, error) {
	return 0, errors.New("PRIVATE_SENTINEL fixture-token")
}

func TestExportPrepareInlineAndArtifactDeliveries(t *testing.T) {
	const inline = `{ "number":9007199254740993, "unknown":{"x":1.234e+50} }`
	for _, delivery := range []string{"inline", "artifact", "inline limit", "record limit", "record denied", "invalid record", "artifact denied", "cancelled"} {
		t.Run(delivery, func(t *testing.T) {
			rep, headers := exportTestArtifact([]byte("# Standalone document\n"))
			raw := exportTestRecord(exportTestRepresentation(inline))
			if delivery == "artifact" || delivery == "artifact denied" {
				encoded, err := json.Marshal(rep)
				if err != nil {
					t.Fatal(err)
				}
				raw = exportTestRecord(string(encoded))
			} else if delivery == "invalid record" {
				raw = bytes.Replace(raw, []byte(`,"version":"1.0.0"`), nil, 1)
			}
			options := exportOptions{releaseID: exportReleaseID, maxContentBytes: remoteMaxBody, remote: remoteOptions{path: "/api/v1/catalog/resources/" + exportReleaseID, maxResponseBytes: remoteMaxBody}}
			if delivery == "inline limit" {
				options.maxContentBytes = int64(len(inline) - 1)
			} else if delivery == "record limit" {
				options.remote.maxResponseBytes = int64(len(raw) - 1)
			}
			calls, closes := 0, []*catalogctlTrackedBody{}
			session := &remoteSession{origin: "https://catalog.invalid", token: "fixture-token", client: &http.Client{Transport: catalogctlRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if delivery == "cancelled" {
					return nil, context.DeadlineExceeded
				}
				if r.URL.Path == options.remote.path {
					body := &catalogctlTrackedBody{Reader: bytes.NewReader(raw)}
					closes = append(closes, body)
					status := 200
					if delivery == "record denied" {
						status = 404
					}
					return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: body}, nil
				}
				if delivery != "artifact" && delivery != "artifact denied" || r.URL.Path != "/api/v1/artifacts/"+rep.ArtifactID+"/content" {
					t.Fatal("unexpected extra request", r.URL)
				}
				body := &catalogctlTrackedBody{Reader: strings.NewReader("# Standalone document\n")}
				closes = append(closes, body)
				if delivery == "artifact denied" {
					return &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{"Content-Type": {"application/problem+json"}}, Body: body}, nil
				}
				return &http.Response{StatusCode: http.StatusOK, Header: headers, Body: body, ContentLength: rep.Size}, nil
			})}}
			manifest, content, err := prepareExport(t.Context(), session, options)
			if delivery == "inline" || delivery == "artifact" {
				if err != nil || manifest.File.Digest != exportChecksum(content) || manifest.File.Size != int64(len(content)) {
					t.Fatal("file checksum lost", manifest, err)
				}
				if delivery == "inline" && (string(content) != inline || manifest.File.Digest == manifest.Recorded.Digest) {
					t.Fatal("inline bytes changed or digest scope conflated")
				}
				if delivery == "artifact" && (manifest.Recorded.Digest != manifest.File.Digest || manifest.Disposition != "exact_document" || calls != 2) {
					t.Fatal("exact retained bytes lost", manifest)
				}
			} else {
				wantCalls := 1
				if delivery == "artifact denied" {
					wantCalls = 2
					var failure remoteAPIError
					if !errors.As(err, &failure) || failure.Status != http.StatusForbidden {
						t.Fatal("artifact denial was lost", err)
					}
				}
				if err == nil || content != nil || calls != wantCalls {
					t.Fatalf("failure allowed later work: %v %d", err, calls)
				}
			}
			for _, body := range closes {
				if body.closes != 1 {
					t.Fatal("body not closed")
				}
			}
		})
	}
}
