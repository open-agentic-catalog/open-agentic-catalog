package api

import (
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

func TestDirectoryQuerySchemas(t *testing.T) {
	document, err := openapi3.NewLoader().LoadFromData(ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path, field    string
		valid, invalid []string
	}{
		{"namespaces", "slug", []string{"tools", " Tools--1 ", "\u2028\u0130\u212a\u3000", strings.Repeat("s", 64)}, []string{"", " ", "-bad", "bad-", "bad_name", strings.Repeat("s", 65)}},
		{"namespaces", "displayName", []string{"Tools", " Mixed  Case ", strings.Repeat("界", 160), "\ufeff"}, []string{"", "\u2028\u3000", "bad\x00", strings.Repeat("界", 161)}},
		{"publishers", "displayName", []string{"Shared 日本語", " Same  Name "}, []string{"", "\u0085\u00a0", "\x00"}},
		{"publishers", "declaredDomain", []string{"example.com", " Example.COM. ", "\u2028\u0130\u212a.example\u3000", "xn--example.test"}, []string{"", " ", "single", "-bad.example", "bad..example", "example.com..", "https://example.com", "*.example", strings.Repeat("d", 64) + ".example"}},
	} {
		t.Run(tc.path+"/"+tc.field, func(t *testing.T) {
			var schema *openapi3.Schema
			for _, parameter := range document.Paths.Value("/api/v1/" + tc.path).Get.Parameters {
				if parameter.Value.Name == tc.field {
					schema = parameter.Value.Schema.Value
				}
			}
			if schema == nil {
				t.Fatal("missing filter schema")
			}
			for _, value := range tc.valid {
				if err := schema.VisitJSON(value, openapi3.EnableJSONSchema2020()); err != nil {
					t.Fatalf("valid %q: %v", value, err)
				}
			}
			for _, value := range tc.invalid {
				if err := schema.VisitJSON(value, openapi3.EnableJSONSchema2020()); err == nil {
					t.Fatalf("invalid %q accepted", value)
				}
			}
		})
	}
	// Adjacent internal hyphens already work in authoring and stable slugs.
	// The retained response and create input must describe that same syntax.
	for _, component := range []string{"Namespace", "CreateNamespace"} {
		if err := document.Components.Schemas[component].Value.Properties["slug"].Value.VisitJSON("tools--one", openapi3.EnableJSONSchema2020()); err != nil {
			t.Fatal(component, err)
		}
	}
}
