//go:build integration

package functional_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
)

type tenantSyncResults struct {
	token   string
	source  federation.Source
	entries []string // Expected names in ascending order, without the common "Ledger " prefix.
	jobIDs  []string // Independently observed completed sync Jobs, newest first.
}

func ledgerIdentifier(name string) string {
	return "urn:air:fixture.example:private:" + strings.ToLower(name)
}

func (tenant tenantSyncResults) requireAbsent(t *testing.T, raw []byte) {
	t.Helper()
	check := func(values ...string) {
		t.Helper()
		for _, value := range values {
			if bytes.Contains(raw, []byte(value)) {
				t.Fatalf("response disclosed foreign value %q", value)
			}
		}
	}
	check(tenant.source.ID, tenant.source.TenantID, tenant.source.Endpoint)
	check(tenant.jobIDs...)
	for _, entry := range tenant.entries {
		identifier := ledgerIdentifier(entry)
		check("Ledger "+entry, identifier, catalog.IndexedSearchResultID(identifier, "", false))
	}
}

func requireRejectedTenantCursor(t *testing.T, raw []byte, owner tenantSyncResults) {
	t.Helper()
	owner.requireAbsent(t, raw)
	var problem struct {
		Code string `json:"code"`
	}
	decodeProcessJSON(t, raw, &problem)
	if problem.Code != "INVALID_ARGUMENT" {
		t.Fatalf("foreign cursor returned %q", problem.Code)
	}
}

func qualifyTenantSearchPages(ctx context.Context, f processFixture, address string, own, foreign tenantSyncResults) {
	t := f.t
	t.Helper()
	search := func(token, text, cursor string, status int) []byte {
		t.Helper()
		body := fmt.Sprintf(`{"text":%q,"order":"name_asc","limit":1,"cursor":%q}`, text, cursor)
		return f.request(ctx, address, http.MethodPost, "/api/v1/catalog/search", token, body, status)
	}
	cursor := ""
	for i, entry := range own.entries {
		raw := search(own.token, "Ledger", cursor, 200)
		foreign.requireAbsent(t, raw)
		var page catalog.CatalogSearchResponse
		decodeProcessJSON(t, raw, &page)
		more := i+1 < len(own.entries)
		if len(page.Items) != 1 || page.DurableTotal != len(own.entries) || page.LiveCount != 0 || page.Partial || (page.NextCursor != "") != more {
			t.Fatalf("%s search page %d: items=%d total=%d live=%d partial=%t more=%t", own.source.DisplayName, i+1, len(page.Items), page.DurableTotal, page.LiveCount, page.Partial, page.NextCursor != "")
		}
		item := page.Items[0]
		identifier := ledgerIdentifier(entry)
		if item.Type != catalog.SearchResultIndexed || item.DisplayName != "Ledger "+entry || item.Identifier != identifier || item.ID != catalog.IndexedSearchResultID(identifier, "", false) {
			t.Fatalf("expected %s, got %s (%s)", identifier, item.Identifier, item.DisplayName)
		}
		evidence := item.Indexed
		if evidence == nil || evidence.PresentationSourceID != own.source.ID || evidence.SourceCount != 1 || !slices.Equal(evidence.SourceIDs, []string{own.source.ID}) {
			t.Fatalf("incorrect source evidence for %s: %+v", identifier, evidence)
		}
		cursor = page.NextCursor
		if more {
			requireRejectedTenantCursor(t, search(foreign.token, "Ledger", cursor, 400), own)
		}
	}
	identifier := ledgerIdentifier(own.entries[0])
	for _, lookup := range []struct {
		token string
		want  int
	}{{own.token, 1}, {foreign.token, 0}} {
		raw := search(lookup.token, identifier, "", 200)
		var page catalog.CatalogSearchResponse
		decodeProcessJSON(t, raw, &page)
		if page.Items == nil || len(page.Items) != lookup.want || page.DurableTotal != lookup.want || page.LiveCount != 0 || page.NextCursor != "" || page.Partial {
			t.Fatalf("exact identifier search returned %d items and total %d, want %d", len(page.Items), page.DurableTotal, lookup.want)
		}
		if lookup.want == 0 {
			own.requireAbsent(t, raw)
		} else if page.Items[0].Identifier != identifier {
			t.Fatalf("exact search returned %s, want %s", page.Items[0].Identifier, identifier)
		}
	}
}

