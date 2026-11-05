//go:build minikube && functional

package main

import (
	"bytes"
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

	"go.yaml.in/yaml/v3"
)

// Render the installed immutable charts, including their actual value schemas.
// This remains optional for generic cluster-free tests; the owned scenario
// always requires the exact archives during preflight.
func TestInstalledCertificateChartsKeepControllersOutsideCatalog(t *testing.T) {
	pins, err := readCertificatePins()
	if err != nil {
		t.Fatal(err)
	}
	images := map[string]string{}
	for name := range pins.Images {
		images[name] = "open-agentic-catalog:minikube-cert-" + name + "-unit"
	}
	for name, pin := range pins.Charts {
		t.Run(name, func(t *testing.T) {
			chart, err := filepath.Abs(filepath.Join("../../.tools/minikube-cert-manager", pin.File))
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(chart) // #nosec G304 -- Embedded inventory selects a local cache path; its checksum is verified before rendering.
			if os.IsNotExist(err) {
				t.Skip("run make cert-manager-tools-install for native chart contracts")
			}
			if err != nil || fmt.Sprintf("%x", sha256.Sum256(raw)) != pin.SHA256 {
				t.Fatal("chart differs from its immutable pin")
			}
			values, scope, release := certificateControllerValues(images), certificateControllerNamespace, "catalog-cert-manager"
			if name == "traefik" {
				values, scope, release = certificateIngressValues(images["traefik"]), namespace, "catalog-edge"
			}
			raw, err = json.Marshal(values)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "values.json")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			args := []string{"template", release, chart, "--namespace", scope, "--kube-version", "1.35.9", "--values", path}
			if name == "cert-manager" {
				args = append(args, "--include-crds")
			}
			result, err := exec.CommandContext(t.Context(), "helm", args...).CombinedOutput() // #nosec G204 -- Fixed Helm and verified local archives; no shell.
			if err != nil {
				t.Fatalf("native chart value contract: %v: %s", err, result)
			}
			decoder := yaml.NewDecoder(bytes.NewReader(result))
			deployments, crds := 0, 0
			for {
				var object struct {
					Kind     string `yaml:"kind"`
					Metadata struct {
						Namespace string `yaml:"namespace"`
					} `yaml:"metadata"`
					Spec struct {
						Type     string `yaml:"type"`
						Template struct {
							Spec struct {
								Containers []struct {
									Image  string `yaml:"image"`
									Policy string `yaml:"imagePullPolicy"`
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
				if name == "traefik" {
					switch object.Kind {
					case "", "ServiceAccount", "Role", "RoleBinding", "Service", "Deployment":
					default:
						t.Fatalf("namespace-scoped ingress renders unselected %s", object.Kind)
					}
					if object.Kind != "" && object.Metadata.Namespace != namespace {
						t.Fatal("ingress controller escapes its namespace")
					}
				}
				if object.Kind == "Job" {
					t.Fatal("unselected startup image or Job rendered")
				}
				if object.Kind == "CustomResourceDefinition" {
					crds++
				}
				if object.Kind == "Service" && object.Spec.Type != "" && object.Spec.Type != "ClusterIP" {
					t.Fatal("external controller exposes a public Service")
				}
				if object.Kind != "Deployment" {
					continue
				}
				deployments++
				if len(object.Spec.Template.Spec.Containers) != 1 {
					t.Fatal("unexpected external controller container inventory")
				}
				container := object.Spec.Template.Spec.Containers[0]
				matched := false
				for component, image := range images {
					if strings.TrimPrefix(container.Image, "docker.io/library/") == image && (name != "traefik" || component == "traefik") {
						matched = true
					}
				}
				if !matched || container.Policy != "Never" {
					t.Fatal("controller does not use its verified offline image")
				}
			}
			if name == "cert-manager" && (deployments != 3 || crds != 6) || name == "traefik" && (deployments != 1 || crds != 0) {
				t.Fatal("native controller inventory changed")
			}
		})
	}
}
