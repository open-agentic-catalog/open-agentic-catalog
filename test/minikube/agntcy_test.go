//go:build minikube

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func directoryTestInputs(t *testing.T) agntcyInputs {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "interoperability", "agntcy", "deployment.json"))
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		Release string            `json:"release"`
		Images  map[string]string `json:"images"`
	}
	if err = json.Unmarshal(raw, &lock); err != nil {
		t.Fatal(err)
	}
	result := agntcyInputs{Release: lock.Release, Images: make(map[string]agntcyImage)}
	for name, ref := range lock.Images {
		result.Images[name] = agntcyImage{ID: "sha256:local-" + name, Reference: ref}
	}
	return result
}

func TestAGNTCYValuesDisablePublicDefaults(t *testing.T) {
	inputs := directoryTestInputs(t)
	values := agntcyValues(inputs, []string{"private-db", "private-oci", "private-sync"}, "synthetic-hash")
	api := directoryMap(t, values["apiserver"])
	config := directoryMap(t, api["config"])
	policy := directoryMap(t, config["policy"])
	operations, ok := policy["operations"].([]any)
	if !ok || len(operations) != 1 {
		t.Fatal(policy)
	}
	validation := directoryMap(t, operations[0])
	if validation["provider"] != "oasf" || validation["name"] != "schema" || fmt.Sprint(validation["op"]) != "[push autosync index]" || validation["schema_url"] != "http://schema-router.agntcy-qualification.svc.cluster.local:8080" {
		t.Fatal(validation)
	}
	if _, obsolete := config["oasf_api_validation"]; obsolete {
		t.Fatal("obsolete validator configuration")
	}
	if directoryMap(t, api["zot"])["mountConfig"] != true || directoryMap(t, directoryMap(t, api["zot"])["secretFiles"])["htpasswd"] != "synthetic-hash" {
		t.Fatal("native OCI credentials not mounted")
	}
	var zot map[string]any
	if err := json.Unmarshal([]byte(fmt.Sprint(directoryMap(t, directoryMap(t, api["zot"])["configFiles"])["config.json"])), &zot); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"search", "trust"} {
		if directoryMap(t, directoryMap(t, zot["extensions"])[name])["enable"] != false {
			t.Fatal(zot)
		}
	}
	if directoryMap(t, zot["storage"])["rootDirectory"] != "/var/lib/registry" {
		t.Fatal(zot)
	}
	routing := directoryMap(t, config["routing"])
	if fmt.Sprint(routing["bootstrap_peers"]) != "[]" || directoryMap(t, routing["autosync"])["enabled"] != false {
		t.Fatal(routing)
	}
	for _, name := range []string{"service", "routingService"} {
		if directoryMap(t, api[name])["type"] != "ClusterIP" {
			t.Fatal(name)
		}
	}
	if directoryMap(t, api["routingService"])["externalTrafficPolicy"] != "" {
		t.Fatal("invalid ClusterIP traffic policy")
	}
	for _, name := range []string{"spire", "oasf-sdk"} {
		if directoryMap(t, api[name])["enabled"] != false {
			t.Fatal(name)
		}
	}
	reconciler := directoryMap(t, directoryMap(t, api["reconciler"])["config"])
	for _, name := range []string{"regsync", "identity", "signature", "scan", "metrics", "prune", "sync", "publish", "policy_evaluation"} {
		if directoryMap(t, reconciler[name])["enabled"] != false {
			t.Fatal(name)
		}
	}
	if directoryMap(t, reconciler["indexer"])["enabled"] != true {
		t.Fatal("native indexing disabled")
	}
	for _, name := range []string{"apiserver", "zot"} {
		image := directoryMap(t, api[map[string]string{"apiserver": "image", "zot": "zot"}[name]])
		if name == "zot" {
			image = directoryMap(t, image["image"])
		}
		if image["pullPolicy"] != "Never" || !strings.Contains(fmt.Sprint(image["tag"]), "@sha256:") {
			t.Fatal(image)
		}
	}
}

