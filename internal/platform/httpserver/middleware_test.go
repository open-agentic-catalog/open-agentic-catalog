package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/idempotency"
)

type flakyCompletionStore struct {
	base      *idempotency.MemoryStore
	remaining atomic.Int32
	complete  atomic.Int32
	mu        sync.Mutex
	contexts  []context.Context
}

type atomicReplayStore struct {
	executions      atomic.Int32
	response        idempotency.Response
	pending         bool
	err             error
	errAfterExecute bool
}

func TestAuthenticationMiddlewareRejectsAValidBearerEnvelopeWithInvalidCredentials(t *testing.T) {
	t.Parallel()

	handler := authentication(discoveryAuthenticator{}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("rejected credentials reached the protected handler")
	}))
	request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/catalog/resources", nil)
	request.Header.Set("Authorization", "Bearer rejected-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || response.Header().Get("WWW-Authenticate") != `Bearer realm="open-agentic-catalog", error="invalid_token"` {
		t.Fatalf("rejected Bearer response = %d, headers %#v", response.Code, response.Header())
	}
}

func (s *atomicReplayStore) Execute(ctx context.Context, _ idempotency.Scope, digest string, _ time.Time, execute func(context.Context) idempotency.Response) (idempotency.Execution, error) {
	s.executions.Add(1)
	if s.err != nil && !s.errAfterExecute {
		return idempotency.Execution{}, s.err
	}
	if s.pending {
		return idempotency.Execution{ReservationID: "00000000-0000-7000-8000-000000000001", Pending: true}, nil
	}
	if s.response.Status != 0 {
		return idempotency.Execution{ReservationID: "00000000-0000-7000-8000-000000000001", Replayed: true, Response: s.response}, nil
	}
	s.response = execute(ctx)
	if s.err != nil {
		return idempotency.Execution{}, s.err
	}
	if s.response.Digest != digest {
		return idempotency.Execution{}, errors.New("digest mismatch")
	}
	return idempotency.Execution{ReservationID: "00000000-0000-7000-8000-000000000001", Response: s.response}, nil
}

func TestIdempotencyMiddlewareUsesAtomicCommandPathAndExactReplay(t *testing.T) {
	store := &atomicReplayStore{}
	var calls atomic.Int32
	handler := newAtomicTestCache(store, slog.New(slog.DiscardHandler)).middleware(1024, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writer.Header().Set("Location", "/api/v1/resources/one")
		writer.WriteHeader(http.StatusCreated)
		_, _ = writer.Write([]byte(`{"id":"one"}`))
	}))
	serve := func() *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/resources", nil)
		request.Header.Set("Idempotency-Key", "atomic-command")
		request = request.WithContext(identity.WithActor(request.Context(), identity.Actor{TenantID: "tenant", Subject: "actor"}))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	first := serve()
	replay := serve()
	if first.Code != http.StatusCreated || replay.Code != http.StatusCreated || replay.Header().Get("Idempotency-Replayed") != "true" || replay.Header().Get("Location") != "/api/v1/resources/one" || replay.Body.String() != `{"id":"one"}` || calls.Load() != 1 || store.executions.Load() != 2 {
		t.Fatalf("atomic responses = first %d, replay %d/%q/%q/%q, calls %d, executions %d", first.Code, replay.Code, replay.Header().Get("Idempotency-Replayed"), replay.Header().Get("Location"), replay.Body.String(), calls.Load(), store.executions.Load())
	}
}

func TestIdempotentProblemReplayKeepsExecutionRequestIDAndUsesCurrentHeader(t *testing.T) {
	t.Parallel()
	store := &atomicReplayStore{}
	cache := newAtomicTestCache(store, slog.New(slog.DiscardHandler))
	handler := requestIDMiddleware(cache.middleware(1024, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writeProblem(writer, request, catalog.E(catalog.CodeConflict, "command conflict"))
	})))
	serve := func(requestIDValue string) (*httptest.ResponseRecorder, Problem) {
		request := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/resources", nil)
		request.Header.Set("Idempotency-Key", "problem-replay")
		request.Header.Set("X-Request-ID", requestIDValue)
		request = request.WithContext(identity.WithActor(request.Context(), identity.Actor{TenantID: "tenant", Subject: "actor"}))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		var problem Problem
		if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
			t.Fatal(err)
		}
		return response, problem
	}
	first, firstProblem := serve("original-command")
	replay, replayProblem := serve("replay-attempt")
	if first.Code != http.StatusConflict || firstProblem.RequestID != "original-command" {
		t.Fatalf("original problem = status %d, %#v", first.Code, firstProblem)
	}
	if replay.Code != http.StatusConflict || replay.Header().Get("X-Request-ID") != "replay-attempt" || len(replay.Header().Values("X-Request-ID")) != 1 || replay.Header().Get("Idempotency-Replayed") != "true" || replayProblem.RequestID != "original-command" {
		t.Fatalf("replayed problem = status %d, headers %#v, problem %#v", replay.Code, replay.Header(), replayProblem)
	}
	retainedHeaders := http.Header(store.response.Headers)
	if retainedHeaders.Get("X-Request-ID") != "" || retainedHeaders.Get("Idempotency-Replayed") != "" {
		t.Fatalf("retained response contains per-attempt headers: %#v", store.response.Headers)
	}
}

