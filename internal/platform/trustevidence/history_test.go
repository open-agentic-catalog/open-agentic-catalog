package trustevidence

import (
	"bytes"
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
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
)

func TestAcquireOneExactSizeHistory(t *testing.T) {
	for _, name := range []string{"valid", "missing", "empty", "extra candidates", "wrong root", "unsigned text", "request limit"} {
		t.Run(name, func(t *testing.T) {
			c, b := fixture(t, nativeOrigin)
			// Add a test log key for an authentic later checkpoint. The original
			// native key still authenticates the receipt and same-size history.
			key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
			if err != nil {
				t.Fatal(err)
			}
			hint := sha256.Sum256(der)
			path := filepath.Join(t.TempDir(), "overlap.pem")
			if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o600); err != nil {
				t.Fatal(err)
			}
			c.TrustVerification.Policies[0].LogKeyFiles = append(c.TrustVerification.Policies[0].LogKeyFiles, path)
			if name == "request limit" {
				c.TrustVerification.MaxRequests = 3
			}
			settings, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			path = filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, settings, 0o600); err != nil {
				t.Fatal(err)
			}
			c, err = config.LoadFor(path, config.RoleAPI)
			if err != nil {
				t.Fatal(err)
			}
			body := "owned-ans\n3\n" + base64.StdEncoding.EncodeToString(make([]byte, 32)) + "\n"
			hash := sha256.Sum256([]byte(body))
			signature, err := ecdsa.SignASN1(rand.Reader, key, hash[:])
			if err != nil {
				t.Fatal(err)
			}
			b.Checkpoint = []byte(body + "\n— owned-ans " + base64.StdEncoding.EncodeToString(append(hint[:4], signature...)) + "\n")
			if name == "empty" {
				b.CheckpointHistory = []byte(`{"checkpoints":[]}`)
			}
			if name == "extra candidates" || name == "wrong root" || name == "unsigned text" {
				var history map[string]any
				if err := json.Unmarshal(b.CheckpointHistory, &history); err != nil {
					t.Fatal(err)
				}
				cp := entryObject(t, entryArray(t, history["checkpoints"])[0])
				switch name {
				case "extra candidates":
					history["checkpoints"] = append(entryArray(t, history["checkpoints"]), cp)
				case "wrong root":
					cp["rootHash"] = base64.StdEncoding.EncodeToString(make([]byte, 32))
				case "unsigned text":
					cp["checkpointText"] = strings.TrimSuffix(entryString(t, cp["checkpointText"]), "\n")
				}
				b.CheckpointHistory, err = json.Marshal(history)
				if err != nil {
					t.Fatal(err)
				}
			}
			var urls []string
			a, err := New(c, outboundFunc(func(_ context.Context, r fetcher.Request) (fetcher.Result, error) {
				urls = append(urls, r.URL)
				response := serveEvidence(b, r)
				if strings.Contains(r.URL, "/history?") && name == "missing" {
					response.Status = 404
					response.Body = nil
				}
				return response, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			got, err := a.Acquire(context.Background(), "owned-ans", tenant, b.Entry)
			if len(urls) < 3 || urls[2] != nativeOrigin+"/v1/log/checkpoint/history?fromSize=2&toSize=2&limit=1" {
				t.Fatal("history not exactly bounded", urls)
			}
			_, v, _ := c.TrustVerification.Policy("owned-ans")
			r, e := v.Verify(context.Background(), got, assessmentTime)
			if e != nil {
				t.Fatal(e)
			}
			switch name {
			case "valid":
				if err != nil || len(urls) != 4 || !r.CurrentRegistration || !bytes.Equal(got.CheckpointHistory, b.CheckpointHistory) {
					t.Fatalf("valid history failed: %+v %v %v", r, err, urls)
				}
			case "missing":
				var h *HTTPError
				if !errors.As(err, &h) || h.Status != 404 || r.Checkpoint.State != "unavailable" || r.Checkpoint.Code != "checkpoint_history_missing" || len(urls) != 3 {
					t.Fatalf("missing history misclassified: %+v %v", r, err)
				}
			case "empty":
				if err != nil || r.Checkpoint.State != "unavailable" || len(urls) != 3 {
					t.Fatal("empty history not unavailable", r, err)
				}
			case "request limit":
				if !errors.Is(err, ErrBudget) || len(urls) != 3 || r.Status.State != "unavailable" {
					t.Fatal("fourth request escaped budget", r, err)
				}
			default:
				if err != nil || len(urls) != 3 || r.Checkpoint.State != "invalid" || r.CurrentRegistration {
					t.Fatal("bad history accepted or caused more contact", r, err)
				}
			}
		})
	}
}
