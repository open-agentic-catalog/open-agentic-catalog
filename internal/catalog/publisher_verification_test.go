package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

type publisherTXTFunc func(context.Context, string) ([]string, error)

func (f publisherTXTFunc) LookupTXT(ctx context.Context, name string) ([]string, error) {
	return f(ctx, name)
}

type failedNonce struct{}

func (failedNonce) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

type verificationIDs struct {
	mu  sync.Mutex
	ids administrationIDs
}

func (g *verificationIDs) New(at time.Time) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.ids.New(at)
}

type verificationAdmission struct {
	identity.PermissionStore
	revoked atomic.Bool
	active  atomic.Bool
}

func (a *verificationAdmission) CheckPermissions(_ context.Context, _ identity.Actor, checks []identity.PermissionCheck) (identity.PermissionDecision, error) {
	allowed := make([]bool, len(checks))
	for i := range allowed {
		allowed[i] = !a.revoked.Load()
	}
	return identity.PermissionDecision{Revision: 1, Allowed: allowed}, nil
}
func (a *verificationAdmission) WithPermissionAdmission(ctx context.Context, _ identity.Actor, _ []identity.PermissionCheck, work func(context.Context) error) error {
	if a.revoked.Load() {
		return identity.E(identity.CodeForbidden, "controlled revoked grant")
	}
	a.active.Store(true)
	defer a.active.Store(false)
	return work(ctx)
}