func TestWriteProblemMarksAtomicCommandRollbackOnly(t *testing.T) {
	t.Parallel()
	want := catalog.E(catalog.CodeConflict, "command conflict")
	var aborted error
	ctx := idempotency.WithCommandAbort(context.Background(), func(err error) { aborted = err })
	request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/resources", nil)
	response := httptest.NewRecorder()
	writeProblem(response, request, want)
	if !errors.Is(aborted, want) || response.Code != http.StatusConflict {
		t.Fatalf("problem abort = %v, status %d", aborted, response.Code)
	}
}

func TestWriteProblemPreservesReviewedOutboundEvidence(t *testing.T) {
	t.Parallel()
	want := catalog.E(catalog.CodeDependencyUnavailable, "source request failed")
	var aborted error
	ctx := idempotency.WithCommandAbort(context.Background(), func(err error) { aborted = err })
	request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/federation/sources:live-search", nil)
	response := httptest.NewRecorder()
	writeProblem(response, request, catalog.PreserveEvidence(want))
	if aborted != nil || response.Code != http.StatusServiceUnavailable {
		t.Fatalf("evidence-bearing problem abort = %v, status %d", aborted, response.Code)
	}
	if !catalog.ErrorPreservesEvidence(catalog.PreserveEvidence(want)) || catalog.ErrorCodeOf(catalog.PreserveEvidence(want)) != catalog.CodeDependencyUnavailable {
		t.Fatal("evidence marker changed the domain error contract")
	}
}

func TestIdempotencyMiddlewareCanonicalizesUUIDTenantCasing(t *testing.T) {
	t.Parallel()
	store := idempotency.NewMemoryStore()
	var calls atomic.Int32
	handler := newIdempotencyCache(store, slog.New(slog.DiscardHandler)).middleware(1024, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writer.WriteHeader(http.StatusCreated)
	}))
	serve := func(tenantID string) *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/resources", nil)
		request.Header.Set("Idempotency-Key", "tenant-case")
		request = request.WithContext(identity.WithActor(request.Context(), identity.Actor{TenantID: tenantID, Subject: "actor"}))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	if first := serve("abcdefab-cdef-7abc-8def-abcdefabcdef"); first.Code != http.StatusCreated {
		t.Fatalf("first tenant spelling returned %d", first.Code)
	}
	if replay := serve("ABCDEFAB-CDEF-7ABC-8DEF-ABCDEFABCDEF"); replay.Code != http.StatusCreated || replay.Header().Get("Idempotency-Replayed") != "true" || calls.Load() != 1 {
		t.Fatalf("canonical tenant replay = status %d, replay %q, calls %d", replay.Code, replay.Header().Get("Idempotency-Replayed"), calls.Load())
	}
}

func TestIdempotencyMiddlewareAtomicPendingAndFailureDoNotExecute(t *testing.T) {
	t.Parallel()
	actor := identity.Actor{TenantID: "tenant", Subject: "actor"}
	for name, store := range map[string]*atomicReplayStore{
		"pending": {pending: true},
		"failure": {err: catalog.E(catalog.CodeDependencyUnavailable, "database unavailable")},
	} {
		t.Run(name, func(t *testing.T) {
			handler := newAtomicTestCache(store, slog.New(slog.DiscardHandler)).middleware(1024, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("unreserved atomic command executed")
			}))
			request := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/resources", nil)
			request.Header.Set("Idempotency-Key", "atomic-"+name)
			request = request.WithContext(identity.WithActor(request.Context(), actor))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusServiceUnavailable || name == "pending" && response.Header().Get("Retry-After") != "1" {
				t.Fatalf("atomic %s response = %d, retry-after %q", name, response.Code, response.Header().Get("Retry-After"))
			}
		})
	}
}

