//go:build integration

package functional_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
)

// Exercise both directions before the settings fixture starts its workers.
func qualifyPrivateSourceIsolation(ctx context.Context, t *testing.T, f processFixture, cfg config.Config, peer, sharedName string, first, second *federation.Source) {
	t.Helper()
	f.t = t
	const sources = "/api/v1/federation/sources"
	tenants := []struct {
		owner, other, newName string
		source                *federation.Source
	}{
		{"local-admin", "other-tenant-admin", "Primary directory", first},
		{"other-tenant-admin", "local-admin", "Secondary directory", second},
	}
	if first.ID == second.ID || first.TenantID == second.TenantID || first.Endpoint == second.Endpoint {
		t.Fatal("isolation fixture requires distinct source IDs, tenants and endpoints")
	}
	requireRecord := func(operation string, got federation.Source, original *federation.Source, name string, revision int64) {
		t.Helper()
		if got.ID != original.ID || got.TenantID != original.TenantID || got.Endpoint != original.Endpoint || got.DisplayName != name || got.Revision != revision {
			t.Fatalf("%s: got source %q tenant %q name %q revision %d, want source %q tenant %q name %q revision %d with its original endpoint",
				operation, got.ID, got.TenantID, got.DisplayName, got.Revision, original.ID, original.TenantID, name, revision)
		}
	}
	inventory := func(token, name string, limit int) []federation.Source {
		t.Helper()
		path := fmt.Sprintf("%s?limit=%d", sources, limit)
		if name != "" {
			path += "&displayName=" + url.QueryEscape(name)
		}
		var page struct {
			Items      []federation.Source `json:"items"`
			NextCursor string              `json:"nextCursor"`
		}
		raw := f.request(ctx, peer, http.MethodGet, path, token, "", 200)
		for _, tenant := range tenants {
			if tenant.owner == token {
				continue
			}
			for _, private := range []string{tenant.source.ID, tenant.source.TenantID, tenant.source.Endpoint, tenant.newName} {
				if bytes.Contains(raw, []byte(private)) {
					t.Fatalf("source inventory disclosed foreign value %q", private)
				}
			}
		}
		decodeProcessJSON(t, raw, &page)
		if page.Items == nil || page.NextCursor != "" {
			t.Fatal("expected a terminal source inventory page")
		}
		return page.Items
	}
	for _, tenant := range tenants {
		want := tenant.source
		if want.ID == "" || want.Endpoint == "" || want.TenantID == "" || want.TenantID != cfg.Auth.DevelopmentTokens[tenant.owner].TenantID || want.DisplayName != sharedName || want.Revision != 1 {
			t.Fatalf("%s did not create the expected private source", tenant.newName)
		}
		items := inventory(tenant.owner, sharedName, 2)
		if len(items) != 1 {
			t.Fatalf("%s inventory returned %d sources", tenant.newName, len(items))
		}
		requireRecord("filtered inventory", items[0], want, sharedName, want.Revision)
	}
	for _, tenant := range tenants {
		source := tenant.source
		path := sources + "/" + source.ID
		requireHidden := func(method string, raw []byte) {
			t.Helper()
			var problem struct {
				Code string `json:"code"`
			}
			decodeProcessJSON(t, raw, &problem)
			if problem.Code != "NOT_FOUND" || bytes.Contains(raw, []byte(source.TenantID)) || bytes.Contains(raw, []byte(source.Endpoint)) || bytes.Contains(raw, []byte(sharedName)) {
				t.Fatalf("%s revealed private source details or gave the wrong problem code for %s", method, tenant.newName)
			}
		}
		requireHidden("GET", f.request(ctx, peer, http.MethodGet, path, tenant.other, "", 404))

		var preview federation.ChangePreview
		proposal := fmt.Sprintf(`{"intent":"update","displayName":%q}`, tenant.newName)
		decodeProcessJSON(t, f.request(ctx, peer, http.MethodPost, path+"/change-previews", tenant.owner, proposal, 200, map[string]string{"Idempotency-Key": "preview-" + source.ID}), &preview)
		if preview.Digest == "" || preview.SourceID != source.ID || preview.SourceRevision != source.Revision {
			t.Fatalf("rename preview did not bind the current %s source", tenant.newName)
		}
		// Give the other administrator the exact body and revision the owner can use.
		body := fmt.Sprintf(`{"displayName":%q,"changePreviewDigest":%q,"reason":"Rename our private directory"}`, tenant.newName, preview.Digest)
		revision := fmt.Sprintf(`"rev-%d"`, source.Revision)
		requireHidden("PATCH", f.request(ctx, cfg.HTTP.Address, http.MethodPatch, path, tenant.other, body, 404, map[string]string{"If-Match": revision, "Idempotency-Key": "foreign-" + source.ID}))

		var stored federation.Source
		decodeProcessJSON(t, f.request(ctx, peer, http.MethodGet, path, tenant.owner, "", 200), &stored)
		requireRecord("owner read after refused update", stored, source, sharedName, source.Revision)
		var updated federation.Source
		decodeProcessJSON(t, f.request(ctx, cfg.HTTP.Address, http.MethodPatch, path, tenant.owner, body, 200, map[string]string{"If-Match": revision, "Idempotency-Key": "owner-" + source.ID}), &updated)
		requireRecord("owner update", updated, source, tenant.newName, source.Revision+1)
	}
	// Read both after both renames, using a different API process from the writer.
	for _, tenant := range tenants {
		want := tenant.source
		var stored federation.Source
		decodeProcessJSON(t, f.request(ctx, peer, http.MethodGet, sources+"/"+want.ID, tenant.owner, "", 200), &stored)
		requireRecord("independent read after both updates", stored, want, tenant.newName, want.Revision+1)
		items := inventory(tenant.owner, "", 1)
		if len(items) != 1 || len(inventory(tenant.other, tenant.newName, 1)) != 0 {
			t.Fatalf("incorrect inventory after renaming %s", tenant.newName)
		}
		requireRecord("unfiltered inventory", items[0], want, tenant.newName, want.Revision+1)
		*tenant.source = stored
	}
}
