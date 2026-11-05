// Package idempotency defines the durable command replay contract.
// The PostgreSQL adapter implements atomic mutation and response capture; MemoryStore
// retains process-local development reservations only. HTTP callers must apply
// current authentication, authorization, and disclosure checks before replay.
package idempotency

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

type commandAbortKey struct{}

// WithCommandAbort installs the atomic store's rollback hook for HTTP panic or
// response-encoding failure paths that happen after a repository call returns.
func WithCommandAbort(ctx context.Context, abort func(error)) context.Context {
	return context.WithValue(ctx, commandAbortKey{}, abort)
}

// AbortCommand marks the current atomic command transaction rollback-only.
// It is a no-op when ctx has no hook installed by [WithCommandAbort].
func AbortCommand(ctx context.Context, err error) {
	abort, _ := ctx.Value(commandAbortKey{}).(func(error))
	if abort != nil {
		abort(err)
	}
}

// Scope identifies a caller command key and its authorization binding.
// AuthorizationDigest is checked on reuse but is not part of the command's
// identity: changed permissions must not create a separate execution lane.
type Scope struct {
	TenantID            string
	Actor               string
	Route               string
	Key                 string
	AuthorizationDigest string
}

// Response is the exact completed HTTP response retained for replay.
type Response struct {
	Authority ReplayAuthority
	Digest    string
	Status    int
	Headers   map[string][]string
	Body      []byte
}

// ReplayAuthority is bounded server-owned disclosure evidence saved with the
// exact reply. It is never constructed from an untrusted retry body.
type ReplayAuthority struct {
	ValidationProblem bool                       `json:"validationProblem,omitempty"`
	Version           int                        `json:"version"`
	Checks            []identity.PermissionCheck `json:"checks"`
	HumanOnly         bool                       `json:"humanOnly,omitempty"`
	JobID             string                     `json:"jobId,omitempty"`
}

// Reservation reports whether a caller owns execution, can replay, or must wait.
type Reservation struct {
	Acquired bool
	Pending  bool
	Response Response
}

// Execution is the result of an atomic command attempt. A replay response was
// committed by an earlier attempt; pending means no command may execute yet.
type Execution struct {
	ReservationID string
	Pending       bool
	Replayed      bool
	Response      Response
}

// Store reserves process-local development keys and retains their responses.
type Store interface {
	// Reserve claims a key until the supplied expiry, reports pending execution,
	// or returns a completed response. Reuse must match the request digest and
	// authorization binding.
	Reserve(context.Context, Scope, string, time.Time) (Reservation, error)
	// Complete captures a response for a matching reservation. It does not make
	// domain writes atomic; durable commands use [AtomicStore] instead.
	Complete(context.Context, Scope, Response) error
}

// AtomicStore serializes a command key and commits the captured response in
// the same PostgreSQL transaction as every command mutation made by execute.
// Implementations may keep pre-mutation work outside that transaction.
// The callback must pass its supplied context to repository calls so they join
// the command transaction. Pending and replayed attempts do not run it; callers
// must authorize the request before executing or returning a retained response.
type AtomicStore interface {
	// Execute attempts the scoped command with the given request digest and
	// expiry. The callback's context carries the shared transaction boundary.
	Execute(context.Context, Scope, string, time.Time, func(context.Context) Response) (Execution, error)
}

// AdvisoryLockKey deterministically maps a command identity to PostgreSQL's
// advisory-lock namespace using a non-negative 63-bit hash. The
// authorization digest is deliberately excluded so changed permissions cannot
// create a second execution lane for the same command key.
func AdvisoryLockKey(scope Scope) int64 {
	hash := sha256.New()
	for _, value := range []string{strings.ToLower(scope.TenantID), scope.Actor, scope.Route, scope.Key} {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(value)))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write([]byte(value))
	}
	sum := hash.Sum(nil)
	return int64(binary.BigEndian.Uint64(sum[:8]) >> 1)
}

type memoryEntry struct {
	digest              string
	authorizationDigest string
	response            Response
	complete            bool
	expiresAt           time.Time
}

// MemoryStore implements process-local idempotency for the development profile.
type MemoryStore struct {
	mu      sync.Mutex
	entries map[Scope]memoryEntry
}

// NewMemoryStore creates an empty development idempotency store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{entries: make(map[Scope]memoryEntry)}
}

// Reserve atomically claims an unused key or reports its existing state.
func (m *MemoryStore) Reserve(_ context.Context, scope Scope, digest string, expiresAt time.Time) (Reservation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	identity := commandIdentity(scope)
	entry, exists := m.entries[identity]
	if exists && !entry.expiresAt.After(time.Now().UTC()) {
		delete(m.entries, identity)
		exists = false
	}
	if !exists {
		m.entries[identity] = memoryEntry{digest: digest, authorizationDigest: scope.AuthorizationDigest, expiresAt: expiresAt}
		return Reservation{Acquired: true}, nil
	}
	if entry.digest != digest {
		return Reservation{}, catalog.E(catalog.CodeIdempotencyKeyReused, "the Idempotency-Key was already used with another request")
	}
	if entry.authorizationDigest != scope.AuthorizationDigest {
		return Reservation{}, catalog.E(catalog.CodeForbidden, "current authorization does not match the idempotent command")
	}
	if !entry.complete {
		return Reservation{Pending: true}, nil
	}
	return Reservation{Response: cloneResponse(entry.response)}, nil
}

// Complete records the exact response for subsequent replay.
func (m *MemoryStore) Complete(_ context.Context, scope Scope, response Response) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	identity := commandIdentity(scope)
	entry, exists := m.entries[identity]
	if !exists || entry.digest != response.Digest || entry.authorizationDigest != scope.AuthorizationDigest {
		return catalog.E(catalog.CodeConflict, "idempotency reservation is not owned by this request")
	}
	entry.response = cloneResponse(response)
	entry.complete = true
	m.entries[identity] = entry
	return nil
}

// commandIdentity normalizes tenant spelling and excludes authorization state so
// changed permissions cannot create another command execution lane.
func commandIdentity(scope Scope) Scope {
	scope.TenantID = strings.ToLower(scope.TenantID)
	scope.AuthorizationDigest = ""
	return scope
}

// cloneResponse copies body bytes and every header value slice so replay callers
// cannot mutate retained responses.
func cloneResponse(response Response) Response {
	copyValue := response
	copyValue.Authority.Checks = slices.Clone(response.Authority.Checks)
	copyValue.Body = append([]byte(nil), response.Body...)
	copyValue.Headers = make(map[string][]string, len(response.Headers))
	for name, values := range response.Headers {
		copyValue.Headers[name] = append([]string(nil), values...)
	}
	return copyValue
}
