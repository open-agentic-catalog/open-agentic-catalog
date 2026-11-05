package anstrust

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
)

var fixtureTime = time.Date(2026, 10, 9, 14, 31, 30, 0, time.UTC)

func native(tb testing.TB, version string) (*Verifier, Bundle) {
	tb.Helper()
	read := func(name string) []byte {
		tb.Helper()
		b, e := os.ReadFile("testdata/native/" + name) // #nosec G304 -- fixed, checked-in public test fixtures.
		if e != nil {
			tb.Fatal(e)
		}
		return b
	}
	v, e := New(Policy{FutureSkew: time.Minute, Profile: Profile, RegistrationAuthority: "ans-ra-local", LogOrigin: "owned-ans", EvidenceOrigin: "https://tl.ans-qualification.svc.cluster.local", PublisherDomains: []string{"trust-v1.ans.test", "trust-v2.ans.test"}, RegistrationKeys: map[string][]byte{"ans-ra-signer": read("ra.pem")}, LogKeys: [][]byte{read("tl.pem")}})
	if e != nil {
		tb.Fatal(e)
	}
	b, e := DecodeBundle(read(version + ".json"))
	if e != nil {
		tb.Fatal(e)
	}
	return v, b
}

func assess(tb testing.TB, v *Verifier, b Bundle, now time.Time) Result {
	tb.Helper()
	r, e := v.Verify(context.Background(), b, now)
	if e != nil {
		tb.Fatal(e)
	}
	return r
}

func TestNativeEvidence(t *testing.T) {
	for _, version := range []string{"v1", "v2"} {
		t.Run(version, func(t *testing.T) {
			v, b := native(t, version)
			r := assess(t, v, b, fixtureTime)
			if !r.CurrentRegistration || r.SchemaVersion != strings.ToUpper(version) || r.EventType != "AGENT_REGISTERED" || r.RegistrationKeyFingerprint != "70db2cf22359c51afbafbab003da91cc5855ebbcc8ee0af1f2f80ac81b5e5995" || r.LogKeyFingerprint != "08759b1452be44ffeff8da887d775a3faa786c7580f43bf3e9860c43c2130027" {
				t.Fatalf("native evidence: %+v", r)
			}
			for _, c := range []Check{r.Receipt, r.Producer, r.Log, r.Checkpoint, r.Inclusion, r.Target, r.Status} {
				if c.State != "valid" {
					t.Fatal(c)
				}
			}
			if r.ValidUntil == nil || !r.ValidUntil.After(fixtureTime) || r.ValidUntil.Location() != time.UTC {
				t.Fatal(r.ValidUntil)
			}
			expired := assess(t, v, b, fixtureTime.Add(2*time.Hour))
			if expired.CurrentRegistration || expired.Receipt.State != "valid" || expired.Inclusion.State != "valid" || expired.Status.Code != "status_freshness" {
				t.Fatalf("historical authenticity lost: %+v", expired)
			}
		})
	}
}

func mutateEntry(tb testing.TB, b Bundle, f func(map[string]any)) Bundle {
	tb.Helper()
	var x map[string]any
	if e := json.Unmarshal(b.Entry, &x); e != nil {
		tb.Fatal(e)
	}
	f(x)
	b.Entry = mustJSON(tb, x)
	return b
}
func mutateCOSE(tb testing.TB, raw []byte, f func([]cbor.RawMessage)) []byte {
	tb.Helper()
	var tag cbor.RawTag
	if e := cbor.Unmarshal(raw, &tag); e != nil {
		tb.Fatal(e)
	}
	var a []cbor.RawMessage
	if e := cbor.Unmarshal(tag.Content, &a); e != nil {
		tb.Fatal(e)
	}
	f(a)
	content, _ := cbor.Marshal(a)
	out, _ := cbor.Marshal(cbor.RawTag{Number: 18, Content: content})
	return out
}
func encoded(x any) cbor.RawMessage {
	b, e := cbor.Marshal(x)
	if e != nil {
		panic(e)
	}
	return b
}

