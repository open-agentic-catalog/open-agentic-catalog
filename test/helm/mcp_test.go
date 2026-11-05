//go:build helm

package helm_test

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestMCPIngressExactAPIPathsAndIndependentUI(t *testing.T) {
	baseline := renderChart(t)
	for _, ui := range []bool{false, true} {
		args := []string{"--set", "api.ingress.enabled=true,api.ingress.host=catalog.example.com,api.ingress.tlsSecretName=catalog-tls", "--set-string", "configEnv.CATALOG_MCP_ENABLED=true", "--set", "api.ingress.className=nginx"}
		if ui {
			args = append(args, "--values", filepath.Join(chartPath(t), "ci/ui-values.yaml"))
		}
		objects := renderChart(t, args...)
		ingress := oneObject(t, objects, "Ingress", "mcp")
		if stringAt(t, ingress, "spec", "ingressClassName") != "nginx" {
			t.Fatal("MCP ingress class was not retained")
		}
		tls := asMap(t, sliceAt(t, ingress, "spec", "tls")[0])
		if stringAt(t, tls, "secretName") != "catalog-tls" || !reflect.DeepEqual(sliceAt(t, tls, "hosts"), []any{"catalog.example.com"}) {
			t.Fatal("MCP ingress TLS host or Secret differs")
		}
		if ui {
			uiPaths := sliceAt(t, asMap(t, sliceAt(t, oneObject(t, objects, "Ingress", "ui"), "spec", "rules")[0]), "http", "paths")
			if len(uiPaths) != 2 || stringAt(t, asMap(t, uiPaths[0]), "path") != "/catalog" || stringAt(t, asMap(t, uiPaths[1]), "path") != "/docs" {
				t.Fatal("MCP ingress changed UI subpath or bundled documentation routing")
			}
		}
		paths := sliceAt(t, asMap(t, sliceAt(t, ingress, "spec", "rules")[0]), "http", "paths")
		if len(paths) != 2 {
			t.Fatal("MCP ingress must expose exactly two paths")
		}
		for i, value := range paths {
			p := asMap(t, value)
			if stringAt(t, p, "path") != []string{"/mcp", "/.well-known/oauth-protected-resource/mcp"}[i] || stringAt(t, p, "pathType") != "Exact" || stringAt(t, p, "backend", "service", "name") != stringAt(t, oneObject(t, objects, "Service", "api"), "metadata", "name") || stringAt(t, p, "backend", "service", "port", "name") != "http" {
				t.Fatal("MCP ingress lost exact API service routing")
			}
		}
		for _, component := range []string{"api", "worker"} {
			if !reflect.DeepEqual(mapAt(t, oneObject(t, baseline, "Deployment", component), "spec", "template", "spec"), mapAt(t, oneObject(t, objects, "Deployment", component), "spec", "template", "spec")) {
				t.Fatal("Ingress changed workload authority or ports")
			}
		}
		validateRenderedConfiguration(t, objects)
	}
}

func TestMCPIngressRefusesInvalidAuthorityAndRewrites(t *testing.T) {
	for name, settings := range map[string][]string{
		"disabled transport": {"--set-string", "configEnv.CATALOG_MCP_ENABLED=false"},
		"wrong host":         {"--set", "api.ingress.host=other.example.com"},
		"missing TLS":        {"--set", "api.ingress.tlsSecretName="},
		"plain HTTP":         {"--set-string", "configEnv.CATALOG_PUBLIC_BASE_URL=http://catalog.example.com"},
		"path":               {"--set-string", "configEnv.CATALOG_PUBLIC_BASE_URL=https://catalog.example.com/api"},
		"port":               {"--set-string", "configEnv.CATALOG_PUBLIC_BASE_URL=https://catalog.example.com:8443"},
		"query":              {"--set-string", "configEnv.CATALOG_PUBLIC_BASE_URL=https://catalog.example.com?rewrite=1"},
		"credentials":        {"--set-string", "configEnv.CATALOG_PUBLIC_BASE_URL=https://secret@catalog.example.com"},
		"invalid DNS host":   {"--set", "api.ingress.host=catalog..example.com"},
		"rewrite":            {"--set-string", `api.ingress.annotations.nginx\.ingress\.kubernetes\.io/rewrite-target=/`},
		"snippet":            {"--set-string", `api.ingress.annotations.nginx\.ingress\.kubernetes\.io/configuration-snippet=unsafe`},
	} {
		t.Run(name, func(t *testing.T) {
			args := []string{"template", testRelease, chartPath(t), "--set", "api.ingress.enabled=true,api.ingress.host=catalog.example.com,api.ingress.tlsSecretName=catalog-tls", "--set-string", "configEnv.CATALOG_MCP_ENABLED=true"}
			if _, err := helm(t, append(args, settings...)...); err == nil {
				t.Fatal("unsafe MCP ingress rendered")
			}
		})
	}
}
