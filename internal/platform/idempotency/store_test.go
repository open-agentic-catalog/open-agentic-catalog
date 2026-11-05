package idempotency

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestCommandAbortHookIsScopedAndOptional(t *testing.T) {
	t.Parallel()
	want := errors.New("abort")
	var got error
	ctx := WithCommandAbort(context.Background(), func(err error) { got = err })
	AbortCommand(ctx, want)
	if !errors.Is(got, want) {
		t.Fatalf("abort hook received %v", got)
	}
	AbortCommand(context.Background(), want)
}

func TestMemoryStoreReserveCompleteReplayAndConflict(t *testing.T) {
	store := NewMemoryStore()
	scope := Scope{TenantID: "tenant", Actor: "actor", Route: "POST /resources", Key: "one"}
	reservation, err := store.Reserve(context.Background(), scope, "digest-a", time.Now().Add(time.Hour))
	if err != nil || !reservation.Acquired {
		t.Fatalf("reserve = %#v, %v", reservation, err)
	}
	reservation, err = store.Reserve(context.Background(), scope, "digest-a", time.Now().Add(time.Hour))
	if err != nil || !reservation.Pending {
		t.Fatalf("pending = %#v, %v", reservation, err)
	}
	response := Response{Digest: "digest-a", Status: 201, Headers: map[string][]string{"Location": {"/resources/one"}}, Body: []byte(`{"id":"one"}`)}
	if err := store.Complete(context.Background(), scope, response); err != nil {
		t.Fatal(err)
	}
	reservation, err = store.Reserve(context.Background(), scope, "digest-a", time.Now().Add(time.Hour))
	if err != nil || reservation.Response.Status != 201 || string(reservation.Response.Body) != string(response.Body) {
		t.Fatalf("replay = %#v, %v", reservation, err)
	}
	_, err = store.Reserve(context.Background(), scope, "digest-b", time.Now().Add(time.Hour))
	if catalog.ErrorCodeOf(err) != catalog.CodeIdempotencyKeyReused {
		t.Fatalf("digest conflict returned %v", err)
	}
	changedAuthorization := scope
	changedAuthorization.AuthorizationDigest = "sha256:changed"
	if _, err := store.Reserve(context.Background(), changedAuthorization, "digest-a", time.Now().Add(time.Hour)); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatalf("authorization change returned %v", err)
	}
}

func TestMemoryStoreRejectsCompletionWithoutMatchingReservation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		reserved      bool
		digest        string
		authorization string
	}{
		// Zero bindings isolate the missing-entry check from digest mismatches.
		{name: "missing reservation"},
		{name: "different request", reserved: true, digest: "digest-b", authorization: "authorization-a"},
		{name: "different authorization", reserved: true, digest: "digest-a", authorization: "authorization-b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := NewMemoryStore()
			ctx := t.Context()
			scope := Scope{TenantID: "tenant", Actor: "actor", Route: "POST /resources", Key: "one", AuthorizationDigest: "authorization-a"}
			expiresAt := time.Now().Add(time.Hour)
			if tc.reserved {
				if reservation, err := store.Reserve(ctx, scope, "digest-a", expiresAt); err != nil || !reservation.Acquired {
					t.Fatalf("initial reservation = %#v, %v", reservation, err)
				}
			}
			completionScope := scope
			completionScope.AuthorizationDigest = tc.authorization
			if err := store.Complete(ctx, completionScope, Response{Digest: tc.digest, Status: 201}); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
				t.Fatalf("unowned completion returned %v, want conflict", err)
			}
			// Rejection must leave an existing reservation pending and must not
			// create a reservation when none existed.
			reservation, err := store.Reserve(ctx, scope, "digest-a", expiresAt)
			if err != nil || reservation.Acquired != !tc.reserved || reservation.Pending != tc.reserved || reservation.Response.Status != 0 {
				t.Fatalf("reservation after rejected completion = %#v, %v", reservation, err)
			}
		})
	}
}

func TestAdvisoryLockKeyBindsEveryScopeFieldWithoutDelimiterAmbiguity(t *testing.T) {
	t.Parallel()
	base := Scope{TenantID: "tenant", Actor: "actor", Route: "POST /resources", Key: "one"}
	want := AdvisoryLockKey(base)
	if want != AdvisoryLockKey(base) {
		t.Fatal("advisory lock key is not deterministic")
	}
	for _, changed := range []Scope{
		{TenantID: "other", Actor: base.Actor, Route: base.Route, Key: base.Key},
		{TenantID: base.TenantID, Actor: "other", Route: base.Route, Key: base.Key},
		{TenantID: base.TenantID, Actor: base.Actor, Route: "PATCH /resources/one", Key: base.Key},
		{TenantID: base.TenantID, Actor: base.Actor, Route: base.Route, Key: "two"},
		{TenantID: "a", Actor: "bc", Route: "d", Key: "e"},
		{TenantID: "ab", Actor: "c", Route: "d", Key: "e"},
	} {
		if got := AdvisoryLockKey(changed); got == want {
			t.Fatalf("changed scope %#v reused the base lock key", changed)
		}
	}
	changedAuthorization := base
	changedAuthorization.AuthorizationDigest = "sha256:changed"
	if AdvisoryLockKey(changedAuthorization) != want {
		t.Fatal("authorization changes created a second command lock lane")
	}
	uuidScope := Scope{TenantID: "abcdefab-cdef-7abc-8def-abcdefabcdef", Actor: base.Actor, Route: base.Route, Key: base.Key}
	upperTenant := uuidScope
	upperTenant.TenantID = "ABCDEFAB-CDEF-7ABC-8DEF-ABCDEFABCDEF"
	if AdvisoryLockKey(uuidScope) != AdvisoryLockKey(upperTenant) {
		t.Fatal("equivalent UUID tenant casing created a second command lock lane")
	}
}

func TestMemoryStoreCanonicalizesUUIDTenantCasing(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()
	lower := Scope{TenantID: "abcdefab-cdef-7abc-8def-abcdefabcdef", Actor: "actor", Route: "POST /resources", Key: "one", AuthorizationDigest: "authorization"}
	upper := lower
	upper.TenantID = "ABCDEFAB-CDEF-7ABC-8DEF-ABCDEFABCDEF"
	if reservation, err := store.Reserve(context.Background(), lower, "digest", time.Now().Add(time.Hour)); err != nil || !reservation.Acquired {
		t.Fatalf("lower-case reservation = %#v, %v", reservation, err)
	}
	if err := store.Complete(context.Background(), lower, Response{Digest: "digest", Status: 201}); err != nil {
		t.Fatal(err)
	}
	if reservation, err := store.Reserve(context.Background(), upper, "digest", time.Now().Add(time.Hour)); err != nil || reservation.Response.Status != 201 {
		t.Fatalf("upper-case replay = %#v, %v", reservation, err)
	}
}
