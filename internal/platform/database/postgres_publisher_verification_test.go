package database

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// These inert transactions test decoding and failure handling. The integration
// tests separately establish SQL, authorization, locking and rollback behavior.
func TestPublisherVerificationRepositoryDecodingAndWriteFailures(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	const tenant = "00000000-0000-7000-8000-000000000001"
	p := catalog.Publisher{ID: "00000000-0000-7000-8000-000000000022", TenantID: tenant, Domains: []string{"example.com"}, Revision: 1, CreatedAt: at, UpdatedAt: at}
	v := catalog.PublisherDomainVerification{ID: "00000000-0000-7000-8000-000000000023", TenantID: tenant, PublisherID: p.ID, Domain: "example.com", Revision: 1, ChallengeID: "00000000-0000-7000-8000-000000000024", Nonce: strings.Repeat("A", 43), ChallengeIssuedAt: at, ChallengeExpiresAt: at.Add(time.Hour), CreatedAt: at, UpdatedAt: at}
	row := func() pgx.Row {
		return scriptedValueRow([]any{v.ID, tenant, p.ID, v.Domain, v.Revision, v.ChallengeID, v.Nonce, &v.ChallengeIssuedAt, &v.ChallengeExpiresAt, false, v.ChallengeID, &at, &v.ChallengeExpiresAt, &at, v.ChallengeID, "matched", at, at})
	}
	decoded, err := readPublisherVerification(t.Context(), &identityScriptedTx{queryRows: []pgx.Row{row()}}, tenant, p.ID, v.Domain, true)
	if err != nil || decoded.Nonce != v.Nonce || !decoded.VerifiedAt.Equal(at) || decoded.LastCheckOutcome != "matched" {
		t.Fatalf("decoded evidence lost exact fields: %+v %v", decoded, err)
	}
	for _, failure := range []error{pgx.ErrNoRows, errors.New("inert read failure")} {
		got, readErr := readPublisherVerification(t.Context(), &identityScriptedTx{queryRows: []pgx.Row{scannerFunc(func(...any) error { return failure })}}, tenant, p.ID, v.Domain, false)
		if errors.Is(failure, pgx.ErrNoRows) {
			if readErr != nil || got.ID != "" || got.Domain != v.Domain {
				t.Fatal("absent state lost binding", readErr)
			}
		} else if catalog.ErrorCodeOf(readErr) != catalog.CodeDependencyUnavailable {
			t.Fatal("read failure classification", readErr)
		}
	}
	evidence, err := audit.NewRecord("00000000-0000-7000-8000-000000000090", tenant, audit.Principal{ID: "catalog-api", Type: audit.PrincipalServiceExecutor}, "publisher.domain_challenge_issued", "publisher", p.ID, "Issue local challenge", audit.Execution{}, map[string]any{"domain": v.Domain, "verificationId": v.ID, "revision": v.Revision, "method": "dns_txt", "challengeId": v.ChallengeID}, at)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"insert", "update", "write failure", "audit failure", "zero rows", "stale", "bad audit binding"} {
		t.Run(mode, func(t *testing.T) {
			pool := &pgxpool.Pool{}
			repo, err := NewPostgresRepository(pool)
			if err != nil {
				t.Fatal(err)
			}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{catalogPublisherRow(t, p), scannerFunc(func(...any) error { return pgx.ErrNoRows })}}
			next := v
			params := catalog.PublisherDomainMutation{State: next, Expected: catalog.PublisherDomainVersion{PublisherRevision: p.Revision}, Audit: evidence}
			expected := catalog.ErrorCode("")
			switch mode {
			case "update":
				tx.queryRows[1] = row()
				params.Expected = catalog.PublisherDomainVersion{ID: v.ID, Revision: v.Revision}
				params.State.Revision++
				params.State.ChallengeID = "00000000-0000-7000-8000-000000000025"
			case "write failure":
				tx.execErrors = []error{errors.New("inert write failure")}
				expected = catalog.CodeDependencyUnavailable
			case "audit failure":
				tx.execErrors = []error{nil, errors.New("inert audit failure")}
				expected = catalog.CodeDependencyUnavailable
			case "zero rows":
				tx.execTags = []pgconn.CommandTag{pgconn.NewCommandTag("UPDATE 0")}
				expected = catalog.CodePreconditionFailed
			case "stale":
				params.Expected.PublisherRevision++
				expected = catalog.CodePreconditionFailed
			case "bad audit binding":
				params.Audit.TargetID = "00000000-0000-7000-8000-000000000091"
				expected = catalog.CodeInternal
			}
			saved, err := repo.SavePublisherDomainVerification(identityRepositoryContext(pool, tx), params)
			if expected == "" && err != nil || expected != "" && catalog.ErrorCodeOf(err) != expected {
				t.Fatalf("%s error: %v", mode, err)
			}
			if expected == "" && (saved.ID != v.ID || len(tx.statements) != 2) {
				t.Fatalf("missing state and private audit: %+v %v", saved, tx.statements)
			}
			if mode == "zero rows" && len(tx.statements) != 1 {
				t.Fatal("zero-row write appended audit")
			}
		})
	}
	if verificationTimestamp(time.Time{}) != nil || verificationTimestamp(at) != at {
		t.Fatal("nullable timestamps changed")
	}
	_, err = readVerificationPublisher(context.Background(), &identityScriptedTx{queryRows: []pgx.Row{catalogPublisherRow(t, p)}}, tenant, p.ID, "undeclared.example", false)
	if catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatal("undeclared domain exposed", err)
	}
}