func publisherVerificationFixture(t *testing.T, mode string) (*Service, *MemoryRepository, *guardrailClock, Publisher, context.Context) {
	t.Helper()
	repo := NewMemoryRepository()
	clock := &guardrailClock{now: time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)}
	policy := DefaultPublisherDomainPolicy()
	policy.Mode = mode
	s, err := NewService(Options{Repository: repo, Clock: clock, IDs: &verificationIDs{}, Validator: administrationValidator{}, CursorKey: []byte("01234567890123456789012345678901"), PublisherDomainPolicy: policy, PublisherTXT: publisherTXTFunc(func(context.Context, string) ([]string, error) { return nil, nil })})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "admin", MembershipID: "00000000-0000-7000-8000-000000000092", RealmID: "00000000-0000-7000-8000-000000000091", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}})
	p, err := s.CreatePublisher(ctx, CreatePublisherInput{DisplayName: "DNS test", Domains: []string{"catalog.example.com", "other.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	return s, repo, clock, p, ctx
}

func issuePublisherChallenge(t *testing.T, s *Service, ctx context.Context, p Publisher) PublisherDomainChallenge {
	t.Helper()
	status, err := s.GetPublisherDomainVerification(ctx, p.ID, "catalog.example.com")
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := s.IssuePublisherDomainChallenge(ctx, p.ID, "catalog.example.com", "Verify owned fixture domain", status.Version)
	if err != nil {
		t.Fatal(err)
	}
	return challenge
}

func TestPublisherVerificationLifecycleAndApproval(t *testing.T) {
	s, repo, clock, p, ctx := publisherVerificationFixture(t, DomainVerificationRequired)
	if _, err := s.ApprovePublisherPublicationDomain(ctx, p.ID, "catalog.example.com", "Require current proof", p.Revision); ErrorCodeOf(err) != CodePreconditionFailed {
		t.Fatalf("approval without proof: %v", err)
	}
	beforeOutbox := len(repo.outbox)
	challenge := issuePublisherChallenge(t, s, ctx, p)
	if challenge.Name != DomainChallengePrefix+"catalog.example.com." || len(strings.TrimPrefix(challenge.Value, DomainTXTValuePrefix)) != 43 || challenge.ChallengeID == "" {
		t.Fatal("invalid challenge instructions")
	}
	lookups := 0
	s.publisherTXT = publisherTXTFunc(func(_ context.Context, name string) ([]string, error) {
		lookups++
		if name != challenge.Name {
			t.Fatal("DNS name changed")
		}
		if !repo.mu.TryLock() {
			t.Fatal("DNS ran under repository lock")
		}
		repo.mu.Unlock()
		return []string{challenge.Value}, nil
	})
	status, err := s.CheckPublisherDomain(ctx, p.ID, "catalog.example.com", "Check exact TXT", challenge.Status.Version)
	if err != nil || status.State != "verified" || status.LastCheckOutcome != "matched" || lookups != 1 {
		t.Fatalf("check: %+v %v", status, err)
	}
	if len(repo.outbox) != beforeOutbox {
		t.Fatal("verification manufactured public notifications")
	}
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), challenge.Value) || strings.Contains(string(raw), strings.TrimPrefix(challenge.Value, DomainTXTValuePrefix)) {
		t.Fatal("status disclosed nonce")
	}
	p, err = s.ApprovePublisherPublicationDomain(ctx, p.ID, "catalog.example.com", "Approve fresh proof", p.Revision)
	if err != nil || lookups != 1 {
		t.Fatalf("approval: %v", err)
	}
	facts, ok := repo.audit[len(repo.audit)-1].Details["facts"].(map[string]any)
	if !ok || facts["verificationId"] != status.Version.ID {
		t.Fatal("approval missing exact proof binding")
	}
	if _, err = s.CheckPublisherDomain(ctx, p.ID, "catalog.example.com", "Do not reuse consumed challenge", status.Version); ErrorCodeOf(err) != CodePreconditionFailed || lookups != 1 {
		t.Fatalf("consumed challenge: %v", err)
	}
	previousVerified, previousExpiry := status.VerifiedAt, status.ExpiresAt
	clock.now = clock.now.Add(time.Minute)
	renewed := issuePublisherChallenge(t, s, ctx, p)
	if renewed.ChallengeID == challenge.ChallengeID || renewed.Value == challenge.Value {
		t.Fatal("renewal reused nonce or challenge identity")
	}
	s.publisherTXT = publisherTXTFunc(func(context.Context, string) ([]string, error) { return []string{challenge.Value}, nil })
	status, err = s.CheckPublisherDomain(ctx, p.ID, "catalog.example.com", "Negative renewal preserves proof", renewed.Status.Version)
	if err != nil || status.LastCheckOutcome != "mismatch" || status.VerifiedAt != previousVerified || status.ExpiresAt != previousExpiry {
		t.Fatalf("negative renewal changed proof: %+v %v", status, err)
	}
	s.publisherDomainPolicy.ProofMaxAge *= 2
	read, err := s.GetPublisherDomainVerification(ctx, p.ID, "catalog.example.com")
	if err != nil || read.ExpiresAt != previousExpiry {
		t.Fatal("configuration extended recorded proof")
	}
	s.publisherDomainPolicy.ProofMaxAge = time.Hour
	clock.now = clock.now.Add(2 * time.Hour)
	read, err = s.GetPublisherDomainVerification(ctx, p.ID, "catalog.example.com")
	if err != nil || read.State != "expired" {
		t.Fatal("shorter current policy ignored")
	}
	status, err = s.ClearPublisherDomainVerification(ctx, p.ID, "catalog.example.com", "Clear evidence only", status.Version)
	if err != nil || status.State != "not_verified" {
		t.Fatal(err)
	}
	stored, err := repo.GetPublisher(ctx, p.TenantID, p.ID)
	if err != nil || !contains(stored.ApprovedPublicationDomains, "catalog.example.com") {
		t.Fatal("clearing evidence revoked approval")
	}
}

