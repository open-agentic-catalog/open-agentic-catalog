//go:build integration

package functional_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/draftfile"
)

// The existing owned process fixture supplies exact runtime logins, migrations
// and two API replicas. Each uncertain command resumes through the other API.
func qualifyProcessDraftImport(ctx context.Context, t *testing.T, f processFixture, owner *pgx.Conn, address, peer, tenant, namespace, publisher string) {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{}, Timeout: 10 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	var mu sync.Mutex
	drop, calls, replays := true, 0, 0
	dropSuffix := "/versions"
	type sent struct {
		path, etag string
		body       []byte
	}
	seen := map[string]sent{}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		upstream := address
		if !drop {
			upstream = peer
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, (2<<20)+1))
		if err != nil || len(body) > 2<<20 {
			t.Error("invalid owned proxy request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		forward, err := http.NewRequestWithContext(r.Context(), r.Method, "http://"+upstream+r.URL.RequestURI(), bytes.NewReader(body)) // #nosec G704 -- fixed owned loopback API origins, no destination from documents.
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		forward.Header = r.Header.Clone()
		if r.Method == http.MethodPost {
			calls++
			key := r.Header.Get("Idempotency-Key")
			if previous, ok := seen[key]; ok {
				replays++
				if previous.path != r.URL.Path || previous.etag != r.Header.Get("If-Match") || !bytes.Equal(previous.body, body) {
					t.Error("cross-replica replay changed command bytes or ETag")
				}
			} else {
				seen[key] = sent{r.URL.Path, r.Header.Get("If-Match"), body}
			}
		}
		response, err := client.Do(forward) // #nosec G704 -- fixed local process fixture; redirects refused.
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer func() { _ = response.Body.Close() }()
		raw, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		for key, values := range response.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(response.StatusCode)
		if drop && r.Method == http.MethodPost && response.StatusCode == http.StatusCreated && strings.HasSuffix(r.URL.Path, dropSuffix) {
			drop = false
			_, _ = io.WriteString(w, `{"interrupted":`)
			return
		}
		_, _ = w.Write(raw)
	}))
	defer proxy.Close()
	type step struct {
		State string
		Ack   struct{ ID, RevisionID string }
	}
	type outcome struct {
		Disposition string
		Items       []struct {
			Disposition string
			Steps       []step
		}
	}
	counts := func() []int {
		t.Helper()
		result := make([]int, 8)
		if err := owner.QueryRow(ctx, `SELECT (SELECT count(*) FROM resources),(SELECT count(*) FROM resource_versions),(SELECT count(*) FROM catalog_listings),(SELECT count(*) FROM listing_revisions),(SELECT count(*) FROM audit_events WHERE action IN ('resource.created','resource_version.created','catalog_listing.created','catalog_listing.revised')),(SELECT count(*) FROM outbox_events),(SELECT count(*) FROM review_submissions)+(SELECT count(*) FROM listing_releases)+(SELECT count(*) FROM publication_snapshots),(SELECT count(*) FROM audit_events WHERE action='evidence.version_revision.read')`).Scan(&result[0], &result[1], &result[2], &result[3], &result[4], &result[5], &result[6], &result[7]); err != nil {
			t.Fatal(err)
		}
		return result
	}
	before := counts()
	resourceID, listingID := "", ""
	for index, mode := range []string{"create-content", "version-content", "version-new-listing", "version-exact-listing"} {
		text := `{"name":"Reusable process import","framework":"custom","unknown":{"integer":9007199254740993,"decimal":0.123456789012345678901}}`
		file := draftfile.File{Format: draftfile.Format, Kind: catalog.KindAgent, CanonicalIdentifier: "custom:reusable-process-import", Version: []string{"one", "two", "three", "four"}[index], Representations: []draftfile.Representation{{Role: catalog.RepresentationPrimary, MediaType: "application/vnd.open-agentic-catalog.agent+json", ProfileID: adapter.ProfilePrivateAgent, Input: draftfile.Input{Mode: "data", Text: &text}}}}
		if index >= 2 {
			file.Listing = &draftfile.Listing{DisplayName: "Reusable process import", Description: "Created by the compiled CLI.", Visibility: catalog.VisibilityPrivate}
		}
		raw, err := draftfile.Encode(file)
		if err != nil {
			t.Fatal(err)
		}
		directory := t.TempDir()
		filename, state := filepath.Join(directory, "draft.json"), filepath.Join(directory, "private", "state.json")
		if err = os.WriteFile(filename, raw, 0600); err != nil {
			t.Fatal(err)
		}
		args := []string{"--file", filename, "--state-file", state, "--base-url", proxy.URL}
		if index == 0 {
			args = append(args, "--namespace-id", namespace)
		} else {
			args = append(args, "--resource-id", resourceID)
		}
		if index >= 2 {
			args = append(args, "--publisher-id", publisher)
		}
		if index == 3 {
			args = append(args, "--listing-id", listingID)
		}
		run := func(action string, exit int) outcome {
			t.Helper()
			command := f.command(ctx, "catalogctl", "", append([]string{"import", action}, args...)...)
			command.Env = append(command.Env, "CATALOG_REMOTE_TOKEN=local-contributor")
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			err := command.Run()
			f.counters(command)
			if command.ProcessState == nil || command.ProcessState.ExitCode() != exit || exit == 0 && stderr.Len() != 0 || exit != 0 && (stderr.Len() == 0 || strings.Count(stderr.String(), "\n") != 1) {
				t.Fatalf("reusable import %s at %s: %v stdout=%s stderr=%s", action, time.Now().UTC().Format(time.RFC3339Nano), err, &stdout, &stderr)
			}
			if exit != 0 {
				t.Logf("%s %s: %s", mode, action, &stderr)
			}
			var result outcome
			decodeProcessJSON(t, stdout.Bytes(), &result)
			return result
		}
		mu.Lock()
		drop = true
		dropSuffix = "/versions"
		if index >= 2 {
			dropSuffix = "/revisions"
		}
		mu.Unlock()
		interrupted := run("apply", 1)
		if interrupted.Disposition != "uncertain" {
			t.Fatalf("lost committed response was not uncertain in %s: %+v", mode, interrupted)
		}
		complete := run("resume", 0)
		wantSteps := []int{2, 1, 3, 2}[index]
		if complete.Disposition != "complete" || len(complete.Items) != 1 || len(complete.Items[0].Steps) != wantSteps {
			t.Fatal("finite plan did not complete", mode)
		}
		if index == 0 {
			resourceID = complete.Items[0].Steps[0].Ack.ID
		}
		if index == 2 {
			listingID = complete.Items[0].Steps[1].Ack.ID
		}
		versionStep := 0
		if index == 0 {
			versionStep = 1
		}
		var numericOK bool
		if err = owner.QueryRow(ctx, `SELECT inline_document->'unknown'->'integer'='9007199254740993'::jsonb AND inline_document->'unknown'->'decimal'='0.123456789012345678901'::jsonb FROM representations WHERE tenant_id=$1 AND version_revision_id=$2`, tenant, complete.Items[0].Steps[versionStep].Ack.RevisionID).Scan(&numericOK); err != nil || !numericOK {
			t.Fatal("shared import changed PostgreSQL native numbers", err)
		}
		mu.Lock()
		prior := calls
		mu.Unlock()
		if run("resume", 0).Disposition != "complete" {
			t.Fatal("completed reusable resume failed")
		}
		mu.Lock()
		changed := calls != prior
		mu.Unlock()
		if changed {
			t.Fatal("completed shared import repeated a mutation")
		}
	}
	after := counts()
	for index, want := range []int{1, 4, 1, 2, 8, 8, 0} {
		if after[index]-before[index] != want {
			t.Fatalf("reusable import effect %d: delta=%d want=%d", index, after[index]-before[index], want)
		}
	}
	if after[7] <= before[7] {
		t.Fatal("shared import lacks required exact revision read audits")
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 12 || replays != 4 || len(seen) != 8 {
		t.Fatal("cross-replica replay created duplicate commands")
	}
}
