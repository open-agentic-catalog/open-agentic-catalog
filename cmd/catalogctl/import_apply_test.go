package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/controlclient"
)

var importTestPrincipal = importPrincipal{TenantID: "00000000-0000-7000-8000-000000000001", RealmID: "00000000-0000-7000-8000-000000000002", MembershipID: "00000000-0000-7000-8000-000000000003", Subject: "test-author"}

func importApplyTestPlan(t *testing.T) importPlan {
	t.Helper()
	dir, manifest := importTestInput(t)
	plan, err := prepareImportTest(t, dir, manifest, 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func importTestJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func importTestResponse(t *testing.T, value any, etag string) remoteEnvelope {
	t.Helper()
	raw := importTestJSON(t, value)
	return remoteEnvelope{Data: raw, Meta: controlclient.Metadata{ETag: etag, RequestID: "import-test-request"}}
}

func importTestReplies(t *testing.T, plan importPlan) []remoteEnvelope {
	t.Helper()
	var desired catalog.CreateResourceInput
	_ = json.Unmarshal(plan.Bodies[0], &desired)
	tenant := importTestPrincipal.TenantID
	resource := catalog.Resource{ID: "00000000-0000-7000-8000-000000000101", TenantID: tenant, NamespaceID: desired.NamespaceID, Kind: desired.Kind, CanonicalIdentifier: desired.CanonicalIdentifier, CreatedBy: importTestPrincipal.Subject, Revision: 1}
	var input catalog.CreateVersionInput
	_ = json.Unmarshal(plan.Bodies[1], &input)
	version := catalog.ResourceVersion{ID: "00000000-0000-7000-8000-000000000102", TenantID: tenant, ResourceID: resource.ID, Version: input.Version, CandidateRevisionID: "00000000-0000-7000-8000-000000000103", Revision: 1}
	revision := catalog.VersionRevision{ID: version.CandidateRevisionID, TenantID: tenant, ResourceVersionID: version.ID, EvidenceSetID: "00000000-0000-7000-8000-000000000104", ContentSetDigest: importDigest([]byte("native evidence"))}
	evidence := catalog.EvidenceSet{ID: revision.EvidenceSetID, TenantID: tenant, SubjectDigest: revision.ContentSetDigest, State: catalog.EvidenceComplete, LegalRights: catalog.LegalRightsSummary{State: catalog.LegalRightsNotAssessed}}
	listing := catalog.CatalogListing{ID: "00000000-0000-7000-8000-000000000105", TenantID: tenant, NamespaceID: resource.NamespaceID, ResourceID: resource.ID, Revision: 1}
	listingRevision := catalog.ListingRevision{ID: "00000000-0000-7000-8000-000000000106", TenantID: tenant, ListingID: listing.ID, VersionRevisionID: revision.ID, Digest: importDigest([]byte("listing"))}
	updatedListing := listing
	updatedListing.CandidateRevisionID, updatedListing.Revision = listingRevision.ID, 2
	return []remoteEnvelope{importTestResponse(t, resource, `"rev-1"`), importTestResponse(t, map[string]any{"version": version, "revision": revision, "evidence": evidence}, `"rev-1"`), importTestResponse(t, listing, `"rev-1"`), importTestResponse(t, map[string]any{"listing": updatedListing, "revision": listingRevision}, `"rev-2"`)}
}

func TestImportResponsesBindParentsAndETags(t *testing.T) {
	plan := importApplyTestPlan(t)
	receipt, err := newImportReceipt(singleImportTestBatch(plan), "http://127.0.0.1", importTestPrincipal)
	if err != nil {
		t.Fatal(err)
	}
	replies := importTestReplies(t, plan)
	for index, reply := range replies {
		ack, err := importAcknowledge(plan, receipt, index, reply)
		if err != nil {
			t.Fatalf("valid step %d: %v", index, err)
		}
		receipt.Steps[index].Ack = ack
		for _, test := range []struct {
			name   string
			mutate func(*testing.T, *remoteEnvelope)
		}{
			{"missing request ID", func(_ *testing.T, r *remoteEnvelope) { r.Meta.RequestID = "" }},
			{"missing ETag", func(_ *testing.T, r *remoteEnvelope) { r.Meta.ETag = "" }},
			{"wrong ETag", func(_ *testing.T, r *remoteEnvelope) { r.Meta.ETag = `W/"rev-1"` }},
			{"malformed", func(_ *testing.T, r *remoteEnvelope) { r.Data = []byte(`{"id":`) }},
			{"wrong tenant", func(_ *testing.T, r *remoteEnvelope) {
				r.Data = bytes.ReplaceAll(r.Data, []byte(importTestPrincipal.TenantID), []byte(importTestPrincipal.RealmID))
			}},
			{"wrong parent", func(t *testing.T, r *remoteEnvelope) {
				t.Helper()
				fields := []string{"namespaceId", "resourceId", "resourceId", "listingId"}
				parents := []string{"00000000-0000-7000-8000-000000000010", "00000000-0000-7000-8000-000000000101", "00000000-0000-7000-8000-000000000101", "00000000-0000-7000-8000-000000000105"}
				old := []byte(`"` + fields[index] + `":"` + parents[index] + `"`)
				if !bytes.Contains(r.Data, old) {
					t.Fatal("valid fixture lacks expected parent")
				}
				r.Data = bytes.Replace(r.Data, old, []byte(`"`+fields[index]+`":"`+importTestPrincipal.MembershipID+`"`), 1)
			}},
			{"missing fields", func(_ *testing.T, r *remoteEnvelope) { r.Data = []byte(`{}`) }},
		} {
			t.Run(test.name+string(rune('0'+index)), func(t *testing.T) {
				changed := reply
				test.mutate(t, &changed)
				if _, err := importAcknowledge(plan, receipt, index, changed); err == nil {
					t.Fatal("accepted inconsistent command success")
				}
			})
		}
		path, body, etag, err := importBoundRequest(plan, receipt, index)
		if err != nil || strings.Contains(path, "{") || !json.Valid(body) {
			t.Fatalf("unbound request %d", index)
		}
		if index == 3 && etag != `"rev-1"` {
			t.Fatal("listing ETag not bound")
		}
	}
	blocked := replies[1]
	blocked.Data = bytes.Replace(blocked.Data, []byte(`"state":"complete"`), []byte(`"state":"blocked"`), 1)
	if ack, err := importAcknowledge(plan, receipt, 1, blocked); err != nil || !ack.Blocked || ack.RevisionID == "" {
		t.Fatal("blocked 201 must acknowledge retained identities")
	}
	if importDigestValid("sha256:"+strings.Repeat("z", 64)) || importDigestValid("short") || importETag(`"rev-0"`, 0) {
		t.Fatal("invalid digest or ETag admitted")
	}
	withFindings := replies[1]
	var value map[string]any
	if err := json.Unmarshal(withFindings.Data, &value); err != nil {
		t.Fatal(err)
	}
	evidence, ok := value["evidence"].(map[string]any)
	if !ok {
		t.Fatal("fixture evidence missing")
	}
	evidence["findings"] = []any{map[string]any{"code": "private-code", "severity": "warning", "message": "private native content", "path": "private-file"}}
	withFindings.Data = importTestJSON(t, value)
	ack, err := importAcknowledge(plan, receipt, 1, withFindings)
	if err != nil || len(ack.Findings) != 1 || ack.Findings[0].Severity != catalog.SeverityWarning {
		t.Fatalf("safe finding summary missing: %v", err)
	}
	encoded := importTestJSON(t, ack)
	if bytes.Contains(encoded, []byte("private")) {
		t.Fatal("native diagnostics leaked into receipt")
	}
	withFindings.Data = bytes.Replace(withFindings.Data, []byte(`"warning"`), []byte(`"invalid"`), 1)
	if _, err := importAcknowledge(plan, receipt, 1, withFindings); err == nil {
		t.Fatal("invalid finding severity accepted")
	}
}

func TestImportReceiptsPrivateAtomicAndBound(t *testing.T) {
	plan := importApplyTestPlan(t)
	r, err := newImportReceipt(singleImportTestBatch(plan), "http://127.0.0.1", importTestPrincipal)
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(canonicalTempDir(t), "private", "progress.json")
	f, err := openImportState(name, false)
	if err != nil {
		t.Fatal(err)
	}
	defer f.close()
	if err = f.save(r); err != nil {
		t.Fatal(err)
	}
	if _, err = openImportState(name, true); err == nil {
		t.Fatal("concurrent writer admitted")
	}
	if _, err = openImportState(name, false); err == nil {
		t.Fatal("concurrent apply admitted")
	}
	raw, err := f.read()
	if err != nil {
		t.Fatal(err)
	}
	retained, err := decodeImportReceipt(raw)
	if err != nil || retained.IntentDigest != importIntent(singleImportTestBatch(plan)) {
		t.Fatal("receipt roundtrip failed")
	}
	if bytes.Contains(raw, []byte("Private example")) || bytes.Contains(raw, []byte("framework")) {
		t.Fatal("native content leaked into receipt")
	}
	r.Steps[0].State = "inflight"
	r.Steps[0].Path = "/api/v1/resources"
	r.Steps[0].BodyDigest = importDigest(plan.Bodies[0])
	r.Steps[0].FirstAttempt = time.Now().UTC()
	if err = f.save(r); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(name, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err = f.save(r); err == nil {
		t.Fatal("changed file overwritten")
	}
	if _, err = decodeImportReceipt([]byte(`{}`)); err == nil {
		t.Fatal("invalid receipt accepted")
	}
	changed := bytes.Replace(raw, []byte(`"format":2`), []byte(`"format":3`), 1)
	if _, err = decodeImportReceipt(changed); err == nil {
		t.Fatal("changed checksum accepted")
	}
	for _, mutate := range []func(*importReceipt){func(r *importReceipt) { r.Format = 3 }, func(r *importReceipt) { r.Steps = r.Steps[:1] }, func(r *importReceipt) { r.Steps[0].State = "unknown" }, func(r *importReceipt) { r.Steps[0].Key = "invalid" }, func(r *importReceipt) { r.Steps[1].Key = r.Steps[0].Key }, func(r *importReceipt) { r.Steps[0].State = "inflight" }, func(r *importReceipt) { r.Steps[0].Ack.ID = importPlaceholderID }} {
		changedReceipt, _ := newImportReceipt(singleImportTestBatch(plan), "http://127.0.0.1", importTestPrincipal)
		mutate(&changedReceipt)
		encoded := importTestJSON(t, changedReceipt)
		file := importTestJSON(t, importReceiptFile{Receipt: changedReceipt, Digest: importDigest(encoded)})
		if _, err := decodeImportReceipt(file); err == nil {
			t.Fatal("invalid receipt state admitted")
		}
	}
}

func TestImportReceiptAttemptAndAcknowledgementStates(t *testing.T) {
	plan := importApplyTestPlan(t)
	for _, fault := range []string{"control", "invalid key hex", "wrong method", "ready path", "missing attempt time", "missing attempt path", "missing acknowledgement identity"} {
		t.Run(fault, func(t *testing.T) {
			receipt, err := newImportReceipt(singleImportTestBatch(plan), "http://127.0.0.1", importTestPrincipal)
			if err != nil {
				t.Fatal(err)
			}
			for index := range 3 {
				step := &receipt.Steps[index]
				step.State = "acknowledged"
				step.FirstAttempt = time.Date(2026, 1, 1, 12, 0, index, 0, time.UTC)
				step.Ack.ID = demoTestUUID(4900 + index)
				path, body, _, err := importBoundRequest(plan, receipt, index)
				if err != nil {
					t.Fatal(err)
				}
				step.Path, step.BodyDigest = path, importDigest(body)
			}
			receipt.Steps[2].State, receipt.Steps[2].Ack = "uncertain", importAcknowledgement{}
			switch fault {
			case "invalid key hex":
				receipt.Steps[0].Key = "import-" + strings.Repeat("g", 48)
			case "wrong method":
				receipt.Steps[0].Method = http.MethodDelete
			case "ready path":
				receipt.Steps[3].Path = "/api/v1/resources"
			case "missing attempt time":
				receipt.Steps[2].FirstAttempt = time.Time{}
			case "missing attempt path":
				receipt.Steps[2].Path = ""
			case "missing acknowledgement identity":
				receipt.Steps[0].Ack.ID = ""
			}
			encoded := importTestJSON(t, receipt)
			raw := importTestJSON(t, importReceiptFile{Receipt: receipt, Digest: importDigest(encoded)})
			got, err := decodeImportReceipt(raw)
			if fault == "control" {
				if err != nil || got.Steps[0].Ack.ID != receipt.Steps[0].Ack.ID || got.Steps[2].State != "uncertain" || !got.Steps[2].FirstAttempt.Equal(receipt.Steps[2].FirstAttempt) {
					t.Fatalf("valid acknowledged prefix/uncertain attempt lost: %v", err)
				}
			} else if err == nil || err.Error() != "import: state is invalid or changed; preserve it for investigation" {
				t.Fatalf("invalid receipt admitted or wrong diagnostic: %v", err)
			}
		})
	}
}

func TestImportStateRejectsLinksAndPermissions(t *testing.T) {
	for _, kind := range []string{"safe resume", "directory permissions", "file permissions", "file link", "lock link", "dangling lock link", "ancestor link", "existing apply"} {
		t.Run(kind, func(t *testing.T) {
			dir := canonicalTempDir(t)
			if err := os.Chmod(dir, 0700); err != nil { // #nosec G302 -- private directory requires owner traversal.
				t.Fatal(err)
			}
			name := filepath.Join(dir, "state.json")
			// Every rejection control starts with an otherwise readable state file.
			if err := os.WriteFile(name, []byte(`{}`), 0600); err != nil {
				t.Fatal(err)
			}
			resume := true
			switch kind {
			case "existing apply":
				// No invocation holds the lock. Refuse the existing state itself.
				resume = false
			case "directory permissions":
				if err := os.Chmod(dir, 0755); err != nil { // #nosec G302 -- deliberate unsafe-mode rejection control.
					t.Fatal(err)
				}
			case "file permissions":
				if err := os.Chmod(name, 0644); err != nil { // #nosec G302 -- deliberate unsafe-mode rejection control.
					t.Fatal(err)
				}
			case "file link":
				if err := os.Rename(name, name+".target"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("state.json.target", name); err != nil {
					t.Fatal(err)
				}
			case "lock link":
				if err := os.WriteFile(name+".lock-target", nil, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("state.json.lock-target", name+".lock"); err != nil {
					t.Fatal(err)
				}
			case "dangling lock link":
				if err := os.Symlink("missing", name+".lock"); err != nil {
					t.Fatal(err)
				}
			case "ancestor link":
				link := filepath.Join(canonicalTempDir(t), "linked")
				if err := os.Symlink(dir, link); err != nil {
					t.Fatal(err)
				}
				name = filepath.Join(link, "state.json")
			}
			f, err := openImportState(name, resume)
			if f != nil {
				f.close()
			}
			if (err != nil) != (kind != "safe resume") {
				t.Fatalf("state admission error = %v", err)
			}
			if kind == "dangling lock link" {
				if _, err := os.Lstat(filepath.Join(dir, "missing")); !os.IsNotExist(err) {
					t.Fatal("lock rejection created the symlink target")
				}
			}
		})
	}
}

func TestImportReplayWindowDoesNotReset(t *testing.T) {
	first := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		first, now time.Time
		want       bool
	}{{first, first, true}, {first, first.Add(23 * time.Hour), true}, {first, first.Add(24*time.Hour - time.Minute), false}, {first, first.Add(25 * time.Hour), false}, {first, first.Add(-time.Second), false}, {time.Time{}, first, false}} {
		if importReplayAllowed(test.first, test.now) != test.want {
			t.Fatal("unsafe replay time decision")
		}
	}
}

func TestImportSessionSnapshotsCredentialFile(t *testing.T) {
	filename := filepath.Join(canonicalTempDir(t), "token")
	if err := os.WriteFile(filename, []byte("original-token"), 0600); err != nil {
		t.Fatal(err)
	}
	session, err := newRemoteSession(t.Context(), remoteOptions{baseURL: "http://127.0.0.1", tokenFile: filename, token: "ignored-environment-token"}) // #nosec G101 -- synthetic precedence control, not a credential.
	if err != nil {
		t.Fatal(err)
	}
	defer session.close()
	if err := os.WriteFile(filename, []byte("replacement-token"), 0600); err != nil {
		t.Fatal(err)
	}
	session.client.Transport = catalogctlRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Bearer original-token" {
			t.Fatal("credential changed during invocation")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})
	for range 2 {
		if _, err := importGET(t.Context(), session, remoteOptions{}, "/api/v1/me"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestImportPreflightUsesExactNamespaceAndOAuth(t *testing.T) {
	for _, test := range []struct {
		name                      string
		tenant, scoped, available []string
		enforce                   bool
		scopes                    []string
		want                      bool
	}{
		{"scoped contributor", nil, []string{"resource.create", "resource.edit"}, nil, false, nil, true},
		{"other namespace", nil, []string{"resource.create", "resource.edit"}, nil, false, nil, false},
		{"tenant contributor", []string{"resource.create", "resource.edit"}, nil, nil, false, nil, true},
		{"navigation union", nil, nil, []string{"resource.create", "resource.edit"}, false, nil, false},
		{"missing edit", nil, []string{"resource.create"}, nil, false, nil, false},
		{"OAuth read only", []string{"resource.create", "resource.edit"}, nil, nil, true, []string{"catalog.read"}, false},
		{"OAuth write only", []string{"resource.create", "resource.edit"}, nil, nil, true, []string{"catalog.write"}, false},
		{"OAuth both", nil, []string{"resource.create", "resource.edit"}, nil, true, []string{"catalog.read", "catalog.write"}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: catalogctlRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				var value any
				if r.URL.Path == "/api/v1/me" {
					if r.URL.Query().Get("namespaceId") != importPlaceholderID {
						t.Fatal("preflight omitted exact namespace")
					}
					grantedNamespace := importPlaceholderID
					if test.name == "other namespace" {
						grantedNamespace = importTestPrincipal.MembershipID
					}
					value = map[string]any{"tenantId": importTestPrincipal.TenantID, "realmId": importTestPrincipal.RealmID, "membershipId": importTestPrincipal.MembershipID, "subject": importTestPrincipal.Subject, "scopeEnforcement": test.enforce, "scopes": test.scopes, "permissions": test.tenant, "availablePermissions": test.available, "namespaceGrants": []any{map[string]any{"namespaceId": grantedNamespace, "permissions": test.scoped}}}
				} else {
					value = map[string]any{"resourceKinds": []string{"agent"}, "limits": map[string]any{"requestBodyBytes": 12345}}
				}
				raw := importTestJSON(t, value)
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(raw))}, nil
			})}
			principal, budget, err := importPreflight(context.Background(), &remoteSession{client: client, origin: "http://127.0.0.1", token: "secret-token"}, remoteOptions{}, importPlaceholderID, catalog.KindAgent)
			if (err == nil) != test.want {
				t.Fatalf("preflight %v", err)
			}
			if test.want && (principal != importTestPrincipal || budget != 12345 || calls != 2) {
				t.Fatal("preflight lost principal/limit")
			}
			if !test.want && calls != 1 {
				t.Fatal("denial did not stop preflight")
			}
		})
	}
}

