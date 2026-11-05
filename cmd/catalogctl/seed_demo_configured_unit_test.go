package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/demodata"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

func configuredSearchSample(index int, kind catalog.Kind) (demoItem, catalog.CatalogSearchResult) {
	base := 4600 + index*10
	item := demoItem{Key: fmt.Sprintf("sample-%d", index), State: "published", Kind: kind, Identifier: fmt.Sprintf("urn:air:example.com:demo:sample-%d", index),
		ResourceID: demoTestUUID(base), VersionID: demoTestUUID(base + 1), VersionRevisionID: demoTestUUID(base + 2),
		ListingID: demoTestUUID(base + 3), ListingRevisionID: demoTestUUID(base + 4), SubmissionID: demoTestUUID(base + 5), ReleaseID: demoTestUUID(base + 6)}
	digest, evidenceID, namespaceID := "sha256:"+strings.Repeat("a", 64), demoTestUUID(base+7), demoTestUUID(4500)
	local := catalog.CatalogSearchLocal{
		Resource:        catalog.CatalogResourceSummary{ID: item.ResourceID, TenantID: demoTenant, NamespaceID: namespaceID, CanonicalIdentifier: item.Identifier, Kind: kind},
		Version:         catalog.CatalogVersionSummary{ID: item.VersionID, Version: "1.0.0"},
		VersionRevision: catalog.CatalogRevisionSummary{ID: item.VersionRevisionID, ContentSetDigest: digest, EvidenceSetID: evidenceID},
		Listing:         catalog.CatalogListingSummary{ID: item.ListingID, NamespaceID: namespaceID},
		ListingRevision: catalog.CatalogListingStatus{ID: item.ListingRevisionID, Visibility: catalog.VisibilityPublic},
		Release:         catalog.CatalogReleaseSummary{ID: item.ReleaseID, VersionRevisionID: item.VersionRevisionID, ListingRevisionID: item.ListingRevisionID, State: catalog.ReleasePublished, ActivatedAt: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)},
		Evidence:        catalog.CatalogEvidenceSummary{ID: evidenceID, Digest: digest, State: catalog.EvidenceComplete, Trust: catalog.CatalogTrustSummary{State: catalog.TrustNotAssessed}, LegalRights: catalog.LegalRightsSummary{State: catalog.LegalRightsNotAssessed}},
	}
	return item, catalog.CatalogSearchResult{ID: "local:" + item.ReleaseID, Type: catalog.SearchResultLocal, Kind: kind, Identifier: item.Identifier,
		VersionPresent: true, Version: "1.0.0", DisplayName: item.Key, Tags: []string{"demo"}, Publisher: demoTestUUID(4501), Score: 100, Local: &local}
}

