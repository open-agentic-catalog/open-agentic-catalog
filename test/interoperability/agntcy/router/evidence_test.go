package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"github.com/fxamacker/cbor/v2"
)

func TestCheckpointGateReleaseCancellationAndBound(t *testing.T) {
	for _, scenario := range []string{"released", "cancelled", "expired", "aborted"} {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				gate := newCheckpointGate()
				if gate.finish("released") || gate.status() != "armed" {
					t.Fatal("unstarted gate released")
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := make(chan bool, 1)
				go func() { done <- gate.wait(ctx) }()
				synctest.Wait()
				if gate.status() != "waiting" {
					t.Fatal("gate did not block")
				}
				switch scenario {
				case "released", "aborted":
					if !gate.finish(scenario) {
						t.Fatal("gate did not finish")
					}
				case "cancelled":
					cancel()
				case "expired":
					time.Sleep(6 * time.Second)
				}
				if <-done != (scenario == "released") || gate.status() != scenario || gate.finish("released") || gate.wait(ctx) {
					t.Fatal("gate reused, failed open or lost its outcome")
				}
			})
		})
	}
}

func TestHistoryCorruptionChangesOnlySignature(t *testing.T) {
	sig := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 70))
	raw := []byte(`{"checkpoints":[{"logSize":3,"rootHash":"root","originName":"owned-ans","checkpointText":"signed body\n","publicKeyPem":"unsigned","signatures":[{"signatureType":"jws","rawSignature":"untouched"},{"signatureType":"c2sp","rawSignature":"` + sig + `","valid":true}]}]}`)
	var original, changed map[string]any
	if json.Unmarshal(raw, &original) != nil || json.Unmarshal(corruptHistorySignature(raw), &changed) != nil {
		t.Fatal("invalid control")
	}
	checkpoints, _ := changed["checkpoints"].([]any)
	if len(checkpoints) != 1 {
		t.Fatal("checkpoint inventory changed")
	}
	cp, _ := checkpoints[0].(map[string]any)
	signatures, _ := cp["signatures"].([]any)
	if len(signatures) != 2 {
		t.Fatal("signature inventory changed")
	}
	signature, _ := signatures[1].(map[string]any)
	if signature["rawSignature"] == sig || signature["valid"] != true {
		t.Fatal("signature unchanged or unsigned valid hint removed")
	}
	signature["rawSignature"] = sig
	if !reflect.DeepEqual(original, changed) {
		t.Fatal("control changed more than the c2sp signature")
	}
	for _, invalid := range []string{`invalid`, `{}`, `{"checkpoints":[]}`, `{"checkpoints":[{"signatures":[{"signatureType":"c2sp","rawSignature":"invalid"}]}]}`} {
		if string(corruptHistorySignature([]byte(invalid))) != "invalid owned history control" {
			t.Fatal("missing corruption accepted")
		}
	}
}

func TestEvidenceRoutesAndNativeBytes(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "tl:8080" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Fatal("authority/header escaped")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader([]byte{0, 1, 2, 3})), Header: http.Header{"Content-Type": []string{"application/scitt-receipt+cose"}}}, nil
	})}
	edge, err := evidenceEdge(http.NotFoundHandler(), "http://tl:8080", client)
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1/agents/00000000-0000-7000-8000-000000000001/receipt"
	request := httptest.NewRequestWithContext(t.Context(), "GET", path, nil)
	request.Header.Set("Authorization", "synthetic")
	request.Header.Set("Cookie", "private")
	w := httptest.NewRecorder()
	edge.ServeHTTP(w, request)
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), []byte{0, 1, 2, 3}) || calls != 1 {
		t.Fatal("native bytes changed")
	}
	for _, path := range []string{"/v1/agents/not-uuid/receipt", "/v1/agents/00000000-0000-7000-8000-000000000001/keys", "/keys", "/v1/log/append"} {
		w = httptest.NewRecorder()
		edge.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), "GET", path, nil))
		if w.Code != 404 || calls != 1 {
			t.Fatal("unselected evidence route", path)
		}
	}
	for _, mode := range []string{"receipt-signature", "missing-status", "gone-status", "unavailable", "native"} {
		control := httptest.NewRecorder()
		edge.ServeHTTP(control, httptest.NewRequestWithContext(t.Context(), "POST", "/_qualification/evidence", bytes.NewBufferString(`{"mode":"`+mode+`"}`)))
		if control.Code != 200 {
			t.Fatal(mode)
		}
		selected := path
		want := 200
		if mode == "missing-status" || mode == "gone-status" {
			selected = "/v1/agents/00000000-0000-7000-8000-000000000001/status-token"
			want = 404
			if mode == "gone-status" {
				want = 410
			}
		}
		if mode == "unavailable" {
			want = 503
		}
		w = httptest.NewRecorder()
		edge.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), "GET", selected, nil))
		if w.Code != want {
			t.Fatal(mode, w.Code)
		}
		if mode == "receipt-signature" && bytes.Equal(w.Body.Bytes(), []byte{0, 1, 2, 3}) {
			t.Fatal("tamper control inactive")
		}
	}
	w = httptest.NewRecorder()
	edge.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), "GET", "/_qualification/evidence", nil))
	var inventory struct {
		Requests []any `json:"requests"`
		Overflow bool  `json:"overflow"`
	}
	if json.Unmarshal(w.Body.Bytes(), &inventory) != nil || len(inventory.Requests) != 6 || inventory.Overflow {
		t.Fatal("request evidence lost")
	}
}

