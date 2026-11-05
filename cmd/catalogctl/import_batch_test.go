package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/open-agentic-catalog/open-agentic-catalog/api"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func importBatchTestInput(t *testing.T, count int) (string, importManifest) {
	t.Helper()
	dir, single := importTestInput(t)
	manifest := importManifest{Format: 1}
	for index := range count {
		item := single.Resources[0]
		item.Key = fmt.Sprintf("item-%d", index)
		item.CanonicalIdentifier = "custom:" + item.Key
		if index%2 == 1 {
			item.NamespaceID = "00000000-0000-7000-8000-000000000011"
		}
		manifest.Resources = append(manifest.Resources, item)
	}
	return dir, manifest
}

func prepareImportBatchTest(t *testing.T, dir string, manifest importManifest) (importBatchPlan, error) {
	t.Helper()
	filename := writeImportTestFile(t, dir, "import.yaml", importTestJSON(t, manifest), 0600)
	return prepareImportFile(t.Context(), filename, 2<<20)
}

func TestImportBatchBoundsAndWholeIntent(t *testing.T) {
	dir, manifest := importBatchTestInput(t, importMaxResources)
	batch, err := prepareImportBatchTest(t, dir, manifest)
	if err != nil || len(batch.Items) != 64 || !batch.Preview.LocallyValid {
		t.Fatalf("valid maximum batch: %v", err)
	}
	total, steps := 0, 0
	for index, item := range batch.Items {
		if item.Preview.Key != fmt.Sprintf("item-%d", index) {
			t.Fatal("manifest order changed")
		}
		for _, body := range item.Bodies {
			total += len(body)
			steps++
		}
	}
	if steps != 256 || total != batch.Preview.PreparedBytes {
		t.Fatal("encoded aggregate accounting changed")
	}
	receipt, err := newImportReceipt(batch, "http://127.0.0.1", importTestPrincipal)
	if err != nil || len(receipt.Steps) != 256 {
		t.Fatal("maximum receipt missing steps")
	}
	raw := importTestJSON(t, receipt)
	if _, err := decodeImportReceipt(importTestJSON(t, importReceiptFile{Receipt: receipt, Digest: importDigest(raw)})); err != nil {
		t.Fatal(err)
	}
	// One private receipt binds earlier assets and the whole original item list.
	original := importIntent(batch)
	writeImportTestFile(t, dir, "agent.json", []byte(`{"name":"Changed earlier input","framework":"custom"}`), 0600)
	changed, err := prepareImportBatchTest(t, dir, manifest)
	if err != nil || importIntent(changed) == original {
		t.Fatal("changed earlier asset retained intent")
	}
	manifest.Resources = append(manifest.Resources, manifest.Resources[0])
	if _, err := prepareImportBatchTest(t, dir, manifest); err == nil {
		t.Fatal("65 resources accepted")
	}
}