func TestConfiguredSearchRequiresExactVisibleSamplesAcrossPages(t *testing.T) {
	t.Parallel()
	for _, fault := range []string{"control", "partial", "missing", "wrong kind", "nonlocal", "wrong release", "duplicate", "draft published outside loader", "repeated cursor", "page bound", "denied"} {
		t.Run(fault, func(t *testing.T) {
			receipt := demoReceipt{}
			results := make([]catalog.CatalogSearchResult, 0, 4)
			for index, kind := range []catalog.Kind{catalog.KindAgent, catalog.KindMCPServer, catalog.KindSkill, catalog.KindPlugin} {
				item, result := configuredSearchSample(index, kind)
				receipt.Items = append(receipt.Items, item)
				results = append(results, result)
			}
			draft, outsideRelease := configuredSearchSample(4, catalog.KindAgent)
			draft.State, draft.SubmissionID, draft.ReleaseID = "draft", "", ""
			receipt.Items = append(receipt.Items, draft)
			switch fault {
			case "missing":
				results = results[:3]
			case "wrong kind":
				results[0].Kind = catalog.KindSkill
			case "nonlocal":
				results[0].Local = nil
			case "wrong release":
				results[0].Local.Release.ID = demoTestUUID(4699)
			case "duplicate":
				results = append(results, results[0])
			case "draft published outside loader":
				// A real released result for a receipt-owned draft is lifecycle drift.
				// State and release ownership both refuse it; neither guard is isolated.
				results = append(results, outsideRelease)
			}
			calls := 0
			c := &smokeHTTPClient{baseURL: "http://127.0.0.1:8080", safeErrors: true, client: &http.Client{Transport: catalogctlRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/catalog/search" || r.Header.Get("Authorization") != "Bearer local-reader" || r.Header.Get("Idempotency-Key") != "" {
					t.Fatalf("unexpected verification request: %s %s", r.Method, r.URL.Path)
				}
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				_ = r.Body.Close()
				want := map[string]any{"limit": float64(20)}
				if calls > 1 {
					want["cursor"] = fmt.Sprintf("page-%d", calls-1)
				}
				if !reflect.DeepEqual(request, want) {
					t.Fatalf("continuation changed verification inputs: %v", request)
				}
				response := catalog.CatalogSearchResponse{Items: results, DurableTotal: len(results) + 1, Profile: catalog.CoreSearchProfile, Generation: "sha256:" + strings.Repeat("c", 64), Tier: "core", Partial: fault == "partial"}
				switch {
				case fault == "page bound" || fault == "repeated cursor":
					response.Items = []catalog.CatalogSearchResult{}
					response.NextCursor = fmt.Sprintf("page-%d", calls)
					if fault == "repeated cursor" {
						response.NextCursor = "page-1"
					}
				case calls == 1:
					response.Items = results[:2]
					// An unrelated release is permitted and must not become an expected sample.
					_, unrelated := configuredSearchSample(5, catalog.KindAgent)
					response.Items = append(slices.Clone(response.Items), unrelated)
					response.NextCursor = "page-1"
				default:
					response.Items = results[2:]
				}
				status := http.StatusOK
				if fault == "denied" {
					status = http.StatusForbidden
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader([]byte(demoUnitJSON(t, response))))}, nil
			})}}
			err := verifyConfiguredSearch(t.Context(), c, receipt)
			want := "seed-demo: local discovery identity differs"
			switch fault {
			case "control":
				if err != nil || calls != 2 {
					t.Fatalf("valid two-page sample readback: %v, calls %d", err, calls)
				}
				return
			case "partial":
				want = "seed-demo: incomplete local discovery"
			case "missing":
				want = "seed-demo: local discovery is missing sample: sample-3"
			case "repeated cursor":
				want = "seed-demo: repeated discovery cursor"
			case "page bound":
				want = "seed-demo: local inventory exceeds verification bound"
			case "denied":
				want = "demo POST /api/v1/catalog/search: HTTP 403, expected 200"
			}
			if err == nil || err.Error() != want {
				t.Fatalf("invalid discovery admitted or wrong refusal: %v; want %q", err, want)
			}
			if fault == "page bound" && calls != 10 || fault == "repeated cursor" && calls != 2 {
				t.Fatalf("continuation was not bounded: calls %d", calls)
			}
		})
	}
}

