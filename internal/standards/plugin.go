// Plugin schema validation compiles pinned embedded schemas and implements the
// specification's name constraint without enabling remote schema retrieval.

package standards

import (
	"regexp"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const PluginManifestSchema = "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json"
const PluginMCPSchema = "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json"
const pluginNamePattern = `^(?!.*(?:--|\.\.))[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`

var pluginSchemas = sync.OnceValues(func() (map[string]*jsonschema.Schema, error) {
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	c.UseRegexpEngine(pluginRegexp)
	result := make(map[string]*jsonschema.Schema)
	for name, url := range map[string]string{"plugin": PluginManifestSchema, "mcp": PluginMCPSchema} {
		doc, err := readSchema("upstream/agent-plugins/agent-plugins-1.0.0-1fc1b627/schemas/1.0.0/" + name + ".schema.json")
		if err != nil {
			return nil, err
		}
		if err := c.AddResource(url, doc); err != nil {
			return nil, err
		}
		schema, err := c.Compile(url)
		if err != nil {
			return nil, err
		}
		result[name] = schema
	}
	server, err := c.Compile(PluginMCPSchema + "#/$defs/server")
	if err != nil {
		return nil, err
	}
	result["server"] = server
	return result, nil
})

// pluginNameRegexp implements only the published lookahead pattern. Go's
// strict end anchor also enforces the prose's exclusion of terminal newlines.
type pluginNameRegexp struct{ *regexp.Regexp }

// String returns the pinned Plugin name pattern represented by this custom
// matcher.
func (p pluginNameRegexp) String() string { return pluginNamePattern }

// MatchString enforces the Plugin name alphabet and rejects repeated dot or
// hyphen sequences.
func (p pluginNameRegexp) MatchString(s string) bool {
	return !strings.Contains(s, "--") && !strings.Contains(s, "..") && p.Regexp.MatchString(s)
}

// pluginRegexp substitutes the pinned lookahead-dependent name pattern with
// equivalent Go checks and compiles ordinary patterns normally.
func pluginRegexp(pattern string) (jsonschema.Regexp, error) {
	if pattern == pluginNamePattern {
		return pluginNameRegexp{regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)}, nil
	}
	return regexp.Compile(pattern)
}

// ValidatePluginManifest validates the unchanged strict authoring schema.
// Catalog admission, component semantics and runtime loading are separate.
func ValidatePluginManifest(raw []byte) error {
	schemas, err := pluginSchemas()
	if err != nil {
		return err
	}
	return validate(schemas["plugin"], raw)
}

// ValidatePluginMCP validates Plugin configuration, not a Registry server.json
// or Server Card. It neither contacts endpoints nor executes commands.
func ValidatePluginMCP(raw []byte) error {
	schemas, err := pluginSchemas()
	if err != nil {
		return err
	}
	return validate(schemas["mcp"], raw)
}

// ValidatePluginMCPServer attributes schema findings to one component without
// relaxing whole-package admission or maintaining a separate server schema.
func ValidatePluginMCPServer(raw []byte) error {
	schemas, err := pluginSchemas()
	if err != nil {
		return err
	}
	return validate(schemas["server"], raw)
}
