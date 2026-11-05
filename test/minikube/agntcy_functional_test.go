//go:build minikube && functional

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"go.yaml.in/yaml/v3"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestAGNTCYInstalledChart qualifies the exact optional archive with real Helm, without a cluster.
func TestAGNTCYInstalledChart(t *testing.T) {
	inputs := directoryTestInputs(t)
	chart, err := filepath.Abs(filepath.Join("../../.tools/agntcy", inputs.Release, "dir-"+inputs.Release+".tgz"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(chart); os.IsNotExist(err) {
		t.Skip("run make agntcy-tools-install for the real Directory chart contract")
	}
	raw, err := json.Marshal(agntcyValues(agntcyRuntimeInputs(directoryTestInputs(t), "chart-contract"), []string{"synthetic-db", "synthetic-oci", "synthetic-sync"}, "synthetic-hash"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "values.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	result, err := exec.CommandContext(t.Context(), "helm", "template", "directory", chart, "--namespace", agntcyNamespace, "--no-hooks", "--values", path).CombinedOutput() // #nosec G204 -- fixed Helm executable and invocation-owned synthetic values; no shell.
	if err != nil {
		t.Fatalf("render: %v: %s", err, result)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(result))
	found, credentials, configs := false, false, 0
	for {
		var object struct {
			Kind string            `yaml:"kind"`
			Data map[string]string `yaml:"data"`
		}
		if err := decoder.Decode(&object); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if raw, ok := object.Data["htpasswd"]; object.Kind == "Secret" && ok {
			value, err := base64.StdEncoding.DecodeString(raw)
			if err != nil || string(value) != "synthetic-hash" {
				t.Fatal("native OCI password differs from fixture")
			}
			credentials = true
		}
		if raw, ok := object.Data["config.json"]; object.Kind == "ConfigMap" && ok {
			var config map[string]any
			if err := json.Unmarshal([]byte(raw), &config); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"search", "trust"} {
				if directoryMap(t, directoryMap(t, config["extensions"])[name])["enable"] != false {
					t.Fatal("Zot public updater remains enabled")
				}
			}
			found = true
		}
		for _, name := range []string{"server.config.yml", "reconciler.config.yml"} {
			if raw, ok := object.Data[name]; object.Kind == "ConfigMap" && ok {
				var config map[string]any
				if err := yaml.Unmarshal([]byte(raw), &config); err != nil {
					t.Fatal(err)
				}
				policy := directoryMap(t, config["policy"])
				operations, ok := policy["operations"].([]any)
				if !ok || len(operations) != 1 || directoryMap(t, operations[0])["schema_url"] != "http://schema-router.agntcy-qualification.svc.cluster.local:8080" {
					t.Fatal("rendered native validation does not use the owned schema", name)
				}
				if name == "reconciler.config.yml" {
					for _, task := range []string{"regsync", "identity", "signature", "scan", "metrics", "prune", "sync", "publish", "policy_evaluation"} {
						if directoryMap(t, config[task])["enabled"] != false {
							t.Fatal("rendered reconciler enables an unselected task", task)
						}
					}
				}
				configs++
			}
		}
	}
	if !found || !credentials || configs != 2 {
		t.Fatal("Zot configuration absent from rendered chart")
	}
	if err = validateAGNTCYRender(result, agntcyRuntimeInputs(directoryTestInputs(t), "chart-contract")); err != nil {
		t.Fatalf("%v\n%s", err, result)
	}
}