func TestAGNTCYRenderRefusesUnexpectedAuthorityAndImages(t *testing.T) {
	inputs := directoryTestInputs(t)
	valid := ""
	for _, image := range inputs.Images {
		valid += "---\nkind: Deployment\nspec:\n  template:\n    spec:\n      containers:\n      - image: " + image.Reference + "\n        imagePullPolicy: Never\n"
	}
	if err := validateAGNTCYRender([]byte(valid), inputs); err != nil {
		t.Fatal(err)
	}
	for _, extra := range []string{"---\nkind: ClusterRole\n", "---\nkind: NetworkPolicy\n", "---\nkind: Ingress\n", "---\nkind: Service\nspec:\n  type: NodePort\n", "---\nkind: Service\nspec:\n  type: ClusterIP\n  externalTrafficPolicy: Local\n", "---\nkind: Job\nspec:\n  template:\n    spec:\n      containers:\n      - image: alpine:latest\n        imagePullPolicy: Never\n"} {
		if err := validateAGNTCYRender([]byte(valid+extra), inputs); err == nil {
			t.Fatal(extra)
		}
	}
	if err := validateAGNTCYRender([]byte(strings.ReplaceAll(valid, "Never", "IfNotPresent")), inputs); err == nil {
		t.Fatal("downloadable images admitted")
	}
	if err := validateAGNTCYRender([]byte("kind: Deployment\n"), inputs); err == nil {
		t.Fatal("empty inventory admitted")
	}
}

func TestAGNTCYWorkloadInventoryRequiresExactRunningImages(t *testing.T) {
	inputs := directoryTestInputs(t)
	pods := []map[string]any{}
	add := func(name, image, id string) {
		pods = append(pods, map[string]any{"metadata": map[string]any{"name": name}, "spec": map[string]any{"containers": []any{map[string]any{"name": "main", "image": image}}}, "status": map[string]any{"containerStatuses": []any{map[string]any{"name": "main", "ready": true, "imageID": "docker-pullable://" + id}}}})
	}
	for name, image := range inputs.Images {
		add(name, image.Reference, "owned@"+image.ID)
	}
	add("schema", "owned:router", "owned@sha256:router")
	add("gateway", "owned:router", "owned@sha256:router")
	encode := func() []byte {
		raw, err := json.Marshal(map[string]any{"items": pods})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	if _, err := agntcyRunningImages(encode(), inputs, "owned:router", "sha256:router"); err != nil {
		t.Fatal(err)
	}
	if _, err := agntcyRunningImages(encode(), inputs, "owned:router", "sha256:wrong"); err == nil {
		t.Fatal("wrong running digest admitted")
	}
	statuses, ok := directoryMap(t, pods[0]["status"])["containerStatuses"].([]any)
	if !ok {
		t.Fatal("status fixture has no container array")
	}
	directoryMap(t, statuses[0])["ready"] = false
	if _, err := agntcyRunningImages(encode(), inputs, "owned:router", "sha256:router"); err == nil {
		t.Fatal("unready workload admitted")
	}
	pods = pods[1:]
	if _, err := agntcyRunningImages(encode(), inputs, "owned:router", "sha256:router"); err == nil {
		t.Fatal("missing workload admitted")
	}
}

func TestAGNTCYManagedBuildArgumentsAndVersionFields(t *testing.T) {
	stamp := map[string]string{"version": "0.1.0", "commit": strings.Repeat("a", 40), "sourceState": "dirty"}
	args := agntcyBuildArguments("owned:current", stamp)
	for _, required := range []string{"--target", "runtime", "VERSION=0.1.0", "COMMIT=" + stamp["commit"], "SOURCE_STATE=dirty", "Containerfile"} {
		if !strings.Contains(strings.Join(args, " "), required) {
			t.Fatal(args)
		}
	}
	actual := map[string]any{"service": "open-agentic-catalog", "version": "0.1.0", "commit": stamp["commit"], "sourceState": "dirty"}
	if !matchesBuildIdentity(actual, stamp) {
		t.Fatal(actual)
	}
	actual["sourceState"] = "clean"
	if matchesBuildIdentity(actual, stamp) {
		t.Fatal("stale source state admitted")
	}
	expected := map[string]string{"version": "minikube", "commit": "local"}
	if !matchesBuildIdentity(map[string]any{"service": "open-agentic-catalog", "version": "minikube", "commit": "local"}, expected) {
		t.Fatal("existing scenarios changed")
	}
	if reflect.DeepEqual(args, agntcyBuildArguments("owned:other", stamp)) {
		t.Fatal("image binding lost")
	}
}

func directoryMap(t *testing.T, value any) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("expected object, got %T", value)
	}
	return result
}

