// Demo verification tests use static receipt and discovery evidence without
// seeding a Catalog or opening a network listener.
package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

// demoVerificationReceipt assigns distinct fixed UUIDs without creating records.
func demoVerificationReceipt() demoReceipt {
	next := 10
	id := func() string {
		next++
		return fmt.Sprintf("00000000-0000-7000-8000-%012d", next)
	}
	r := demoReceipt{Dataset: demoID, BaseURL: "https://catalog.invalid", TenantID: demoTenant, NamespaceID: id(), PublisherID: id(), SnapshotID: id()}
	for _, f := range demoFixtures() {
		item := demoUnitItem(f)
		item.ResourceID, item.VersionID, item.VersionRevisionID = id(), id(), id()
		item.ListingID, item.ListingRevisionID = id(), id()
		if item.SubmissionID != "" {
			item.SubmissionID = id()
		}
		if item.ReleaseID != "" {
			item.ReleaseID = id()
		}
		r.Items = append(r.Items, item)
	}
	return r
}

// demoInventoryResponses retains exact request bounds and deliberately reverses
// resource order so receipt matching cannot depend on enumeration order.
func demoInventoryResponses(t *testing.T, r demoReceipt, populated bool) []smokeClientStep {
	t.Helper()
	resources := make([]string, 0, len(r.Items))
	for _, item := range r.Items {
		resources = append(resources, item.ResourceID)
	}
	slices.Reverse(resources)
	collections := []struct {
		path string
		ids  []string
	}{
		{"/api/v1/publication/snapshots?host=catalog.invalid&limit=6", []string{r.SnapshotID}},
		{"/api/v1/resources?limit=6", resources},
		{"/api/v1/namespaces?limit=6", []string{r.NamespaceID}},
		{"/api/v1/publishers?limit=6", []string{r.PublisherID}},
		{"/api/v1/federation/sources?limit=6", nil},
	}
	steps := make([]smokeClientStep, 0, len(collections))
	for _, collection := range collections {
		items := []map[string]string{}
		if populated {
			for _, id := range collection.ids {
				items = append(items, map[string]string{"id": id})
			}
		}
		steps = append(steps, smokeClientStep{name: collection.path, method: "GET", path: collection.path, token: "local-admin", status: 200, body: demoUnitJSON(t, map[string]any{"items": items})})
	}
	return steps
}

// demoDiscoveryDocuments supplies the projections consumed by the demo verifier;
// profile conformance and search ranking remain owned by their existing tests.
func demoDiscoveryDocuments(r demoReceipt) (publication.ARDManifest, publication.AICatalog, catalog.CatalogSearchResponse) {
	manifest := publication.ARDManifest{Entries: []publication.Entry{{Type: "application/ai-registry+json"}}}
	aiCatalog := publication.AICatalog{}
	search := catalog.CatalogSearchResponse{}
	for _, item := range r.Items[:3] {
		manifest.Entries = append(manifest.Entries, publication.Entry{Identifier: item.Identifier})
		aiCatalog.Entries = append(aiCatalog.Entries, publication.AICatalogEntry{Identifier: item.Identifier})
		search.Items = append(search.Items, catalog.CatalogSearchResult{Identifier: item.Identifier, Kind: item.Kind, Local: new((catalog.CatalogRecord{
			Release:  catalog.ListingRelease{ID: item.ReleaseID},
			Evidence: catalog.EvidenceSet{LegalRights: catalog.LegalRightsSummary{State: catalog.LegalRightsNotAssessed}},
		}).Summary().SearchLocal())})
	}
	return manifest, aiCatalog, search
}

// demoVerificationResponses reuses item evidence while keeping top-level parent,
// publication and discovery responses fixed and independent of server behavior.
func demoVerificationResponses(t *testing.T, r demoReceipt) []smokeClientStep {
	t.Helper()
	steps := demoInventoryResponses(t, r, true)
	add := func(name, path, token string, value any) {
		steps = append(steps, smokeClientStep{name: name, method: "GET", path: path, token: token, status: 200, body: demoUnitJSON(t, value)})
	}
	add("namespace", "/api/v1/namespaces/"+r.NamespaceID, "local-admin", map[string]any{"id": r.NamespaceID, "tenantId": demoTenant, "slug": demoID, "displayName": "Catalog demo", "revision": 1})
	add("publisher", "/api/v1/publishers/"+r.PublisherID, "local-admin", map[string]any{"id": r.PublisherID, "tenantId": demoTenant, "displayName": "Demo Publisher", "domains": []string{demoDomain}, "approvedPublicationDomains": []string{demoDomain}, "revision": 2})
	for i, f := range demoFixtures() {
		for _, step := range demoItemReadResponses(t, f, r, r.Items[i]) {
			step.name = f.Segment + " " + step.name
			steps = append(steps, step)
		}
	}
	add("snapshot", "/api/v1/publication/snapshots/"+r.SnapshotID+"?host=catalog.invalid", "local-admin", map[string]any{"id": r.SnapshotID, "tenantId": demoTenant, "host": "catalog.invalid", "state": "active", "audience": "anonymous"})
	manifest, aiCatalog, search := demoDiscoveryDocuments(r)
	add("ARD", "/.well-known/ard.json", "", manifest)
	add("AI Catalog", "/.well-known/ai-catalog.json", "", aiCatalog)
	steps = append(steps, smokeClientStep{name: "search", method: "POST", path: "/api/v1/catalog/search", token: "local-reader", status: 200, body: demoUnitJSON(t, search)})
	return steps
}

