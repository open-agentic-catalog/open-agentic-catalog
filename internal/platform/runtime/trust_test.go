package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/standards/anstrust"
)

const nativeTrustOrigin = "https://tl.ans-qualification.svc.cluster.local"

type trustStoreProbe struct {
	jobs.Store
	job                   jobs.Job
	err, readErr          error
	enqueues, completions int
	result                json.RawMessage
	evidence              []audit.Record
}

func (s *trustStoreProbe) Get(context.Context, string, string) (jobs.Job, error) {
	return s.job, s.readErr
}
func (s *trustStoreProbe) Enqueue(_ context.Context, job jobs.Job, evidence ...audit.Record) (jobs.Job, bool, error) {
	s.enqueues++
	s.evidence = evidence
	return job, false, s.err
}
func (s *trustStoreProbe) Complete(_ context.Context, tenant, id, owner string, _ time.Time, raw json.RawMessage, evidence ...audit.Record) (jobs.Job, error) {
	s.completions++
	s.result, s.evidence = raw, evidence
	return jobs.Job{ID: id, TenantID: tenant, LeaseOwner: owner}, s.err
}

func TestTrustMemorySourceFenceAndDelegation(t *testing.T) {
	for _, operation := range []string{"enqueue", "complete"} {
		for _, scenario := range []string{"current", "ordinary", "changed", "foreign tenant", "malformed", "store failure", "read failure"} {
			t.Run(operation+"/"+scenario, func(t *testing.T) {
				entry := []byte(`{"identifier":"urn:air:peer.example:agents:one","type":"application/a2a-agent-card+json","url":"https://peer.example/card"}`)
				source := federation.Source{ID: "00000000-0000-7000-8000-000000000010", TenantID: "00000000-0000-7000-8000-000000000001", Revision: 1, Status: federation.SourceActive, TrustVerificationPolicy: "owned", PolicyDigest: "sha256:" + strings.Repeat("a", 64)}
				p := jobs.TrustPayload{SourceID: source.ID, SourceRevision: 1, SourcePolicyDigest: source.PolicyDigest, PolicyName: "owned", PolicyDigest: source.PolicyDigest, Identifier: "urn:air:peer.example:agents:one", MediaType: "application/a2a-agent-card+json", Delivery: "url", ExactEntryDigest: audit.DigestBytes(entry), Entry: entry}
				payload, err := json.Marshal(p)
				if err != nil {
					t.Fatal(err)
				}
				job := jobs.Job{ID: "00000000-0000-7000-8000-000000000011", TenantID: source.TenantID, Kind: jobs.TrustKind, PayloadVersion: 1, Payload: payload}
				sentinel := errors.New("injected store failure")
				probe := &trustStoreProbe{}
				switch scenario {
				case "ordinary":
					job.Kind = "source.sync"
				case "changed":
					source.Revision++
				case "foreign tenant":
					source.TenantID = "00000000-0000-7000-8000-000000000002"
				case "malformed":
					job.Payload = []byte(`{}`)
				case "store failure":
					probe.err = sentinel
				case "read failure":
					probe.readErr = sentinel
				}
				probe.job = job
				sources := federation.NewMemoryStore()
				if _, err = sources.CreateSource(t.Context(), source); err != nil {
					t.Fatal(err)
				}
				store := memoryTrustJobs{Store: probe, sources: sources}
				evidence := audit.Record{ID: "forwarded-evidence"}
				var got jobs.Job
				if operation == "enqueue" {
					got, _, err = store.Enqueue(t.Context(), job, evidence)
				} else {
					got, err = store.Complete(t.Context(), job.TenantID, job.ID, "owned", time.Time{}, []byte(`{"private":true}`), evidence)
				}
				calls := probe.enqueues + probe.completions
				wantDelegation := scenario == "current" || scenario == "ordinary" || scenario == "store failure" || operation == "enqueue" && scenario == "read failure"
				if wantDelegation {
					if calls != 1 || len(probe.evidence) != 1 || probe.evidence[0].ID != evidence.ID || scenario != "store failure" && (err != nil || got.ID != job.ID || got.TenantID != job.TenantID) {
						t.Fatalf("delegation calls=%d result=%+v err=%v", calls, got, err)
					}
					if operation == "complete" && (string(probe.result) != `{"private":true}` || got.LeaseOwner != "owned") {
						t.Fatal("completion changed exact result bytes or lease binding")
					}
				} else if err == nil || calls != 0 {
					t.Fatalf("invalid target crossed the store fence: calls=%d err=%v", calls, err)
				}
				if (scenario == "store failure" || operation == "complete" && scenario == "read failure") && !errors.Is(err, sentinel) {
					t.Fatal("store error was lost", err)
				}
			})
		}
	}
}

