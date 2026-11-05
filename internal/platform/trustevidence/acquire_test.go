package trustevidence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/standards/anstrust"
)

const tenant = "00000000-0000-7000-8000-000000000001"
const nativeOrigin = "https://tl.ans-qualification.svc.cluster.local"

var assessmentTime = time.Date(2026, 10, 9, 14, 31, 30, 0, time.UTC)

type outboundFunc func(context.Context, fetcher.Request) (fetcher.Result, error)

func (f outboundFunc) Fetch(ctx context.Context, r fetcher.Request) (fetcher.Result, error) {
	return f(ctx, r)
}

func fixture(t *testing.T, origin string) (config.Config, anstrust.Bundle) {
	t.Helper()
	// Isolate explicit configuration loading from inherited workflow settings.
	for _, name := range config.EnvironmentVariables() {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	base, err := config.LoadFor("../../../config/local.json", config.RoleAPI)
	if err != nil {
		t.Fatal(err)
	}
	ra, err := filepath.Abs("../../standards/anstrust/testdata/native/ra.pem")
	if err != nil {
		t.Fatal(err)
	}
	log, err := filepath.Abs("../../standards/anstrust/testdata/native/tl.pem")
	if err != nil {
		t.Fatal(err)
	}
	base.TrustVerification.Enabled = true
	base.TrustVerification.Policies = []config.TrustPolicy{{Name: "owned-ans", Profile: anstrust.Profile, RegistrationAuthority: "ans-ra-local", LogOrigin: "owned-ans", EvidenceOrigin: origin, PublisherDomains: []string{"trust-v1.ans.test", "trust-v2.ans.test"}, RegistrationKeys: []config.TrustKey{{ID: "ans-ra-signer", File: ra}}, LogKeyFiles: []string{log}}}
	dir := t.TempDir()
	policy := filepath.Join(dir, "egress.json")
	raw, err := json.Marshal(config.FederationPolicy{SchemaVersion: 1, PolicyID: "owned", Revision: 1, Public: config.FederationPublicPolicy{Mode: "allowlist", Origins: []string{origin}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policy, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	base.Federation.PolicyFile = policy
	path := filepath.Join(dir, "config.json")
	raw, err = json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := config.LoadFor(path, config.RoleAPI)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile("../../standards/anstrust/testdata/native/v1.json")
	if err != nil {
		t.Fatal(err)
	}
	b, err := anstrust.DecodeBundle(raw)
	if err != nil {
		t.Fatal(err)
	}
	b.Entry = bytes.ReplaceAll(b.Entry, []byte(nativeOrigin), []byte(origin))
	return c, b
}

func serveEvidence(b anstrust.Bundle, r fetcher.Request) fetcher.Result {
	var data []byte
	media := "application/scitt-receipt+cose"
	switch {
	case strings.HasSuffix(r.URL, "/receipt"):
		data = b.Receipt
	case strings.HasSuffix(r.URL, "/checkpoint"):
		data = b.Checkpoint
		media = "text/plain; charset=utf-8"
	case strings.Contains(r.URL, "/history?"):
		data = b.CheckpointHistory
		media = "application/json"
	case strings.HasSuffix(r.URL, "/status-token"):
		data = b.StatusToken
		media = "application/ans-status-token+cbor"
	}
	return fetcher.Result{Status: 200, MediaType: media, Body: data, FinalURL: r.URL}
}

func TestAcquireNativeEvidenceWithoutCredentials(t *testing.T) {
	c, b := fixture(t, nativeOrigin)
	var urls []string
	var deadline time.Time
	a, err := New(c, outboundFunc(func(ctx context.Context, r fetcher.Request) (fetcher.Result, error) {
		urls = append(urls, r.URL)
		if r.Method != http.MethodGet || r.Headers != nil || r.CredentialBinding != "" || r.ExpectedCredentialRevision != "" || len(r.Body) != 0 || r.ETag != "" || r.LastModified != "" || !r.RejectRedirects || !r.RejectOriginChanges || !r.IdentityEncoding || r.TenantID != tenant || r.ExpectedPolicyDigest != c.Federation.PolicyDigest {
			t.Fatalf("unsafe request: %+v", r)
		}
		current, ok := ctx.Deadline()
		if !ok || time.Until(current) > 15*time.Second {
			t.Fatal("attempt deadline missing")
		}
		if !deadline.IsZero() && !deadline.Equal(current) {
			t.Fatal("a later request renewed the attempt deadline")
		}
		deadline = current
		return serveEvidence(b, r), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.Acquire(context.Background(), "owned-ans", tenant, b.Entry)
	if err != nil {
		t.Fatal(err)
	}
	if len(urls) != 3 || urls[1] != nativeOrigin+"/checkpoint" || !bytes.Equal(got.Receipt, b.Receipt) || !bytes.Equal(got.StatusToken, b.StatusToken) || len(got.CheckpointHistory) != 0 {
		t.Fatal("wrong evidence or requests", urls)
	}
	_, v, _ := c.TrustVerification.Policy("owned-ans")
	r, err := v.Verify(context.Background(), got, assessmentTime)
	if err != nil || !r.CurrentRegistration {
		t.Fatalf("native evidence invalid: %+v %v", r, err)
	}
}

func TestAcquireRejectsUnsafeReferencesWithoutContact(t *testing.T) {
	c, b := fixture(t, nativeOrigin)
	calls := 0
	a, err := New(c, outboundFunc(func(context.Context, fetcher.Request) (fetcher.Result, error) { calls++; return fetcher.Result{}, nil }))
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(map[string]any){
		"unknown framework": func(e map[string]any) {
			entryObject(t, e["trustManifest"])["trustSchema"] = map[string]any{"verificationMethod": "https://attacker.test/execute"}
		},
		"another origin": func(e map[string]any) {
			entryObject(t, entryArray(t, entryObject(t, e["trustManifest"])["attestations"])[0])["uri"] = "https://attacker.test/v1/agents/00000000-0000-4000-8000-000000000001/receipt"
		},
		"duplicate references": func(e map[string]any) {
			m := entryObject(t, e["trustManifest"])
			a := entryArray(t, m["attestations"])
			m["attestations"] = append(a, a[0])
		},
		"path traversal": func(e map[string]any) {
			entryObject(t, entryArray(t, entryObject(t, e["trustManifest"])["attestations"])[0])["uri"] = nativeOrigin + "/v1/agents/../receipt"
		},
		"wrong publisher": func(e map[string]any) { e["identifier"] = "urn:air:attacker.test:agents:sample" },
		"no reference":    func(e map[string]any) { entryObject(t, e["trustManifest"])["attestations"] = []any{} },
	} {
		t.Run(name, func(t *testing.T) {
			var e map[string]any
			if err := json.Unmarshal(b.Entry, &e); err != nil {
				t.Fatal(err)
			}
			change(e)
			raw, err := json.Marshal(e)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := a.Acquire(context.Background(), "owned-ans", tenant, raw); err == nil {
				t.Fatal("unsafe entry accepted")
			}
		})
	}
	if _, err := a.Acquire(context.Background(), "unknown", tenant, b.Entry); !errors.Is(err, ErrPolicy) {
		t.Fatal(err)
	}
	if _, err := a.Acquire(context.Background(), "owned-ans", "invalid", b.Entry); !errors.Is(err, ErrPolicy) {
		t.Fatal(err)
	}
	if _, err := a.Acquire(context.Background(), "owned-ans", tenant, bytes.Repeat([]byte("x"), anstrust.MaxEntryBytes+1)); !errors.Is(err, anstrust.ErrBundle) {
		t.Fatal("oversized entry admitted", err)
	}
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	if _, err := a.Acquire(expired, "owned-ans", tenant, b.Entry); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("expired caller budget admitted", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Acquire(ctx, "owned-ans", tenant, b.Entry); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	c.TrustVerification.Enabled = false
	a, err = New(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Acquire(context.Background(), "owned-ans", tenant, b.Entry); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("rejected input contacted upstream")
	}
}

func TestAcquireLimitsFailuresAndPermanentNegatives(t *testing.T) {
	for _, name := range []string{"body", "bundle", "requests", "media", "redirect", "http503", "http404", "transport", "signature", "checkpoint", "cancel"} {
		t.Run(name, func(t *testing.T) {
			c, b := fixture(t, nativeOrigin)
			switch name {
			case "bundle":
				c.TrustVerification.Limits.BundleBytes = 1024
			case "requests":
				c.TrustVerification.MaxRequests = 1
			}
			calls := 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			a, err := New(c, outboundFunc(func(_ context.Context, r fetcher.Request) (fetcher.Result, error) {
				calls++
				response := serveEvidence(b, r)
				switch name {
				case "body":
					response.Body = bytes.Repeat([]byte("a"), int(r.MaxResponseBytes)+1)
				case "media":
					response.MediaType = "text/html"
				case "redirect":
					response.FinalURL = "https://elsewhere.test/receipt"
				case "http503":
					response.Status = 503
				case "http404":
					response.Status = 404
				case "transport":
					return fetcher.Result{}, fetcher.ErrPolicyDenied
				case "signature":
					response.Body = append([]byte(nil), response.Body...)
					response.Body[len(response.Body)-1] ^= 1
				case "checkpoint":
					if calls == 2 {
						response.Body = []byte("unsigned checkpoint")
					}
				case "cancel":
					cancel()
				}
				return response, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			got, err := a.Acquire(ctx, "owned-ans", tenant, b.Entry)
			if name == "signature" || name == "checkpoint" {
				if err != nil {
					t.Fatal(err)
				}
				_, v, _ := c.TrustVerification.Policy("owned-ans")
				r, e := v.Verify(context.Background(), got, assessmentTime)
				if e != nil || r.CurrentRegistration {
					t.Fatal("negative evidence lost", r, e)
				}
				if name == "signature" && calls != 1 || name == "checkpoint" && calls != 2 {
					t.Fatal("contact continued after permanent failure")
				}
			} else if err == nil {
				t.Fatal("failure accepted")
			}
			if name == "bundle" {
				if !errors.Is(err, ErrBudget) || calls != 0 {
					t.Fatal("entry exceeding complete bundle budget fetched evidence or lost budget refusal", err)
				}
				raw, e := json.Marshal(got)
				if e != nil || int64(len(raw)) > c.TrustVerification.Limits.BundleBytes {
					t.Fatal("failed acquisition returned evidence beyond the complete bundle budget", e)
				}
			}
			if name == "requests" && calls != 1 {
				t.Fatal("request budget not enforced")
			}
			if strings.HasPrefix(name, "http") {
				var h *HTTPError
				if !errors.As(err, &h) || h.Status != map[string]int{"http503": 503, "http404": 404}[name] {
					t.Fatal("status classification lost", err)
				}
				if h.Error() != "ANS evidence endpoint did not return success" {
					t.Fatal("HTTP diagnostic contains unreviewed remote text", h.Error())
				}
			}
			if name == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}

func TestAcquirerCopiesRouteSelections(t *testing.T) {
	c, b := fixture(t, nativeOrigin)
	a, err := New(c, outboundFunc(func(_ context.Context, r fetcher.Request) (fetcher.Result, error) {
		if r.NetworkProfile != "" {
			t.Fatal("caller mutated route authority")
		}
		return serveEvidence(b, r), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	c.TrustVerification.Policies[0].NetworkProfile = "attacker-route"
	got, err := a.Acquire(context.Background(), "owned-ans", tenant, b.Entry)
	if err != nil || !reflect.DeepEqual(got.Entry, b.Entry) {
		t.Fatal(err)
	}
}

func TestAcquireCompleteBundleByteBoundary(t *testing.T) {
	c, b := fixture(t, nativeOrigin)
	b.CheckpointHistory = nil // Same-size checkpoints need no history request.
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	for _, exact := range []bool{false, true} {
		t.Run(map[bool]string{false: "one byte short", true: "exact fit"}[exact], func(t *testing.T) {
			settings := c
			settings.TrustVerification.Limits.BundleBytes = int64(len(raw))
			if !exact {
				settings.TrustVerification.Limits.BundleBytes--
			}
			calls := 0
			a, err := New(settings, outboundFunc(func(_ context.Context, r fetcher.Request) (fetcher.Result, error) {
				calls++
				return serveEvidence(b, r), nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			got, err := a.Acquire(context.Background(), "owned-ans", tenant, b.Entry)
			if calls != 3 {
				t.Fatal("fixture did not exercise the cumulative status budget", calls)
			}
			if exact {
				if err != nil || !reflect.DeepEqual(got, b) {
					t.Fatal("complete bundle within the byte budget was refused", err)
				}
			} else if !errors.Is(err, ErrBudget) || len(got.StatusToken) != 0 {
				t.Fatal("complete bundle escaped its cumulative byte budget", err)
			}
		})
	}
	t.Run("receipt fits without room for checkpoint", func(t *testing.T) {
		partial := b
		partial.Checkpoint, partial.StatusToken = nil, nil
		raw, err := json.Marshal(partial)
		if err != nil {
			t.Fatal(err)
		}
		settings := c
		settings.TrustVerification.Limits.BundleBytes = int64(len(raw))
		calls := 0
		a, err := New(settings, outboundFunc(func(_ context.Context, r fetcher.Request) (fetcher.Result, error) {
			calls++
			return serveEvidence(b, r), nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		got, err := a.Acquire(context.Background(), "owned-ans", tenant, b.Entry)
		if !errors.Is(err, ErrBudget) || calls != 1 || !reflect.DeepEqual(got, partial) {
			t.Fatal("partial receipt was lost or a checkpoint request exceeded the budget", err, calls)
		}
	})
	t.Run("minimal invalid receipt at exact limit", func(t *testing.T) {
		// A malformed receipt is retained negative evidence when it fits. The
		// complete budget must not reject an exact fit before the first request.
		b.Receipt = []byte{0}
		b.Checkpoint, b.StatusToken = nil, nil
		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		c.TrustVerification.Limits.BundleBytes = int64(len(raw))
		calls := 0
		a, err := New(c, outboundFunc(func(_ context.Context, r fetcher.Request) (fetcher.Result, error) {
			calls++
			return serveEvidence(b, r), nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		got, err := a.Acquire(context.Background(), "owned-ans", tenant, b.Entry)
		if err != nil || calls != 1 || !reflect.DeepEqual(got, b) {
			t.Fatal("exact-fit negative evidence was not retained", err, calls)
		}
		_, verifier, _ := c.TrustVerification.Policy("owned-ans")
		result, err := verifier.Verify(context.Background(), got, assessmentTime)
		if err != nil || result.Receipt.State != "invalid" || result.CurrentRegistration {
			t.Fatal("malformed receipt was not a negative assessment", result, err)
		}
	})
}

func TestAcquirerRequiresLoadedAuthorityAndEgress(t *testing.T) {
	for _, missing := range []string{"transport", "egress identity", "loaded keys", "valid settings"} {
		t.Run(missing, func(t *testing.T) {
			c, _ := fixture(t, nativeOrigin)
			var outbound Outbound = outboundFunc(func(context.Context, fetcher.Request) (fetcher.Result, error) {
				t.Fatal("construction contacted the authority")
				return fetcher.Result{}, nil
			})
			switch missing {
			case "transport":
				outbound = nil
			case "egress identity":
				c.Federation.PolicyDigest = ""
			case "loaded keys":
				var unloaded config.TrustVerification
				raw, err := json.Marshal(c.TrustVerification)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(raw, &unloaded); err != nil {
					t.Fatal(err)
				}
				c.TrustVerification = unloaded
			case "valid settings":
				c.TrustVerification.MaxRequests = 0
			}
			if a, err := New(c, outbound); err == nil || a != nil {
				t.Fatal("unusable acquisition configuration admitted", missing)
			}
		})
	}
}

func entryObject(t *testing.T, value any) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("expected object, got %T", value)
	}
	return result
}
func entryArray(t *testing.T, value any) []any {
	t.Helper()
	result, ok := value.([]any)
	if !ok || len(result) == 0 {
		t.Fatalf("expected nonempty array, got %T", value)
	}
	return result
}
func entryString(t *testing.T, value any) string {
	t.Helper()
	result, ok := value.(string)
	if !ok {
		t.Fatalf("expected string, got %T", value)
	}
	return result
}
