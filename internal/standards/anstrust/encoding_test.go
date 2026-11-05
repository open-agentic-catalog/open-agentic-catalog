package anstrust

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPolicyAdmission(t *testing.T) {
	ra, log := newSigner(t), newSigner(t)
	p := Policy{Profile: Profile, RegistrationAuthority: "ra", LogOrigin: "log", EvidenceOrigin: "https://log.test", PublisherDomains: []string{"publisher.test"}, RegistrationKeys: map[string][]byte{"ra-key": ra.public}, LogKeys: [][]byte{log.public}}
	first, e := New(p)
	if e != nil {
		t.Fatal(e)
	}
	for _, change := range []func(*Policy){
		func(p *Policy) { p.Profile = "unknown" }, func(p *Policy) { p.RegistrationAuthority = "bad name" }, func(p *Policy) { p.LogOrigin = strings.Repeat("a", 129) },
		func(p *Policy) { p.EvidenceOrigin = "http://log.test" }, func(p *Policy) { p.EvidenceOrigin = "https://user:secret@log.test" }, func(p *Policy) { p.EvidenceOrigin = "https://log.test/" }, func(p *Policy) { p.EvidenceOrigin = "https://log.test?" },
		func(p *Policy) { p.PublisherDomains = []string{"Publisher.test"} }, func(p *Policy) { p.PublisherDomains = []string{"*.test"} }, func(p *Policy) { p.PublisherDomains = []string{"publisher.test", "publisher.test"} },
		func(p *Policy) { p.MaxStatusAge = 25 * time.Hour }, func(p *Policy) { p.FutureSkew = 61 * time.Second },
		func(p *Policy) { p.RegistrationKeys = map[string][]byte{"bad id": ra.public} }, func(p *Policy) { p.RegistrationKeys = map[string][]byte{"key1": ra.public, "key2": ra.public} },
		func(p *Policy) { p.LogKeys = [][]byte{log.public, log.public} }, func(p *Policy) { p.LogKeys = [][]byte{[]byte("not a key")} }, func(p *Policy) { p.LogKeys = nil },
	} {
		candidate := p
		change(&candidate)
		if _, err := New(candidate); !errors.Is(err, ErrPolicy) {
			t.Fatal("accepted invalid authority policy", err)
		}
	}
	for _, raw := range [][]byte{bytes.Repeat([]byte("x"), 4097), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("secret")}), append(append([]byte(nil), ra.public...), ra.public...), pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: []byte("invalid DER")})} {
		if _, e := parseKey(raw); e == nil {
			t.Fatal("accepted invalid public key")
		}
	}
	other, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&other.PublicKey)
	if _, e := parseKey(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})); e == nil {
		t.Fatal("accepted non-P256")
	}
	p.LogKeys[0] = append([]byte("\n"), p.LogKeys[0]...)
	next, e := New(p)
	if e != nil || first.digest != next.digest {
		t.Fatal("PEM whitespace changed policy", e)
	}
	p.PublisherDomains[0] = "other.test"
	if first.policy.PublisherDomains[0] != "publisher.test" {
		t.Fatal("caller changed retained policy")
	}
}

