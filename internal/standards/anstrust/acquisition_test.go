package anstrust

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestReferenceCanonicalSubjectAndFramework(t *testing.T) {
	v, b := native(t, "v1")
	subject, url, err := v.Reference(b.Entry)
	if err != nil || subject == "" || url != v.policy.EvidenceOrigin+"/v1/agents/"+subject+"/receipt" {
		t.Fatal("native reference rejected", err)
	}
	for _, suffix := range []string{"?secret=token", "#fragment", "/extra"} {
		entry := mutateEntry(t, b, func(e map[string]any) {
			a := object(t, array(t, object(t, e["trustManifest"])["attestations"])[0])
			uri, ok := a["uri"].(string)
			if !ok {
				t.Fatal("fixture receipt URI is not a string")
			}
			a["uri"] = uri + suffix
		})
		if _, _, err := v.Reference(entry.Entry); !errors.Is(err, ErrReference) {
			t.Fatal("non-exact reference accepted", suffix, err)
		}
	}
	for _, entry := range [][]byte{nil, []byte(`{"identifier":"x","identifier":"y"}`), []byte(strings.Repeat("x", MaxEntryBytes+1))} {
		if _, _, err := v.Reference(entry); !errors.Is(err, ErrReference) {
			t.Fatal(err)
		}
	}
	if size, err := v.RequiredHistory(b.Entry, b.Receipt, b.Checkpoint); err != nil || size != 0 {
		t.Fatal("same-size native tree requested history", size, err)
	}
	if _, err := v.RequiredHistory(b.Entry, b.Receipt, []byte("unsigned")); err == nil {
		t.Fatal("unsigned checkpoint selected history")
	}
	if _, err := v.RequiredHistory(b.Entry, []byte("not COSE"), b.Checkpoint); err == nil {
		t.Fatal("bad receipt selected history")
	}
}

func TestExplicitZeroFutureSkewDoesNotSelectDefault(t *testing.T) {
	v, b := synthetic(t, nil, func(s *statusClaims) { s.Issued = fixtureTime.Unix() + 1 })
	// Zero skew is a supported policy value, distinct from the CLI/runtime default.
	v.policy.FutureSkew = 0
	r, err := v.Verify(context.Background(), b, fixtureTime)
	if err != nil || r.Status.Code != "status_freshness" || r.CurrentRegistration {
		t.Fatal("zero skew silently widened", r, err)
	}
}

func TestMissingReceiptPreservesUnsupportedFramework(t *testing.T) {
	for _, scenario := range []string{"native", "explicit schema", "unknown attestation", "sparse"} {
		t.Run(scenario, func(t *testing.T) {
			v, b := native(t, "v1")
			b.Receipt, b.Checkpoint, b.CheckpointHistory, b.StatusToken = nil, nil, nil, nil
			if scenario == "sparse" {
				b.Entry = []byte(`{"identifier":"urn:air:trust-v1.ans.test:agents:one"}`)
			} else if scenario != "native" {
				b = mutateEntry(t, b, func(entry map[string]any) {
					trust := object(t, entry["trustManifest"])
					if scenario == "explicit schema" {
						trust["trustSchema"] = map[string]any{"identifier": "https://framework.example/unsupported", "version": "1"}
					} else {
						object(t, array(t, trust["attestations"])[0])["type"] = "Unknown-Registration"
					}
				})
			}
			r := assess(t, v, b, fixtureTime)
			want := "not_assessed"
			if scenario == "explicit schema" || scenario == "unknown attestation" {
				want = "unsupported"
			}
			if r.Target.State != want || r.Receipt.Code != "receipt_missing" || r.CurrentRegistration || r.ValidUntil != nil {
				t.Fatalf("missing evidence classification: %+v", r)
			}
			for _, c := range []Check{r.Producer, r.Log, r.Checkpoint, r.Inclusion, r.Status} {
				if c.State != "not_assessed" {
					t.Fatal("missing evidence manufactured a verification result", c)
				}
			}
		})
	}
}

func TestTargetCannotBeReboundByCaseInsensitiveExtension(t *testing.T) {
	v, b := native(t, "v1")
	var original targetEntry
	if err := json.Unmarshal(b.Entry, &original); err != nil {
		t.Fatal(err)
	}
	b = mutateEntry(t, b, func(entry map[string]any) {
		entry["identifier"] = "urn:air:trust-v1.ans.test:agents:another-agent"
	})
	// An unknown extension must not overwrite the real, case-sensitive ARD key.
	b.Entry = append(b.Entry[:len(b.Entry)-1], []byte(`,"Identifier":`+string(mustJSON(t, original.Identifier))+`}`)...)
	r, err := v.Verify(context.Background(), b, fixtureTime)
	if err != nil || r.CurrentRegistration || r.Target.Code != "target_mismatch" {
		t.Fatalf("case-folded extension rebound the signed subject: %+v %v", r, err)
	}
	if _, err := v.RequiredHistory(b.Entry, b.Receipt, b.Checkpoint); err == nil {
		t.Fatal("case-folded extension selected evidence for another target")
	}
	b.Entry = []byte(strings.Replace(string(b.Entry), "urn:air:trust-v1.ans.test:agents:another-agent", "urn:air:attacker.test:agents:another-agent", 1))
	if _, _, err := v.Reference(b.Entry); !errors.Is(err, ErrReference) {
		t.Fatal("case-folded extension bypassed publisher admission", err)
	}
}

func TestExplicitTrustManifestAliasPreservesNativeRegistration(t *testing.T) {
	for _, version := range []string{"v1", "v2"} {
		t.Run(version, func(t *testing.T) {
			v, b := native(t, version)
			b = mutateEntry(t, b, func(entry map[string]any) {
				entry["TrustManifest"] = entry["trustManifest"]
				delete(entry, "trustManifest")
				entry["extension"] = map[string]any{"future": true}
			})
			r, err := v.Verify(context.Background(), b, fixtureTime)
			if err != nil || !r.CurrentRegistration {
				t.Fatal("explicit ARD alias or unknown extension was rejected", r, err)
			}
			if _, _, err := v.Reference(b.Entry); err != nil {
				t.Fatal("explicit ARD alias could not select its receipt", err)
			}
			for _, lower := range []any{nil, map[string]any{"identity": "https://attacker.test"}} {
				ambiguous := mutateEntry(t, b, func(entry map[string]any) { entry["trustManifest"] = lower })
				r, err := v.Verify(context.Background(), ambiguous, fixtureTime)
				if err != nil || r.CurrentRegistration || r.Target.Code != "entry_encoding" {
					t.Fatal("ambiguous Trust Manifest aliases accepted", r, err)
				}
				if _, _, err := v.Reference(ambiguous.Entry); !errors.Is(err, ErrReference) {
					t.Fatal("ambiguous aliases could select evidence", err)
				}
			}
		})
	}
}
