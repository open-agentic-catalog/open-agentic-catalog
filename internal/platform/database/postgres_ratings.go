package database

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/ratings"
)

type PostgresRatingsStore struct{ repository *PostgresRepository }

func NewPostgresRatingsStore(repository *PostgresRepository) (*PostgresRatingsStore, error) {
	if repository == nil {
		return nil, errors.New("PostgreSQL repository is required")
	}
	return &PostgresRatingsStore{repository}, nil
}

const ratingColumns = `vote.id,vote.tenant_id,vote.principal_key,vote.submitted_by,target.target,coalesce(vote.release_id::text,''),vote.entry_digest,vote.score,vote.consent,vote.state,vote.revision,vote.created_at,vote.updated_at`
const ratingJoin = ` FROM item_ratings vote JOIN rating_targets target ON target.tenant_id=vote.tenant_id AND target.target_key=vote.target_key `

func scanRating(row pgx.Row) (ratings.Record, error) {
	var r ratings.Record
	var raw []byte
	err := row.Scan(&r.ID, &r.TenantID, &r.PrincipalKey, &r.SubmittedBy, &raw, &r.ReleaseID, &r.EntryDigest, &r.Score, &r.Consent, &r.State, &r.Revision, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return r, translateError("read rating", err)
	}
	if err = json.Unmarshal(raw, &r.Target); err == nil {
		err = ratings.ValidateTarget(r.Target)
	}
	return r, err
}
func (p *PostgresRatingsStore) Get(ctx context.Context, tenant, id string) (ratings.Record, error) {
	return withTenantRead(ctx, p.repository.pool, tenant, func(tx pgx.Tx) (ratings.Record, error) {
		return scanRating(tx.QueryRow(ctx, `SELECT `+ratingColumns+ratingJoin+` WHERE vote.tenant_id=$1 AND vote.id=$2`, tenant, id))
	})
}
func (p *PostgresRatingsStore) Own(ctx context.Context, tenant string, target ratings.Target, owner string) (ratings.Record, error) {
	return withTenantRead(ctx, p.repository.pool, tenant, func(tx pgx.Tx) (ratings.Record, error) {
		return scanRating(tx.QueryRow(ctx, `SELECT `+ratingColumns+ratingJoin+` WHERE vote.tenant_id=$1 AND vote.target_key=$2 AND vote.principal_key=$3`, tenant, ratings.Key(target), owner))
	})
}
func (p *PostgresRatingsStore) List(ctx context.Context, tenant string, q ratings.ListInput) ([]ratings.Record, error) {
	if q.Limit < 1 || q.Limit > 101 {
		return nil, catalog.E(catalog.CodeInvalidArgument, "rating page is invalid")
	}
	return withTenantRead(ctx, p.repository.pool, tenant, func(tx pgx.Tx) ([]ratings.Record, error) {
		rows, err := tx.Query(ctx, `SELECT `+ratingColumns+ratingJoin+` WHERE vote.tenant_id=$1 AND ($2='' OR vote.principal_key=$2) AND ($3='' OR vote.state=$3) AND ($4='' OR vote.id>nullif($4,'')::uuid) ORDER BY vote.id LIMIT $5`, tenant, q.PrincipalKey, q.State, q.AfterID, q.Limit)
		if err != nil {
			return nil, translateError("list ratings", err)
		}
		defer rows.Close()
		out := []ratings.Record{}
		for rows.Next() {
			r, err := scanRating(rows)
			if err != nil {
				return nil, err
			}
			out = append(out, r)
		}
		return out, translateError("iterate ratings", rows.Err())
	})
}
func (p *PostgresRatingsStore) Save(ctx context.Context, r ratings.Record, expected int64, admission ratings.Admission, e audit.Record) (ratings.Record, error) {
	return withTenantTx(ctx, p.repository.pool, r.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (ratings.Record, error) {
		if r.State == "active" {
			if err := admitRatingWrite(ctx, tx, r, admission); err != nil {
				return ratings.Record{}, err
			}
		}
		raw, err := json.Marshal(r.Target)
		if err != nil {
			return ratings.Record{}, err
		}
		key := ratings.Key(r.Target)
		_, err = tx.Exec(ctx, `INSERT INTO rating_targets(tenant_id,target_key,target,resource_id,version_revision_id,source_id) VALUES($1,$2,$3,nullif($4,'')::uuid,nullif($5,'')::uuid,nullif($6,'')::uuid) ON CONFLICT(tenant_id,target_key) DO NOTHING`, r.TenantID, key, raw, r.Target.ResourceID, r.Target.VersionRevisionID, r.Target.SourceID)
		if err != nil {
			return ratings.Record{}, translateError("create rating target", err)
		}
		var retained []byte
		if err = tx.QueryRow(ctx, `SELECT target FROM rating_targets WHERE tenant_id=$1 AND target_key=$2 FOR UPDATE`, r.TenantID, key).Scan(&retained); err != nil {
			return ratings.Record{}, translateError("lock rating target", err)
		}
		var target ratings.Target
		if err = json.Unmarshal(retained, &target); err != nil || target != r.Target {
			return ratings.Record{}, catalog.E(catalog.CodeConflict, "rating target identity mismatch")
		}
		old, err := scanRating(tx.QueryRow(ctx, `SELECT `+ratingColumns+ratingJoin+` WHERE vote.tenant_id=$1 AND vote.target_key=$2 AND vote.principal_key=$3 FOR UPDATE OF vote`, r.TenantID, key, r.PrincipalKey))
		if err != nil && catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
			return ratings.Record{}, err
		}
		if err = ratings.ValidateSave(old, r, expected, e); err != nil {
			return ratings.Record{}, err
		}
		if expected == 0 {
			_, err = tx.Exec(ctx, `INSERT INTO item_ratings(tenant_id,id,target_key,principal_key,submitted_by,score,consent,state,release_id,entry_digest,revision,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,nullif($9,'')::uuid,$10,$11,$12,$13)`, r.TenantID, r.ID, key, r.PrincipalKey, r.SubmittedBy, r.Score, r.Consent, r.State, r.ReleaseID, r.EntryDigest, r.Revision, r.CreatedAt, r.UpdatedAt)
		} else {
			_, err = tx.Exec(ctx, `UPDATE item_ratings SET submitted_by=$3,score=$4,consent=$5,state=$6,release_id=nullif($7,'')::uuid,entry_digest=$8,revision=$9,updated_at=$10 WHERE tenant_id=$1 AND id=$2`, r.TenantID, r.ID, r.SubmittedBy, r.Score, r.Consent, r.State, r.ReleaseID, r.EntryDigest, r.Revision, r.UpdatedAt)
		}
		if err != nil {
			return ratings.Record{}, translateError("save rating", err)
		}
		if err = insertAuditEvent(ctx, tx, e); err != nil {
			return ratings.Record{}, err
		}
		return r, nil
	})
}
func admitRatingWrite(ctx context.Context, tx pgx.Tx, r ratings.Record, a ratings.Admission) error {
	actor, ok := identity.FromContext(ctx)
	key, err := ratings.PrincipalKey(actor)
	if !ok || err != nil || key != r.PrincipalKey || actor.MembershipID != r.SubmittedBy {
		return catalog.E(catalog.CodeForbidden, "rating requires the current human identity")
	}
	var active bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tenant_memberships WHERE tenant_id=$1 AND id=$2 AND state='active' AND actor_type='human')`, r.TenantID, r.SubmittedBy).Scan(&active); err != nil {
		return translateError("check rating member", err)
	}
	if !active {
		return catalog.E(catalog.CodeForbidden, "rating membership is inactive")
	}
	if a.Target != r.Target {
		return catalog.E(catalog.CodePreconditionFailed, "rating target admission changed")
	}
	if r.Target.Origin == "local" {
		var namespace string
		err = tx.QueryRow(ctx, `SELECT resource.namespace_id::text FROM listing_releases release JOIN catalog_listings listing ON listing.tenant_id=release.tenant_id AND listing.id=release.listing_id AND listing.current_release_id=release.id JOIN resources resource ON resource.tenant_id=listing.tenant_id AND resource.id=listing.resource_id WHERE release.tenant_id=$1 AND release.id=$2 AND release.version_revision_id=$3 AND resource.id=$4 AND resource.deleted_at IS NULL AND release.state IN ('published','deprecated') FOR SHARE OF release,listing,resource`, r.TenantID, r.ReleaseID, r.Target.VersionRevisionID, r.Target.ResourceID).Scan(&namespace)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && namespace != a.NamespaceID {
			return catalog.E(catalog.CodePreconditionFailed, "rating release visibility changed")
		}
		if err != nil {
			return translateError("lock current rated release", err)
		}
	}
	if r.Target.Origin == "foreign" {
		source, err := readFederationSource(ctx, tx, r.TenantID, r.Target.SourceID, true)
		if err != nil {
			return err
		}
		if source.Revision != a.SourceRevision || federation.EffectivePresentationGeneration(source) != r.Target.PresentationGeneration || source.Status != federation.SourceActive && source.Status != federation.SourceError || a.Live && (source.Status != federation.SourceActive || !source.Live.Enabled) {
			return catalog.E(catalog.CodePreconditionFailed, "rating source changed")
		}
		if !a.Live {
			var eligible bool
			err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM observations WHERE tenant_id=$1 AND source_id=$2 AND identifier=$3 AND version_present=$4 AND coalesce(version,'')=$5 AND kind=$6 AND normalized_entry_digest=$7 AND freshness='current' AND completeness='complete')`, r.TenantID, r.Target.SourceID, r.Target.Identifier, r.Target.VersionPresent, r.Target.Version, r.Target.Kind, a.EntryDigest).Scan(&eligible)
			if err != nil {
				return translateError("check rating observation", err)
			}
			if !eligible {
				return catalog.E(catalog.CodePreconditionFailed, "rating observation changed")
			}
		}
	}
	return nil
}
func (p *PostgresRatingsStore) Aggregates(ctx context.Context, tenant string, targets []ratings.Target) (map[string]ratings.Aggregate, error) {
	if len(targets) > catalog.MaxPublicationRecords {
		return nil, catalog.E(catalog.CodeWorkLimitExceeded, "rating batch is too large")
	}
	keys := make([]string, len(targets))
	for i, t := range targets {
		if err := ratings.ValidateTarget(t); err != nil {
			return nil, err
		}
		keys[i] = ratings.Key(t)
	}
	return withTenantRead(ctx, p.repository.pool, tenant, func(tx pgx.Tx) (map[string]ratings.Aggregate, error) {
		rows, err := tx.Query(ctx, `SELECT target_key,vote_count,score_sum,shared_count,shared_sum FROM rating_targets WHERE tenant_id=$1 AND target_key=ANY($2::text[])`, tenant, keys)
		if err != nil {
			return nil, translateError("read rating aggregates", err)
		}
		defer rows.Close()
		out := map[string]ratings.Aggregate{}
		for rows.Next() {
			var key string
			var a ratings.Aggregate
			if err = rows.Scan(&key, &a.Count, &a.Sum, &a.SharedCount, &a.SharedSum); err != nil {
				return nil, translateError("scan rating totals", err)
			}
			if !a.Valid() {
				return nil, catalog.E(catalog.CodeInternal, "invalid retained rating totals")
			}
			out[key] = a
		}
		return out, translateError("iterate rating totals", rows.Err())
	})
}
func scanRatingSharing(row pgx.Row) (ratings.Sharing, error) {
	var r ratings.Sharing
	err := row.Scan(&r.ID, &r.TenantID, &r.Host, &r.Audience, &r.Enabled, &r.Revision, &r.UpdatedAt)
	return r, translateError("read rating sharing", err)
}
func (p *PostgresRatingsStore) GetSharing(ctx context.Context, tenant, host, audience string) (ratings.Sharing, error) {
	return withTenantRead(ctx, p.repository.pool, tenant, func(tx pgx.Tx) (ratings.Sharing, error) {
		return scanRatingSharing(tx.QueryRow(ctx, `SELECT id,tenant_id,host,audience,enabled,revision,updated_at FROM rating_sharing WHERE tenant_id=$1 AND host=$2 AND audience=$3`, tenant, host, audience))
	})
}
func (p *PostgresRatingsStore) SaveSharing(ctx context.Context, r ratings.Sharing, expected int64, e audit.Record) (ratings.Sharing, error) {
	return withTenantTx(ctx, p.repository.pool, r.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (ratings.Sharing, error) {
		var old ratings.Sharing
		var err error
		if expected > 0 {
			old, err = scanRatingSharing(tx.QueryRow(ctx, `SELECT id,tenant_id,host,audience,enabled,revision,updated_at FROM rating_sharing WHERE tenant_id=$1 AND host=$2 AND audience=$3 FOR UPDATE`, r.TenantID, r.Host, r.Audience))
			if err != nil {
				return ratings.Sharing{}, err
			}
		}
		if err = ratings.ValidateSharingSave(old, r, expected, e); err != nil {
			return ratings.Sharing{}, err
		}
		if expected == 0 {
			_, err = tx.Exec(ctx, `INSERT INTO rating_sharing(tenant_id,id,host,audience,enabled,revision,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, r.TenantID, r.ID, r.Host, r.Audience, r.Enabled, r.Revision, r.UpdatedAt)
		} else {
			_, err = tx.Exec(ctx, `UPDATE rating_sharing SET enabled=$3,revision=$4,updated_at=$5 WHERE tenant_id=$1 AND id=$2`, r.TenantID, r.ID, r.Enabled, r.Revision, r.UpdatedAt)
		}
		if err != nil {
			return ratings.Sharing{}, translateError("save rating sharing", err)
		}
		if err = insertAuditEvent(ctx, tx, e); err != nil {
			return ratings.Sharing{}, err
		}
		return r, nil
	})
}