func TestPublisherVerificationNegativeLookupsAndBounds(t *testing.T) {
	for _, name := range []string{"empty", "mismatch", "case", "split", "error", "count", "bytes", "oversized_record", "timeout", "cancel", "expiry", "backward"} {
		t.Run(name, func(t *testing.T) {
			s, repo, clock, p, ctx := publisherVerificationFixture(t, DomainVerificationOptional)
			challenge := issuePublisherChallenge(t, s, ctx, p)
			before := len(repo.audit)
			expected := "mismatch"
			s.publisherTXT = publisherTXTFunc(func(call context.Context, _ string) ([]string, error) {
				switch name {
				case "empty":
					expected = "not_found"
					return nil, nil
				case "mismatch":
					return []string{" " + challenge.Value}, nil
				case "case":
					return []string{strings.ToUpper(challenge.Value)}, nil
				case "split":
					return []string{DomainTXTValuePrefix, strings.TrimPrefix(challenge.Value, DomainTXTValuePrefix)}, nil
				case "error":
					expected = "unavailable"
					return []string{challenge.Value}, errors.New("PRIVATE_DNS_REPLY")
				case "count":
					expected = "response_limit"
					return append([]string{challenge.Value}, make([]string, 64)...), nil
				case "bytes":
					expected = "response_limit"
					return []string{challenge.Value, strings.Repeat("x", 8192)}, nil
				case "oversized_record":
					expected = "response_limit"
					return []string{challenge.Value, strings.Repeat("x", 8193)}, nil
				case "timeout":
					expected = "unavailable"
					<-call.Done()
					return []string{challenge.Value}, nil
				case "expiry":
					clock.now = challenge.ExpiresAt
					return []string{challenge.Value}, nil
				case "backward":
					clock.now = clock.now.Add(-time.Second)
					return []string{challenge.Value}, nil
				case "cancel":
					return nil, context.Canceled
				}
				return nil, nil
			})
			if name == "timeout" {
				s.publisherDomainPolicy.LookupTimeout = 100 * time.Millisecond
			}
			status, err := s.CheckPublisherDomain(ctx, p.ID, "catalog.example.com", "Observe controlled DNS result", challenge.Status.Version)
			if name == "expiry" || name == "backward" {
				if ErrorCodeOf(err) != CodePreconditionFailed || len(repo.audit) != before {
					t.Fatal("invalid completion persisted", err)
				}
				return
			}
			if name == "cancel" {
				expected = "unavailable"
			}
			if err != nil || status.LastCheckOutcome != expected || status.State != "not_verified" || len(repo.audit) != before+1 {
				t.Fatalf("outcome: %+v %v", status, err)
			}
			raw, _ := json.Marshal(repo.audit[before:])
			if strings.Contains(string(raw), "PRIVATE_DNS_REPLY") || strings.Contains(string(raw), challenge.Value) {
				t.Fatal("DNS evidence leaked")
			}
		})
	}
}

