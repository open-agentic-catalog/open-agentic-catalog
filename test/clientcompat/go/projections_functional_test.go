//go:build functional

package clientcompat_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	client "github.com/open-agentic-catalog/open-agentic-catalog/test/clientcompat/go/generated"
)

func qualifyBrowse(t *testing.T, f *fixture, namespace uuid.UUID, ids []uuid.UUID, card string) {
	t.Helper()
	ctx := context.Background()
	c := f.client("reader")
	params := &client.BrowseCatalogParams{Limit: ptr(1), NamespaceId: &namespace, Kind: ptr(client.ResourceKind("agent")), Visibility: ptr(client.Visibility("tenant"))}
	var firstCursor string
	seen := map[uuid.UUID]bool{}
	for i := range 2 {
		r, err := c.BrowseCatalog(ctx, params)
		data := f.result(r, err, 200)
		page := decode[client.CatalogRecordPage](t, data)
		if len(page.Items) != 1 || page.Total != 2 {
			t.Fatalf("unexpected Browse page: %+v", page)
		}
		item := page.Items[0]
		if item.Release.Id != ids[i] || seen[item.Release.Id] || item.Source != "local" || item.Resource.Kind != "agent" || item.Resource.NamespaceId != namespace || item.Resource.TenantId.String() != tenant || item.Version.Version != "1.0.0" || item.ListingRevision.DisplayName != fmt.Sprintf("Listing %d", i) || item.ListingRevision.Description != "Compatibility" || !reflect.DeepEqual(item.ListingRevision.Tags, []string{"compat"}) || item.ListingRevision.Visibility != "tenant" || item.Evidence.State != "complete" || item.Release.State != "published" || item.Display == nil || item.Display.PublisherName == nil || *item.Display.PublisherName != "Publisher" {
			t.Fatalf("incorrect compact Browse metadata: %+v", item)
		}
		seen[item.Release.Id] = true
		raw := decode[struct {
			Items []json.RawMessage `json:"items"`
		}](t, data)
		assertCompactBrowse(t, raw.Items[0], ids[i])
		if i == 0 {
			if page.NextCursor == nil || *page.NextCursor == "" {
				t.Fatal("missing Browse continuation")
			}
			firstCursor = *page.NextCursor
		} else if page.NextCursor != nil {
			t.Fatal("Browse continuation after final page")
		}
		params.Cursor = page.NextCursor
		r, err = c.GetCatalogRecord(ctx, item.Release.Id)
		exact := f.result(r, err, 200)
		record := decode[client.CatalogRecord](t, exact)
		if record.Release.Id != ids[i] || record.VersionRevision.ContentSetDigest != item.VersionRevision.ContentSetDigest || record.Evidence.Digest != item.Evidence.Digest || !bytes.Contains(exact, []byte("listing-detail-sentinel")) {
			t.Fatal("exact record lost retained metadata or listing detail")
		}
		var evidence struct {
			VersionRevision struct {
				Representations []struct {
					Data json.RawMessage `json:"data"`
				} `json:"representations"`
			} `json:"versionRevision"`
		}
		must(t, json.Unmarshal(exact, &evidence))
		if len(evidence.VersionRevision.Representations) != 1 || !reflect.DeepEqual(exactJSON(t, evidence.VersionRevision.Representations[0].Data), exactJSON(t, []byte(card))) {
			t.Fatal("exact record lost retained representation")
		}
	}
	r, err := f.client("other").BrowseCatalog(ctx, &client.BrowseCatalogParams{Limit: ptr(1)})
	emptyData := f.result(r, err, 200)
	empty := decode[client.CatalogRecordPage](t, emptyData)
	if len(empty.Items) != 0 || empty.Total != 0 || empty.NextCursor != nil || !bytes.Equal([]byte("[]"), decode[map[string]json.RawMessage](t, emptyData)["items"]) {
		t.Fatal("foreign tenant Browse not empty")
	}
	for _, tc := range []struct {
		name, token, cursor string
		kind                client.ResourceKind
	}{
		{"malformed", "reader", "malformed", "agent"}, {"foreign", "other", firstCursor, "agent"}, {"filter", "reader", firstCursor, "skill"},
	} {
		t.Run("browse-cursor-"+tc.name, func(t *testing.T) {
			r, err := f.client(tc.token).BrowseCatalog(ctx, &client.BrowseCatalogParams{Limit: ptr(1), NamespaceId: &namespace, Kind: &tc.kind, Visibility: ptr(client.Visibility("tenant")), Cursor: &tc.cursor})
			assertInvalidProblem(t, f.result(r, err, 400))
		})
	}
	qualifyJobs(t, f, firstCursor)
}