func TestNativeRejections(t *testing.T) {
	v, b := native(t, "v1")
	cases := []struct {
		name, field, code string
		change            func(Bundle) Bundle
	}{
		{"receipt signature", "receipt", "receipt_signature_or_encoding", func(b Bundle) Bundle {
			b.Receipt = append([]byte(nil), b.Receipt...)
			b.Receipt[len(b.Receipt)-1] ^= 1
			return b
		}},
		{"missing receipt", "receipt", "receipt_missing", func(b Bundle) Bundle { b.Receipt = nil; return b }},
		{"unsupported profile", "receipt", "profile_unsupported", func(b Bundle) Bundle { b.Profile = "other"; return b }},
		{"detached payload", "receipt", "receipt_signature_or_encoding", func(b Bundle) Bundle {
			b.Receipt = mutateCOSE(t, b.Receipt, func(a []cbor.RawMessage) { a[2] = encoded(nil) })
			return b
		}},
		{"missing algorithm", "receipt", "receipt_signature_or_encoding", func(b Bundle) Bundle {
			b.Receipt = mutateCOSE(t, b.Receipt, func(a []cbor.RawMessage) {
				var h []byte
				mustCBORDecode(t, a[0], &h)
				var m map[int64]cbor.RawMessage
				mustCBORDecode(t, h, &m)
				delete(m, 1)
				a[0] = encoded([]byte(encoded(m)))
			})
			return b
		}},
		{"conflicting algorithm", "receipt", "receipt_signature_or_encoding", func(b Bundle) Bundle {
			b.Receipt = mutateCOSE(t, b.Receipt, func(a []cbor.RawMessage) {
				var m map[int64]cbor.RawMessage
				mustCBORDecode(t, a[1], &m)
				m[1] = encoded(-7)
				a[1] = encoded(m)
			})
			return b
		}},
		{"unsigned root substitution", "checkpoint", "checkpoint_binding", func(b Bundle) Bundle {
			b.Receipt = mutateCOSE(t, b.Receipt, func(a []cbor.RawMessage) {
				var m map[int64]cbor.RawMessage
				mustCBORDecode(t, a[1], &m)
				var p inclusion
				mustCBORDecode(t, m[396], &p)
				p.Root = make([]byte, 32)
				m[396] = encoded(p)
				a[1] = encoded(m)
			})
			return b
		}},
		{"wrong leaf index", "inclusion", "inclusion_proof", func(b Bundle) Bundle {
			b.Receipt = mutateCOSE(t, b.Receipt, func(a []cbor.RawMessage) {
				var m map[int64]cbor.RawMessage
				mustCBORDecode(t, a[1], &m)
				var p inclusion
				mustCBORDecode(t, m[396], &p)
				p.Index = 1
				m[396] = encoded(p)
				a[1] = encoded(m)
			})
			return b
		}},
		{"missing checkpoint", "checkpoint", "checkpoint_missing", func(b Bundle) Bundle { b.Checkpoint = nil; return b }},
		{"wrong origin", "checkpoint", "checkpoint_binding", func(b Bundle) Bundle {
			b.Checkpoint = []byte(strings.Replace(string(b.Checkpoint), "owned-ans", "other-ans", 1))
			return b
		}},
		{"missing status", "status", "status_missing", func(b Bundle) Bundle { b.StatusToken = nil; return b }},
		{"status signature", "status", "status_signature_or_encoding", func(b Bundle) Bundle {
			b.StatusToken = append([]byte(nil), b.StatusToken...)
			b.StatusToken[len(b.StatusToken)-1] ^= 1
			return b
		}},
		{"entry version", "target", "target_mismatch", func(b Bundle) Bundle { return mutateEntry(t, b, func(x map[string]any) { x["version"] = "2.0" }) }},
		{"renewed log id", "target", "target_mismatch", func(b Bundle) Bundle {
			return mutateEntry(t, b, func(x map[string]any) { object(t, x["metadata"])["logId"] = "replacement" })
		}},
		{"ans name", "target", "target_mismatch", func(b Bundle) Bundle {
			return mutateEntry(t, b, func(x map[string]any) { object(t, x["metadata"])["ansName"] = "other" })
		}},
		{"publisher identity", "target", "identity_mismatch", func(b Bundle) Bundle {
			return mutateEntry(t, b, func(x map[string]any) { object(t, x["trustManifest"])["identity"] = "https://other.test" })
		}},
		{"receipt path", "target", "receipt_reference_mismatch", func(b Bundle) Bundle {
			return mutateEntry(t, b, func(x map[string]any) {
				a := object(t, array(t, object(t, x["trustManifest"])["attestations"])[0])
				uri, ok := a["uri"].(string)
				if !ok {
					t.Fatal("expected receipt URI")
				}
				a["uri"] = uri + "?token=secret"
			})
		}},
		{"unsupported schema", "target", "trust_schema_unsupported", func(b Bundle) Bundle {
			return mutateEntry(t, b, func(x map[string]any) { object(t, x["trustManifest"])["trustSchema"] = "https://unknown.test" })
		}},
		{"duplicate references", "target", "receipt_reference_ambiguous", func(b Bundle) Bundle {
			return mutateEntry(t, b, func(x map[string]any) {
				m := object(t, x["trustManifest"])
				a := array(t, m["attestations"])
				m["attestations"] = append(a, a[0])
			})
		}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			r := assess(t, v, tt.change(b), fixtureTime)
			c := map[string]Check{"receipt": r.Receipt, "checkpoint": r.Checkpoint, "inclusion": r.Inclusion, "target": r.Target, "status": r.Status}[tt.field]
			if r.CurrentRegistration || c.Code != tt.code {
				t.Fatalf("want %s: %+v", tt.code, r)
			}
		})
	}
	// Native variants may share a signed registration. Endpoint fields remain
	// outside its claims, but the exact selected bytes retain different digests.
	other := mutateEntry(t, b, func(x map[string]any) {
		x["type"] = "application/a2a+json"
		x["url"] = "https://unverified-endpoint.test/card"
	})
	first, next := assess(t, v, b, fixtureTime), assess(t, v, other, fixtureTime)
	if !next.CurrentRegistration || first.EntryDigest == next.EntryDigest {
		t.Fatal(next)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := v.Verify(ctx, b, fixtureTime); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}

