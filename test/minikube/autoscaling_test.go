//go:build minikube

package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestAutoscalingExternalResources(t *testing.T) {
	raw, e := json.Marshal(hpaObject())
	if e != nil {
		t.Fatal(e)
	}
	var hp struct {
		Spec struct {
			Target struct {
				Name       string `json:"name"`
				Kind       string `json:"kind"`
				APIVersion string `json:"apiVersion"`
			} `json:"scaleTargetRef"`
			Min     int `json:"minReplicas"`
			Max     int `json:"maxReplicas"`
			Metrics []struct {
				Resource struct {
					Name   string `json:"name"`
					Target struct {
						Type string `json:"type"`
						CPU  int    `json:"averageUtilization"`
					} `json:"target"`
				} `json:"resource"`
			} `json:"metrics"`
			Behavior struct {
				Up struct {
					Window   int `json:"stabilizationWindowSeconds"`
					Policies []struct {
						Type          string `json:"type"`
						Value         int    `json:"value"`
						PeriodSeconds int    `json:"periodSeconds"`
					} `json:"policies"`
				} `json:"scaleUp"`
				Down struct {
					Window   int `json:"stabilizationWindowSeconds"`
					Policies []struct {
						Type          string `json:"type"`
						Value         int    `json:"value"`
						PeriodSeconds int    `json:"periodSeconds"`
					} `json:"policies"`
				} `json:"scaleDown"`
			} `json:"behavior"`
		} `json:"spec"`
	}
	if json.Unmarshal(raw, &hp) != nil {
		t.Fatal("invalid HPA")
	}
	s := hp.Spec
	if s.Target.Name != "catalog-api" || s.Target.Kind != "Deployment" || s.Target.APIVersion != "apps/v1" || s.Min != 2 || s.Max != 3 || len(s.Metrics) != 1 || s.Metrics[0].Resource.Name != "cpu" || s.Metrics[0].Resource.Target.Type != "Utilization" || s.Metrics[0].Resource.Target.CPU != 50 {
		t.Fatal("HPA target or CPU bound changed")
	}
	if len(s.Behavior.Up.Policies) != 1 || s.Behavior.Up.Window != 0 || s.Behavior.Up.Policies[0].Type != "Pods" || s.Behavior.Up.Policies[0].Value != 1 || s.Behavior.Up.Policies[0].PeriodSeconds != 15 || len(s.Behavior.Down.Policies) != 1 || s.Behavior.Down.Window != 60 || s.Behavior.Down.Policies[0].Type != "Pods" || s.Behavior.Down.Policies[0].Value != 1 || s.Behavior.Down.Policies[0].PeriodSeconds != 60 {
		t.Fatal("HPA stabilization changed")
	}
	rows, e := metricsServerObjects("catalog:owned-metrics")
	if e != nil {
		t.Fatal(e)
	}
	kinds := map[string]int{}
	for _, r := range rows {
		kind, ok := r["kind"].(string)
		if !ok {
			t.Fatal("metrics object lacks its kind")
		}
		kinds[kind]++
		if r["kind"] != "Deployment" {
			continue
		}
		b, e := json.Marshal(r)
		if e != nil {
			t.Fatal(e)
		}
		var d struct {
			Spec struct {
				Replicas int `json:"replicas"`
				Template struct {
					Spec struct {
						Containers []struct {
							Image           string   `json:"image"`
							ImagePullPolicy string   `json:"imagePullPolicy"`
							Args            []string `json:"args"`
						} `json:"containers"`
					} `json:"spec"`
				} `json:"template"`
			} `json:"spec"`
		}
		if json.Unmarshal(b, &d) != nil || d.Spec.Replicas != 1 || len(d.Spec.Template.Spec.Containers) != 1 {
			t.Fatal("invalid metrics deployment")
		}
		c := d.Spec.Template.Spec.Containers[0]
		if c.Image != "catalog:owned-metrics" || c.ImagePullPolicy != "Never" || c.Args[len(c.Args)-1] != "--kubelet-insecure-tls" {
			t.Fatal("external metrics pin or local-only kubelet setting changed")
		}
	}
	if kinds["Deployment"] != 1 || kinds["APIService"] != 1 || kinds["ClusterRole"] != 2 || kinds["ClusterRoleBinding"] != 2 || kinds["RoleBinding"] != 1 || kinds["ServiceAccount"] != 1 || kinds["Service"] != 1 {
		t.Fatal("unexpected metrics resources")
	}
	h := &harness{temporary: t.TempDir(), application: "catalog:fixture", options: options{scenario: "autoscaling"}}
	path, e := h.writeHelmValues("hpa", "catalog_api")
	if e != nil {
		t.Fatal(e)
	}
	values, e := os.ReadFile(filepath.Clean(path))
	if e != nil {
		t.Fatal(e)
	}
	var v struct {
		API struct {
			Replicas  int `json:"replicas"`
			Resources struct {
				Requests struct {
					CPU string `json:"cpu"`
				} `json:"requests"`
			} `json:"resources"`
		} `json:"api"`
		Worker struct {
			Replicas  int `json:"replicas"`
			Resources struct {
				Requests struct {
					CPU string `json:"cpu"`
				} `json:"requests"`
			} `json:"resources"`
		} `json:"worker"`
		UI struct {
			Enabled bool `json:"enabled"`
		} `json:"ui"`
	}
	if json.Unmarshal(values, &v) != nil || v.API.Replicas != 2 || v.Worker.Replicas != 2 || v.API.Resources.Requests.CPU != "50m" || v.Worker.Resources.Requests.CPU != "25m" || v.UI.Enabled {
		t.Fatal("selected replica, CPU or UI scope changed")
	}
}