// verifyDemoResponses checks the reader search body as well as every request's
// method, authority and absence of mutation headers through the shared transport.
func verifyDemoResponses(t *testing.T, r demoReceipt, steps []smokeClientStep, failTransport bool) error {
	t.Helper()
	fields := make([]map[string]any, len(steps))
	for i, step := range steps {
		if step.name == "search" {
			fields[i] = map[string]any{"tags": []string{demoID}, "limit": 6}
		}
	}
	return verifyDemo(t.Context(), demoUnitClient(t, steps, failTransport, fields), r)
}

// TestDemoInventoryRequiresExactSets rejects missing or substituted records as
// well as excess content, including any federation source on a demo target.
func TestDemoInventoryRequiresExactSets(t *testing.T) {
	t.Parallel()
	r := demoVerificationReceipt()
	for _, populated := range []bool{false, true} {
		t.Run(fmt.Sprintf("populated=%t", populated), func(t *testing.T) {
			baseline := demoInventoryResponses(t, r, populated)
			t.Run("success", func(t *testing.T) {
				if err := demoInventory(t.Context(), demoUnitClient(t, baseline, false, nil), "catalog.invalid", r, populated); err != nil {
					t.Fatal(err)
				}
			})
			for i, step := range baseline {
				faults := []string{"extra"}
				if populated && i < len(baseline)-1 {
					faults = append(faults, "missing", "substituted")
				}
				for _, fault := range faults {
					t.Run(step.name+" "+fault, func(t *testing.T) {
						steps := append([]smokeClientStep(nil), baseline[:i+1]...)
						var page struct {
							Items []map[string]string `json:"items"`
						}
						if err := json.Unmarshal([]byte(step.body), &page); err != nil {
							t.Fatal(err)
						}
						want := "receipt does not match catalog inventory"
						switch fault {
						case "extra":
							page.Items = append(page.Items, map[string]string{"id": "00000000-0000-7000-8000-000000000099"})
							want = "unrelated or partial demo content"
						case "missing":
							page.Items = page.Items[1:]
						case "substituted":
							page.Items[0]["id"] = "00000000-0000-7000-8000-000000000099"
						}
						steps[i].body = demoUnitJSON(t, page)
						err := demoInventory(t.Context(), demoUnitClient(t, steps, false, nil), "catalog.invalid", r, populated)
						if err == nil || !strings.Contains(err.Error(), want) {
							t.Fatalf("rejection=%v, want %q", err, want)
						}
					})
				}
			}
		})
	}
}

// TestVerifyDemoStopsOnReadFailure checks top-level failure propagation, including
// fixture attribution, without repeating every item-level field rejection.
func TestVerifyDemoStopsOnReadFailure(t *testing.T) {
	t.Parallel()
	r := demoVerificationReceipt()
	t.Run("invalid receipt before requests", func(t *testing.T) {
		invalid := r
		invalid.TenantID = "00000000-0000-7000-8000-000000000099"
		if err := verifyDemoResponses(t, invalid, nil, false); err == nil || !strings.Contains(err.Error(), "invalid dataset, target or record IDs") {
			t.Fatalf("invalid receipt: %v", err)
		}
	})
	baseline := demoVerificationResponses(t, r)
	for i, step := range baseline {
		fixture := ""
		for _, item := range r.Items {
			if strings.HasPrefix(step.name, item.Key+" ") {
				fixture = item.Key
			}
		}
		if fixture != "" && step.name != fixture+" resource" {
			continue
		}
		for _, fault := range []string{"transport", "JSON", "status"} {
			t.Run(step.name+" "+fault, func(t *testing.T) {
				steps := append([]smokeClientStep(nil), baseline[:i+1]...)
				want := "request failed or deadline exceeded"
				switch fault {
				case "JSON":
					steps[i].body, want = `[`, "invalid API JSON response"
				case "status":
					steps[i].status, steps[i].body, want = 503, "private-demo-response-body", "HTTP 503, expected 200"
				}
				err := verifyDemoResponses(t, r, steps, fault == "transport")
				if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "private-demo") || fixture != "" && !strings.HasPrefix(err.Error(), fixture+": ") {
					t.Fatalf("rejection=%v, want %q attributed to %q without private details", err, want, fixture)
				}
			})
		}
	}
}

