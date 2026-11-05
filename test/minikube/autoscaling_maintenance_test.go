//go:build minikube

package main

import (
	"encoding/json"
	"testing"
)

func TestMaintenanceHPAControlRequiresExactIdentityAndCondition(t *testing.T) {
	good := []byte(`{"metadata":{"name":"owned","uid":"identity"},"spec":{"minReplicas":2},"status":{"conditions":[{"type":"ScalingActive","status":"False"}]}}`)
	state, ready := decodeHPAControl(good, "owned", "False")
	if !ready || state.Metadata.UID != "identity" {
		t.Fatal("inactive HPA control rejected")
	}
	if _, ready := decodeHPAControl(good, "owned", "True"); ready {
		t.Fatal("inactive HPA accepted as active")
	}
	for _, raw := range []string{`{}`, `{"metadata":{"name":"owned"},"spec":{},"status":{"conditions":[{"type":"ScalingActive","status":"False"}]}}`, `{"metadata":{"name":"other","uid":"id"},"spec":{},"status":{"conditions":[{"type":"ScalingActive","status":"False"}]}}`, `{"metadata":{"name":"owned","uid":"id"},"spec":{},"status":{"conditions":[{"type":"AbleToScale","status":"False"}]}}`, `{`} {
		if _, ready := decodeHPAControl([]byte(raw), "owned", "False"); ready {
			t.Fatal("missing or mismatched control accepted")
		}
	}
}

func TestMaintenanceRuntimeReadinessRequiresDistinctCurrentProcesses(t *testing.T) {
	ready := []podIdentity{
		{UID: "first", Ready: true, ImageID: "current", ServiceAccount: "catalog-api"},
		{UID: "second", Ready: true, ImageID: "current", ServiceAccount: "catalog-api"},
	}
	if !maintenanceRuntimeReady(ready, "api", "current") {
		t.Fatal("two ready current API processes rejected")
	}
	for _, change := range []func([]podIdentity) []podIdentity{
		func(v []podIdentity) []podIdentity { return nil },
		func(v []podIdentity) []podIdentity { return v[:1] },
		func(v []podIdentity) []podIdentity { return append(v, v[0]) },
		func(v []podIdentity) []podIdentity { v[1].Ready = false; return v },
		func(v []podIdentity) []podIdentity { v[1].UID = v[0].UID; return v },
		func(v []podIdentity) []podIdentity { v[1].UID = ""; return v },
		func(v []podIdentity) []podIdentity { v[1].Restarts = 1; return v },
		func(v []podIdentity) []podIdentity { v[1].ImageID = "old"; return v },
		func(v []podIdentity) []podIdentity { v[1].ServiceAccount = "catalog-worker"; return v },
	} {
		if maintenanceRuntimeReady(change(append([]podIdentity(nil), ready...)), "api", "current") {
			t.Fatal("incomplete or incorrect runtime inventory accepted")
		}
	}
	if maintenanceRuntimeReady(ready, "api", "") {
		t.Fatal("missing build identity accepted")
	}
	for i := range ready {
		ready[i].ServiceAccount = "catalog-worker"
	}
	if !maintenanceRuntimeReady(ready, "worker", "current") {
		t.Fatal("two ready current worker processes rejected")
	}
}

func TestMaintenanceAuxiliaryHPAsKeepTheSelectedTargetAndFixedBounds(t *testing.T) {
	for _, test := range []struct {
		name, target string
		replicas     int
		missing      bool
	}{{neighborHPA, "decoy-fixture", 1, false}, {inactiveWorkerHPA, "catalog-worker", 2, true}} {
		raw, e := json.Marshal(auxiliaryHPA(test.name, test.target, test.replicas, test.missing))
		if e != nil {
			t.Fatal(e)
		}
		var v struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Spec struct {
				Target struct {
					APIVersion string `json:"apiVersion"`
					Kind       string `json:"kind"`
					Name       string `json:"name"`
				} `json:"scaleTargetRef"`
				Min     int `json:"minReplicas"`
				Max     int `json:"maxReplicas"`
				Metrics []struct {
					Type string `json:"type"`
					Pods struct {
						Metric struct {
							Name string `json:"name"`
						} `json:"metric"`
						Target struct {
							Type  string `json:"type"`
							Value string `json:"averageValue"`
						} `json:"target"`
					} `json:"pods"`
				} `json:"metrics"`
			} `json:"spec"`
		}
		if json.Unmarshal(raw, &v) != nil || v.Metadata.Name != test.name || v.Metadata.Namespace != namespace || v.Spec.Target.Name != test.target || v.Spec.Target.Kind != "Deployment" || v.Spec.Target.APIVersion != "apps/v1" || v.Spec.Min != test.replicas || v.Spec.Max != test.replicas || len(v.Spec.Metrics) != 1 {
			t.Fatal("HPA target or fixed bounds changed")
		}
		m := v.Spec.Metrics[0]
		if test.missing {
			if m.Type != "Pods" || m.Pods.Metric.Name != "catalog_test_missing_metric" || m.Pods.Target.Type != "AverageValue" || m.Pods.Target.Value != "1" {
				t.Fatal("inactive control no longer requires the absent custom metric")
			}
		} else if m.Type != "Resource" {
			t.Fatal("neighbor no longer uses the real metrics provider")
		}
	}
}
