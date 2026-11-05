package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

func TestDemoItemHistoryKeepsOnlyReceiptOwnedMembers(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"published", "draft", "pending_review"} {
		for _, fault := range []string{"control", "different version", "extra revision", "unexpected submission", "unexpected release", "denied"} {
			t.Run(state+"/"+fault, func(t *testing.T) {
				item := demoItem{State: state, ResourceID: demoTestUUID(4700), VersionID: demoTestUUID(4701), VersionRevisionID: demoTestUUID(4702), ListingID: demoTestUUID(4703), ListingRevisionID: demoTestUUID(4704)}
				if state != "draft" {
					item.SubmissionID = demoTestUUID(4705)
				}
				if state == "published" {
					item.ReleaseID = demoTestUUID(4706)
				}
				collections := map[string][]string{
					"/api/v1/resources/" + item.ResourceID + "/versions":         {item.VersionID},
					"/api/v1/resources/" + item.ResourceID + "/listings":         {item.ListingID},
					"/api/v1/resource-versions/" + item.VersionID + "/revisions": {item.VersionRevisionID},
					"/api/v1/catalog-listings/" + item.ListingID + "/revisions":  {item.ListingRevisionID},
					"/api/v1/review-submissions?listingId=" + item.ListingID:     {},
					"/api/v1/catalog-listings/" + item.ListingID + "/releases":   {},
				}
				if item.SubmissionID != "" {
					collections["/api/v1/review-submissions?listingId="+item.ListingID] = []string{item.SubmissionID}
				}
				if item.ReleaseID != "" {
					collections["/api/v1/catalog-listings/"+item.ListingID+"/releases"] = []string{item.ReleaseID}
				}
				switch fault {
				case "different version":
					collections["/api/v1/resources/"+item.ResourceID+"/versions"] = []string{demoTestUUID(4799)}
				case "extra revision":
					path := "/api/v1/resource-versions/" + item.VersionID + "/revisions"
					collections[path] = append(collections[path], demoTestUUID(4799))
				case "unexpected submission":
					path := "/api/v1/review-submissions?listingId=" + item.ListingID
					collections[path] = append(collections[path], demoTestUUID(4799))
				case "unexpected release":
					path := "/api/v1/catalog-listings/" + item.ListingID + "/releases"
					collections[path] = append(collections[path], demoTestUUID(4799))
				}
				seen := map[string]bool{}
				c := &smokeHTTPClient{baseURL: "http://127.0.0.1:8080", safeErrors: true, client: &http.Client{Transport: catalogctlRoundTripFunc(func(r *http.Request) (*http.Response, error) {
					query := r.URL.Query()
					query.Del("limit")
					path := r.URL.Path
					if len(query) != 0 {
						path += "?" + query.Encode()
					}
					ids, ok := collections[path]
					if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer local-admin" || !ok || seen[path] {
						t.Fatalf("unexpected or repeated history request: %s %s", r.Method, r.URL.RequestURI())
					}
					seen[path] = true
					items := make([]map[string]string, 0, len(ids))
					for _, id := range ids {
						items = append(items, map[string]string{"id": id})
					}
					status := http.StatusOK
					if fault == "denied" {
						status = http.StatusForbidden
					}
					return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(demoUnitJSON(t, map[string]any{"items": items})))}, nil
				})}}
				err := verifyDemoItemHistory(t.Context(), c, item)
				if fault == "control" {
					if err != nil || len(seen) != 6 {
						t.Fatalf("exact owned history refused: %v, reads %d", err, len(seen))
					}
					return
				}
				want, calls := "seed-demo: unrelated or partial demo content; refusing writes", 1
				switch fault {
				case "different version":
					want = "seed-demo: owned history differs; refusing writes"
				case "extra revision":
					calls = 3
				case "unexpected submission":
					calls = 5
				case "unexpected release":
					calls = 6
				case "denied":
					want = "demo GET /api/v1/resources/" + item.ResourceID + "/versions: HTTP 403, expected 200"
				}
				if err == nil || err.Error() != want || len(seen) != calls {
					t.Fatalf("changed or denied history admitted: %v, reads %d; want %q after %d reads", err, len(seen), want, calls)
				}
			})
		}
	}
}

func expandedUnitReceipt() demoReceipt {
	r := demoVerificationReceipt()
	r.Dataset, r.ArchiveOrigin, r.PluginNamespaceID = expandedDemoID, "https://127.0.0.1:18443", "00000000-0000-7000-8000-000000000090"
	fixtures := expandedDemoFixtures(r.ArchiveOrigin)
	for i, f := range fixtures[5:] {
		item := demoUnitItem(f)
		// Allocate independent, fixed UUIDs, not the implementation's ID generator.
		ids := []*string{&item.ResourceID, &item.VersionID, &item.VersionRevisionID, &item.ListingID, &item.ListingRevisionID, &item.SubmissionID, &item.ReleaseID}
		for j, p := range ids {
			*p = demoTestUUID(1000 + i*10 + j)
		}
		r.Items = append(r.Items, item)
	}
	return r
}

