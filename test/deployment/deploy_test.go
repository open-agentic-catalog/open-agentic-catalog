package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func fixtureObjects() []object {
	var result []object
	for _, role := range []string{"api", "worker"} {
		var v object
		v.Kind = "Deployment"
		v.Metadata.Name = "catalog-" + role
		v.Metadata.Labels = map[string]string{"app.kubernetes.io/component": role}
		v.Metadata.Annotations = map[string]string{"meta.helm.sh/release-name": "catalog", "meta.helm.sh/release-namespace": "test"}
		v.Spec.Replicas = 2
		v.Spec.Selector.MatchLabels = map[string]string{"app.kubernetes.io/instance": "catalog", "app.kubernetes.io/component": role}
		result = append(result, v)
	}
	var hook object
	hook.Kind = "Job"
	hook.Metadata.Labels = map[string]string{"app.kubernetes.io/component": "migration"}
	hook.Metadata.Annotations = map[string]string{"helm.sh/hook": "pre-install,pre-upgrade"}
	return append(result, hook)
}
func rendered(t *testing.T, objects []object) []byte {
	t.Helper()
	parts := make([]string, 0, len(objects))
	for _, v := range objects {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, string(raw))
	}
	return []byte(strings.Join(parts, "\n---\n"))
}
func testOptions() options {
	return options{context: "owned", namespace: "test", release: "catalog", chart: "chart", values: "values", helm: "helm", kubectl: "kubectl", timeout: time.Second}
}

func TestTargetsRejectIncompleteOrChangedAuthority(t *testing.T) {
	for _, change := range []func([]object) []object{
		func(v []object) []object { return v[:2] },
		func(v []object) []object { v[0].Spec.Replicas = 0; return v },
		func(v []object) []object {
			v[0].Spec.Selector.MatchLabels["app.kubernetes.io/instance"] = "other"
			return v
		},
		func(v []object) []object { return append(v, v[0]) },
		func(v []object) []object { return v[1:] },
		func(v []object) []object { v[0].Metadata.Name = ""; return v },
		func(v []object) []object { v[0].Metadata.Labels["app.kubernetes.io/component"] = "unknown"; return v },
	} {
		if _, err := targets(rendered(t, change(fixtureObjects())), "catalog"); err == nil {
			t.Fatal("unsafe target admitted")
		}
	}
	if _, err := targets([]byte("bad: ["), "catalog"); err == nil {
		t.Fatal("malformed render admitted")
	}
	targets, err := targets(rendered(t, fixtureObjects()), "catalog")
	if err != nil || targets["worker"].replicas != 2 {
		t.Fatalf("valid target: %v", err)
	}
}

func withCompanions() []object {
	objects := fixtureObjects()
	for _, role := range []string{"ui", "pgbouncer"} {
		var companion object
		companion.Kind = "Deployment"
		companion.Metadata.Name = "catalog-" + role
		companion.Metadata.Labels = map[string]string{"app.kubernetes.io/component": role}
		objects = append(objects, companion)
	}
	return objects
}

func TestTargetsExcludeOptionalCompanions(t *testing.T) {
	selected, err := targets(rendered(t, withCompanions()), "catalog")
	if err != nil || len(selected) != 2 || selected["api"].name != "catalog-api" || selected["worker"].name != "catalog-worker" {
		t.Fatalf("runtime targets with companions: %v, %v", selected, err)
	}
}

