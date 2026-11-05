package main

import (
	"bytes"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func publisherDNSQuestion(name string, kind uint16) []byte {
	raw := []byte{0, 42, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0}
	for _, label := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		labelSize := len(label)
		if labelSize < 1 || labelSize > 63 {
			return nil
		}
		raw = append(raw, byte(labelSize))
		raw = append(raw, label...)
	}
	raw = append(raw, 0)
	raw = binary.BigEndian.AppendUint16(raw, kind)
	return append(raw, 0, 1)
}

func TestPublisherDNSOwnedAnswers(t *testing.T) {
	s := newPublisherDNS()
	name := "_open-agentic-catalog-challenge.example.test."
	value := strings.Repeat("x", 255)
	w := httptest.NewRecorder()
	s.control(w, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/publisher-dns", strings.NewReader(`{"name":"`+name+`","value":"`+value+`"}`)))
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
	query := publisherDNSQuestion(name, 16)
	answer := s.answer(query)
	if len(answer) != len(query)+13+255 || answer[3] != 0x80 || answer[7] != 1 || binary.BigEndian.Uint16(answer[len(query)+10:]) != 256 || !bytes.HasSuffix(answer, []byte(value)) {
		t.Fatal("TXT answer differs from the exact controlled value")
	}
	for _, query := range [][]byte{publisherDNSQuestion(name, 1), publisherDNSQuestion("external.example.", 16), publisherDNSQuestion("_open-agentic-catalog-challenge.other.test.", 16)} {
		answer := s.answer(query)
		if answer[3] != 0x83 || answer[7] != 0 {
			t.Fatal("unconfigured question was forwarded or answered")
		}
	}
	w = httptest.NewRecorder()
	s.report(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/publisher-dns", nil))
	if strings.TrimSpace(w.Body.String()) != `{"queries":2}` {
		t.Fatal("counter did not distinguish TXT checks")
	}
	w = httptest.NewRecorder()
	s.control(w, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/publisher-dns", strings.NewReader(`{"name":"`+name+`","value":""}`)))
	if w.Code != 204 || s.answer(query)[3] != 0x83 {
		t.Fatal("removing an answer did not restore NXDOMAIN")
	}
}

func TestPublisherDNSRefusesInvalidControlsAndPackets(t *testing.T) {
	s := newPublisherDNS()
	for _, body := range []string{`{`, `{}`, `{"name":"external.example.","value":"x"}`, `{"name":"_open-agentic-catalog-challenge.a.test.","value":"` + strings.Repeat("x", 256) + `"}`, `{"name":"_open-agentic-catalog-challenge.a.test.","extra":true}`, `{} {}`} {
		w := httptest.NewRecorder()
		s.control(w, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/publisher-dns", strings.NewReader(body)))
		if w.Code != 400 {
			t.Fatal("invalid fixture control accepted")
		}
	}
	query := publisherDNSQuestion("_open-agentic-catalog-challenge.a.test.", 16)
	for n := range len(query) {
		if s.answer(query[:n]) != nil {
			t.Fatalf("truncated packet accepted at %d", n)
		}
	}
	query[12] = 0xc0
	if s.answer(query) != nil {
		t.Fatal("compressed fixture question accepted")
	}
}
