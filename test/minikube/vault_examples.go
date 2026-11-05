//go:build minikube

package main

import (
	"bytes"
	_ "embed"
	"errors"
	"io"

	"go.yaml.in/yaml/v3"
)

//go:embed fixtures/vault/catalog-sync.yaml
var vaultSyncExample []byte

//go:embed fixtures/vault/runtime-policy.hcl
var vaultRuntimePolicy string

//go:embed fixtures/vault/owner-policy.hcl
var vaultOwnerPolicy string

// vaultExampleObjects uses the published example with only namespace and server-address substitutions.
func vaultExampleObjects(selectedNamespace, address string) ([]kubernetesObject, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(vaultSyncExample))
	var objects []kubernetesObject
	for {
		var object map[string]any
		if err := decoder.Decode(&object); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, err
		}
		metadata, ok := object["metadata"].(map[string]any)
		if !ok {
			return nil, errors.New("vault example lacks metadata")
		}
		metadata["namespace"] = selectedNamespace
		if object["kind"] == "VaultConnection" {
			spec, ok := object["spec"].(map[string]any)
			if !ok {
				return nil, errors.New("vault example lacks connection spec")
			}
			spec["address"] = address
		}
		objects = append(objects, kubernetesObject(object))
	}
	if len(objects) != 10 {
		return nil, errors.New("unexpected Vault example resource inventory")
	}
	return objects, nil
}