// These test keys and signing helpers create negative cases for the reviewed
// protocol. Authentic native captures above are the independent positive oracle.
type signer struct {
	private *ecdsa.PrivateKey
	public  []byte
}

func newSigner(tb testing.TB) signer {
	tb.Helper()
	k, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		tb.Fatal(e)
	}
	b, e := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if e != nil {
		tb.Fatal(e)
	}
	return signer{k, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: b})}
}
func (s signer) signature(tb testing.TB, data []byte) []byte {
	tb.Helper()
	h := sha256.Sum256(data)
	r, z, e := ecdsa.Sign(rand.Reader, s.private, h[:])
	if e != nil {
		tb.Fatal(e)
	}
	out := make([]byte, 64)
	r.FillBytes(out[:32])
	z.FillBytes(out[32:])
	return out
}
func (s signer) cose(tb testing.TB, payload []byte, protected map[int64]any, unprotected any) []byte {
	tb.Helper()
	header := []byte(encoded(protected))
	input := encoded([]any{"Signature1", header, []byte{}, payload})
	return encoded(cbor.Tag{Number: 18, Content: []any{header, unprotected, payload, s.signature(tb, input)}})
}
func (s signer) jws(tb testing.TB, payload []byte, id, authority string) string {
	tb.Helper()
	h := mustJSON(tb, map[string]any{"alg": "ES256", "kid": id, "typ": "JWT", "timestamp": fixtureTime.Unix(), "raid": authority})
	p, e := canonical(payload)
	if e != nil {
		tb.Fatal(e)
	}
	b64 := base64.RawURLEncoding.EncodeToString
	head := b64(h)
	sig := s.signature(tb, []byte(head+"."+b64(p)))
	return head + ".." + b64(sig)
}

