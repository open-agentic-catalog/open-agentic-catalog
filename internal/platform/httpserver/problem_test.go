package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
)

func TestARDProblemsClassifyFailuresWithoutDisclosingInternalCauses(t *testing.T) {
	for _, test := range []struct {
		name    string
		err     error
		status  int
		code    catalog.ErrorCode
		message string
	}{
		{
			name: "unclassified error", err: errors.New("private unclassified failure"),
			status: http.StatusInternalServerError, code: catalog.CodeInternal,
			message: "The service could not complete the request. Use the request ID to inspect safe diagnostics.",
		},
		{
			name:   "publication unavailable",
			err:    catalog.E(catalog.CodePublicationNotReady, "publication is not ready"),
			status: http.StatusServiceUnavailable, code: catalog.CodePublicationNotReady,
			message: "publication is not ready",
		},
		{
			name:   "wrapped deadline",
			err:    fmt.Errorf("private upstream location: %w", context.DeadlineExceeded),
			status: http.StatusServiceUnavailable, code: catalog.CodeRequestDeadlineExceeded,
			message: "the request exceeded the service work deadline",
		},
		{
			name:   "dependency failure",
			err:    catalog.Wrap(catalog.CodeDependencyUnavailable, "private database address", errors.New("private credentials")),
			status: http.StatusServiceUnavailable, code: catalog.CodeDependencyUnavailable,
			message: "The service could not complete the request. Use the request ID to inspect safe diagnostics.",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			for _, name := range []string{"Content-Digest", "Content-Disposition", "ETag", "Last-Modified", "Link", "Location", "Server-Timing"} {
				response.Header().Set(name, "stale-success-metadata")
			}
			response.Header().Set("X-Request-ID", "safe-request")
			writeARDProblem(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/ard/v1/search", nil), test.err)
			var body map[string]string
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if response.Code != test.status || len(body) != 2 || body["errorCode"] != string(test.code) || body["message"] != test.message {
				t.Fatalf("ARD problem = status %d body %s", response.Code, response.Body.String())
			}
			for _, name := range []string{"Content-Digest", "Content-Disposition", "ETag", "Last-Modified", "Link", "Location", "Server-Timing"} {
				if response.Header().Get(name) != "" {
					t.Errorf("error retained success header %s", name)
				}
			}
			if response.Header().Get("Content-Type") != "application/json" || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Content-Length") != strconv.Itoa(response.Body.Len()) || response.Header().Get("X-Request-ID") != "safe-request" {
				t.Fatalf("unexpected ARD problem headers: %v", response.Header())
			}
		})
	}
}

func TestIdentityErrorMappingPreservesClassificationAndCauses(t *testing.T) {
	for _, test := range []struct {
		identityCode identity.ErrorCode
		catalogCode  catalog.ErrorCode
	}{
		{identity.CodeInvalidArgument, catalog.CodeInvalidArgument},
		{identity.CodeUnauthenticated, catalog.CodeUnauthenticated},
		{identity.CodeForbidden, catalog.CodeForbidden},
		{identity.CodeNotFound, catalog.CodeNotFound},
		{identity.CodeConflict, catalog.CodeConflict},
		{identity.CodePreconditionFailed, catalog.CodePreconditionFailed},
		{identity.CodeSeparationOfDuties, catalog.CodeSeparationOfDuties},
		{identity.CodeDependency, catalog.CodeDependencyUnavailable},
		{identity.CodeInternal, catalog.CodeInternal},
	} {
		t.Run(string(test.identityCode), func(t *testing.T) {
			cause := errors.New("private identity storage failure")
			original := identity.Wrap(test.identityCode, "safe identity message", cause)
			mapped := catalogIdentityError(original)
			var domain *catalog.DomainError
			if !errors.As(mapped, &domain) || domain.Code != test.catalogCode || domain.Message != "safe identity message" || !errors.Is(mapped, original) || !errors.Is(mapped, cause) {
				t.Fatalf("identity mapping = %#v", mapped)
			}
		})
	}
	original := fmt.Errorf("wrapped catalog failure: %w", catalog.E(catalog.CodeForbidden, "safe denial"))
	if mapped := catalogIdentityError(original); !errors.Is(mapped, original) || catalog.ErrorCodeOf(mapped) != catalog.CodeForbidden || mapped.Error() != original.Error() {
		t.Fatalf("existing Catalog error replaced: %v", mapped)
	}
	unclassified := errors.New("private unclassified identity failure")
	if mapped := catalogIdentityError(unclassified); catalog.ErrorCodeOf(mapped) != catalog.CodeInternal || !errors.Is(mapped, unclassified) {
		t.Fatalf("unclassified identity mapping = %v", mapped)
	}
}

func TestHTTPProblemStatusAndSafeUnknownError(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status int
		code   catalog.ErrorCode
		title  string
		detail string
	}{
		{"unknown error", errors.New("private unclassified failure"), http.StatusInternalServerError, catalog.CodeInternal, "Internal error", "The service could not complete the request. Use the request ID to inspect safe diagnostics."},
		{"unsupported profile", catalog.Wrap(catalog.CodeUnsupportedProfile, "profile is unsupported", errors.New("private profile cause")), http.StatusUnprocessableEntity, catalog.CodeUnsupportedProfile, "Unsupported profile", "profile is unsupported"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequestWithContext(context.WithValue(t.Context(), requestIDKey{}, "safe-request"), http.MethodGet, "/api/v1/example", nil)
			response := httptest.NewRecorder()
			writeProblem(response, request, test.err)
			var problem Problem
			if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
				t.Fatal(err)
			}
			if response.Code != test.status || problem.Status != test.status || problem.Code != test.code || problem.Title != test.title || problem.Detail != test.detail || problem.RequestID != "safe-request" || problem.Instance != request.URL.Path || strings.Contains(response.Body.String(), "private") {
				t.Fatalf("problem = %d %s", response.Code, response.Body.String())
			}
			if response.Header().Get("Content-Type") != "application/problem+json" || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("problem headers = %v", response.Header())
			}
		})
	}
	// Unknown codes are not part of the public contract; the status helper
	// must still classify them as internal failures.
	if status, title := problemStatus(catalog.ErrorCode("UNREGISTERED")); status != http.StatusInternalServerError || title != "Internal error" {
		t.Fatalf("unknown code status/title = %d/%q", status, title)
	}
}

func TestHTTPOutcomeStatusBoundaries(t *testing.T) {
	for _, test := range []struct {
		status int
		want   observability.Outcome
	}{
		{http.StatusOK, observability.OutcomeSucceeded},
		{399, observability.OutcomeSucceeded},
		{http.StatusBadRequest, observability.OutcomeRejected},
		{499, observability.OutcomeRejected},
		{http.StatusInternalServerError, observability.OutcomeFailed},
		{http.StatusServiceUnavailable, observability.OutcomeFailed},
	} {
		if got := httpOutcome(test.status); got != test.want {
			t.Errorf("status %d outcome = %q, want %q", test.status, got, test.want)
		}
	}
}
