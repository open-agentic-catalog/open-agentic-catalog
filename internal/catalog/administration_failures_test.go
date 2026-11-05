package catalog

import (
	"errors"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAdministrativePatchPreparationPreservesStoredState(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"namespace", "publisher"} {
		for _, mode := range []string{"missing reason", "missing record", "blank name", "long name", "invalid secondary field", "audit ID failure"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				f := newGuardrailFixture(t)
				namespace, err := f.service.CreateNamespace(f.adminContext(), CreateNamespaceInput{Slug: "example", DisplayName: "Example"})
				if err != nil {
					t.Fatal(err)
				}
				publisher, err := f.service.CreatePublisher(f.adminContext(), CreatePublisherInput{DisplayName: "Publisher", Domains: []string{"example.com"}})
				if err != nil {
					t.Fatal(err)
				}
				beforeAudit, beforeOutbox := len(f.repo.audit), len(f.repo.outbox)
				name, description, domains, reason := "Changed", "Changed description", []string{"other.example.com"}, "Reviewed metadata correction"
				namespaceID, publisherID := namespace.ID, publisher.ID
				want := CodeInvalidArgument
				sentinel := errors.New("audit identifier unavailable")
				switch mode {
				case "missing reason":
					reason = ""
				case "missing record":
					namespaceID, publisherID, want = "00000000-0000-7000-8000-000000000090", "00000000-0000-7000-8000-000000000090", CodeNotFound
				case "blank name":
					name = " "
				case "long name":
					name = strings.Repeat("x", 161)
				case "invalid secondary field":
					description, domains = strings.Repeat("x", 2001), []string{}
				case "audit ID failure":
					f.service.ids = auditIDFunc(func(time.Time) (string, error) { return "", sentinel })
					want = CodeInternal
				}
				if kind == "namespace" {
					_, err = f.service.UpdateNamespace(f.adminContext(), namespaceID, PatchNamespaceInput{DisplayName: &name, Description: &description, Reason: reason}, 1)
				} else {
					_, err = f.service.UpdatePublisher(f.adminContext(), publisherID, PatchPublisherInput{DisplayName: &name, Domains: &domains, Reason: reason}, 1)
				}
				if ErrorCodeOf(err) != want || mode == "audit ID failure" && !errors.Is(err, sentinel) {
					t.Fatalf("patch returned %v, want %s", err, want)
				}
				if !reflect.DeepEqual(f.repo.namespaces[namespace.ID], namespace) || !reflect.DeepEqual(f.repo.publishers[publisher.ID], publisher) || len(f.repo.audit) != beforeAudit || len(f.repo.outbox) != beforeOutbox {
					t.Fatal("failed preparation changed metadata or evidence")
				}
			})
		}
	}
}

func TestAdministrativeCreationIDFailuresLeaveNoRecords(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"namespace", "publisher", "resource"} {
		for failAt := 1; failAt <= 3; failAt++ {
			t.Run(fmt.Sprintf("%s/%d", kind, failAt), func(t *testing.T) {
				f := newGuardrailFixture(t)
				var namespace Namespace
				if kind == "resource" {
					var err error
					namespace, err = f.service.CreateNamespace(f.adminContext(), CreateNamespaceInput{Slug: "example", DisplayName: "Example"})
					if err != nil {
						t.Fatal(err)
					}
				}
				beforeNamespaces, beforeAudit, beforeOutbox := len(f.repo.namespaces), len(f.repo.audit), len(f.repo.outbox)
				calls := 0
				sentinel := errors.New("creation identifier unavailable")
				f.service.ids = auditIDFunc(func(time.Time) (string, error) {
					calls++
					if calls == failAt {
						return "", sentinel
					}
					return fmt.Sprintf("00000000-0000-7000-8000-%012d", 100+calls), nil
				})
				var err error
				switch kind {
				case "namespace":
					_, err = f.service.CreateNamespace(f.adminContext(), CreateNamespaceInput{Slug: "example", DisplayName: "Example"})
				case "publisher":
					_, err = f.service.CreatePublisher(f.adminContext(), CreatePublisherInput{DisplayName: "Example", Domains: []string{"example.com"}})
				case "resource":
					_, err = f.service.CreateResource(f.adminContext(), CreateResourceInput{NamespaceID: namespace.ID, Kind: KindAgent, CanonicalIdentifier: "urn:air:example.com:default:agent"})
				}
				if !errors.Is(err, sentinel) || ErrorCodeOf(err) != CodeInternal || calls != failAt {
					t.Fatalf("identifier failure=%v calls=%d want=%d", err, calls, failAt)
				}
				if len(f.repo.namespaces) != beforeNamespaces || len(f.repo.resources) != 0 || len(f.repo.publishers) != 0 || len(f.repo.audit) != beforeAudit || len(f.repo.outbox) != beforeOutbox {
					t.Fatal("failed creation retained partial records or evidence")
				}
			})
		}
	}
}

func TestAdministrativeReadsAndWritesKeepAuthorityChecks(t *testing.T) {
	t.Parallel()
	f := newGuardrailFixture(t)
	for _, read := range []func() error{
		func() error { _, err := f.service.GetNamespace(f.readerContext(), "invalid"); return err },
		func() error { _, err := f.service.GetPublisher(f.readerContext(), "invalid"); return err },
	} {
		if err := read(); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatalf("invalid exact identifier returned %v", err)
		}
	}
	// Use an authenticated Reader, not a missing actor, to exercise the role
	// boundary before any repository lookup or identifier allocation.
	ctx := identityfixture.WithActor(t.Context(), f.reader)
	for _, write := range []func() error{
		func() error { _, err := f.service.CreatePublisher(ctx, CreatePublisherInput{}); return err },
		func() error {
			_, err := f.service.UpdatePublisher(ctx, "invalid", PatchPublisherInput{}, 1)
			return err
		},
		func() error { _, err := f.service.CreateResource(ctx, CreateResourceInput{}); return err },
	} {
		if err := write(); ErrorCodeOf(err) != CodeForbidden {
			t.Fatalf("Reader mutation returned %v", err)
		}
	}
	if len(f.repo.audit) != 0 || len(f.repo.outbox) != 0 || len(f.repo.resources) != 0 || len(f.repo.publishers) != 0 {
		t.Fatal("rejected administration changed state")
	}
}