func TestStrictEncodingsAndCanonicalization(t *testing.T) {
	// RFC 8785 examples exercise numeric formatting, UTF-16 property ordering,
	// preserved Unicode and whitespace independently of a signature round trip.
	for _, tt := range []struct{ input, want string }{
		{`{"z":1.0,"a":1e30,"small":0.000001}`, `{"a":1e+30,"small":0.000001,"z":1}`},
		{"{\"\ue000\":2,\"😀\":1}", "{\"😀\":1,\"\ue000\":2}"},
		{`{"s":"é","a":[true,null,0.002]}`, `{"a":[true,null,0.002],"s":"é"}`},
	} {
		got, e := canonical([]byte(tt.input))
		if e != nil || string(got) != tt.want {
			t.Fatalf("canonical %s: %s %v", tt.input, got, e)
		}
	}
	for _, bad := range []string{`{"a":1,"a":2}`, `{"s":"\ud800"}`, `{"a":1e999}`, `{} {}`, "{\"s\":\"\xff\"}"} {
		if _, e := canonical([]byte(bad)); e == nil {
			t.Fatal("accepted invalid signed JSON", bad)
		}
	}
	for _, s := range []string{"YQ==", "YQ\n", "YR", "++", "a"} {
		if _, e := strictBase64(s, true); e == nil {
			t.Fatal("noncanonical JWS base64", s)
		}
	}
	for _, s := range []string{"YQ", "YR==", "YQ==\n"} {
		if _, e := strictBase64(s, false); e == nil {
			t.Fatal("noncanonical note base64", s)
		}
	}
	for _, data := range [][]byte{[]byte(`{"profile":"x","profile":"y"}`), []byte(`{"profile":null}`), []byte(`{"unknown":1}`), bytes.Repeat([]byte(" "), MaxBundleBytes+1)} {
		if _, e := DecodeBundle(data); e == nil {
			t.Fatal("accepted invalid bundle")
		}
	}
	v, b := native(t, "v1")
	b.Entry = make([]byte, MaxEntryBytes+1)
	if _, e := v.Verify(t.Context(), b, fixtureTime); !errors.Is(e, ErrBundle) {
		t.Fatal(e)
	}
	for _, raw := range [][]byte{{0xa2, 1, 1, 1, 2}, {0x9f, 1, 0xff}, {0xff}} {
		var value any
		if decodeCBOR(raw, &value) == nil {
			t.Fatal("accepted invalid CBOR")
		}
	}
}

func TestJWSHeaderAndSignatureRejection(t *testing.T) {
	s := newSigner(t)
	key, _ := parseKey(s.public)
	payload := []byte(`{"answer":42}`)
	good := s.jws(t, payload, "key", "ra")
	if verifyJWS(good, payload, key, "key", "ra") != nil {
		t.Fatal("valid test signature rejected")
	}
	for _, bad := range []string{strings.Repeat("a", 4097), "not-jws", strings.Replace(good, "..", ".payload.", 1), good + "=", good[:len(good)-1] + "!"} {
		if verifyJWS(bad, payload, key, "key", "ra") == nil {
			t.Fatal("accepted invalid JWS")
		}
	}
	header := map[string]any{"alg": "ES256", "kid": "key", "typ": "JWT", "timestamp": fixtureTime.Unix(), "raid": "ra"}
	for _, extra := range []map[string]any{{"crit": []string{"unknown"}}, {"b64": false}, {"jwk": map[string]string{"kty": "EC"}}, {"alg": "HS256"}, {"raid": "other"}, {"kid": "other"}} {
		h := map[string]any{}
		for k, v := range header {
			h[k] = v
		}
		for k, v := range extra {
			h[k] = v
		}
		raw := mustJSON(t, h)
		text := base64.RawURLEncoding.EncodeToString(raw)
		sig := s.signature(t, []byte(text+"."+base64.RawURLEncoding.EncodeToString(payload)))
		if verifyJWS(text+".."+base64.RawURLEncoding.EncodeToString(sig), payload, key, "key", "ra") == nil {
			t.Fatal("accepted unsupported signed header", extra)
		}
	}
	if verifyJWS(good, []byte(`{"answer":43}`), key, "key", "ra") == nil {
		t.Fatal("accepted tampered payload")
	}
}