func TestConcurrentTransientIdempotencyResponseIsNotMarkedAsReplay(t *testing.T) {
	t.Parallel()
	actor := identity.Actor{TenantID: "tenant", Subject: "actor"}
	digestInput, err := json.Marshal(struct {
		Query       string `json:"query"`
		IfMatch     string `json:"ifMatch"`
		ContentType string `json:"contentType"`
		Body        []byte `json:"body"`
	}{Body: []byte{}})
	if err != nil {
		t.Fatal(err)
	}
	digestSum := sha256.Sum256(digestInput)
	done := make(chan struct{})
	close(done)
	cache := newIdempotencyCache(idempotency.NewMemoryStore(), slog.New(slog.DiscardHandler))
	cache.entries["tenant\x00"+actor.CommandPrincipal()+"\x00POST /api/v1/resources\x00transient-command"] = &storedResponse{
		digest:              fmt.Sprintf("%x", digestSum),
		authorizationDigest: actor.CommandDigest(),
		status:              http.StatusServiceUnavailable,
		header:              http.Header{"Retry-After": {"1"}},
		body:                []byte(`{"code":"DEPENDENCY_UNAVAILABLE"}`),
		retained:            false,
		done:                done,
	}
	handler := cache.middleware(1024, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("transient waiter executed the command")
	}))
	request := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/resources", nil)
	request.Header.Set("Idempotency-Key", "transient-command")
	request = request.WithContext(identity.WithActor(request.Context(), actor))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "1" || response.Header().Get("Idempotency-Replayed") != "" {
		t.Fatalf("transient waiter response = %d, retry-after %q, replay %q", response.Code, response.Header().Get("Retry-After"), response.Header().Get("Idempotency-Replayed"))
	}
}

func TestIdempotencyReplayFailsClosedWhenActorAuthorizationChanges(t *testing.T) {
	t.Parallel()
	store := idempotency.NewMemoryStore()
	var calls atomic.Int32
	cache := newIdempotencyCache(store, slog.New(slog.DiscardHandler))
	cache.replayAdmission = func(r *http.Request, _ idempotency.ReplayAuthority, emit func()) error {
		actor, _ := identity.FromContext(r.Context())
		if actor.HasRole(identity.RoleReader, "") {
			return catalog.E(catalog.CodeForbidden, "current disclosure denied")
		}
		emit()
		return nil
	}
	handler := cache.middleware(1024, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writer.WriteHeader(http.StatusCreated)
	}))
	serve := func(roles ...identity.Role) *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/resources", nil)
		request.Header.Set("Idempotency-Key", "authorization-bound-command")
		request = request.WithContext(identity.WithActor(request.Context(), identity.Actor{TenantID: "tenant", Subject: "actor", Roles: roles}))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	if first := serve(identity.RoleContributor); first.Code != http.StatusCreated {
		t.Fatalf("first response = %d", first.Code)
	}
	if changed := serve(identity.RoleReader); changed.Code != http.StatusForbidden || changed.Header().Get("Idempotency-Replayed") != "" || calls.Load() != 1 {
		t.Fatalf("authorization-changed response = %d replay %q calls %d", changed.Code, changed.Header().Get("Idempotency-Replayed"), calls.Load())
	}
}

func TestConcurrentIdempotencyWaiterCannotReplayAcrossAuthorizationChange(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	release := make(chan struct{})
	releaseCommand := func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}
	defer releaseCommand()
	store := idempotency.NewMemoryStore()
	cache := newIdempotencyCache(store, slog.New(slog.DiscardHandler))
	cache.replayAdmission = func(r *http.Request, _ idempotency.ReplayAuthority, emit func()) error {
		actor, _ := identity.FromContext(r.Context())
		if actor.HasRole(identity.RoleReader, "") {
			return catalog.E(catalog.CodeForbidden, "current disclosure denied")
		}
		emit()
		return nil
	}
	handler := cache.middleware(1024, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		writer.WriteHeader(http.StatusCreated)
	}))
	serve := func(role identity.Role) *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/resources", nil)
		request.Header.Set("Idempotency-Key", "concurrent-authorization-bound-command")
		request = request.WithContext(identity.WithActor(request.Context(), identity.Actor{TenantID: "tenant", Subject: "actor", Roles: []identity.Role{role}}))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	firstResult := make(chan *httptest.ResponseRecorder, 1)
	go func() { firstResult <- serve(identity.RoleContributor) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first command did not reach its handler")
	}
	changedResult := make(chan *httptest.ResponseRecorder, 1)
	go func() { changedResult <- serve(identity.RoleReader) }()
	releaseCommand()
	changed := <-changedResult
	if changed.Code != http.StatusForbidden || changed.Header().Get("Idempotency-Replayed") != "" {
		t.Fatalf("authorization-changed waiter = %d replay %q", changed.Code, changed.Header().Get("Idempotency-Replayed"))
	}
	releaseCommand()
	select {
	case first := <-firstResult:
		if first.Code != http.StatusCreated {
			t.Fatalf("first response = %d", first.Code)
		}
	case <-time.After(time.Second):
		t.Fatal("first command did not finish")
	}
}

