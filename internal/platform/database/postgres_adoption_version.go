package database

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func (p *PostgresRepository) AdoptExternalVersion(ctx context.Context, pms catalog.AdoptExternalVersionParams) (catalog.ExternalVersionAdoption, error) {
	if err := catalog.ValidateAdoptedVersionBinding(pms); err != nil {
		return catalog.ExternalVersionAdoption{}, err
	}
	return withTenantTx(ctx, p.pool, pms.Resource.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (catalog.ExternalVersionAdoption, error) {
		if err := lockActiveResource(ctx, tx, pms.Resource.TenantID, pms.Resource.ID); err != nil {
			return catalog.ExternalVersionAdoption{}, err
		}
		proof := pms.VersionRevision.Adoption
		if proof == nil {
			return catalog.ExternalVersionAdoption{}, catalog.E(catalog.CodeInvalidArgument, "adoption provenance is required")
		}
		if err := lockAdoptionOccurrence(ctx, tx, pms.Resource.TenantID, proof, pms.SourceAdmissionRevision); err != nil {
			return catalog.ExternalVersionAdoption{}, err
		}
		var eligible bool
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM resources r
 JOIN resource_versions v ON v.tenant_id=r.tenant_id AND v.resource_id=r.id
 JOIN resource_version_revisions prior ON prior.tenant_id=v.tenant_id AND prior.resource_version_id=v.id
 JOIN observations original ON original.tenant_id=prior.tenant_id AND original.id=(prior.adoption_provenance->>'observationId')::uuid
 WHERE r.tenant_id=$1 AND r.id=$2 AND r.deleted_at IS NULL AND r.canonical_identifier=$3 AND r.kind=$4
 AND prior.id=$5 AND prior.adoption_provenance->>'sourceId'=$6 AND original.source_id=$6::uuid
 AND original.identifier=$3 AND original.kind=$4 AND original.version_present AND original.version<>'' AND original.version<>$7
 AND v.version<>$7)`, pms.Resource.TenantID, pms.Resource.ID, pms.Resource.CanonicalIdentifier, pms.Resource.Kind, pms.PriorVersionRevisionID, proof.SourceID, pms.Version.Version).Scan(&eligible)
		if err != nil {
			return catalog.ExternalVersionAdoption{}, translateError("check prior adoption", err)
		}
		if !eligible {
			return catalog.ExternalVersionAdoption{}, catalog.E(catalog.CodePreconditionFailed, "an exact same-source versioned prior adoption is required")
		}
		// The existing child writer joins the command transaction; it does not
		// create a second transaction or alter Resource/Listing pointers.
		bundle, err := createVersionInTx(ctx, tx, pms.CreateVersionParams)
		if err != nil {
			return catalog.ExternalVersionAdoption{}, err
		}
		return catalog.ExternalVersionAdoption{ResourceID: pms.Resource.ID, Version: bundle.version, VersionRevision: bundle.revision, Evidence: bundle.evidence}, nil
	})
}

func (p *PostgresRepository) FindVersionAdoptionTarget(ctx context.Context, tenant, source, identifier string, kind catalog.Kind, selectedVersion string) (catalog.AdoptionTarget, error) {
	return withTenantCollectionRead(ctx, p.pool, tenant, func(tx pgx.Tx) (catalog.AdoptionTarget, error) {
		var out catalog.AdoptionTarget
		err := tx.QueryRow(ctx, `SELECT r.id,r.namespace_id,prior.id,v.version FROM resources r
 JOIN resource_versions v ON v.tenant_id=r.tenant_id AND v.resource_id=r.id
 JOIN resource_version_revisions prior ON prior.tenant_id=v.tenant_id AND prior.resource_version_id=v.id
 JOIN observations original ON original.tenant_id=prior.tenant_id AND original.id=(prior.adoption_provenance->>'observationId')::uuid
 WHERE r.tenant_id=$1 AND r.deleted_at IS NULL AND r.canonical_identifier=$3 AND r.kind=$4
 AND prior.adoption_provenance->>'sourceId'=$2 AND original.source_id=$2::uuid AND original.identifier=$3 AND original.kind=$4 AND original.version_present AND original.version<>''
 AND v.version<>$5 AND `+collectionPermissionSQL("resource.edit", "r.namespace_id")+` LIMIT 1`, tenant, source, identifier, kind, selectedVersion).Scan(&out.ResourceID, &out.NamespaceID, &out.PriorVersionRevisionID, &out.Version)
		return out, translateError("read adoption destination", err)
	})
}

func (p *PostgresRepository) GetVersionAdoptionProof(ctx context.Context, tenant, resourceID, revisionID string) (catalog.ResourceVersion, catalog.AdoptionProvenance, error) {
	type result struct {
		v catalog.ResourceVersion
		p catalog.AdoptionProvenance
	}
	out, err := withTenantRead(ctx, p.pool, tenant, func(tx pgx.Tx) (result, error) {
		var out result
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT v.id,v.tenant_id,v.resource_id,v.version,revision.adoption_provenance FROM resource_versions v JOIN resource_version_revisions revision ON revision.tenant_id=v.tenant_id AND revision.resource_version_id=v.id WHERE v.tenant_id=$1 AND v.resource_id=$2 AND revision.id=$3 AND revision.adoption_provenance IS NOT NULL`, tenant, resourceID, revisionID).Scan(&out.v.ID, &out.v.TenantID, &out.v.ResourceID, &out.v.Version, &raw)
		if err != nil {
			return result{}, translateError("read exact prior adoption", err)
		}
		if err = json.Unmarshal(raw, &out.p); err != nil {
			return result{}, translateError("decode prior adoption", err)
		}
		return out, nil
	})
	return out.v, out.p, err
}
