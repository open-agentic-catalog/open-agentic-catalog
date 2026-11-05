// Observation-group tests preserve exact lookup boundaries and reject partial reads.
package federation

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// The group reader projects already retained observations. Unexpected calls,
// including mutation or adoption, have no implementation in this narrow port.
type groupFailureStore struct {
	Store
	t      *testing.T
	member Observation
	fail   string
	err    error
	calls  []string
}

func (s *groupFailureStore) checkKey(tenant, identifier, version string, present bool) {
	s.t.Helper()
	if tenant != s.member.TenantID || identifier != s.member.Identifier || version != s.member.Version || present != s.member.VersionPresent {
		s.t.Fatal("group lookup lost tenant or exact typed identity")
	}
}

func (s *groupFailureStore) ListObservationGroupMembers(_ context.Context, tenant, identifier, version string, present bool, limit int) ([]Observation, error) {
	s.calls = append(s.calls, "members")
	s.checkKey(tenant, identifier, version, present)
	if limit != 101 {
		s.t.Fatalf("group selection limit=%d; want 100 plus overflow witness", limit)
	}
	if s.fail == "members" {
		return nil, s.err
	}
	if s.fail == "missing" {
		return nil, nil
	}
	return []Observation{s.member}, nil
}

func (s *groupFailureStore) ObservationGroupConflicts(_ context.Context, tenant, identifier, version string, present bool, sources []string) (bool, map[string]bool, error) {
	s.calls = append(s.calls, "conflicts")
	s.checkKey(tenant, identifier, version, present)
	if !reflect.DeepEqual(sources, []string{s.member.SourceID}) {
		s.t.Fatalf("unbounded conflict lookup: %v", sources)
	}
	if s.fail == "conflicts" {
		return false, nil, s.err
	}
	return false, map[string]bool{}, nil
}

func (s *groupFailureStore) ObservationOccurrenceSummaries(_ context.Context, tenant string, observations []string) (map[string]OccurrenceSummary, error) {
	s.calls = append(s.calls, "occurrence")
	if tenant != s.member.TenantID || !reflect.DeepEqual(observations, []string{s.member.ID}) {
		s.t.Fatal("occurrence lookup lost tenant or observation binding")
	}
	if s.fail == "omitted occurrence" {
		return map[string]OccurrenceSummary{}, nil
	}
	if s.fail == "zero occurrence" {
		return map[string]OccurrenceSummary{s.member.ID: {Latest: ObservationOccurrence{ObservationID: s.member.ID}}}, nil
	}
	if s.fail == "wrong occurrence" {
		return map[string]OccurrenceSummary{s.member.ID: {Total: 1, Latest: ObservationOccurrence{ObservationID: "other"}}}, nil
	}
	return nil, s.err
}

func TestObservationGroupRejectsInvalidKeysAndDependencyFailures(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"unauthorized", "invalid identifier", "empty present version", "nonempty absent version", "long version", "missing", "members", "conflicts", "occurrence", "omitted occurrence", "zero occurrence", "wrong occurrence"} {
		t.Run(mode, func(t *testing.T) {
			m, store, _, evidence, source, _ := newChangePreviewTest(t)
			member := Observation{ID: "observation", TenantID: source.TenantID, SourceID: source.ID, Identifier: "urn:air:source.example:default:agent", Version: "1", VersionPresent: true, Kind: catalog.KindAgent, NormalizedEntryDigest: "sha256:" + strings.Repeat("a", 64)}
			reader := &groupFailureStore{t: t, member: member, fail: mode, err: errors.New("group read failed")}
			m.store = reader
			ctx, identifier, version, present := auditEvidenceContext(), " "+member.Identifier+" ", member.Version, true
			wantCode := catalog.CodeInvalidArgument
			var wantCalls []string
			switch mode {
			case "unauthorized":
				ctx, wantCode = context.Background(), catalog.CodeForbidden
			case "invalid identifier":
				identifier = "https://source.example/agent"
			case "empty present version":
				version = ""
			case "nonempty absent version":
				present = false
			case "long version":
				version = strings.Repeat("x", 129)
			case "missing", "members":
				wantCalls, wantCode = []string{"members"}, catalog.CodeNotFound
			case "conflicts":
				wantCalls = []string{"members", "conflicts"}
			case "occurrence", "omitted occurrence", "zero occurrence", "wrong occurrence":
				wantCode = catalog.CodeDependencyUnavailable
				wantCalls = []string{"members", "conflicts", "occurrence"}
			}
			got, err := m.ObservationGroup(ctx, identifier, version, present)
			if mode == "members" || mode == "conflicts" || mode == "occurrence" {
				if !errors.Is(err, reader.err) {
					t.Fatalf("lost read cause: %v", err)
				}
			} else if catalog.ErrorCodeOf(err) != wantCode {
				t.Fatalf("error=%v; want %s", err, wantCode)
			}
			if !reflect.DeepEqual(got, ObservationGroup{}) || !reflect.DeepEqual(reader.calls, wantCalls) {
				t.Fatalf("partial group=%+v calls=%v; want %v", got, reader.calls, wantCalls)
			}
			assertUnchangedPreviewSource(t, store, source, evidence, 0)
		})
	}
}
