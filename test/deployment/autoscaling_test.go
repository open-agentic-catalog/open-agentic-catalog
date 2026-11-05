package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestHPAAdmissionUsesRuntimeTargetRatherThanStatusOrHPAName(t *testing.T) {
	for _, test := range []struct {
		name, target, kind, active string
		zero, blocked              bool
	}{
		{"api-active", "catalog-api", "Deployment", "True", false, true},
		{"worker-active", "catalog-worker", "Deployment", "True", false, true},
		{"api-inactive", "catalog-api", "Deployment", "False", true, true},
		{"worker-inactive", "catalog-worker", "Deployment", "False", true, true},
		{"catalog-api", "decoy-fixture", "Deployment", "True", false, false},
		{"ui", "catalog-ui", "Deployment", "True", false, false},
		{"similar-name", "catalog-api-other", "Deployment", "True", false, false},
		{"other-kind", "catalog-api", "StatefulSet", "True", false, false},
	} {
		modes := []bool{true}
		if test.blocked {
			modes = append(modes, false)
		}
		for _, check := range modes {
			mode := "deploy"
			if check {
				mode = "check"
			}
			t.Run(test.name+"/"+mode, func(t *testing.T) {
				o := testOptions()
				o.check = check
				readHPA := false
				run := func(_ context.Context, program string, args ...string) ([]byte, error) {
					if program == o.helm {
						if args[0] == "template" {
							return rendered(t, fixtureObjects()), nil
						}
						if args[0] == "upgrade" && slices.Contains(args, "--dry-run=server") {
							return nil, nil
						}
						t.Fatalf("preflight performed a Helm mutation: %v", args)
					}
					if program != o.kubectl || !slices.Equal(args[:5], []string{"--context", "owned", "--namespace", "test", "--request-timeout=30s"}) {
						t.Fatalf("unscoped command: %s %v", program, args)
					}
					args = args[5:]
					switch {
					case len(args) > 1 && args[0] == "get" && args[1] == "deployments":
						items := fixtureObjects()[:2]
						if test.zero {
							for i := range items {
								items[i].Spec.Replicas = 0
							}
						}
						return json.Marshal(list{Items: items})
					case len(args) > 1 && args[0] == "get" && args[1] == "hpa":
						readHPA = true
						return json.Marshal(map[string]any{"items": []any{map[string]any{"metadata": map[string]string{"name": test.name}, "spec": map[string]any{"scaleTargetRef": map[string]string{"apiVersion": "apps/v1", "kind": test.kind, "name": test.target}}, "status": map[string]any{"currentReplicas": 0, "conditions": []any{map[string]string{"type": "ScalingActive", "status": test.active}}}}}})
					default:
						t.Fatalf("preflight mutated or inspected outside its boundary: %v", args)
					}
					return nil, errors.New("unexpected test command")
				}
				err := deploy(t.Context(), o, run, func(string) {})
				if test.blocked {
					if err == nil || !strings.Contains(err.Error(), "targeting HPA") {
						t.Fatalf("targeting HPA admitted: %v", err)
					}
				} else if err != nil {
					t.Fatalf("unrelated HPA refused: %v", err)
				}
				if !readHPA {
					t.Fatal("preflight skipped the HPA inventory")
				}
			})
		}
	}
}
