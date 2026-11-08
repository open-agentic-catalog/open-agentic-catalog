package anstrust

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/fxamacker/cbor/v2"
)

// The signed-note fixture uses the profile's SPKI hint and exact body bytes.
// All signing is local; it does not run or contact an ANS producer.
func historySelectionNote(t *testing.T, key signer, origin string, size uint64, root []byte) []byte {
	t.Helper()
	spki, err := x509.MarshalPKIXPublicKey(&key.private.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	hint := sha256.Sum256(spki)
	body := fmt.Sprintf("%s\n%d\n%s\n", origin, size, base64.StdEncoding.EncodeToString(root))
	digest := sha256.Sum256([]byte(body))
	signature, err := ecdsa.SignASN1(rand.Reader, key.private, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return []byte(body + "\n— " + origin + " " + base64.StdEncoding.EncodeToString(append(hint[:4:4], signature...)) + "\n")
}

func TestHistorySelectionRequiresAuthenticatedMatchingEvidence(t *testing.T) {
	for _, version := range []string{"v1", "v2"} {
		t.Run(version, func(t *testing.T) {
			original, bundle := native(t, version)
			if result := assess(t, original, bundle, fixtureTime); !result.CurrentRegistration {
				t.Fatal("native complete evidence control is invalid", result)
			}
			if size, err := original.RequiredHistory(bundle.Entry, bundle.Receipt, bundle.Checkpoint); err != nil || size != 0 {
				t.Fatal("matching native checkpoint must not select history", size, err)
			}
			log := newSigner(t)
			policy := original.policy
			// Verifiers deliberately discard original PEM bytes. Supply the
			// checked-in public fixtures when constructing the overlap policy.
			registrationPEM, err := os.ReadFile("testdata/native/ra.pem")
			if err != nil {
				t.Fatal(err)
			}
			logPEM, err := os.ReadFile("testdata/native/tl.pem")
			if err != nil {
				t.Fatal(err)
			}
			policy.RegistrationKeys = map[string][]byte{"ans-ra-signer": registrationPEM}
			policy.LogKeys = [][]byte{logPEM, log.public}
			verifier, err := New(policy)
			if err != nil {
				t.Fatal("valid overlapping operator log keys refused", err)
			}
			// Native fixtures retain a proof of tree size two. An authenticated
			// latest tree of size three selects exactly that proof's history.
			checkpoint := historySelectionNote(t, log, policy.LogOrigin, 3, bytes.Repeat([]byte{42}, 32))
			if size, err := verifier.RequiredHistory(bundle.Entry, bundle.Receipt, checkpoint); err != nil || size != 2 {
				t.Fatal("advanced checkpoint did not select the exact native proof size", size, err)
			}
			message, _, err := verifier.verifyCOSE(bundle.Receipt, false)
			if err != nil {
				t.Fatal(err)
			}
			proof, err := decodeProof(message)
			if err != nil || proof.Size != 2 || !verifyInclusion(message.payload, proof) {
				t.Fatal("fixture must retain an authentic, valid size-two proof", proof, err)
			}
			for _, scenario := range []string{"unaccepted signer", "wrong origin", "older tree", "same-size wrong root", "proof encoding", "proof mismatch", "selected target mismatch"} {
				t.Run(scenario, func(t *testing.T) {
					entry, receipt, note := bundle.Entry, bundle.Receipt, checkpoint
					switch scenario {
					case "unaccepted signer":
						note = historySelectionNote(t, newSigner(t), policy.LogOrigin, 3, bytes.Repeat([]byte{42}, 32))
					case "wrong origin":
						note = historySelectionNote(t, log, "other-log", 3, bytes.Repeat([]byte{42}, 32))
					case "older tree":
						note = historySelectionNote(t, log, policy.LogOrigin, 1, proof.Root)
					case "same-size wrong root":
						root := bytes.Clone(proof.Root)
						root[0] ^= 1
						note = historySelectionNote(t, log, policy.LogOrigin, 2, root)
					case "proof encoding", "proof mismatch":
						candidate := proof
						candidate.Root = bytes.Clone(proof.Root)
						if scenario == "proof encoding" {
							candidate.Root = candidate.Root[:31]
						} else {
							candidate.Root[0] ^= 1
						}
						// Inclusion is an unprotected COSE field: tampering does
						// not invalidate the otherwise authentic signed envelope.
						receipt = mutateCOSE(t, receipt, func(parts []cbor.RawMessage) { parts[1] = encoded(map[int64]any{396: candidate}) })
						if _, _, err := verifier.verifyCOSE(receipt, false); err != nil {
							t.Fatal("proof mutation must preserve receipt authentication", err)
						}
					case "selected target mismatch":
						changed := mutateEntry(t, bundle, func(e map[string]any) { e["displayName"] = "Another selected agent" })
						entry = changed.Entry
					}
					if size, err := verifier.RequiredHistory(entry, receipt, note); !errors.Is(err, errEvidence) || size != 0 {
						t.Fatal("failed prerequisite selected history or lost its evidence classification", size, err)
					}
				})
			}
		})
	}
}

func TestHistorySelectionEntryByteBoundary(t *testing.T) {
	verifier, bundle := native(t, "v1")
	if size, err := verifier.RequiredHistory(bundle.Entry, bundle.Receipt, bundle.Checkpoint); err != nil || size != 0 {
		t.Fatal("native control is invalid", size, err)
	}
	entry := bytes.Clone(bundle.Entry)
	entry = append(entry, bytes.Repeat([]byte(" "), MaxEntryBytes-len(entry))...)
	if size, err := verifier.RequiredHistory(entry, bundle.Receipt, bundle.Checkpoint); err != nil || size != 0 {
		t.Fatal("valid exact-byte-boundary entry refused", size, err)
	}
	entry = append(entry, ' ')
	if !json.Valid(entry) {
		t.Fatal("oversized fixture must remain valid JSON")
	}
	if size, err := verifier.RequiredHistory(entry, bundle.Receipt, bundle.Checkpoint); !errors.Is(err, ErrBundle) || size != 0 {
		t.Fatal("oversized entry selected history or lost its bound classification", size, err)
	}
}

func TestHistorySelectionBoundsAdditionalCheckpointSignatures(t *testing.T) {
	verifier, bundle := native(t, "v1")
	body, signatureText, ok := strings.Cut(string(bundle.Checkpoint), "\n\n")
	if !ok {
		t.Fatal("native fixture must contain a signed note")
	}
	signatures := strings.Split(strings.TrimSuffix(signatureText, "\n"), "\n")
	if len(signatures) > 8 {
		t.Fatal("native signature fixture exceeds the profile bound")
	}
	for len(signatures) < 8 {
		signatures = append(signatures, "unsupported additional signature")
	}
	atLimit := []byte(body + "\n\n" + strings.Join(signatures, "\n") + "\n")
	if size, err := verifier.RequiredHistory(bundle.Entry, bundle.Receipt, atLimit); err != nil || size != 0 {
		t.Fatal("accepted native signature among eight lines was lost", size, err)
	}
	oversized := append(bytes.Clone(atLimit), []byte("unsupported additional signature\n")...)
	if size, err := verifier.RequiredHistory(bundle.Entry, bundle.Receipt, oversized); !errors.Is(err, errEvidence) || size != 0 {
		t.Fatal("over-limit signatures were accepted", size, err)
	}
}