func synthetic(tb testing.TB, changeEvent func(map[string]any), changeStatus func(*statusClaims)) (*Verifier, Bundle) {
	tb.Helper()
	return syntheticEnvelope(tb, changeEvent, changeStatus, nil)
}

func syntheticEnvelope(tb testing.TB, changeEvent func(map[string]any), changeStatus func(*statusClaims), changeEnvelope func(map[string]any)) (*Verifier, Bundle) {
	tb.Helper()
	_, b := native(tb, "v2")
	var tag cbor.RawTag
	mustCBORDecode(tb, b.Receipt, &tag)
	var a []cbor.RawMessage
	mustCBORDecode(tb, tag.Content, &a)
	var payload []byte
	mustCBORDecode(tb, a[2], &payload)
	var outer map[string]any
	mustJSONDecode(tb, payload, &outer)
	producer := object(tb, object(tb, outer["payload"])["producer"])
	ev := object(tb, producer["event"])
	ra, log := newSigner(tb), newSigner(tb)
	if changeEvent != nil {
		changeEvent(ev)
	}
	raw := mustJSON(tb, ev)
	producer["signature"] = ra.jws(tb, raw, "ans-ra-signer", "ans-ra-local")
	if changeEnvelope != nil {
		changeEnvelope(outer)
	}
	delete(outer, "signature")
	raw = mustJSON(tb, outer)
	outer["signature"] = log.jws(tb, raw, "ans-tl-attestation", "owned-ans")
	raw = mustJSON(tb, outer)
	payload, e := canonical(raw)
	if e != nil {
		tb.Fatal(e)
	}
	k, _ := parseKey(log.public)
	root := sha256.Sum256(append([]byte{0}, payload...))
	p := inclusion{Size: 1, Index: 0, Path: [][]byte{}, Root: root[:]}
	protected := map[int64]any{1: -7, 4: k.hint[:], 395: 1, 15: map[int64]any{1: "owned-ans", 6: fixtureTime.Unix()}}
	b.Receipt = log.cose(tb, payload, protected, map[int64]any{396: p})
	body := "owned-ans\n1\n" + base64.StdEncoding.EncodeToString(root[:]) + "\n"
	hash := sha256.Sum256([]byte(body))
	sig, e := ecdsa.SignASN1(rand.Reader, log.private, hash[:])
	if e != nil {
		tb.Fatal(e)
	}
	b.Checkpoint = []byte(body + "\n— owned-ans " + base64.StdEncoding.EncodeToString(append(k.hint[:], sig...)) + "\n")
	b.CheckpointHistory = nil
	var original cbor.RawTag
	mustCBORDecode(tb, b.StatusToken, &original)
	var parts []cbor.RawMessage
	mustCBORDecode(tb, original.Content, &parts)
	var claimsBytes []byte
	mustCBORDecode(tb, parts[2], &claimsBytes)
	var s statusClaims
	mustCBORDecode(tb, claimsBytes, &s)
	if changeStatus != nil {
		changeStatus(&s)
	}
	b.StatusToken = log.cose(tb, encoded(s), map[int64]any{1: -7, 4: k.hint[:], 3: "application/ans-status-token+cbor"}, map[int64]any{})
	v, e := New(Policy{FutureSkew: time.Minute, Profile: Profile, RegistrationAuthority: "ans-ra-local", LogOrigin: "owned-ans", EvidenceOrigin: "https://tl.ans-qualification.svc.cluster.local", PublisherDomains: []string{"trust-v1.ans.test", "trust-v2.ans.test"}, RegistrationKeys: map[string][]byte{"ans-ra-signer": ra.public}, LogKeys: [][]byte{log.public}})
	if e != nil {
		tb.Fatal(e)
	}
	return v, b
}

