package anstrust

import (
	"bytes"
	"errors"

	"github.com/fxamacker/cbor/v2"
	"github.com/ldclabs/cose/cose"
	coseecdsa "github.com/ldclabs/cose/key/ecdsa"
)

var errEvidence = errors.New("invalid ANS evidence")

// Decode the entire tree first, so duplicate nested map keys cannot disappear
// behind RawMessage fields. Keep library verification on original signed bytes.
var cborDecoder = func() cbor.DecMode {
	d, err := (cbor.DecOptions{DupMapKey: cbor.DupMapKeyEnforcedAPF, IndefLength: cbor.IndefLengthForbidden, MaxNestedLevels: 16, MaxArrayElements: 128, MaxMapPairs: 64, UTF8: cbor.UTF8RejectInvalid}).DecMode()
	if err != nil {
		panic("invalid fixed ANS CBOR decoder options")
	}
	return d
}()

type signedMessage struct {
	payload     []byte
	protected   map[int64]cbor.RawMessage
	unprotected map[int64]cbor.RawMessage
}

func decodeCBOR(data []byte, out any) error {
	var all any
	if err := cborDecoder.Unmarshal(data, &all); err != nil {
		return errEvidence
	}
	if err := cborDecoder.Unmarshal(data, out); err != nil {
		return errEvidence
	}
	return nil
}

func (v *Verifier) verifyCOSE(data []byte, status bool) (signedMessage, publicKey, error) {
	var m signedMessage
	if len(data) == 0 || data[0] != 0xd2 {
		return m, publicKey{}, errEvidence
	}
	var tag cbor.RawTag
	if decodeCBOR(data, &tag) != nil || tag.Number != 18 {
		return m, publicKey{}, errEvidence
	}
	var parts []cbor.RawMessage
	if cborDecoder.Unmarshal(tag.Content, &parts) != nil || len(parts) != 4 {
		return m, publicKey{}, errEvidence
	}
	var header, signature []byte
	if cborDecoder.Unmarshal(parts[0], &header) != nil || len(header) == 0 || decodeCBOR(header, &m.protected) != nil || m.protected == nil ||
		cborDecoder.Unmarshal(parts[1], &m.unprotected) != nil || m.unprotected == nil ||
		cborDecoder.Unmarshal(parts[2], &m.payload) != nil || len(m.payload) == 0 ||
		cborDecoder.Unmarshal(parts[3], &signature) != nil || len(signature) != 64 {
		return m, publicKey{}, errEvidence
	}
	for key := range m.unprotected {
		if _, ok := m.protected[key]; ok {
			return m, publicKey{}, errEvidence
		}
	}
	var algorithm int
	var hint []byte
	if cborDecoder.Unmarshal(m.protected[1], &algorithm) != nil || algorithm != -7 || cborDecoder.Unmarshal(m.protected[4], &hint) != nil || len(hint) != 4 {
		return m, publicKey{}, errEvidence
	}
	// The fixed profile has no optional critical or delegated key semantics.
	allowed := map[int64]bool{1: true, 4: true, 15: true, 395: true}
	if status {
		allowed = map[int64]bool{1: true, 3: true, 4: true}
	}
	for key := range m.protected {
		if !allowed[key] {
			return m, publicKey{}, errEvidence
		}
	}
	if status {
		var typ string
		if cborDecoder.Unmarshal(m.protected[3], &typ) != nil || typ != "application/ans-status-token+cbor" || len(m.unprotected) != 0 {
			return m, publicKey{}, errEvidence
		}
	} else {
		var algorithm int
		var claims map[int64]cbor.RawMessage
		var issuer string
		var issued int64
		if len(m.unprotected) != 1 || m.unprotected[396] == nil || cborDecoder.Unmarshal(m.protected[395], &algorithm) != nil || algorithm != 1 ||
			decodeCBOR(m.protected[15], &claims) != nil || len(claims) != 2 || cborDecoder.Unmarshal(claims[1], &issuer) != nil || issuer != v.policy.LogOrigin ||
			cborDecoder.Unmarshal(claims[6], &issued) != nil || issued <= 0 {
			return m, publicKey{}, errEvidence
		}
	}
	for _, k := range v.log {
		if !bytes.Equal(k.hint[:], hint) {
			continue
		}
		key, err := coseecdsa.KeyFromPublic(k.key)
		if err != nil {
			return m, publicKey{}, errEvidence
		}
		verifier, err := coseecdsa.NewVerifier(key)
		if err != nil {
			return m, publicKey{}, errEvidence
		}
		if _, err := cose.VerifySign1Message[[]byte](verifier, data, nil); err == nil {
			return m, k, nil
		}
	}
	return m, publicKey{}, errEvidence
}