func (s *flakyCompletionStore) Reserve(ctx context.Context, scope idempotency.Scope, digest string, expiresAt time.Time) (idempotency.Reservation, error) {
	return s.base.Reserve(ctx, scope, digest, expiresAt)
}

func (s *flakyCompletionStore) Complete(ctx context.Context, scope idempotency.Scope, response idempotency.Response) error {
	s.complete.Add(1)
	s.mu.Lock()
	s.contexts = append(s.contexts, ctx)
	s.mu.Unlock()
	if s.remaining.Add(-1) >= 0 {
		return errors.New("transient completion failure")
	}
	return s.base.Complete(ctx, scope, response)
}

func TestIdempotencyCompletionRetriesAndDurablyReplays(t *testing.T) {
	store := &flakyCompletionStore{base: idempotency.NewMemoryStore()}
	store.remaining.Store(1)
	var calls atomic.Int32
	next := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writer.Header().Set("Location", "/api/v1/resources/one")
		writer.WriteHeader(http.StatusCreated)
		_, _ = writer.Write([]byte(`{"id":"one"}`))
	})
	handler := newIdempotencyCache(store, slog.New(slog.DiscardHandler)).middleware(1024, next)

	serve := func() *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/resources", nil)
		request.Header.Set("Idempotency-Key", "create-one")
		request = request.WithContext(identity.WithActor(request.Context(), identity.Actor{TenantID: "tenant", Subject: "actor"}))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	first := serve()
	if first.Code != http.StatusCreated || store.complete.Load() != 2 {
		t.Fatalf("first command = status %d, completion attempts %d", first.Code, store.complete.Load())
	}
	store.mu.Lock()
	if len(store.contexts) != 2 || store.contexts[0] != store.contexts[1] {
		store.mu.Unlock()
		t.Fatal("completion retries did not share one cleanup context")
	}
	deadline, ok := store.contexts[0].Deadline()
	store.mu.Unlock()
	if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 5*time.Second {
		t.Fatalf("completion cleanup deadline = %v", deadline)
	}
	second := serve()
	if second.Code != http.StatusCreated || second.Header().Get("Idempotency-Replayed") != "true" || calls.Load() != 1 {
		t.Fatalf("replay = status %d, header %q, handler calls %d", second.Code, second.Header().Get("Idempotency-Replayed"), calls.Load())
	}
}

type pendingThenReplayStore struct{ reserves atomic.Int32 }

func (s *pendingThenReplayStore) Reserve(context.Context, idempotency.Scope, string, time.Time) (idempotency.Reservation, error) {
	if s.reserves.Add(1) == 1 {
		return idempotency.Reservation{Pending: true}, nil
	}
	return idempotency.Reservation{Response: idempotency.Response{Status: http.StatusCreated, Body: []byte(`{"id":"one"}`)}}, nil
}

func (*pendingThenReplayStore) Complete(context.Context, idempotency.Scope, idempotency.Response) error {
	return nil
}

type failingIdempotencyStore struct {
	reserveErr  error
	completeErr error
	completes   atomic.Int32
}

func (s *failingIdempotencyStore) Reserve(context.Context, idempotency.Scope, string, time.Time) (idempotency.Reservation, error) {
	if s.reserveErr != nil {
		return idempotency.Reservation{}, s.reserveErr
	}
	return idempotency.Reservation{Acquired: true}, nil
}

func (s *failingIdempotencyStore) Complete(context.Context, idempotency.Scope, idempotency.Response) error {
	s.completes.Add(1)
	return s.completeErr
}

type failingRequestBody struct{ err error }

