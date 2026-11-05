// Package trustevidence acquires bounded public ANS evidence through the hardened
// fetch boundary. It owns no credentials, network authority, Jobs or persistence.
package trustevidence

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/standards/anstrust"
)

// Outbound is implemented by the deployment's hardened fetcher.Router.
type Outbound interface {
	Fetch(context.Context, fetcher.Request) (fetcher.Result, error)
}

var (
	ErrDisabled = errors.New("ANS evidence acquisition is disabled")
	ErrPolicy   = errors.New("ANS authority policy is unavailable")
	ErrBudget   = errors.New("ANS evidence acquisition budget exceeded")
	ErrResponse = errors.New("ANS evidence response is invalid")
)

// HTTPError preserves status for Job retry classification without remote text.
type HTTPError struct{ Status int }

func (e *HTTPError) Error() string { return "ANS evidence endpoint did not return success" }

type Acquirer struct {
	settings     config.TrustVerification
	egressDigest string
	outbound     Outbound
}

// New requires startup-loaded configuration. Construction performs no I/O.
func New(c config.Config, outbound Outbound) (*Acquirer, error) {
	if err := c.TrustVerification.Validate(c.Federation.Policy, config.RoleAPI, c.Worker.LeaseDuration.Duration); err != nil {
		return nil, err
	}
	if c.TrustVerification.Enabled {
		if outbound == nil || c.Federation.PolicyDigest == "" {
			return nil, ErrPolicy
		}
		for _, p := range c.TrustVerification.Policies {
			if _, _, ok := c.TrustVerification.Policy(p.Name); !ok {
				return nil, ErrPolicy
			}
		}
	}
	// Copy route selections so later caller edits cannot change acquisition authority.
	settings := c.TrustVerification
	settings.Policies = append([]config.TrustPolicy(nil), settings.Policies...)
	return &Acquirer{settings: settings, egressDigest: c.Federation.PolicyDigest, outbound: outbound}, nil
}

// Acquire retains exact bytes, including a partial bundle after a failure.
// The caller verifies that bundle separately and retains its classified result.
// Invalid signatures stop further requests. No request inherits source secrets.
func (a *Acquirer) Acquire(ctx context.Context, policyName, tenantID string, entry []byte) (anstrust.Bundle, error) {
	b := anstrust.Bundle{Profile: anstrust.Profile}
	if !a.settings.Enabled {
		return b, ErrDisabled
	}
	if len(entry) > anstrust.MaxEntryBytes {
		return b, anstrust.ErrBundle
	}
	b.Entry = append([]byte(nil), entry...)
	if raw, err := json.Marshal(b); err != nil || int64(len(raw)) > a.settings.Limits.BundleBytes {
		// Even a failed acquisition must not return an over-budget partial bundle.
		b.Entry = nil
		return b, ErrBudget
	}
	p, verifier, ok := a.settings.Policy(policyName)
	if !ok || !identity.ValidTenantID(tenantID) {
		return b, ErrPolicy
	}
	ctx, cancel := context.WithTimeout(ctx, a.settings.Timeout.Duration)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return b, err
	}
	subject, receipt, err := verifier.Reference(b.Entry)
	if err != nil {
		return b, err
	}
	requests := 0
	get := func(url, media string, maxBytes int64, target *[]byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if requests >= a.settings.MaxRequests {
			return ErrBudget
		}
		// Reserve serialized JSON/base64 overhead before every network request.
		// A one-byte placeholder includes an otherwise omitted history member.
		// Its four base64 characters are replaced by the real encoded payload.
		previous := *target
		*target = []byte{0}
		raw, e := json.Marshal(b)
		*target = previous
		if e != nil || int64(len(raw)) > a.settings.Limits.BundleBytes {
			return ErrBudget
		}
		remaining := (a.settings.Limits.BundleBytes - int64(len(raw)) + 4) / 4 * 3
		maxBytes = min(maxBytes, remaining)
		if maxBytes <= 0 {
			return ErrBudget
		}
		requests++
		r, e := a.outbound.Fetch(ctx, fetcher.Request{URL: url, Method: http.MethodGet, Accept: []string{media}, IdentityEncoding: true,
			TenantID: tenantID, NetworkProfile: p.NetworkProfile, ExpectedPolicyDigest: a.egressDigest,
			RejectRedirects: true, RejectOriginChanges: true, RedactQueryInErrors: true, MaxResponseBytes: maxBytes})
		if e != nil {
			return e
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if r.Status != http.StatusOK {
			return &HTTPError{Status: r.Status}
		}
		actual, _, e := mime.ParseMediaType(r.MediaType)
		if e != nil || actual != media || (r.FinalURL != "" && r.FinalURL != url) || len(r.Body) == 0 {
			return ErrResponse
		}
		if int64(len(r.Body)) > maxBytes {
			return ErrBudget
		}
		*target = append([]byte(nil), r.Body...)
		raw, e = json.Marshal(b)
		if e != nil || int64(len(raw)) > a.settings.Limits.BundleBytes {
			*target = nil
			return ErrBudget
		}
		return nil
	}
	if err := get(receipt, "application/scitt-receipt+cose", a.settings.Limits.ReceiptBytes, &b.Receipt); err != nil {
		return b, err
	}
	// Reject unauthentic receipts before contacting additional evidence endpoints.
	r, err := verifier.Verify(ctx, b, time.Time{})
	if err != nil {
		return b, err
	}
	if r.Receipt.State != "valid" || r.Producer.State != "valid" || r.Log.State != "valid" || r.Inclusion.State == "invalid" {
		return b, nil
	}
	if err := get(p.EvidenceOrigin+"/checkpoint", "text/plain", a.settings.Limits.CheckpointBytes, &b.Checkpoint); err != nil {
		return b, err
	}
	size, err := verifier.RequiredHistory(b.Entry, b.Receipt, b.Checkpoint)
	if contextErr := ctx.Err(); contextErr != nil {
		return b, contextErr
	}
	if err != nil {
		// The retained bundle carries a permanent negative assessment.
		return b, nil
	}
	if size != 0 {
		n := strconv.FormatUint(size, 10)
		if err := get(p.EvidenceOrigin+"/v1/log/checkpoint/history?fromSize="+n+"&toSize="+n+"&limit=1", "application/json", a.settings.Limits.HistoryBytes, &b.CheckpointHistory); err != nil {
			return b, err
		}
	}
	r, err = verifier.Verify(ctx, b, time.Time{})
	if err != nil {
		return b, err
	}
	if r.Checkpoint.State != "valid" || r.Inclusion.State != "valid" || r.Target.State != "valid" {
		return b, nil
	}
	err = get(p.EvidenceOrigin+"/v1/agents/"+subject+"/status-token", "application/ans-status-token+cbor", a.settings.Limits.StatusBytes, &b.StatusToken)
	return b, err
}
