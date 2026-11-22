//go:build integration

package functional_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/personal"
)

// Drop the successful duplicate-follow response before the CLI can record it.
// Keep the CLI origin and receipt fixed while the actual API process is replaced.
func qualifyProcessPersonalRestore(ctx context.Context, t *testing.T, f processFixture, owner *pgx.Conn, address string, release catalog.ListingRelease, restartAPI func()) {
	t.Helper()
	const items = "/api/v1/personal/items"
	const contributor = "local-contributor"
	var follow personal.Item
	create := fmt.Sprintf(`{"type":"follow","listingId":%q,"label":"Exported label"}`, release.ListingID)
	decodeProcessJSON(t, f.request(ctx, address, "POST", items, contributor, create, 201), &follow)
	if follow.ID == "" || follow.Revision != 1 {
		t.Fatal("missing initial follow")
	}

	var saves atomic.Int32
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	proxy := httptest.NewServer(&httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(&url.URL{Scheme: "http", Host: address})
		},
		Transport: transport,
		ModifyResponse: func(r *http.Response) error {
			if r.Request.Method != http.MethodPost || r.Request.URL.Path != items {
				return nil
			}
			attempt := saves.Add(1)
			if r.StatusCode != http.StatusOK || (r.Header.Get("Idempotency-Replayed") == "true") != (attempt == 2) {
				t.Errorf("follow save %d: status %d, replay header %q", attempt, r.StatusCode, r.Header.Get("Idempotency-Replayed"))
			}
			if attempt == 1 {
				_ = r.Body.Close()
				panic(http.ErrAbortHandler)
			}
			return nil
		},
	})
	defer proxy.Close()
	cli := func(args ...string) *exec.Cmd {
		cmd := f.command(ctx, "catalogctl", "", append([]string{"personal"}, args...)...)
		cmd.Env = append(cmd.Env, "CATALOG_REMOTE_BASE_URL="+proxy.URL, "CATALOG_REMOTE_TOKEN="+contributor)
		return cmd
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(root, "backup")
	var exported struct {
		File  string
		Items int
	}
	decodeProcessJSON(t, f.run(ctx, cli("export", "--output-dir", backup), 0), &exported)
	if exported.File != "library.json" || exported.Items != 1 {
		t.Fatalf("unexpected personal export: %+v", exported)
	}
	const label = "Owner's current label"
	update := fmt.Sprintf(`{"type":"follow","listingId":%q,"label":%q,"observed":{"releaseId":%q,"revision":%d}}`, release.ListingID, label, release.ID, release.Revision)
	f.request(ctx, address, "PATCH", items+"/"+follow.ID, contributor, update, 200, map[string]string{"If-Match": `"rev-1"`})

	restore := func(action string) *exec.Cmd {
		return cli(action, "--file", filepath.Join(backup, exported.File), "--state-file", filepath.Join(root, "progress", "state.json"), "--all", "--confirm")
	}
	apply := restore("apply")
	stdout, err := apply.Output()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || bytes.Count(exit.Stderr, []byte("\n")) != 1 || !bytes.Contains(exit.Stderr, []byte("save was not acknowledged")) {
		t.Fatalf("personal apply did not exit 1 with a lost-response diagnostic: %v", err)
	}
	f.counters(apply)
	type report struct {
		Status string
		Items  []struct {
			Index     int
			ID, State string
		}
	}
	var interrupted report
	decodeProcessJSON(t, stdout, &interrupted)
	if interrupted.Status != "incomplete" || len(interrupted.Items) != 1 || interrupted.Items[0].Index != 1 || interrupted.Items[0].State != "uncertain" || interrupted.Items[0].ID != "" || saves.Load() != 1 {
		t.Fatalf("unacknowledged apply: %+v, saves=%d", interrupted, saves.Load())
	}
	restartAPI()
	var completed report
	decodeProcessJSON(t, f.run(ctx, restore("resume"), 0), &completed)
	if completed.Status != "complete" || len(completed.Items) != 1 || completed.Items[0].Index != 1 || completed.Items[0].State != "already-followed" || completed.Items[0].ID != follow.ID || saves.Load() != 2 {
		t.Fatalf("resumed duplicate follow: %+v, saves=%d", completed, saves.Load())
	}

	var follows, matching, creations int
	err = owner.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE id::text=$3 AND revision=2 AND input->>'label'=$4
			AND input->'acknowledged'=jsonb_build_object('releaseId',$5::text,'revision',$6::bigint)),
			(SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND target_type='personal_item'
				AND target_id=$3 AND action='personal.item.created')
		FROM personal_items WHERE tenant_id=$1 AND input->>'type'='follow' AND input->>'listingId'=$2`,
		f.tenantID, release.ListingID, follow.ID, label, release.ID, release.Revision).Scan(&follows, &matching, &creations)
	if err != nil || follows != 1 || matching != 1 || creations != 1 {
		t.Fatalf("retained follow after resume: follows=%d exact matches=%d creation audits=%d: %v", follows, matching, creations, err)
	}
	t.Log("personal restore: unacknowledged follow replayed after API replacement; PostgreSQL retained the owner's label, acknowledgement and single creation")
}