func (body failingRequestBody) Read([]byte) (int, error) { return 0, body.err }
func (failingRequestBody) Close() error                  { return nil }

func TestIdempotencyPendingResponseDoesNotStickInLocalCache(t *testing.T) {
	store := &pendingThenReplayStore{}
	handler := newIdempotencyCache(store, slog.New(slog.DiscardHandler)).middleware(1024, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("a request without a reservation must not execute")
	}))
	serve := func() *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/resources", nil)
		request.Header.Set("Idempotency-Key", "create-one")
		request = request.WithContext(identity.WithActor(request.Context(), identity.Actor{TenantID: "tenant", Subject: "actor"}))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	if first := serve(); first.Code != http.StatusServiceUnavailable || first.Header().Get("Retry-After") != "1" {
		t.Fatalf("pending response = status %d, retry-after %q", first.Code, first.Header().Get("Retry-After"))
	}
	if second := serve(); second.Code != http.StatusCreated || second.Header().Get("Idempotency-Replayed") != "true" || store.reserves.Load() != 2 {
		t.Fatalf("later replay = status %d, replay %q, reserves %d", second.Code, second.Header().Get("Idempotency-Replayed"), store.reserves.Load())
	}
}

func TestRecoveredHandlerPanicCompletesIdempotencyResponse(t *testing.T) {
	store := idempotency.NewMemoryStore()
	logger := slog.New(slog.DiscardHandler)
	cache := newIdempotencyCache(store, logger)
	var invocations atomic.Int32
	inner := recoverPanics(logger, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		invocations.Add(1)
		writer.WriteHeader(http.StatusAccepted)
		_, _ = writer.Write([]byte(`{"partial":true}`))
		panic("test panic")
	}))
	handler := cache.middleware(1024, inner)
	actor := identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "contributor"}
	request := func() *http.Request {
		value := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/resources", nil)
		value.Header.Set("Idempotency-Key", "panic-key")
		return value.WithContext(identity.WithActor(value.Context(), actor))
	}
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, request())
	if first.Code != http.StatusInternalServerError || strings.Contains(first.Body.String(), "partial") {
		t.Fatalf("first panic response = %d, body=%s", first.Code, first.Body.String())
	}
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, request())
	if second.Code != http.StatusInternalServerError || second.Header().Get("Idempotency-Replayed") != "true" || invocations.Load() != 1 {
		t.Fatalf("replayed panic response = %d, replay=%q, invocations=%d", second.Code, second.Header().Get("Idempotency-Replayed"), invocations.Load())
	}
}

func TestPanicRecoveryPreservesRequestIDWithoutLoggingPanicValues(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	handler := requestIDMiddleware(recoverPanics(logger, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("sensitive panic value")
	})))
	request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/resources/private-id?token=private", nil)
	request.Header.Set("X-Request-ID", "request-panic")
	request.Header.Set("Traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	var problem Problem
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusInternalServerError || response.Header().Get("X-Request-ID") != "request-panic" || problem.Code != catalog.CodeInternal || problem.RequestID != "request-panic" {
		t.Fatalf("panic response = status %d, headers %#v, problem %#v", response.Code, response.Header(), problem)
	}
	if !strings.Contains(logs.String(), "request_id=request-panic") || !strings.Contains(logs.String(), "trace_id=4bf92f3577b34da6a3ce929d0e0e4736") || !strings.Contains(logs.String(), "route=unmatched") || strings.Contains(logs.String(), "sensitive panic value") || strings.Contains(logs.String(), "private-id") || strings.Contains(logs.String(), "token=private") {
		t.Fatalf("panic diagnostics = %s", logs.String())
	}
}

func TestRequestCorrelationRejectsInvalidTraceContext(t *testing.T) {
	t.Parallel()
	var reservedFlagsTraceID string
	reservedFlags := requestIDMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		reservedFlagsTraceID = traceID(request.Context())
	}))
	reservedFlagsRequest := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/livez", nil)
	reservedFlagsRequest.Header.Set("Traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-02")
	reservedFlags.ServeHTTP(httptest.NewRecorder(), reservedFlagsRequest)
	if reservedFlagsTraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("valid trace context with reserved flags produced trace ID %q", reservedFlagsTraceID)
	}
	for name, traceparent := range map[string]string{
		"uppercase":      "00-4BF92F3577B34DA6A3CE929D0E0E4736-00f067aa0ba902b7-01",
		"zero trace":     "00-00000000000000000000000000000000-00f067aa0ba902b7-01",
		"zero parent":    "00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01",
		"future":         "01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"future ff":      "ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"malformed flag": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-0g",
		"suffix":         "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-private",
	} {
		t.Run(name, func(t *testing.T) {
			var captured string
			handler := requestIDMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
				captured = traceID(request.Context())
			}))
			request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/livez", nil)
			request.Header.Set("Traceparent", traceparent)
			handler.ServeHTTP(httptest.NewRecorder(), request)
			if captured != "" {
				t.Fatalf("invalid trace context produced trace ID %q", captured)
			}
		})
	}
}

