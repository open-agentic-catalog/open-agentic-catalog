package main

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io/fs"
	"reflect"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/demodata"
	"github.com/open-agentic-catalog/open-agentic-catalog/samples"
	"go.yaml.in/yaml/v3"
)

// Retention units prepare valid inert plans without invoking the sample loader.
type retainedManifestAssets struct {
	fs.FS
	manifest fstest.MapFS
}

func (a retainedManifestAssets) Open(name string) (fs.File, error) {
	if name == "catalog.yaml" {
		return a.manifest.Open(name)
	}
	return a.FS.Open(name)
}

// retainedManifest reads a fresh document so each refusal changes one valid input.
func retainedManifest(t *testing.T) map[string]any {
	t.Helper()
	raw, err := fs.ReadFile(samples.Files, "catalog.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := yaml.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func retainedManifestFirst(t *testing.T, manifest map[string]any, collection string) map[string]any {
	t.Helper()
	items, ok := manifest[collection].([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("fixture must contain a nonempty %s collection", collection)
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("first %s entry must be an object", collection)
	}
	return item
}

// retainedPreparedPlan uses the public decoder to validate and hash the fixture.
func retainedPreparedPlan(t *testing.T, value map[string]any, profile, origin string) demodata.Plan {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	assets := retainedManifestAssets{FS: samples.Files, manifest: fstest.MapFS{"catalog.yaml": &fstest.MapFile{Data: raw}}}
	plan, err := demodata.Decode(assets, "catalog.yaml", profile, origin)
	if err != nil {
		t.Fatal("unit fixture is not a valid prepared plan", err)
	}
	return plan
}

func TestRetainedDemoPlanRejectsChangedBoundInputs(t *testing.T) {
	for _, profile := range []string{"minimal", "expanded", "bound expanded"} {
		t.Run(profile, func(t *testing.T) {
			receipt := demoVerificationReceipt()
			selected := profile
			if profile == "expanded" {
				receipt = expandedUnitReceipt()
			}
			if profile == "bound expanded" {
				receipt, _ = configuredUnitReceipt(t)
				receipt.Bindings.LegacyProfile = "expanded"
				selected = "expanded"
			}
			if err := validateDemoReceipt(receipt, receipt.BaseURL); err != nil {
				t.Fatal("invalid receipt control", err)
			}
			control := retainedPreparedPlan(t, retainedManifest(t), selected, receipt.ArchiveOrigin)
			if _, err := retainedDemoPlan(control, receipt); err != nil {
				t.Fatal("unchanged prepared plan refused", err)
			}
			for _, field := range []string{"resource", "namespace", "publisher"} {
				t.Run(field, func(t *testing.T) {
					manifest := retainedManifest(t)
					var expected string
					switch field {
					case "resource":
						retainedManifestFirst(t, manifest, "resources")["description"] = "A reviewed change to the retained sample."
						expected = "seed-demo: prior sample changed: order-agent"
					case "namespace":
						retainedManifestFirst(t, manifest, "namespaces")["displayName"] = "Changed retained namespace"
						expected = "seed-demo: prior namespace changed: catalog-demo-v1"
					case "publisher":
						retainedManifestFirst(t, manifest, "publishers")["displayName"] = "Changed retained publisher"
						expected = "seed-demo: prior publisher changed: demo"
					}
					changed := retainedPreparedPlan(t, manifest, selected, receipt.ArchiveOrigin)
					if _, err := retainedDemoPlan(changed, receipt); err == nil || err.Error() != expected {
						t.Fatalf("changed retained input: got %v, want %q", err, expected)
					}
				})
			}
		})
	}
}

// retainedSelectedManifest describes only new valid resources and their parents.
func retainedSelectedManifest(t *testing.T, count int) map[string]any {
	t.Helper()
	manifest := retainedManifest(t)
	template, err := json.Marshal(retainedManifestFirst(t, manifest, "resources"))
	if err != nil {
		t.Fatal(err)
	}
	resources := make([]any, count)
	keys := make([]string, count)
	for i := range count {
		var resource map[string]any
		if err := json.Unmarshal(template, &resource); err != nil {
			t.Fatal(err)
		}
		key := fmt.Sprintf("selected-%02d", i)
		resource["key"], resource["identifier"], resource["name"] = key, "urn:air:selected.catalog.example.com:demo:"+key, "Selected sample "+key
		resource["namespace"], resource["publisher"] = "selected", "selected"
		resources[i], keys[i] = resource, key
	}
	manifest["resources"], manifest["profiles"] = resources, map[string]any{"selected": keys}
	manifest["namespaces"] = []any{map[string]any{"key": "selected", "displayName": "Selected namespace"}}
	manifest["publishers"] = []any{map[string]any{"key": "selected", "displayName": "Selected publisher", "domains": []string{"selected.catalog.example.com"}}}
	return manifest
}

// Snapshot before merging so mutations of shared nested values cannot alter the
// expectation. Include exact raw data and archive bytes outside their special
// JSON representations on PreparedResource.
func retainedResourceSnapshot(t *testing.T, resource demodata.PreparedResource) string {
	t.Helper()
	raw, err := json.Marshal(struct {
		Prepared demodata.PreparedResource `json:"prepared"`
		Archive  []byte                    `json:"archive"`
		Data     []byte                    `json:"data"`
	}{Prepared: resource, Archive: resource.Archive, Data: resource.Representation.Data})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestRetainedDemoPlanPreservesOmittedParentsAndExactResourceCeiling(t *testing.T) {
	receipt := demoVerificationReceipt()
	retained := demodata.DefaultPlan("minimal", receipt.ArchiveOrigin)
	for _, total := range []int{demodata.MaxResources, demodata.MaxResources + 1} {
		t.Run(fmt.Sprintf("merged-%d", total), func(t *testing.T) {
			selectedCount := total - len(retained.Resources)
			plan := retainedPreparedPlan(t, retainedSelectedManifest(t, selectedCount), "selected", receipt.ArchiveOrigin)
			if len(plan.Resources) != selectedCount {
				t.Fatal("selected plan fixture lost resources")
			}
			wantResources := make(map[string]string, total)
			for _, resources := range [][]demodata.PreparedResource{retained.Resources, plan.Resources} {
				for _, resource := range resources {
					key := resource.Resource.Key
					if _, exists := wantResources[key]; exists {
						t.Fatal("fixture must select distinct new and retained resources", key)
					}
					wantResources[key] = retainedResourceSnapshot(t, resource)
				}
			}
			merged, err := retainedDemoPlan(plan, receipt)
			if total > demodata.MaxResources {
				if err == nil || err.Error() != "seed-demo: retained plus selected sample bound exceeded" {
					t.Fatal("oversized retained plus selected plan admitted", err)
				}
				return
			}
			if err != nil || len(merged.Resources) != total {
				t.Fatal("exact resource ceiling refused", len(merged.Resources), err)
			}
			for i, resource := range merged.Resources {
				key := resource.Resource.Key
				want, ok := wantResources[key]
				if !ok || retainedResourceSnapshot(t, resource) != want {
					t.Fatal("adoption changed, duplicated or substituted a selected or retained resource", key)
				}
				delete(wantResources, key)
				if i > 0 && merged.Resources[i-1].Resource.Key >= key {
					t.Fatal("adopted resources are not ordered by key", key)
				}
			}
			if len(wantResources) != 0 {
				t.Fatal("adoption omitted selected or retained resources")
			}
			wantNamespaces := append(slices.Clone(retained.Namespaces), demodata.Namespace{Key: "selected", DisplayName: "Selected namespace"})
			wantPublishers := append(slices.Clone(retained.Publishers), demodata.Publisher{Key: "selected", DisplayName: "Selected publisher", Domains: []string{"selected.catalog.example.com"}})
			slices.SortFunc(wantNamespaces, func(a, b demodata.Namespace) int { return cmp.Compare(a.Key, b.Key) })
			slices.SortFunc(wantPublishers, func(a, b demodata.Publisher) int { return cmp.Compare(a.Key, b.Key) })
			if !reflect.DeepEqual(merged.Namespaces, wantNamespaces) || !reflect.DeepEqual(merged.Publishers, wantPublishers) {
				t.Fatal("adoption omitted or changed retained and selected parents")
			}
		})
	}
}