// TestVerifyDemoRequiresConsistentDiscovery protects active anonymous publication,
// the published-only membership of both manifests and exact local search evidence.
func TestVerifyDemoRequiresConsistentDiscovery(t *testing.T) {
	t.Parallel()
	r := demoVerificationReceipt()
	baseline := demoVerificationResponses(t, r)
	type rejection struct{ name, step, body, want string }
	cases := []rejection{{name: "success"}}
	for _, step := range baseline {
		var fields []string
		switch step.name {
		case "namespace":
			fields = []string{"id", "tenantId", "slug", "displayName", "revision"}
		case "publisher":
			fields = []string{"id", "tenantId", "displayName", "domains", "approvedPublicationDomains", "revision"}
		case "snapshot":
			fields = []string{"id", "tenantId", "host", "state", "audience"}
		}
		for _, field := range fields {
			var body map[string]any
			if err := json.Unmarshal([]byte(step.body), &body); err != nil {
				t.Fatal(err)
			}
			body[field] = "wrong"
			switch field {
			case "id", "tenantId":
				body[field] = "00000000-0000-7000-8000-000000000099"
			case "revision":
				body[field] = 99
			case "domains", "approvedPublicationDomains":
				body[field] = []string{"other.catalog.invalid"}
			}
			cases = append(cases, rejection{step.name + " " + field, step.name, demoUnitJSON(t, body), "fixture field " + field + " differs"})
		}
	}
	for _, fault := range []string{"ARD count", "ARD excess", "Registry absent", "ARD duplicate", "ARD draft", "ARD pending", "AI count", "AI excess", "AI duplicate", "AI draft", "AI pending", "search count", "search excess", "search cursor", "search partial", "search local absent", "search release", "search identifier", "search kind", "search duplicate", "reordered"} {
		manifest, aiCatalog, search := demoDiscoveryDocuments(r)
		step, want := "ARD", "public discovery does not match demo lifecycle"
		var body any
		switch fault {
		case "ARD count":
			manifest.Entries = manifest.Entries[:3]
			want = "public manifest must contain three Resources and the Registry"
		case "ARD excess":
			manifest.Entries = append(manifest.Entries, publication.Entry{Identifier: "urn:air:demo.catalog.example.com:demo:unrelated"})
			want = "public manifest must contain three Resources and the Registry"
		case "Registry absent":
			manifest.Entries[0].Type = "application/json"
			want = "public manifest must contain three Resources and the Registry"
		case "ARD duplicate":
			manifest.Entries[1] = manifest.Entries[2]
		case "ARD draft":
			manifest.Entries[0].Identifier = r.Items[3].Identifier
		case "ARD pending":
			manifest.Entries[0].Identifier = r.Items[4].Identifier
		case "AI count":
			aiCatalog.Entries = aiCatalog.Entries[:2]
		case "AI excess":
			aiCatalog.Entries = append(aiCatalog.Entries, publication.AICatalogEntry{Identifier: "urn:air:demo.catalog.example.com:demo:unrelated"})
		case "AI duplicate":
			aiCatalog.Entries[0] = aiCatalog.Entries[1]
		case "AI draft":
			aiCatalog.Entries[0].Identifier = r.Items[3].Identifier
		case "AI pending":
			aiCatalog.Entries[0].Identifier = r.Items[4].Identifier
		case "search count":
			search.Items = search.Items[:2]
		case "search excess":
			search.Items = append(search.Items, search.Items[0])
		case "search cursor":
			search.NextCursor = "another-page"
		case "search partial":
			search.Partial = true
		case "search local absent":
			search.Items[0].Local = nil
		case "search release":
			search.Items[0].Local.Release.ID = r.Items[1].ReleaseID
		case "search identifier":
			search.Items[0].Identifier = r.Items[1].Identifier
		case "search kind":
			search.Items[0].Kind = catalog.KindSkill
		case "search duplicate":
			search.Items[0] = search.Items[1]
		case "reordered":
			slices.Reverse(manifest.Entries)
			slices.Reverse(aiCatalog.Entries)
			slices.Reverse(search.Items)
		}
		body = manifest
		if strings.HasPrefix(fault, "AI ") {
			step, body = "AI Catalog", aiCatalog
			if fault == "AI count" || fault == "AI excess" {
				want = "AI Catalog must contain three published Resources"
			}
		} else if strings.HasPrefix(fault, "search ") {
			step, body, want = "search", search, "governed Search identities differ"
			if fault == "search count" || fault == "search excess" || fault == "search cursor" || fault == "search partial" {
				want = "governed Search does not contain exactly three published items"
			}
		}
		if fault == "reordered" {
			t.Run(fault, func(t *testing.T) {
				steps := append([]smokeClientStep(nil), baseline...)
				steps[len(steps)-3].body = demoUnitJSON(t, manifest)
				steps[len(steps)-2].body = demoUnitJSON(t, aiCatalog)
				steps[len(steps)-1].body = demoUnitJSON(t, search)
				if err := verifyDemoResponses(t, r, steps, false); err != nil {
					t.Fatal(err)
				}
			})
			continue
		}
		cases = append(cases, rejection{fault, step, demoUnitJSON(t, body), want})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			steps := append([]smokeClientStep(nil), baseline...)
			if tc.step != "" {
				index := slices.IndexFunc(steps, func(step smokeClientStep) bool { return step.name == tc.step })
				if index < 0 {
					t.Fatal("fault has no matching request")
				}
				steps[index].body = tc.body
				// Membership comparisons require both manifests before deciding.
				if tc.step == "ARD" && tc.want == "public discovery does not match demo lifecycle" {
					index++
				}
				steps = steps[:index+1]
			}
			err := verifyDemoResponses(t, r, steps, false)
			if tc.step == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("rejection=%v, want %q", err, tc.want)
			}
		})
	}
}

