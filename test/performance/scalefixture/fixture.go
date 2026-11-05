// Package scalefixture defines the versioned synthetic scale corpus. It is test
// tooling, not an import API or a model of production traffic.
package scalefixture

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/demodata"
)

// Version identifies the fixed corpus used to compare qualification results.
const Version = 2

// Tenant is the synthetic tenant provisioned by the isolated scale harness.
const Tenant = "00000000-0000-7000-8000-000000000001"

// Publisher labels synthetic records without introducing variable facet values.
const Publisher = "Scale publisher"

// ValidSize reports whether n is one of the supported qualification populations.
func ValidSize(n int) bool { return n == 1000 || n == 10000 || n == 100000 }

// Count excludes the three smoke records in each population.
func Count(n int, indexed bool) int {
	if indexed {
		return n/10 - 3
	}
	return n*9/10 - 3
}

// Kind cycles nonnegative corpus indexes across the four dataset-v2 kinds.
func Kind(i int) catalog.Kind {
	return []catalog.Kind{catalog.KindAgent, catalog.KindMCPServer, catalog.KindSkill, catalog.KindPlugin}[i%4]
}

var pluginSamples = sync.OnceValue(func() []demodata.PreparedResource {
	var selected []demodata.PreparedResource
	for _, item := range demodata.DefaultPlan("expanded", "https://scale.example/packages").Resources {
		if item.Resource.Kind == catalog.KindPlugin {
			selected = append(selected, item)
		}
	}
	if len(selected) != 3 {
		panic("invalid embedded scale Plugin fixtures")
	}
	return selected
})

// PluginArchive supplies only the embedded, digest-bound sample packages.
func PluginArchive(url string) ([]byte, bool) {
	for _, item := range pluginSamples() {
		if url == item.Representation.URL {
			return append([]byte(nil), item.Archive...), true
		}
	}
	return nil, false
}

// Name gives each population a stable, zero-padded listing label for ordering tests.
func Name(i int, indexed bool) string {
	population := "Local"
	if indexed {
		population = "Indexed"
	}
	return fmt.Sprintf("Scale %s %06d", population, i)
}

// Identifier keeps local and indexed records distinct even when their indexes match.
func Identifier(i int, indexed bool) string {
	population := "local"
	if indexed {
		population = "indexed"
	}
	return fmt.Sprintf("urn:air:scale.example:scale:%s-%06d", population, i)
}

// Tags creates fixed facet groups and a small terminal-page subset in each population.
func Tags(i int) []string {
	tags := []string{"scale", fmt.Sprintf("group-%d", i%5)}
	if i < 7 {
		tags = append(tags, "terminal")
	}
	return tags
}

// Description gives every record a common search term and every hundredth a rare one.
func Description(i int) string {
	text := "Synthetic scale corpus commonterm"
	if i%100 == 0 {
		text += " rareterm"
	}
	return text
}

// Representation deliberately reuses small documents; every fiftieth non-MCP
// record has larger content. Listing identity and metadata remain distinct.
func Representation(i int) catalog.RepresentationInput {
	description := "Synthetic scale representation."
	if i%50 == 0 && Kind(i) != catalog.KindMCPServer {
		description += strings.Repeat(" Metadata only.", 128)
	}
	input := catalog.RepresentationInput{Role: catalog.RepresentationPrimary}
	switch Kind(i) {
	case catalog.KindAgent:
		input.MediaType = adapter.MediaA2AAgentCard
		quoted := strconv.Quote(description)
		input.Data = json.RawMessage(fmt.Sprintf(`{"name":"Scale agent","description":%s,"version":"1.0.0","supportedInterfaces":[{"url":"https://scale.example/agent","protocolBinding":"JSONRPC","protocolVersion":"1.0"}],"capabilities":{},"defaultInputModes":["application/json"],"defaultOutputModes":["application/json"],"skills":[{"id":"scale","name":"Scale","description":"Metadata only","tags":["scale"]}]}`, quoted))
	case catalog.KindMCPServer:
		input.MediaType = adapter.MediaMCPRegistry
		input.Data = json.RawMessage(`{"name":"io.example/scale","description":"Synthetic scale representation.","version":"1.0.0","remotes":[{"type":"streamable-http","url":"https://scale.example/mcp"}]}`)
	case catalog.KindSkill:
		input.MediaType = adapter.MediaSkillMarkdown
		input.Content = "---\nname: scale-skill\ndescription: Metadata only.\n---\n# Instructions\n" + description + "\n"
	case catalog.KindPlugin:
		item := pluginSamples()[i/4%len(pluginSamples())]
		digest := item.BodyDigest
		input.MediaType, input.URL, input.ExpectedDigest = catalog.PluginMediaZIP, item.Representation.URL, &digest
	}
	return input
}

// Entry builds an indexed ARD entry using the same deterministic metadata as local
// records, advertising Skill JSON metadata instead of embedding Markdown text.
func Entry(i int) map[string]any {
	r := Representation(i)
	entry := map[string]any{"identifier": Identifier(i, true), "version": "1.0.0", "displayName": Name(i, true), "description": Description(i), "tags": Tags(i), "publisher": Publisher, "type": r.MediaType}
	switch {
	case Kind(i) == catalog.KindPlugin:
		item := pluginSamples()[i/4%len(pluginSamples())]
		entry["url"] = r.URL
		entry["metadata"] = map[string]any{"packageDigest": item.BodyDigest, "packageSize": len(item.Archive)}
	case len(r.Data) > 0:
		entry["data"] = r.Data
	default:
		// ARD embeds structured metadata, not a bare Markdown string. Local
		// Skills retain SKILL.md; indexed Skills advertise metadata only.
		entry["type"] = adapter.MediaSkillJSON
		entry["data"] = map[string]string{"name": "scale-skill", "description": "Metadata only."}
	}
	return entry
}
