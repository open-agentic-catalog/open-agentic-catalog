// Personal remote units use an injected transport, never sockets or application processes.
package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/personal"
)

// personalUnitReply constructs bounded protocol replies from independent expected values.
func personalUnitReply(t *testing.T, status int, value any) *http.Response {
	t.Helper()
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}, "X-Request-Id": {"owned-request"}, "Etag": {`"rev-1"`}}, Body: io.NopCloser(bytes.NewReader(personalTestBytes(t, value)))}
}

// personalUnitAccess advertises exactly the current-owner facts consumed by preflight.
func personalUnitAccess() map[string]any {
	p := importTestPrincipal
	return map[string]any{"tenantId": p.TenantID, "realmId": p.RealmID, "membershipId": p.MembershipID, "subject": p.Subject, "actorType": "human", "permissions": []string{"library.own"}, "scopeEnforcement": true, "scopes": []string{"catalog.read", "personal.write"}}
}

func TestPersonalObservedEnumerationAndBounds(t *testing.T) {
	for _, fault := range []string{"none", "later failure", "continued budget", "repeated cursor", "repeated item", "too many", "changed identity", "wrong filter", "malformed item", "empty continuation"} {
		t.Run(fault, func(t *testing.T) {
			pages, identities := 0, 0
			file := personalTestFile()
			client := &http.Client{Transport: catalogctlRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.URL.Scheme+"://"+request.URL.Host != file.Origin || request.Method != http.MethodGet || request.Header.Get("Authorization") != "Bearer unit-token" {
					t.Fatal("unexpected destination or mutation")
				}
				if request.URL.Path == "/api/v1/me" {
					identities++
					access := personalUnitAccess()
					if identities == 2 && fault == "changed identity" {
						access["membershipId"] = importTestPrincipal.RealmID
					}
					return personalUnitReply(t, 200, access), nil
				}
				if request.URL.Path != personalSavePath || request.URL.Query().Get("limit") != "100" || request.URL.Query().Get("type") != "search" {
					t.Fatal("unbounded/target read or missing filter")
				}
				pages++
				if pages == 2 && request.URL.Query().Get("cursor") != "opaque +/雪" {
					t.Fatal("continuation changed")
				}
				if pages == 2 && fault == "later failure" {
					return personalUnitReply(t, 503, map[string]string{"code": "UNAVAILABLE"}), nil
				}
				count := 100
				if fault == "too many" {
					count = 101
				}
				items := make([]personal.Item, count)
				for i := range items {
					n := (pages-1)*100 + i + 1
					items[i] = personal.Item{Input: file.Items[0], ID: fmt.Sprintf("00000000-0000-7000-8000-%012x", n), Revision: 1, CreatedAt: time.Unix(1, 0), UpdatedAt: time.Unix(2, 0)}
				}
				// The first read remains observed even when later pages see an edited label.
				if pages == 2 {
					items[0].Label = "Changed after first page"
				}
				cursor := ""
				if pages == 1 || fault == "continued budget" || fault == "repeated cursor" {
					cursor = "opaque +/雪"
					if pages == 2 && fault == "continued budget" {
						cursor = "third-page"
					}
				}
				switch fault {
				case "repeated item":
					if pages == 2 {
						items[0].ID = "00000000-0000-7000-8000-000000000001"
					}
				case "wrong filter":
					items[0].Input = file.Items[1]
				case "malformed item":
					items[0].Search = nil
				case "empty continuation":
					items = []personal.Item{}
				}
				return personalUnitReply(t, 200, map[string]any{"items": items, "nextCursor": cursor}), nil
			})}
			got, err := readPersonalExport(t.Context(), &remoteSession{client: client, origin: file.Origin, token: "unit-token"}, remoteOptions{}, "search")
			if fault == "none" {
				if err != nil || len(got.Items) != 200 || pages != 2 || identities != 2 || !reflect.DeepEqual(got.Items[0], file.Items[0]) || got.Items[100].Label != "Changed after first page" || got.Consistency != "observed-pages" {
					t.Fatal("observed enumeration changed or failed", err)
				}
			} else if err == nil || got.Format != "" || pages > 2 {
				t.Fatal("partial/unbounded export called complete", err)
			}
		})
	}
}