func TestPanicRecoveryAbortsAfterACommittedNonResettableResponse(t *testing.T) {
	t.Parallel()
	handler := recoverPanics(slog.New(slog.DiscardHandler), http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusAccepted)
		_, _ = writer.Write([]byte(`{"partial":true}`))
		panic("after commit")
	}))
	request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/resources", nil)
	response := httptest.NewRecorder()
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		handler.ServeHTTP(response, request)
	}()
	recoveredErr, ok := recovered.(error)
	if !ok || !errors.Is(recoveredErr, http.ErrAbortHandler) {
		t.Fatalf("committed panic propagated %v", recovered)
	}
	if response.Code != http.StatusAccepted || response.Body.String() != `{"partial":true}` || strings.Contains(response.Body.String(), "requestId") {
		t.Fatalf("committed panic response = status %d, body %s", response.Code, response.Body.String())
	}
}

func TestPanicRecoveryPassesThroughAbortSentinel(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	handler := recoverPanics(slog.New(slog.NewTextHandler(&logs, nil)), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/resources", nil)
	response := httptest.NewRecorder()
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		handler.ServeHTTP(response, request)
	}()
	recoveredErr, ok := recovered.(error)
	if !ok || !errors.Is(recoveredErr, http.ErrAbortHandler) || logs.Len() != 0 || response.Body.Len() != 0 {
		t.Fatalf("abort sentinel = recovered %v, logs %q, body %q", recovered, logs.String(), response.Body.String())
	}
}

func TestIdempotencyCacheReleasesWaitersWhenHTTPAbortEscapes(t *testing.T) {
	t.Parallel()
	store := idempotency.NewMemoryStore()
	var invocations atomic.Int32
	next := recoverPanics(slog.New(slog.DiscardHandler), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		invocations.Add(1)
		panic(http.ErrAbortHandler)
	}))
	handler := newIdempotencyCache(store, slog.New(slog.DiscardHandler)).middleware(1024, next)
	newRequest := func() *http.Request {
		request := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/resources", nil)
		request.Header.Set("Idempotency-Key", "aborted-command")
		return request.WithContext(identity.WithActor(request.Context(), identity.Actor{TenantID: "tenant", Subject: "actor"}))
	}
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		handler.ServeHTTP(httptest.NewRecorder(), newRequest())
	}()
	if recoveredErr, ok := recovered.(error); !ok || !errors.Is(recoveredErr, http.ErrAbortHandler) {
		t.Fatalf("first command recovered = %v", recovered)
	}
	retry := httptest.NewRecorder()
	handler.ServeHTTP(retry, newRequest())
	if retry.Code != http.StatusServiceUnavailable || retry.Header().Get("Retry-After") != "1" || invocations.Load() != 1 {
		t.Fatalf("aborted retry = %d retry-after %q invocations %d", retry.Code, retry.Header().Get("Retry-After"), invocations.Load())
	}
}