func TestImportBatchDuplicatesAndRelationships(t *testing.T) {
	tests := []struct {
		name   string
		change func(*importManifest)
		valid  bool
	}{
		{"duplicate key", func(m *importManifest) { m.Resources[1].Key = m.Resources[0].Key }, false},
		{"trimmed duplicate", func(m *importManifest) {
			m.Resources[1].CanonicalIdentifier = "  " + m.Resources[0].CanonicalIdentifier + "  "
		}, false},
		{"AIR authority duplicate", func(m *importManifest) {
			m.Resources[0].CanonicalIdentifier = "urn:air:CATALOG.EXAMPLE.COM:agent:one"
			m.Resources[1].CanonicalIdentifier = "urn:air:catalog.example.com:agent:one"
		}, false},
		{"earlier version", func(m *importManifest) {
			m.Resources[1].Listing.Relationships = []catalog.Relationship{{Type: " DEPENDS_ON ", TargetIdentifier: " custom:item-0 ", TargetVersion: " release candidate "}}
		}, true},
		{"forward resource", func(m *importManifest) {
			m.Resources[0].Listing.Relationships = []catalog.Relationship{{Type: "depends_on", TargetIdentifier: m.Resources[1].CanonicalIdentifier}}
		}, false},
		{"self", func(m *importManifest) {
			m.Resources[0].Listing.Relationships = []catalog.Relationship{{Type: "depends_on", TargetIdentifier: m.Resources[0].CanonicalIdentifier}}
		}, false},
		{"wrong earlier version", func(m *importManifest) {
			m.Resources[1].Listing.Relationships = []catalog.Relationship{{Type: "depends_on", TargetIdentifier: m.Resources[0].CanonicalIdentifier, TargetVersion: "different"}}
		}, false},
		{"external target", func(m *importManifest) {
			m.Resources[1].Listing.Relationships = []catalog.Relationship{{Type: "recommended_with", TargetIdentifier: "custom:existing", TargetVersion: "prior"}}
		}, true},
		{"invalid AIR", func(m *importManifest) {
			m.Resources[1].Listing.Relationships = []catalog.Relationship{{Type: "depends_on", TargetIdentifier: "urn:air:invalid"}}
		}, false},
		{"unsupported type", func(m *importManifest) {
			m.Resources[1].Listing.Relationships = []catalog.Relationship{{Type: "invokes", TargetIdentifier: "custom:existing"}}
		}, false},
		{"supersedes needs version", func(m *importManifest) {
			m.Resources[1].Listing.Relationships = []catalog.Relationship{{Type: "supersedes", TargetIdentifier: "custom:existing"}}
		}, false},
		{"duplicate relationship", func(m *importManifest) {
			r := catalog.Relationship{Type: "depends_on", TargetIdentifier: m.Resources[0].CanonicalIdentifier}
			m.Resources[1].Listing.Relationships = []catalog.Relationship{r, r}
		}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir, m := importBatchTestInput(t, 2)
			test.change(&m)
			batch, err := prepareImportBatchTest(t, dir, m)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%t error=%v", test.valid, err)
			}
			if test.valid && !batch.Preview.LocallyValid {
				t.Fatal("control rejected native input")
			}
			if test.name == "earlier version" {
				var listing catalog.CreateListingRevisionInput
				if err := json.Unmarshal(batch.Items[1].Bodies[3], &listing); err != nil || listing.Relationships[0].TargetIdentifier != "custom:item-0" || listing.Relationships[0].Type != "depends_on" {
					t.Fatal("relationship normalization differs")
				}
			}
		})
	}
}

func TestImportUUIDInputsUseCanonicalSpelling(t *testing.T) {
	dir, manifest := importBatchTestInput(t, 1)
	manifest.Resources[0].NamespaceID = "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE"
	manifest.Resources[0].Listing.PublisherID = "ABCDEFAB-CDEF-ABCD-EFAB-CDEFABCDEFAB"
	batch, err := prepareImportBatchTest(t, dir, manifest)
	if err != nil {
		t.Fatal(err)
	}
	var resource catalog.CreateResourceInput
	var listing catalog.CreateListingRevisionInput
	if err := json.Unmarshal(batch.Items[0].Bodies[0], &resource); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(batch.Items[0].Bodies[3], &listing); err != nil {
		t.Fatal(err)
	}
	if resource.NamespaceID != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" || listing.PublisherID != "abcdefab-cdef-abcd-efab-cdefabcdefab" {
		t.Fatal("UUID spelling does not match canonical API replies")
	}
}

func TestImportBatchLocalFailureBeforeRemoteWork(t *testing.T) {
	for _, nativeInvalid := range []bool{false, true} {
		t.Run(fmt.Sprint(nativeInvalid), func(t *testing.T) {
			dir, m := importBatchTestInput(t, 2)
			m.Resources[1].Representations = append([]importRepresentation(nil), m.Resources[1].Representations...)
			m.Resources[1].Representations[0].DataFile = "later.json"
			data := []byte(`{"name":"Later invalid"}`)
			if !nativeInvalid {
				data = []byte(`{"duplicate":1,"duplicate":2}`)
			}
			writeImportTestFile(t, dir, "later.json", data, 0600)
			filename := writeImportTestFile(t, dir, "import.yaml", importTestJSON(t, m), 0600)
			var out bytes.Buffer
			state := filepath.Join(dir, "private", "state.json")
			err := runImport(t.Context(), []string{"apply", "--file", filename, "--state-file", state, "--base-url", "http://127.0.0.1:1"}, &out)
			if err == nil {
				t.Fatal("invalid later input was applied")
			}
			if _, err := os.Stat(filepath.Dir(state)); !os.IsNotExist(err) {
				t.Fatal("invalid preparation reached receipt or network setup")
			}
		})
	}
}