func TestPersonalCurrentHumanAdmission(t *testing.T) {
	for _, field := range []string{"valid", "actorType", "membershipId", "realmId", "tenantId", "subject", "scopeEnforcement", "permissions", "read-scope", "write-scope"} {
		for _, write := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/write=%v", field, write), func(t *testing.T) {
				access := personalUnitAccess()
				switch field {
				case "valid":
				case "actorType":
					access[field] = "service"
				case "permissions":
					access[field] = []string{"tenant.admin"}
				case "scopeEnforcement":
					delete(access, field)
				case "read-scope":
					access["scopes"] = []string{"personal.write"}
				case "write-scope":
					access["scopes"] = []string{"catalog.read"}
				default:
					access[field] = ""
				}
				client := &http.Client{Transport: catalogctlRoundTripFunc(func(request *http.Request) (*http.Response, error) {
					if request.Method != http.MethodGet || request.URL.Path != "/api/v1/me" {
						t.Fatal("admission made another request")
					}
					return personalUnitReply(t, 200, access), nil
				})}
				got, err := personalPrincipal(t.Context(), &remoteSession{client: client, origin: "https://catalog.example.test", token: "unit"}, remoteOptions{}, write)
				valid := field == "valid" || write && field == "read-scope" || !write && field == "write-scope"
				if (err == nil) != valid || valid && got != importTestPrincipal {
					t.Fatal("incorrect human/scope admission", err)
				}
			})
		}
	}
}

