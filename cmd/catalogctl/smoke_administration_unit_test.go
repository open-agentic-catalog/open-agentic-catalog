// Administration-client tests exercise response validation without a Catalog,
// database, listener or simulated server-side state transitions.
package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// administrationRejection changes one response or interrupts its transport.
type administrationRejection struct {
	name, step, body, removeHeader, etag, want string
	replace                                    [2]string
	transport                                  bool
	status                                     int
}

// checkAdministrationResponses verifies request bindings, bounded continuation
// and error propagation against static responses for the two smoke helpers.
func checkAdministrationResponses(t *testing.T, steps []smokeClientStep, cases []administrationRejection, run func(*smokeHTTPClient) error) {
	t.Helper()
	transportFailure := errors.New("controlled administration transport failure")
	for _, step := range steps {
		cases = append(cases,
			administrationRejection{name: step.name + " transport", step: step.name, transport: true},
			administrationRejection{name: step.name + " status", step: step.name, status: http.StatusBadGateway, want: "check:"},
		)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stop := len(steps) - 1
			if tc.step != "" {
				stop = -1
				for i, step := range steps {
					if step.name == tc.step {
						stop = i
					}
				}
				if stop < 0 {
					t.Fatal("rejection has no matching request")
				}
			}
			calls := 0
			var firstResourceBody map[string]any
			client := &smokeHTTPClient{baseURL: "https://catalog.invalid", client: &http.Client{Transport: catalogctlRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				i := calls
				calls++
				if i > stop {
					t.Errorf("issued request after rejection: %s", request.URL.RequestURI())
					return nil, transportFailure
				}
				step := steps[i]
				if request.Method != step.method || request.URL.RequestURI() != step.path || request.Header.Get("Authorization") != "Bearer "+step.token || request.Header.Get("Idempotency-Key") != step.key || request.Header.Get("If-Match") != step.match {
					t.Fatalf("request %d lost method, path, cursor, actor, command key or revision binding: %s %s", i, request.Method, request.URL.RequestURI())
				}
				if request.Method == http.MethodPost {
					var body map[string]any
					if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					switch step.name {
					case "grant":
						if body["subject"] != "unit-temporary-reader" || body["actorType"] != "human" || body["role"] != "reader" || body["reason"] != "Exercise membership administration" {
							t.Fatalf("incorrect disposable grant: %#v", body)
						}
					case "resource":
						firstResourceBody = body
						if body["namespaceId"] != "namespace" || body["kind"] != "skill" || body["canonicalIdentifier"] != "urn:example:open-agentic-catalog:unit:discarded-skill" {
							t.Fatalf("incorrect draft fixture: %#v", body)
						}
					case "reuse":
						if !reflect.DeepEqual(body, firstResourceBody) {
							t.Fatalf("identifier reuse changed the Resource: %#v", body)
						}
					}
				}
				header := step.header.Clone()
				if i == stop && tc.step != "" {
					if tc.transport {
						return nil, transportFailure
					}
					if tc.body != "" {
						step.body = tc.body
					}
					if tc.replace[0] != "" {
						if !strings.Contains(step.body, tc.replace[0]) {
							t.Fatal("rejection does not change the response")
						}
						step.body = strings.ReplaceAll(step.body, tc.replace[0], tc.replace[1])
					}
					header.Del(tc.removeHeader)
					if tc.etag != "" {
						if header == nil {
							header = make(http.Header)
						}
						header.Set("ETag", tc.etag)
					}
					if tc.status != 0 {
						step.status = tc.status
					}
				}
				return &http.Response{StatusCode: step.status, Header: header, Body: io.NopCloser(strings.NewReader(step.body)), Request: request}, nil
			})}}
			err := run(client)
			if calls != stop+1 {
				t.Fatalf("requests=%d, want %d", calls, stop+1)
			}
			if tc.step == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || (tc.transport && !errors.Is(err, transportFailure)) || (!tc.transport && !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("rejection error=%v, want %q", err, tc.want)
			}
			passedRequests := calls
			if tc.transport || tc.status != 0 {
				passedRequests--
			}
			if len(client.checks) != passedRequests {
				t.Fatalf("status checks=%d, want %d", len(client.checks), passedRequests)
			}
		})
	}
}