func demoTestUUID(n int) string {
	const digits = "0123456789abcdef"
	tail := []byte("000000000000")
	for i := len(tail) - 1; n > 0; i-- {
		tail[i] = digits[n%16]
		n /= 16
	}
	return "00000000-0000-7000-8000-" + string(tail)
}

func TestExpandedReceiptAndInventory(t *testing.T) {
	r := expandedUnitReceipt()
	if err := validateDemoReceipt(r, r.BaseURL); err != nil {
		t.Fatal(err)
	}
	counts := map[catalog.Kind]int{}
	for _, f := range expandedDemoFixtures(r.ArchiveOrigin) {
		if f.state == "published" {
			counts[f.Kind]++
		}
	}
	if !reflect.DeepEqual(counts, map[catalog.Kind]int{catalog.KindAgent: 3, catalog.KindMCPServer: 3, catalog.KindSkill: 3, catalog.KindPlugin: 3}) {
		t.Fatal(counts)
	}
	for _, fault := range []string{"namespace", "duplicate", "origin", "state", "missing", "identifier", "submission", "invalid-id", "snapshot-alias", "draft-release", "published-release", "item-order", "tenant", "instance"} {
		t.Run(fault, func(t *testing.T) {
			changed := expandedUnitReceipt()
			switch fault {
			case "namespace":
				changed.PluginNamespaceID = changed.NamespaceID
			case "duplicate":
				changed.Items[5].ResourceID = changed.Items[0].ResourceID
			case "origin":
				changed.ArchiveOrigin = "https://public.example.com"
			case "state":
				changed.Items[5].State = "draft"
			case "missing":
				changed.Items = changed.Items[:13]
			case "identifier":
				changed.Items[13].Identifier = "urn:air:demo.catalog.example.com:demo:release-workflow"
			case "submission":
				changed.Items[0].SubmissionID = ""
			case "invalid-id":
				changed.Items[0].VersionRevisionID = "not-a-uuid"
			case "snapshot-alias":
				changed.SnapshotID = changed.Items[0].ReleaseID
			case "draft-release":
				changed.Items[3].ReleaseID = demoTestUUID(3000)
			case "published-release":
				changed.Items[0].ReleaseID = ""
			case "item-order":
				changed.Items[0], changed.Items[1] = changed.Items[1], changed.Items[0]
			case "tenant":
				changed.TenantID = demoTestUUID(3000)
			case "instance":
				changed.BaseURL = "http://127.0.0.1:8081"
			}
			if err := validateDemoReceipt(changed, r.BaseURL); err == nil || err.Error() != "seed-demo: invalid expanded dataset receipt" {
				t.Fatalf("receipt guard: %v", err)
			}
		})
	}
}

func TestExpandedSnapshotMembership(t *testing.T) {
	// Unrelated releases are allowed, but each receipt
	// member must occur in both public documents exactly when it is published.
	r := demoReceipt{Items: []demoItem{
		{Key: "published", Identifier: "urn:air:example.com:demo:published", State: "published"},
		{Key: "draft", Identifier: "urn:air:example.com:demo:draft", State: "draft"},
		{Key: "pending", Identifier: "urn:air:example.com:demo:pending", State: "pending_review"},
	}}
	control := publication.Snapshot{
		ARD:       publication.ARDManifest{Entries: []publication.Entry{{Identifier: r.Items[0].Identifier}, {Identifier: "urn:air:example.com:demo:unrelated"}}},
		AICatalog: publication.AICatalog{Entries: []publication.AICatalogEntry{{Identifier: "urn:air:example.com:demo:unrelated"}, {Identifier: r.Items[0].Identifier}}},
	}
	if err := verifyExpandedSnapshot(control, r); err != nil {
		t.Fatal(err)
	}
	for _, fault := range []string{"missing-ard", "missing-ai", "missing-both", "draft-ard", "draft-ai", "pending-both"} {
		t.Run(fault, func(t *testing.T) {
			s := control
			s.ARD.Entries = slices.Clone(control.ARD.Entries)
			s.AICatalog.Entries = slices.Clone(control.AICatalog.Entries)
			key := "published"
			switch fault {
			case "missing-ard":
				s.ARD.Entries = s.ARD.Entries[1:]
			case "missing-ai":
				s.AICatalog.Entries = s.AICatalog.Entries[:1]
			case "missing-both":
				s.ARD.Entries, s.AICatalog.Entries = nil, nil
			case "draft-ard":
				key = "draft"
				s.ARD.Entries = append(s.ARD.Entries, publication.Entry{Identifier: r.Items[1].Identifier})
			case "draft-ai":
				key = "draft"
				s.AICatalog.Entries = append(s.AICatalog.Entries, publication.AICatalogEntry{Identifier: r.Items[1].Identifier})
			case "pending-both":
				key = "pending"
				s.ARD.Entries = append(s.ARD.Entries, publication.Entry{Identifier: r.Items[2].Identifier})
				s.AICatalog.Entries = append(s.AICatalog.Entries, publication.AICatalogEntry{Identifier: r.Items[2].Identifier})
			}
			want := "seed-demo: snapshot membership differs from reviewed demo release: " + key
			if err := verifyExpandedSnapshot(s, r); err == nil || err.Error() != want {
				t.Fatalf("snapshot guard: %v; want %q", err, want)
			}
		})
	}
}

func TestDemoPrivateStateAndLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipt.json")
	lock, err := lockDemoState(path)
	if err != nil {
		t.Fatal(err)
	}
	if other, e := lockDemoState(path); e == nil {
		_ = other.Close()
		t.Fatal("concurrent lock acquired")
	}
	if err = lock.Close(); err != nil {
		t.Fatal(err)
	}
	lock, err = lockDemoState(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if err = replaceDemoFile(path, map[string]string{"state": "one"}, nil, false); err != nil {
		t.Fatal(err)
	}
	before, err := remoteReadFile(path, demoReceiptLimit)
	if err != nil {
		t.Fatal(err)
	}
	if replaceDemoFile(path, map[string]string{"state": "two"}, []byte("wrong"), true) == nil {
		t.Fatal("changed receipt overwritten")
	}
	if replaceDemoFile(path, map[string]string{"state": "two"}, nil, false) == nil {
		t.Fatal("existing receipt overwritten")
	}
	if err = replaceDemoFile(path, map[string]string{"state": "two"}, before, true); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("state permissions")
	}
}

func TestDemoProgressBoundsAndBinding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "progress.json")
	p, exists, err := readDemoProgress(path, "http://127.0.0.1:8080", "https://127.0.0.1:18443")
	if err != nil || exists {
		t.Fatal(err)
	}
	if err = p.save(); err != nil {
		t.Fatal(err)
	}
	if _, ok, e := readDemoProgress(path, p.BaseURL, p.Origin); e != nil || !ok {
		t.Fatal(e)
	}
	if _, _, e := readDemoProgress(path, "http://127.0.0.1:8081", p.Origin); e == nil {
		t.Fatal("different target admitted")
	}
	for _, raw := range []string{`null`, `{}`, `{"dataset":"a","dataset":"b"}`} {
		if err = os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, e := readDemoProgress(path, p.BaseURL, p.Origin); e == nil {
			t.Fatal("invalid progress admitted")
		}
	}
	// The journal serializes only public fixed fixture data and lookup IDs.
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0644); err != nil { // #nosec G302 -- deliberately public permissions test private-state rejection.
		t.Fatal(err)
	}
	if _, _, e := readDemoProgress(path, p.BaseURL, p.Origin); e == nil {
		t.Fatal("public progress admitted")
	}
}

func TestDemoOriginAndIntentRoutes(t *testing.T) {
	for _, origin := range []string{"http://127.0.0.1:18443", "https://localhost:18443", "https://example.com", "https://user@127.0.0.1", "https://127.0.0.1/path", "https://127.0.0.1?x=1", "https://127.0.0.1#x", "https://[::ffff:127.0.0.1]"} {
		if validateDemoArchiveOrigin(origin) == nil {
			t.Fatal(origin)
		}
	}
	if validateDemoArchiveOrigin("https://127.0.0.1:18443") != nil {
		t.Fatal("valid local origin")
	}
	if _, e := demoStepCollection(demoStep{Path: "/unrecognized", Body: json.RawMessage(`{}`)}); e == nil {
		t.Fatal("unrecognized route")
	}
}

func TestMinimalSeedRefusesExpandedStateBeforeHTTP(t *testing.T) {
	// Cancellation prevents real I/O even if a guard regresses; exact errors prove the intended guard ran.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, fault := range []string{"expanded receipt", "invalid receipt", "progress"} {
		t.Run(fault, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "receipt.json")
			r := expandedUnitReceipt()
			r.BaseURL = "http://127.0.0.1:1"
			var name, want string
			var raw []byte
			switch fault {
			case "expanded receipt":
				want = "seed-demo: minimal sample set cannot use an expanded receipt"
				name = path
				var err error
				raw, err = json.Marshal(r)
				if err != nil {
					t.Fatal(err)
				}
			case "invalid receipt":
				want = "seed-demo: receipt is invalid or belongs to another dataset/instance; keep it with its owned demo catalog"
				name = path
				raw = []byte(`{}`)
			case "progress":
				want = "seed-demo: minimal sample set cannot use expanded progress"
				name = path + ".progress"
				raw = []byte(`{}`)
			}
			if err := os.WriteFile(name, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := executeMinimalDemo(ctx, r.BaseURL, path); err == nil || err.Error() != want {
				t.Fatalf("minimal seed reached the wrong guard: got %v; want %q", err, want)
			}
			saved, err := os.ReadFile(name) // #nosec G304 -- reads only the fixture created above in this unit test's private temporary directory.
			if err != nil || string(saved) != string(raw) {
				t.Fatal("selected state was modified")
			}
		})
	}
}
