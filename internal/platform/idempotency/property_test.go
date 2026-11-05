package idempotency

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"pgregory.net/rapid"
)

// The model retains immutable values, not response slices or the store's keys.
// Its array position identifies one of five explicitly distinct command scopes.
type propertyCommand struct {
	digest, authorization string
	complete              bool
	status                int
	body, header          string
}

func (m propertyCommand) response() Response {
	return Response{Digest: m.digest, Status: m.status, Body: []byte(m.body), Headers: map[string][]string{
		"Content-Type": {"application/json"}, "X-Test": {m.header, "second-value"},
	}}
}

func propertyResponseEqual(got, want Response) bool {
	if got.Digest != want.Digest || got.Status != want.Status || !bytes.Equal(got.Body, want.Body) || len(got.Headers) != len(want.Headers) {
		return false
	}
	for name, values := range want.Headers {
		if !slices.Equal(got.Headers[name], values) {
			return false
		}
	}
	return true
}

func propertyMutateResponse(response Response) {
	response.Body[0] ^= 1
	response.Headers["X-Test"][0] = "caller-changed-value"
	response.Headers["Caller-Added"] = []string{"caller-changed-map"}
	delete(response.Headers, "Content-Type")
}

func TestPropertyMemoryIdempotency(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ctx := context.Background()
		store := NewMemoryStore()
		// Expiry and scheduling have existing examples/functional owners. Keeping
		// expiry far in the future makes these sequences independent of wall time.
		expires := time.Date(9999, time.January, 1, 0, 0, 0, 0, time.UTC)
		base := Scope{
			TenantID: fmt.Sprintf("abcdefab-0000-7000-8000-%012x", rapid.Uint64Range(1, 65535).Draw(t, "tenant")),
			Actor:    rapid.StringMatching(`[a-z]{1,12}`).Draw(t, "actor"),
			Route:    rapid.SampledFrom([]string{"POST /resources", "POST /namespaces"}).Draw(t, "route"),
			Key:      rapid.StringMatching(`[a-z0-9]{1,16}`).Draw(t, "key"),
		}
		scopes := [5]Scope{base, base, base, base, base}
		scopes[1].TenantID = "fedcbaab" + base.TenantID[8:]
		scopes[2].Actor += "-other"
		scopes[3].Route += "/other"
		scopes[4].Key += "-other"
		var model [5]*propertyCommand
		for step := range rapid.IntRange(1, 30).Draw(t, "steps") {
			slot := rapid.IntRange(0, len(scopes)-1).Draw(t, "scope")
			scope := scopes[slot]
			if rapid.Bool().Draw(t, "tenant casing") {
				scope.TenantID = strings.ToUpper(scope.TenantID)
			}
			entry := model[slot]
			if entry == nil {
				digest := rapid.SampledFrom([]string{"request-a", "request-b"}).Draw(t, "request binding")
				scope.AuthorizationDigest = rapid.SampledFrom([]string{"authorization-a", "authorization-b"}).Draw(t, "authorization binding")
				got, err := store.Reserve(ctx, scope, digest, expires)
				if err != nil || !got.Acquired || got.Pending || !propertyResponseEqual(got.Response, Response{}) {
					t.Fatalf("step %d acquire scope %d: %+v, %v", step, slot, got, err)
				}
				model[slot] = &propertyCommand{digest: digest, authorization: scope.AuthorizationDigest}
			} else {
				scope.AuthorizationDigest = entry.authorization
				actions := []string{"reserve", "request conflict", "authorization conflict", "unowned request completion", "unowned authorization completion"}
				if !entry.complete {
					actions = append(actions, "complete")
				}
				action := rapid.SampledFrom(actions).Draw(t, "action")
				switch action {
				case "complete":
					entry.status = rapid.SampledFrom([]int{200, 201, 202, 409}).Draw(t, "response status")
					entry.body = fmt.Sprintf(`{"value":%q}`, rapid.StringMatching(`[a-z0-9]{1,24}`).Draw(t, "response body"))
					entry.header = rapid.StringMatching(`[a-z]{1,12}`).Draw(t, "header value")
					response := entry.response()
					if err := store.Complete(ctx, scope, response); err != nil {
						t.Fatalf("step %d owned completion: %v", step, err)
					}
					entry.complete = true
					propertyMutateResponse(response)
				case "unowned request completion", "unowned authorization completion":
					response := Response{Digest: entry.digest, Status: 201}
					if action == "unowned request completion" {
						response.Digest += "-other"
					} else {
						scope.AuthorizationDigest += "-other"
					}
					if err := store.Complete(ctx, scope, response); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
						t.Fatalf("step %d %s: %v", step, action, err)
					}
				default:
					digest := entry.digest
					var wantCode catalog.ErrorCode
					if action == "request conflict" {
						digest += "-other"
						wantCode = catalog.CodeIdempotencyKeyReused
					}
					if action == "authorization conflict" {
						scope.AuthorizationDigest += "-other"
						wantCode = catalog.CodeForbidden
					}
					got, err := store.Reserve(ctx, scope, digest, expires)
					if wantCode != "" {
						if catalog.ErrorCodeOf(err) != wantCode || got.Acquired || got.Pending || !propertyResponseEqual(got.Response, Response{}) {
							t.Fatalf("step %d %s: %+v, %v", step, action, got, err)
						}
					} else {
						propertyCheckReservation(t, step, slot, got, err, *entry)
						if entry.complete {
							propertyMutateResponse(got.Response)
						}
					}
				}
			}
			// Observe every known reservation after each action. Do not probe absent
			// scopes: Reserve would create them and silently change the modeled state.
			for index, retained := range model {
				if retained == nil {
					continue
				}
				observedScope := scopes[index]
				observedScope.AuthorizationDigest = retained.authorization
				got, err := store.Reserve(ctx, observedScope, retained.digest, expires)
				propertyCheckReservation(t, step, index, got, err, *retained)
			}
		}
	})
}

func propertyCheckReservation(t *rapid.T, step, slot int, got Reservation, err error, entry propertyCommand) {
	want := Response{}
	if entry.complete {
		want = entry.response()
	}
	if err != nil || got.Acquired || got.Pending == entry.complete || !propertyResponseEqual(got.Response, want) {
		t.Fatalf("step %d retained scope %d: %+v, %v; complete=%v, response=%+v", step, slot, got, err, entry.complete, want)
	}
}
