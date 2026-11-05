//go:build minikube

// Model-fixture contracts need no Kubernetes cluster or downloaded weights.
package main

import (
	"strings"
	"testing"
)

func TestSemanticModelIdentity(t *testing.T) {
	inputs := map[string]string{"CATALOG_EMBEDDING_MODEL": "fixture", "CATALOG_EMBEDDING_REVISION": "pinned"}
	valid := `{"served_model_name":"fixture","model_sha":"pinned","model_dtype":"float32","max_input_length":256,"auto_truncate":false,"version":"1.9.4","model_type":{"embedding":{"pooling":"mean"}}}`
	if !validSemanticModelInfo([]byte(valid), inputs) {
		t.Fatal("valid profile rejected")
	}
	for _, bad := range []string{"{}", strings.Replace(valid, "pinned", "drift", 1), strings.Replace(valid, "false", "true", 1), strings.Replace(valid, "256", "512", 1), strings.Replace(valid, "mean", "cls", 1), strings.Replace(valid, "float32", "float16", 1)} {
		if validSemanticModelInfo([]byte(bad), inputs) {
			t.Fatal("incompatible model accepted")
		}
	}
}