func qualifyTenantJobPages(ctx context.Context, f processFixture, address string, own, foreign tenantSyncResults) {
	t := f.t
	t.Helper()
	const base = "/api/v1/jobs?kind=source.sync&state=succeeded&limit=1"
	path := base
	for i, id := range own.jobIDs {
		raw := f.request(ctx, address, http.MethodGet, path, own.token, "", 200)
		foreign.requireAbsent(t, raw)
		var page struct {
			Items      []jobs.View `json:"items"`
			NextCursor string      `json:"nextCursor"`
		}
		decodeProcessJSON(t, raw, &page)
		more := i+1 < len(own.jobIDs)
		if len(page.Items) != 1 || (page.NextCursor != "") != more {
			t.Fatalf("%s job page %d: items=%d more=%t", own.source.DisplayName, i+1, len(page.Items), page.NextCursor != "")
		}
		job := page.Items[0]
		if job.ID != id || job.TenantID != own.source.TenantID || job.Kind != "source.sync" || job.State != jobs.StateSucceeded || job.Result == nil || job.Result.SourceID != own.source.ID || job.Result.State != "complete" {
			t.Fatalf("job page did not return %s with its own completed snapshot", id)
		}
		raw = f.request(ctx, address, http.MethodGet, "/api/v1/jobs/"+id, own.token, "", 200)
		foreign.requireAbsent(t, raw)
		var detail jobs.View
		decodeProcessJSON(t, raw, &detail)
		if !reflect.DeepEqual(detail, job) {
			t.Fatalf("completed job %s differed between list and detail", id)
		}
		raw = f.request(ctx, address, http.MethodGet, "/api/v1/federation/snapshots/"+job.Result.SnapshotID, own.token, "", 200)
		foreign.requireAbsent(t, raw)
		var snapshot federation.Snapshot
		decodeProcessJSON(t, raw, &snapshot)
		if snapshot.ID != job.Result.SnapshotID || snapshot.SourceID != own.source.ID || snapshot.TenantID != own.source.TenantID || snapshot.MemberCount != len(own.entries) {
			t.Fatalf("incorrect snapshot or member count for job %s", id)
		}
		if i == 0 {
			requireMissingTenantRecord(ctx, f, address, foreign.token, "/api/v1/jobs/", id)
			requireMissingTenantRecord(ctx, f, address, foreign.token, "/api/v1/federation/snapshots/", snapshot.ID)
		}
		if more {
			path = base + "&cursor=" + url.QueryEscape(page.NextCursor)
			requireRejectedTenantCursor(t, f.request(ctx, address, http.MethodGet, path, foreign.token, "", 400), own)
		}
	}
}

// Compare the complete refusal with an absent record, allowing for the two request-specific fields.
func requireMissingTenantRecord(ctx context.Context, f processFixture, address, token, collection, foreignID string) {
	t := f.t
	t.Helper()
	problem := func(id string) map[string]any {
		t.Helper()
		path := collection + id
		raw := f.request(ctx, address, http.MethodGet, path, token, "", 404)
		var body map[string]any
		decodeProcessJSON(t, raw, &body)
		requestID, _ := body["requestId"].(string)
		if body["instance"] != path || body["code"] != "NOT_FOUND" || requestID == "" {
			t.Fatal("unexpected missing record response", body)
		}
		delete(body, "requestId")
		delete(body, "instance")
		return body
	}
	if !reflect.DeepEqual(problem(foreignID), problem("00000000-0000-7000-8000-ffffffffffff")) {
		t.Fatalf("foreign and missing records were distinguishable in %s", collection)
	}
}