func assertCompactBrowse(t *testing.T, data []byte, id uuid.UUID) {
	t.Helper()
	raw := decode[map[string]json.RawMessage](t, data)
	if decode[map[string]string](t, raw["release"])["id"] != id.String() {
		t.Fatal("wrong raw Browse release")
	}
	for _, marker := range []string{"listing-detail-sentinel", "9007199254740993", "supportedInterfaces"} {
		if bytes.Contains(data, []byte(marker)) {
			t.Fatalf("Browse leaked %s", marker)
		}
	}
	for field, allowed := range map[string][]string{
		"versionRevision": {"id", "contentSetDigest", "evidenceSetId"},
		"listingRevision": {"id", "displayName", "description", "tags", "publisherId", "visibility"},
		"evidence":        {"id", "digest", "state", "trust", "legalRights", "expiresAt"},
	} {
		object := decode[map[string]json.RawMessage](t, raw[field])
		for key := range object {
			found := false
			for _, candidate := range allowed {
				if key == candidate {
					found = true
				}
			}
			if !found {
				t.Fatalf("Browse %s contains detail key %s", field, key)
			}
		}
	}
}

func qualifyJobs(t *testing.T, f *fixture, browseCursor string) {
	t.Helper()
	ctx := context.Background()
	administrator := f.client("admin")
	ids := []uuid.UUID{uuid.MustParse("00000000-0000-7000-8000-000000000094"), uuid.MustParse("00000000-0000-7000-8000-000000000093"), uuid.MustParse("00000000-0000-7000-8000-000000000095")}
	snapshot := "00000000-0000-7000-8000-000000000096"
	for i, id := range ids {
		at := f.now
		if i == 2 {
			at = at.Add(-time.Second)
		}
		expires := at.Add(time.Minute)
		job := jobs.Job{ID: id.String(), TenantID: tenant, Kind: "publication.generate", Actor: "private-actor-sentinel", PayloadVersion: 1, Payload: json.RawMessage(`{"credentialBinding":"private-credential-sentinel"}`), State: jobs.StateSucceeded, Progress: 100, Attempt: 1, AttemptLimit: 3, NotBefore: at, CreatedAt: at, UpdatedAt: at, DeduplicationKey: "private-dedup-sentinel", Result: json.RawMessage(`{"id":"` + snapshot + `","host":"catalog.example.com","generation":7,"state":"candidate","document":{"private":"private-snapshot-sentinel"}}`)}
		if i == 2 {
			job.State, job.Progress, job.Result = jobs.StateRunning, 40, nil
			job.LeaseOwner, job.LeaseExpiresAt = "private-lease-sentinel", &expires
		}
		_, _, err := f.jobStore.Enqueue(ctx, job)
		must(t, err)
		retained, err := f.jobStore.Get(ctx, tenant, id.String())
		must(t, err)
		if !bytes.Contains(retained.Payload, []byte("private-credential-sentinel")) || retained.Actor != "private-actor-sentinel" || retained.DeduplicationKey != "private-dedup-sentinel" {
			t.Fatal("private Job controls not retained")
		}
		if i == 2 {
			if retained.State != jobs.StateRunning || retained.LeaseOwner != "private-lease-sentinel" || retained.LeaseExpiresAt == nil || len(retained.Result) != 0 {
				t.Fatal("running Job lease/absent-result controls not retained")
			}
		} else if retained.State != jobs.StateSucceeded || !bytes.Contains(retained.Result, []byte("private-snapshot-sentinel")) || retained.LeaseOwner != "" || retained.LeaseExpiresAt != nil {
			t.Fatal("completed Job result/cleared-lease controls not retained")
		}
	}
	foreignID := uuid.MustParse("00000000-0000-7000-8000-000000000097")
	_, _, err := f.jobStore.Enqueue(ctx, jobs.Job{ID: foreignID.String(), TenantID: otherTenant, Kind: "publication.generate", State: jobs.StateQueued, AttemptLimit: 3, NotBefore: f.now, CreatedAt: f.now, UpdatedAt: f.now})
	must(t, err)
	denied, deniedErr := f.client("reader").ListJobs(ctx, nil)
	readerPage := decode[client.JobPage](t, f.result(denied, deniedErr, 200))
	if len(readerPage.Items) != 0 || readerPage.NextCursor != nil && *readerPage.NextCursor != "" {
		t.Fatal("Reader received unauthorized family results", readerPage)
	}
	denied, deniedErr = f.client("reader").GetJob(ctx, ids[0])
	if decode[client.Problem](t, f.result(denied, deniedErr, 403)).Code != "FORBIDDEN" {
		t.Fatal("Reader received a private exact Job")
	}
	var cursor *string
	var firstCursor string
	for i, id := range ids {
		r, err := administrator.ListJobs(ctx, &client.ListJobsParams{Limit: ptr(1), Cursor: cursor})
		data := f.result(r, err, 200)
		page := decode[client.JobPage](t, data)
		if len(page.Items) != 1 || page.Items[0].Id != id {
			t.Fatalf("Job ordering/isolation changed: %+v", page)
		}
		at := f.now
		if i == 2 {
			at = at.Add(-time.Second)
		}
		state, progress := "succeeded", 100
		resultJSON := `,"result":{"type":"publication_snapshot","snapshotId":"` + snapshot + `","host":"catalog.example.com","generation":7,"state":"candidate"}`
		if i == 2 {
			state, progress, resultJSON = "running", 40, ""
		}
		want := []byte(fmt.Sprintf(`{"id":%q,"tenantId":%q,"kind":"publication.generate","state":%q,"priority":0,"progress":%d,"notBefore":%q,"attempt":1,"attemptLimit":3,"cancellationRequested":false,"createdAt":%q,"updatedAt":%q%s}`, id, tenant, state, progress, at.Format(time.RFC3339Nano), at.Format(time.RFC3339Nano), at.Format(time.RFC3339Nano), resultJSON))
		raw := decode[struct {
			Items []json.RawMessage `json:"items"`
		}](t, data)
		if !reflect.DeepEqual(exactJSON(t, raw.Items[0]), exactJSON(t, want)) {
			t.Fatalf("raw Job List projection changed: %s", raw.Items[0])
		}
		assertTypedJob(t, page.Items[0], id, at, uuid.MustParse(snapshot), i < 2)
		r, err = administrator.GetJob(ctx, id)
		data = f.result(r, err, 200)
		if !reflect.DeepEqual(exactJSON(t, data), exactJSON(t, want)) {
			t.Fatalf("raw Job Get projection changed: %s", data)
		}
		assertTypedJob(t, decode[client.Job](t, data), id, at, uuid.MustParse(snapshot), i < 2)
		if i < 2 {
			if page.NextCursor == nil || *page.NextCursor == "" {
				t.Fatal("missing Job continuation")
			}
		} else if page.NextCursor != nil {
			t.Fatal("Job continuation after final page")
		}
		if i == 0 {
			firstCursor = *page.NextCursor
		}
		cursor = page.NextCursor
	}
	r, err := administrator.GetJob(ctx, foreignID)
	if decode[client.Problem](t, f.result(r, err, 404)).Code != "NOT_FOUND" {
		t.Fatal("foreign Job exposed")
	}
	r, err = f.client("other").GetJob(ctx, ids[0])
	if decode[client.Problem](t, f.result(r, err, 404)).Code != "NOT_FOUND" {
		t.Fatal("local Job exposed to foreign tenant")
	}
	r, err = administrator.ListJobs(ctx, &client.ListJobsParams{Limit: ptr(10)})
	complete := decode[client.JobPage](t, f.result(r, err, 200))
	if len(complete.Items) != len(ids) || complete.NextCursor != nil {
		t.Fatal("incomplete unpaged Job List")
	}
	for i, item := range complete.Items {
		if item.Id != ids[i] || item.TenantId.String() != tenant {
			t.Fatal("unpaged Job ordering/isolation changed")
		}
	}
	for _, tc := range []struct{ name, token, cursor string }{{"malformed", "admin", "malformed"}, {"foreign", "other", firstCursor}, {"wrong-purpose", "admin", browseCursor}} {
		t.Run("job-cursor-"+tc.name, func(t *testing.T) {
			r, err := f.client(tc.token).ListJobs(ctx, &client.ListJobsParams{Limit: ptr(1), Cursor: &tc.cursor})
			assertInvalidProblem(t, f.result(r, err, 400))
		})
	}
	r, err = f.client("reader").BrowseCatalog(ctx, &client.BrowseCatalogParams{Limit: ptr(1), Cursor: &firstCursor})
	assertInvalidProblem(t, f.result(r, err, 400))
	r, err = administrator.ListJobs(ctx, &client.ListJobsParams{Kind: ptr("publication.generate"), State: ptr(client.ListJobsParamsState("succeeded")), Limit: ptr(10)})
	filtered := decode[client.JobPage](t, f.result(r, err, 200))
	if len(filtered.Items) != 2 || filtered.Items[0].Id != ids[0] || filtered.Items[1].Id != ids[1] {
		t.Fatalf("typed filters: %+v", filtered)
	}
	r, err = administrator.ListJobs(ctx, &client.ListJobsParams{Kind: ptr("unrecognized.job")})
	if page := decode[client.JobPage](t, f.result(r, err, 200)); len(page.Items) != 0 {
		t.Fatal("unknown literal kind selected Jobs")
	}
	r, err = administrator.ListJobs(ctx, &client.ListJobsParams{ParentJobId: &ids[0]})
	if page := decode[client.JobPage](t, f.result(r, err, 200)); len(page.Items) != 0 {
		t.Fatal("absent retry children selected")
	}
	r, err = administrator.ListJobs(ctx, &client.ListJobsParams{Limit: ptr(1), Cursor: &firstCursor, Kind: ptr("publication.generate")})
	assertInvalidProblem(t, f.result(r, err, 400))
	for _, query := range []struct{ field, value string }{{"State", "succeeded"}, {"Kind", "publication.generate"}, {"unsupported", "value"}} {
		r, err := administrator.ListJobs(ctx, nil, queryEditor(query.field, query.value))
		assertInvalidProblem(t, f.result(r, err, 400))
	}
	r, err = f.client("reader").BrowseCatalog(ctx, nil, queryEditor("state", "active"))
	assertInvalidProblem(t, f.result(r, err, 400))
	semanticID := uuid.MustParse("00000000-0000-7000-8000-000000000092")
	_, _, err = f.jobStore.Enqueue(ctx, jobs.Job{ID: semanticID.String(), TenantID: tenant, Kind: "semantic.reindex", State: jobs.StateSucceeded, AttemptLimit: 3, NotBefore: f.now, CreatedAt: f.now, UpdatedAt: f.now, Result: json.RawMessage(`{"generation":"` + semanticID.String() + `","members":0,"private":"private-semantic-sentinel"}`)})
	must(t, err)
	r, err = administrator.GetJob(ctx, semanticID)
	data := f.result(r, err, 200)
	if bytes.Contains(data, []byte("private-semantic")) {
		t.Fatal("semantic result exposed private data")
	}
	semantic := decode[client.Job](t, data)
	if semantic.Result == nil {
		t.Fatal("typed semantic summary absent")
	}
	summary, err := semantic.Result.AsSemanticJobResultSummary()
	must(t, err)
	if summary.Type != "semantic_index" || summary.GenerationId != semanticID || summary.Members != 0 {
		t.Fatalf("typed semantic completion: %+v", summary)
	}
}

