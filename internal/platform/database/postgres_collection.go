// Collection policy is selected in the same snapshot as rows, counts and facets.
package database

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

var collectionPermissions = []identity.Permission{
	"namespace.read", "resource.read", "resource.read_history", "resource.read_content",
	"federation.discover", "job.read", "source.read", "source.read_evidence", "publication.read", "operations.reindex",
	"webhook.read", "webhook.read_evidence",
	"feedback.moderate", "ratings.moderate",
}

// initializeCollectionPolicy computes global decisions once. Scoped predicates
// remain indexed EXISTS queries; neither role arrays nor namespace lists are
// supplied by the client. The revision used to decode cursors must agree here.
func initializeCollectionPolicy(ctx context.Context, tx pgx.Tx, tenantID string) error {
	expected, selected := identity.CollectionRevision(ctx)
	if !selected {
		return nil
	}
	actor, ok := identity.FromContext(ctx)
	if !ok || actor.TenantID != tenantID {
		return catalog.E(catalog.CodeForbidden, "current collection principal is required")
	}

	checks := make([]identity.PermissionCheck, len(collectionPermissions))
	keys := make([]string, len(checks))
	scopes := make([]string, len(checks))
	for i, permission := range collectionPermissions {
		checks[i] = identity.PermissionCheck{Permission: permission}
		keys[i] = string(permission)
	}
	var active bool
	var decision identity.PermissionDecision
	// One tuple owns principal activity, policy revision and all finite global
	// decisions. Reuse the exact decision SQL, rather than a second evaluator.
	query := `SELECT state.revision,EXISTS(SELECT 1 FROM tenant_memberships m JOIN tenant_identity_realms r ON r.tenant_id=m.tenant_id AND r.id=m.realm_id AND r.state='active' WHERE m.tenant_id=$1 AND m.id=$2 AND m.realm_id=$3 AND m.subject=$4 AND m.actor_type=$5 AND m.state='active'),ARRAY(` + currentPermissionChecksSQL + `) FROM tenant_authorization_state state WHERE state.tenant_id=$1`
	if err := tx.QueryRow(ctx, query, tenantID, actor.MembershipID, actor.RealmID, actor.Subject, actor.ActorType, keys, scopes).Scan(&decision.Revision, &active, &decision.Allowed); err != nil {
		return catalog.Wrap(catalog.CodeDependencyUnavailable, "read collection policy", err)
	}
	if !active {
		return catalog.E(catalog.CodeForbidden, "a current Catalog principal is required")
	}
	if decision.Revision != expected {
		return catalog.E(catalog.CodeInvalidArgument, "collection authorization changed; restart the read")
	}
	if len(decision.Allowed) != len(checks) {
		return catalog.E(catalog.CodeDependencyUnavailable, "collection decision count is invalid")
	}
	globals := make(map[identity.Permission]bool, len(checks))
	for i, check := range checks {
		globals[check.Permission] = decision.Allowed[i]
	}
	data, err := json.Marshal(globals)
	if err != nil {
		return catalog.Wrap(catalog.CodeInternal, "encode collection decisions", err)
	}
	_, err = tx.Exec(ctx, `SELECT set_config('app.collection_permissions',$1,true)`, string(data))
	return translateError("bind collection decisions", err)
}

// collectionPermissionSQL receives only code-owned permission/scope expressions.
// A scalar subquery evaluates the global JSON decision once per statement.
// Sparse access uses an uncorrelated same-tenant namespace subquery. PostgreSQL
// can hash permitted namespaces instead of probing grants for each candidate.
// Direct/group paths never duplicate a row. Unmarked owner/worker
// repository calls retain their existing internal contract.
func collectionPermissionSQL(permission, namespace string) string {
	global := `(SELECT coalesce((nullif(current_setting('app.collection_permissions',true),'')::jsonb->>'` + permission + `')::boolean,false))`
	if namespace == "" {
		return "(" + global + ")"
	}
	return "(" + global + ` OR (nullif(current_setting('app.collection_permissions',true),'') IS NOT NULL AND ` + namespace + ` IN (
 SELECT ca.namespace_id FROM role_assignments ca
 JOIN role_definitions cd ON cd.tenant_id=ca.tenant_id AND cd.id=ca.role_definition_id AND cd.state='active'
 JOIN role_permissions cp ON cp.tenant_id=cd.tenant_id AND cp.role_definition_id=cd.id AND cp.permission='` + permission + `'
 WHERE ca.tenant_id=$1 AND ca.state='active' AND ca.namespace_id IS NOT NULL
 AND (ca.membership_id=nullif(current_setting('app.membership_id',true),'')::uuid OR EXISTS (
 SELECT 1 FROM access_group_members cm JOIN access_groups cg ON cg.tenant_id=cm.tenant_id AND cg.id=cm.group_id AND cg.realm_id=cm.realm_id AND cg.state='active'
 WHERE cm.tenant_id=ca.tenant_id AND cm.group_id=ca.group_id AND cm.membership_id=nullif(current_setting('app.membership_id',true),'')::uuid AND cm.state='active')))))`
}

// withTenantCollectionRead initializes policy only for actual selection. Exact
// permission probes and unrelated repository reads keep their narrow existing
// transactions; they do not repeat the global collection decision vector.
func withTenantCollectionRead[T any](ctx context.Context, pool *pgxpool.Pool, tenantID string, work func(pgx.Tx) (T, error)) (T, error) {
	return withTenantRead(ctx, pool, tenantID, func(tx pgx.Tx) (T, error) {
		if err := initializeCollectionPolicy(ctx, tx, tenantID); err != nil {
			var zero T
			return zero, err
		}
		return work(tx)
	})
}

// Owner/worker templates have no user-policy table references at all. PostgreSQL
// checks privileges even on a branch that never executes, so a true guard is
// insufficient. These finite code-owned fragments are reused in API selection;
// no runtime SQL, parser, role array or arbitrary namespace expression is accepted.
var ownerCollectionPredicates = strings.NewReplacer(
	collectionPermissionSQL("feedback.moderate", ""), "true",
	collectionPermissionSQL("ratings.moderate", ""), "true",
	collectionPermissionSQL("webhook.read", ""), "true",
	collectionPermissionSQL("webhook.read_evidence", ""), "true",
	collectionPermissionSQL("resource.read", "r.namespace_id"), "true",
	collectionPermissionSQL("resource.read_history", "r.namespace_id"), "true",
	collectionPermissionSQL("resource.read_content", "r.namespace_id"), "true",
	collectionPermissionSQL("resource.read", "local_listing.namespace_id"), "true",
	collectionPermissionSQL("resource.read", "listing.namespace_id"), "true",
	collectionPermissionSQL("resource.read", "resources.namespace_id"), "true",
	collectionPermissionSQL("resource.read_history", "p.namespace_id"), "true",
	collectionPermissionSQL("resource.read", "p.namespace_id"), "true",
	collectionPermissionSQL("resource.read_content", "p.namespace_id"), "true",
	collectionPermissionSQL("namespace.read", "namespaces.id"), "true",
	collectionPermissionSQL("federation.discover", ""), "true",
	collectionPermissionSQL("source.read", ""), "true",
	collectionPermissionSQL("source.read_evidence", ""), "true",
	collectionPermissionSQL("publication.read", ""), "true",
	collectionPermissionSQL("operations.reindex", ""), "true",
)

func collectionStatement(ctx context.Context, statement string) string {
	if _, selected := identity.CollectionRevision(ctx); selected {
		return statement
	}
	return ownerCollectionPredicates.Replace(statement)
}
