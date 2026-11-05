package anstrust

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/fxamacker/cbor/v2"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/transparency-dev/merkle/proof"
	"github.com/transparency-dev/merkle/rfc6962"
)

type inclusion struct {
	Size  uint64   `cbor:"-1,keyasint"`
	Index uint64   `cbor:"-2,keyasint"`
	Path  [][]byte `cbor:"-3,keyasint"`
	Root  []byte   `cbor:"-4,keyasint"`
}

func decodeProof(m signedMessage) (inclusion, error) {
	var p inclusion
	var fields map[int64]cbor.RawMessage
	if decodeCBOR(m.unprotected[396], &fields) != nil || len(fields) != 4 || decodeCBOR(m.unprotected[396], &p) != nil ||
		p.Size == 0 || p.Index >= p.Size || len(p.Path) > 64 || len(p.Root) != 32 {
		return p, errEvidence
	}
	for _, h := range p.Path {
		if len(h) != 32 {
			return p, errEvidence
		}
	}
	return p, nil
}

// checkpointBody is the exact signed note body, including its final newline.
func (v *Verifier) checkpointBody(body string) (uint64, []byte, error) {
	lines := strings.Split(body, "\n")
	if len(lines) != 4 || lines[0] != v.policy.LogOrigin || lines[3] != "" {
		return 0, nil, errEvidence
	}
	size, err := strconv.ParseUint(lines[1], 10, 64)
	if err != nil || size == 0 || strconv.FormatUint(size, 10) != lines[1] {
		return 0, nil, errEvidence
	}
	root, err := strictBase64(lines[2], false)
	if err != nil || len(root) != 32 {
		return 0, nil, errEvidence
	}
	return size, root, nil
}

func (v *Verifier) checkpointSignature(body, signature, name string) bool {
	if name != v.policy.LogOrigin {
		return false
	}
	raw, err := strictBase64(signature, false)
	if err != nil || len(raw) < 12 || len(raw) > 128 {
		return false
	}
	h := sha256.Sum256([]byte(body))
	for _, k := range v.log {
		if bytes.Equal(raw[:4], k.hint[:]) && ecdsa.VerifyASN1(k.key, h[:], raw[4:]) {
			return true
		}
	}
	return false
}

func (v *Verifier) rawCheckpoint(note []byte) (uint64, []byte, error) {
	body, signatures, ok := strings.Cut(string(note), "\n\n")
	if !ok {
		return 0, nil, errEvidence
	}
	body += "\n"
	size, root, err := v.checkpointBody(body)
	if err != nil {
		return 0, nil, errEvidence
	}
	lines := strings.Split(strings.TrimSuffix(signatures, "\n"), "\n")
	if len(lines) > 8 {
		return 0, nil, errEvidence
	}
	for _, line := range lines {
		parts := strings.Fields(line)
		// The native producer adds a JWS signature as well. Unsupported extra
		// signatures cannot replace the required accepted c2sp DER signature.
		if len(parts) == 3 && parts[0] == "—" && v.checkpointSignature(body, parts[2], parts[1]) {
			return size, root, nil
		}
	}
	return 0, nil, errEvidence
}

func (v *Verifier) historicalCheckpoint(data []byte, p inclusion) bool {
	var history struct {
		Checkpoints []struct {
			Text       string `json:"checkpointText"`
			Size       uint64 `json:"logSize"`
			Root       string `json:"rootHash"`
			Origin     string `json:"originName"`
			Signatures []struct {
				Raw  string `json:"rawSignature"`
				Name string `json:"signerName"`
				Type string `json:"signatureType"`
			} `json:"signatures"`
		} `json:"checkpoints"`
	}
	if jsondoc.Validate(data) != nil || json.Unmarshal(data, &history) != nil || len(history.Checkpoints) != 1 {
		return false
	}
	c := history.Checkpoints[0]
	size, root, err := v.checkpointBody(c.Text)
	if err != nil || size != p.Size || c.Size != size || c.Origin != v.policy.LogOrigin || !bytes.Equal(root, p.Root) || len(c.Signatures) > 8 {
		return false
	}
	outerRoot, err := strictBase64(c.Root, false)
	if err != nil || !bytes.Equal(root, outerRoot) {
		return false
	}
	for _, s := range c.Signatures {
		if s.Type == "c2sp" && v.checkpointSignature(c.Text, s.Raw, s.Name) {
			return true
		}
	}
	return false
}

func (v *Verifier) verifyCheckpoint(b Bundle, p inclusion) bool {
	size, root, err := v.rawCheckpoint(b.Checkpoint)
	if err != nil || size < p.Size {
		return false
	}
	if size == p.Size {
		return bytes.Equal(root, p.Root)
	}
	return v.historicalCheckpoint(b.CheckpointHistory, p)
}

// missingCheckpointHistory distinguishes an empty native page from malformed
// or mismatched evidence. Neither case can establish authenticated inclusion.
func missingCheckpointHistory(data []byte) bool {
	if len(data) == 0 {
		return true
	}
	var fields map[string]json.RawMessage
	if jsondoc.Validate(data) != nil || json.Unmarshal(data, &fields) != nil {
		return false
	}
	raw := bytes.TrimSpace(fields["checkpoints"])
	var checkpoints []json.RawMessage
	return len(raw) > 0 && raw[0] == '[' && json.Unmarshal(raw, &checkpoints) == nil && len(checkpoints) == 0
}

func verifyInclusion(payload []byte, p inclusion) bool {
	return proof.VerifyInclusion(rfc6962.DefaultHasher, p.Index, p.Size, rfc6962.DefaultHasher.HashLeaf(payload), p.Path, p.Root) == nil
}