func TestImportAggregateReportAndReceiptLimits(t *testing.T) {
	dir, m := importBatchTestInput(t, 1)
	resource, err := normalizeImportResource(m.Resources[0])
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	loader := openapi3.NewLoader()
	contract, err := loader.LoadFromData(api.ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := prepareImportBatchTest(t, dir, m)
	if err != nil {
		t.Fatal(err)
	}
	exact := batch.Preview.PreparedBytes
	if _, err := prepareImportResource(context.Background(), root, resource, "digest", 2<<20, exact, contract); err != nil {
		t.Fatal("exact aggregate bound refused", err)
	}
	if _, err := prepareImportResource(t.Context(), root, resource, "digest", 2<<20, exact-1, contract); err == nil {
		t.Fatal("aggregate overrun accepted")
	}
	if _, err := prepareImportResource(t.Context(), root, resource, "digest", 2<<20, 1, contract); err == nil {
		t.Fatal("incremental version bound not enforced")
	}
	if _, err := encodeImportReport(strings.Repeat("x", importMaxReportBytes-3)); err != nil {
		t.Fatal("exact report bound refused")
	}
	if _, err := encodeImportReport(strings.Repeat("x", importMaxReportBytes-2)); err == nil {
		t.Fatal("report bound exceeded")
	}
	receipt, err := newImportReceipt(batch, "http://127.0.0.1", importTestPrincipal)
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(dir, "private", "state.json")
	state, err := openImportState(name, false)
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	if err := state.save(receipt); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(name) // #nosec G304 -- invocation-owned private receipt path.
	if err != nil {
		t.Fatal(err)
	}
	receipt.Origin = strings.Repeat("x", importMaxReceiptBytes)
	if err := state.save(receipt); err == nil {
		t.Fatal("oversize receipt accepted")
	}
	after, err := os.ReadFile(name) // #nosec G304 -- same invocation-owned private receipt path.
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("oversize receipt destroyed progress")
	}
	receipt, err = newImportReceipt(batch, "http://127.0.0.1", importTestPrincipal)
	if err != nil {
		t.Fatal(err)
	}
	receipt.Steps[1].State = "inflight"
	receipt.Steps[1].Path = "/api/v1/resources"
	receipt.Steps[1].BodyDigest = "digest"
	receipt.Steps[1].FirstAttempt = time.Now()
	raw := importTestJSON(t, receipt)
	if _, err := decodeImportReceipt(importTestJSON(t, importReceiptFile{Receipt: receipt, Digest: importDigest(raw)})); err == nil {
		t.Fatal("future attempted step accepted before earlier acknowledgement")
	}
}

// Admission must cover the whole manifest before any item can start.
func TestImportBatchPreflightWithControlledTransport(t *testing.T) {
	for _, fault := range []string{"none", "changed principal", "later budget", "later permission"} {
		t.Run(fault, func(t *testing.T) {
			dir, manifest := importBatchTestInput(t, 3)
			batch, err := prepareImportBatchTest(t, dir, manifest)
			if err != nil {
				t.Fatal(err)
			}
			state, err := openImportState(filepath.Join(dir, "private", "state.json"), false)
			if err != nil {
				t.Fatal(err)
			}
			defer state.close()
			selfCalls, capCalls, writes := 0, 0, 0
			client := &http.Client{Transport: catalogctlRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				status, media := http.StatusOK, "application/json"
				var value any
				switch r.URL.Path {
				case "/api/v1/me":
					selfCalls++
					principal := importTestPrincipal
					permissions := []string{"resource.create", "resource.edit"}
					if selfCalls == 2 {
						if fault == "changed principal" {
							principal.Subject = "different-author"
						}
						if fault == "later permission" {
							permissions = nil
						}
					}
					value = map[string]any{"tenantId": principal.TenantID, "realmId": principal.RealmID,
						"membershipId": principal.MembershipID, "subject": principal.Subject,
						"scopeEnforcement": false, "permissions": permissions}
				case "/api/v1/capabilities":
					capCalls++
					budget := 2 << 20
					if capCalls == 2 && fault == "later budget" {
						budget = 1
					}
					value = map[string]any{"resourceKinds": []string{"agent"}, "limits": map[string]any{"requestBodyBytes": budget}}
				default:
					if r.Method != http.MethodPost || selfCalls != 2 || capCalls != 2 {
						t.Fatal("a write preceded full namespace admission or cached admission was repeated")
					}
					writes++
					status, media = http.StatusUnauthorized, "application/problem+json"
					value = map[string]any{"code": "UNAUTHENTICATED"}
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {media}},
					Body: io.NopCloser(bytes.NewReader(importTestJSON(t, value)))}, nil
			})}
			report, err := applyPreparedImport(t.Context(), batch, remoteOptions{}, state, false,
				&remoteSession{client: client, origin: "http://127.0.0.1", token: "unit-token"})
			if err == nil {
				t.Fatal("injected refusal was ignored")
			}
			expected := 0
			if fault == "none" {
				expected = 1
			}
			if writes != expected || selfCalls != 2 {
				t.Fatal("incorrect whole-batch admission", writes, selfCalls)
			}
			if fault == "none" && (report.Disposition != "uncertain" || report.CurrentKey != "item-0" || report.Items[2].Disposition != "unattempted") {
				t.Fatal("incorrect stopped-item report")
			}
		})
	}
}