func TestDeploymentOrderingAndFailures(t *testing.T) {
	for _, scenario := range []string{"install", "upgrade", "retry-zero", "fixture-peer", "companions", "companions-check", "missing-inventory", "check", "render-fail", "dry-run-fail", "foreign", "selector-change", "hpa", "bad-pods", "drain-fail", "migration-fail", "readiness-fail"} {
		t.Run(scenario, func(t *testing.T) {
			o := testOptions()
			o.check = scenario == "check" || scenario == "companions-check"
			var mutations []string
			run := func(_ context.Context, program string, args ...string) ([]byte, error) {
				if program == "helm" {
					if !slices.Contains(args, "owned") || !slices.Contains(args, "test") {
						t.Fatal("unscoped helm")
					}
					if args[0] == "template" {
						if scenario == "render-fail" {
							return nil, errors.New("private")
						}
						if strings.HasPrefix(scenario, "companions") {
							return rendered(t, withCompanions()), nil
						}
						return rendered(t, fixtureObjects()), nil
					}
					if slices.Contains(args, "--dry-run=server") {
						if scenario == "dry-run-fail" {
							return nil, errors.New("private")
						}
						return nil, nil
					}
					if slices.Contains(args, "--atomic") || slices.Contains(args, "--reuse-values") {
						t.Fatal("unsafe automatic recovery")
					}
					mutations = append(mutations, "migrate")
					if scenario == "migration-fail" {
						return nil, errors.New("private")
					}
					return nil, nil
				}
				if !slices.Equal(args[:4], []string{"--context", "owned", "--namespace", "test"}) {
					t.Fatal("unscoped kubectl")
				}
				args = args[5:]
				if args[0] == "get" {
					switch args[1] {
					case "deployments":
						if scenario == "missing-inventory" {
							return []byte(`{}`), nil
						}
						items := fixtureObjects()[:2]
						if strings.HasPrefix(scenario, "companions") {
							items = withCompanions()
						}
						if scenario == "install" {
							items = []object{}
						}
						if scenario == "fixture-peer" {
							var peer object
							peer.Metadata.Labels = map[string]string{"app.kubernetes.io/component": "identity"}
							items = append(items, peer)
						}
						if scenario == "foreign" {
							items[0].Metadata.Annotations["meta.helm.sh/release-name"] = "other"
						}
						if scenario == "selector-change" {
							items[0].Spec.Selector.MatchLabels["extra"] = "changed"
						}
						if scenario == "retry-zero" {
							for i := range items {
								items[i].Spec.Replicas = 0
							}
						}
						return json.Marshal(list{Items: items})
					case "hpa":
						if scenario == "hpa" {
							var h object
							h.Spec.ScaleTargetRef.Name = "catalog-api"
							h.Spec.ScaleTargetRef.Kind = "Deployment"
							return json.Marshal(list{Items: []object{h}})
						}
					case "pods":
						mutations = append(mutations, "confirm-empty")
						if scenario == "bad-pods" {
							return []byte(`{}`), nil
						}
					}
					return []byte(`{"items":[]}`), nil
				}
				mutations = append(mutations, strings.Join(args, " "))
				if scenario == "drain-fail" && args[0] == "scale" {
					return nil, errors.New("private")
				}
				if scenario == "readiness-fail" && args[0] == "rollout" {
					return nil, errors.New("private")
				}
				return nil, nil
			}
			err := deploy(t.Context(), o, run, func(string) {})
			success := slices.Contains([]string{"install", "upgrade", "retry-zero", "fixture-peer", "companions", "companions-check", "check"}, scenario)
			if (err == nil) != success {
				t.Fatalf("outcome %v; calls %v", err, mutations)
			}
			if err != nil && strings.Contains(err.Error(), "private") {
				t.Fatal("dependency text escaped")
			}
			if scenario == "install" && !slices.Equal(mutations[:3], []string{"confirm-empty", "confirm-empty", "migrate"}) {
				t.Fatal("first installation did not exclude orphaned runtime Pods before migration")
			}
			if scenario == "upgrade" || scenario == "retry-zero" || scenario == "companions" {
				want := []string{"scale deployment/catalog-api --replicas=0", "confirm-empty", "scale deployment/catalog-worker --replicas=0", "confirm-empty", "migrate", "scale deployment/catalog-api --replicas=2", "rollout status deployment/catalog-api --timeout=1s", "scale deployment/catalog-worker --replicas=2", "rollout status deployment/catalog-worker --timeout=1s"}
				if !slices.Equal(mutations, want) {
					t.Fatalf("unsafe ordering: %v", mutations)
				}
			}
			if slices.Contains([]string{"check", "companions-check", "missing-inventory", "render-fail", "dry-run-fail", "foreign", "selector-change", "hpa"}, scenario) && len(mutations) != 0 {
				t.Fatalf("preflight mutated: %v", mutations)
			}
			if slices.Contains([]string{"drain-fail", "bad-pods"}, scenario) && slices.Contains(mutations, "migrate") {
				t.Fatal("migration preceded drain")
			}
			if (scenario == "migration-fail" || scenario == "readiness-fail") && !slices.Equal(mutations[len(mutations)-2:], []string{"scale deployment/catalog-api --replicas=0", "scale deployment/catalog-worker --replicas=0"}) {
				t.Fatal("failed migration restarted target")
			}
		})
	}
}