func TestSignedStatusBoundaries(t *testing.T) {
	tests := []struct {
		name, code string
		event      func(map[string]any)
		status     func(*statusClaims)
	}{
		{"valid", "", nil, nil},
		{"renewed event", "", func(e map[string]any) { e["eventType"] = "AGENT_RENEWED" }, nil},
		{"unknown signed fields", "", func(e map[string]any) { e["futureField"] = map[string]any{"number": 1.25, "text": "é"} }, nil},
		{"exact maximum age", "status_freshness", nil, func(s *statusClaims) { s.Issued = fixtureTime.Add(-time.Hour).Unix() }},
		{"expired", "status_freshness", nil, func(s *statusClaims) { s.Expires = fixtureTime.Unix() }},
		{"future within skew", "", nil, func(s *statusClaims) { s.Issued = fixtureTime.Add(time.Minute).Unix() }},
		{"future outside skew", "status_freshness", nil, func(s *statusClaims) { s.Issued = fixtureTime.Add(time.Minute + time.Second).Unix() }},
		{"old", "status_freshness", nil, func(s *statusClaims) { s.Issued = fixtureTime.Add(-time.Hour - time.Second).Unix() }},
		{"reversed time", "status_freshness", nil, func(s *statusClaims) { s.Expires = s.Issued }},
		{"wrong subject", "status_subject", nil, func(s *statusClaims) { s.ID = "different" }},
		{"wrong name", "status_subject", nil, func(s *statusClaims) { s.Name = "ans://different" }},
		{"revoked", "registration_not_active", nil, func(s *statusClaims) { s.State = "REVOKED" }},
		{"deprecated", "registration_not_active", nil, func(s *statusClaims) { s.State = "DEPRECATED" }},
		{"expired lifecycle", "registration_not_active", nil, func(s *statusClaims) { s.State = "EXPIRED" }},
		{"unknown", "status_state_unsupported", nil, func(s *statusClaims) { s.State = "WAITING" }},
		{"unbound metadata", "status_metadata_mismatch", nil, func(s *statusClaims) { s.Hashes = map[string]string{"mcp": "pretend-hash"} }},
		{"matching metadata", "", func(e map[string]any) {
			object(t, e["attestations"])["metadataHashes"] = map[string]string{"MCP": "SHA256:registered-claim"}
		}, func(s *statusClaims) { s.Hashes = map[string]string{"MCP": "SHA256:registered-claim"} }},
		{"certificate mismatch", "status_certificate_mismatch", nil, func(s *statusClaims) { s.Identity[0].Fingerprint = "other" }},
		{"signed registration expired", "registration_validity_missing_or_expired", func(e map[string]any) { e["expiresAt"] = fixtureTime.Format(time.RFC3339) }, nil},
		{"missing validity", "registration_validity_missing_or_expired", func(e map[string]any) { delete(e, "expiresAt") }, nil},
		{"future registration issuance", "registration_validity_missing_or_expired", func(e map[string]any) { e["issuedAt"] = fixtureTime.Add(time.Second).Format(time.RFC3339) }, nil},
		{"missing server certificates", "registration_validity_missing_or_expired", func(e map[string]any) { delete(object(t, e["attestations"]), "serverCerts") }, nil},
		{"undated identity certificate", "registration_validity_missing_or_expired", func(e map[string]any) {
			delete(object(t, array(t, object(t, e["attestations"])["identityCerts"])[0]), "notAfter")
		}, nil},
		{"identity certificate expires now", "registration_validity_missing_or_expired", func(e map[string]any) {
			object(t, array(t, object(t, e["attestations"])["identityCerts"])[0])["notAfter"] = fixtureTime.Format(time.RFC3339)
		}, nil},
		{"revoked event", "registration_not_active", func(e map[string]any) { e["eventType"] = "AGENT_REVOKED" }, nil},
		{"deprecated event", "registration_not_active", func(e map[string]any) { e["eventType"] = "AGENT_DEPRECATED" }, nil},
		{"unsupported event", "event_type_unsupported", func(e map[string]any) { e["eventType"] = "CERTIFICATE_ISSUED" }, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, b := synthetic(t, tt.event, tt.status)
			r := assess(t, v, b, fixtureTime)
			if tt.code == "" {
				if !r.CurrentRegistration {
					t.Fatal(r)
				}
			} else if r.CurrentRegistration || r.Status.Code != tt.code && r.Producer.Code != tt.code {
				t.Fatalf("want %s: %+v", tt.code, r)
			}
		})
	}
}

