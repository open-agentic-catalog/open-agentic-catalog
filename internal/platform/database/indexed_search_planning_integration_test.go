//go:build integration

package database

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func TestPostgresIndexedSearchJITIsTransactionLocal(t *testing.T) {
	db, repo, _, _ := newSearchCostFixture(t)
	ctx, _, err := identity.WithCollectionContext(semanticDisclosureActor(t, db, housekeepingTenantA))
	if err != nil {
		t.Fatal(err)
	}
	var beforePID uint32
	var beforeJIT, beforeMode string
	if err := repo.pool.QueryRow(ctx, `SELECT pg_backend_pid(),current_setting('jit'),current_setting('plan_cache_mode')`).Scan(&beforePID, &beforeJIT, &beforeMode); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("abort read-only fixture transaction")
	for _, abort := range []bool{false, true} {
		_, err := withTenantCollectionRead(ctx, repo.pool, housekeepingTenantA, func(tx pgx.Tx) (bool, error) {
			page, err := searchIndexedCatalogTx(ctx, tx, housekeepingTenantA, catalog.FederatedCatalogSearchRequest{Limit: 20})
			if err != nil {
				return false, err
			}
			if page.IndexedTotal != 0 || page.Indexed == nil || len(page.Indexed) != 0 {
				t.Fatal("empty indexed result changed")
			}
			var pid uint32
			var jit, mode string
			if err := tx.QueryRow(ctx, `SELECT pg_backend_pid(),current_setting('jit'),current_setting('plan_cache_mode')`).Scan(&pid, &jit, &mode); err != nil {
				return false, err
			}
			if pid != beforePID || jit != "off" || mode != beforeMode {
				t.Fatal("indexed selection changed its connection or operator plan mode", pid, jit, mode)
			}
			if abort {
				return false, sentinel
			}
			return true, nil
		})
		if abort && !errors.Is(err, sentinel) || !abort && err != nil {
			t.Fatal(err)
		}
		var afterPID uint32
		var afterJIT, afterMode string
		if err := repo.pool.QueryRow(ctx, `SELECT pg_backend_pid(),current_setting('jit'),current_setting('plan_cache_mode')`).Scan(&afterPID, &afterJIT, &afterMode); err != nil {
			t.Fatal(err)
		}
		if afterPID != beforePID || afterJIT != beforeJIT || afterMode != beforeMode {
			t.Fatal("indexed planning escaped commit or rollback", afterPID, afterJIT, afterMode)
		}
	}
}