func TestPersonalSaveDurabilityAndConservativeRecovery(t *testing.T) {
	for _, fault := range []string{"success", "lost", "malformed", "forbidden", "quota"} {
		t.Run(fault, func(t *testing.T) {
			file := personalTestFile()
			raw := personalTestBytes(t, file)
			receipt, err := newPersonalRestore(file, raw, []bool{true, false, false}, importTestPrincipal)
			if err != nil {
				t.Fatal(err)
			}
			state, err := openImportState(filepath.Join(canonicalTempDir(t), "private", "progress.json"), false)
			if err != nil {
				t.Fatal(err)
			}
			defer state.close()
			if err := savePersonalRestore(state, receipt); err != nil {
				t.Fatal(err)
			}
			calls := 0
			var originalKey, originalBody string
			var originalTime time.Time
			client := &http.Client{Transport: catalogctlRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				persisted, err := decodePersonalRestore(state.previous, file, raw, receipt.Selected)
				if err != nil || persisted.Steps[0].State != "inflight" {
					t.Fatal("mutation preceded durable intent", err)
				}
				body, _ := io.ReadAll(request.Body)
				key := request.Header.Get("Idempotency-Key")
				if request.Method != http.MethodPost || request.URL.Path != personalSavePath || key != receipt.Steps[0].Key || !bytes.Equal(body, personalTestBytes(t, file.Items[0])) {
					t.Fatal("save binding changed")
				}
				if calls == 1 {
					originalKey, originalBody, originalTime = key, string(body), persisted.Steps[0].FirstAttempt
					switch fault {
					case "lost":
						return nil, io.ErrUnexpectedEOF
					case "malformed":
						return personalUnitReply(t, 201, map[string]any{"id": "invalid", "revision": 1}), nil
					case "forbidden":
						return personalUnitReply(t, 403, map[string]string{"code": "FORBIDDEN"}), nil
					case "quota":
						return personalUnitReply(t, 409, map[string]string{"code": "CONFLICT"}), nil
					}
				} else if originalKey != key || originalBody != string(body) || !originalTime.Equal(persisted.Steps[0].FirstAttempt) {
					t.Fatal("recovery rebased command")
				}
				return personalUnitReply(t, 201, map[string]any{"id": importTestPrincipal.RealmID, "revision": 1}), nil
			})}
			session := &remoteSession{client: client, origin: file.Origin, token: "unit"}
			err = savePersonalItem(t.Context(), session, remoteOptions{}, state, &receipt, 0, file.Items)
			want := "uncertain"
			if fault == "success" {
				want = "acknowledged"
			}
			if fault == "quota" {
				want = "failed"
			}
			if receipt.Steps[0].State != want || (err == nil) != (fault == "success") {
				t.Fatal("incorrect save outcome", err, receipt.Steps[0].State)
			}
			progress, err := decodePersonalRestore(state.previous, file, raw, receipt.Selected)
			if err != nil {
				t.Fatal("persisted recovery invalid", err)
			}
			if want == "uncertain" {
				if err := savePersonalItem(t.Context(), session, remoteOptions{}, state, &progress, 0, file.Items); err != nil || calls != 2 || progress.Steps[0].State != "acknowledged" {
					t.Fatal("same-command recovery failed", err)
				}
			}
			if want == "failed" || want == "acknowledged" {
				if err := savePersonalItem(t.Context(), session, remoteOptions{}, state, &progress, 0, file.Items); err == nil || calls != 1 {
					t.Fatal("terminal save repeated")
				}
			}
			for _, first := range []time.Time{{}, time.Now().Add(time.Hour), time.Now().Add(-24 * time.Hour), time.Now().Add(-23*time.Hour - 59*time.Minute)} {
				progress.Steps[0].State, progress.Steps[0].FirstAttempt = "uncertain", first
				count := calls
				if err := savePersonalItem(t.Context(), session, remoteOptions{}, state, &progress, 0, file.Items); err == nil || calls != count || !progress.Steps[0].FirstAttempt.Equal(first) {
					t.Fatal("unsafe replay sent or chronology refreshed")
				}
			}
			output := personalTestBytes(t, personalRestoreProgress(receipt, "incomplete"))
			if bytes.Contains(output, []byte(file.Items[0].Label)) || bytes.Contains(output, []byte(originalKey)) || bytes.Contains(output, []byte("private")) {
				t.Fatal("routine progress disclosed private intent")
			}
			if entries, err := os.ReadDir(state.root.Name()); err != nil || len(entries) != 2 {
				t.Fatal("temporary state files leaked", err)
			}
		})
	}
}

func TestPersonalRestoreRejectsChangedStateAndIntent(t *testing.T) {
	file := personalTestFile()
	input := personalTestBytes(t, file)
	r, err := newPersonalRestore(file, input, []bool{true, false, true}, importTestPrincipal)
	if err != nil {
		t.Fatal(err)
	}
	encode := func(state personalRestoreState) []byte {
		return personalTestBytes(t, personalRestoreFile{state, importDigest(personalTestBytes(t, state))})
	}
	if _, err := decodePersonalRestore(encode(r), file, input, r.Selected); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*personalRestoreState){
		func(r *personalRestoreState) { r.Format = "other" },
		func(r *personalRestoreState) { r.InputDigest = importDigest([]byte("changed")) },
		func(r *personalRestoreState) { r.Origin = "https://other.example.test" },
		func(r *personalRestoreState) { r.Principal.TenantID = r.Principal.RealmID },
		func(r *personalRestoreState) { r.Steps[1].State = "ready" },
		func(r *personalRestoreState) { r.Steps[0].Key = r.Steps[2].Key },
		func(r *personalRestoreState) { r.Steps[0].FirstAttempt = time.Now() },
		func(r *personalRestoreState) { r.Steps[0].Path = "/api/v1/catalog/search" },
		func(r *personalRestoreState) { r.Steps = r.Steps[:2] },
		func(r *personalRestoreState) { r.Selected = []bool{false, false, true} },
	} {
		changed := r
		changed.Steps = append([]personalRestoreStep(nil), r.Steps...)
		change(&changed)
		if _, err := decodePersonalRestore(encode(changed), file, input, r.Selected); err == nil {
			t.Fatal("changed or impossible state admitted")
		}
	}
	raw := encode(r)
	corrupt := personalRestoreFile{r, importDigest([]byte("different progress"))}
	if _, err := decodePersonalRestore(personalTestBytes(t, corrupt), file, input, r.Selected); err == nil {
		t.Fatal("changed checksum admitted otherwise valid progress")
	}
	for _, bad := range [][]byte{bytes.Replace(raw, []byte(`"format":`), []byte(`"format":"duplicate","format":`), 1), bytes.Replace(raw, []byte(`"digest":`), []byte(`"extra":true,"digest":`), 1), append(bytes.Clone(raw), []byte("{}")...)} {
		if _, err := decodePersonalRestore(bad, file, input, r.Selected); err == nil {
			t.Fatal("ambiguous progress admitted")
		}
	}
	if _, err := decodePersonalRestore(encode(r), file, append(input, ' '), r.Selected); err == nil {
		t.Fatal("changed exact file accepted")
	}
	if strings.Contains(string(raw), "private\\nquery") {
		t.Fatal("criteria copied into progress")
	}
}