func assertTypedJob(t *testing.T, job client.Job, id uuid.UUID, at time.Time, snapshot uuid.UUID, completed bool) {
	t.Helper()
	state, progress := client.JobState("running"), 40
	if completed {
		state, progress = "succeeded", 100
	}
	if job.Id != id || job.TenantId.String() != tenant || job.Kind != "publication.generate" || job.State != state || job.Progress != progress || job.Attempt != 1 || job.AttemptLimit != 3 || job.CancellationRequested || job.Priority != 0 || !job.CreatedAt.Equal(at) || !job.UpdatedAt.Equal(at) || !job.NotBefore.Equal(at) || job.ParentJobId != nil || job.SafeError != nil || job.FailureClass != nil {
		t.Fatalf("typed Job projection changed: %+v", job)
	}
	if !completed {
		if job.Result != nil {
			t.Fatal("running Job result should be absent")
		}
		return
	}
	if job.Result == nil {
		t.Fatal("completed Job reference absent")
	}
	result, err := job.Result.AsPublicationJobResultSummary()
	must(t, err)
	if result.Type != "publication_snapshot" || result.SnapshotId != snapshot || result.Host != "catalog.example.com" || result.Generation != 7 || result.State != "candidate" {
		t.Fatalf("typed Job reference changed: %+v", result)
	}
}

func assertInvalidProblem(t *testing.T, data []byte) {
	t.Helper()
	if decode[client.Problem](t, data).Code != "INVALID_ARGUMENT" {
		t.Fatalf("wrong cursor/query Problem: %s", data)
	}
}

func queryEditor(name, value string) client.RequestEditorFn {
	return func(_ context.Context, r *http.Request) error {
		q := r.URL.Query()
		q.Set(name, value)
		r.URL.RawQuery = q.Encode()
		return nil
	}
}
