//go:build functional

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/open-agentic-catalog/open-agentic-catalog/api"
)

func TestRemoteSourceComparisonContractAndWire(t *testing.T) {
	t.Parallel()
	document, err := openapi3.NewLoader().LoadFromData(api.ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	op := document.Paths.Value("/api/v1/federation/snapshots/{snapshotId}/comparison").Get
	if op == nil || op.OperationID != "compareFederationSourceSnapshots" {
		t.Fatal("comparison operation drift")
	}
	const before = "00000000-0000-7000-8000-000000000001"
	const after = "00000000-0000-7000-8000-000000000002"
	const responseBody = `{"sourceId":"00000000-0000-7000-8000-000000000003","sourceRevision":7,"before":{"id":"00000000-0000-7000-8000-000000000001","memberCount":2,"startedAt":"2026-01-01T00:00:00Z","withdrawalAuthority":true},"after":{"id":"00000000-0000-7000-8000-000000000002","memberCount":1,"startedAt":"2026-01-02T00:00:00Z","withdrawalAuthority":false},"items":[{"position":1,"sourceKey":"a-missing-version","change":"absent","beforeCount":1,"afterCount":0,"before":{"observationId":"00000000-0000-7000-8000-000000000004","identifier":"urn:air:example.com:demo:agent","version":"","versionPresent":false,"kind":"agent","identityDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","occurrence":{"observationId":"00000000-0000-7000-8000-000000000004","sourceId":"00000000-0000-7000-8000-000000000003","sourceRevision":7,"snapshotId":"00000000-0000-7000-8000-000000000001","retrievalDigest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","entryLocation":"/entries/0","exactEntryDigest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","evidenceComplete":true,"observedAt":"2026-01-01T00:00:00Z"}}}],"nextCursor":"opaque+/=& caf\u00e9"}`
	var expected map[string]any
	if err := json.Unmarshal([]byte(responseBody), &expected); err != nil {
		t.Fatal(err)
	}
	if err := op.Responses.Status(200).Value.Content.Get("application/json").Schema.Value.VisitJSON(expected); err != nil {
		t.Fatal("comparison fixture violates the response contract", err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		q := r.URL.Query()
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/federation/snapshots/"+before+"/comparison" || q.Get("otherSnapshotId") != after || q.Get("view") != "all" || q.Get("limit") != "1" || r.Header.Get("Authorization") != "Bearer explicit" || r.Header.Get("Idempotency-Key") != "" {
			t.Error("incorrect comparison exchange", r.Method, r.URL)
		}
		w.Header().Set("X-Request-ID", "comparison-request")
		if n == 1 {
			if q.Has("cursor") {
				t.Error("first page has cursor")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, responseBody)
			return
		}
		if q.Get("cursor") != "opaque+/=& café" {
			t.Error("opaque cursor changed")
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"type":"about:blank","title":"Invalid argument","status":400,"code":"INVALID_ARGUMENT","detail":"private upstream text","requestId":"comparison-request"}`)
	}))
	defer server.Close()
	args := make([]string, 0, 11)
	args = append(args, "source-compare", "--before-snapshot-id", before, "--after-snapshot-id", after, "--view", "all", "--limit", "1")
	var out bytes.Buffer
	if err := runRemote(context.Background(), args, remoteTestEnvironment(server.URL, "explicit"), &out); err != nil {
		t.Fatal(err)
	}
	var envelope remoteEnvelope
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	var page struct {
		NextCursor string `json:"nextCursor"`
	}
	if err := json.Unmarshal(envelope.Data, &page); err != nil || envelope.Meta.RequestID != "comparison-request" || page.NextCursor != "opaque+/=& café" || requests.Load() != 1 {
		t.Fatalf("page lost or automatically continued: %s %v", out.Bytes(), err)
	}
	out.Reset()
	err = runRemote(context.Background(), append(args, "--cursor", "opaque+/=& café"), remoteTestEnvironment(server.URL, "explicit"), &out)
	var failure remoteAPIError
	if !errors.As(err, &failure) || failure.Status != 400 || failure.Code != "INVALID_ARGUMENT" || failure.RequestID != "comparison-request" || out.Len() != 0 || requests.Load() != 2 {
		t.Fatalf("refusal lost, emitted success or retried: %v %s", err, &out)
	}
}