func TestPersonalSaveRejectsUnverifiableAcknowledgements(t *testing.T) {
	newID, earlierID := importTestPrincipal.MembershipID, importTestPrincipal.RealmID
	const requestID = "owned-request"
	for _, tc := range []struct {
		name                           string
		follow                         bool
		status, revision               int
		id, etag, requestID, wantState string
	}{
		{"valid", false, 201, 1, newID, `"rev-1"`, requestID, "acknowledged"},
		{"revision", false, 201, 2, newID, `"rev-2"`, requestID, "uncertain"},
		{"etag", false, 201, 1, newID, `"rev-2"`, requestID, "uncertain"},
		{"request id", false, 201, 1, newID, `"rev-1"`, "unsafe PRIVATE_SENTINEL", "uncertain"},
		{"reused id", false, 201, 1, earlierID, `"rev-1"`, requestID, "uncertain"},
		{"existing follow", true, 200, 2, newID, `"rev-2"`, requestID, "already-followed"},
		{"existing follow mismatched etag", true, 200, 2, newID, `"rev-1"`, requestID, "uncertain"},
		{"200 for a bookmark", false, 200, 1, newID, `"rev-1"`, requestID, "uncertain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := personalTestFile()
			if tc.follow {
				file.Items[2] = personal.Input{Type: "follow", Label: "Release notes", ListingID: "00000000-0000-7000-8000-00000000000a"}
			}
			raw := personalTestBytes(t, file)
			receipt, err := newPersonalRestore(file, raw, []bool{true, false, true}, importTestPrincipal)
			if err != nil {
				t.Fatal(err)
			}
			// An older acknowledged create must survive any failure on the next selected item.
			receipt.Steps[0].importStep = importStep{
				State: "acknowledged", Key: receipt.Steps[0].Key, Method: http.MethodPost,
				Path: personalSavePath, BodyDigest: importDigest(personalTestBytes(t, file.Items[0])),
				FirstAttempt: time.Unix(1, 0).UTC(), RequestID: "earlier-request",
				Ack: importAcknowledgement{ID: earlierID, ETag: `"rev-1"`},
			}
			earlier := receipt.Steps[0]
			state, err := openImportState(filepath.Join(canonicalTempDir(t), "private", "state.json"), false)
			if err != nil {
				t.Fatal(err)
			}
			defer state.close()
			if err := savePersonalRestore(state, receipt); err != nil {
				t.Fatal(err)
			}
			originalKey := receipt.Steps[2].Key
			calls := 0
			client := &http.Client{Transport: catalogctlRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				if request.Method != http.MethodPost || request.URL.Path != personalSavePath || request.Header.Get("Idempotency-Key") != originalKey {
					t.Fatal("acknowledgement check made an unexpected request")
				}
				reply := personalUnitReply(t, tc.status, map[string]any{"id": tc.id, "revision": tc.revision})
				reply.Header.Set("ETag", tc.etag)
				reply.Header.Set("X-Request-ID", tc.requestID)
				return reply, nil
			})}
			err = savePersonalItem(t.Context(), &remoteSession{client: client, origin: file.Origin, token: "unit"}, remoteOptions{}, state, &receipt, 2, file.Items)
			accepted := tc.wantState != "uncertain"
			if (err == nil) != accepted || calls != 1 {
				t.Fatal("incorrect acknowledgement admission or implicit retry", err, calls)
			}
			saved, err := state.root.ReadFile(state.name)
			if err != nil {
				t.Fatal(err)
			}
			persisted, err := decodePersonalRestore(saved, file, raw, receipt.Selected)
			if err != nil || !reflect.DeepEqual(persisted.Steps[0], earlier) || persisted.Steps[1].State != "skipped" {
				t.Fatal("later acknowledgement changed earlier progress", err)
			}
			last := persisted.Steps[2]
			wantAck, wantRequestID := importAcknowledgement{}, ""
			if accepted {
				wantAck, wantRequestID = importAcknowledgement{ID: tc.id, ETag: tc.etag}, requestID
			}
			if last.Key != originalKey || last.State != tc.wantState || !reflect.DeepEqual(last.Ack, wantAck) || last.RequestID != wantRequestID {
				t.Fatalf("persisted receipt: key=%q state=%s ack=%+v request=%q", last.Key, last.State, last.Ack, last.RequestID)
			}
			if bytes.Contains(saved, []byte("PRIVATE_SENTINEL")) {
				t.Fatal("unsafe response metadata entered private progress")
			}
		})
	}
}

