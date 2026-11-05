// Demo creation tests verify orchestration and partial receipts using static
// transport responses without creating records or publishing a live snapshot.
package main

import (
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// demoCreationResponses binds parent creation, each fixture and final publication
// to distinct fixed IDs while reusing the item-level request expectations.
func demoCreationResponses(t *testing.T, r demoReceipt) ([]smokeClientStep, []map[string]any) {
	t.Helper()
	steps := []smokeClientStep{
		{"namespace", "POST", "/api/v1/namespaces", "local-admin", demoID + "-namespace", "", 201, nil, demoUnitJSON(t, map[string]any{"id": r.NamespaceID})},
		{"publisher", "POST", "/api/v1/publishers", "local-admin", demoID + "-publisher", "", 201, http.Header{"Etag": {`"publisher-created"`}}, demoUnitJSON(t, map[string]any{"id": r.PublisherID})},
		{"domain", "POST", "/api/v1/publishers/" + r.PublisherID + "/publication-domains/" + demoDomain + ":approve", "local-admin", demoID + "-domain", `"publisher-created"`, 200, nil, demoUnitJSON(t, map[string]any{"id": r.PublisherID})},
	}
	fields := []map[string]any{
		{"slug": demoID, "displayName": "Catalog demo"},
		{"displayName": "Demo Publisher", "domains": []string{demoDomain}},
		{"reason": "Approve the local demo publication domain"},
	}
	for i, f := range demoFixtures() {
		itemSteps, itemFields := demoItemCreationResponses(t, f, r, r.Items[i])
		for _, step := range itemSteps {
			step.name = f.Segment + " " + step.name
			steps = append(steps, step)
		}
		fields = append(fields, itemFields...)
	}
	steps = append(steps,
		smokeClientStep{"snapshot", "POST", "/api/v1/publication/snapshots", "local-publisher", demoID + "-snapshot", "", 201, http.Header{"Etag": {`"snapshot-candidate"`}}, demoUnitJSON(t, map[string]any{"id": r.SnapshotID})},
		smokeClientStep{"activation", "POST", "/api/v1/publication/snapshots/" + r.SnapshotID + ":activate", "local-publisher", demoID + "-activate", `"snapshot-candidate"`, 200, nil, demoUnitJSON(t, map[string]any{"id": r.SnapshotID})},
	)
	fields = append(fields,
		map[string]any{"host": "catalog.invalid"},
		map[string]any{"host": "catalog.invalid", "reason": "Publish the three reviewed demo resources"},
	)
	return steps, fields
}

// TestCreateDemoRetainsOnlyCompletedWork verifies exact receipt prefixes, fixture
// attribution and failure stops before any later item or snapshot command.
func TestCreateDemoRetainsOnlyCompletedWork(t *testing.T) {
	t.Parallel()
	r := demoVerificationReceipt()
	baseline, fields := demoCreationResponses(t, r)
	empty := demoReceipt{Dataset: demoID, BaseURL: r.BaseURL, TenantID: demoTenant}
	namespace := empty
	namespace.NamespaceID = r.NamespaceID
	parents := namespace
	parents.PublisherID = r.PublisherID
	items := r
	items.SnapshotID = ""
	type checkpoint struct {
		name, fixture string
		want          demoReceipt
	}
	cases := make([]checkpoint, 0, 6+2*len(r.Items))
	cases = append(cases,
		checkpoint{"namespace", "", empty},
		checkpoint{"publisher", "", namespace},
		checkpoint{"domain", "", parents},
		checkpoint{"snapshot", "", items},
		checkpoint{"activation", "", r},
		checkpoint{"", "", r},
	)
	for i, item := range r.Items {
		want := parents
		want.Items = append([]demoItem(nil), r.Items[:i]...)
		last := "release"
		if item.State == "draft" {
			last = "listing revision"
		} else if item.State == "pending_review" {
			last = "submission"
		}
		// A late failure must not add a partially created item to the receipt.
		for _, phase := range []string{"resource", last} {
			cases = append(cases, checkpoint{item.Key + " " + phase, item.Key, want})
		}
	}
	for _, tc := range cases {
		faults := []string{"transport", "JSON", "status"}
		if tc.name == "" {
			faults = []string{"success"}
		}
		for _, fault := range faults {
			t.Run(strings.TrimSpace(tc.name+" "+fault), func(t *testing.T) {
				steps := append([]smokeClientStep(nil), baseline...)
				wantError := ""
				if tc.name != "" {
					index := slices.IndexFunc(steps, func(step smokeClientStep) bool { return step.name == tc.name })
					if index < 0 {
						t.Fatal("failure has no matching command")
					}
					steps = steps[:index+1]
					switch fault {
					case "transport":
						wantError = "request failed or deadline exceeded"
					case "JSON":
						steps[index].body, wantError = `[`, "invalid API JSON response"
					case "status":
						wantError = fmt.Sprintf("HTTP 503, expected %d", steps[index].status)
						steps[index].status, steps[index].body = 503, "private-demo-response-body"
					}
				}
				got, err := createDemo(t.Context(), demoUnitClient(t, steps, fault == "transport", fields[:len(steps)]), "catalog.invalid")
				if !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("receipt=%#v, want %#v", got, tc.want)
				}
				if tc.name == "" {
					if err != nil {
						t.Fatal(err)
					}
				} else if err == nil || !strings.Contains(err.Error(), wantError) || strings.Contains(err.Error(), "private-demo") || tc.fixture != "" && !strings.HasPrefix(err.Error(), tc.fixture+": ") {
					t.Fatalf("rejection=%v, want %q attributed to %q without private details", err, wantError, tc.fixture)
				}
			})
		}
	}
}
