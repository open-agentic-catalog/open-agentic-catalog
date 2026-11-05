package anstrust

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
)

func TestInclusionProofEncodingBounds(t *testing.T) {
	valid := inclusion{Size: 2, Index: 1, Root: bytes.Repeat([]byte{1}, 32), Path: [][]byte{bytes.Repeat([]byte{2}, 32)}}
	decode := func(value inclusion) (inclusion, error) {
		return decodeProof(signedMessage{unprotected: map[int64]cbor.RawMessage{396: encoded(value)}})
	}
	got, err := decode(valid)
	if err != nil || got.Size != 2 || got.Index != 1 || !bytes.Equal(got.Root, valid.Root) || len(got.Path) != 1 || !bytes.Equal(got.Path[0], valid.Path[0]) {
		t.Fatalf("valid proof fields changed: %+v %v", got, err)
	}
	for _, test := range []struct {
		name   string
		change func(*inclusion)
	}{
		{"zero size", func(p *inclusion) { p.Size = 0 }},
		{"index at size", func(p *inclusion) { p.Index = p.Size }},
		{"short root", func(p *inclusion) { p.Root = p.Root[:31] }},
		{"short path hash", func(p *inclusion) { p.Path = [][]byte{make([]byte, 31)} }},
		{"oversized path", func(p *inclusion) {
			p.Path = make([][]byte, 65)
			for i := range p.Path {
				p.Path[i] = make([]byte, 32)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.change(&candidate)
			if _, err := decode(candidate); !errors.Is(err, errEvidence) {
				t.Fatalf("invalid proof accepted: %v", err)
			}
		})
	}
}

func TestCheckpointBodyRequiresCanonicalSizeAndCompleteRoot(t *testing.T) {
	v, _ := native(t, "v1")
	root := bytes.Repeat([]byte{3}, 32)
	encode := func(size, root string) string { return v.policy.LogOrigin + "\n" + size + "\n" + root + "\n" }
	goodRoot := base64.StdEncoding.EncodeToString(root)
	size, got, err := v.checkpointBody(encode("2", goodRoot))
	if err != nil || size != 2 || !bytes.Equal(got, root) {
		t.Fatalf("valid checkpoint changed: %d %x %v", size, got, err)
	}
	for _, body := range []string{
		encode("0", goodRoot), encode("02", goodRoot), encode("+2", goodRoot),
		encode("18446744073709551616", goodRoot), encode("two", goodRoot),
		encode("2", "invalid"), encode("2", base64.StdEncoding.EncodeToString(root[:31])),
		strings.TrimSuffix(encode("2", goodRoot), "\n"),
	} {
		if _, _, err := v.checkpointBody(body); !errors.Is(err, errEvidence) {
			t.Fatalf("noncanonical or incomplete checkpoint accepted: %q %v", body, err)
		}
	}
}

func TestStatusMaximumAgeBoundsAssessmentBeforeTokenExpiry(t *testing.T) {
	v, bundle := synthetic(t, nil, func(s *statusClaims) {
		s.Issued = fixtureTime.Add(-5 * time.Minute).Unix()
		s.Expires = fixtureTime.Add(2 * time.Hour).Unix()
	})
	if v.policy.MaxStatusAge != time.Hour {
		t.Fatal("fixture must use the documented one-hour age policy")
	}
	result := assess(t, v, bundle, fixtureTime)
	want := fixtureTime.Add(55 * time.Minute)
	if !result.CurrentRegistration || result.ValidUntil == nil || !result.ValidUntil.Equal(want) {
		t.Fatalf("token expiry bypassed the configured evidence age: %+v", result)
	}
	result = assess(t, v, bundle, want)
	if result.CurrentRegistration || result.ValidUntil != nil || result.Status.Code != "status_freshness" || result.Inclusion.State != "valid" {
		t.Fatalf("evidence remained current at its exclusive age bound: %+v", result)
	}
}
