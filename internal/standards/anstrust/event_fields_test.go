package anstrust

import (
	"strings"
	"testing"
)

func TestSignedLogEventIdentifierBounds(t *testing.T) {
	v, b := native(t, "v2")
	m, _, err := v.verifyCOSE(b.Receipt, false)
	if err != nil {
		t.Fatal(err)
	}
	var outer map[string]any
	mustJSONDecode(t, m.payload, &outer)
	log := newSigner(t)
	key, err := parseKey(log.public)
	if err != nil {
		t.Fatal(err)
	}
	v.log = []publicKey{key}
	for _, size := range []int{128, 129} {
		id := strings.Repeat("e", size)
		object(t, outer["payload"])["logId"] = id
		delete(outer, "signature")
		outer["signature"] = log.jws(t, mustJSON(t, outer), "log-key", v.policy.LogOrigin)
		env, _, code := v.verifyEvent(mustJSON(t, outer))
		if size == 128 && (code != "" || env.Payload.LogID != id) {
			t.Fatalf("bounded signed event identifier refused: %s", code)
		}
		if size == 129 && code != "event_subject" {
			t.Fatalf("oversized event identifier classification: %s", code)
		}
	}
}

func TestSignedEventFieldsRemainCaseSensitive(t *testing.T) {
	control, bundle := syntheticEnvelope(t, func(event map[string]any) {
		event["Agent"] = map[string]any{"host": "untrusted.example"}
	}, nil, func(envelope map[string]any) {
		envelope["Payload"] = map[string]any{"logId": "ignored-extension"}
	})
	if !assess(t, control, bundle, fixtureTime).CurrentRegistration {
		t.Fatal("correctly named signed fields must establish the valid control")
	}
	for _, selection := range []struct {
		path  []string
		field string
	}{
		{nil, "schemaVersion"}, {nil, "payload"},
		{[]string{"payload"}, "logId"}, {[]string{"payload"}, "producer"},
		{[]string{"payload", "producer"}, "event"}, {[]string{"payload", "producer"}, "keyId"},
		{[]string{"payload", "producer"}, "signature"},
		{[]string{"payload", "producer", "event"}, "agent"},
		{[]string{"payload", "producer", "event"}, "raId"},
		{[]string{"payload", "producer", "event"}, "issuedAt"},
		{[]string{"payload", "producer", "event"}, "attestations"},
		{[]string{"payload", "producer", "event", "agent"}, "host"},
	} {
		t.Run(strings.Join(append(append([]string(nil), selection.path...), selection.field), "/"), func(t *testing.T) {
			rename := func(value map[string]any, path []string) {
				for _, field := range path {
					value = object(t, value[field])
				}
				alias := strings.ToUpper(selection.field[:1]) + selection.field[1:]
				value[alias] = value[selection.field]
				delete(value, selection.field)
			}
			var changeEvent, changeEnvelope func(map[string]any)
			if len(selection.path) >= 3 && selection.path[2] == "event" {
				changeEvent = func(value map[string]any) { rename(value, selection.path[3:]) }
			} else {
				changeEnvelope = func(value map[string]any) { rename(value, selection.path) }
			}
			v, b := syntheticEnvelope(t, changeEvent, nil, changeEnvelope)
			r := assess(t, v, b, fixtureTime)
			if r.Receipt.State != "valid" || r.CurrentRegistration {
				t.Fatalf("wrong-case signed field replaced the required field: %+v", r)
			}
		})
	}
}