func TestPublisherVerificationRefusalsAndRemoval(t *testing.T) {
	s, repo, _, p, ctx := publisherVerificationFixture(t, DomainVerificationOptional)
	for _, domain := range []string{"127.0.0.1", "*.example.com", "bad..example.com", strings.Repeat("a", 64) + ".com"} {
		if _, err := s.GetPublisherDomainVerification(ctx, p.ID, domain); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatalf("domain %q: %v", domain, err)
		}
	}
	status, _ := s.GetPublisherDomainVerification(ctx, p.ID, "catalog.example.com")
	before := len(repo.audit)
	s.publisherDomainNonce = failedNonce{}
	if _, err := s.IssuePublisherDomainChallenge(ctx, p.ID, "catalog.example.com", "Entropy failure", status.Version); ErrorCodeOf(err) != CodeInternal || len(repo.audit) != before {
		t.Fatal("entropy failure changed state", err)
	}
	s.publisherDomainNonce = publisherDomainNonce(nil)
	challenge := issuePublisherChallenge(t, s, ctx, p)
	if _, err := s.IssuePublisherDomainChallenge(ctx, p.ID, "catalog.example.com", "Reject stale state", status.Version); ErrorCodeOf(err) != CodePreconditionFailed {
		t.Fatal("stale issue accepted", err)
	}
	reader := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: p.TenantID, Subject: "reader", MembershipID: "00000000-0000-7000-8000-000000000093", RealmID: "00000000-0000-7000-8000-000000000091", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleReader}})
	if _, err := s.GetPublisherDomainChallenge(reader, p.ID, "catalog.example.com"); ErrorCodeOf(err) != CodeForbidden {
		t.Fatal("reader saw challenge", err)
	}
	if _, err := s.ClearPublisherDomainVerification(ctx, p.ID, "catalog.example.com", "", challenge.Status.Version); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal("empty reason accepted", err)
	}
	domains := make([]string, 1, 2)
	domains[0] = "other.example.com"
	p, err := s.UpdatePublisher(ctx, p.ID, PatchPublisherInput{Domains: &domains, Reason: "Remove unapproved domain"}, p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CheckPublisherDomain(ctx, p.ID, "catalog.example.com", "Removed domain", challenge.Status.Version); ErrorCodeOf(err) != CodeNotFound {
		t.Fatal("removed domain checked", err)
	}
	domains = append(domains, "catalog.example.com")
	p, err = s.UpdatePublisher(ctx, p.ID, PatchPublisherInput{Domains: &domains, Reason: "Readd declaration"}, p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	read, _ := s.GetPublisherDomainVerification(ctx, p.ID, "catalog.example.com")
	if read.Version.ID != "" || read.Version.Revision != 0 {
		t.Fatal("domain removal retained active verification")
	}
	if _, err = s.IssuePublisherDomainChallenge(ctx, p.ID, "catalog.example.com", "Old challenge cannot return", challenge.Status.Version); ErrorCodeOf(err) != CodePreconditionFailed {
		t.Fatal("old verification identity accepted", err)
	}
	s.publisherDomainPolicy.Mode = DomainVerificationDisabled
	if _, err = s.IssuePublisherDomainChallenge(ctx, p.ID, "catalog.example.com", "Disabled", read.Version); ErrorCodeOf(err) != CodeInvalidTransition {
		t.Fatal("disabled mode issued challenge", err)
	}
	if _, err = s.ApprovePublisherPublicationDomain(ctx, p.ID, "catalog.example.com", "Disabled preserves approval workflow", p.Revision); err != nil {
		t.Fatal(err)
	}
}

func TestPublisherVerificationLookupFencesAndCapacity(t *testing.T) {
	for _, scenario := range []string{"revoked", "reissued", "cleared", "removed", "readded", "cancelled", "capacity"} {
		t.Run(scenario, func(t *testing.T) {
			s, repo, _, p, ctx := publisherVerificationFixture(t, DomainVerificationOptional)
			challenge := issuePublisherChallenge(t, s, ctx, p)
			before := len(repo.audit)
			actor, _ := identity.FromContext(ctx)
			admission := &verificationAdmission{}
			bound := identity.WithActor(ctx, identity.BindPermissionStore(actor, admission))
			if scenario == "capacity" {
				started := make(chan struct{}, 4)
				release := make(chan struct{})
				result := make(chan error, 4)
				s.publisherTXT = publisherTXTFunc(func(context.Context, string) ([]string, error) {
					started <- struct{}{}
					<-release
					return []string{challenge.Value}, nil
				})
				for range 4 {
					go func() {
						_, err := s.CheckPublisherDomain(ctx, p.ID, "catalog.example.com", "Concurrent controlled check", challenge.Status.Version)
						result <- err
					}()
				}
				for range 4 {
					select {
					case <-started:
					case <-time.After(2 * time.Second):
						close(release)
						t.Fatal("four lookups did not start")
					}
				}
				if _, err := s.CheckPublisherDomain(ctx, p.ID, "catalog.example.com", "Reject excess lookup", challenge.Status.Version); ErrorCodeOf(err) != CodeWorkLimitExceeded {
					close(release)
					t.Fatal("lookup limit not enforced", err)
				}
				close(release)
				success := 0
				for range 4 {
					err := <-result
					if err == nil {
						success++
					} else if ErrorCodeOf(err) != CodePreconditionFailed {
						t.Fatal(err)
					}
				}
				if success != 1 {
					t.Fatalf("concurrent checks committed %d results", success)
				}
				return
			}
			call, cancel := context.WithCancel(bound)
			defer cancel()
			s.publisherTXT = publisherTXTFunc(func(context.Context, string) ([]string, error) {
				if admission.active.Load() {
					t.Error("DNS ran under permission admission")
				}
				switch scenario {
				case "revoked":
					admission.revoked.Store(true)
				case "cancelled":
					cancel()
				case "reissued":
					issuePublisherChallenge(t, s, ctx, p)
				case "cleared":
					if _, err := s.ClearPublisherDomainVerification(ctx, p.ID, "catalog.example.com", "Clear during DNS", challenge.Status.Version); err != nil {
						t.Error(err)
					}
				case "removed", "readded":
					domains := []string{"other.example.com"}
					changed, err := s.UpdatePublisher(ctx, p.ID, PatchPublisherInput{Domains: &domains, Reason: "Remove during DNS"}, p.Revision)
					if err != nil {
						t.Error(err)
						break
					}
					if scenario == "readded" {
						domains = append(domains, "catalog.example.com")
						changed, err = s.UpdatePublisher(ctx, p.ID, PatchPublisherInput{Domains: &domains, Reason: "Readd during DNS"}, changed.Revision)
						if err != nil {
							t.Error(err)
						}
						replacement := issuePublisherChallenge(t, s, ctx, changed)
						if replacement.Status.Version.ID == challenge.Status.Version.ID || replacement.Status.Version.Revision != challenge.Status.Version.Revision {
							t.Error("replacement did not isolate the reused numeric revision")
						}
					}
				}
				return []string{challenge.Value}, nil
			})
			_, err := s.CheckPublisherDomain(call, p.ID, "catalog.example.com", "Fence final mutation", challenge.Status.Version)
			switch scenario {
			case "revoked":
				if ErrorCodeOf(err) != CodeForbidden || len(repo.audit) != before {
					t.Fatal("revoked check committed", err)
				}
			case "cancelled":
				if !errors.Is(err, context.Canceled) || len(repo.audit) != before {
					t.Fatal("cancelled check committed", err)
				}
			case "reissued":
				if ErrorCodeOf(err) != CodePreconditionFailed || len(repo.audit) != before+1 {
					t.Fatal("replaced challenge committed", err)
				}
			case "cleared":
				if ErrorCodeOf(err) != CodePreconditionFailed || len(repo.audit) != before+1 {
					t.Fatal("cleared challenge committed", err)
				}
			case "removed":
				if ErrorCodeOf(err) != CodeNotFound || len(repo.audit) != before+1 {
					t.Fatal("removed challenge committed", err)
				}
			case "readded":
				if ErrorCodeOf(err) != CodePreconditionFailed || len(repo.audit) != before+3 {
					t.Fatal("removed challenge affected replacement", err)
				}
			}
		})
	}
}

func TestPublisherVerificationInstructionsAndDisabledReads(t *testing.T) {
	s, _, clock, p, ctx := publisherVerificationFixture(t, DomainVerificationOptional)
	status, err := s.GetPublisherDomainVerification(ctx, p.ID, "catalog.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CheckPublisherDomain(ctx, p.ID, "catalog.example.com", "No pending challenge", status.Version); ErrorCodeOf(err) != CodePreconditionFailed {
		t.Fatal(err)
	}
	if _, err = s.ClearPublisherDomainVerification(ctx, p.ID, "catalog.example.com", "No state to clear", status.Version); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal(err)
	}
	challenge := issuePublisherChallenge(t, s, ctx, p)
	read, err := s.GetPublisherDomainChallenge(ctx, p.ID, "catalog.example.com")
	if err != nil || read.Value != challenge.Value {
		t.Fatal("management instructions missing", err)
	}
	clock.now = challenge.ExpiresAt
	read, err = s.GetPublisherDomainChallenge(ctx, p.ID, "catalog.example.com")
	if err != nil || read.Value != "" {
		t.Fatal("expired challenge disclosed as usable", err)
	}
	s.publisherDomainPolicy.Mode = DomainVerificationDisabled
	read, err = s.GetPublisherDomainChallenge(ctx, p.ID, "catalog.example.com")
	if err != nil || read.Value != "" {
		t.Fatal("disabled instructions", err)
	}
	if _, err = s.GetPublisherDomainVerification(context.Background(), p.ID, "catalog.example.com"); ErrorCodeOf(err) != CodeForbidden {
		t.Fatal("unauthenticated status", err)
	}
	if _, err = s.GetPublisherDomainVerification(ctx, "bad", "catalog.example.com"); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal("invalid publisher identifier", err)
	}
	if _, err = s.GetPublisherDomainVerification(ctx, p.ID, "absent.example.com"); ErrorCodeOf(err) != CodeNotFound {
		t.Fatal("undeclared domain", err)
	}
}