func TestPersonalSaveRequiresDurableIntentBeforeSending(t *testing.T) {
	file := personalTestFile()
	raw := personalTestBytes(t, file)
	receipt, err := newPersonalRestore(file, raw, []bool{true, false, false}, importTestPrincipal)
	if err != nil {
		t.Fatal(err)
	}
	state, err := openImportState(filepath.Join(canonicalTempDir(t), "private", "state.json"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	if err := savePersonalRestore(state, receipt); err != nil {
		t.Fatal(err)
	}
	original := bytes.Clone(state.previous)
	// Closing this owned filesystem handle injects a write failure without changing its files.
	if err := state.root.Close(); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: catalogctlRoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("mutation sent without durably recording its original command")
		return nil, io.ErrUnexpectedEOF
	})}
	if err := savePersonalItem(t.Context(), &remoteSession{client: client, origin: file.Origin, token: "unit"}, remoteOptions{}, state, &receipt, 0, file.Items); err == nil {
		t.Fatal("failed intent persistence was accepted")
	}
	root, err := os.OpenRoot(state.root.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	retained, err := root.ReadFile(state.name)
	if err != nil || !bytes.Equal(original, retained) || !bytes.Equal(original, state.previous) {
		t.Fatal("failed persistence replaced earlier private progress", err)
	}
}

