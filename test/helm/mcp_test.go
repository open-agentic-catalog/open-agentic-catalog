//go:build helm

package helm_test

import (
	"fmt"
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

func TestARDMCPIngressFollowsExactDiscoveryHost(t *testing.T) {
	for _, control := range []bool{false, true} {
		t.Run(fmt.Sprint(control), func(t *testing.T) {
			host := "discovery.example.com"
			if control {
				host = "catalog.example.com"
			}
			hosts := fmt.Sprintf(`[{"host":%q,"tenantId":"00000000-0000-7000-8000-000000000001","mode":"anonymous","publisherDomain":"catalog.example.com","mcpEnabled":true}]`, host)
			args := []string{"--set", "api.ingress.enabled=true,api.ingress.tlsSecretName=catalog-tls,api.ingress.host=" + host, "--set-string", "configEnv.CATALOG_MCP_ENABLED=" + fmt.Sprint(control), "--set-literal", "configEnv.CATALOG_DISCOVERY_HOSTS=" + hosts}
			objects := renderChart(t, args...)
			paths := sliceAt(t, asMap(t, sliceAt(t, oneObject(t, objects, "Ingress", "mcp"), "spec", "rules")[0]), "http", "paths")
			want := []string{"/ard/v1/mcp", "/.well-known/oauth-protected-resource/ard/v1/mcp"}
			if control {
				want = append([]string{"/mcp", "/.well-known/oauth-protected-resource/mcp"}, want...)
			}
			if len(paths) != len(want) {
				t.Fatal(paths)
			}
			for i, path := range paths {
				if stringAt(t, asMap(t, path), "path") != want[i] || stringAt(t, asMap(t, path), "pathType") != "Exact" {
					t.Fatal(path)
				}
			}
			validateRenderedConfiguration(t, objects)
		})
	}
	for _, mode := range []string{"disabled", "anonymous"} {
		hosts := fmt.Sprintf(`[{"host":"catalog.example.com","tenantId":"00000000-0000-7000-8000-000000000001","mode":%q,"publisherDomain":"catalog.example.com","mcpEnabled":false}]`, mode)
		_, err := helm(t, "template", testRelease, chartPath(t), "--set", "api.ingress.enabled=true,api.ingress.host=catalog.example.com,api.ingress.tlsSecretName=catalog-tls", "--set-string", "configEnv.CATALOG_MCP_ENABLED=false", "--set-literal", "configEnv.CATALOG_DISCOVERY_HOSTS="+hosts)
		if err == nil {
			t.Fatal("disabled ARD MCP exposed through ingress", mode)
		}
	}
}