func TestSignedValidityBounds(t *testing.T) {
	for _, field := range []string{"expiresAt", "identityCerts", "serverCerts"} {
		t.Run(field, func(t *testing.T) {
			bound := fixtureTime.Add(10 * time.Second)
			v, b := synthetic(t, func(e map[string]any) {
				if field == "expiresAt" {
					e[field] = bound.Format(time.RFC3339)
					return
				}
				object(t, array(t, object(t, e["attestations"])[field])[0])["notAfter"] = bound.Format(time.RFC3339)
			}, nil)
			r := assess(t, v, b, fixtureTime)
			if !r.CurrentRegistration || r.ValidUntil == nil || !r.ValidUntil.Equal(bound) {
				t.Fatalf("signed validity did not limit assessment: %+v", r)
			}
			// All signatures and inclusion remain valid at the exclusive expiry.
			r = assess(t, v, b, bound)
			if r.CurrentRegistration || r.ValidUntil != nil || r.Status.Code != "registration_validity_missing_or_expired" || r.Inclusion.State != "valid" {
				t.Fatalf("accepted expired signed validity: %+v", r)
			}
		})
	}
}

func TestNameCollisionAndPublisher(t *testing.T) {
	v, b := synthetic(t, func(e map[string]any) { object(t, e["agent"])["name"] = "alpha beta" }, nil)
	b = mutateEntry(t, b, func(e map[string]any) {
		e["identifier"] = "urn:air:trust-v2.ans.test:agents:alpha-beta"
		e["displayName"] = "alpha beta"
	})
	if r := assess(t, v, b, fixtureTime); !r.CurrentRegistration {
		t.Fatal(r)
	}
	b = mutateEntry(t, b, func(e map[string]any) { e["displayName"] = "alpha-beta" })
	if r := assess(t, v, b, fixtureTime); r.Target.Code != "target_mismatch" {
		t.Fatal(r)
	}
	v, b = native(t, "v1")
	v.policy.PublisherDomains = []string{"other.test"}
	if r := assess(t, v, b, fixtureTime); r.Target.Code != "publisher_not_permitted" {
		t.Fatal(r)
	}
}

func mustJSON(tb testing.TB, x any) []byte {
	tb.Helper()
	b, e := json.Marshal(x)
	if e != nil {
		tb.Fatal(e)
	}
	return b
}
func mustJSONDecode(tb testing.TB, b []byte, x any) {
	tb.Helper()
	if e := json.Unmarshal(b, x); e != nil {
		tb.Fatal(e)
	}
}
func mustCBORDecode(tb testing.TB, b []byte, x any) {
	tb.Helper()
	if e := cbor.Unmarshal(b, x); e != nil {
		tb.Fatal(e)
	}
}
func object(tb testing.TB, x any) map[string]any {
	tb.Helper()
	m, ok := x.(map[string]any)
	if !ok {
		tb.Fatal("expected fixture object")
	}
	return m
}
func array(tb testing.TB, x any) []any {
	tb.Helper()
	a, ok := x.([]any)
	if !ok {
		tb.Fatal("expected fixture array")
	}
	return a
}