func TestAutoscalingRejectsUnavailableStaleAndFutureMetrics(t *testing.T) {
	encode := func(timestamp time.Time, cpu string) []byte {
		b, e := json.Marshal(map[string]any{"items": []any{map[string]any{"metadata": map[string]string{"name": "catalog-api-first"}, "timestamp": timestamp, "containers": []any{map[string]any{"usage": map[string]string{"cpu": cpu}}}}, map[string]any{"metadata": map[string]string{"name": "catalog-api-second"}, "timestamp": timestamp, "containers": []any{map[string]any{"usage": map[string]string{"cpu": cpu}}}}}})
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	if !usableHPAMetrics(encode(time.Now().Add(-time.Second), "1000n")) {
		t.Fatal("fresh metrics rejected")
	}
	for _, raw := range [][]byte{nil, []byte(`{}`), encode(time.Now().Add(-2*time.Minute), "1000n"), encode(time.Now().Add(time.Minute), "1000n"), encode(time.Now().Add(-time.Second), ""), encode(time.Now().Add(-time.Second), "1mu"), encode(time.Now().Add(-time.Second), "NaN"), encode(time.Now().Add(-time.Second), "-1m")} {
		if usableHPAMetrics(raw) {
			t.Fatal("unusable metrics accepted")
		}
	}
}

func TestAutoscalingSearchKeepsContentExactAcrossGenerations(t *testing.T) {
	page := catalog.CatalogSearchResponse{Generation: "1", DurableTotal: 3}
	for _, id := range []string{"a", "b", "c"} {
		page.Items = append(page.Items, catalog.CatalogSearchResult{ID: id, Local: new((catalog.CatalogRecord{Resource: catalog.Resource{TenantID: primaryTenantID}, Evidence: catalog.EvidenceSet{LegalRights: catalog.LegalRightsSummary{State: catalog.LegalRightsNotAssessed}}}).Summary().SearchLocal())})
	}
	encode := func() httpResult {
		raw, e := json.Marshal(page)
		if e != nil {
			t.Fatal(e)
		}
		return httpResult{Status: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: raw}
	}
	first, e := hpaSearchDigest(encode())
	if e != nil {
		t.Fatal(e)
	}
	page.Generation = "2"
	second, e := hpaSearchDigest(encode())
	if e != nil || first != second {
		t.Fatal("generation-only change rejected")
	}
	page.Profile = "changed"
	changed, e := hpaSearchDigest(encode())
	if e != nil || first == changed {
		t.Fatal("changed result metadata hidden")
	}
	page.Items[0].Local.Resource.TenantID = otherTenantID
	if _, e := hpaSearchDigest(encode()); e == nil {
		t.Fatal("cross-tenant result accepted")
	}
	page.Items[0].Local.Resource.TenantID = primaryTenantID
	page.Generation = ""
	if _, e := hpaSearchDigest(encode()); e == nil {
		t.Fatal("missing generation accepted")
	}
}

func TestAutoscalingMetricsDenialRejectsToolFailures(t *testing.T) {
	if !hpaMetricsDenied(errors.New("kubectl: exit status 1\nno\n")) {
		t.Fatal("explicit denial rejected")
	}
	for _, e := range []error{nil, errors.New("kubectl: connection refused"), errors.New("kubectl: exit status 1\nyes"), errors.New("kubectl: exit status 1\nno\ntransport failed")} {
		if hpaMetricsDenied(e) {
			t.Fatal("tool failure or permission accepted as denial")
		}
	}
}

func TestAutoscalingSyncUsesTheImplementedAcceptedEnvelope(t *testing.T) {
	id := "00000000-0000-7000-8000-000000000003"
	r := httpResult{Status: 202, Body: []byte(`{"job":{"id":"` + id + `"},"existing":false}`)}
	if got, e := hpaAcceptedJob(r); e != nil || got != id {
		t.Fatal("current sync envelope rejected")
	}
	for _, body := range []string{`{"jobId":"` + id + `"}`, `{"job":{"id":"invalid"}}`, `{"job":{"id":"` + id + `"},"existing":true}`, `{`} {
		r.Body = []byte(body)
		if _, e := hpaAcceptedJob(r); e == nil {
			t.Fatal("invalid or previously accepted Job used as new progress")
		}
	}
}

func TestMinikubePreloadUsesTheActualPrivateCache(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "preload.tar.lz4")
	if err := os.WriteFile(archive, []byte("fixture archive"), 0600); err != nil {
		t.Fatal(err)
	}
	h := &harness{minikubeHome: filepath.Join(dir, "private-home"), options: options{preloadArchive: archive}}
	if err := h.preparePreload(); err != nil {
		t.Fatal(err)
	}
	copied := filepath.Join(h.minikubeHome, ".minikube", "cache", "preloaded-tarball", filepath.Base(archive))
	raw, err := os.ReadFile(filepath.Clean(copied))
	if err != nil || string(raw) != "fixture archive" {
		t.Fatal("archive not copied to Minikube's private cache")
	}
	info, err := os.Stat(copied)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("private archive permission changed")
	}
	if _, err := os.Stat(filepath.Join(h.minikubeHome, "cache")); !os.IsNotExist(err) {
		t.Fatal("obsolete cache path used")
	}
}

