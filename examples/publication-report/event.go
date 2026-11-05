package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"time"
	"unicode/utf8"
)

const eventSource = "https://github.com/open-agentic-catalog/open-agentic-catalog/tenants/"
const eventSchema = "https://github.com/open-agentic-catalog/open-agentic-catalog/schemas/event/v1"

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Event retains trigger context, never authority over the current publication.
type Event struct {
	ID       string `json:"id"`
	Source   string `json:"source"`
	Action   string `json:"action"`
	TargetID string `json:"targetId"`
	Digest   string `json:"snapshotStateDigest,omitempty"`
}

// uniqueJSON rejects duplicate keys at every depth before typed decoding.
func uniqueJSON(raw []byte) error {
	if !utf8.Valid(raw) {
		return errors.New("invalid JSON")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var visit func(int) error
	visit = func(depth int) error {
		if depth > 8 {
			return errors.New("JSON nesting limit")
		}
		t, err := d.Token()
		if err != nil {
			return err
		}
		if delim, ok := t.(json.Delim); ok {
			if delim != '{' && delim != '[' {
				return errors.New("invalid JSON delimiter")
			}
			seen := map[string]bool{}
			for d.More() {
				if delim == '{' {
					key, err := d.Token()
					if err != nil {
						return err
					}
					name, ok := key.(string)
					if !ok || seen[name] {
						return errors.New("duplicate JSON key")
					}
					seen[name] = true
				}
				if err := visit(depth + 1); err != nil {
					return err
				}
			}
			end, err := d.Token()
			if err != nil || delim == '{' && end != json.Delim('}') || delim == '[' && end != json.Delim(']') {
				return errors.New("invalid JSON end")
			}
		}
		return nil
	}
	if err := visit(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func fields(raw []byte, allowed ...string) (map[string]json.RawMessage, error) {
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return nil, errors.New("expected JSON object")
	}
	for key := range out {
		found := false
		for _, name := range allowed {
			found = found || key == name
		}
		if !found {
			return nil, errors.New("unknown field")
		}
	}
	return out, nil
}

func stringField(m map[string]json.RawMessage, key string) (string, error) {
	raw, ok := m[key]
	if !ok || len(raw) == 0 || raw[0] != '"' {
		return "", errors.New("expected string")
	}
	var s string
	err := json.Unmarshal(raw, &s)
	return s, err
}

// parseEvent implements the three selected branches of catalog-event-v1.schema.json.
// Other valid Catalog events are deliberately outside this subscription.
func parseEvent(raw []byte, tenant, host string) (Event, error) {
	invalid := errors.New("invalid notification")
	if err := uniqueJSON(raw); err != nil {
		return Event{}, invalid
	}
	root, err := fields(raw, "specversion", "id", "source", "type", "subject", "time", "datacontenttype", "dataschema", "data")
	if err != nil || len(root) != 9 {
		return Event{}, invalid
	}
	values := map[string]string{}
	for _, key := range []string{"specversion", "id", "source", "type", "subject", "time", "datacontenttype", "dataschema"} {
		values[key], err = stringField(root, key)
		if err != nil {
			return Event{}, invalid
		}
	}
	if values["specversion"] != "1.0" || values["source"] != eventSource+tenant || !uuidPattern.MatchString(values["id"]) || !uuidPattern.MatchString(values["subject"]) || values["datacontenttype"] != "application/json" || values["dataschema"] != eventSchema {
		return Event{}, invalid
	}
	if _, err := time.Parse(time.RFC3339Nano, values["time"]); err != nil {
		return Event{}, invalid
	}
	data, err := fields(root["data"], "action", "targetType", "targetId", "digest", "host")
	if err != nil {
		return Event{}, invalid
	}
	action, err := stringField(data, "action")
	if err != nil {
		return Event{}, invalid
	}
	targetType, err := stringField(data, "targetType")
	if err != nil {
		return Event{}, invalid
	}
	targetID, err := stringField(data, "targetId")
	if err != nil || targetID != values["subject"] {
		return Event{}, invalid
	}
	if values["type"] != "com.open.agentic.catalog."+action+".v1" {
		return Event{}, invalid
	}
	e := Event{ID: values["id"], Source: values["source"], Action: action, TargetID: targetID}
	if _, exists := data["digest"]; exists {
		e.Digest, err = stringField(data, "digest")
		if err != nil || !digestPattern.MatchString(e.Digest) {
			return Event{}, invalid
		}
	}
	switch action {
	case "publication.activated", "publication.unpublished":
		got, err := stringField(data, "host")
		if err != nil || got != host || targetType != "publication_snapshot" {
			return Event{}, invalid
		}
	case "webhook.test":
		if _, exists := data["host"]; exists || targetType != "webhook_endpoint" {
			return Event{}, invalid
		}
	default:
		return Event{}, invalid
	}
	return e, nil
}