func TestIdempotencyDigestBindsQueryPreconditionAndMediaType(t *testing.T) {
	store := idempotency.NewMemoryStore()
	var invocations atomic.Int32
	handler := newIdempotencyCache(store, slog.New(slog.DiscardHandler)).middleware(1024, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		invocations.Add(1)
		writer.WriteHeader(http.StatusCreated)
	}))
	serve := func(query, ifMatch, contentType string) *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/resources"+query, strings.NewReader(`{"name":"one"}`))
		request.Header.Set("Idempotency-Key", "bound-command")
		request.Header.Set("If-Match", ifMatch)
		request.Header.Set("Content-Type", contentType)
		request = request.WithContext(identity.WithActor(request.Context(), identity.Actor{TenantID: "tenant", Subject: "actor"}))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	if first := serve("?mode=one", `"rev-1"`, "application/json"); first.Code != http.StatusCreated {
		t.Fatalf("first command status = %d", first.Code)
	}
	if replay := serve("?mode=one", `"rev-1"`, "application/json"); replay.Code != http.StatusCreated || replay.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("exact replay = status %d, replay %q", replay.Code, replay.Header().Get("Idempotency-Replayed"))
	}
	if changed := serve("?mode=one", `"rev-2"`, "application/json"); changed.Code != http.StatusConflict {
		t.Fatalf("changed precondition status = %d", changed.Code)
	}
	if changed := serve("?mode=two", `"rev-1"`, "application/json"); changed.Code != http.StatusConflict {
		t.Fatalf("changed query status = %d", changed.Code)
	}
	if changed := serve("?mode=one", `"rev-1"`, "application/merge-patch+json"); changed.Code != http.StatusConflict {
		t.Fatalf("changed media type status = %d", changed.Code)
	}
	if invocations.Load() != 1 {
		t.Fatalf("handler invocations = %d, want 1", invocations.Load())
	}
}

func TestPOSTQueryIdempotencyClassification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		method   string
		path     string
		requires bool
	}{
		{method: http.MethodPost, path: "/api/v1/catalog/search"},
		{method: http.MethodPost, path: "/api/v1/catalog/explore"},
		{method: http.MethodPost, path: "/api/v1/observations:search"},
		{method: http.MethodPost, path: "/api/v1/federation/sources:live-search", requires: true},
		{method: http.MethodPost, path: "/api/v1/resources", requires: true},
		{method: http.MethodPatch, path: "/api/v1/federation/sources/source-id", requires: true},
		{method: http.MethodPost, path: "/api/v1/federation/sources/source-id:remove", requires: true},
		{method: http.MethodGet, path: "/api/v1/resources"},
		{method: http.MethodPost, path: "/ard/v1/search"},
	}
	for _, test := range tests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequestWithContext(context.Background(), test.method, test.path, nil)
			if got := requiresIdempotency(request); got != test.requires {
				t.Fatalf("requiresIdempotency() = %v, want %v", got, test.requires)
			}
		})
	}
}

func TestIdempotencyMiddlewareRejectsInvalidCommandEnvelopes(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.DiscardHandler)
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("invalid command reached the handler")
	})

	serve := func(cache *idempotencyCache, request *http.Request, maximum int64) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		cache.middleware(maximum, next).ServeHTTP(response, request)
		return response
	}
	request := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/resources", nil)
	request.Header.Set("Idempotency-Key", "command")
	if response := serve(newIdempotencyCache(idempotency.NewMemoryStore(), logger), request, 1024); response.Code != http.StatusForbidden {
		t.Fatalf("actor-free command status = %d", response.Code)
	}

	actor := identity.Actor{TenantID: "tenant", Subject: "actor"}
	for name, key := range map[string]string{"missing": "", "too long": strings.Repeat("x", 201), "line break": "valid\ninvalid"} {
		t.Run(name, func(t *testing.T) {
			invalid := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/resources", nil)
			invalid.Header["Idempotency-Key"] = []string{key}
			invalid = invalid.WithContext(identity.WithActor(invalid.Context(), actor))
			if response := serve(newIdempotencyCache(idempotency.NewMemoryStore(), logger), invalid, 1024); response.Code != http.StatusBadRequest {
				t.Fatalf("invalid key status = %d", response.Code)
			}
		})
	}

	oversized := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/resources", strings.NewReader("12345"))
	oversized.Header.Set("Idempotency-Key", "oversized")
	oversized = oversized.WithContext(identity.WithActor(oversized.Context(), actor))
	if response := serve(newIdempotencyCache(idempotency.NewMemoryStore(), logger), oversized, 4); response.Code != http.StatusBadRequest {
		t.Fatalf("oversized command status = %d", response.Code)
	}
	unreadable := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/resources", nil)
	unreadable.Body = failingRequestBody{err: errors.New("read failed")}
	unreadable.Header.Set("Idempotency-Key", "unreadable")
	unreadable = unreadable.WithContext(identity.WithActor(unreadable.Context(), actor))
	if response := serve(newIdempotencyCache(idempotency.NewMemoryStore(), logger), unreadable, 1024); response.Code != http.StatusBadRequest {
		t.Fatalf("unreadable command status = %d", response.Code)
	}

	reserveFailure := &failingIdempotencyStore{reserveErr: errors.New("reservation failed")}
	failed := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/resources", nil)
	failed.Header.Set("Idempotency-Key", "reserve-failure")
	failed = failed.WithContext(identity.WithActor(failed.Context(), actor))
	if response := serve(newIdempotencyCache(reserveFailure, logger), failed, 1024); response.Code != http.StatusInternalServerError {
		t.Fatalf("reservation failure status = %d", response.Code)
	}
}

