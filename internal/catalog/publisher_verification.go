package catalog

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

const (
	DomainVerificationDisabled = "disabled"
	DomainVerificationOptional = "optional"
	DomainVerificationRequired = "required_for_approval"
	DomainChallengePrefix      = "_open-agentic-catalog-challenge."
	DomainTXTValuePrefix       = "open-agentic-catalog-verification="
)

// PublisherDomainPolicy is startup policy, never tenant-owned DNS configuration.
type PublisherDomainPolicy struct {
	Mode          string        `json:"mode"`
	ChallengeTTL  time.Duration `json:"challengeTTL"`
	ProofMaxAge   time.Duration `json:"proofMaxAge"`
	LookupTimeout time.Duration `json:"lookupTimeout"`
}

func DefaultPublisherDomainPolicy() PublisherDomainPolicy {
	return PublisherDomainPolicy{Mode: DomainVerificationDisabled, ChallengeTTL: 24 * time.Hour, ProofMaxAge: 30 * 24 * time.Hour, LookupTimeout: 5 * time.Second}
}

func (p PublisherDomainPolicy) Validate() error {
	if p.Mode != DomainVerificationDisabled && p.Mode != DomainVerificationOptional && p.Mode != DomainVerificationRequired || p.ChallengeTTL < 5*time.Minute || p.ChallengeTTL > 7*24*time.Hour || p.ProofMaxAge < time.Hour || p.ProofMaxAge > 365*24*time.Hour || p.LookupTimeout < 100*time.Millisecond || p.LookupTimeout > 10*time.Second {
		return E(CodeInvalidArgument, "publisher domain verification policy is invalid")
	}
	return nil
}

// PublisherTXTResolver returns complete TXT records, with DNS chunks joined by
// the resolver. The Catalog never treats their values as network destinations.
type PublisherTXTResolver interface {
	LookupTXT(context.Context, string) ([]string, error)
}

// PublisherDomainVersion binds a verification revision, or an absent record to
// the Publisher revision. Removing/readding a domain cannot reuse a state ID.
type PublisherDomainVersion struct {
	ID                string `json:"id"`
	Revision          int64  `json:"revision"`
	PublisherRevision int64  `json:"publisherRevision"`
}

// PublisherDomainVerification is bounded current evidence. Nonce is restricted
// to management instructions and must never enter serialized status or audit.
type PublisherDomainVerification struct {
	ID, TenantID, PublisherID, Domain      string
	Revision                               int64
	ChallengeID                            string
	Nonce                                  string `json:"-"`
	ChallengeIssuedAt, ChallengeExpiresAt  time.Time
	ChallengeConsumed                      bool
	VerifiedChallengeID                    string
	VerifiedAt, ProofExpiresAt             time.Time
	LastCheckAt                            time.Time
	LastCheckChallengeID, LastCheckOutcome string
	CreatedAt, UpdatedAt                   time.Time
}

func (v PublisherDomainVerification) Version(publisher Publisher) PublisherDomainVersion {
	return PublisherDomainVersion{ID: v.ID, Revision: v.Revision, PublisherRevision: publisher.Revision}
}

func (v PublisherDomainVerification) ProofFresh(at time.Time, maxAge time.Duration) bool {
	return !v.VerifiedAt.IsZero() && !at.Before(v.VerifiedAt) && at.Before(v.ProofExpiresAt) && at.Before(v.VerifiedAt.Add(maxAge))
}

// CheckPublisherDomainVersion is shared by both stores while holding Publisher
// then verification locks. An existing record uses its independent revision.
func CheckPublisherDomainVersion(p Publisher, v PublisherDomainVerification, expected PublisherDomainVersion) error {
	if expected.ID != v.ID || expected.Revision != v.Revision || v.ID == "" && expected.PublisherRevision != p.Revision {
		return E(CodePreconditionFailed, "publisher domain verification revision does not match")
	}
	return nil
}

type PublisherDomainMutation struct {
	State    PublisherDomainVerification
	Expected PublisherDomainVersion
	Audit    AuditEvent
	Now      func() time.Time
}

