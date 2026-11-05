// Semantic completion projections disclose only validated recorded accounting facts.
package jobs

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPublicJobViewKeepsExecutionStatePrivate(t *testing.T) {
	at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	expires := at.Add(time.Minute)
	for _, kind := range []string{"semantic.reindex", "source.sync", "publication.generate", "source.trust_verify", "unrecognized.job"} {
		t.Run(kind, func(t *testing.T) {
			job := Job{
				ID: "00000000-0000-7000-8000-000000000009", TenantID: "00000000-0000-7000-8000-000000000001", Kind: kind,
				Actor: "private-initiating-actor", PayloadVersion: 7, Payload: json.RawMessage(`{"private":"execution-input"}`),
				State: StateRunning, Attempt: 1, AttemptLimit: 3, Progress: 40,
				LeaseOwner: "private-worker", LeaseExpiresAt: &expires, DeduplicationKey: "private-deduplication-key",
				NotBefore: at, CreatedAt: at, UpdatedAt: at,
			}
			encoded, err := json.Marshal(PublicView(job))
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(encoded, &got); err != nil {
				t.Fatal(err)
			}
			// This allowlist comes from the public contract, independently of View
			// or PublicView. A new private field must not silently enter the wire.
			want := map[string]any{
				"id": "00000000-0000-7000-8000-000000000009", "tenantId": "00000000-0000-7000-8000-000000000001", "kind": kind,
				"state": "running", "priority": float64(0), "progress": float64(40), "attempt": float64(1), "attemptLimit": float64(3),
				"notBefore": "2026-10-20T12:00:00Z", "createdAt": "2026-10-20T12:00:00Z", "updatedAt": "2026-10-20T12:00:00Z",
				"cancellationRequested": false,
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("public Job metadata changed or disclosed execution state: got %s", encoded)
			}
		})
	}
}

func TestSemanticResultProjectionIsBoundToSucceededJob(t *testing.T) {
	const key = "00000000-0000-7000-8000-000000000009"
	for _, members := range []int64{0, 1, 9223372036854775807} {
		raw, err := json.Marshal(struct {
			Generation string `json:"generation"`
			Members    int64  `json:"members"`
			Private    string `json:"private"`
		}{key, members, "private sentinel"})
		if err != nil {
			t.Fatal(err)
		}
		view := PublicView(Job{ID: key, Kind: "semantic.reindex", State: StateSucceeded, Result: raw, Payload: json.RawMessage(`{"secret":"private payload"}`)})
		if view.Result == nil || view.Result.Type != "semantic_index" || view.Result.GenerationID != key || view.Result.Members == nil || *view.Result.Members != members {
			t.Fatalf("completion lost: %+v", view)
		}
		encoded, err := json.Marshal(view.Result)
		if err != nil || strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), "snapshotId") || strings.Contains(string(encoded), `"state"`) || strings.Contains(string(encoded), `"generation":`) {
			t.Fatalf("private/other variant fields: %s, %v", encoded, err)
		}
	}
	for _, raw := range []string{`null`, `[]`, `{}`, `{"generation":"` + key + `"}`, `{"generation":"` + key + `","members":null}`, `{"generation":"` + key + `","members":-1}`, `{"generation":"` + key + `","members":1.5}`, `{"generation":"` + key + `","members":"0"}`, `{"generation":"` + key + `","members":9223372036854775808}`, `{"generation":"other","members":0}`, `{"generation":"00000000-0000-7000-8000-000000000008","members":0}`, `{"generation":false,"members":0}`} {
		if view := PublicView(Job{ID: key, Kind: "semantic.reindex", State: StateSucceeded, Result: json.RawMessage(raw)}); view.Result != nil {
			t.Fatalf("malformed retained completion exposed: %s => %+v", raw, view.Result)
		}
	}
	for _, state := range []State{StateQueued, StateRunning, StateWaiting, StateFailed, StateCancelled, StateDeadLetter} {
		if view := PublicView(Job{ID: key, Kind: "semantic.reindex", State: state, Result: json.RawMessage(`{"generation":"` + key + `","members":0}`)}); view.Result != nil {
			t.Fatalf("nonterminal/unsuccessful result exposed: %s", state)
		}
	}
}