func TestImportMaximumProgressReportKeepsDiagnosticsOnce(t *testing.T) {
	dir, manifest := importBatchTestInput(t, 64)
	batch, err := prepareImportBatchTest(t, dir, manifest)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := newImportReceipt(batch, "http://127.0.0.1", importTestPrincipal)
	if err != nil {
		t.Fatal(err)
	}
	collector := catalog.NewFindingCollector(catalog.DefaultDiagnosticLimits())
	for range 128 {
		collector.Add(catalog.Finding{Code: "SERVER_VALIDATION_FINDING", Severity: catalog.SeverityWarning,
			Message: "Read the exact VersionRevision for the retained diagnostic."})
	}
	findings := collector.Findings()
	for index := range receipt.Steps {
		receipt.Steps[index].State = "acknowledged"
		receipt.Steps[index].Path = "/api/v1/resources/" + importPlaceholderID + "/versions"
		receipt.Steps[index].BodyDigest = importDigest([]byte("request"))
		receipt.Steps[index].FirstAttempt = time.Now().UTC()
		receipt.Steps[index].Ack = importAcknowledgement{ID: importPlaceholderID, RevisionID: importPlaceholderID,
			EvidenceID: importPlaceholderID, ETag: `"rev-1"`, Digest: importDigest([]byte("response"))}
		if index%4 == 1 {
			receipt.Steps[index].Ack.Findings = findings
		}
	}
	report := importProgress(batch, receipt, "complete")
	if _, err := encodeImportReport(report); err != nil {
		t.Fatal("maximum diagnostic report does not fit", err)
	}
	raw := importTestJSON(t, receipt)
	if len(raw) >= importMaxReceiptBytes {
		t.Fatal("maximum compact receipt does not fit")
	}
	for index, item := range report.Items {
		if len(item.Findings) != len(findings) || len(item.Steps[1].Ack.Findings) != 0 || len(receipt.Steps[index*4+1].Ack.Findings) != len(findings) {
			t.Fatal("report lost diagnostics or modified retained evidence")
		}
	}
}

func FuzzImportManifest(f *testing.F) {
	for _, seed := range []string{
		"format: 1\nresources: [{}, {}]\n",
		"format: 1\nresources: [{listing: {extensions: {value: 9007199254740993}}}]\n",
		"format: 1\nresources: &items [*items]\n",
		"format: 1\nformat: 2\nresources: []\n",
		"format: 1\nresources: [{listing: {extensions: {value: 1e999}}}]\n",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		value, err := decodeImportManifest(raw)
		if err != nil {
			return
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal("accepted manifest cannot form JSON", err)
		}
		// Required fields are checked in preparation. JSON serialization can
		// introduce explicit nulls for absent fields, so it is not YAML input.
		var roundTrip importManifest
		if err := json.Unmarshal(encoded, &roundTrip); err != nil {
			t.Fatal("accepted manifest is not JSON-compatible", err)
		}
		repeated, err := json.Marshal(roundTrip)
		if err != nil || !bytes.Equal(encoded, repeated) {
			t.Fatal("manifest values changed through JSON round-trip")
		}
	})
}
