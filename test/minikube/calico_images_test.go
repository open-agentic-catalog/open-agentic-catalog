//go:build minikube

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func calicoImageControl(t *testing.T, ids map[string]string) []byte {
	t.Helper()
	node, cni, controller := "quay.io/calico/node:v3.32.2", "quay.io/calico/cni:v3.32.2", "quay.io/calico/kube-controllers:v3.32.2"
	value := map[string]any{"items": []map[string]any{
		{"metadata": map[string]any{"labels": map[string]string{"k8s-app": "calico-node"}},
			"spec":   map[string]any{"initContainers": []map[string]string{{"name": "install-cni", "image": cni}}, "containers": []map[string]string{{"name": "calico-node", "image": node}}},
			"status": map[string]any{"initContainerStatuses": []map[string]string{{"name": "install-cni", "imageID": ids[cni]}}, "containerStatuses": []map[string]string{{"name": "calico-node", "imageID": ids[node]}}}},
		{"metadata": map[string]any{"labels": map[string]string{"k8s-app": "calico-kube-controllers"}},
			"spec":   map[string]any{"containers": []map[string]string{{"name": "calico-kube-controllers", "image": controller}}},
			"status": map[string]any{"containerStatuses": []map[string]string{{"name": "calico-kube-controllers", "imageID": ids[controller]}}}},
	}}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestCalicoImageInventoryAcceptsOnlyReviewedIndexAndAMD64Identities(t *testing.T) {
	indexes := map[string]string{
		"quay.io/calico/cni:v3.32.2":              "quay.io/calico/cni@sha256:0ef740bc587f25565905adf1d1f61a7faff0d571c449c6bdd789feed743d3ef7",
		"quay.io/calico/node:v3.32.2":             "quay.io/calico/node@sha256:99b03fe91e8bfbcb153ae65ef4b701b24ce541ffdd74ff314eb041096008f7fd",
		"quay.io/calico/kube-controllers:v3.32.2": "quay.io/calico/kube-controllers@sha256:7870b67ebb13fabc3005252b44fe6e78b21635649bd3072b80afa1684b6565d0",
	}
	platforms := map[string]string{
		"quay.io/calico/cni:v3.32.2":              "quay.io/calico/cni@sha256:8de23af2a36514e5027cf3f8577be61c577ae21664d19bd726bb9ccedb9e0d5a",
		"quay.io/calico/node:v3.32.2":             "quay.io/calico/node@sha256:d7a9b58573b165caa8128d2735a19b788434547064b2b4af2abf68cea24f7ec3",
		"quay.io/calico/kube-controllers:v3.32.2": "quay.io/calico/kube-controllers@sha256:f241490840083743e747389af8d5067c961370bd5e2e2af6538db2f7e183f60b",
	}
	nodes := []nodeReport{{Name: "owned-node", Architecture: "amd64"}}
	for _, ids := range []map[string]string{indexes, platforms, {
		"quay.io/calico/cni:v3.32.2":              indexes["quay.io/calico/cni:v3.32.2"],
		"quay.io/calico/node:v3.32.2":             platforms["quay.io/calico/node:v3.32.2"],
		"quay.io/calico/kube-controllers:v3.32.2": indexes["quay.io/calico/kube-controllers:v3.32.2"],
	}} {
		images, err := verifyCalicoImageInventory(calicoImageControl(t, ids), nodes)
		if err != nil || len(images) != 9 {
			t.Fatalf("reviewed image inventory = %#v, %v", images, err)
		}
		for _, name := range []string{"cni", "node", "kube-controllers"} {
			reference := "quay.io/calico/" + name + ":v3.32.2"
			if images["calico-"+name] != ids[reference] || images["calico-"+name+"-index-pin"] != indexes[reference] || images["calico-"+name+"-amd64-pin"] != platforms[reference] {
				t.Fatal("observed identities and reviewed platform pins were conflated")
			}
		}
	}
	base := string(calicoImageControl(t, indexes))
	replace := func(old, replacement string) string {
		t.Helper()
		if !strings.Contains(base, old) {
			t.Fatalf("negative control did not select a fixture field: %q", old)
		}
		return strings.Replace(base, old, replacement, 1)
	}
	for _, test := range []struct {
		name, raw, want string
		nodes           []nodeReport
	}{
		{"malformed inventory", `{`, "decode Calico", nodes},
		{"missing nodes", base, "node inventory", nil},
		{"foreign node platform", base, "every node", []nodeReport{{Architecture: "arm64"}}},
		{"mixed node platforms", base, "every node", []nodeReport{{Architecture: "amd64"}, {Architecture: "arm64"}}},
		{"node count mismatch", base, "Pod inventory", []nodeReport{{Architecture: "amd64"}, {Architecture: "amd64"}}},
		{"no Pods", `{"items":[]}`, "Pod inventory", nodes},
		{"wrong Pod label", replace(`"k8s-app":"calico-node"`, `"k8s-app":"unrelated"`), "unexpected Pod", nodes},
		{"missing controller", replace(`"k8s-app":"calico-kube-controllers"`, `"k8s-app":"calico-node"`), "Pod inventory", nodes},
		{"unreviewed requested tag", replace(`"quay.io/calico/node:v3.32.2"`, `"quay.io/calico/node:v3.32.1"`), "resolved", nodes},
		{"tag-only identity", replace(indexes["quay.io/calico/node:v3.32.2"], "quay.io/calico/node:v3.32.2"), "resolved", nodes},
		{"different repository", replace(indexes["quay.io/calico/node:v3.32.2"], "example.invalid/calico/node@sha256:99b03fe91e8bfbcb153ae65ef4b701b24ce541ffdd74ff314eb041096008f7fd"), "resolved", nodes},
		{"unreviewed digest", replace(indexes["quay.io/calico/node:v3.32.2"], "quay.io/calico/node@sha256:"+strings.Repeat("a", 64)), "resolved", nodes},
		{"other platform manifest", replace(indexes["quay.io/calico/kube-controllers:v3.32.2"], "quay.io/calico/kube-controllers@sha256:c27f8f4cdb829a630f2d3c3fbf3f5bb9b6ecf98f9d9183cc2d477cbf43664177"), "resolved", nodes},
		{"missing status", replace(indexes["quay.io/calico/node:v3.32.2"], ""), "resolved", nodes},
		{"status belongs to another container", replace(`"name":"install-cni"`, `"name":"other-container"`), "resolved", nodes},
		{"missing requested CNI image", replace(`"initContainers":[{"image":"quay.io/calico/cni:v3.32.2","name":"install-cni"}]`, `"initContainers":[]`), "was not resolved", nodes},
	} {
		t.Run(test.name, func(t *testing.T) {
			images, err := verifyCalicoImageInventory([]byte(test.raw), test.nodes)
			if err == nil || !strings.Contains(err.Error(), test.want) || images != nil {
				t.Fatalf("unqualified CNI image inventory returned %#v, %v", images, err)
			}
		})
	}
}
