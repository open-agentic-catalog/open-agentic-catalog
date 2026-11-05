// Package api embeds the reviewed HTTP contracts and examples shipped with the service.
package api

import "embed"

// Files contains the OpenAPI contract, event schema, and reviewed JSON examples.
//
//go:embed openapi/control-v1.yaml examples/*.json schemas/*.json
var Files embed.FS

// ControlV1 is the exact checked-in OpenAPI document served by the API listener.
var ControlV1 = mustRead("openapi/control-v1.yaml")

// mustRead loads a required embedded asset and panics only when the compiled
// bundle violates its build invariant.
func mustRead(path string) []byte {
	content, err := Files.ReadFile(path)
	if err != nil {
		panic(err)
	}
	return content
}