func TestPersonalProgressValidatesAttemptedStates(t *testing.T) {
	file := personalTestFile()
	input := personalTestBytes(t, file)
	for _, status := range []string{"acknowledged", "inflight", "uncertain", "failed"} {
		t.Run(status, func(t *testing.T) {
			step := personalRestoreStep{importStep: importStep{
				State: status, Key: "personal-" + strings.Repeat("a", 48), Method: http.MethodPost,
				Path: personalSavePath, BodyDigest: importDigest(personalTestBytes(t, file.Items[0])), FirstAttempt: time.Unix(1, 0),
			}}
			if status == "acknowledged" {
				step.Ack = importAcknowledgement{ID: importTestPrincipal.RealmID, ETag: `"rev-1"`}
				step.RequestID = "ack-request"
			} else if status != "inflight" {
				step.Failure = &personalRestoreFailure{Status: http.StatusConflict, Code: "CONFLICT", RequestID: "failure-request"}
			}
			receipt := personalRestoreState{
				Format: personalRestoreFormat, Origin: file.Origin, Principal: importTestPrincipal,
				InputDigest: importDigest(input), Selected: []bool{true, false, true},
				Steps: []personalRestoreStep{step, {importStep: importStep{State: "skipped"}}, {importStep: importStep{State: "ready", Key: "personal-" + strings.Repeat("b", 48), Method: http.MethodPost}}},
			}
			decode := func(changed personalRestoreState) error {
				_, err := decodePersonalRestore(personalTestBytes(t, personalRestoreFile{changed, importDigest(personalTestBytes(t, changed))}), file, input, receipt.Selected)
				return err
			}
			if err := decode(receipt); err != nil {
				t.Fatal("valid attempted-state control failed", err)
			}
			if status != "acknowledged" {
				changed := receipt
				changed.Steps = append([]personalRestoreStep(nil), receipt.Steps...)
				changed.Steps[2].importStep = importStep{
					State: "acknowledged", Key: changed.Steps[2].Key, Method: http.MethodPost,
					Path: personalSavePath, BodyDigest: importDigest(personalTestBytes(t, file.Items[2])),
					FirstAttempt: time.Unix(1, 0), RequestID: "later-request",
					Ack: importAcknowledgement{ID: importTestPrincipal.MembershipID, ETag: `"rev-1"`},
				}
				if err := decode(changed); err == nil {
					t.Fatal("later work was acknowledged before resolving the earlier attempt")
				}
			}
			changes := map[string]func(*personalRestoreStep){
				"missing first attempt": func(s *personalRestoreStep) { s.FirstAttempt = time.Time{} },
				"different body":        func(s *personalRestoreStep) { s.BodyDigest = importDigest([]byte("changed")) },
				"nonhex key":            func(s *personalRestoreStep) { s.Key = "personal-" + strings.Repeat("z", 48) },
				"unknown state":         func(s *personalRestoreStep) { s.State = "complete" },
			}
			if status == "acknowledged" {
				changes["missing request id"] = func(s *personalRestoreStep) { s.RequestID = "" }
				changes["wrong etag"] = func(s *personalRestoreStep) { s.Ack.ETag = `"rev-2"` }
				changes["unrelated acknowledgement"] = func(s *personalRestoreStep) { s.Ack.RevisionID = importTestPrincipal.MembershipID }
				changes["ack with failure"] = func(s *personalRestoreStep) {
					s.Failure = &personalRestoreFailure{Status: http.StatusConflict, Code: "CONFLICT"}
				}
			} else {
				changes["unverified id"] = func(s *personalRestoreStep) { s.Ack.ID = importTestPrincipal.RealmID }
				changes["unverified request id"] = func(s *personalRestoreStep) { s.RequestID = "unverified" }
				changes["invalid status"] = func(s *personalRestoreStep) { s.Failure = &personalRestoreFailure{Status: 600, Code: "http_error"} }
				changes["private failure text"] = func(s *personalRestoreStep) {
					s.Failure = &personalRestoreFailure{Status: http.StatusConflict, Code: "PRIVATE_SENTINEL"}
				}
				changes["unsafe request id"] = func(s *personalRestoreStep) {
					s.Failure = &personalRestoreFailure{Status: http.StatusConflict, Code: "CONFLICT", RequestID: "unsafe request"}
				}
			}
			for name, change := range changes {
				t.Run(name, func(t *testing.T) {
					changed := receipt
					changed.Steps = append([]personalRestoreStep(nil), receipt.Steps...)
					change(&changed.Steps[0])
					if err := decode(changed); err == nil || strings.Contains(err.Error(), "PRIVATE_SENTINEL") {
						t.Fatal("invalid progress admitted or private failure text echoed", err)
					}
				})
			}
		})
	}
}

