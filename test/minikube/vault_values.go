//go:build minikube

package main

import "strings"

func vaultImageValues(image string) map[string]any {
	repository, tag, _ := strings.Cut(image, ":")
	return map[string]any{"repository": repository, "tag": tag, "pullPolicy": "Never"}
}
func vaultServerValues(image string) map[string]any {
	config := `ui = false
disable_mlock = true
listener "tcp" {
 address = "[::]:8200"
 cluster_address = "[::]:8201"
 tls_cert_file = "/vault/tls/tls.crt"
 tls_key_file = "/vault/tls/tls.key"
}
storage "raft" { path = "/vault/data" }
service_registration "kubernetes" {}
`
	return map[string]any{
		"global": map[string]any{"tlsDisable": false}, "injector": map[string]any{"enabled": false}, "csi": map[string]any{"enabled": false}, "ui": map[string]any{"enabled": false},
		"server": map[string]any{
			"image": vaultImageValues(image), "dev": map[string]any{"enabled": false}, "standalone": map[string]any{"enabled": false},
			"resources":   map[string]any{"requests": map[string]string{"cpu": "100m", "memory": "256Mi"}, "limits": map[string]string{"cpu": "1", "memory": "512Mi"}},
			"dataStorage": map[string]any{"enabled": true, "size": "1Gi"}, "authDelegator": map[string]any{"enabled": true},
			"extraEnvironmentVars": map[string]string{"VAULT_CACERT": "/vault/tls/ca.pem"},
			"volumes":              []any{map[string]any{"name": "vault-tls", "secret": map[string]any{"secretName": "vault-tls", "defaultMode": 288}}},
			"volumeMounts":         []any{map[string]any{"name": "vault-tls", "mountPath": "/vault/tls", "readOnly": true}},
			"readinessProbe":       map[string]any{"enabled": true, "path": "/v1/sys/health?standbyok=true", "port": 8200},
			"ha":                   map[string]any{"enabled": true, "replicas": 1, "apiAddr": "https://" + vaultRelease + "." + vaultNamespace + ".svc:8200", "raft": map[string]any{"enabled": true, "setNodeId": true, "config": config}},
		}}
}
func vaultOperatorValues(images map[string]string) map[string]any {
	return map[string]any{
		"controller": map[string]any{"replicas": 1, "kubeRbacProxy": map[string]any{"image": vaultImageValues(images["proxy"])}, "manager": map[string]any{
			"image": vaultImageValues(images["vso"]), "clientCache": map[string]any{"persistenceModel": "none"},
			"globalTransformationOptions": map[string]any{"excludeRaw": true}, "globalVaultAuthOptions": map[string]any{"allowDefaultGlobals": false}, "extraArgs": []string{"--global-vault-auth-options="}}},
		"defaultVaultConnection": map[string]any{"enabled": false}, "defaultAuthMethod": map[string]any{"enabled": false}, "csi": map[string]any{"enabled": false}, "tests": map[string]any{"enabled": false}, "telemetry": map[string]any{"serviceMonitor": map[string]any{"enabled": false}},
	}
}
func vaultRuntimeSecret(name string, data map[string]string) kubernetesObject {
	templates := map[string]any{"vault-kv-version": map[string]string{"text": `{{ get .Metadata "version" }}`}}
	if name == vaultSourceSecret {
		templates["bearer.token"] = map[string]string{"text": `{{ get .Secrets "token" }}`}
		templates["bearer.json"] = map[string]string{"text": `{{ dict "schemaVersion" 1 "tenantId" (get .Secrets "tenantId") "revision" (printf "%s-kv-%v" (get .Secrets "epoch") (get .Metadata "version")) "origin" (get .Secrets "origin") "type" "bearer" "secretFile" "bearer.token" | toJson }}`}
	} else {
		for key := range data {
			templates[key] = map[string]string{"text": `{{ get .Secrets "` + key + `" }}`}
		}
	}
	return kubernetesObject{"apiVersion": "secrets.hashicorp.com/v1beta1", "kind": "VaultStaticSecret", "metadata": map[string]string{"name": name, "namespace": namespace}, "spec": map[string]any{
		"vaultAuthRef": "catalog-vault-runtime", "type": "kv-v2", "mount": "catalog", "path": name, "refreshAfter": "5s", "hmacSecretData": true,
		"destination": map[string]any{"create": true, "overwrite": false, "name": name, "transformation": map[string]any{"excludeRaw": true, "excludes": []string{".*"}, "templates": templates}}}}
}