// These are parser and continuation decisions over static bytes. Real HTTP
// pagination qualification remains in the functional demo suite.
func TestDemoInventoryPaginationRefusal(t *testing.T) {
	t.Parallel()
	const first = "00000000-0000-7000-8000-000000000041"
	const second = "00000000-0000-7000-8000-000000000042"
	const base = "/api/v1/publication/snapshots?host=catalog.invalid&limit=6"
	page := func(ids []string, cursor string) string {
		items := make([]map[string]string, 0, len(ids))
		for _, id := range ids {
			items = append(items, map[string]string{"id": id})
		}
		return demoUnitJSON(t, map[string]any{"items": items, "nextCursor": cursor})
	}
	for _, tc := range []struct {
		name         string
		maximum      int
		pages, paths []string
		want         []string
		problem      string
	}{
		{name: "empty zero-bound", maximum: 0, pages: []string{page(nil, "")}},
		{name: "escaped continuation at bound", maximum: 2, pages: []string{page([]string{first}, "a+b /?&"), page([]string{second}, "")}, paths: []string{base, base + "&cursor=a%2Bb+%2F%3F%26"}, want: []string{first, second}},
		{name: "maximum cursor", maximum: 2, pages: []string{page([]string{first}, strings.Repeat("c", 2048)), page([]string{second}, "")}, paths: []string{base, base + "&cursor=" + strings.Repeat("c", 2048)}, want: []string{first, second}},
		{name: "missing items", maximum: 2, pages: []string{`{}`}, problem: "inventory response must contain an items array"},
		{name: "null items", maximum: 2, pages: []string{`{"items":null}`}, problem: "inventory response must contain an items array"},
		{name: "invalid identity", maximum: 2, pages: []string{page([]string{"not-a-uuid"}, "")}, problem: "invalid or repeated inventory ID"},
		{name: "duplicate across pages", maximum: 2, pages: []string{page([]string{first}, "next"), page([]string{first}, "")}, problem: "invalid or repeated inventory ID"},
		{name: "extra after full page", maximum: 1, pages: []string{page([]string{first}, "next"), page([]string{second}, "")}, problem: "unrelated or partial demo content; refusing writes"},
		{name: "empty continued page", maximum: 2, pages: []string{page(nil, "next")}, problem: "inventory pagination exceeded the demo bound"},
		{name: "repeated cursor", maximum: 2, pages: []string{page([]string{first}, "next"), page([]string{second}, "next")}, problem: "inventory pagination exceeded the demo bound"},
		{name: "oversized cursor", maximum: 2, pages: []string{page([]string{first}, strings.Repeat("c", 2049))}, problem: "inventory pagination exceeded the demo bound"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			steps := make([]smokeClientStep, len(tc.pages))
			for i, body := range tc.pages {
				path := base
				if i > 0 {
					path += "&cursor=next"
				}
				if tc.paths != nil {
					path = tc.paths[i]
				}
				steps[i] = smokeClientStep{name: tc.name, method: "GET", path: path, token: "local-admin", status: 200, body: body}
			}
			ids, err := demoPageIDs(t.Context(), demoUnitClient(t, steps, false, nil), "/api/v1/publication/snapshots?host=catalog.invalid", tc.maximum)
			if tc.problem != "" {
				if err == nil || err.Error() != "seed-demo: "+tc.problem || ids != nil {
					t.Fatalf("ids=%v err=%v", ids, err)
				}
			} else if err != nil || !slices.Equal(ids, tc.want) {
				t.Fatalf("ids=%v err=%v want=%v", ids, err, tc.want)
			}
		})
	}
}