type PublisherDomainApproval struct {
	TenantID, PublisherID, Domain string
	ExpectedRevision              int64
	Policy                        PublisherDomainPolicy
	Now                           func() time.Time
	Audit                         AuditEvent
	Outbox                        OutboxEvent
}

type PublisherDomainStatus struct {
	ChallengeState       string                 `json:"challengeState"`
	ChallengeID          string                 `json:"challengeId,omitempty"`
	ChallengeIssuedAt    time.Time              `json:"challengeIssuedAt,omitzero"`
	ChallengeExpiresAt   time.Time              `json:"challengeExpiresAt,omitzero"`
	Mode                 string                 `json:"mode"`
	Version              PublisherDomainVersion `json:"-"`
	Domain               string                 `json:"domain"`
	State                string                 `json:"state"`
	VerifiedAt           time.Time              `json:"verifiedAt,omitzero"`
	ExpiresAt            time.Time              `json:"expiresAt,omitzero"`
	LastCheckAt          time.Time              `json:"lastCheckAt,omitzero"`
	LastCheckOutcome     string                 `json:"lastCheckOutcome,omitempty"`
	LastCheckChallengeID string                 `json:"lastCheckChallengeId,omitempty"`
}

type PublisherDomainChallenge struct {
	Status      PublisherDomainStatus `json:"status"`
	ChallengeID string                `json:"challengeId,omitempty"`
	Name        string                `json:"name,omitempty"`
	Value       string                `json:"value,omitempty"`
	ExpiresAt   time.Time             `json:"expiresAt,omitzero"`
}

func publisherVerificationDomain(value string) (string, error) {
	domain := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
	if !validDomain(domain) || net.ParseIP(domain) != nil || len(DomainChallengePrefix+domain) > 253 {
		return "", E(CodeInvalidArgument, "publisher verification requires an exact DNS domain within DNS name limits")
	}
	return domain, nil
}

func (s *Service) preparePublisherDomain(ctx context.Context, publisherID, domain string, permission identity.Permission) (context.Context, identity.Actor, Publisher, PublisherDomainVerification, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := requirePermission(ctx, permission, "")
	if err != nil {
		return ctx, actor, Publisher{}, PublisherDomainVerification{}, err
	}
	if !identity.ValidTenantID(publisherID) {
		return ctx, actor, Publisher{}, PublisherDomainVerification{}, E(CodeInvalidArgument, "publisher UUID is required")
	}
	domain, err = publisherVerificationDomain(domain)
	if err != nil {
		return ctx, actor, Publisher{}, PublisherDomainVerification{}, err
	}
	p, v, err := s.repo.ReadPublisherDomainVerification(ctx, actor.TenantID, publisherID, domain)
	return ctx, actor, p, v, err
}

func (s *Service) publisherDomainStatus(p Publisher, v PublisherDomainVerification) PublisherDomainStatus {
	now := s.clock.Now().UTC()
	challengeState := "not_issued"
	if v.ChallengeID != "" {
		switch {
		case v.ChallengeConsumed:
			challengeState = "consumed"
		case now.Before(v.ChallengeIssuedAt) || !now.Before(v.ChallengeExpiresAt):
			challengeState = "expired"
		default:
			challengeState = "pending"
		}
	}
	state := "not_verified"
	expires := v.ProofExpiresAt
	if !v.VerifiedAt.IsZero() {
		state = "expired"
		if earlier := v.VerifiedAt.Add(s.publisherDomainPolicy.ProofMaxAge); earlier.Before(expires) {
			expires = earlier
		}
		if v.ProofFresh(now, s.publisherDomainPolicy.ProofMaxAge) {
			state = "verified"
		}
	}
	return PublisherDomainStatus{ChallengeState: challengeState, ChallengeID: v.ChallengeID, ChallengeIssuedAt: v.ChallengeIssuedAt, ChallengeExpiresAt: v.ChallengeExpiresAt, Mode: s.publisherDomainPolicy.Mode, Version: v.Version(p), Domain: v.Domain, State: state, VerifiedAt: v.VerifiedAt, ExpiresAt: expires, LastCheckAt: v.LastCheckAt, LastCheckOutcome: v.LastCheckOutcome, LastCheckChallengeID: v.LastCheckChallengeID}
}