func TestPublisherVerificationReleasesSlotsAfterFailures(t *testing.T) {
	s, _, _, p, ctx := publisherVerificationFixture(t, DomainVerificationOptional)
	challenge := issuePublisherChallenge(t, s, ctx, p)
	for _, cancelled := range []bool{false, true} {
		for range 5 { // Exceed the per-process slot count without replacing the Service.
			call, cancel := context.WithCancel(ctx)
			s.publisherTXT = publisherTXTFunc(func(context.Context, string) ([]string, error) {
				if cancelled {
					cancel()
				}
				return nil, errors.New("controlled resolver failure")
			})
			status, err := s.CheckPublisherDomain(call, p.ID, "catalog.example.com", "Release failed lookup slot", challenge.Status.Version)
			cancel()
			if cancelled {
				if !errors.Is(err, context.Canceled) {
					t.Fatal("cancellation was not returned", err)
				}
			} else {
				if err != nil || status.LastCheckOutcome != "unavailable" {
					t.Fatal("failed check did not complete", err)
				}
				challenge.Status.Version = status.Version
			}
		}
	}
	s.publisherTXT = publisherTXTFunc(func(context.Context, string) ([]string, error) { return []string{challenge.Value}, nil })
	status, err := s.CheckPublisherDomain(ctx, p.ID, "catalog.example.com", "Verify after failures", challenge.Status.Version)
	if err != nil || status.State != "verified" {
		t.Fatal("failed or cancelled checks leaked a slot", err)
	}
}

