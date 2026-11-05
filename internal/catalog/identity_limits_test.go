package catalog

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestAuthoringIdentityCharacterLimits(t *testing.T) {
	t.Parallel()
	for _, character := range []string{"x", "é", "🙂"} {
		for _, field := range []string{"identifier", "version", "relationship identifier", "relationship version"} {
			limit := 128
			if strings.Contains(field, "identifier") {
				limit = 512
			}
			for _, count := range []int{limit - 1, limit, limit + 1} {
				t.Run(fmt.Sprintf("%s/%s/%d", field, character, count), func(t *testing.T) {
					f := newGuardrailFixture(t)
					value := strings.Repeat(character, count)
					f.repo.namespaces["namespace"] = Namespace{ID: "namespace", TenantID: f.admin.TenantID}
					resource := Resource{ID: "resource", TenantID: f.admin.TenantID, NamespaceID: "namespace", Kind: KindAgent}
					f.repo.resources[resource.ID] = resource
					var err error
					switch field {
					case "identifier":
						var got Resource
						got, err = f.service.CreateResource(f.adminContext(), CreateResourceInput{NamespaceID: "namespace", CanonicalIdentifier: " " + value + " ", Kind: KindAgent})
						if err == nil && got.CanonicalIdentifier != value {
							t.Fatal("identifier was altered beyond whitespace trimming")
						}
					case "version":
						var got ResourceVersion
						got, _, _, err = f.service.CreateVersion(f.adminContext(), resource.ID, CreateVersionInput{Version: " " + value + " ", Representations: []RepresentationInput{{Role: RepresentationPrimary, MediaType: "application/json", Data: json.RawMessage(`{"name":"Example"}`)}}})
						if err == nil && got.Version != value {
							t.Fatal("version was altered beyond whitespace trimming")
						}
					default:
						relationship := Relationship{Type: "depends_on", TargetIdentifier: "target", TargetVersion: "1"}
						if field == "relationship identifier" {
							relationship.TargetIdentifier = value
						} else {
							relationship.TargetVersion = value
						}
						f.repo.resources["target"] = Resource{ID: "target", TenantID: f.admin.TenantID, CanonicalIdentifier: relationship.TargetIdentifier}
						f.repo.versions["target-version"] = ResourceVersion{ID: "target-version", TenantID: f.admin.TenantID, ResourceID: "target", Version: relationship.TargetVersion}
						var got []Relationship
						got, err = f.service.normalizeRelationships(f.adminContext(), f.admin.TenantID, resource, ResourceVersion{ID: "source-version"}, []Relationship{relationship})
						if err == nil && (len(got) != 1 || got[0] != relationship) {
							t.Fatal("relationship did not preserve its exact target")
						}
					}
					if count <= limit && err != nil {
						t.Fatalf("valid character count rejected: %v", err)
					}
					if count > limit && ErrorCodeOf(err) != CodeInvalidArgument {
						t.Fatalf("excess characters accepted: %v", err)
					}
					if count > limit && (len(f.repo.audit) != 0 || len(f.repo.outbox) != 0 || len(f.repo.versionRevisions) != 0 || len(f.repo.resources) != 1 && field == "identifier" || len(f.repo.versions) != 0 && field == "version") {
						t.Fatal("rejected input wrote domain state or evidence")
					}
				})
			}
		}
	}
}

func TestAuthoringIdentityRejectsInvalidText(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"", " \t ", "bad\x00text", "bad\xfftext"} {
		f := newGuardrailFixture(t)
		f.repo.resources["resource"] = Resource{ID: "resource", TenantID: f.admin.TenantID, NamespaceID: "namespace", Kind: KindAgent}
		_, err := f.service.CreateResource(f.adminContext(), CreateResourceInput{NamespaceID: "namespace", CanonicalIdentifier: value, Kind: KindAgent})
		if ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatalf("invalid identifier %q: %v", value, err)
		}
		_, _, _, err = f.service.CreateVersion(f.adminContext(), "resource", CreateVersionInput{Version: value, Representations: []RepresentationInput{{Role: RepresentationPrimary, MediaType: "application/json", Data: json.RawMessage(`{"name":"Example"}`)}}})
		if ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatalf("invalid version %q: %v", value, err)
		}
		if len(f.repo.audit) != 0 || len(f.repo.outbox) != 0 {
			t.Fatal("invalid text wrote evidence")
		}
	}
}

func TestAdoptionVersionCharacterLimits(t *testing.T) {
	t.Parallel()
	for _, value := range []string{strings.Repeat("é", 127), strings.Repeat("🙂", 128), strings.Repeat("🙂", 129), "bad\x00text", "bad\xfftext"} {
		t.Run(fmt.Sprintf("%q", value), func(t *testing.T) {
			fixture, input := adoptionFixture(t)
			input.Version = value
			beforeAudit, beforeOutbox := len(fixture.repo.audit), len(fixture.repo.outbox)
			result, err := fixture.service.AdoptExternalObservation(fixture.adminContext(), input)
			if value == strings.Repeat("é", 127) || value == strings.Repeat("🙂", 128) {
				if err != nil || result.Version.Version != value {
					t.Fatalf("valid adopted version changed or rejected: %v", err)
				}
			} else if ErrorCodeOf(err) != CodeInvalidArgument || len(fixture.repo.versions) != 0 || len(fixture.repo.audit) != beforeAudit || len(fixture.repo.outbox) != beforeOutbox {
				t.Fatalf("invalid adopted version wrote state: %v", err)
			}
		})
	}
}

func TestAuthoringPreservesDistinctUnicodeSpellings(t *testing.T) {
	t.Parallel()
	f := newGuardrailFixture(t)
	f.repo.namespaces["namespace"] = Namespace{ID: "namespace", TenantID: f.admin.TenantID}
	var first Resource
	versionIDs := make([]string, 0, 2)
	for _, spelling := range []string{"é", "e\u0301"} {
		resource, err := f.service.CreateResource(f.adminContext(), CreateResourceInput{NamespaceID: "namespace", CanonicalIdentifier: spelling, Kind: KindAgent})
		if err != nil || resource.CanonicalIdentifier != spelling {
			t.Fatalf("exact identifier spelling: %v", err)
		}
		if first.ID == "" {
			first = resource
		} else if first.ID == resource.ID {
			t.Fatal("distinct Unicode identifier spellings were merged")
		}
		version, _, _, err := f.service.CreateVersion(f.adminContext(), first.ID, CreateVersionInput{Version: spelling, Representations: []RepresentationInput{{Role: RepresentationPrimary, MediaType: "application/json", Data: json.RawMessage(`{"name":"Example"}`)}}})
		if err != nil || version.Version != spelling {
			t.Fatalf("exact version spelling: %v", err)
		}
		versionIDs = append(versionIDs, version.ID)
	}
	if len(f.repo.resources) != 2 || len(f.repo.versions) != 2 || versionIDs[0] == versionIDs[1] {
		t.Fatal("exact Unicode identities were collapsed")
	}
}