func (s *Service) GetPublisherDomainVerification(ctx context.Context, publisherID, domain string) (PublisherDomainStatus, error) {
	_, _, p, v, err := s.preparePublisherDomain(ctx, publisherID, domain, "publisher.read")
	if err != nil {
		return PublisherDomainStatus{}, err
	}
	return s.publisherDomainStatus(p, v), nil
}

func (s *Service) GetPublisherDomainChallenge(ctx context.Context, publisherID, domain string) (PublisherDomainChallenge, error) {
	_, _, p, v, err := s.preparePublisherDomain(ctx, publisherID, domain, "publisher.manage")
	if err != nil {
		return PublisherDomainChallenge{}, err
	}
	result := PublisherDomainChallenge{Status: s.publisherDomainStatus(p, v)}
	if s.publisherDomainPolicy.Mode != DomainVerificationDisabled && result.Status.ChallengeState == "pending" {
		result.ChallengeID = v.ChallengeID
		result.Name = DomainChallengePrefix + v.Domain + "."
		result.Value = DomainTXTValuePrefix + v.Nonce
		result.ExpiresAt = v.ChallengeExpiresAt
	}
	return result, nil
}

func (s *Service) IssuePublisherDomainChallenge(ctx context.Context, publisherID, domain, reason string, expected PublisherDomainVersion) (PublisherDomainChallenge, error) {
	ctx, actor, p, v, err := s.preparePublisherDomain(ctx, publisherID, domain, "publisher.manage")
	if err != nil {
		return PublisherDomainChallenge{}, err
	}
	if err = s.publisherDomainCommand(p, v, expected, reason, "publisher.domain_challenge_issued"); err != nil {
		return PublisherDomainChallenge{}, err
	}
	var nonce [32]byte
	if _, err = io.ReadFull(s.publisherDomainNonce, nonce[:]); err != nil {
		return PublisherDomainChallenge{}, Wrap(CodeInternal, "create publisher domain challenge", err)
	}
	at := mutationTime(s.clock.Now(), p.UpdatedAt, v.UpdatedAt)
	if v.ID == "" {
		v.ID, err = s.ids.New(at)
		if err != nil {
			return PublisherDomainChallenge{}, Wrap(CodeInternal, "create verification identifier", err)
		}
		v.CreatedAt = at
	}
	v.ChallengeID, err = s.ids.New(at)
	if err != nil {
		return PublisherDomainChallenge{}, Wrap(CodeInternal, "create challenge identifier", err)
	}
	v.Nonce = base64.RawURLEncoding.EncodeToString(nonce[:])
	v.ChallengeIssuedAt = at
	v.ChallengeExpiresAt = at.Add(s.publisherDomainPolicy.ChallengeTTL)
	v.ChallengeConsumed = false
	v, err = s.savePublisherDomain(ctx, actor, p, v, expected, reason, "publisher.domain_challenge_issued", at)
	if err != nil {
		return PublisherDomainChallenge{}, err
	}
	return PublisherDomainChallenge{Status: s.publisherDomainStatus(p, v), ChallengeID: v.ChallengeID, Name: DomainChallengePrefix + v.Domain + ".", Value: DomainTXTValuePrefix + v.Nonce, ExpiresAt: v.ChallengeExpiresAt}, nil
}

func (s *Service) publisherDomainCommand(p Publisher, v PublisherDomainVerification, expected PublisherDomainVersion, reason, action string) error {
	if s.publisherDomainPolicy.Mode == DomainVerificationDisabled {
		return E(CodeInvalidTransition, "publisher domain verification is disabled")
	}
	if err := CheckPublisherDomainVersion(p, v, expected); err != nil {
		return err
	}
	_, err := normalizeAuditReason(action, reason, "publisher verification reason is invalid")
	return err
}