func TestDrainRejectsLateEmptyObservation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	err := waitGone(ctx, func(context.Context, ...string) ([]byte, error) { cancel(); return []byte(`{"items":[]}`), nil }, "owned")
	if !errors.Is(err, context.Canceled) {
		t.Fatal("late success accepted")
	}
}

func TestLocalInputsAndBounds(t *testing.T) {
	o := testOptions()
	if o.validate() != nil {
		t.Fatal("valid inputs refused")
	}
	o.context = ""
	if o.validate() == nil {
		t.Fatal("ambient context admitted")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "in")
	dst := filepath.Join(dir, "out")
	if err := os.WriteFile(src, []byte("values"), 0o600); err != nil {
		t.Fatal(err)
	}
	if copyInput(src, dst, 3) == nil {
		t.Fatal("oversized input accepted")
	}
	if err := copyInput(src, dst, 10); err != nil {
		t.Fatal(err)
	}
	if err := copyInput(src, dir, 10); err == nil || err.Error() != "deployment input snapshot write failed" {
		t.Fatalf("unsafe write diagnostic: %v", err)
	}
	if copyInput(dir, dst, 10) == nil {
		t.Fatal("directory accepted")
	}
	var out boundedOutput
	_, _ = out.Write(make([]byte, (8<<20)+1))
	if !out.overflow || out.Len() != 0 {
		t.Fatal("unbounded tool output")
	}
}

func TestSnapshotFreezesPrivateLocalInputs(t *testing.T) {
	dir := t.TempDir()
	o := testOptions()
	o.chart = filepath.Join(dir, "selected.tgz")
	o.values = filepath.Join(dir, "selected.yaml")
	for path, body := range map[string]string{o.chart: "selected-chart", o.values: "selected-values"} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	prepared, err := snapshot(t.Context(), o, func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("an existing local archive must not download or package another chart")
		return nil, nil
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for original, copyPath := range map[string]string{o.chart: prepared.chart, o.values: prepared.values} {
		before, err := os.ReadFile(copyPath) // #nosec G304 -- snapshot path returned from an invocation-owned test directory.
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(original, []byte("changed after preflight"), 0o600); err != nil {
			t.Fatal(err)
		}
		after, err := os.ReadFile(copyPath) // #nosec G304 -- same invocation-owned snapshot under test.
		if err != nil || string(before) != string(after) {
			t.Fatalf("selected input changed: %v", err)
		}
		info, err := os.Stat(copyPath)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("snapshot is not private: %v", err)
		}
	}
	o.chart = t.TempDir()
	if _, err := snapshot(t.Context(), o, func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("private detail") }, t.TempDir()); err == nil || strings.Contains(err.Error(), "private detail") {
		t.Fatalf("package failure: %v", err)
	}
}

func TestFailedDeploymentCleanupSurvivesCancellationAndReportsFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var roles []string
	err := failedDeployment(ctx, func(cleanup context.Context, args ...string) ([]byte, error) {
		if cleanup.Err() != nil {
			t.Fatal("cleanup inherited cancellation")
		}
		if _, ok := cleanup.Deadline(); !ok {
			t.Fatal("unbounded cleanup")
		}
		roles = append(roles, args[1])
		if len(roles) == 1 {
			return nil, errors.New("private failure")
		}
		return nil, nil
	}, map[string]target{"api": {name: "api"}, "worker": {name: "worker"}})
	if !slices.Equal(roles, []string{"deployment/api", "deployment/worker"}) || !strings.Contains(err.Error(), "stop/inspect") || strings.Contains(err.Error(), "private failure") {
		t.Fatalf("cleanup outcome: %v %v", roles, err)
	}
}