func TestTrustHandlerRefusesMalformedWork(t *testing.T) {
	for _, payload := range []json.RawMessage{[]byte(`{}`), nil} {
		progress := 0
		handler := trustJobHandler(nil, slog.New(slog.DiscardHandler))
		_, err := handler(t.Context(), jobs.Job{Kind: jobs.TrustKind, PayloadVersion: 1, Payload: payload}, func(int) error { progress++; return nil })
		var classified *jobs.HandlerError
		if !errors.As(err, &classified) || classified.Class != jobs.FailureInvalidInput || progress != 0 {
			t.Fatalf("malformed work was not refused before progress: %v", err)
		}
	}
}

func TestTrustVerifierActivationAndPolicySelection(t *testing.T) {
	for _, scenario := range []string{"enabled", "disabled", "invalid configuration"} {
		t.Run(scenario, func(t *testing.T) {
			c, bundle, _ := trustRuntimeFixture(t)
			if scenario == "disabled" {
				c.TrustVerification.Enabled = false
			} else if scenario == "invalid configuration" {
				c.TrustVerification.Timeout.Duration = -time.Second
			}
			outbound := &trustScript{bundle: bundle, digest: c.Federation.PolicyDigest}
			verifier, err := newTrustVerifier(c, outbound, nil)
			if outbound.calls != 0 {
				t.Fatal("startup contacted an evidence endpoint")
			}
			if scenario == "invalid configuration" {
				if err == nil {
					t.Fatal("invalid startup budget was accepted")
				}
				return
			}
			if err != nil || verifier.Enabled() != (scenario == "enabled") {
				t.Fatal("activation differs from configuration", err)
			}
			digest, authority, available := verifier.Policy("owned")
			if available != (scenario == "enabled") || available && (digest != c.TrustVerification.PolicyDigest(c.Federation.PolicyDigest) || authority == "") {
				t.Fatal("authority policy differs from startup selection")
			}
			if _, _, available = verifier.Policy("unconfigured"); available {
				t.Fatal("unknown authority policy was accepted")
			}
		})
	}
}

