package clientcompat_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"go.yaml.in/yaml/v3"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/api"
	client "github.com/open-agentic-catalog/open-agentic-catalog/test/clientcompat/go/generated"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Schema validation is independent of generated decoders: successfully decoding
// into a Go struct does not establish OpenAPI 3.1 validity.
func component(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	var document map[string]any
	must(t, yaml.Unmarshal(api.ControlV1, &document))
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	must(t, c.AddResource("https://catalog.example/control.json", document))
	schema, err := c.Compile("https://catalog.example/control.json#/components/schemas/" + name)
	must(t, err)
	return schema
}

func TestPrivateTrustAssessmentCodecs(t *testing.T) {
	entry := []byte(`{ "identifier":"urn:air:peer.example:agents:one", "type":"application/a2a-agent-card+json", "url":"https://peer.example/card", "score":100, "source":"https://peer.example/search" }`)
	digest := fmt.Sprintf("%x", sha256.Sum256(entry))
	request := fmt.Appendf(nil, `{"sourceRevision":1,"identifier":"urn:air:peer.example:agents:one","versionPresent":false,"mediaType":"application/a2a-agent-card+json","delivery":"url","exactEntryDigest":"sha256:%s","text":"one","reason":"Assess selected registration"}`, digest)
	requestSchema := component(t, "TrustAssessmentRequest")
	roundTrip[client.TrustAssessmentRequest](t, requestSchema, request)
	if requestSchema.Validate(exactJSON(t, bytes.Replace(request, []byte(`"versionPresent":false`), []byte(`"versionPresent":false,"version":""`), 1))) == nil {
		t.Fatal("absent version acquired an explicit value")
	}
	na := map[string]string{"state": "not_assessed", "code": "prerequisite_failed"}
	// This is an insufficient assessment, not a fabricated signed positive.
	result := map[string]any{
		"sourceId": tenant, "sourceRevision": 1, "policyName": "owned", "policyDigest": "sha256:" + strings.Repeat("a", 64), "authority": "owned",
		"exactEntryDigest": "sha256:" + digest, "mediaType": "application/a2a-agent-card+json", "delivery": "url",
		"softwareTrust": "not_assessed", "artifactIntegrity": "not_assessed", "rights": "not_assessed",
		"assessment": map[string]any{"profile": "ans-registration-v1", "policyDigest": strings.Repeat("b", 64), "entryDigest": digest, "checkedAt": "2026-10-09T14:31:24Z", "currentRegistration": false,
			"receipt": map[string]string{"state": "unavailable", "code": "receipt_missing"}, "producer": na, "log": na, "checkpoint": na, "inclusion": na, "target": na, "status": na},
		"evidence": map[string]any{"profile": "ans-registration-v1", "entry": base64.StdEncoding.EncodeToString(entry), "receipt": "", "checkpoint": "", "statusToken": ""},
	}
	body, err := json.Marshal(map[string]any{"result": result, "current": false, "applicability": "negative_or_insufficient"})
	must(t, err)
	responseSchema := component(t, "TrustAssessmentResponse")
	roundTrip[client.TrustAssessmentResponse](t, responseSchema, body)
	if responseSchema.Validate(exactJSON(t, bytes.Replace(body, []byte(`"softwareTrust":"not_assessed"`), []byte(`"softwareTrust":"trusted"`), 1))) == nil {
		t.Fatal("broad software trust claim accepted")
	}
}

func TestBoundedConditionalWireCodecs(t *testing.T) {
	trustSchema, representationSchema := component(t, "TrustAssessmentRequest"), component(t, "RepresentationInput")
	for seed := 0; seed < 16; seed++ {
		request := map[string]any{"sourceRevision": seed + 1, "identifier": fmt.Sprintf("urn:air:example.com:wire:item-%d", seed),
			"versionPresent": seed%2 == 0, "mediaType": "application/json", "delivery": []string{"data", "url"}[seed%2],
			"exactEntryDigest": "sha256:" + strings.Repeat("a", 64), "reason": "Check generated wire semantics"}
		if seed%2 == 0 {
			request["version"] = []string{"", "1.2.3"}[(seed/2)%2]
		}
		if seed%3 == 0 {
			request["observationId"] = tenant
		} else {
			request["text"] = fmt.Sprintf("Entry %d é 日本語", seed)
		}
		wire, err := json.Marshal(request)
		must(t, err)
		roundTrip[client.TrustAssessmentRequest](t, trustSchema, wire)
		invalid := make(map[string]any, len(request))
		for key, value := range request {
			invalid[key] = value
		}
		if seed%2 == 0 {
			delete(invalid, "version")
		} else {
			invalid["version"] = ""
		}
		invalidWire, err := json.Marshal(invalid)
		must(t, err)
		if trustSchema.Validate(exactJSON(t, invalidWire)) == nil {
			t.Fatal("conditional version neighbor unexpectedly valid", seed)
		}
		representation := map[string]any{"role": "primary", "mediaType": "application/json"}
		switch seed % 4 {
		case 0:
			representation["data"] = map[string]any{"nested": []any{nil, false, "", seed}}
		case 1:
			representation["content"] = fmt.Sprintf("Native é %d\n", seed)
		case 2:
			representation["url"] = fmt.Sprintf("https://resource.example/card-%d.json", seed)
		case 3:
			representation["mediaType"] = "application/agent-skills+zip"
			representation["packageData"] = base64.StdEncoding.EncodeToString([]byte{0, 1, byte(seed), 255})
			representation["handoff"] = map[string]any{"git": map[string]any{"repository": "https://example.com/skill.git", "commit": strings.Repeat("a", 40), "directory": "skill"}}
		}
		wire, err = json.Marshal(representation)
		must(t, err)
		roundTrip[client.RepresentationInput](t, representationSchema, wire)
		if representationSchema.Validate(map[string]any{"role": "primary", "mediaType": "application/json", "data": map[string]any{}, "content": "conflicting delivery"}) == nil {
			t.Fatal("conflicting delivery unexpectedly valid")
		}
	}
}