func TestPublisherVerificationAcceptsExactResultLimits(t *testing.T) {
	for _, limit := range []string{"64 records", "8192 bytes"} {
		t.Run(limit, func(t *testing.T) {
			s, _, _, p, ctx := publisherVerificationFixture(t, DomainVerificationOptional)
			challenge := issuePublisherChallenge(t, s, ctx, p)
			records := make([]string, 64)
			records[63] = challenge.Value
			if limit == "8192 bytes" {
				records = []string{strings.Repeat("x", 8192-len(challenge.Value)), challenge.Value}
			}
			s.publisherTXT = publisherTXTFunc(func(context.Context, string) ([]string, error) { return records, nil })
			status, err := s.CheckPublisherDomain(ctx, p.ID, "catalog.example.com", "Accept the inclusive bound", challenge.Status.Version)
			if err != nil || status.State != "verified" || status.LastCheckOutcome != "matched" {
				t.Fatalf("valid boundary was refused: %+v %v", status, err)
			}
		})
	}
}

func TestPublisherVerificationAbsoluteDNSNameBoundary(t *testing.T) {
	s, _, _, _, ctx := publisherVerificationFixture(t, DomainVerificationOptional)
	// 221 domain characters plus the fixed 32-character prefix fill 253 characters.
	domain := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 29)
	p, err := s.CreatePublisher(ctx, CreatePublisherInput{DisplayName: "DNS name boundary", Domains: []string{domain}})
	if err != nil {
		t.Fatal(err)
	}
	status, err := s.GetPublisherDomainVerification(ctx, p.ID, domain)
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := s.IssuePublisherDomainChallenge(ctx, p.ID, strings.ToUpper(domain)+".", "Normalize the exact domain", status.Version)
	if err != nil || challenge.Name != "_open-agentic-catalog-challenge."+domain+"." || len(challenge.Name) != 254 {
		t.Fatal("absolute DNS limit or normalization changed", err)
	}
	if _, err = s.GetPublisherDomainVerification(ctx, p.ID, domain+"d"); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal("overlong prefixed name accepted", err)
	}
}

func TestPublisherVerificationApprovalUsesCurrentProofAge(t *testing.T) {
	s, repo, c, p, ctx := publisherVerificationFixture(t, DomainVerificationRequired)
	challenge := issuePublisherChallenge(t, s, ctx, p)
	s.publisherTXT = publisherTXTFunc(func(context.Context, string) ([]string, error) { return []string{challenge.Value}, nil })
	status, err := s.CheckPublisherDomain(ctx, p.ID, "catalog.example.com", "Record fixed proof expiry", challenge.Status.Version)
	if err != nil {
		t.Fatal(err)
	}
	observed := status.VerifiedAt
	beforeAudit, beforeOutbox := len(repo.audit), len(repo.outbox)
	for _, scenario := range []string{"future observation", "shortened policy", "recorded expiry"} {
		t.Run(scenario, func(t *testing.T) {
			switch scenario {
			case "future observation":
				c.now = observed.Add(-time.Microsecond)
			case "shortened policy":
				s.publisherDomainPolicy.ProofMaxAge = time.Hour
				c.now = observed.Add(time.Hour)
			case "recorded expiry":
				s.publisherDomainPolicy.ProofMaxAge = 60 * 24 * time.Hour
				c.now = status.ExpiresAt
			}
			read, err := s.GetPublisherDomainVerification(ctx, p.ID, "catalog.example.com")
			if err != nil || read.State != "expired" {
				t.Fatal("status accepted stale or future proof", err)
			}
			if _, err = s.ApprovePublisherPublicationDomain(ctx, p.ID, "catalog.example.com", "Do not approve stale proof", p.Revision); ErrorCodeOf(err) != CodePreconditionFailed {
				t.Fatal("approval accepted stale or future proof", err)
			}
			if len(repo.audit) != beforeAudit || len(repo.outbox) != beforeOutbox {
				t.Fatal("refused approval appended evidence")
			}
		})
	}
}