func (s *Service) ClearPublisherDomainVerification(ctx context.Context, publisherID, domain, reason string, expected PublisherDomainVersion) (PublisherDomainStatus, error) {
	ctx, actor, p, v, err := s.preparePublisherDomain(ctx, publisherID, domain, "publisher.manage")
	if err != nil {
		return PublisherDomainStatus{}, err
	}
	if err = s.publisherDomainCommand(p, v, expected, reason, "publisher.domain_verification_cleared"); err != nil {
		return PublisherDomainStatus{}, err
	}
	if v.ID == "" {
		return PublisherDomainStatus{}, E(CodeInvalidArgument, "publisher domain has no verification state")
	}
	v.ChallengeID = ""
	v.Nonce = ""
	v.ChallengeIssuedAt = time.Time{}
	v.ChallengeExpiresAt = time.Time{}
	v.ChallengeConsumed = false
	v.VerifiedChallengeID = ""
	v.VerifiedAt = time.Time{}
	v.ProofExpiresAt = time.Time{}
	v, err = s.savePublisherDomain(ctx, actor, p, v, expected, reason, "publisher.domain_verification_cleared", mutationTime(s.clock.Now(), v.UpdatedAt))
	if err != nil {
		return PublisherDomainStatus{}, err
	}
	return s.publisherDomainStatus(p, v), nil
}

// CheckPublisherDomain performs one DNS call between short read preparation and
// final permission-admitted persistence. A completed negative check is data.
func (s *Service) CheckPublisherDomain(ctx context.Context, publisherID, domain, reason string, expected PublisherDomainVersion) (PublisherDomainStatus, error) {
	ctx, actor, p, v, err := s.preparePublisherDomain(ctx, publisherID, domain, "publisher.manage")
	if err != nil {
		return PublisherDomainStatus{}, err
	}
	if err = s.publisherDomainCommand(p, v, expected, reason, "publisher.domain_verification_checked"); err != nil {
		return PublisherDomainStatus{}, err
	}
	if err = ctx.Err(); err != nil {
		return PublisherDomainStatus{}, err
	}
	now := s.clock.Now().UTC()
	if v.ChallengeID == "" || v.ChallengeConsumed || now.Before(v.ChallengeIssuedAt) || !now.Before(v.ChallengeExpiresAt) {
		return PublisherDomainStatus{}, E(CodePreconditionFailed, "publisher domain challenge is absent, consumed or expired")
	}
	select {
	case s.publisherDomainLookups <- struct{}{}:
	default:
		return PublisherDomainStatus{}, E(CodeWorkLimitExceeded, "publisher DNS lookup capacity is exhausted")
	}
	defer func() { <-s.publisherDomainLookups }()
	lookupCtx, cancel := context.WithTimeout(ctx, s.publisherDomainPolicy.LookupTimeout)
	records, lookupErr := s.publisherTXT.LookupTXT(lookupCtx, DomainChallengePrefix+v.Domain+".")
	if lookupCtx.Err() != nil {
		lookupErr = lookupCtx.Err()
	}
	cancel()
	if err = ctx.Err(); err != nil {
		return PublisherDomainStatus{}, err
	}
	observed := s.clock.Now().UTC().Truncate(time.Microsecond)
	if observed.Before(v.UpdatedAt) || observed.Before(v.ChallengeIssuedAt) || !observed.Before(v.ChallengeExpiresAt) {
		return PublisherDomainStatus{}, E(CodePreconditionFailed, "publisher domain challenge changed or expired during lookup")
	}
	outcome := "mismatch"
	switch {
	case lookupErr != nil:
		outcome = "unavailable"
		var dnsError *net.DNSError
		if errors.As(lookupErr, &dnsError) && dnsError.IsNotFound {
			outcome = "not_found"
		}
	case len(records) == 0:
		outcome = "not_found"
	default:
		bytes := 0
		if len(records) <= 64 {
			for _, value := range records {
				if len(value) > 8<<10 {
					bytes = 8193
					break
				}
				bytes += len(value)
				if bytes > 8<<10 {
					break
				}
			}
		}
		if len(records) > 64 || bytes > 8<<10 {
			outcome = "response_limit"
		} else if slices.Contains(records, DomainTXTValuePrefix+v.Nonce) {
			outcome = "matched"
		}
	}
	v.LastCheckAt = observed
	v.LastCheckChallengeID = v.ChallengeID
	v.LastCheckOutcome = outcome
	if outcome == "matched" {
		v.VerifiedChallengeID = v.ChallengeID
		v.VerifiedAt = observed
		v.ProofExpiresAt = observed.Add(s.publisherDomainPolicy.ProofMaxAge)
		v.ChallengeConsumed = true
	}
	v, err = s.savePublisherDomain(ctx, actor, p, v, expected, reason, "publisher.domain_verification_checked", observed)
	if err != nil {
		return PublisherDomainStatus{}, err
	}
	return s.publisherDomainStatus(p, v), nil
}