// TestSmokeMembershipAdministrationRejectsResponses protects pagination,
// lifecycle evidence and cross-tenant denial at the command client boundary.
func TestSmokeMembershipAdministrationRejectsResponses(t *testing.T) {
	t.Parallel()
	options := smokeOptions{RunID: "unit", AdminToken: "administrator", OtherTenantToken: "other-tenant"}
	steps := []smokeClientStep{
		{"grant", "POST", "/api/v1/role-assignments", options.AdminToken, "unit-membership-grant", "", 201, nil, `{"id":"grant","membershipId":"member","subject":"unit-temporary-reader","revision":1}`},
		{"first page", "GET", "/api/v1/memberships?limit=1", options.AdminToken, "", "", 200, nil, `{"items":[{"id":"first"}],"nextCursor":"next+/="}`},
		{"second page", "GET", "/api/v1/memberships?limit=1&cursor=next%2B%2F%3D", options.AdminToken, "", "", 200, nil, `{"items":[{"id":"second"}]}`},
		{"read", "GET", "/api/v1/memberships/member", options.AdminToken, "", "", 200, http.Header{"Etag": {`"rev-1"`}}, `{"id":"member","subject":"unit-temporary-reader","realmId":"realm","state":"active","revision":1}`},
		{"filtered", "GET", "/api/v1/memberships?limit=1&subject=unit-temporary-reader&state=active&actorType=human&realmId=realm", options.AdminToken, "", "", 200, nil, `{"items":[{"id":"member"}]}`},
		{"contributions", "GET", "/api/v1/memberships/member/access", options.AdminToken, "", "", 200, nil, `{"membership":{"id":"member"},"groupCoverage":"current_realm","items":[{"assignment":{"id":"grant","membershipId":"member","state":"active"},"role":{"key":"reader"},"effective":true}]}`},
		{"foreign contributions", "GET", "/api/v1/memberships/member/access", options.OtherTenantToken, "", "", 404, nil, `{}`},
		{"suspend", "POST", "/api/v1/memberships/member:suspend", options.AdminToken, "unit-membership-suspend", `"rev-1"`, 200, http.Header{"Etag": {`"rev-2"`}}, `{"membership":{"state":"suspended","revision":2},"revokedRoleAssignments":0}`},
		{"reactivate", "POST", "/api/v1/memberships/member:reactivate", options.AdminToken, "unit-membership-reactivate", `"rev-2"`, 200, http.Header{"Etag": {`"rev-3"`}}, `{"membership":{"state":"active","revision":3},"revokedRoleAssignments":0}`},
		{"offboard", "POST", "/api/v1/memberships/member:offboard", options.AdminToken, "unit-membership-offboard", `"rev-3"`, 200, http.Header{"Etag": {`"rev-4"`}}, `{"membership":{"state":"offboarded","revision":4},"revokedRoleAssignments":1}`},
		{"final read", "GET", "/api/v1/memberships/member", options.AdminToken, "", "", 200, nil, `{"state":"offboarded","revision":4}`},
		{"cross tenant", "GET", "/api/v1/memberships/member", options.OtherTenantToken, "", "", 404, nil, `{}`},
	}
	cases := make([]administrationRejection, 0, 64)
	cases = append(cases,
		administrationRejection{name: "success"},
		administrationRejection{name: "grant ID", step: "grant", replace: [2]string{`"membershipId":"member"`, `"membershipId":""`}, want: "unexpected assignment"},
		administrationRejection{name: "grant subject", step: "grant", replace: [2]string{"unit-temporary-reader", "wrong-subject"}, want: "unexpected assignment"},
		administrationRejection{name: "grant revision", step: "grant", replace: [2]string{`"revision":1`, `"revision":2`}, want: "unexpected assignment"},
		administrationRejection{name: "empty first page", step: "first page", body: `{"items":[],"nextCursor":"next"}`, want: "unexpected page"},
		administrationRejection{name: "oversized first page", step: "first page", body: `{"items":[{"id":"first"},{"id":"second"}],"nextCursor":"next"}`, want: "unexpected page"},
		administrationRejection{name: "absent cursor", step: "first page", replace: [2]string{"next+/=", ""}, want: "unexpected page"},
		administrationRejection{name: "empty second page", step: "second page", body: `{"items":[]}`, want: "unexpected page"},
		administrationRejection{name: "oversized second page", step: "second page", body: `{"items":[{"id":"second"},{"id":"third"}]}`, want: "unexpected page"},
		administrationRejection{name: "repeated page", step: "second page", replace: [2]string{"second", "first"}, want: "unexpected page"},
		administrationRejection{name: "read ID", step: "read", replace: [2]string{`"id":"member"`, `"id":"other"`}, want: "unexpected membership"},
		administrationRejection{name: "read subject", step: "read", replace: [2]string{"unit-temporary-reader", "other"}, want: "unexpected membership"},
		administrationRejection{name: "read ETag", step: "read", removeHeader: "ETag", want: "unexpected membership"},
		administrationRejection{name: "filtered target", step: "filtered", replace: [2]string{`"id":"member"`, `"id":"other"`}, want: "does not match"},
		administrationRejection{name: "filtered continuation", step: "filtered", body: `{"items":[{"id":"member"}],"nextCursor":"unexpected"}`, want: "does not match"},
		administrationRejection{name: "contribution target", step: "contributions", replace: [2]string{`"id":"member"`, `"id":"other"`}, want: "does not match"},
		administrationRejection{name: "contribution scope", step: "contributions", replace: [2]string{`"current_realm"`, `"unavailable"`}, want: "does not match"},
		administrationRejection{name: "contribution eligibility", step: "contributions", replace: [2]string{`"effective":true`, `"effective":false`}, want: "does not match"},
		administrationRejection{name: "contribution assignment", step: "contributions", replace: [2]string{`"id":"grant"`, `"id":"other"`}, want: "does not match"},
		administrationRejection{name: "contribution role", step: "contributions", replace: [2]string{`"reader"`, `"contributor"`}, want: "does not match"},
		administrationRejection{name: "final state", step: "final read", replace: [2]string{"offboarded", "active"}, want: "unexpected membership"},
		administrationRejection{name: "final revision", step: "final read", replace: [2]string{`"revision":4`, `"revision":3`}, want: "unexpected membership"},
		administrationRejection{name: "cross tenant disclosed", step: "cross tenant", status: 200, want: "deny cross-tenant membership read check:"},
	)
	for _, step := range steps {
		if step.status >= 400 {
			continue
		}
		cases = append(cases, administrationRejection{name: step.name + " JSON", step: step.name, body: `[`, want: "invalid response"})
	}
	for _, step := range []string{"suspend", "reactivate", "offboard"} {
		cases = append(cases,
			administrationRejection{name: step + " state", step: step, replace: [2]string{`"state":"`, `"state":"wrong-`}, want: "unexpected result"},
			administrationRejection{name: step + " revision", step: step, replace: [2]string{`"revision":`, `"revision":9`}, want: "unexpected result"},
			administrationRejection{name: step + " revoked", step: step, replace: [2]string{`"revokedRoleAssignments":`, `"revokedRoleAssignments":9`}, want: "unexpected result"},
			administrationRejection{name: step + " ETag", step: step, removeHeader: "ETag", want: "unexpected result"},
			administrationRejection{name: step + " stale ETag", step: step, etag: `"rev-1"`, want: "unexpected result"},
		)
	}
	checkAdministrationResponses(t, steps, cases, func(client *smokeHTTPClient) error {
		return smokeExerciseMembershipAdministration(t.Context(), client, options)
	})
}

