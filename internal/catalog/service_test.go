package catalog_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

type lifecycleExternalFetcher struct {
	now          func() time.Time
	url          string
	body         []byte
	policyDigest string
}

type lifecyclePublicationWithdrawer struct {
	manager *publication.Manager
}

func (w *lifecyclePublicationWithdrawer) WithdrawPublicationDomain(ctx context.Context, tenantID, domain, action, targetID, cause string) ([]string, error) {
	if w.manager == nil {
		return nil, nil
	}
	changed, err := w.manager.WithdrawDomain(ctx, tenantID, domain, publication.WithdrawalCause{Cause: cause, OriginatingAction: action, OriginatingTargetID: targetID})
	if err != nil || !changed {
		return nil, err
	}
	return []string{"catalog.example.com"}, nil
}

func (w *lifecyclePublicationWithdrawer) WithdrawPublicationRelease(ctx context.Context, tenantID, releaseID, action, targetID, cause string) ([]string, error) {
	if w.manager == nil {
		return nil, nil
	}
	changed, err := w.manager.WithdrawRelease(ctx, tenantID, releaseID, publication.WithdrawalCause{Cause: cause, OriginatingAction: action, OriginatingTargetID: targetID})
	if err != nil || !changed {
		return nil, err
	}
	return []string{"catalog.example.com"}, nil
}

func (f lifecycleExternalFetcher) PolicyDigest() string { return f.policyDigest }

func (f lifecycleExternalFetcher) FetchExternalRepresentation(_ context.Context, request catalog.ExternalRepresentationFetchRequest) (catalog.ExternalRepresentationFetchResult, error) {
	if request.URL != f.url || request.TenantID != testTenantID || request.MaxResponseBytes < int64(len(f.body)) {
		return catalog.ExternalRepresentationFetchResult{}, fmt.Errorf("unexpected external representation request: %#v", request)
	}
	digest := sha256.Sum256(f.body)
	return catalog.ExternalRepresentationFetchResult{RequestedURL: request.URL, FinalURL: request.URL, Status: 200, MediaType: request.MediaType, Body: append([]byte(nil), f.body...), ContentDigest: fmt.Sprintf("sha256:%x", digest), PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: f.policyDigest, RetrievedAt: f.now()}, nil
}

