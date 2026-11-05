//go:build functional

package httpserver

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestHTTPAuthoringUnicodeIdentityLimits(t *testing.T) {
	t.Parallel()
	s := newWebhookHTTPServer(t)
	t.Cleanup(s.Close)
	ns := requestJSON(t, s.URL, "POST", "/api/v1/namespaces", "admin", "unicode-ns", "", map[string]string{"slug": "unicode", "displayName": "Unicode"})
	requireStatus(t, ns, 201)
	var namespace catalog.Namespace
	decodeResult(t, ns, &namespace)
	identifier, version := strings.Repeat("x", 512), strings.Repeat("é", 128)
	create := catalog.CreateResourceInput{NamespaceID: namespace.ID, CanonicalIdentifier: identifier, Kind: catalog.KindAgent}
	created := requestJSON(t, s.URL, "POST", "/api/v1/resources", "admin", "unicode-resource", "", create)
	requireStatus(t, created, 201)
	var resource catalog.Resource
	decodeResult(t, created, &resource)
	if resource.CanonicalIdentifier != identifier {
		t.Fatal("resource identity changed")
	}
	input := catalog.CreateVersionInput{Version: version, Representations: []catalog.RepresentationInput{{Role: catalog.RepresentationPrimary, MediaType: "application/vnd.open-agentic-catalog.agent+json", Data: json.RawMessage(`{"name":"Unicode","framework":"custom"}`)}}}
	path := "/api/v1/resources/" + resource.ID + "/versions"
	first := requestJSON(t, s.URL, "POST", path, "admin", "unicode-version", "", input)
	requireStatus(t, first, 201)
	var envelope versionEvidenceResponse
	decodeResult(t, first, &envelope)
	if envelope.Version.Version != version {
		t.Fatal("version label changed")
	}
	read := requestJSON(t, s.URL, "GET", "/api/v1/resource-versions/"+envelope.Version.ID, "admin", "", "", nil)
	requireStatus(t, read, 200)
	var retained catalog.ResourceVersion
	decodeResult(t, read, &retained)
	if retained.Version != version {
		t.Fatal("readback changed the version")
	}
	replay := requestJSON(t, s.URL, "POST", path, "admin", "unicode-version", "", input)
	requireStatus(t, replay, 201)
	if string(replay.body) != string(first.body) || replay.header.Get("Idempotency-Replayed") != "true" {
		t.Fatal("Unicode version creation did not replay exactly")
	}
	create.CanonicalIdentifier = strings.Repeat("🙂", 512)
	unicodeResource := requestJSON(t, s.URL, "POST", "/api/v1/resources", "admin", "unicode-opaque-resource", "", create)
	requireStatus(t, unicodeResource, 201)
	decodeResult(t, unicodeResource, &resource)
	if resource.CanonicalIdentifier != create.CanonicalIdentifier {
		t.Fatal("Unicode identifier changed")
	}
	listed := requestJSON(t, s.URL, "GET", "/api/v1/resources?limit=1", "admin", "", "", nil)
	requireStatus(t, listed, 200)
	var resources cursorPage[catalog.Resource]
	decodeResult(t, listed, &resources)
	if len(resources.Items) != 1 || resources.NextCursor == "" {
		t.Fatal("resource continuation missing")
	}
	last := requestJSON(t, s.URL, "GET", "/api/v1/resources?limit=1&cursor="+url.QueryEscape(resources.NextCursor), "admin", "", "", nil)
	requireStatus(t, last, 200)
	var terminal cursorPage[catalog.Resource]
	decodeResult(t, last, &terminal)
	if len(terminal.Items) != 1 || terminal.NextCursor != "" || terminal.Items[0].ID == resources.Items[0].ID {
		t.Fatal("resource continuation duplicated or lost an identity")
	}
	identifiers := map[string]bool{resources.Items[0].CanonicalIdentifier: true, terminal.Items[0].CanonicalIdentifier: true}
	if !identifiers[identifier] || !identifiers[create.CanonicalIdentifier] {
		t.Fatal("resource continuation changed Unicode identifiers")
	}
	create.CanonicalIdentifier += "x"
	requireControlProblem(t, requestJSON(t, s.URL, "POST", "/api/v1/resources", "admin", "excess-resource", "", create), 400, catalog.CodeInvalidArgument)
	input.Version += "x"
	requireControlProblem(t, requestJSON(t, s.URL, "POST", path, "admin", "excess-version", "", input), 400, catalog.CodeInvalidArgument)
	page := requestJSON(t, s.URL, "GET", path, "admin", "", "", nil)
	requireStatus(t, page, 200)
	var versions cursorPage[catalog.ResourceVersion]
	decodeResult(t, page, &versions)
	if len(versions.Items) != 1 || versions.Items[0].Version != version {
		t.Fatal("refusal or replay created another version")
	}
}
