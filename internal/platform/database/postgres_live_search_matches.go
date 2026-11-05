package database

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// identityProbeParameters retains typed absent-version identities. The caller
// always selects at most 50 identities at SQL selection, without documents.
func identityProbeParameters(probes []catalog.IndexedIdentity) ([]any, error) {
	if len(probes) > catalog.LiveSearchWindow {
		return nil, catalog.E(catalog.CodeInvalidArgument, "identity probe exceeds its bound")
	}
	identifiers, versions, present := []string{}, []string{}, []bool{}
	for _, probe := range probes {
		identifiers = append(identifiers, probe.Identifier)
		versions = append(versions, probe.Version)
		present = append(present, probe.VersionPresent)
	}
	return []any{identifiers, versions, present}, nil
}

func localLiveMatches(ctx context.Context, tx pgx.Tx, tenant string, request catalog.CatalogSearchRepositoryRequest, generation string) (catalog.CatalogSearchRepositoryPage, error) {
	probes, err := identityProbeParameters(request.IdentityProbes)
	if err != nil {
		return catalog.CatalogSearchRepositoryPage{}, err
	}
	cte, _ := catalogSearchStatements(request.Query)
	statement := cte + `SELECT DISTINCT records.canonical_identifier,version.version,true
  FROM matched
  JOIN records ON records.release_id=matched.release_id
  JOIN listing_releases release ON release.tenant_id=$1 AND release.id=matched.release_id
  JOIN resource_version_revisions revision ON revision.tenant_id=$1 AND revision.id=release.version_revision_id
  JOIN resource_versions version ON version.tenant_id=$1 AND version.id=revision.resource_version_id
  JOIN unnest($11::text[],$12::text[],$13::boolean[]) probe(identifier,version,present)
    ON probe.identifier=records.canonical_identifier AND probe.version=version.version AND probe.present
  LIMIT 51`
	parameters := append(catalogSearchParameters(tenant, request.Query), probes...)
	matches, err := readLiveMatches(ctx, tx, collectionStatement(ctx, statement), parameters)
	return catalog.CatalogSearchRepositoryPage{Generation: generation, Matches: matches}, err
}

func readLiveMatches(ctx context.Context, tx pgx.Tx, statement string, parameters []any) ([]catalog.IndexedIdentity, error) {
	rows, err := tx.Query(ctx, statement, parameters...)
	if err != nil {
		return nil, translateError("query bounded live identity matches", err)
	}
	defer rows.Close()
	matches := []catalog.IndexedIdentity{}
	for rows.Next() {
		var match catalog.IndexedIdentity
		if err := rows.Scan(&match.Identifier, &match.Version, &match.VersionPresent); err != nil {
			return nil, translateError("scan bounded live identity matches", err)
		}
		matches = append(matches, match)
		if len(matches) > catalog.LiveSearchWindow {
			return nil, catalog.E(catalog.CodeInternal, "live identity projection exceeded its bound")
		}
	}
	return matches, translateError("iterate bounded live identity matches", rows.Err())
}