func TestPersonalFollowRestoreRepeatedIdentityBindsTheSameListing(t *testing.T) {
	original := personal.Input{Type: "follow", Label: "first", ListingID: "00000000-0000-7000-8000-00000000000a"}
	renamed := personal.Input{Type: "follow", Label: "second", ListingID: "00000000-0000-7000-8000-00000000000A"}
	steps := make([]personalRestoreStep, 2)
	steps[0].State = "acknowledged"
	steps[0].Ack.ID = "00000000-0000-7000-8000-0000000000af"
	for _, tc := range []struct {
		previous       personal.Input
		existing, want bool
	}{
		{original, true, true}, {original, false, false}, {personal.Input{Type: "follow", ListingID: "00000000-0000-7000-8000-00000000000b"}, true, false}, {personal.Input{Type: "search"}, true, false},
	} {
		for _, id := range []string{steps[0].Ack.ID, strings.ToUpper(steps[0].Ack.ID)} {
			got := personalAckMatchesSelection(steps, []personal.Input{tc.previous, renamed}, 1, id, tc.existing)
			if got != tc.want {
				t.Fatalf("previous %s, receipt %s, duplicate %t, accepted %t", tc.previous.Type, id, tc.existing, got)
			}
		}
	}
}

func TestPersonalRestoreDecodesAlreadyFollowedAcknowledgements(t *testing.T) {
	first := personal.Input{Type: "follow", Label: "First", ListingID: "00000000-0000-7000-8000-00000000000a"}
	other := personal.Input{Type: "follow", Label: "Second", ListingID: "00000000-0000-7000-8000-00000000000b"}
	const firstAck = "00000000-0000-7000-8000-000000000010"
	const otherAck = "00000000-0000-7000-8000-000000000011"
	for _, tc := range []struct {
		name      string
		second    personal.Input
		secondAck string
		state     string
		wantValid bool
	}{
		{"same Listing and saved item", first, firstAck, "already-followed", true},
		{"different Listing reuses saved item", other, firstAck, "already-followed", false},
		{"bookmark with its own saved item", personalTestFile().Items[1], otherAck, "acknowledged", true},
		{"bookmark claims already-followed with its own saved item", personalTestFile().Items[1], otherAck, "already-followed", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := personalTestFile()
			file.Items = []personal.Input{first, tc.second}
			input := personalTestBytes(t, file)
			selected := []bool{true, true}
			receipt, err := newPersonalRestore(file, input, selected, importTestPrincipal)
			if err != nil {
				t.Fatal(err)
			}
			for i := range receipt.Steps {
				step := &receipt.Steps[i]
				step.FirstAttempt = time.Unix(10, 0).UTC()
				step.Path = personalSavePath
				step.BodyDigest = importDigest(personalTestBytes(t, file.Items[i]))
				step.RequestID = fmt.Sprintf("save-%d", i)
				step.Ack = importAcknowledgement{ID: firstAck, ETag: `"rev-1"`}
			}
			receipt.Steps[0].State = "acknowledged"
			receipt.Steps[1].State = tc.state
			receipt.Steps[1].Ack.ID = tc.secondAck
			encoded := personalTestBytes(t, personalRestoreFile{receipt, importDigest(personalTestBytes(t, receipt))})
			got, err := decodePersonalRestore(encoded, file, input, selected)
			if !tc.wantValid {
				if err == nil {
					t.Fatalf("accepted invalid restore acknowledgements: %+v", got.Steps)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Steps) != 2 || got.Steps[0].Ack.ID != firstAck || got.Steps[1].Ack.ID != tc.secondAck || got.Steps[1].State != tc.state {
				t.Fatalf("decoded steps = %+v", got.Steps)
			}
		})
	}
}
