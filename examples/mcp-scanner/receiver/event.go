package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strconv"
	"time"
	"unicode/utf8"
)

const eventSource = "https://github.com/open-agentic-catalog/open-agentic-catalog/tenants/"
const eventSchema = "https://github.com/open-agentic-catalog/open-agentic-catalog/schemas/event/v1"
const reconciliationSource = "urn:catalog-mcp-scanner:reconciliation"

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var toolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

// Event is notification context, never authority to select an MCP destination.
type Event struct {
	ID               string `json:"id"`
	Source           string `json:"source"`
	Action           string `json:"action"`
	TargetID         string `json:"targetId"`
	TransitionDigest string `json:"transitionDigest,omitempty"`
}

// uniqueJSON extends the receiver example's token check to MCP schema depth.
func uniqueJSON(raw []byte) error {
	if !utf8.Valid(raw) {
		return errors.New("invalid_json")
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) || raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return errors.New("invalid_json")
		}
		n, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if err != nil || n >= 0xdc00 && n <= 0xdfff {
			return errors.New("invalid_json")
		}
		i += 4
		if n >= 0xd800 && n <= 0xdbff {
			if i+6 >= len(raw) || string(raw[i+1:i+3]) != `\u` {
				return errors.New("invalid_json")
			}
			low, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return errors.New("invalid_json")
			}
			i += 6
		}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var visit func(int) error
	visit = func(depth int) error {
		if depth > 64 {
			return errors.New("json_depth_limit")
		}
		t, err := d.Token()
		if err != nil {
			return err
		}
		if delim, ok := t.(json.Delim); ok {
			if delim != '{' && delim != '[' {
				return errors.New("invalid_json")
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
						return errors.New("duplicate_json_key")
					}
					seen[name] = true
				}
				if err := visit(depth + 1); err != nil {
					return err
				}
			}
			end, err := d.Token()
			if err != nil || delim == '{' && end != json.Delim('}') || delim == '[' && end != json.Delim(']') {
				return errors.New("invalid_json")
			}
		}
		return nil
	}
	if err := visit(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing_json")
	}
	return nil
}

func fields(raw []byte, allowed ...string) (map[string]json.RawMessage, error) {
	var out map[string]json.RawMessage
	if json.Unmarshal(raw, &out) != nil || out == nil {
		return nil, errors.New("expected_object")
	}
	for key := range out {
		found := false
		for _, name := range allowed {
			found = found || name == key
		}
		if !found {
			return nil, errors.New("unknown_field")
		}
	}
	return out, nil
}

func stringField(m map[string]json.RawMessage, key string) (string, error) {
	raw, ok := m[key]
	if !ok || len(raw) == 0 || raw[0] != '"' {
		return "", errors.New("expected_string")
	}
	var out string
	err := json.Unmarshal(raw, &out)
	return out, err
}

func parseEvent(raw []byte, tenant string) (Event, error) {
	invalid := errors.New("invalid_event")
	if uniqueJSON(raw) != nil {
		return Event{}, invalid
	}
	root, err := fields(raw, "specversion", "id", "source", "type", "subject", "time", "datacontenttype", "dataschema", "data")
	if err != nil || len(root) != 9 {
		return Event{}, invalid
	}
	v := map[string]string{}
	for _, key := range []string{"specversion", "id", "source", "type", "subject", "time", "datacontenttype", "dataschema"} {
		v[key], err = stringField(root, key)
		if err != nil {
			return Event{}, invalid
		}
	}
	if v["specversion"] != "1.0" || v["source"] != eventSource+tenant || !uuidPattern.MatchString(v["id"]) || !uuidPattern.MatchString(v["subject"]) || v["datacontenttype"] != "application/json" || v["dataschema"] != eventSchema {
		return Event{}, invalid
	}
	if _, err = time.Parse(time.RFC3339Nano, v["time"]); err != nil {
		return Event{}, invalid
	}
	data, err := fields(root["data"], "action", "targetType", "targetId", "digest")
	if err != nil {
		return Event{}, invalid
	}
	action, err := stringField(data, "action")
	if err != nil {
		return Event{}, invalid
	}
	kind, err := stringField(data, "targetType")
	if err != nil {
		return Event{}, invalid
	}
	id, err := stringField(data, "targetId")
	if err != nil || id != v["subject"] || v["type"] != "com.open.agentic.catalog."+action+".v1" {
		return Event{}, invalid
	}
	if action != "listing.release.published" && action != "webhook.test" || action == "listing.release.published" && kind != "listing_release" || action == "webhook.test" && kind != "webhook_endpoint" {
		return Event{}, invalid
	}
	e := Event{ID: v["id"], Source: v["source"], Action: action, TargetID: id}
	if _, ok := data["digest"]; ok {
		e.TransitionDigest, err = stringField(data, "digest")
		if err != nil || !digestPattern.MatchString(e.TransitionDigest) {
			return Event{}, invalid
		}
	}
	return e, nil
}
