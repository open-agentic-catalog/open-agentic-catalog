package audit

import (
	"maps"
	"testing"
)

func TestWebhookAuditFactSemantics(t *testing.T) {
	cases := []struct {
		action  string
		valid   map[string]any
		invalid []map[string]any
	}{
		{ActionWebhookCreated, map[string]any{"revision": 1, "secretVersion": 1, "status": "active"}, []map[string]any{{"revision": 0}, {"revision": 2}, {"secretVersion": 2}, {"status": "disabled"}, {"status": "unknown"}, {"revokePrevious": false}}},
		{ActionWebhookUpdated, map[string]any{"revision": 2, "secretVersion": 1, "status": "active"}, []map[string]any{{"revision": 0}, {"secretVersion": 3}, {"secretVersion": 0}, {"revokePrevious": false}}},
		{ActionWebhookDisabled, map[string]any{"revision": 2, "secretVersion": 1, "status": "disabled"}, []map[string]any{{"status": "active"}, {"status": "unknown"}}},
		{ActionWebhookSecretRotated, map[string]any{"revision": 2, "secretVersion": 2, "status": "active", "revokePrevious": false}, []map[string]any{{"status": "disabled"}, {"secretVersion": 1}, {"revokePrevious": "false"}}},
		{ActionWebhookDeliveryAttempted, map[string]any{"endpointId": testMembership, "eventId": testRealm, "attempt": 1, "state": "succeeded", "httpStatus": 204}, []map[string]any{{"endpointId": "bad"}, {"attempt": 0}, {"attempt": 21}, {"httpStatus": 99}, {"httpStatus": 600}, {"state": "pending"}, {"state": "waiting"}, {"state": "dead_letter"}, {"httpStatus": 500}, {"httpStatus": 0}}},
		{ActionWebhookDeliveriesRead, map[string]any{"deliveryCount": 1, "deliveriesDigest": DigestBytes(nil)}, []map[string]any{{"deliveryCount": -1}, {"deliveryCount": 101}, {"deliveriesDigest": "not-a-digest"}}},
	}
	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			if err := validateActionFacts(tc.action, tc.valid); err != nil {
				t.Fatal(err)
			}
			for _, change := range tc.invalid {
				facts := maps.Clone(tc.valid)
				maps.Copy(facts, change)
				if err := validateActionFacts(tc.action, facts); err == nil {
					t.Fatalf("invalid change accepted: %v", change)
				}
			}
			for key := range tc.valid {
				facts := maps.Clone(tc.valid)
				delete(facts, key)
				if err := validateActionFacts(tc.action, facts); err == nil {
					t.Fatalf("missing %s accepted", key)
				}
			}
		})
	}
	for _, state := range []string{"waiting", "dead_letter"} {
		if err := validateActionFacts(ActionWebhookDeliveryAttempted, map[string]any{"endpointId": testMembership, "eventId": testRealm, "attempt": 2, "state": state, "httpStatus": 0}); err != nil {
			t.Fatal(err)
		}
	}
}