func TestProofRootCorruptionPreservesSignedBytes(t *testing.T) {
	proof := map[int64]any{-1: uint64(2), -2: uint64(1), -3: [][]byte{}, -4: bytes.Repeat([]byte{9}, 32)}
	sign1 := []any{[]byte("protected"), map[int64]any{396: proof}, []byte("payload"), []byte("signature")}
	content, err := cbor.Marshal(sign1)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := cbor.Marshal(cbor.RawTag{Number: 18, Content: content})
	if err != nil {
		t.Fatal(err)
	}
	changed := corruptProofRoot(raw)
	if bytes.Equal(raw, changed) {
		t.Fatal("proof unchanged")
	}
	var tag cbor.RawTag
	var decoded []cbor.RawMessage
	if cbor.Unmarshal(changed, &tag) != nil || cbor.Unmarshal(tag.Content, &decoded) != nil {
		t.Fatal("corrupted envelope rather than unsigned root")
	}
	for i, expected := range map[int]string{0: "protected", 2: "payload", 3: "signature"} {
		var value []byte
		if cbor.Unmarshal(decoded[i], &value) != nil || string(value) != expected {
			t.Fatal("signed bytes changed", i)
		}
	}
	if bytes.Equal(corruptProofRoot([]byte("invalid")), []byte("invalid")) {
		t.Fatal("invalid control not rejected")
	}
}

func TestUnsupportedFrameworkControlAndStrictSelection(t *testing.T) {
	original := []byte(`{"results":[{"identifier":"urn:air:alpha.ans.test:agents:alpha","trustManifest":{"identity":"https://alpha.ans.test"}}]}`)
	discovery := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(original) })
	edge, err := evidenceEdge(discovery, "http://tl:8080", &http.Client{})
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{`{"mode":"native"} {}`, `{"mode":"native","unknown":true}`, `{"mode":""}`} {
		w := httptest.NewRecorder()
		edge.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), "POST", "/_qualification/evidence", bytes.NewBufferString(invalid)))
		if w.Code != 400 {
			t.Fatal("invalid control admitted", invalid)
		}
	}
	for _, mode := range []string{"native", "unsupported-framework", "native"} {
		w := httptest.NewRecorder()
		edge.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), "POST", "/_qualification/evidence", bytes.NewBufferString(`{"mode":"`+mode+`"}`)))
		if w.Code != 200 {
			t.Fatal(mode)
		}
		w = httptest.NewRecorder()
		edge.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), "POST", "/v1/search", bytes.NewBufferString(`{}`)))
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
		if mode == "native" && !bytes.Equal(w.Body.Bytes(), original) {
			t.Fatal("native response changed")
		}
		if mode != "native" && !bytes.Contains(w.Body.Bytes(), []byte(`"trustSchema":{"identifier":"https://framework.example/unsupported","version":"1"}`)) {
			t.Fatal("framework control inactive")
		}
	}
}