func TestCatalogServicePersistsSkillInventoryAndImmutableHandoffWithoutPackageBytes(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 29, 12, 0, 0, 123456789, time.UTC)
	cursorKey := sha256.Sum256([]byte(t.Name()))
	repository := catalog.NewMemoryRepository()
	service, err := catalog.NewService(catalog.Options{Repository: repository, Clock: clock.NewFake(now), IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: cursorKey[:], Policy: "test-policy"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{MembershipID: "00000000-0000-7000-8000-0000f5120eb8", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: testTenantID, Subject: "contributor", Roles: []identity.Role{identity.RoleTenantAdmin, identity.RoleContributor}})
	namespace, err := service.CreateNamespace(ctx, catalog.CreateNamespaceInput{Slug: "packages", DisplayName: "Packages"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := service.CreateResource(ctx, catalog.CreateResourceInput{NamespaceID: namespace.ID, CanonicalIdentifier: "urn:air:catalog.example.com:packages:safe-skill", Kind: catalog.KindSkill})
	if err != nil {
		t.Fatal(err)
	}
	packageBytes := packagedSkillZIP(t, "safe-skill", map[string]string{"SKILL.md": "---\nname: safe-skill\ndescription: A safely inventoried Skill package.\n---\n# Instructions\n", "scripts/run.sh": "#!/bin/sh\nexit 99\n", "references/REFERENCE.md": "# Reference\n"})
	handoff := &catalog.SkillHandoff{
		Git: &catalog.GitHandoffReference{Repository: "https://git.example.com/team/skills.git", Commit: strings.Repeat("a", 40), Directory: "skills/safe-skill"},
		OCI: &catalog.OCIHandoffReference{Reference: "registry.example.com/team/safe-skill@sha256:" + strings.Repeat("b", 64)},
	}
	version, revision, evidence, err := service.CreateVersion(ctx, resource.ID, catalog.CreateVersionInput{Version: "1.0.0", Representations: []catalog.RepresentationInput{{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaSkillZIP, PackageData: packageBytes, Handoff: handoff}}})
	if err != nil {
		t.Fatal(err)
	}
	if evidence.State != catalog.EvidenceComplete || evidence.ValidationProfile != "catalog-validation-v7" || revision.Representations[0].Package == nil || revision.Representations[0].Package.FileCount != 3 || revision.Representations[0].Handoff == nil || revision.Representations[0].ArtifactID != "" || revision.Representations[0].URL != "" || len(revision.Representations[0].Data) != 0 {
		t.Fatalf("package version = %#v / %#v", revision, evidence)
	}
	if evidence.CreatedAt.Nanosecond()%1000 != 0 {
		t.Fatalf("validation evidence time is not PostgreSQL-stable: %s", evidence.CreatedAt)
	}
	if validateErr := catalog.ValidateEvidenceSetIntegrity(evidence); validateErr != nil {
		t.Fatalf("fresh validation evidence failed its retained digest check: %v", validateErr)
	}
	loaded, loadedEvidence, namespaceID, err := service.GetVersionRevision(ctx, revision.ID)
	if err != nil || loaded.ContentSetDigest != revision.ContentSetDigest || loadedEvidence.Digest != evidence.Digest || namespaceID != resource.NamespaceID || loaded.Representations[0].Handoff.Git.Commit != strings.Repeat("a", 40) {
		t.Fatalf("loaded package revision = %#v / %#v / %v", loaded, loadedEvidence, err)
	}
	serialized, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(serialized, packageBytes) || bytes.Contains(serialized, []byte(`"packageData"`)) || bytes.Contains(serialized, []byte("exit 99")) {
		t.Fatalf("transient package bytes leaked into persisted revision: %s", serialized)
	}
	validatedRevision, renewedEvidence, namespaceID, err := service.RevalidateVersionRevision(ctx, revision.ID)
	if err != nil {
		t.Fatal(err)
	}
	if namespaceID != resource.NamespaceID || renewedEvidence.State != catalog.EvidenceComplete || renewedEvidence.ValidationProfile != "catalog-validation-v7" || !renewedEvidence.ExpiresAt.IsZero() || renewedEvidence.ID == evidence.ID || validatedRevision.ContentSetDigest != revision.ContentSetDigest || validatedRevision.EvidenceSetID != renewedEvidence.ID {
		t.Fatalf("renewed package validation = %#v / %#v", validatedRevision, renewedEvidence)
	}
	retainedEvidence, err := repository.GetValidationEvidence(context.Background(), testTenantID, revision.ID, evidence.ID)
	if err != nil || retainedEvidence.Digest != evidence.Digest {
		t.Fatalf("retained package evidence = %#v, %v", retainedEvidence, err)
	}

	changedHandoff := *handoff
	changedOCI := *handoff.OCI
	changedOCI.Reference = "registry.example.com/team/safe-skill@sha256:" + strings.Repeat("c", 64)
	changedHandoff.OCI = &changedOCI
	_, changedRevision, _, err := service.CreateVersionRevision(ctx, version.ID, []catalog.RepresentationInput{{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaSkillZIP, PackageData: packageBytes, Handoff: &changedHandoff}}, version.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if changedRevision.ContentSetDigest == revision.ContentSetDigest {
		t.Fatal("changing the immutable handoff did not change the content-set digest")
	}
}

func TestCatalogServiceRejectsUnsafeSkillPackageControlInputs(t *testing.T) {
	t.Parallel()
	cursorKey := sha256.Sum256([]byte(t.Name()))
	service, err := catalog.NewService(catalog.Options{Repository: catalog.NewMemoryRepository(), Clock: clock.NewFake(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)), IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: cursorKey[:]})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{MembershipID: "00000000-0000-7000-8000-0000f5120eb8", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: testTenantID, Subject: "contributor", Roles: []identity.Role{identity.RoleTenantAdmin, identity.RoleContributor}})
	namespace, err := service.CreateNamespace(ctx, catalog.CreateNamespaceInput{Slug: "unsafe-packages", DisplayName: "Unsafe packages"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := service.CreateResource(ctx, catalog.CreateResourceInput{NamespaceID: namespace.ID, CanonicalIdentifier: "urn:air:catalog.example.com:packages:unsafe", Kind: catalog.KindSkill})
	if err != nil {
		t.Fatal(err)
	}
	packageBytes := packagedSkillZIP(t, "safe-skill", map[string]string{"SKILL.md": "---\nname: safe-skill\ndescription: Safe Skill.\n---\n"})
	validHandoff := &catalog.SkillHandoff{Git: &catalog.GitHandoffReference{Repository: "https://git.example.com/team/skills.git", Commit: strings.Repeat("a", 40), Directory: "skills/safe-skill"}}
	tests := []catalog.RepresentationInput{
		{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaSkillZIP, PackageData: packageBytes},
		{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaSkillZIP, PackageData: packageBytes, Handoff: &catalog.SkillHandoff{Git: &catalog.GitHandoffReference{Repository: strings.Join([]string{"https://user", "credential@git.example.com/repo"}, ":"), Commit: strings.Repeat("a", 40), Directory: "safe-skill"}}},
		{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaA2AAgentCard, PackageData: packageBytes, Handoff: validHandoff},
		{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaSkillZIP, PackageData: packageBytes, Content: "two forms", Handoff: validHandoff},
	}
	for index, input := range tests {
		if _, _, _, err := service.CreateVersion(ctx, resource.ID, catalog.CreateVersionInput{Version: strconv.Itoa(index), Representations: []catalog.RepresentationInput{input}}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("unsafe input %d returned %v", index, err)
		}
	}
	mismatched := &catalog.SkillHandoff{Git: &catalog.GitHandoffReference{Repository: "https://git.example.com/team/skills.git", Commit: strings.Repeat("a", 40), Directory: "skills/other-skill"}}
	_, revision, evidence, err := service.CreateVersion(ctx, resource.ID, catalog.CreateVersionInput{Version: "blocked", Representations: []catalog.RepresentationInput{{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaSkillZIP, PackageData: packageBytes, Handoff: mismatched}}})
	if err != nil {
		t.Fatal(err)
	}
	if evidence.State != catalog.EvidenceBlocked || revision.Representations[0].ValidationState != "invalid" {
		t.Fatalf("root-mismatched handoff was not retained as blocked evidence: %#v / %#v", revision, evidence)
	}
}

func packagedSkillZIP(t *testing.T, root string, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, name := range sortedMapKeys(files) {
		file, err := writer.Create(root + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte(files[name])); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func sortedMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

const testTenantID = "00000000-0000-7000-8000-000000000001"

func TestCatalogServiceRequiresStrongCursorKey(t *testing.T) {
	t.Parallel()

	options := catalog.Options{
		Repository: catalog.NewMemoryRepository(),
		Clock:      clock.NewFake(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)),
		IDs:        id.Generator{},
		Validator:  adapter.NewRegistry(),
	}
	for _, key := range [][]byte{nil, make([]byte, 31)} {
		options.CursorKey = key
		if _, err := catalog.NewService(options); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("cursor key length %d returned %v", len(key), err)
		}
	}
	options.CursorKey = make([]byte, 32)
	if _, err := catalog.NewService(options); err != nil {
		t.Fatalf("32-byte cursor key returned %v", err)
	}
}

func TestNativeVersionMustMatchCatalogVersion(t *testing.T) {
	cursorKey := sha256.Sum256([]byte(t.Name()))
	service, err := catalog.NewService(catalog.Options{
		Repository: catalog.NewMemoryRepository(),
		Clock:      clock.NewFake(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)),
		IDs:        id.Generator{},
		Validator:  adapter.NewRegistry(),
		CursorKey:  cursorKey[:],
		Policy:     "test-policy",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{MembershipID: "00000000-0000-7000-8000-0000f5120eb8", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: testTenantID, Subject: "contributor", Roles: []identity.Role{identity.RoleTenantAdmin, identity.RoleContributor}})
	namespace, err := service.CreateNamespace(ctx, catalog.CreateNamespaceInput{Slug: "default", DisplayName: "Default"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := service.CreateResource(ctx, catalog.CreateResourceInput{NamespaceID: namespace.ID, CanonicalIdentifier: "urn:air:catalog.example.com:default:version-mismatch", Kind: catalog.KindAgent})
	if err != nil {
		t.Fatal(err)
	}
	data := json.RawMessage(`{"name":"Agent","description":"Version mismatch fixture","version":"2.0.0","supportedInterfaces":[{"url":"https://agents.example.com/a2a","protocolBinding":"JSONRPC","protocolVersion":"1.0"}],"capabilities":{},"defaultInputModes":["application/json"],"defaultOutputModes":["application/json"],"skills":[{"id":"test","name":"Test","description":"Tests version binding","tags":["test"]}]}`)
	_, revision, evidence, err := service.CreateVersion(ctx, resource.ID, catalog.CreateVersionInput{Version: "1.0.0", Representations: []catalog.RepresentationInput{{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaA2AAgentCard, Data: data}}})
	if err != nil {
		t.Fatal(err)
	}
	if revision.Representations[0].ValidationState != "invalid" || evidence.State == catalog.EvidenceComplete {
		t.Fatalf("version mismatch did not block evidence: revision=%#v evidence=%#v", revision, evidence)
	}
	found := false
	for _, finding := range revision.Representations[0].Findings {
		found = found || finding.Code == "NATIVE_VERSION_MISMATCH"
	}
	if !found {
		t.Fatalf("version mismatch finding missing: %#v", revision.Representations[0].Findings)
	}
}

func TestEvidenceDigestDistinguishesRepeatedValidationRuns(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	cursorKey := sha256.Sum256([]byte(t.Name()))
	service, err := catalog.NewService(catalog.Options{
		Repository: catalog.NewMemoryRepository(),
		Clock:      clock.NewFake(now),
		IDs:        id.Generator{},
		Validator:  adapter.NewRegistry(),
		CursorKey:  cursorKey[:],
		Policy:     "test-policy",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{MembershipID: "00000000-0000-7000-8000-0000f5120eb8", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: testTenantID, Subject: "contributor", Roles: []identity.Role{identity.RoleTenantAdmin, identity.RoleContributor}})
	namespace, err := service.CreateNamespace(ctx, catalog.CreateNamespaceInput{Slug: "evidence", DisplayName: "Evidence"})
	if err != nil {
		t.Fatal(err)
	}
	representation := catalog.RepresentationInput{
		Role:      catalog.RepresentationPrimary,
		MediaType: adapter.MediaA2AAgentCard,
		Data:      json.RawMessage(`{"name":"Shared Agent","description":"Exercises repeated evidence","version":"1.0.0","supportedInterfaces":[{"url":"https://agents.example.com/shared","protocolBinding":"JSONRPC","protocolVersion":"1.0"}],"capabilities":{},"defaultInputModes":["application/json"],"defaultOutputModes":["application/json"],"skills":[{"id":"shared","name":"Shared","description":"Exercises repeated evidence","tags":["test"]}]}`),
	}
	evidence := make([]catalog.EvidenceSet, 0, 2)
	for _, name := range []string{"one", "two"} {
		resource, createErr := service.CreateResource(ctx, catalog.CreateResourceInput{NamespaceID: namespace.ID, CanonicalIdentifier: "urn:air:catalog.example.com:evidence:" + name, Kind: catalog.KindAgent})
		if createErr != nil {
			t.Fatal(createErr)
		}
		_, revision, result, createErr := service.CreateVersion(ctx, resource.ID, catalog.CreateVersionInput{Version: "1.0.0", Representations: []catalog.RepresentationInput{representation}})
		if createErr != nil {
			t.Fatal(createErr)
		}
		if revision.EvidenceSetID != result.ID || result.ValidationProfile != "catalog-validation-v7" || result.LegalRights.State != catalog.LegalRightsNotAssessed || result.Trust.State != catalog.TrustNotAssessed {
			t.Fatalf("unexpected evidence binding: revision=%#v evidence=%#v", revision, result)
		}
		evidence = append(evidence, result)
	}
	if evidence[0].SubjectDigest != evidence[1].SubjectDigest {
		t.Fatalf("identical content produced different subject digests: %#v", evidence)
	}
	if evidence[0].ID == evidence[1].ID || evidence[0].Digest == evidence[1].Digest {
		t.Fatalf("distinct validation runs reused evidence identity: %#v", evidence)
	}
}

func TestVersionRevisionRequiresCurrentRevisionAndPreservesHistory(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	repository := catalog.NewMemoryRepository()
	cursorKey := sha256.Sum256([]byte(t.Name()))
	service, err := catalog.NewService(catalog.Options{
		Repository: repository,
		Clock:      clock.NewFake(now),
		IDs:        id.Generator{},
		Validator:  adapter.NewRegistry(),
		CursorKey:  cursorKey[:],
		Policy:     "test-policy",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{MembershipID: "00000000-0000-7000-8000-0000f5120eb8", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: testTenantID, Subject: "contributor", Roles: []identity.Role{identity.RoleTenantAdmin, identity.RoleContributor}})
	namespace, err := service.CreateNamespace(ctx, catalog.CreateNamespaceInput{Slug: "revisions", DisplayName: "Revisions"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := service.CreateResource(ctx, catalog.CreateResourceInput{NamespaceID: namespace.ID, CanonicalIdentifier: "urn:air:catalog.example.com:revisions:agent", Kind: catalog.KindAgent})
	if err != nil {
		t.Fatal(err)
	}
	representation := func(description string) []catalog.RepresentationInput {
		return []catalog.RepresentationInput{{
			Role:      catalog.RepresentationPrimary,
			MediaType: adapter.MediaA2AAgentCard,
			Data:      json.RawMessage(`{"name":"Revision Agent","description":"` + description + `","version":"1.0.0","supportedInterfaces":[{"url":"https://agents.example.com/revisions","protocolBinding":"JSONRPC","protocolVersion":"1.0"}],"capabilities":{},"defaultInputModes":["application/json"],"defaultOutputModes":["application/json"],"skills":[{"id":"revise","name":"Revise","description":"Exercises immutable revisions","tags":["test"]}]}`),
		}}
	}
	version, original, _, err := service.CreateVersion(ctx, resource.ID, catalog.CreateVersionInput{Version: "1.0.0", Representations: representation("Original representation")})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := service.CreateVersionRevision(ctx, version.ID, representation("Rejected stale revision"), version.Revision+1); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("future version precondition returned %v", err)
	}
	updated, revised, evidence, err := service.CreateVersionRevision(ctx, version.ID, representation("Revised representation"), version.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != version.Revision+1 || updated.CandidateRevisionID != revised.ID || revised.RevisionNumber != original.RevisionNumber+1 || revised.EvidenceSetID != evidence.ID {
		t.Fatalf("revised version = %#v, revision = %#v, evidence = %#v", updated, revised, evidence)
	}
	storedOriginal, storedEvidence, err := repository.GetVersionRevision(context.Background(), testTenantID, original.ID)
	if err != nil || storedOriginal.ID != original.ID || storedEvidence.ID == evidence.ID {
		t.Fatalf("original revision history = %#v, evidence = %#v, err = %v", storedOriginal, storedEvidence, err)
	}
	if _, _, _, err := service.CreateVersionRevision(ctx, version.ID, representation("Another stale revision"), version.Revision); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("stale version precondition returned %v", err)
	}
}

func TestGovernedReadsRequireCurrentAuthority(t *testing.T) {
	cursorKey := sha256.Sum256([]byte(t.Name()))
	service, err := catalog.NewService(catalog.Options{
		Repository: catalog.NewMemoryRepository(),
		Clock:      clock.NewFake(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)),
		IDs:        id.Generator{},
		Validator:  adapter.NewRegistry(),
		CursorKey:  cursorKey[:],
		Policy:     "test-policy",
	})
	if err != nil {
		t.Fatal(err)
	}
	withoutReader := identityfixture.WithActor(context.Background(), identity.Actor{MembershipID: "00000000-0000-7000-8000-0000e5bd510e", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: testTenantID, Subject: "authenticated"})
	if rows, err := service.ListResources(withoutReader, catalog.ResourceListRequest{Limit: 10}); err != nil || len(rows) != 0 {
		t.Fatalf("principal without grants must receive an empty authorized collection: %v / %v", rows, err)
	}
	reader := identityfixture.WithActor(context.Background(), identity.Actor{MembershipID: "00000000-0000-7000-8000-0000869c4bfc", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: testTenantID, Subject: "reader", Roles: []identity.Role{identity.RoleReader}})
	if resources, err := service.ListResources(reader, catalog.ResourceListRequest{Limit: 20}); err != nil || len(resources) != 0 {
		t.Fatalf("authorized empty read = %#v, %v", resources, err)
	}
}

func TestListingCannotMoveAResourceAcrossNamespaces(t *testing.T) {
	cursorKey := sha256.Sum256([]byte(t.Name()))
	service, err := catalog.NewService(catalog.Options{
		Repository: catalog.NewMemoryRepository(),
		Clock:      clock.NewFake(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)),
		IDs:        id.Generator{},
		Validator:  adapter.NewRegistry(),
		CursorKey:  cursorKey[:],
		Policy:     "test-policy",
	})
	if err != nil {
		t.Fatal(err)
	}
	adminContext := identityfixture.WithActor(context.Background(), identity.Actor{MembershipID: "00000000-0000-7000-8000-0000cbe16062", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: testTenantID, Subject: "admin", Roles: []identity.Role{identity.RoleTenantAdmin, identity.RoleContributor}})
	namespaceA, err := service.CreateNamespace(adminContext, catalog.CreateNamespaceInput{Slug: "alpha", DisplayName: "Alpha"})
	if err != nil {
		t.Fatal(err)
	}
	namespaceB, err := service.CreateNamespace(adminContext, catalog.CreateNamespaceInput{Slug: "beta", DisplayName: "Beta"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := service.CreateResource(adminContext, catalog.CreateResourceInput{NamespaceID: namespaceA.ID, CanonicalIdentifier: "urn:air:catalog.example.com:alpha:agent", Kind: catalog.KindAgent})
	if err != nil {
		t.Fatal(err)
	}
	betaContributor := identityfixture.WithActor(context.Background(), identity.Actor{MembershipID: "00000000-0000-7000-8000-0000b01bfe7f", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: testTenantID, Subject: "beta-contributor", Namespaces: map[string][]identity.Role{namespaceB.ID: {identity.RoleContributor}}})
	if _, err := service.CreateListing(betaContributor, catalog.CreateListingInput{NamespaceID: namespaceB.ID, ResourceID: resource.ID}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("cross-namespace listing returned %v", err)
	}
	if _, err := service.CreateListing(adminContext, catalog.CreateListingInput{NamespaceID: namespaceA.ID, ResourceID: resource.ID}); err != nil {
		t.Fatalf("same-namespace listing returned %v", err)
	}
}

func TestListingRelationshipsResolveExactLocalTargets(t *testing.T) {
	cursorKey := sha256.Sum256([]byte(t.Name()))
	service, err := catalog.NewService(catalog.Options{
		Repository: catalog.NewMemoryRepository(),
		Clock:      clock.NewFake(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)),
		IDs:        id.Generator{},
		Validator:  adapter.NewRegistry(),
		CursorKey:  cursorKey[:],
		Policy:     "test-policy",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{MembershipID: "00000000-0000-7000-8000-0000cbe16062", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: testTenantID, Subject: "admin", Roles: []identity.Role{identity.RoleTenantAdmin, identity.RoleContributor}})
	namespace, err := service.CreateNamespace(ctx, catalog.CreateNamespaceInput{Slug: "default", DisplayName: "Default"})
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := service.CreatePublisher(ctx, catalog.CreatePublisherInput{DisplayName: "Example", Domains: []string{"catalog.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	createAgent := func(name, versionLabel string) (catalog.Resource, catalog.ResourceVersion, catalog.VersionRevision) {
		resource, createErr := service.CreateResource(ctx, catalog.CreateResourceInput{NamespaceID: namespace.ID, CanonicalIdentifier: "urn:air:catalog.example.com:default:" + name, Kind: catalog.KindAgent})
		if createErr != nil {
			t.Fatal(createErr)
		}
		data := json.RawMessage(`{"name":"` + name + `","description":"Relationship fixture","version":"` + versionLabel + `","supportedInterfaces":[{"url":"https://agents.example.com/` + name + `","protocolBinding":"JSONRPC","protocolVersion":"1.0"}],"capabilities":{},"defaultInputModes":["application/json"],"defaultOutputModes":["application/json"],"skills":[{"id":"relate-resources","name":"Relate resources","description":"Relates Catalog resources","tags":["catalog"]}]}`)
		version, revision, _, createErr := service.CreateVersion(ctx, resource.ID, catalog.CreateVersionInput{Version: versionLabel, Representations: []catalog.RepresentationInput{{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaA2AAgentCard, Data: data}}})
		if createErr != nil {
			t.Fatal(createErr)
		}
		return resource, version, revision
	}
	sourceResource, sourceVersion, sourceRevision := createAgent("source-agent", "1.0.0")
	targetResource, _, _ := createAgent("target-agent", "2.0.0")
	listing, err := service.CreateListing(ctx, catalog.CreateListingInput{NamespaceID: namespace.ID, ResourceID: sourceResource.ID})
	if err != nil {
		t.Fatal(err)
	}
	baseInput := catalog.CreateListingRevisionInput{VersionRevisionID: sourceRevision.ID, DisplayName: "Source agent", Description: "Agent with a governed dependency.", PublisherID: publisher.ID, Visibility: catalog.VisibilityTenant}
	invalid := baseInput
	invalid.Relationships = []catalog.Relationship{{Type: "depends_on", TargetIdentifier: targetResource.CanonicalIdentifier, TargetVersion: "missing"}}
	if _, _, err := service.CreateListingRevision(ctx, listing.ID, invalid, listing.Revision); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("missing relationship version returned %v", err)
	}
	self := baseInput
	self.Relationships = []catalog.Relationship{{Type: "depends_on", TargetIdentifier: sourceResource.CanonicalIdentifier, TargetVersion: sourceVersion.Version}}
	if _, _, err := service.CreateListingRevision(ctx, listing.ID, self, listing.Revision); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("self relationship returned %v", err)
	}
	valid := baseInput
	valid.Relationships = []catalog.Relationship{{Type: "depends_on", TargetIdentifier: targetResource.CanonicalIdentifier, TargetVersion: "2.0.0"}}
	_, revision, err := service.CreateListingRevision(ctx, listing.ID, valid, listing.Revision)
	if err != nil || len(revision.Relationships) != 1 || revision.Relationships[0].TargetIdentifier != targetResource.CanonicalIdentifier {
		t.Fatalf("valid relationship revision = %#v, %v", revision, err)
	}
}

func TestGovernedLifecycleForAllSupportedKinds(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	fakeClock := clock.NewFake(now)
	repository := catalog.NewMemoryRepository()
	cursorKey := sha256.Sum256([]byte("catalog-lifecycle-test-key"))
	externalURL := "https://content.example.com/policy-agent.json"
	externalBody := []byte(`{"name":"Policy Agent","description":"Answers policy questions","version":"1.0.0","supportedInterfaces":[{"url":"https://agents.example.com/policy","protocolBinding":"JSONRPC","protocolVersion":"1.0"}],"capabilities":{},"defaultInputModes":["application/json"],"defaultOutputModes":["application/json"],"skills":[{"id":"answer-policy","name":"Answer policy questions","description":"Answers policy questions","tags":["policy"]}]}`)
	externalFetcher := &lifecycleExternalFetcher{now: fakeClock.Now, url: externalURL, body: externalBody, policyDigest: "sha256:" + strings.Repeat("a", 64)}
	withdrawer := &lifecyclePublicationWithdrawer{}
	service, err := catalog.NewService(catalog.Options{Repository: repository, Clock: fakeClock, IDs: id.Generator{}, Validator: adapter.NewRegistry(), ExternalFetcher: externalFetcher, CursorKey: cursorKey[:], Policy: "test-policy", PublicationWithdrawer: withdrawer})
	if err != nil {
		t.Fatal(err)
	}
	admin := identity.Actor{MembershipID: "00000000-0000-7000-8000-0000cbe16062", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: testTenantID, Subject: "admin", Roles: []identity.Role{identity.RoleTenantAdmin, identity.RoleContributor, identity.RolePublisher}}
	contributor := identity.Actor{MembershipID: "00000000-0000-7000-8000-0000f5120eb8", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: testTenantID, Subject: "contributor", Roles: []identity.Role{identity.RoleContributor}}
	adminContext := identityfixture.WithActor(context.Background(), admin)
	namespace, err := service.CreateNamespace(adminContext, catalog.CreateNamespaceInput{Slug: "default", DisplayName: "Default"})
	if err != nil {
		t.Fatal(err)
	}
	reviewer := identity.Actor{MembershipID: "00000000-0000-7000-8000-000075bea508", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: testTenantID, Subject: "reviewer", Namespaces: map[string][]identity.Role{namespace.ID: {identity.RoleReviewer}}}
	publisherActor := identity.Actor{MembershipID: "00000000-0000-7000-8000-0000a7a77f47", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: testTenantID, Subject: "publisher", Namespaces: map[string][]identity.Role{namespace.ID: {identity.RolePublisher}}}
	publisher, err := service.CreatePublisher(adminContext, catalog.CreatePublisherInput{DisplayName: "Example", Domains: []string{"catalog.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	publisher, err = service.ApprovePublisherPublicationDomain(adminContext, publisher.ID, "catalog.example.com", "approved for test publication", publisher.Revision)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name           string
		kind           catalog.Kind
		mediaType      string
		representation catalog.RepresentationInput
	}{
		{name: "agent", kind: catalog.KindAgent, mediaType: adapter.MediaA2AAgentCard, representation: catalog.RepresentationInput{URL: externalURL}},
		{name: "mcp", kind: catalog.KindMCPServer, mediaType: adapter.MediaMCPServerCard, representation: catalog.RepresentationInput{Data: json.RawMessage(`{"$schema":"https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json","name":"com.example/integration-tools","description":"Provides integration tools.","version":"1.0.1","remotes":[{"type":"streamable-http","url":"https://mcp.example.com/messages"}]}`)}},
		{name: "skill", kind: catalog.KindSkill, mediaType: adapter.MediaSkillMarkdown, representation: catalog.RepresentationInput{Content: "---\nname: summarize-policy\ndescription: Summarize a policy document.\n---\n\n# Instructions\nSummarize the supplied policy.\n"}},
	}
	externalRevisionID := ""
	for index, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fakeClock.Advance(time.Minute)
			contributorContext := identityfixture.WithActor(adminContext, contributor)
			identifier := "urn:air:catalog.example.com:default:" + testCase.name
			if testCase.name == "agent" {
				identifier = "urn:air:catalog.example.com:agent"
			}
			resource, createErr := service.CreateResource(contributorContext, catalog.CreateResourceInput{NamespaceID: namespace.ID, CanonicalIdentifier: identifier, Kind: testCase.kind})
			if createErr != nil {
				t.Fatal(createErr)
			}
			testCase.representation.Role = catalog.RepresentationPrimary
			testCase.representation.MediaType = testCase.mediaType
			version, versionRevision, evidence, createErr := service.CreateVersion(contributorContext, resource.ID, catalog.CreateVersionInput{Version: "1.0." + strconv.Itoa(index), Representations: []catalog.RepresentationInput{testCase.representation}})
			if createErr != nil {
				t.Fatal(createErr)
			}
			if evidence.State != catalog.EvidenceComplete || version.CandidateRevisionID != versionRevision.ID {
				t.Fatalf("unexpected version evidence: %#v %#v", version, evidence)
			}
			if testCase.name == "agent" {
				externalRevisionID = versionRevision.ID
			}
			listing, createErr := service.CreateListing(contributorContext, catalog.CreateListingInput{NamespaceID: namespace.ID, ResourceID: resource.ID})
			if createErr != nil {
				t.Fatal(createErr)
			}
			listing, _, createErr = service.CreateListingRevision(contributorContext, listing.ID, catalog.CreateListingRevisionInput{VersionRevisionID: versionRevision.ID, DisplayName: "Published " + testCase.name, Description: "A governed " + testCase.name + " used by the catalog lifecycle test.", RepresentativeQueries: []string{"Find " + testCase.name + " resources", "Show governed " + testCase.name + " resources"}, PublisherID: publisher.ID, Visibility: catalog.VisibilityPublic, License: "Apache-2.0"}, listing.Revision)
			if createErr != nil {
				t.Fatal(createErr)
			}
			submission, createErr := service.Submit(contributorContext, listing.ID)
			if createErr != nil {
				t.Fatal(createErr)
			}
			if _, _, createErr = service.DecideSubmission(contributorContext, submission.ID, "approve", "self approval", submission.Revision); catalog.ErrorCodeOf(createErr) != catalog.CodeForbidden && catalog.ErrorCodeOf(createErr) != catalog.CodeSeparationOfDuties {
				t.Fatalf("self approval returned %v", createErr)
			}
			submission, version, createErr = service.DecideSubmission(identityfixture.WithActor(adminContext, reviewer), submission.ID, "approve", "reviewed", submission.Revision)
			if createErr != nil {
				t.Fatal(createErr)
			}
			if submission.State != catalog.SubmissionApproved || version.SealedRevisionID != versionRevision.ID {
				t.Fatalf("submission or version was not sealed: %#v %#v", submission, version)
			}
			if _, _, createErr = service.DecideSubmission(identityfixture.WithActor(adminContext, reviewer), submission.ID, "approve", "again", submission.Revision); catalog.ErrorCodeOf(createErr) != catalog.CodeInvalidTransition {
				t.Fatalf("terminal decision returned %v", createErr)
			}
			if testCase.name == "agent" {
				revocation, revokeErr := service.RevokePublisherPublicationDomain(adminContext, publisher.ID, "catalog.example.com", "verify terminal identifier publisher boundary", publisher.Revision)
				if revokeErr != nil {
					t.Fatal(revokeErr)
				}
				publisher = revocation.Publisher
				if _, _, _, publishErr := service.Publish(identityfixture.WithActor(adminContext, publisherActor), listing.ID, catalog.PublishInput{SubmissionID: submission.ID}, listing.Revision); catalog.ErrorCodeOf(publishErr) != catalog.CodePublicationNotReady {
					t.Fatalf("terminal identifier bypassed publisher authorization: %v", publishErr)
				}
				publisher, createErr = service.ApprovePublisherPublicationDomain(adminContext, publisher.ID, "catalog.example.com", "restore approved publication domain", publisher.Revision)
				if createErr != nil {
					t.Fatal(createErr)
				}
			}
			listing, release, _, createErr := service.Publish(identityfixture.WithActor(adminContext, publisherActor), listing.ID, catalog.PublishInput{SubmissionID: submission.ID}, listing.Revision)
			if createErr != nil {
				t.Fatal(createErr)
			}
			if listing.CurrentReleaseID != release.ID || release.State != catalog.ReleasePublished {
				t.Fatalf("release was not current: %#v %#v", listing, release)
			}
		})
	}
	publishAudits, err := repository.AuditEvents(context.Background(), testTenantID, catalog.AuditEventListRequest{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	externalVerificationRecorded := false
	for _, event := range publishAudits {
		if event.Action != "listing_release.published" || len(event.Details) == 0 {
			continue
		}
		details, marshalErr := json.Marshal(event.Details)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if bytes.Contains(details, []byte(externalURL)) {
			t.Fatal("publication audit exposed an external URL")
		}
		if bytes.Contains(details, []byte("externalVerificationCount")) && bytes.Contains(details, []byte("externalVerificationDigest")) {
			externalVerificationRecorded = true
		}
	}
	if !externalVerificationRecorded {
		t.Fatal("publication did not retain its bounded external verification evidence")
	}

	records, err := service.PublicRecords(context.Background(), testTenantID)
	if err != nil || len(records) != len(cases) {
		t.Fatalf("public records = %d, err = %v", len(records), err)
	}
	manager, err := publication.NewManager(publication.Options{Records: service, Store: publication.NewMemoryStore(), Clock: fakeClock, IDs: id.Generator{}, BaseURL: "https://catalog.example.com", PublisherDomain: "catalog.example.com", ARDProfile: "ard-test", AICatalogProfile: "ai-catalog-test", CursorKey: cursorKey[:]})
	if err != nil {
		t.Fatal(err)
	}
	withdrawer.manager = manager
	snapshot, err := manager.BuildCandidate(identityfixture.WithActor(context.Background(), identity.Actor{TenantID: testTenantID, Subject: "publication-fixture", Roles: []identity.Role{identity.RolePublisher}}), testTenantID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.ARD.Entries) != 4 || snapshot.ETag == snapshot.AICatalogETag || snapshot.ARDContentDigest == "" || snapshot.AIContentDigest == "" {
		t.Fatalf("invalid publication candidate: %#v", snapshot)
	}
	skillFound := false
	aiSkillFound := false
	externalAgentFound := false
	for _, entry := range snapshot.ARD.Entries {
		if entry.Identifier == "urn:air:catalog.example.com:default:skill" && entry.Type != adapter.MediaSkillMarkdownARD {
			t.Fatalf("Skill ARD media type = %q", entry.Type)
		}
		if entry.Identifier == "urn:air:catalog.example.com:default:skill" {
			skillFound = true
		}
		if entry.Identifier == "urn:air:catalog.example.com:agent" && entry.URL == externalURL {
			externalAgentFound = true
		}
	}
	for _, entry := range snapshot.AICatalog.Entries {
		if entry.Identifier == "urn:air:catalog.example.com:default:skill" && entry.Type == adapter.MediaSkillMarkdown {
			aiSkillFound = true
		}
	}
	if !skillFound {
		t.Fatal("Skill was not included in the ARD publication candidate")
	}
	if !aiSkillFound {
		t.Fatal("Skill was not emitted with the AI Catalog Markdown media type")
	}
	if !externalAgentFound {
		t.Fatal("HTTPS representation was not published as its reviewed external delivery URL")
	}
	if _, err = manager.Activate(identityfixture.WithActor(context.Background(), identity.Actor{TenantID: testTenantID, Subject: "publication-fixture", Roles: []identity.Role{identity.RolePublisher}}), testTenantID, snapshot.ID, "Activate reviewed snapshot"); err != nil {
		t.Fatal(err)
	}
	search, err := manager.Search(context.Background(), testTenantID, publication.SearchRequest{Query: publication.SearchQuery{Text: "governed agent"}, Federation: "none"})
	if err != nil || len(search.Results) != 1 || search.Results[0].Score < 0 || search.Results[0].Score > 100 {
		t.Fatalf("unexpected search: %#v, %v", search, err)
	}
	explore, err := manager.Explore(context.Background(), testTenantID, publication.ExploreRequest{ResultType: publication.ExploreResultType{Facets: []publication.ExploreFacetRequest{{Field: "type"}}}})
	if err != nil || len(explore.Facets["type"].Buckets) != 4 {
		t.Fatalf("unexpected facets: %#v, %v", explore, err)
	}
	fakeClock.Advance(time.Minute)
	externalFetcher.body = []byte(`{"name":"Policy Agent","description":"Changed after publication","version":"1.0.0","supportedInterfaces":[{"url":"https://agents.example.com/policy","protocolBinding":"JSONRPC","protocolVersion":"1.0"}],"capabilities":{},"defaultInputModes":["application/json"],"defaultOutputModes":["application/json"],"skills":[{"id":"answer-policy","name":"Answer policy questions","description":"Answers policy questions","tags":["policy"]}]}`)
	_, driftEvidence, _, err := service.RevalidateVersionRevision(identityfixture.WithActor(context.Background(), contributor), externalRevisionID)
	if err != nil || driftEvidence.State != catalog.EvidenceBlocked {
		t.Fatalf("published external drift evidence = %#v, %v", driftEvidence, err)
	}
	if _, err := manager.Active(context.Background(), testTenantID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("published external drift left the discovery snapshot active: %v", err)
	}
}