func TestImportPreparedSequenceWithControlledTransport(t *testing.T) {
	for _, fault := range []string{"none", "later candidate", "blocked", "unauthorized", "invalid response", "failed command", "readback", "readback version parent", "readback evidence binding", "readback evidence unavailable", "readback listing parent", "readback listing revision parent", "budget"} {
		t.Run(fault, func(t *testing.T) {
			plan := importApplyTestPlan(t)
			replies := importTestReplies(t, plan)
			state, err := openImportState(filepath.Join(canonicalTempDir(t), "private", "state.json"), false)
			if err != nil {
				t.Fatal(err)
			}
			defer state.close()
			writes := 0
			failed := false
			client := &http.Client{Transport: catalogctlRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				status, etag := 200, ""
				var raw []byte
				switch {
				case request.URL.Path == "/api/v1/me":
					raw, _ = json.Marshal(map[string]any{"tenantId": importTestPrincipal.TenantID, "realmId": importTestPrincipal.RealmID, "membershipId": importTestPrincipal.MembershipID, "subject": importTestPrincipal.Subject, "scopeEnforcement": false, "permissions": []string{"resource.create", "resource.edit"}})
				case request.URL.Path == "/api/v1/capabilities":
					budget := 2 << 20
					if fault == "budget" {
						budget = 1
					}
					raw, _ = json.Marshal(map[string]any{"resourceKinds": []string{"agent"}, "limits": map[string]any{"requestBodyBytes": budget}})
				case request.Method == http.MethodPost:
					index := writes
					if failed && fault == "unauthorized" {
						index--
					}
					writes++
					status = 201
					raw = replies[index].Data
					etag = replies[index].Meta.ETag
					if index == 1 && !failed {
						switch fault {
						case "blocked":
							raw = bytes.Replace(raw, []byte(`"state":"complete"`), []byte(`"state":"blocked"`), 1)
						case "unauthorized":
							status = 401
							raw = []byte(`{}`)
							failed = true
						case "invalid response":
							raw = []byte(`{}`)
						case "failed command":
							status = 412
							raw = []byte(`{}`)
						}
					}
				default:
					var value map[string]json.RawMessage
					switch {
					case strings.Contains(request.URL.Path, "/resource-versions/"):
						_ = json.Unmarshal(replies[1].Data, &value)
						raw = value["version"]
						if fault == "later candidate" || fault == "readback version parent" {
							var version catalog.ResourceVersion
							if err := json.Unmarshal(raw, &version); err != nil {
								t.Fatal(err)
							}
							if fault == "later candidate" {
								version.CandidateRevisionID, version.Revision = importPlaceholderID, 8
							} else {
								version.ResourceID = importPlaceholderID
							}
							raw = importTestJSON(t, version)
						}
					case strings.Contains(request.URL.Path, "/version-revisions/"):
						_ = json.Unmarshal(replies[1].Data, &value)
						if fault == "readback evidence binding" {
							var evidence catalog.EvidenceSet
							if err := json.Unmarshal(value["evidence"], &evidence); err != nil {
								t.Fatal(err)
							}
							evidence.SubjectDigest = importDigest([]byte("different retained content"))
							value["evidence"] = importTestJSON(t, evidence)
						}
						raw, _ = json.Marshal(map[string]json.RawMessage{"revision": value["revision"], "evidence": value["evidence"]})
						if fault == "readback evidence unavailable" {
							status, raw = http.StatusServiceUnavailable, []byte(`{"code":"DEPENDENCY_UNAVAILABLE"}`)
						}
					case strings.Contains(request.URL.Path, "/resources/"):
						raw = replies[0].Data
						etag = replies[0].Meta.ETag
					case strings.Contains(request.URL.Path, "/catalog-listings/"):
						_ = json.Unmarshal(replies[3].Data, &value)
						raw = value["listing"]
						if fault == "later candidate" || fault == "readback listing parent" {
							var listing catalog.CatalogListing
							if err := json.Unmarshal(raw, &listing); err != nil {
								t.Fatal(err)
							}
							if fault == "later candidate" {
								listing.CandidateRevisionID, listing.CurrentReleaseID, listing.Revision = importPlaceholderID, importPlaceholderID, 8
							} else {
								listing.ResourceID = importPlaceholderID
							}
							raw = importTestJSON(t, listing)
						}
					case strings.Contains(request.URL.Path, "/listing-revisions/"):
						_ = json.Unmarshal(replies[3].Data, &value)
						raw = value["revision"]
						if fault == "readback listing revision parent" {
							var revision catalog.ListingRevision
							if err := json.Unmarshal(raw, &revision); err != nil {
								t.Fatal(err)
							}
							revision.VersionRevisionID = importPlaceholderID
							raw = importTestJSON(t, revision)
						}
					}
					if fault == "readback" && writes == 4 {
						raw = []byte(`{}`)
					}
				}
				media := "application/json"
				if status >= 400 {
					media = "application/problem+json"
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{media}, "Etag": []string{etag}, "X-Request-Id": []string{"unit-request"}}, Body: io.NopCloser(bytes.NewReader(raw))}, nil
			})}
			session := &remoteSession{client: client, origin: "http://127.0.0.1", token: "unit-token"}
			outcome, err := applyPreparedImport(t.Context(), singleImportTestBatch(plan), remoteOptions{}, state, false, session)
			if fault == "none" || fault == "later candidate" {
				if err != nil || outcome.Disposition != "complete" || writes != 4 {
					t.Fatalf("valid sequence: %v %#v", err, outcome)
				}
				if _, err = applyPreparedImport(t.Context(), singleImportTestBatch(plan), remoteOptions{}, state, true, session); err != nil || writes != 4 {
					t.Fatal("completed input created new writes")
				}
			} else {
				if err == nil {
					t.Fatal("injected fault accepted")
				}
				if fault == "blocked" && outcome.Disposition != "blocked" {
					t.Fatal("blocked evidence not reported")
				}
				if fault == "budget" && writes != 0 {
					t.Fatal("server limit checked after writes")
				}
				if strings.HasPrefix(fault, "readback ") {
					if outcome.Disposition != "uncertain" || writes != 4 {
						t.Fatal("failed exact readback lost acknowledged progress or repeated mutations")
					}
					before := bytes.Clone(state.previous)
					if _, err := applyPreparedImport(t.Context(), singleImportTestBatch(plan), remoteOptions{}, state, true, session); err == nil || writes != 4 || !bytes.Equal(before, state.previous) {
						t.Fatal("resume ignored invalid retained evidence or changed the receipt")
					}
				}
			}
			if fault == "unauthorized" {
				outcome, err = applyPreparedImport(t.Context(), singleImportTestBatch(plan), remoteOptions{}, state, true, session)
				if err != nil || outcome.Disposition != "complete" || writes != 5 {
					t.Fatalf("explicit resume after token refusal failed: %v", err)
				}
			}
		})
	}
}