func TestTrustUnsupportedFrameworkMakesNoEvidenceRequests(t *testing.T) {
	c, b, now := trustRuntimeFixture(t)
	var entry map[string]any
	if err := json.Unmarshal(b.Entry, &entry); err != nil {
		t.Fatal(err)
	}
	manifest, ok := entry["trustManifest"].(map[string]any)
	if !ok {
		t.Fatal("native fixture lacks Trust Manifest")
	}
	manifest["trustSchema"] = map[string]any{"identifier": "https://framework.example/unsupported", "version": "1"}
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	transport := &trustScript{bundle: b, digest: c.Federation.PolicyDigest}
	v, err := newTrustVerifier(c, transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := identity.WithActor(t.Context(), identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001"})
	_, assessment, err := v.Assess(ctx, jobs.TrustPayload{PolicyName: "owned", Entry: raw}, func(context.Context) error { return nil }, func() time.Time { return now })
	if err != nil || transport.calls != 0 || assessment.CurrentRegistration || assessment.Target.State != "unsupported" || assessment.Target.Code != "trust_framework_unsupported" {
		t.Fatalf("unsupported framework: %+v calls=%d err=%v", assessment, transport.calls, err)
	}
}

func TestTrustHandlerRequiresWorkerAuthority(t *testing.T) {
	c, bundle, _ := trustRuntimeFixture(t)
	p := jobs.TrustPayload{SourceID: "00000000-0000-7000-8000-000000000010", SourceRevision: 1, SourcePolicyDigest: c.Federation.PolicyDigest, PolicyName: "owned", PolicyDigest: c.TrustVerification.PolicyDigest(c.Federation.PolicyDigest), Identifier: "urn:air:trust-v2.ans.test:agents:trust-v2", Version: "1.0.0", VersionPresent: true, MediaType: "application/mcp-server-card+json", Delivery: "url", ExactEntryDigest: audit.DigestBytes(bundle.Entry), Entry: bundle.Entry}
	payload, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	progress := 0
	handler := trustJobHandler(nil, slog.New(slog.DiscardHandler))
	_, err = handler(t.Context(), jobs.Job{TenantID: "00000000-0000-7000-8000-000000000001", Kind: jobs.TrustKind, PayloadVersion: 1, Payload: payload}, func(int) error { progress++; return nil })
	if catalog.ErrorCodeOf(err) != catalog.CodeForbidden || progress != 0 {
		t.Fatal("unclaimed handler executed work", err)
	}
}

func TestTrustExecutionFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		code catalog.ErrorCode
		want jobs.FailureClass
	}{
		{catalog.CodeForbidden, jobs.FailurePolicyDenied},
		{catalog.CodeNotFound, jobs.FailurePolicyDenied},
		{catalog.CodePreconditionFailed, jobs.FailurePolicyDenied},
		{catalog.CodeDependencyUnavailable, jobs.FailureDependency},
		{catalog.CodeInvalidArgument, jobs.FailureInvalidInput},
		{catalog.CodeInternal, jobs.FailureInternal},
	} {
		t.Run(string(tc.code), func(t *testing.T) {
			cause := catalog.E(tc.code, "private failure with secret")
			var got *jobs.HandlerError
			if !errors.As(trustExecutionFailure(cause), &got) || got.Class != tc.want || !errors.Is(got.Cause, cause) || strings.Contains(got.Safe, "secret") {
				t.Fatalf("wrong classification or unsafe text: %+v", got)
			}
		})
	}
	for _, cause := range []error{context.Canceled, &jobs.HandlerError{Class: jobs.FailureTransientUpstream, Safe: "Safe failure."}} {
		if !errors.Is(trustExecutionFailure(cause), cause) {
			t.Fatal("existing cancellation or retry classification was replaced")
		}
	}
}

func TestTrustOutboundRequiresCurrentGuard(t *testing.T) {
	for _, scenario := range []string{"missing", "refused", "allowed"} {
		t.Run(scenario, func(t *testing.T) {
			outbound := &trustScript{}
			ctx := t.Context()
			guardCalls := 0
			if scenario != "missing" {
				ctx = context.WithValue(ctx, trustGuardKey{}, func(context.Context) error {
					guardCalls++
					if scenario == "refused" {
						return context.Canceled
					}
					return nil
				})
			}
			_, err := (trustOutbound{outbound}).Fetch(ctx, fetcher.Request{URL: nativeTrustOrigin + "/receipt"})
			if scenario == "allowed" {
				if err != nil || outbound.calls != 1 || guardCalls != 1 {
					t.Fatal("current guard did not permit exactly one request", err)
				}
			} else if err == nil || outbound.calls != 0 {
				t.Fatal("missing or refused guard contacted evidence", err)
			}
		})
	}
}

type trustCredentialResolver func(context.Context, fetcher.CredentialRequest) (fetcher.ResolvedCredential, error)

func (f trustCredentialResolver) Resolve(ctx context.Context, request fetcher.CredentialRequest) (fetcher.ResolvedCredential, error) {
	return f(ctx, request)
}

