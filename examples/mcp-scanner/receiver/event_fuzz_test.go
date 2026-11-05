package main

import (
	"encoding/json"
	"testing"
)

func FuzzEventAdmission(f *testing.F) {
	f.Add(eventBytes("listing.release.published"))
	f.Add(eventBytes("webhook.test"))
	f.Add([]byte(`{"id":"one","id":"two"}`))
	f.Add([]byte{'"', 0xff, '"'})
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 256<<10 {
			return
		}
		event, err := parseEvent(raw, tenant)
		if err != nil {
			return
		}
		var document struct {
			Source  string `json:"source"`
			ID      string `json:"id"`
			Subject string `json:"subject"`
			Type    string `json:"type"`
		}
		if json.Unmarshal(raw, &document) != nil || document.Source != eventSource+tenant || event.Source != document.Source || event.ID != document.ID || event.TargetID != document.Subject {
			t.Fatal("admitted event lost its tenant or subject binding")
		}
		if document.Type != "com.open.agentic.catalog.listing.release.published.v1" && document.Type != "com.open.agentic.catalog.webhook.test.v1" {
			t.Fatal("admitted event has no supported action")
		}
	})
}