func (s *Service) savePublisherDomain(ctx context.Context, actor identity.Actor, p Publisher, v PublisherDomainVerification, expected PublisherDomainVersion, reason, action string, at time.Time) (PublisherDomainVerification, error) {
	v.Revision++
	v.UpdatedAt = at
	facts := map[string]any{"domain": v.Domain, "verificationId": v.ID, "revision": v.Revision, "method": "dns_txt"}
	if action == "publisher.domain_verification_checked" {
		facts["outcome"] = v.LastCheckOutcome
		facts["challengeId"] = v.LastCheckChallengeID
	}
	if action == "publisher.domain_challenge_issued" {
		facts["challengeId"] = v.ChallengeID
	}
	evidence, err := s.newActorAuditRecord(ctx, actor, action, "publisher", p.ID, reason, facts, at)
	if err != nil {
		return PublisherDomainVerification{}, err
	}
	return s.repo.SavePublisherDomainVerification(ctx, PublisherDomainMutation{State: v, Expected: expected, Audit: evidence, Now: s.clock.Now})
}

// CheckPublisherDomainCompletion runs after the store locks, including any wait.
func CheckPublisherDomainCompletion(params PublisherDomainMutation, current PublisherDomainVerification) error {
	if params.Audit.Action != "publisher.domain_verification_checked" {
		return nil
	}
	if params.Now == nil {
		return E(CodeInternal, "publisher verification clock is unavailable")
	}
	now := params.Now().UTC()
	if current.ChallengeID == "" || current.ChallengeConsumed || now.Before(current.ChallengeIssuedAt) || !now.Before(current.ChallengeExpiresAt) || now.Before(params.State.LastCheckAt) || params.State.ChallengeID != current.ChallengeID || params.State.Nonce != current.Nonce {
		return E(CodePreconditionFailed, "publisher domain challenge changed or expired before completion")
	}
	return nil
}

// crypto/rand is the production source. Options permit an inert failure source
// to establish that issuance does not change retained state on entropy failure.
func publisherDomainNonce(reader io.Reader) io.Reader {
	if reader == nil {
		return rand.Reader
	}
	return reader
}