func TestCheckpointHistoryAuthentication(t *testing.T) {
	v, b := native(t, "v1")
	m, _, e := v.verifyCOSE(b.Receipt, false)
	if e != nil {
		t.Fatal(e)
	}
	p, e := decodeProof(m)
	if e != nil {
		t.Fatal(e)
	}
	if !v.historicalCheckpoint(b.CheckpointHistory, p) {
		t.Fatal("native signed history rejected")
	}
	var history map[string]any
	mustJSONDecode(t, b.CheckpointHistory, &history)
	cp := object(t, array(t, history["checkpoints"])[0])
	cp["publicKeyPem"] = "attacker key"
	cp["createdAt"] = "not authoritative"
	object(t, array(t, cp["signatures"])[0])["valid"] = false
	raw := mustJSON(t, history)
	if !v.historicalCheckpoint(raw, p) {
		t.Fatal("trusted unsigned history decorations")
	}
	for _, field := range []string{"rootHash", "originName", "checkpointText"} {
		save := cp[field]
		cp[field] = "wrong"
		raw := mustJSON(t, history)
		if v.historicalCheckpoint(raw, p) {
			t.Fatal("accepted history mismatch", field)
		}
		cp[field] = save
	}
	log := newSigner(t)
	k, _ := parseKey(log.public)
	v.log = append(v.log, k)
	body := "owned-ans\n3\n" + base64.StdEncoding.EncodeToString(make([]byte, 32)) + "\n"
	hash := sha256.Sum256([]byte(body))
	sig, _ := ecdsa.SignASN1(rand.Reader, log.private, hash[:])
	b.Checkpoint = []byte(body + "\n— owned-ans " + base64.StdEncoding.EncodeToString(append(k.hint[:], sig...)) + "\n")
	if !v.verifyCheckpoint(b, p) {
		t.Fatal("advanced checkpoint lost authenticated same-size history")
	}
	b.CheckpointHistory = nil
	if v.verifyCheckpoint(b, p) {
		t.Fatal("accepted latest root without matching history")
	}
}

func TestEventSignatureAndValidity(t *testing.T) {
	v, b := native(t, "v1")
	m, _, e := v.verifyCOSE(b.Receipt, false)
	if e != nil {
		t.Fatal(e)
	}
	env, ev, code := v.verifyEvent(m.payload)
	if code != "" {
		t.Fatal(code)
	}
	var outer map[string]any
	mustJSONDecode(t, m.payload, &outer)
	producer := object(t, object(t, outer["payload"])["producer"])
	for _, tt := range []struct {
		key   string
		value any
		want  string
	}{{"schemaVersion", "V3", "event_schema_unsupported"}, {"signature", "invalid", "log_signature"}} {
		save := outer[tt.key]
		outer[tt.key] = tt.value
		raw := mustJSON(t, outer)
		_, _, code := v.verifyEvent(raw)
		if code != tt.want {
			t.Fatal(code)
		}
		outer[tt.key] = save
	}
	producer["keyId"] = "unknown"
	raw := mustJSON(t, outer)
	if _, _, code := v.verifyEvent(raw); code != "producer_signature" {
		t.Fatal(code)
	}
	// Expired overlap is acceptable only when a currently valid certificate remains.
	ev.Attestations.ValidIdentityCerts = append(ev.Attestations.ValidIdentityCerts, certificate{Fingerprint: "old", Type: "X509-OV-CLIENT", NotAfter: fixtureTime.Add(-time.Hour).Format(time.RFC3339)})
	if _, ok := eventValidity(ev, env.SchemaVersion, fixtureTime); !ok {
		t.Fatal("expired overlap suppressed valid successor")
	}
	ev.ExpiresAt = fixtureTime.Add(time.Second).Format(time.RFC3339)
	if bound, ok := eventValidity(ev, env.SchemaVersion, fixtureTime); !ok || !bound.Equal(fixtureTime.Add(time.Second)) {
		t.Fatal("ignored signed registration expiry")
	}
}

func FuzzVerifyBundle(f *testing.F) {
	v, b := native(f, "v1")
	raw := mustJSON(f, b)
	f.Add(raw)
	f.Add([]byte(`{"profile":"ans-registration-v1"}`))
	f.Add([]byte{0xff})
	f.Fuzz(func(t *testing.T, data []byte) {
		bundle, e := DecodeBundle(data)
		if e != nil {
			return
		}
		// Acquisition helpers parse the same untrusted entry and signed inputs.
		_, _, _ = v.Reference(bundle.Entry)
		_, _ = v.RequiredHistory(bundle.Entry, bundle.Receipt, bundle.Checkpoint)
		result, e := v.Verify(t.Context(), bundle, fixtureTime)
		if e == nil && result.CurrentRegistration && (result.Receipt.State != "valid" || result.Inclusion.State != "valid" || result.Target.State != "valid" || result.Status.State != "valid") {
			t.Fatal("registration without complete checks")
		}
	})
}