func configuredUnitReceipt(t *testing.T) (demoReceipt, demodata.Plan) {
	t.Helper()
	r := expandedUnitReceipt()
	plan := demodata.DefaultPlan("expanded", r.ArchiveOrigin)
	sortDemoPlan(&plan)
	r.Dataset = plan.Dataset
	r.Bindings = &demoBindings{Format: 1, Inputs: map[string]string{}, Parents: map[string]string{}, Namespaces: map[string]string{demoID: r.NamespaceID, "ide-plugins": r.PluginNamespaceID}, Publishers: map[string]string{"demo": r.PublisherID}}
	for _, p := range plan.Resources {
		r.Bindings.Inputs[p.Resource.Key] = p.Digest
	}
	for _, ns := range plan.Namespaces {
		r.Bindings.Parents["namespace:"+ns.Key] = demoHash(ns)
	}
	for _, pub := range plan.Publishers {
		r.Bindings.Parents["publisher:"+pub.Key] = demoHash(pub)
	}
	return r, plan
}
func TestConfiguredReceiptInputBinding(t *testing.T) {
	r, plan := configuredUnitReceipt(t)
	if err := validateDemoReceipt(r, r.BaseURL); err != nil {
		t.Fatal(err)
	}
	if n, err := checkDemoBindings(plan, r); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	original := demoPlanBinding(plan)
	slices.Reverse(plan.Resources)
	slices.Reverse(plan.Namespaces)
	plan.Revision = "new-document-revision"
	plan.Profile = "equivalent-profile"
	sortDemoPlan(&plan)
	if demoPlanBinding(plan) != original {
		t.Fatal("source order/profile/revision changed execution identity")
	}
	for _, fault := range []string{"format", "parent", "digest", "namespace", "duplicate", "identifier", "submission", "kind", "profile", "origin", "snapshot", "alias", "parent-prefix", "invalid-id", "duplicate-key", "missing-plugin-origin", "plugin-namespace-alias", "draft-submission", "pending-release"} {
		t.Run(fault, func(t *testing.T) {
			changed, _ := configuredUnitReceipt(t)
			switch fault {
			case "format":
				changed.Bindings.Format = 2
			case "parent":
				delete(changed.Bindings.Parents, "publisher:demo")
				changed.Bindings.Parents["publisher:missing"] = demoHash("fake")
			case "digest":
				changed.Bindings.Inputs[changed.Items[0].Key] = "sha256:" + strings.Repeat("G", 64)
			case "namespace":
				changed.Bindings.Namespaces["../bad"] = demoTestUUID(999)
			case "duplicate":
				changed.Items[1].ResourceID = changed.Items[0].ResourceID
			case "identifier":
				changed.Items[0].Identifier = "broken"
			case "submission":
				changed.Items[0].SubmissionID = ""
			case "kind":
				changed.Items[0].Kind = "unknown"
			case "profile":
				changed.Bindings.LegacyProfile = "unknown"
			case "origin":
				changed.ArchiveOrigin = "https://external.example.com"
			case "snapshot":
				changed.SnapshotID = changed.PublisherID
			case "alias":
				changed.PublisherID = demoTestUUID(999)
			case "parent-prefix":
				delete(changed.Bindings.Parents, "publisher:demo")
				changed.Bindings.Parents["resource:demo"] = "sha256:" + strings.Repeat("a", 64)
			case "invalid-id":
				changed.Items[0].ListingRevisionID = "not-a-uuid"
			case "duplicate-key":
				changed.Items[1].Key = changed.Items[0].Key
			case "missing-plugin-origin":
				changed.ArchiveOrigin = ""
			case "plugin-namespace-alias":
				changed.PluginNamespaceID = changed.NamespaceID
			case "draft-submission":
				changed.Items[3].SubmissionID = demoTestUUID(3000)
			case "pending-release":
				changed.Items[4].ReleaseID = demoTestUUID(3000)
			}
			if err := validateDemoReceipt(changed, changed.BaseURL); err == nil || err.Error() != "seed-demo: invalid configured sample receipt" {
				t.Fatalf("configured receipt guard: %v", err)
			}
		})
	}
	for _, fault := range []string{"resource", "parent", "dataset", "removed", "kind", "state", "identifier"} {
		t.Run("changed-"+fault, func(t *testing.T) {
			r, p := configuredUnitReceipt(t)
			switch fault {
			case "resource":
				p.Resources[0].Digest = demoHash("edited")
			case "parent":
				p.Namespaces[0].DisplayName = "edited"
			case "dataset":
				p.Dataset = "another-dataset"
			case "removed":
				p.Resources = p.Resources[1:]
			case "kind":
				p.Resources[0].Resource.Kind = catalog.KindPlugin
			case "state":
				p.Resources[0].Resource.State = "draft"
			case "identifier":
				p.Resources[0].Resource.Identifier = "urn:air:example.com:demo:changed"
			}
			want := "seed-demo: prior sample missing or changed: documentation-agent"
			if fault == "parent" {
				want = "seed-demo: prior parent missing or changed"
			} else if fault == "dataset" {
				want = "seed-demo: dataset differs from receipt"
			}
			if n, err := checkDemoBindings(p, r); n != 0 || err == nil || err.Error() != want {
				t.Fatalf("retained input guard: count %d, error %v; want %q", n, err, want)
			}
		})
	}
}

