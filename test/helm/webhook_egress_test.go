//go:build helm

package helm_test

import "testing"

func TestWebhookEgressMountIsEnabledRuntimeOnly(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		args := []string{"--set-string", "webhooks.egressConfigMap=receiver-policy-v1"}
		if enabled {
			args = append(args, "--set-string", "configEnv.CATALOG_WEBHOOKS_ENABLED=true", "--set-string", "secrets.webhookEncryption.name=receiver-key", "--set-string", "secrets.webhookEncryption.key=key")
		}
		objects := renderChart(t, args...)
		for _, role := range []string{"api", "worker", "migration"} {
			kind := "Deployment"
			if role == "migration" {
				kind = "Job"
			}
			resource := oneObject(t, objects, kind, role)
			pod := mapAt(t, resource, "spec", "template", "spec")
			container := asMap(t, asSlice(t, pod["containers"])[0])
			want := enabled && role != "migration"
			entry, present := environmentEntry(t, container, "CATALOG_WEBHOOKS_POLICY_FILE")
			if present != want {
				t.Fatalf("%s webhook env presence=%t enabled=%t", role, present, enabled)
			}
			if present && stringAt(t, entry, "value") != "/etc/open-agentic-catalog/webhooks/policy.json" {
				t.Fatal("webhook policy path is not fixed")
			}
			found := false
			for _, value := range asSlice(t, container["volumeMounts"]) {
				mount := asMap(t, value)
				if stringAt(t, mount, "name") == "webhook-egress" {
					found = true
					if mount["readOnly"] != true || stringAt(t, mount, "mountPath") != "/etc/open-agentic-catalog/webhooks" {
						t.Fatal("webhook policy mount is not scoped read-only")
					}
				}
			}
			if found != want {
				t.Fatalf("%s webhook mount presence=%t enabled=%t", role, found, enabled)
			}
			found = false
			for _, value := range asSlice(t, pod["volumes"]) {
				volume := asMap(t, value)
				if stringAt(t, volume, "name") == "webhook-egress" {
					found = true
					if stringAt(t, volume, "configMap", "name") != "receiver-policy-v1" {
						t.Fatal("wrong webhook policy ConfigMap")
					}
				}
			}
			if found != want {
				t.Fatalf("%s webhook volume presence=%t enabled=%t", role, found, enabled)
			}
		}
	}
}