// CheckPublisherDomainTransition preserves the same current-state shape and
// immutable challenge/proof fields in memory and PostgreSQL repositories.
func CheckPublisherDomainTransition(current, next PublisherDomainVerification) error {
	invalid := next.Revision != current.Revision+1 || !identity.ValidTenantID(next.ID) || next.CreatedAt.IsZero() || next.UpdatedAt.Before(next.CreatedAt)
	if current.ID != "" {
		invalid = invalid || next.ID != current.ID || !next.CreatedAt.Equal(current.CreatedAt) || next.UpdatedAt.Before(current.UpdatedAt)
	}
	if next.ChallengeID == "" {
		invalid = invalid || next.Nonce != "" || !next.ChallengeIssuedAt.IsZero() || !next.ChallengeExpiresAt.IsZero() || next.ChallengeConsumed
	} else {
		nonce, err := base64.RawURLEncoding.DecodeString(next.Nonce)
		invalid = invalid || !identity.ValidTenantID(next.ChallengeID) || err != nil || len(nonce) != 32 || base64.RawURLEncoding.EncodeToString(nonce) != next.Nonce || next.ChallengeIssuedAt.Before(next.CreatedAt) || next.ChallengeIssuedAt.After(next.UpdatedAt) || !next.ChallengeExpiresAt.After(next.ChallengeIssuedAt)
	}
	if next.VerifiedChallengeID == "" {
		invalid = invalid || !next.VerifiedAt.IsZero() || !next.ProofExpiresAt.IsZero()
	} else {
		invalid = invalid || !identity.ValidTenantID(next.VerifiedChallengeID) || next.VerifiedAt.Before(next.CreatedAt) || next.VerifiedAt.After(next.UpdatedAt) || !next.ProofExpiresAt.After(next.VerifiedAt)
	}
	if next.LastCheckChallengeID == "" {
		invalid = invalid || !next.LastCheckAt.IsZero() || next.LastCheckOutcome != ""
	} else {
		invalid = invalid || !identity.ValidTenantID(next.LastCheckChallengeID) || next.LastCheckAt.Before(next.CreatedAt) || next.LastCheckAt.After(next.UpdatedAt) || !slices.Contains([]string{"matched", "mismatch", "not_found", "unavailable", "response_limit"}, next.LastCheckOutcome)
	}
	invalid = invalid || next.ChallengeConsumed && (next.ChallengeID == "" || next.ChallengeID != next.VerifiedChallengeID)
	if current.ChallengeID != "" && next.ChallengeID == current.ChallengeID {
		invalid = invalid || next.Nonce != current.Nonce || !next.ChallengeIssuedAt.Equal(current.ChallengeIssuedAt) || !next.ChallengeExpiresAt.Equal(current.ChallengeExpiresAt) || current.ChallengeConsumed && !next.ChallengeConsumed
	}
	if current.VerifiedChallengeID != "" && next.VerifiedChallengeID == current.VerifiedChallengeID {
		invalid = invalid || !next.VerifiedAt.Equal(current.VerifiedAt) || !next.ProofExpiresAt.Equal(current.ProofExpiresAt)
	}
	if invalid {
		return E(CodePreconditionFailed, "verification transition is invalid")
	}
	return nil
}

// PublisherApprovalAudit checks fresh proof and binds its exact identity into
// approval audit evidence while the store holds the Publisher and proof locks.
func PublisherApprovalAudit(params PublisherDomainApproval, v PublisherDomainVerification) (AuditEvent, error) {
	event := cloneAuditEvent(params.Audit)
	if params.Policy.Mode != DomainVerificationRequired {
		return event, nil
	}
	if params.Now == nil || v.TenantID != params.TenantID || v.PublisherID != params.PublisherID || v.Domain != params.Domain || !v.ProofFresh(params.Now().UTC(), params.Policy.ProofMaxAge) {
		return AuditEvent{}, E(CodePreconditionFailed, "a fresh DNS TXT verification is required before approving this domain")
	}
	if event.Details == nil {
		event.Details = map[string]any{}
	}
	expires := v.ProofExpiresAt
	if earlier := v.VerifiedAt.Add(params.Policy.ProofMaxAge); earlier.Before(expires) {
		expires = earlier
	}
	event.Details["facts"] = map[string]any{"domain": params.Domain, "verificationMethod": "dns_txt", "verificationId": v.ID, "challengeId": v.VerifiedChallengeID, "verifiedAt": v.VerifiedAt.Format("2006-01-02T15:04:05.999999999Z07:00"), "expiresAt": expires.Format("2006-01-02T15:04:05.999999999Z07:00")}
	return event, nil
}

func PublisherVerificationAction(action string) bool {
	return action == "publisher.domain_challenge_issued" || action == "publisher.domain_verification_checked" || action == "publisher.domain_verification_cleared"
}

// PublisherDomainVerificationPolicy returns immutable startup configuration.
// It performs no DNS lookup and conveys no permission to read private evidence.
func (s *Service) PublisherDomainVerificationPolicy() PublisherDomainPolicy {
	return s.publisherDomainPolicy
}
