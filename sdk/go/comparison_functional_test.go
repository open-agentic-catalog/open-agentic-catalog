//go:build functional

package catalogclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestFunctionalSnapshotComparison(t *testing.T) {
	const before = "00000000-0000-7000-8000-000000000001"
	const after = "00000000-0000-7000-8000-000000000002"
	const cursor = "opaque+/=& café"
	const page = `{"sourceId":"00000000-0000-7000-8000-000000000003","sourceRevision":7,"before":{"id":"00000000-0000-7000-8000-000000000001","memberCount":2,"startedAt":"2026-01-01T00:00:00Z","withdrawalAuthority":true},"after":{"id":"00000000-0000-7000-8000-000000000002","memberCount":1,"startedAt":"2026-01-02T00:00:00Z","withdrawalAuthority":false},"items":[{"position":1,"sourceKey":"a-missing-version","change":"absent","beforeCount":1,"afterCount":0,"before":{"observationId":"00000000-0000-7000-8000-000000000004","identifier":"urn:air:example.com:demo:agent","version":"","versionPresent":false,"kind":"agent","identityDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","occurrence":{"observationId":"00000000-0000-7000-8000-000000000004","sourceId":"00000000-0000-7000-8000-000000000003","sourceRevision":7,"snapshotId":"00000000-0000-7000-8000-000000000001","retrievalDigest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","entryLocation":"/entries/0","exactEntryDigest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","evidenceComplete":true,"observedAt":"2026-01-01T00:00:00Z"}}},{"position":2,"sourceKey":"b-empty-version","change":"added","beforeCount":0,"afterCount":1,"after":{"observationId":"00000000-0000-7000-8000-000000000005","identifier":"urn:air:example.com:demo:agent","version":"","versionPresent":true,"kind":"agent","identityDigest":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","occurrence":{"observationId":"00000000-0000-7000-8000-000000000005","sourceId":"00000000-0000-7000-8000-000000000003","sourceRevision":7,"snapshotId":"00000000-0000-7000-8000-000000000002","retrievalDigest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","entryLocation":"/entries/0","exactEntryDigest":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","evidenceComplete":true,"observedAt":"2026-01-02T00:00:00Z"}}}],"nextCursor":"opaque+/=& caf\u00e9"}`
	// One before member remains beyond the first two change rows.
	var terminal map[string]any
	if err := json.Unmarshal([]byte(page), &terminal); err != nil {
		t.Fatal(err)
	}
	items, ok := terminal["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatal("comparison fixture needs two initial rows")
	}
	last, ok := items[0].(map[string]any)
	if !ok {
		t.Fatal("comparison fixture row is not an object")
	}
	last["position"], last["sourceKey"] = 3, "c-other"
	member, ok := last["before"].(map[string]any)
	if !ok {
		t.Fatal("comparison fixture has no before member")
	}
	member["observationId"] = "00000000-0000-7000-8000-000000000006"
	member["identifier"] = "urn:air:example.com:demo:other"
	member["identityDigest"] = "sha256:" + strings.Repeat("e", 64)
	occurrence, ok := member["occurrence"].(map[string]any)
	if !ok {
		t.Fatal("comparison fixture has no occurrence")
	}
	occurrence["observationId"], occurrence["entryLocation"] = member["observationId"], "/entries/1"
	occurrence["exactEntryDigest"] = "sha256:" + strings.Repeat("f", 64)
	terminal["items"] = []any{last}
	delete(terminal, "nextCursor")
	terminalBody, err := json.Marshal(terminal)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		q := r.URL.Query()
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/federation/snapshots/"+before+"/comparison" || q.Get("otherSnapshotId") != after || q.Get("view") != "changes" || q.Get("limit") != "2" || r.Header.Get("Authorization") != "Bearer explicit" || r.Header.Get("Idempotency-Key") != "" {
			t.Error("incorrect selected comparison", r.URL)
		}
		if n == 1 {
			if q.Has("cursor") {
				t.Error("unexpected first cursor")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, page)
			return
		}
		if q.Get("cursor") != cursor {
			t.Error("opaque cursor changed")
		}
		if n == 2 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(terminalBody)
			return
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Set("X-Request-ID", "comparison-refusal")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"type":"about:blank","title":"Invalid argument","status":400,"code":"INVALID_ARGUMENT","detail":"Selected evidence unavailable","requestId":"comparison-refusal"}`)
	}))
	defer server.Close()
	c, err := New(Config{Origin: server.URL, AllowLoopbackHTTP: true, Token: "explicit"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	view, limit := CompareFederationSourceSnapshotsParamsViewChanges, 2
	var beforeID, afterID UUID
	if err := beforeID.UnmarshalText([]byte(before)); err != nil {
		t.Fatal(err)
	}
	if err := afterID.UnmarshalText([]byte(after)); err != nil {
		t.Fatal(err)
	}
	params := &CompareFederationSourceSnapshotsParams{OtherSnapshotId: afterID, View: &view, Limit: &limit}
	response, err := c.CompareFederationSourceSnapshotsWithResponse(context.Background(), beforeID, params)
	if err != nil || response.JSON200 == nil {
		t.Fatal("comparison failed", err)
	}
	data := response.JSON200
	if data.Before.Id.String() != before || data.After.Id.String() != after || data.SourceRevision != 7 || data.After.WithdrawalAuthority || len(data.Items) != 2 || data.NextCursor == nil || *data.NextCursor != cursor {
		t.Fatalf("page changed: %+v", data)
	}
	if data.Items[0].Before == nil || data.Items[0].After != nil || data.Items[0].Before.VersionPresent || data.Items[0].Before.Version != "" || data.Items[1].Before != nil || data.Items[1].After == nil || !data.Items[1].After.VersionPresent || data.Items[1].After.Version != "" {
		t.Fatal("one-sided members or version presence lost")
	}
	if calls.Load() != 1 {
		t.Fatal("SDK automatically continued")
	}
	params.Cursor = data.NextCursor
	completed, err := c.CompareFederationSourceSnapshotsWithResponse(context.Background(), beforeID, params)
	if err != nil || completed.JSON200 == nil {
		t.Fatal("normal continuation failed", err)
	}
	lastPage := completed.JSON200
	if lastPage.NextCursor != nil || len(lastPage.Items) != 1 || lastPage.Before.MemberCount != 2 || lastPage.Items[0].Position != 3 || lastPage.Items[0].SourceKey != "c-other" || lastPage.Items[0].Before == nil || lastPage.Items[0].Before.ObservationId.String() != "00000000-0000-7000-8000-000000000006" || calls.Load() != 2 {
		t.Fatalf("terminal membership or page boundary lost: %+v", lastPage)
	}
	// An explicit later reuse of the cursor can be refused; never restart it.
	refused, err := c.CompareFederationSourceSnapshotsWithResponse(context.Background(), beforeID, params)
	if err != nil || refused.StatusCode() != 400 || refused.ApplicationproblemJSONDefault == nil || refused.ApplicationproblemJSONDefault.Code != "INVALID_ARGUMENT" || refused.HTTPResponse.Header.Get("X-Request-ID") != "comparison-refusal" || calls.Load() != 3 {
		t.Fatal("refusal lost or retried", err)
	}
}
