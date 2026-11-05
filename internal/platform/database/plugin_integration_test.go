//go:build integration

package database

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

// Exercise actual migration, exact-role insertion, immutable inspection and RLS
// on the suite-owned disposable schema. No package content is executed.
func TestPostgresPluginInspectionRoundTripAndIsolation(t *testing.T) {
	db := newHousekeepingDatabase(t)
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for _, file := range []struct{ name, body string }{
		{"plugin.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"database-tools"}`},
		{"skills/read/SKILL.md", "---\nname: read\ndescription: Read metadata.\n---\nDo not execute."},
		{"mcp.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"metadata":{"type":"stdio","command":"node"}}}`},
	} {
		w, err := writer.Create(file.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write([]byte(file.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	fetch := &postgresExternalFetcher{at: now, body: archive.Bytes(), policyDigest: "sha256:" + strings.Repeat("b", 64)}
	repo, err := NewPostgresRepository(db.api)
	if err != nil {
		t.Fatal(err)
	}
	key := sha256.Sum256([]byte(t.Name()))
	c := clock.NewFake(now)
	options := catalog.Options{Repository: repo, Clock: c, IDs: id.Generator{}, Validator: adapter.NewRegistry(), ExternalFetcher: fetch, CursorKey: key[:], Policy: "plugin-test"}
	service, err := catalog.NewService(options)
	if err != nil {
		t.Fatal(err)
	}
	roles := []identity.Role{identity.RoleTenantAdmin, identity.RoleContributor, identity.RolePublisher, identity.RoleReader}
	principal, err := ProvisionIdentity(t.Context(), db.operator, IdentityProvisioningInput{TenantID: housekeepingTenantA, TenantDisplayName: "Housekeeping tenant A", Issuer: "https://identity.plugins.example", TenancyMode: "dedicated", Subject: "plugin-author", ActorType: identity.ActorHuman, Roles: roles, Operator: "plugin-test", Reason: "synthetic plugin fixture", AcknowledgeOutbox: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantA, Subject: "plugin-author", Roles: roles, MembershipID: principal.MembershipID, RealmID: principal.RealmID, ActorType: principal.ActorType})
	ns, err := service.CreateNamespace(ctx, catalog.CreateNamespaceInput{Slug: "plugins", DisplayName: "Plugins"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := service.CreateResource(ctx, catalog.CreateResourceInput{NamespaceID: ns.ID, CanonicalIdentifier: "urn:air:catalog.example.com:plugins:database-tools", Kind: catalog.KindPlugin})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("encoded inspection admission", func(t *testing.T) {
		bounded, createErr := service.CreateResource(ctx, catalog.CreateResourceInput{NamespaceID: ns.ID, CanonicalIdentifier: "urn:air:catalog.example.com:plugins:encoded-tools", Kind: catalog.KindPlugin})
		if createErr != nil {
			t.Fatal(createErr)
		}
		original := fetch.body
		defer func() { fetch.body = original }()
		for _, count := range []int{7, 8} {
			var data bytes.Buffer
			z := zip.NewWriter(&data)
			for i := -1; i < count; i++ {
				name, body := "plugin.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"encoded-tools"}`
				if i >= 0 {
					name = fmt.Sprintf("skills/skill-%d/SKILL.md", i)
					body = fmt.Sprintf("---\nname: skill-%d\ndescription: Synthetic metadata.\n---\n", i) + strings.Repeat("\x01", 450_000)
				}
				// Store avoids a compression-ratio rejection masking JSON growth.
				w, writeErr := z.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
				if writeErr != nil {
					t.Fatal(writeErr)
				}
				if _, writeErr = w.Write([]byte(body)); writeErr != nil {
					t.Fatal(writeErr)
				}
			}
			if closeErr := z.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
			fetch.body = data.Bytes()
			_, revision, evidence, createErr := service.CreateVersion(ctx, bounded.ID, catalog.CreateVersionInput{Version: fmt.Sprintf("0.%d.0", count), Representations: []catalog.RepresentationInput{{Role: catalog.RepresentationPrimary, MediaType: catalog.PluginMediaZIP, URL: "https://packages.example.com/encoded.zip"}}})
			if count == 8 {
				if catalog.ErrorCodeOf(createErr) != catalog.CodeValidationFailed {
					t.Fatalf("oversized inspection must fail domain admission: %v", createErr)
				}
				continue
			}
			if createErr != nil || evidence.State != catalog.EvidenceComplete {
				t.Fatalf("bounded escaped documents rejected: %v", createErr)
			}
			var storedBytes int
			if queryErr := db.operator.QueryRow(ctx, `SELECT octet_length(plugin_inspection::text) FROM representations WHERE tenant_id=$1 AND version_revision_id=$2`, housekeepingTenantA, revision.ID).Scan(&storedBytes); queryErr != nil {
				t.Fatal(queryErr)
			}
			if storedBytes <= 18<<20 || storedBytes > catalog.MaxPluginInspectionBytes {
				t.Fatalf("unexpected encoded PostgreSQL size: %d", storedBytes)
			}
		}
		versions, listErr := repo.ListVersions(ctx, housekeepingTenantA, bounded.ID, catalog.HistoryPageRequest{Limit: 10})
		if listErr != nil || len(versions) != 1 || versions[0].Version != "0.7.0" {
			t.Fatalf("failed admission left partial version state: count=%d err=%v", len(versions), listErr)
		}
	})
	t.Run("decoded metadata admission", func(t *testing.T) {
		original := fetch.body
		defer func() { fetch.body = original }()
		for _, files := range [][]struct{ name, body string }{
			{{"plugin.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"database-tools","description":"bad\u0000description"}`}},
			{{"plugin.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"database-tools"}`}, {"mcp.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"bad\u0000name":{"type":"stdio","command":"server"}}}`}},
		} {
			var data bytes.Buffer
			z := zip.NewWriter(&data)
			for _, file := range files {
				w, writeErr := z.Create(file.name)
				if writeErr != nil {
					t.Fatal(writeErr)
				}
				if _, writeErr = w.Write([]byte(file.body)); writeErr != nil {
					t.Fatal(writeErr)
				}
			}
			if closeErr := z.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
			fetch.body = data.Bytes()
			if _, _, _, createErr := service.CreateVersion(ctx, resource.ID, catalog.CreateVersionInput{Version: "0.0.1", Representations: []catalog.RepresentationInput{{Role: catalog.RepresentationPrimary, MediaType: catalog.PluginMediaZIP, URL: "https://packages.example.com/database-tools.zip"}}}); catalog.ErrorCodeOf(createErr) != catalog.CodeValidationFailed {
				t.Fatalf("unrepresentable metadata was not rejected before persistence: %v", createErr)
			}
		}
		versions, listErr := repo.ListVersions(ctx, housekeepingTenantA, resource.ID, catalog.HistoryPageRequest{Limit: 10})
		if listErr != nil || len(versions) != 0 {
			t.Fatalf("invalid metadata left partial versions: count=%d err=%v", len(versions), listErr)
		}
	})
	_, revision, evidence, err := service.CreateVersion(ctx, resource.ID, catalog.CreateVersionInput{Version: "1.0.0", Representations: []catalog.RepresentationInput{{Role: catalog.RepresentationPrimary, MediaType: catalog.PluginMediaZIP, URL: "https://packages.example.com/database-tools.zip"}}})
	if err != nil {
		t.Fatal(err)
	}
	if evidence.State != catalog.EvidenceComplete {
		t.Fatalf("inspection blocked: %+v", evidence.Findings)
	}
	t.Run("restore accepts retained inspection without archive bytes", func(t *testing.T) {
		tx, beginErr := db.operator.BeginTx(t.Context(), pgx.TxOptions{AccessMode: pgx.ReadOnly})
		if beginErr != nil {
			t.Fatal(beginErr)
		}
		defer rollbackForCleanup(t.Context(), tx)()
		records, more, scanErr := scanRestoreVersionRevisions(t.Context(), tx, housekeepingTenantA, "", 16)
		if scanErr != nil || more {
			t.Fatalf("restore scan: more=%t err=%v", more, scanErr)
		}
		for _, record := range records {
			if record.key == revision.ID {
				if record.finding != nil {
					t.Fatalf("valid retained Plugin rejected: %s", record.finding.Code)
				}
				return
			}
		}
		t.Fatal("restore scan omitted the inspected Plugin")
	})
	read, _, _, err := service.GetVersionRevision(ctx, revision.ID)
	readJSON, marshalErr := json.Marshal(read.Representations)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	writtenJSON, marshalErr := json.Marshal(revision.Representations)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if err != nil || !bytes.Equal(readJSON, writtenJSON) {
		t.Fatalf("inspection round trip: %v\nread=%+v\nwritten=%+v", err, read.Representations, revision.Representations)
	}
	if _, _, _, err = service.RevalidateVersionRevision(ctx, revision.ID); err != nil {
		t.Fatal(err)
	}
	publisher, err := service.CreatePublisher(ctx, catalog.CreatePublisherInput{DisplayName: "Plugin publisher", Domains: []string{"catalog.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.ApprovePublisherPublicationDomain(ctx, publisher.ID, "catalog.example.com", "Synthetic test", publisher.Revision); err != nil {
		t.Fatal(err)
	}
	listing, err := service.CreateListing(ctx, catalog.CreateListingInput{NamespaceID: ns.ID, ResourceID: resource.ID})
	if err != nil {
		t.Fatal(err)
	}
	listing, _, err = service.CreateListingRevision(ctx, listing.ID, catalog.CreateListingRevisionInput{VersionRevisionID: revision.ID, PublisherID: publisher.ID, DisplayName: "Database Plugin tools", Description: "Searchable synthetic Plugin", Visibility: catalog.VisibilityTenant, Tags: []string{"plugin-test"}}, listing.Revision)
	if err != nil {
		t.Fatal(err)
	}
	submission, err := service.Submit(ctx, listing.ID)
	if err != nil {
		t.Fatal(err)
	}
	reviewer, err := ProvisionIdentity(t.Context(), db.operator, IdentityProvisioningInput{TenantID: housekeepingTenantA, TenantDisplayName: "Housekeeping tenant A", Issuer: "https://identity.plugins.example", TenancyMode: "dedicated", Subject: "plugin-reviewer", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleReviewer}, Operator: "plugin-test", Reason: "Independent synthetic review", AcknowledgeOutbox: true})
	if err != nil {
		t.Fatal(err)
	}
	reviewContext := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantA, Subject: "plugin-reviewer", Roles: []identity.Role{identity.RoleReviewer}, MembershipID: reviewer.MembershipID, RealmID: reviewer.RealmID, ActorType: reviewer.ActorType})
	if _, _, err = service.DecideSubmission(reviewContext, submission.ID, "approve", "Inspected metadata only", submission.Revision); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = service.Publish(ctx, listing.ID, catalog.PublishInput{SubmissionID: submission.ID}, listing.Revision); err != nil {
		t.Fatal(err)
	}
	page, err := service.Search(ctx, catalog.SearchQuery{Text: "Plugin", Kinds: []catalog.Kind{catalog.KindPlugin}, Tags: []string{"plugin-test"}})
	if err != nil || len(page.Items) != 1 || page.Items[0].Resource.ID != resource.ID {
		t.Fatalf("PostgreSQL Plugin search: %+v %v", page, err)
	}
	// A fresh repository/service must read durable evidence, not an in-memory
	// inspection cache. Revalidation changes the current run, never sealed bytes
	// or the evidence an independent reviewer approved.
	reopened, err := NewPostgresRepository(db.api)
	if err != nil {
		t.Fatal(err)
	}
	options.Repository = reopened
	service, err = catalog.NewService(options)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := service.GetSubmissionEvidence(ctx, submission.ID)
	if err != nil {
		t.Fatal(err)
	}
	// An archive comment changes the checksum while leaving the ZIP valid.
	changed := append([]byte(nil), archive.Bytes()...)
	changed[len(changed)-2] = 1
	changed = append(changed, 'x')
	if _, err := zip.NewReader(bytes.NewReader(changed), int64(len(changed))); err != nil {
		t.Fatalf("invalid drift fixture: %v", err)
	}
	previousID := bound.ID
	for _, step := range []struct {
		body  []byte
		state catalog.EvidenceState
	}{{changed, catalog.EvidenceBlocked}, {archive.Bytes(), catalog.EvidenceComplete}} {
		fetch.body = step.body
		c.Advance(time.Minute)
		fetch.at = c.Now()
		_, expected, _, err := service.RevalidateVersionRevision(ctx, revision.ID)
		if err != nil || expected.State != step.state || expected.ID == previousID {
			t.Fatalf("Plugin %s evidence: %+v %v", step.state, expected, err)
		}
		if step.state == catalog.EvidenceBlocked && !slices.ContainsFunc(expected.Findings, func(f catalog.Finding) bool { return f.Code == "EXTERNAL_CONTENT_CHANGED" }) {
			t.Fatalf("missing archive drift finding: %+v", expected.Findings)
		}
		previousID = expected.ID
		retained, latest, err := reopened.GetVersionRevision(ctx, housekeepingTenantA, revision.ID)
		storedJSON, marshalErr := json.Marshal(retained.Representations)
		if err != nil || marshalErr != nil || latest.ID != expected.ID || latest.State != expected.State || !bytes.Equal(storedJSON, writtenJSON) || retained.ContentSetDigest != revision.ContentSetDigest {
			t.Fatalf("Plugin durable validation/evidence changed: %+v %v %v", latest, err, marshalErr)
		}
		historical, err := service.GetSubmissionEvidence(ctx, submission.ID)
		beforeJSON, marshalErr := json.Marshal(bound)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		afterJSON, marshalErr := json.Marshal(historical)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if err != nil || !bytes.Equal(beforeJSON, afterJSON) {
			t.Fatalf("Plugin review evidence rewritten: %+v %v", historical, err)
		}
	}
	// Changed archive bytes belong to a new immutable revision. They must not
	// replace the version revision already referenced by the published listing.
	version, err := service.GetVersion(ctx, revision.ResourceVersionID)
	if err != nil {
		t.Fatal(err)
	}
	fetch.body = changed
	input := []catalog.RepresentationInput{{Role: catalog.RepresentationPrimary, MediaType: catalog.PluginMediaZIP, URL: "https://packages.example.com/database-tools.zip"}}
	if _, _, _, err := service.CreateVersionRevision(ctx, version.ID, input, version.Revision); catalog.ErrorCodeOf(err) != catalog.CodeVersionSealed {
		t.Fatalf("sealed Plugin version accepted replacement: %v", err)
	}
	fetch.body = archive.Bytes()
	version, draft, draftEvidence, err := service.CreateVersion(ctx, resource.ID, catalog.CreateVersionInput{Version: "2.0.0", Representations: input})
	if err != nil || draftEvidence.State != catalog.EvidenceComplete {
		t.Fatalf("new Plugin draft version: %+v %v", draft, err)
	}
	fetch.body = changed
	updated, replacement, replacementEvidence, err := service.CreateVersionRevision(ctx, version.ID, input, version.Revision)
	if err != nil || updated.Revision != version.Revision+1 || replacement.ID == draft.ID || replacement.ContentSetDigest == draft.ContentSetDigest || replacementEvidence.State != catalog.EvidenceComplete {
		t.Fatalf("Plugin replacement revision: %+v %+v %v", updated, replacement, err)
	}
	if _, _, _, err := service.CreateVersionRevision(ctx, version.ID, input, version.Revision); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("stale Plugin revision accepted: %v", err)
	}
	for _, expected := range []catalog.VersionRevision{revision, draft, replacement} {
		retained, _, err := reopened.GetVersionRevision(ctx, housekeepingTenantA, expected.ID)
		actualJSON, marshalErr := json.Marshal(retained.Representations)
		if err != nil || marshalErr != nil {
			t.Fatalf("read retained Plugin revision: %v %v", err, marshalErr)
		}
		expectedJSON, err := json.Marshal(expected.Representations)
		if err != nil || !bytes.Equal(actualJSON, expectedJSON) || retained.ContentSetDigest != expected.ContentSetDigest {
			t.Fatalf("Plugin replacement changed retained history: %v", err)
		}
	}
	page, err = service.Search(ctx, catalog.SearchQuery{Kinds: []catalog.Kind{catalog.KindPlugin}, Tags: []string{"plugin-test"}})
	if err != nil || len(page.Items) != 1 || page.Items[0].VersionRevision.ID != revision.ID {
		t.Fatalf("Plugin draft replacement changed published content: %+v %v", page, err)
	}
	if _, _, err = repo.GetVersionRevision(t.Context(), housekeepingTenantB, revision.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("cross-tenant read: %v", err)
	}
	worker, err := NewPostgresRepository(db.worker)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = worker.GetVersionRevision(t.Context(), housekeepingTenantB, revision.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("worker cross-tenant read: %v", err)
	}
	if _, _, err = worker.GetVersionRevision(t.Context(), housekeepingTenantA, revision.ID); err != nil {
		t.Fatal(err)
	}
	for _, pool := range []*PostgresRepository{repo, worker} {
		_, err = withTenantRead(t.Context(), pool.pool, housekeepingTenantA, func(tx pgx.Tx) (int, error) {
			_, e := tx.Exec(t.Context(), "UPDATE representations SET plugin_inspection=plugin_inspection WHERE tenant_id=$1 AND id=$2", housekeepingTenantA, revision.Representations[0].ID)
			return 0, e
		})
		if err == nil {
			t.Fatal("runtime can rewrite inspection")
		}
	}
	if _, err = db.operator.Exec(t.Context(), "UPDATE representations SET plugin_inspection=plugin_inspection WHERE tenant_id=$1 AND id=$2", housekeepingTenantA, revision.Representations[0].ID); err == nil {
		t.Fatal("owner bypassed immutable history")
	}
}
