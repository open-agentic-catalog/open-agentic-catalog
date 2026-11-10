//go:build minikube && functional

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

func TestInstalledVaultChartsPreserveSelectedScope(t *testing.T) {
	pins, err := readVaultPins()
	if err != nil {
		t.Fatal(err)
	}
	images := map[string]string{"vault": "open-agentic-catalog:vault-test", "vso": "open-agentic-catalog:vso-test", "proxy": "open-agentic-catalog:proxy-test"}
	for name, pin := range pins.Charts {
		t.Run(name, func(t *testing.T) {
			chart := filepath.Join("../../.tools/minikube-vault", pin.File)
			raw, err := os.ReadFile(chart) // #nosec G304 -- Embedded inventory; verify before Helm executes.
			if os.IsNotExist(err) {
				t.Skip("run make vault-tools-install for external-chart contracts")
			}
			if err != nil || fmt.Sprintf("%x", sha256.Sum256(raw)) != pin.SHA256 {
				t.Fatal("changed external chart")
			}
			values := vaultOperatorValues(images)
			if name == "vault" {
				values = vaultServerValues(images["vault"])
			}
			raw, err = json.Marshal(values)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "values.json")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			raw, err = exec.CommandContext(ctx, "helm", "template", vaultRelease, chart, "--namespace", vaultNamespace, "--kube-version", "1.37.1", "--include-crds", "-f", path).CombinedOutput() // #nosec G204 -- Fixed Helm and checksum-verified charts.
			if err != nil {
				t.Fatalf("native external-chart rendering: %v: %s", err, raw)
			}
			text := string(raw)
			decoder := yaml.NewDecoder(bytes.NewReader(raw))
			crds, deployments, stateful := 0, 0, 0
			for {
				var object struct {
					Kind string `yaml:"kind"`
					Spec struct {
						Replicas int `yaml:"replicas"`
						Template struct {
							Spec struct {
								Containers []struct {
									Image string   `yaml:"image"`
									Args  []string `yaml:"args"`
									Env   []struct {
										Name  string `yaml:"name"`
										Value string `yaml:"value"`
									} `yaml:"env"`
								} `yaml:"containers"`
							} `yaml:"spec"`
						} `yaml:"template"`
					} `yaml:"spec"`
				}
				if err := decoder.Decode(&object); errors.Is(err, io.EOF) {
					break
				} else if err != nil {
					t.Fatal(err)
				}
				switch object.Kind {
				case "DaemonSet", "Ingress", "VaultAuthGlobal":
					t.Fatal("unselected external integration rendered")
				case "CustomResourceDefinition":
					crds++
				case "Deployment":
					deployments++
				case "StatefulSet":
					stateful++
					if object.Spec.Replicas != 1 {
						t.Fatal("lab Vault is not single-member")
					}
				}
				for _, container := range object.Spec.Template.Spec.Containers {
					if container.Image != images["vault"] && container.Image != images["vso"] && container.Image != images["proxy"] {
						t.Fatalf("unreviewed container %s", container.Image)
					}
					environment := make(map[string]string)
					for _, variable := range container.Env {
						if _, duplicate := environment[variable.Name]; duplicate {
							t.Fatalf("%s container %s repeats environment variable %s", object.Kind, container.Image, variable.Name)
						}
						environment[variable.Name] = variable.Value
					}
					if container.Image == images["vault"] && (environment["SKIP_CHOWN"] != "true" || environment["SKIP_SETCAP"] != "true" || environment["VAULT_CACERT"] != "/vault/tls/ca.pem") {
						t.Fatal("Vault non-root bootstrap or private CA environment changed")
					}
				}
			}
			if name == "vault" {
				if stateful != 1 || deployments != 0 || crds != 0 || !strings.Contains(text, "/v1/sys/health?standbyok=true") || !strings.Contains(text, "storage: 1Gi") || strings.Contains(text, "tls_disable = 1") {
					t.Fatal("Vault persistence/TLS/bootstrap contract changed")
				}
			} else {
				if stateful != 0 || deployments != 1 || crds != 8 || strings.Contains(text, "helm.sh/hook: test") {
					t.Fatal("VSO workload/CRD inventory changed")
				}
				for _, argument := range []string{"--global-vault-auth-options=", "--client-cache-persistence-model=none", "--global-transformation-options=exclude-raw"} {
					if !strings.Contains(text, argument) {
						t.Fatal("missing explicit operator option", argument)
					}
				}
			}
		})
	}
}