func TestConfiguredPlanPublisherOrderingAndBinding(t *testing.T) {
	// Each publisher's domains must be canonicalized before the execution
	// digest is bound; sorting must not rewrite the caller's domain slices.
	domains := []string{"z.example.com", "a.example.com"}
	p := demodata.Plan{Dataset: "demo", Namespaces: []demodata.Namespace{{Key: "zulu"}, {Key: "alpha"}}, Publishers: []demodata.Publisher{
		{Key: "zulu", DisplayName: "Z", Domains: domains},
		{Key: "alpha", DisplayName: "A", Domains: []string{"b.example.com"}},
	}, Resources: []demodata.PreparedResource{
		{Resource: demodata.Resource{Key: "z-resource", Publisher: "zulu"}, Digest: "z-digest"},
		{Resource: demodata.Resource{Key: "a-resource", Publisher: "alpha"}, Digest: "a-digest"},
	}}
	sortDemoPlan(&p)
	if !slices.Equal(domains, []string{"z.example.com", "a.example.com"}) {
		t.Fatal("domain sorting mutated caller input")
	}
	if p.Namespaces[0].Key != "alpha" || p.Publishers[0].Key != "alpha" || p.Resources[0].Resource.Key != "a-resource" || !slices.Equal(p.Publishers[1].Domains, []string{"a.example.com", "z.example.com"}) {
		t.Fatal("plan is not in stable key/domain order")
	}
	// This literal independently fixes the execution identity's content and
	// ordering; revision/profile labels do not belong in the bound document.
	raw := `{"Dataset":"demo","Resources":{"a-resource":"a-digest","z-resource":"z-digest"},"Namespaces":[{"key":"alpha","displayName":""},{"key":"zulu","displayName":""}],"Publishers":[{"key":"alpha","displayName":"A","domains":["b.example.com"]},{"key":"zulu","displayName":"Z","domains":["a.example.com","z.example.com"]}]}`
	want := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(raw)))
	if got := demoPlanBinding(p); got != want {
		t.Fatalf("execution digest: got %s; want %s", got, want)
	}
	fixtures := configuredDemoFixtures(p)
	if len(fixtures) != 2 || fixtures[0].publisher != "alpha" || fixtures[1].publisher != "zulu" {
		t.Fatalf("publisher references lost: %+v", fixtures)
	}
	p.Publishers[1].Domains[0] = "changed.example.com"
	if demoPlanBinding(p) == want {
		t.Fatal("changed second publisher did not change execution identity")
	}
}