func FuzzVerifyReceipt(f *testing.F) {
	v, b := native(f, "v1")
	f.Add(b.Receipt)
	f.Add([]byte{0xd2, 0x84})
	f.Add([]byte{0xff})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > MaxReceiptBytes {
			return
		}
		b.Receipt = data
		_, _ = v.Verify(t.Context(), b, fixtureTime)
	})
}

func TestStatusCertificateOverlap(t *testing.T) {
	certs := []certificate{{Fingerprint: "old", Type: "identity", NotAfter: fixtureTime.Format(time.RFC3339)}, {Fingerprint: "new", Type: "identity", NotAfter: fixtureTime.Add(time.Minute).Format(time.RFC3339)}}
	if _, ok := matchingCertificates([]statusCertificate{{Fingerprint: "old", Type: "identity"}}, certs, fixtureTime); ok {
		t.Fatal("expired status-only certificate inherited successor validity")
	}
	if bound, ok := matchingCertificates([]statusCertificate{{Fingerprint: "old", Type: "identity"}, {Fingerprint: "new", Type: "identity"}}, certs, fixtureTime); !ok || !bound.Equal(fixtureTime.Add(time.Minute)) {
		t.Fatal("valid certificate overlap lost")
	}
}

func TestCOSEProfileHeaders(t *testing.T) {
	log := newSigner(t)
	key, err := parseKey(log.public)
	if err != nil {
		t.Fatal(err)
	}
	v := &Verifier{policy: Policy{LogOrigin: "owned-ans"}, log: []publicKey{key}}
	base := map[int64]any{1: -7, 4: key.hint[:], 395: 1, 15: map[int64]any{1: "owned-ans", 6: fixtureTime.Unix()}}
	sign := func(h map[int64]any) []byte {
		return log.cose(t, []byte(`{}`), h, map[int64]any{396: inclusion{Size: 1, Root: make([]byte, 32)}})
	}
	if _, _, e := v.verifyCOSE(sign(base), false); e != nil {
		t.Fatal("valid header control rejected", e)
	}
	for _, change := range []func(map[int64]any){func(h map[int64]any) { delete(h, 1) }, func(h map[int64]any) { h[1] = -35 }, func(h map[int64]any) { h[2] = []int{999} }, func(h map[int64]any) { h[4] = []byte{0, 0, 0, 0} }, func(h map[int64]any) { h[15] = map[int64]any{1: "other", 6: fixtureTime.Unix()} }, func(h map[int64]any) { h[395] = 2 }} {
		h := map[int64]any{}
		for k, v := range base {
			h[k] = v
		}
		change(h)
		if _, _, e := v.verifyCOSE(sign(h), false); e == nil {
			t.Fatal("accepted unsupported signed COSE header")
		}
	}
}

func FuzzCanonicalEvent(f *testing.F) {
	f.Add([]byte(`{"future":{"text":"é","number":1e30},"eventType":"AGENT_REGISTERED"}`))
	f.Add([]byte(`{"a":1,"a":2}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > MaxReceiptBytes {
			return
		}
		_, _ = canonical(data)
	})
}

func TestTargetInsufficientEvidenceAndAuthorityMismatch(t *testing.T) {
	v, b := native(t, "v1")
	incomplete := mutateEntry(t, b, func(e map[string]any) { delete(e, "metadata") })
	if r := assess(t, v, incomplete, fixtureTime); r.Target.State != "unavailable" || r.CurrentRegistration {
		t.Fatal(r)
	}
	wrong, _ := parseKey(newSigner(t).public)
	v.registration["ans-ra-signer"] = wrong
	if r := assess(t, v, b, fixtureTime); r.Receipt.State != "valid" || r.Producer.Code != "producer_signature" || r.CurrentRegistration {
		t.Fatal(r)
	}
}