func exactJSON(t *testing.T, data []byte) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var result any
	must(t, decoder.Decode(&result))
	return result
}

func roundTrip[T any](t *testing.T, schema *jsonschema.Schema, data []byte) T {
	t.Helper()
	must(t, schema.Validate(exactJSON(t, data)))
	got := decode[T](t, data)
	encoded, err := json.Marshal(got)
	must(t, err)
	must(t, schema.Validate(exactJSON(t, encoded)))
	if !reflect.DeepEqual(exactJSON(t, data), exactJSON(t, encoded)) {
		t.Fatalf("codec changed fields: before=%s after=%s", data, encoded)
	}
	return got
}

func TestResourceKindsAndPluginEvidenceCodecs(t *testing.T) {
	schema := component(t, "Resource")
	for _, kind := range []string{"agent", "mcp_server", "skill", "plugin"} {
		t.Run(kind, func(t *testing.T) {
			data := fmt.Appendf(nil, `{"id":%q,"tenantId":%q,"namespaceId":%q,"canonicalIdentifier":"urn:air:example:compat:item","kind":%q,"createdBy":"admin","revision":1,"createdAt":"2026-09-13T12:00:00Z","updatedAt":"2026-09-13T12:00:00Z"}`, tenant, tenant, tenant, kind)
			got := roundTrip[client.Resource](t, schema, data)
			if string(got.Kind) != kind {
				t.Fatal("kind changed")
			}
		})
	}
	data := fmt.Appendf(nil, `{"rootDirectory":"","expandedSize":2,"directories":[],"files":[{"path":"plugin.json","size":2,"digest":"sha256:%s","executable":false}],"documents":[{"path":"plugin.json","content":"{}"}],"components":[{"kind":"skill","name":"helper","path":"skills/helper/SKILL.md","findings":[]}],"assessments":{"malware":"not_assessed","secrets":"not_assessed","license":"not_assessed","sbom":"not_assessed","provenance":"not_assessed"}}`, strings.Repeat("a", 64))
	roundTrip[client.PluginInspection](t, component(t, "PluginInspection"), data)
}

func TestExternalSearchCodecs(t *testing.T) {
	// Local results are validated and decoded in the real in-memory workflow.
	// External fixtures exercise serialization, not a second federation engine.
	schema := component(t, "CatalogSearchResult")
	for _, kind := range []string{"indexed", "live"} {
		t.Run(kind, func(t *testing.T) {
			evidence := fmt.Sprintf(`"indexed":{"observationGroupId":"observation-group-v1:sha256:%s","normalizedEntryDigest":"sha256:%s","sourceIds":[%q],"sourceCount":1,"sourcesTruncated":false,"conflict":false,"sameSourceConflict":false}`, strings.Repeat("a", 64), strings.Repeat("b", 64), tenant)
			if kind == "live" {
				evidence = fmt.Sprintf(`"live":{"sourceId":%q,"source":"https://catalog.example/ard","entry":{"future":{"empty":[],"null":null,"enabled":false}}}`, tenant)
			}
			data := fmt.Appendf(nil, `{"id":"%s:opaque","type":%q,"identifier":"urn:air:example:compat:plugin","versionPresent":false,"kind":"plugin","displayName":"Plugin","tags":[],"score":0,%s}`, kind, kind, evidence)
			got := roundTrip[client.CatalogSearchResult](t, schema, data)
			if got.Version != nil {
				t.Fatal("omitted version became present")
			}
			invalid := bytes.Replace(data, []byte(`"versionPresent":false`), []byte(`"versionPresent":false,"version":null`), 1)
			if schema.Validate(exactJSON(t, invalid)) == nil {
				t.Fatal("null version accepted")
			}
		})
	}
}