func TestManagedMigrationImageRejectsStaleOrAmbiguousEvidence(t *testing.T) {
	valid := `{"items":[{"spec":{"containers":[{"image":"owned:current"}]},"status":{"containerStatuses":[{"imageID":"docker://sha256:current"}]}}]}`
	id, err := managedMigrationImage([]byte(valid), "owned:current", "sha256:current")
	if err != nil || id != "sha256:current" {
		t.Fatal(id, err)
	}
	for _, raw := range []string{strings.ReplaceAll(valid, "owned:current", "owned:stale"), strings.ReplaceAll(valid, "sha256:current", "sha256:stale"), `{"items":[{},{}]}`, `{"items":[{"spec":{"containers":[]}}]}`, `not json`} {
		if _, err := managedMigrationImage([]byte(raw), "owned:current", "sha256:current"); err == nil {
			t.Fatal(raw)
		}
	}
	for _, raw := range []string{`{"items":[]}`, `{"items":[{"spec":{"containers":[{"image":"owned:current"}]}}]}`} {
		if id, err := managedMigrationImage([]byte(raw), "owned:current", "sha256:current"); err != nil || id != "" {
			t.Fatal(id, err)
		}
	}
}

func TestDirectoryPhaseReportIsPrivateAndRedacted(t *testing.T) {
	root := t.TempDir()
	h := &harness{options: options{scenario: "agntcy", repositoryRoot: root}, suffix: "test-owned", profile: "owned-profile", temporary: "owned-temporary", agntcy: &agntcyState{router: "owned:router", secrets: []string{"synthetic-secret"}}}
	if err := h.interopProgress("finished", fmt.Errorf("failed: synthetic-secret"), nil); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "output/interoperability/minikube-agntcy-test-owned/result.json")
	raw, err := os.ReadFile(path) // #nosec G304 -- path is under this test's private temporary root.
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "synthetic-secret") {
		t.Fatal("secret in report")
	}
	var result map[string]any
	if err = json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result["passed"] != false || result["cleanupPassed"] != true || result["profile"] != "owned-profile" {
		t.Fatal(result)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
}

func TestDirectoryAliasesPreservePinnedIdentities(t *testing.T) {
	pinned := directoryTestInputs(t)
	actual := agntcyRuntimeInputs(pinned, "unique")
	for name, image := range actual.Images {
		if image.ID != pinned.Images[name].ID || image.Reference != "open-agentic-catalog:minikube-directory-"+name+"-unique" || !strings.Contains(pinned.Images[name].Reference, "@sha256:") {
			t.Fatal(image)
		}
	}
	api := directoryMap(t, agntcyValues(actual, []string{"db", "oci", "sync"}, "synthetic-hash")["apiserver"])
	postgres := directoryMap(t, directoryMap(t, api["postgresql"])["image"])
	if postgres["repository"] != "library/open-agentic-catalog" || postgres["tag"] != "minikube-directory-postgres-unique" || postgres["digest"] != "" {
		t.Fatal(postgres)
	}
	if agntcyImageMatches("open-agentic-catalog:minikube-directory-postgres-wrong", actual.Images["postgres"].Reference) {
		t.Fatal("wrong alias admitted")
	}
}

func TestAGNTCYEnvironmentExcludesAmbientCredentialsAndProxy(t *testing.T) {
	for key, value := range map[string]string{"AWS_SECRET_ACCESS_KEY": "private", "HTTPS_PROXY": "http://external", "NO_PROXY": "*", "GIT_DIR": "/other", "KUBECONFIG": "/ambient", "DOCKER_CONTEXT": "selected-local", "PATH": "/tools"} {
		t.Setenv(key, value)
	}
	h := &harness{options: options{scenario: "agntcy"}, kubeconfig: "/owned/kubeconfig", profile: "owned-profile", minikubeHome: "/owned/minikube"}
	actual := make(map[string]string)
	for _, item := range h.environment() {
		key, value, _ := strings.Cut(item, "=")
		actual[key] = value
	}
	for _, key := range []string{"AWS_SECRET_ACCESS_KEY", "HTTPS_PROXY", "NO_PROXY", "GIT_DIR"} {
		if _, ok := actual[key]; ok {
			t.Fatal("ambient setting retained", key)
		}
	}
	if actual["KUBECONFIG"] != h.kubeconfig || actual["MINIKUBE_PROFILE"] != h.profile || actual["DOCKER_CONTEXT"] != "selected-local" || actual["PATH"] != "/tools" {
		t.Fatal(actual)
	}
}