func TestTrustSourceCredentialGeneration(t *testing.T) {
	for _, scenario := range []string{"anonymous", "current", "missing resolver", "malformed endpoint", "rotated", "wrong origin", "unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			source := federation.Source{TenantID: "00000000-0000-7000-8000-000000000001", Endpoint: "https://peer.example/search", CredentialBinding: "owned-peer", CredentialRevision: "generation-one"}
			calls := 0
			v := &trustVerifier{credentials: trustCredentialResolver(func(ctx context.Context, request fetcher.CredentialRequest) (fetcher.ResolvedCredential, error) {
				calls++
				if ctx.Err() != nil || request.TenantID != source.TenantID || request.Binding != source.CredentialBinding || request.Origin != "https://peer.example:443" {
					t.Fatalf("credential request lost scope: %+v", request)
				}
				value := fetcher.ResolvedCredential{Revision: "generation-one", Origin: request.Origin}
				switch scenario {
				case "rotated":
					value.Revision = "generation-two"
				case "wrong origin":
					value.Origin = nativeTrustOrigin
				case "unavailable":
					return value, errors.New("private credential failure")
				}
				return value, nil
			})}
			if scenario == "anonymous" {
				source.CredentialBinding = ""
			}
			if scenario == "missing resolver" {
				v.credentials = nil
			}
			if scenario == "malformed endpoint" {
				source.Endpoint = "https://peer.example/%invalid"
			}
			err := v.ValidateSource(t.Context(), source)
			wantValid := scenario == "anonymous" || scenario == "current"
			wantError := fetcher.ErrCredentialChanged
			if scenario == "malformed endpoint" {
				wantError = fetcher.ErrPolicyDenied
			}
			if (err == nil) != wantValid || !wantValid && !errors.Is(err, wantError) {
				t.Fatalf("generation check: %v", err)
			}
			wantCalls := 1
			if scenario == "anonymous" || scenario == "missing resolver" || scenario == "malformed endpoint" {
				wantCalls = 0
			}
			if calls != wantCalls {
				t.Fatalf("resolver calls=%d, want %d", calls, wantCalls)
			}
		})
	}
}

