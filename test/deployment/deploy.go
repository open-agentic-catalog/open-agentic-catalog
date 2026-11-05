package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

type object struct {
	Kind     string `json:"kind" yaml:"kind"`
	Metadata struct {
		Name        string            `json:"name" yaml:"name"`
		Labels      map[string]string `json:"labels" yaml:"labels"`
		Annotations map[string]string `json:"annotations" yaml:"annotations"`
	} `json:"metadata" yaml:"metadata"`
	Spec struct {
		Replicas int `json:"replicas" yaml:"replicas"`
		Selector struct {
			MatchLabels map[string]string `json:"matchLabels" yaml:"matchLabels"`
		} `json:"selector" yaml:"selector"`
		ScaleTargetRef struct {
			Name string `json:"name"`
			Kind string `json:"kind"`
		} `json:"scaleTargetRef"`
	} `json:"spec" yaml:"spec"`
}
type list struct {
	Items []object `json:"items"`
}
type target struct {
	name, selector string
	replicas       int
}

func selector(labels map[string]string) string {
	parts := make([]string, 0, len(labels))
	for k, v := range labels {
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func targets(raw []byte, release string) (map[string]target, error) {
	result := map[string]target{}
	hook := false
	d := yaml.NewDecoder(bytes.NewReader(raw))
	for count := 0; ; count++ {
		var v object
		if err := d.Decode(&v); errors.Is(err, io.EOF) {
			break
		} else if err != nil || count > 64 {
			return nil, errors.New("invalid bounded chart rendering")
		}
		if v.Kind == "Job" && v.Metadata.Labels["app.kubernetes.io/component"] == "migration" {
			hook = v.Metadata.Annotations["helm.sh/hook"] == "pre-install,pre-upgrade"
		}
		if v.Kind != "Deployment" {
			continue
		}
		role := v.Metadata.Labels["app.kubernetes.io/component"]
		if role == "ui" || role == "pgbouncer" {
			continue
		}
		labels := v.Spec.Selector.MatchLabels
		if (role != "api" && role != "worker") || result[role].name != "" || v.Metadata.Name == "" || v.Spec.Replicas < 1 || labels["app.kubernetes.io/instance"] != release || labels["app.kubernetes.io/component"] != role {
			return nil, errors.New("chart must select exactly one nonzero API and worker Deployment")
		}
		result[role] = target{v.Metadata.Name, selector(labels), v.Spec.Replicas}
	}
	if len(result) != 2 || !hook {
		return nil, errors.New("chart must retain both runtimes and the migration hook")
	}
	return result, nil
}

func deploy(ctx context.Context, o options, run command, progress func(string)) error {
	kube := func(c context.Context, args ...string) ([]byte, error) {
		return run(c, o.kubectl, append([]string{"--context", o.context, "--namespace", o.namespace, "--request-timeout=30s"}, args...)...)
	}
	helm := func(c context.Context, args ...string) ([]byte, error) {
		return run(c, o.helm, append(args, "--kube-context", o.context, "--namespace", o.namespace)...)
	}
	progress("validating target before drain")
	pre, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	raw, err := helm(pre, "template", o.release, o.chart, "--values", o.values, "--kube-version", "1.34.0")
	if err != nil {
		return errors.New("chart render failed; workloads unchanged")
	}
	selected, err := targets(raw, o.release)
	if err != nil {
		return err
	}
	upgrade := []string{"upgrade", "--install", o.release, o.chart, "--values", o.values, "--reset-values", "--wait", "--wait-for-jobs", "--timeout", o.timeout.String()}
	if _, err := helm(pre, append(append([]string{}, upgrade...), "--dry-run=server", "--hide-secret")...); err != nil {
		return errors.New("helm dry-run failed; workloads unchanged")
	}
	raw, err = kube(pre, "get", "deployments", "-l", "app.kubernetes.io/instance="+o.release, "-o", "json")
	if err != nil {
		return errors.New("workload inventory unavailable")
	}
	var existing list
	if json.Unmarshal(raw, &existing) != nil || existing.Items == nil {
		return errors.New("invalid workload inventory")
	}
	live := map[string]bool{}
	for _, v := range existing.Items {
		role := v.Metadata.Labels["app.kubernetes.io/component"]
		if role != "api" && role != "worker" {
			continue
		}
		if selected[role].name != v.Metadata.Name || selected[role].selector != selector(v.Spec.Selector.MatchLabels) || v.Metadata.Annotations["meta.helm.sh/release-name"] != o.release || v.Metadata.Annotations["meta.helm.sh/release-namespace"] != o.namespace {
			return errors.New("workload ownership or names differ; manual review required")
		}
		live[role] = true
	}
	raw, err = kube(pre, "get", "hpa", "-o", "json")
	if err != nil {
		return errors.New("autoscaler inventory unavailable")
	}
	var autoscalers list
	if json.Unmarshal(raw, &autoscalers) != nil || autoscalers.Items == nil {
		return errors.New("invalid autoscaler inventory")
	}
	for _, v := range autoscalers.Items {
		for _, t := range selected {
			if v.Spec.ScaleTargetRef.Kind == "Deployment" && v.Spec.ScaleTargetRef.Name == t.name {
				return errors.New("suspend and remove targeting HPA before coordinated deployment")
			}
		}
	}
	if o.check {
		progress("validation passed; no deployment mutation")
		return nil
	}
	drain, done := context.WithTimeout(ctx, o.timeout)
	defer done()
	for _, role := range []string{"api", "worker"} {
		if live[role] {
			progress("draining " + role)
			if _, err := kube(drain, "scale", "deployment/"+selected[role].name, "--replicas=0"); err != nil {
				return errors.New("drain failed; migration not started; recover with the same target inputs")
			}
		}
		if err := waitGone(drain, kube, selected[role].selector); err != nil {
			return errors.New("drain incomplete; migration not started; recover with the same target inputs")
		}
	}
	progress("old runtimes stopped; applying migration and target release")
	// Never roll back SQL or restart the prior image after mutation. On a
	// target startup failure, best-effort stop both target roles and report it.
	if _, err := helm(ctx, upgrade...); err != nil {
		return failedDeployment(ctx, kube, selected)
	}
	ready, finish := context.WithTimeout(ctx, o.timeout)
	defer finish()
	for _, role := range []string{"api", "worker"} {
		t := selected[role]
		if _, err := kube(ready, "scale", "deployment/"+t.name, fmt.Sprintf("--replicas=%d", t.replicas)); err != nil {
			return failedDeployment(ctx, kube, selected)
		}
		if _, err := kube(ready, "rollout", "status", "deployment/"+t.name, "--timeout="+o.timeout.String()); err != nil {
			return failedDeployment(ctx, kube, selected)
		}
	}
	progress("target replicas ready; perform authorized application readback")
	return nil
}

// Cleanup remains bounded after cancellation and only scales the selected targets.
func failedDeployment(ctx context.Context, kube func(context.Context, ...string) ([]byte, error), selected map[string]target) error {
	cleanup, done := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer done()
	stopped := true
	for _, role := range []string{"api", "worker"} {
		if _, err := kube(cleanup, "scale", "deployment/"+selected[role].name, "--replicas=0"); err != nil {
			stopped = false
		}
	}
	if !stopped {
		return errors.New("deployment failed; stop/inspect target workloads and pending hook before retry; no rollback attempted")
	}
	return errors.New("deployment failed; target replicas set to zero; inspect hook and repair forward with the same inputs")
}

func waitGone(ctx context.Context, kube func(context.Context, ...string) ([]byte, error), selector string) error {
	for {
		raw, err := kube(ctx, "get", "pods", "-l", selector, "-o", "json")
		if err != nil {
			return err
		}
		var pods list
		if err := json.Unmarshal(raw, &pods); err != nil || pods.Items == nil {
			return errors.New("invalid Pod inventory")
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if len(pods.Items) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