func TestConfiguredReportCountsAndReceipt(t *testing.T) {
	r, p := configuredUnitReceipt(t)
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var expected demoReceipt
	if err := json.Unmarshal(raw, &expected); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"created", "extended", "resumed", "verified"} {
		t.Run(status, func(t *testing.T) {
			got := configuredDemoReport(r, status)
			if got.Status != status || got.Published != 12 || got.Drafts != 1 || got.PendingReview != 1 || !reflect.DeepEqual(got.demoReceipt, expected) {
				t.Fatalf("report lost receipt/status/counts: %+v", got)
			}
		})
	}
	// A selected addition is counted without altering receipt-bound entries.
	p.Resources = append(p.Resources, demodata.PreparedResource{Resource: demodata.Resource{Key: "new-sample"}, Digest: "sha256:" + strings.Repeat("a", 64)})
	if n, err := checkDemoBindings(p, r); err != nil || n != 1 {
		t.Fatalf("addition count: %d, %v", n, err)
	}
}
func TestConfiguredKnownReceiptAdoption(t *testing.T) {
	for _, legacy := range []string{"minimal", "expanded"} {
		r := demoVerificationReceipt()
		want := 17
		if legacy == "expanded" {
			r = expandedUnitReceipt()
			want = 26
		}
		p, err := demodata.Load("", "curated", r.ArchiveOrigin)
		if err != nil {
			t.Fatal(err)
		}
		merged, err := retainedDemoPlan(p, r)
		if err != nil || len(merged.Resources) != want {
			t.Fatal(err, len(merged.Resources))
		}
		for _, f := range defaultDemoFixtures(legacy, r.ArchiveOrigin) {
			if !slices.ContainsFunc(merged.Resources, func(p demodata.PreparedResource) bool { return p.Resource.Key == f.Segment }) {
				t.Fatal("lost original")
			}
		}
		p.Dataset = "another-dataset"
		if _, err = retainedDemoPlan(p, r); err == nil {
			t.Fatal("foreign dataset adopted original")
		}
	}
}
func TestConfiguredSnapshotSkillDelivery(t *testing.T) {
	r, p := configuredUnitReceipt(t)
	snapshot := publication.Snapshot{}
	for _, item := range r.Items {
		if item.State == "published" {
			snapshot.ARD.Entries = append(snapshot.ARD.Entries, publication.Entry{Identifier: item.Identifier})
			snapshot.AICatalog.Entries = append(snapshot.AICatalog.Entries, publication.AICatalogEntry{Identifier: item.Identifier})
		}
	}
	if err := verifyConfiguredSnapshot(snapshot, r, p); err != nil {
		t.Fatal(err)
	}
	for _, document := range []string{"ARD", "AI Catalog", "input"} {
		t.Run("missing-"+document, func(t *testing.T) {
			s, plan := snapshot, p
			want := "seed-demo: snapshot delivery differs for sample: " + r.Items[0].Key
			switch document {
			case "ARD":
				s.ARD.Entries = s.ARD.Entries[1:]
			case "AI Catalog":
				s.AICatalog.Entries = s.AICatalog.Entries[1:]
			case "input":
				plan.Resources = nil
				want = "seed-demo: missing snapshot input"
			}
			if err := verifyConfiguredSnapshot(s, r, plan); err == nil || err.Error() != want {
				t.Fatalf("snapshot guard: %v; want %q", err, want)
			}
		})
	}
	// Package-only exclusion is a service contract, not an invented public archive URL.
	r, p = configuredUnitReceipt(t)
	p.Resources = nil
	index := slices.IndexFunc(r.Items, func(item demoItem) bool { return item.Kind == "skill" && item.State == "published" })
	if index < 0 {
		t.Fatal("missing published Skill control")
	}
	item := r.Items[index]
	r.Items = []demoItem{item}
	prepared := demodata.PreparedResource{Resource: demodata.Resource{Key: item.Key}}
	prepared.Resource.Listing.Visibility = "public"
	prepared.Representation.PackageData = []byte("archive")
	p.Resources = append(p.Resources, prepared)
	snapshot = publication.Snapshot{Exclusions: []publication.Exclusion{{ResourceID: item.ResourceID, Reasons: []string{"PUBLIC_DELIVERY_FORM_REQUIRED"}}}}
	if err := verifyConfiguredSnapshot(snapshot, r, p); err != nil {
		t.Fatal(err)
	}
	for _, fault := range []string{"missing", "resource", "reason", "ard", "ai"} {
		t.Run("package-only-"+fault, func(t *testing.T) {
			s := publication.Snapshot{Exclusions: []publication.Exclusion{{ResourceID: item.ResourceID, Reasons: []string{"PUBLIC_DELIVERY_FORM_REQUIRED"}}}}
			want := "seed-demo: missing package-only exclusion"
			switch fault {
			case "missing":
				s.Exclusions = nil
			case "resource":
				s.Exclusions[0].ResourceID = demoTestUUID(3000)
			case "reason":
				s.Exclusions[0].Reasons = []string{"UNRELATED_REASON"}
			case "ard":
				s.ARD.Entries = []publication.Entry{{Identifier: item.Identifier}}
				want = "seed-demo: snapshot delivery differs for sample: " + item.Key
			case "ai":
				s.AICatalog.Entries = []publication.AICatalogEntry{{Identifier: item.Identifier}}
				want = "seed-demo: snapshot delivery differs for sample: " + item.Key
			}
			if err := verifyConfiguredSnapshot(s, r, p); err == nil || err.Error() != want {
				t.Fatalf("package-only guard: %v; want %q", err, want)
			}
		})
	}
	snapshot.Exclusions = nil
	for _, visibility := range []catalog.Visibility{catalog.VisibilityPrivate, catalog.VisibilityTenant} {
		p.Resources[0].Resource.Listing.Visibility = visibility
		if err := verifyConfiguredSnapshot(snapshot, r, p); err != nil {
			t.Fatal("non-public Skill must not require public delivery exclusion", err)
		}
	}
}
func TestConfiguredNativeWireDigests(t *testing.T) {
	plan, err := demodata.Load("", "curated", "https://127.0.0.1:18443")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range configuredDemoFixtures(plan) {
		value, err := demoWireDigest(f)
		if err != nil || !demoDigestValid(value) {
			t.Fatal(f.Segment, err)
		}
		if f.Kind == "skill" || f.Kind == "plugin" {
			if value != f.bodyDigest {
				t.Fatal("archive digest changed")
			}
		}
		if f.Representation["data"] != nil {
			raw, err := json.Marshal(f.Representation["data"])
			if err != nil {
				t.Fatal(err)
			}
			if value != demoHash(f.Representation["data"]) || len(raw) == 0 {
				t.Fatal("JSON wire digest differed")
			}
		}
	}
	clone := configuredDemoFixtures(plan)
	if !reflect.DeepEqual(clone, configuredDemoFixtures(plan)) {
		t.Fatal("fixture construction mutated snapshot")
	}
}

