package standards

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPluginManifestNameAndSchema(t *testing.T) {
	for _, tc := range []struct {
		name  string
		valid bool
	}{
		{"a", true}, {"acme.tools", true}, {"a.-b", true}, {"a-.b", true}, {strings.Repeat("a", 64), true},
		{"", false}, {"a..b", false}, {"a--b", false}, {"a.", false}, {".a", false}, {"A", false}, {"a\n", false}, {"a\r", false}, {"é", false}, {strings.Repeat("a", 65), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := json.Marshal(map[string]any{"$schema": PluginManifestSchema, "name": tc.name})
			if got := ValidatePluginManifest(b) == nil; got != tc.valid {
				t.Fatalf("valid=%v want %v", got, tc.valid)
			}
		})
	}
	if _, err := pluginRegexp("(?=new-pattern)"); err == nil {
		t.Fatal("unsupported pattern accepted")
	}
	engine, err := pluginRegexp(pluginNamePattern)
	if err != nil || engine.String() != pluginNamePattern {
		t.Fatal("pattern identity lost")
	}
}

func TestPluginSchemaAdmissionBoundaries(t *testing.T) {
	for _, tc := range []struct {
		extra string
		valid bool
	}{
		{`,"version":"","homepage":"not a URL","license":"custom"`, true},
		{`,"extensions":{"com.example":{"nested":[null,true]}}`, true},
		{`,"extensions":false`, false}, {`,"unknown":true`, false}, {`,"author":{"unknown":"x"}`, false},
	} {
		b := []byte(`{"$schema":"` + PluginManifestSchema + `","name":"a"` + tc.extra + `}`)
		if got := ValidatePluginManifest(b) == nil; got != tc.valid {
			t.Fatalf("%s valid=%v", tc.extra, got)
		}
	}
	for _, tc := range []struct {
		server string
		valid  bool
	}{
		{``, true}, {`"a":{"type":"stdio","command":"node","cwd":"${PLUGIN_DATA}"}`, true},
		{`"a":{"type":"sse","url":"https://example.com"}`, true},
		{`"a":{"type":"stdio","command":"node","cwd":"${PLUGIN_ROOT}\n"}`, false},
		{`"a":{"type":"stdio","command":"node","env":{"PLUGIN_ROOT":"bad"}}`, false},
		{`"a":{"type":"stdio","command":"node","extra":true}`, false},
	} {
		b := []byte(`{"$schema":"` + PluginMCPSchema + `","mcpServers":{` + tc.server + `}}`)
		if got := ValidatePluginMCP(b) == nil; got != tc.valid {
			t.Fatalf("%s valid=%v", tc.server, got)
		}
	}
}