func TestIdempotencyMiddlewareBoundsWaitsAndCompletionRetries(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		logger := slog.New(slog.DiscardHandler)
		actor := identity.Actor{TenantID: "tenant", Subject: "actor"}

		completionFailure := &failingIdempotencyStore{completeErr: errors.New("completion failed")}
		completionHandler := newIdempotencyCache(completionFailure, logger).middleware(1024, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		completionRequest := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/resources", nil)
		completionRequest.Header.Set("Idempotency-Key", "completion-failure")
		completionRequest = completionRequest.WithContext(identity.WithActor(completionRequest.Context(), actor))
		completionResponse := httptest.NewRecorder()
		completionHandler.ServeHTTP(completionResponse, completionRequest)
		if completionResponse.Code != http.StatusOK || completionFailure.completes.Load() != 3 {
			t.Fatalf("completion failure response = %d, attempts = %d", completionResponse.Code, completionFailure.completes.Load())
		}

		started := make(chan struct{})
		release := make(chan struct{})
		var invocations atomic.Int32
		cache := newIdempotencyCache(idempotency.NewMemoryStore(), logger)
		waitingHandler := cache.middleware(1024, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			invocations.Add(1)
			close(started)
			<-release
			writer.Header().Set("Location", "/api/v1/resources/one")
			writer.WriteHeader(http.StatusCreated)
			_, _ = writer.Write([]byte(`{"id":"one"}`))
		}))
		newRequest := func(ctx context.Context) *http.Request {
			ctx = identity.WithActor(ctx, actor)
			request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/resources", nil)
			request.Header.Set("Idempotency-Key", "concurrent-command")
			return request
		}
		firstDone := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			response := httptest.NewRecorder()
			waitingHandler.ServeHTTP(response, newRequest(context.Background()))
			firstDone <- response
		}()
		<-started
		changedRequest := httptest.NewRequestWithContext(identity.WithActor(context.Background(), actor), http.MethodPost, "/api/v1/resources", strings.NewReader(`{"different":true}`))
		changedRequest.Header.Set("Idempotency-Key", "concurrent-command")
		changed := httptest.NewRecorder()
		waitingHandler.ServeHTTP(changed, changedRequest)
		if changed.Code != http.StatusConflict || invocations.Load() != 1 {
			t.Fatalf("concurrent changed request = status %d, invocations %d", changed.Code, invocations.Load())
		}
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		second := httptest.NewRecorder()
		waitingHandler.ServeHTTP(second, newRequest(canceled))
		if second.Code != http.StatusServiceUnavailable {
			t.Fatalf("canceled replay wait status = %d", second.Code)
		}
		replayDone := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			response := httptest.NewRecorder()
			waitingHandler.ServeHTTP(response, newRequest(context.Background()))
			replayDone <- response
		}()
		// Prove the duplicate is waiting on the in-flight entry before allowing
		// completion to remove it from the local cache.
		synctest.Wait()
		select {
		case <-replayDone:
			t.Fatal("duplicate request returned before the original completed")
		default:
		}
		close(release)
		if first := <-firstDone; first.Code != http.StatusCreated {
			t.Fatalf("first concurrent command status = %d", first.Code)
		}
		replay := <-replayDone
		if replay.Code != http.StatusCreated || replay.Header().Get("Idempotency-Replayed") != "true" || replay.Header().Get("Location") != "/api/v1/resources/one" || replay.Body.String() != `{"id":"one"}` || invocations.Load() != 1 {
			t.Fatalf("concurrent replay = status %d, replay %q, location %q, body %q, invocations %d", replay.Code, replay.Header().Get("Idempotency-Replayed"), replay.Header().Get("Location"), replay.Body.String(), invocations.Load())
		}

		capture := newResponseCapture()
		if _, err := io.WriteString(capture, "body"); err != nil || capture.status != http.StatusOK {
			t.Fatalf("implicit capture status = %d, %v", capture.status, err)
		}
	})
}

func newAtomicTestCache(store idempotency.AtomicStore, logger *slog.Logger) *idempotencyCache {
	cache := newIdempotencyCache(nil, logger)
	cache.atomic = store
	return cache
}