func TestAutoscalingDiagnosticsRetainNamesWithoutValues(t *testing.T) {
	got := hpaChangedFields([]byte(`{"generation":"old","items":["private-old"]}`), []byte(`{"generation":"new","items":["private-new"],"profile":"sensitive"}`))
	if got != "[items profile]" {
		t.Fatalf("unexpected field-only diagnostic: %s", got)
	}
}

func TestHPAProblemEvidenceIsFiniteAndDoesNotRetainPrivateText(t *testing.T) {
	for _, code := range []string{"INVALID_ARGUMENT", "FORBIDDEN", "CONFLICT", "DEPENDENCY_UNAVAILABLE"} {
		body, err := json.Marshal(map[string]string{"code": code, "detail": "private-token", "tenant": "private-tenant"})
		if err != nil {
			t.Fatal(err)
		}
		if got := hpaProblemCode(httpResult{Status: 403, Body: body}); got != code {
			t.Fatalf("code: %s", got)
		}
	}
	for _, body := range []string{`{"code":"private-token"}`, `{"code":[]}`, `invalid`, `{}`} {
		if got := hpaProblemCode(httpResult{Body: []byte(body)}); got != "unclassified" {
			t.Fatalf("unreviewed evidence: %s", got)
		}
	}
	load := &hpaLoad{value: hpaLoadReport{Failures: []hpaFailure{{Code: "FORBIDDEN"}}}}
	load.setPhase("scaled-up-integrity")
	saved := load.report()
	saved.Failures[0].Code = "mutated"
	if load.report().Failures[0].Code != "FORBIDDEN" {
		t.Fatal("report aliases live evidence")
	}
}

func TestHPAProblemClassRejectsArbitraryDetails(t *testing.T) {
	for _, test := range []struct {
		status             int
		code, detail, want string
	}{
		{400, "INVALID_ARGUMENT", "collection authorization changed; restart the read", "collection-authorization-revision"},
		{403, "FORBIDDEN", "federation admission failed", "federation-admission"},
		{400, "FORBIDDEN", "federation admission failed", "unclassified"},
		{403, "FORBIDDEN", "private-token", "unclassified"},
	} {
		body, err := json.Marshal(map[string]string{"code": test.code, "detail": test.detail})
		if err != nil {
			t.Fatal(err)
		}
		if got := hpaProblemClass(httpResult{Status: test.status, Body: body}); got != test.want {
			t.Fatalf("got %s want %s", got, test.want)
		}
	}
}

func TestHPAAuthorityChangesBracketStablePolicyLoad(t *testing.T) {
	for _, stage := range []string{"scaled-up", "metrics-unavailable"} {
		if hpaChangesAuthority(stage) {
			t.Fatal("load phase mutates authorization policy", stage)
		}
	}
	for _, stage := range []string{"initial", "recovered"} {
		if !hpaChangesAuthority(stage) {
			t.Fatal("authority transition checks omitted", stage)
		}
	}
	if got := hpaChangedFields([]byte(`{"private-token":1}`), []byte(`{}`)); got != "unknown-fields" {
		t.Fatal("arbitrary field entered diagnostics", got)
	}
}