func TestEvidencePrecisionBoundary(t *testing.T) {
	// These deliberately partial projections characterize the generator, not
	// valid API exchanges. The workflow test proves valid raw write/read-back.
	data := []byte(`{"role":"primary","mediaType":"application/json","data":{"future":{"number":9007199254740993,"values":[null,false,""]}}}`)
	for _, tc := range []struct {
		name      string
		target    any
		useNumber bool
		rounded   bool
	}{
		{"representation", &client.Representation{}, false, true},
		{"representation-use-number", &client.Representation{}, true, false},
		{"input", &client.RepresentationInput{}, false, true},
		{"input-use-number", &client.RepresentationInput{}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := json.NewDecoder(bytes.NewReader(data))
			if tc.useNumber {
				d.UseNumber()
			}
			must(t, d.Decode(tc.target))
			encoded, err := json.Marshal(tc.target)
			must(t, err)
			if bytes.Contains(encoded, []byte("9007199254740992")) != tc.rounded {
				t.Fatalf("precision behavior changed; review documented boundary: %s", encoded)
			}
			if !tc.rounded && !bytes.Contains(encoded, []byte("9007199254740993")) {
				t.Fatal("exact number missing")
			}
		})
	}
	body := append([]byte(`{"version":"1.0.0","representations":[`), data...)
	body = append(body, []byte(`]}`)...)
	schema := component(t, "CreateVersion")
	must(t, schema.Validate(exactJSON(t, body)))
	if schema.Validate(exactJSON(t, bytes.Replace(body, []byte(`"version"`), []byte(`"label"`), 1))) == nil {
		t.Fatal("mislabeled request accepted")
	}
	r, err := client.NewCreateResourceVersionRequestWithBody("https://catalog.example", decode[client.ResourceId](t, []byte(fmt.Sprintf("%q", tenant))), &client.CreateResourceVersionParams{IdempotencyKey: "raw-body"}, "application/json", bytes.NewReader(body))
	must(t, err)
	defer func() { must(t, r.Body.Close()) }()
	got, err := io.ReadAll(r.Body)
	must(t, err)
	if !bytes.Equal(got, body) || r.Header.Get("Content-Type") != "application/json" {
		t.Fatal("raw request changed")
	}
}

func TestBoundedUpstreamContinuationCodecs(t *testing.T) {
	// Validate independently of generated enum decoding, then prove that the
	// partial flag, useful result and source-bound warning survive serialization.
	for _, status := range []string{"continuation_unavailable", "insufficient_evidence"} {
		diagnostic := fmt.Sprintf(`{"sourceId":%q,"status":%q,"message":"Bounded live candidate diagnostic."}`, tenant, status)
		live := fmt.Sprintf(`{"sourceId":%q,"source":"https://peer.example/ard/","entry":{"identifier":"urn:air:peer.example:agents:helper"}}`, tenant)
		catalogBody := fmt.Appendf(nil, `{"items":[{"id":"live:opaque","type":"live","identifier":"urn:air:peer.example:agents:helper","versionPresent":false,"kind":"agent","displayName":"Peer helper","tags":[],"score":60,"live":%s}],"durableTotal":0,"liveCount":1,"profile":"core-lexical-v1","generation":"sha256:%s","tier":"core","partial":true,"diagnostics":[%s]}`, live, strings.Repeat("a", 64), diagnostic)
		catalogSchema := component(t, "CatalogSearchResponse")
		roundTrip[client.CatalogSearchResponse](t, catalogSchema, catalogBody)
		entryDigest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(`{"identifier":"urn:air:peer.example:agents:helper"}`)))
		liveBody := fmt.Appendf(nil, `{"results":[{"identifier":"urn:air:peer.example:agents:helper","versionPresent":false,"score":91,"sourceId":%q,"sourceRevision":1,"exactEntryDigest":%q,"source":"https://peer.example/ard/","entry":{"identifier":"urn:air:peer.example:agents:helper"}}],"partial":true,"diagnostics":[%s]}`, tenant, entryDigest, diagnostic)
		liveSchema := component(t, "LiveSearchResponse")
		roundTrip[client.LiveSearchResponse](t, liveSchema, liveBody)
		for _, tc := range []struct {
			name   string
			schema *jsonschema.Schema
			body   []byte
		}{{"CatalogSearchResponse", catalogSchema, catalogBody}, {"LiveSearchResponse", liveSchema, liveBody}} {
			t.Run(tc.name, func(t *testing.T) {
				invalid := bytes.Replace(tc.body, []byte(`"`+status+`"`), []byte(`"unknown_continuation"`), 1)
				if tc.schema.Validate(exactJSON(t, invalid)) == nil {
					t.Fatal("undeclared diagnostic status accepted")
				}
			})
		}
	}
}