// TestSmokeDraftDeletionRejectsResponses verifies that failed evidence stops
// deletion, and that hidden descendants and reserved identifiers are required.
func TestSmokeDraftDeletionRejectsResponses(t *testing.T) {
	t.Parallel()
	options := smokeOptions{RunID: "unit", ContributorToken: "contributor", ReaderToken: "reader"}
	steps := []smokeClientStep{
		{"resource", "POST", "/api/v1/resources", options.ContributorToken, "unit-draft-resource", "", 201, http.Header{"Etag": {`"rev-1"`}}, `{"id":"resource"}`},
		{"version", "POST", "/api/v1/resources/resource/versions", options.ContributorToken, "unit-draft-version", "", 201, nil, `{"revision":{"id":"revision","resourceVersionId":"version","representations":[{"artifactId":"document"}]}}`},
		{"delete", "POST", "/api/v1/resources/resource:delete", options.ContributorToken, "unit-draft-delete", `"rev-1"`, 204, nil, ""},
		{"hidden resource", "GET", "/api/v1/resources/resource", options.ReaderToken, "", "", 404, nil, `{}`},
		{"hidden version", "GET", "/api/v1/resource-versions/version", options.ReaderToken, "", "", 404, nil, `{}`},
		{"hidden revision", "GET", "/api/v1/version-revisions/revision", options.ReaderToken, "", "", 404, nil, `{}`},
		{"hidden document", "GET", "/api/v1/artifacts/document/content", options.ReaderToken, "", "", 404, nil, `{}`},
		{"reuse", "POST", "/api/v1/resources", options.ContributorToken, "unit-draft-reuse", "", 409, nil, `{}`},
	}
	cases := make([]administrationRejection, 0, 64)
	cases = append(cases,
		administrationRejection{name: "success"},
		administrationRejection{name: "resource JSON", step: "resource", body: `[`, want: "invalid response"},
		administrationRejection{name: "resource ID", step: "resource", body: `{}`, want: "unexpected Resource"},
		administrationRejection{name: "resource ETag", step: "resource", removeHeader: "ETag", want: "unexpected Resource"},
		administrationRejection{name: "resource stale ETag", step: "resource", etag: `"rev-2"`, want: "unexpected Resource"},
		administrationRejection{name: "version JSON", step: "version", body: `[`, want: "invalid response"},
		administrationRejection{name: "revision ID", step: "version", replace: [2]string{`"id":"revision"`, `"id":""`}, want: "unexpected revision"},
		administrationRejection{name: "version ID", step: "version", replace: [2]string{`"resourceVersionId":"version"`, `"resourceVersionId":""`}, want: "unexpected revision"},
		administrationRejection{name: "no representations", step: "version", replace: [2]string{`[{"artifactId":"document"}]`, `[]`}, want: "unexpected revision"},
		administrationRejection{name: "extra representation", step: "version", replace: [2]string{`[{"artifactId":"document"}]`, `[{"artifactId":"document"},{"artifactId":"extra"}]`}, want: "unexpected revision"},
		administrationRejection{name: "document ID", step: "version", replace: [2]string{`"artifactId":"document"`, `"artifactId":""`}, want: "unexpected revision"},
		administrationRejection{name: "identifier reused", step: "reuse", status: 201, want: "reserve deleted draft canonical identifier check:"},
	)
	for _, step := range steps[3:7] {
		cases = append(cases, administrationRejection{name: step.name + " disclosed", step: step.name, status: 200, want: "check:"})
	}
	checkAdministrationResponses(t, steps, cases, func(client *smokeHTTPClient) error {
		return smokeExerciseDraftDeletion(t.Context(), client, options, "namespace")
	})
}