func TestConfiguredRevalidationResponseBinding(t *testing.T) {
	revisionID, evidenceID := demoTestUUID(2000), demoTestUUID(2001)
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, fault := range []string{"", "revision", "evidence-id", "subject", "blocked", "tenant"} {
		t.Run(fault, func(t *testing.T) {
			revision := map[string]any{"id": revisionID, "tenantId": demoTenant, "evidenceSetId": evidenceID, "contentSetDigest": digest}
			evidence := map[string]any{"id": evidenceID, "tenantId": demoTenant, "subjectDigest": digest, "state": "complete"}
			switch fault {
			case "revision":
				revision["id"] = demoTestUUID(2222)
			case "evidence-id":
				evidence["id"] = demoTestUUID(2222)
			case "subject":
				evidence["subjectDigest"] = "sha256:" + strings.Repeat("b", 64)
			case "blocked":
				evidence["state"] = "blocked"
			case "tenant":
				evidence["tenantId"] = demoTestUUID(2222)
			}
			raw := demoUnitJSON(t, map[string]any{"revision": revision, "evidence": evidence})
			err := validateDemoStepResponse(demoStep{Path: "/api/v1/version-revisions/" + revisionID + ":validate", Body: []byte("{}"), Response: []byte(raw)})
			if (err == nil) != (fault == "") {
				t.Fatal("incorrect response/evidence binding", err)
			}
		})
	}
	if demoHash(make(chan int)) != "" {
		t.Fatal("unsupported identity input got a digest")
	}
	plan := demodata.DefaultPlan("minimal", "")
	plan.Resources[0].Representation.Data = []byte("bad JSON")
	if configuredDemoFixtures(plan) != nil {
		t.Fatal("invalid native input was silently dropped")
	}
}
