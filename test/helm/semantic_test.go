//go:build helm

// Optional inference inputs belong to runtime roles, never migrations or UI.
package helm_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestSemanticConfigurationAndSecretScope(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		args := []string{"--values", filepath.Join(chartPath(t), "ci/semantic-values.yaml"), "--values", filepath.Join(chartPath(t), "ci/ui-values.yaml")}
		if !enabled {
			args = append(args, "--set", "semanticSearch.enabled=false")
		}
		objects := renderChart(t, args...)
		var configuration struct {
			Enabled  bool   `json:"enabled"`
			Endpoint string `json:"endpoint"`
			Secret   string `json:"existingSecret"`
		}
		if err := json.Unmarshal([]byte(stringAt(t, oneObject(t, objects, "ConfigMap", "configuration"), "data", "CATALOG_SEMANTIC_SEARCH")), &configuration); err != nil || configuration.Enabled != enabled || configuration.Secret != "" || (configuration.Endpoint != "") != enabled {
			t.Fatalf("invalid runtime provider projection: %+v %v", configuration, err)
		}
		for _, role := range []string{"api", "worker", "ui", "migration"} {
			kind := "Deployment"
			if role == "migration" {
				kind = "Job"
			}
			object := oneObject(t, objects, kind, role)
			raw, err := json.Marshal(object)
			if err != nil {
				t.Fatal(err)
			}
			want := enabled && (role == "api" || role == "worker")
			if strings.Contains(string(raw), `"secretName":"catalog-embeddings"`) != want || strings.Contains(string(raw), `"mountPath":"/etc/open-agentic-catalog/embeddings"`) != want {
				t.Fatalf("provider Secret scope drift for %s enabled=%v", role, enabled)
			}
			if role == "migration" && strings.Contains(string(raw), `"--semantic"`) != enabled {
				t.Fatal("optional migration selection drift")
			}
		}
	}
}