// The actual file resolver requires the same canonical origin as the router.
// Files are inert synthetic credentials; this test makes no network request.
func TestTrustSourceUsesCanonicalFileCredentialOrigin(t *testing.T) {
	directory := t.TempDir()
	metadata := `{"schemaVersion":1,"tenantId":"00000000-0000-7000-8000-000000000001","revision":"generation-one","origin":"https://peer.example","type":"bearer","secretFile":"token"}`
	for name, data := range map[string]string{"owned-peer.json": metadata, "token": "synthetic-private-token"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	resolver, err := fetcher.NewFileCredentialResolver(directory)
	if err != nil {
		t.Fatal(err)
	}
	v := &trustVerifier{credentials: resolver}
	for _, endpoint := range []string{"https://peer.example/search", "https://peer.example:443/search"} {
		t.Run(endpoint, func(t *testing.T) {
			source := federation.Source{TenantID: "00000000-0000-7000-8000-000000000001", Endpoint: endpoint, CredentialBinding: "owned-peer", CredentialRevision: "generation-one"}
			if err := v.ValidateSource(t.Context(), source); err != nil {
				t.Fatal("current origin-bound credential refused", err)
			}
			source.CredentialRevision = "obsolete"
			if err := v.ValidateSource(t.Context(), source); !errors.Is(err, fetcher.ErrCredentialChanged) {
				t.Fatal("stale credential admitted", err)
			}
			source.CredentialRevision = "generation-one"
			source.Endpoint = "https://other.example/search"
			if err := v.ValidateSource(t.Context(), source); !errors.Is(err, fetcher.ErrCredentialChanged) {
				t.Fatal("cross-origin credential admitted", err)
			}
		})
	}
}

type trustScript struct {
	bundle anstrust.Bundle
	calls  int
	status int
	fault  func(context.Context, fetcher.Request) error
	digest string
}

func (s *trustScript) PolicyDigest() string { return s.digest }
func (s *trustScript) Fetch(ctx context.Context, r fetcher.Request) (fetcher.Result, error) {
	s.calls++
	if s.fault != nil {
		if err := s.fault(ctx, r); err != nil {
			return fetcher.Result{}, err
		}
	}
	if r.CredentialBinding != "" || r.Headers != nil {
		panic("evidence inherited source credentials")
	}
	if s.status != 0 {
		return fetcher.Result{Status: s.status}, nil
	}
	var body []byte
	media := "application/scitt-receipt+cose"
	switch {
	case strings.HasSuffix(r.URL, "/receipt"):
		body = s.bundle.Receipt
	case strings.HasSuffix(r.URL, "/checkpoint"):
		body = s.bundle.Checkpoint
		media = "text/plain"
	case strings.Contains(r.URL, "/history?"):
		body = s.bundle.CheckpointHistory
		media = "application/json"
	case strings.HasSuffix(r.URL, "/status-token"):
		body = s.bundle.StatusToken
		media = "application/ans-status-token+cbor"
	default:
		panic("unexpected evidence destination")
	}
	return fetcher.Result{Status: 200, FinalURL: r.URL, MediaType: media, Body: body, PolicyDigest: s.digest}, nil
}
func trustRuntimeFixture(t *testing.T) (config.Config, anstrust.Bundle, time.Time) {
	t.Helper()
	c := localRuntimeConfig()
	c.HTTP.RequestTimeout.Duration = 20 * time.Second
	c.HTTP.WriteTimeout.Duration = 26 * time.Second
	c.HTTP.ShutdownTimeout.Duration = 26 * time.Second
	c.Federation.PolicyDigest = "sha256:" + strings.Repeat("a", 64)
	c.Federation.Policy = config.FederationPolicy{SchemaVersion: 1, PolicyID: "owned", Revision: 1, Public: config.FederationPublicPolicy{Mode: "allowlist", Origins: []string{nativeTrustOrigin}}}
	c.TrustVerification.Enabled = true
	ra, _ := filepath.Abs("../../standards/anstrust/testdata/native/ra.pem")
	tl, _ := filepath.Abs("../../standards/anstrust/testdata/native/tl.pem")
	c.TrustVerification.Policies = []config.TrustPolicy{{Name: "owned", Profile: anstrust.Profile, RegistrationAuthority: "ans-ra-local", LogOrigin: "owned-ans", EvidenceOrigin: nativeTrustOrigin, PublisherDomains: []string{"trust-v1.ans.test", "trust-v2.ans.test"}, RegistrationKeys: []config.TrustKey{{ID: "ans-ra-signer", File: ra}}, LogKeyFiles: []string{tl}}}
	if err := c.TrustVerification.Validate(c.Federation.Policy, config.RoleAPI, time.Minute); err != nil {
		t.Fatal(err)
	}
	// LoadFor is the owning key-loading boundary; write a local public policy file.
	dir := t.TempDir()
	policy := filepath.Join(dir, "egress.json")
	raw, marshalErr := json.Marshal(c.Federation.Policy)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if err := os.WriteFile(policy, raw, 0600); err != nil {
		t.Fatal(err)
	}
	c.Federation.PolicyFile = policy
	path := filepath.Join(dir, "config.json")
	raw, marshalErr = json.Marshal(c)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadFor(path, config.RoleAPI)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile("../../standards/anstrust/testdata/native/v2.json")
	if err != nil {
		t.Fatal(err)
	}
	b, err := anstrust.DecodeBundle(raw)
	if err != nil {
		t.Fatal(err)
	}
	return loaded, b, time.Date(2026, 10, 9, 14, 31, 24, 0, time.UTC)
}
func TestTrustAcquisitionClassificationAndFinalTime(t *testing.T) {
	c, b, now := trustRuntimeFixture(t)
	ctx := identity.WithActor(t.Context(), identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001"})
	for _, name := range []string{"valid", "signature", "target log mismatch", "404", "410", "429", "503", "401", "403", "400", "policy denied", "timeout", "cancelled", "guard changed", "disabled", "bundle limit"} {
		t.Run(name, func(t *testing.T) {
			selected := c
			script := &trustScript{bundle: b, digest: c.Federation.PolicyDigest}
			p := jobs.TrustPayload{PolicyName: "owned", Entry: b.Entry}
			wantClass := jobs.FailureClass("")
			guards := 0
			switch name {
			case "signature":
				script.bundle.Receipt = bytes.Clone(b.Receipt)
				script.bundle.Receipt[len(b.Receipt)-1] ^= 1
			case "target log mismatch":
				p.Entry = bytes.ReplaceAll(b.Entry, []byte(`"logId":"01a12113-6c2f-794a-812e-36a4ac1f43f0"`), []byte(`"logId":"different"`))
			case "404":
				script.status = 404
			case "410":
				script.status = 410
			case "429":
				script.status = 429
				wantClass = jobs.FailureRateLimited
			case "503":
				script.status = 503
				wantClass = jobs.FailureTransientUpstream
			case "401":
				script.status = 401
				wantClass = jobs.FailureAuthentication
			case "403":
				script.status = 403
				wantClass = jobs.FailureAuthentication
			case "400":
				script.status = 400
				wantClass = jobs.FailurePermanentUpstream
			case "policy denied":
				script.fault = func(context.Context, fetcher.Request) error { return fetcher.ErrPolicyDenied }
				wantClass = jobs.FailurePolicyDenied
			case "timeout":
				script.fault = func(context.Context, fetcher.Request) error { return context.DeadlineExceeded }
				wantClass = jobs.FailureTransientUpstream
			case "cancelled":
				script.fault = func(context.Context, fetcher.Request) error { return context.Canceled }
			case "guard changed":
				wantClass = jobs.FailurePolicyDenied
			case "disabled":
				selected.TrustVerification.Enabled = false
				wantClass = jobs.FailurePolicyDenied
			case "bundle limit":
				selected.TrustVerification.Limits.BundleBytes = 1024
				wantClass = jobs.FailureInvalidInput
			}
			v, err := newTrustVerifier(selected, script, nil)
			if err != nil {
				t.Fatal(err)
			}
			got, result, err := v.Assess(ctx, p, func(context.Context) error {
				guards++
				if name == "guard changed" {
					return catalog.E(catalog.CodePreconditionFailed, "changed")
				}
				return nil
			}, func() time.Time { return now })
			if name == "cancelled" {
				if !errors.Is(err, context.Canceled) || script.calls != 1 {
					t.Fatal("cancellation was converted into an assessment or retry", err)
				}
				return
			}
			if wantClass != "" {
				var failure *jobs.HandlerError
				if !errors.As(err, &failure) || failure.Class != wantClass {
					t.Fatalf("classification %v, want %s", err, wantClass)
				}
				return
			}
			if err != nil || string(got.Entry) != string(p.Entry) || !result.CheckedAt.Equal(now) {
				t.Fatalf("assessment=%+v err=%v", result, err)
			}
			if name == "valid" {
				if !result.CurrentRegistration || script.calls != 3 || guards != 3 {
					t.Fatalf("positive assessment=%+v calls=%d guards=%d", result, script.calls, guards)
				}
			} else if result.CurrentRegistration {
				t.Fatal("negative evidence became valid")
			}
			if name == "signature" && script.calls != 1 {
				t.Fatal("invalid receipt fetched further evidence")
			}
			if name == "target log mismatch" && result.Target.Code != "target_mismatch" {
				t.Fatalf("latest event transplant accepted: %+v", result.Target)
			}
		})
	}
}
