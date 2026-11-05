package database

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func postgresMembershipRow(t *testing.T, membership identity.Membership) pgx.Row {
	t.Helper()
	return scannerFunc(func(destinations ...any) error {
		setScanDestination(t, destinations, 0, membership.ID)
		setScanDestination(t, destinations, 1, membership.TenantID)
		setScanDestination(t, destinations, 2, membership.RealmID)
		setScanDestination(t, destinations, 3, membership.Subject)
		setScanDestination(t, destinations, 4, membership.ActorType)
		setScanDestination(t, destinations, 5, membership.State)
		setScanDestination(t, destinations, 6, membership.Revision)
		setScanDestination(t, destinations, 7, membership.CreatedAt)
		setScanDestination(t, destinations, 8, membership.UpdatedAt)
		return nil
	})
}

func TestPostgresMembershipReadsAreBoundedAndTenantScoped(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	now := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	pool := &pgxpool.Pool{}
	repository, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresIdentityStore(repository)
	if err != nil {
		t.Fatal(err)
	}
	memberships := []identity.Membership{
		{ID: "00000000-0000-7000-8000-000000000010", TenantID: tenantID, RealmID: "00000000-0000-7000-8000-000000000100", Subject: "alpha", ActorType: identity.ActorHuman, State: identity.MembershipActive, Revision: 1, CreatedAt: now, UpdatedAt: now},
		{ID: "00000000-0000-7000-8000-000000000011", TenantID: tenantID, RealmID: "00000000-0000-7000-8000-000000000100", Subject: "service", ActorType: identity.ActorService, State: identity.MembershipSuspended, Revision: 2, CreatedAt: now, UpdatedAt: now},
	}
	rows := &identityScriptedRows{values: [][]any{
		{memberships[0].ID, tenantID, memberships[0].RealmID, memberships[0].Subject, memberships[0].ActorType, memberships[0].State, memberships[0].Revision, now, now},
		{memberships[1].ID, tenantID, memberships[1].RealmID, memberships[1].Subject, memberships[1].ActorType, memberships[1].State, memberships[1].Revision, now, now},
	}}
	listTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{{rows: rows}}}
	items, err := store.ListMemberships(identityRepositoryContext(pool, listTx), tenantID, "", "", 3)
	if err != nil || len(items) != 2 || items[1].State != identity.MembershipSuspended || !rows.closed {
		t.Fatalf("membership list = %#v rowsClosed=%t, %v", items, rows.closed, err)
	}

	getTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{postgresMembershipRow(t, memberships[1])}}
	got, err := store.GetMembership(identityRepositoryContext(pool, getTx), tenantID, memberships[1].ID)
	if err != nil || got.ID != memberships[1].ID || got.Revision != 2 {
		t.Fatalf("membership read = %#v, %v", got, err)
	}
}

func TestPostgresMembershipListPropagatesBoundedReadFailures(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	pool := &pgxpool.Pool{}
	repository, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresIdentityStore(repository)
	if err != nil {
		t.Fatal(err)
	}
	queryFailure := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{{err: errors.New("query unavailable")}}}
	if _, err := store.ListMemberships(identityRepositoryContext(pool, queryFailure), tenantID, "member", "00000000-0000-7000-8000-000000000010", 10); identity.ErrorCodeOf(err) != identity.CodeDependency {
		t.Fatalf("membership query failure = %v", err)
	}
	failedRows := &identityScriptedRows{err: errors.New("iteration interrupted")}
	iterationFailure := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{{rows: failedRows}}}
	if _, err := store.ListMemberships(identityRepositoryContext(pool, iterationFailure), tenantID, "", "", 10); identity.ErrorCodeOf(err) != identity.CodeDependency || !failedRows.closed {
		t.Fatalf("membership iteration failure = %v, rowsClosed=%t", err, failedRows.closed)
	}
}

func TestPostgresMembershipTransitionsAreAtomicAndPreserveFinalAdministrator(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	now := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	pool := &pgxpool.Pool{}
	repository, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresIdentityStore(repository)
	if err != nil {
		t.Fatal(err)
	}
	base := identity.Membership{ID: "00000000-0000-7000-8000-000000000010", TenantID: tenantID, RealmID: "00000000-0000-7000-8000-000000000100", Subject: "member", ActorType: identity.ActorHuman, State: identity.MembershipActive, Revision: 1, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour)}
	lockRow := func() pgx.Row {
		return scannerFunc(func(destinations ...any) error {
			setScanDestination(t, destinations, 0, tenantID)
			return nil
		})
	}
	boolRow := func(value bool) pgx.Row {
		return scannerFunc(func(destinations ...any) error {
			setScanDestination(t, destinations, 0, value)
			return nil
		})
	}
	countRow := func(value int) pgx.Row {
		return scannerFunc(func(destinations ...any) error {
			setScanDestination(t, destinations, 0, value)
			return nil
		})
	}
	evidence := currentTestIdentityEvidence(t, "identity.membership.suspended", "tenant_membership", base.ID, now)
	params := identity.ChangeMembershipParams{TenantID: tenantID, MembershipID: base.ID, ExpectedRevision: 1, Transition: identity.MembershipSuspend, Actor: "admin", Reason: evidence.Reason, At: now, Evidence: evidence}

	suspendTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{lockRow(), postgresMembershipRow(t, base), boolRow(false)}}
	suspended, err := store.ChangeMembership(identityRepositoryContext(pool, suspendTx), params)
	if err != nil || suspended.Membership.State != identity.MembershipSuspended || suspended.Membership.Revision != 2 || len(suspendTx.statements) != 4 {
		t.Fatalf("suspended membership = %#v, statements=%d, %v", suspended, len(suspendTx.statements), err)
	}

	params.ExpectedRevision = 2
	params.Transition = identity.MembershipReactivate
	params.Evidence = currentTestIdentityEvidence(t, "identity.membership.reactivated", "tenant_membership", base.ID, now)
	suspendedMembership := suspended.Membership
	reactivateTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{lockRow(), postgresMembershipRow(t, suspendedMembership), boolRow(true)}}
	reactivated, err := store.ChangeMembership(identityRepositoryContext(pool, reactivateTx), params)
	if err != nil || reactivated.Membership.State != identity.MembershipActive || reactivated.Membership.Revision != 3 || len(reactivateTx.statements) != 4 {
		t.Fatalf("reactivated membership = %#v, statements=%d, %v", reactivated, len(reactivateTx.statements), err)
	}

	params.ExpectedRevision = 3
	params.Transition = identity.MembershipOffboard
	params.Evidence = currentTestIdentityEvidence(t, "identity.membership.offboarded", "tenant_membership", base.ID, now)
	offboardTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{lockRow(), postgresMembershipRow(t, reactivated.Membership), boolRow(false)}}
	offboarded, err := store.ChangeMembership(identityRepositoryContext(pool, offboardTx), params)
	if err != nil || offboarded.Membership.State != identity.MembershipOffboarded || offboarded.Membership.Revision != 4 || offboarded.RevokedRoleAssignments != 1 || offboarded.RevokedNamespaceMemberships != 1 || offboarded.RevokedGroupMemberships != 1 || len(offboardTx.statements) != 7 {
		t.Fatalf("offboarded membership = %#v, statements=%d, %v", offboarded, len(offboardTx.statements), err)
	}

	params.ExpectedRevision = 1
	params.Transition = identity.MembershipSuspend
	params.Evidence = currentTestIdentityEvidence(t, "identity.membership.suspended", "tenant_membership", base.ID, now)
	finalAdminTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{lockRow(), postgresMembershipRow(t, base), boolRow(true), countRow(1)}}
	if _, err := store.ChangeMembership(identityRepositoryContext(pool, finalAdminTx), params); identity.ErrorCodeOf(err) != identity.CodeSeparationOfDuties || len(finalAdminTx.statements) != 1 {
		t.Fatalf("final administrator transition = %v, statements=%d", err, len(finalAdminTx.statements))
	}

	params.ExpectedRevision = 2
	params.Transition = identity.MembershipReactivate
	realmSuspendedTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{lockRow(), postgresMembershipRow(t, suspendedMembership), boolRow(false)}}
	if _, err := store.ChangeMembership(identityRepositoryContext(pool, realmSuspendedTx), params); identity.ErrorCodeOf(err) != identity.CodeConflict || len(realmSuspendedTx.statements) != 1 {
		t.Fatalf("suspended-realm reactivation = %v, statements=%d", err, len(realmSuspendedTx.statements))
	}

	params.ExpectedRevision = 2
	params.Transition = identity.MembershipSuspend
	alreadySuspendedTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{lockRow(), postgresMembershipRow(t, suspendedMembership)}}
	if _, err := store.ChangeMembership(identityRepositoryContext(pool, alreadySuspendedTx), params); identity.ErrorCodeOf(err) != identity.CodeConflict || len(alreadySuspendedTx.statements) != 1 {
		t.Fatalf("duplicate membership suspension = %v, statements=%d", err, len(alreadySuspendedTx.statements))
	}
	offboardedMembership := base
	offboardedMembership.State = identity.MembershipOffboarded
	offboardedMembership.Revision = 4
	params.ExpectedRevision = offboardedMembership.Revision
	params.Transition = identity.MembershipOffboard
	alreadyOffboardedTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{lockRow(), postgresMembershipRow(t, offboardedMembership)}}
	if _, err := store.ChangeMembership(identityRepositoryContext(pool, alreadyOffboardedTx), params); identity.ErrorCodeOf(err) != identity.CodeConflict || len(alreadyOffboardedTx.statements) != 1 {
		t.Fatalf("duplicate membership offboarding = %v, statements=%d", err, len(alreadyOffboardedTx.statements))
	}
	params.ExpectedRevision = base.Revision
	params.Transition = identity.MembershipTransition("unknown")
	invalidTransitionTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{lockRow(), postgresMembershipRow(t, base)}}
	if _, err := store.ChangeMembership(identityRepositoryContext(pool, invalidTransitionTx), params); identity.ErrorCodeOf(err) != identity.CodeInvalidArgument || len(invalidTransitionTx.statements) != 1 {
		t.Fatalf("invalid membership transition = %v, statements=%d", err, len(invalidTransitionTx.statements))
	}

	params.Transition = identity.MembershipSuspend
	additionalAdministratorTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{lockRow(), postgresMembershipRow(t, base), boolRow(true), countRow(2)}}
	additionalAdministrator, err := store.ChangeMembership(identityRepositoryContext(pool, additionalAdministratorTx), params)
	if err != nil || additionalAdministrator.Membership.State != identity.MembershipSuspended || len(additionalAdministratorTx.statements) != 4 {
		t.Fatalf("non-final administrator suspension = %#v, statements=%d, %v", additionalAdministrator, len(additionalAdministratorTx.statements), err)
	}

	params.Transition = identity.MembershipOffboard
	roleFailureTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{execErrors: []error{nil, errors.New("role revocation failed")}}, queryRows: []pgx.Row{lockRow(), postgresMembershipRow(t, base), boolRow(false)}}
	if _, err := store.ChangeMembership(identityRepositoryContext(pool, roleFailureTx), params); identity.ErrorCodeOf(err) != identity.CodeDependency || len(roleFailureTx.statements) != 2 {
		t.Fatalf("role-revocation failure = %v, statements=%d", err, len(roleFailureTx.statements))
	}
	namespaceFailureTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{execErrors: []error{nil, nil, errors.New("namespace revocation failed")}}, queryRows: []pgx.Row{lockRow(), postgresMembershipRow(t, base), boolRow(false)}}
	if _, err := store.ChangeMembership(identityRepositoryContext(pool, namespaceFailureTx), params); identity.ErrorCodeOf(err) != identity.CodeDependency || len(namespaceFailureTx.statements) != 3 {
		t.Fatalf("namespace-revocation failure = %v, statements=%d", err, len(namespaceFailureTx.statements))
	}
}

func postgresRealmRow(t *testing.T, realm identity.Realm) pgx.Row {
	t.Helper()
	return scannerFunc(func(destinations ...any) error {
		setScanDestination(t, destinations, 0, realm.ID)
		setScanDestination(t, destinations, 1, realm.TenantID)
		setScanDestination(t, destinations, 2, realm.Issuer)
		setScanDestination(t, destinations, 3, realm.TenancyMode)
		setScanDestination(t, destinations, 4, realm.ExternalOrganization)
		setScanDestination(t, destinations, 5, realm.State)
		setScanDestination(t, destinations, 6, realm.Revision)
		setScanDestination(t, destinations, 7, realm.CreatedAt)
		setScanDestination(t, destinations, 8, realm.UpdatedAt)
		return nil
	})
}

func TestIdentityRealmTransactionHelpersEnforceShutdownInterlocks(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	now := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	active := identity.Realm{ID: "00000000-0000-7000-8000-000000000100", TenantID: tenantID, Issuer: "https://identity.example.com", TenancyMode: identity.RealmDedicated, State: identity.RealmActive, Revision: 1, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour)}
	second := identity.Realm{ID: "00000000-0000-7000-8000-000000000101", TenantID: tenantID, Issuer: active.Issuer, TenancyMode: identity.RealmShared, ExternalOrganization: "organization", State: identity.RealmSuspended, Revision: 2, CreatedAt: now, UpdatedAt: now}
	countRow := func(value int) pgx.Row {
		return scannerFunc(func(destinations ...any) error {
			setScanDestination(t, destinations, 0, value)
			return nil
		})
	}
	tenantRow := scannerFunc(func(destinations ...any) error {
		setScanDestination(t, destinations, 0, tenantID)
		return nil
	})
	updatedAtRow := scannerFunc(func(destinations ...any) error {
		setScanDestination(t, destinations, 0, now)
		return nil
	})
	auditAttributionRow := scannerFunc(func(destinations ...any) error {
		setScanDestination(t, destinations, 0, "00000000-0000-7000-8000-000000000199")
		setScanDestination(t, destinations, 1, "catalog_migration")
		setScanDestination(t, destinations, 2, "catalog_migration")
		return nil
	})
	outboxIDRow := scannerFunc(func(destinations ...any) error {
		setScanDestination(t, destinations, 0, "00000000-0000-7000-8000-000000000198")
		return nil
	})

	listRows := &identityScriptedRows{values: [][]any{
		{active.ID, tenantID, active.Issuer, active.TenancyMode, active.ExternalOrganization, active.State, active.Revision, active.CreatedAt, active.UpdatedAt},
		{second.ID, tenantID, second.Issuer, second.TenancyMode, second.ExternalOrganization, second.State, second.Revision, second.CreatedAt, second.UpdatedAt},
	}}
	listTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{countRow(2)}, queryResults: []identityQueryResult{{rows: listRows}}}
	page, err := listIdentityRealmsTx(t.Context(), listTx, tenantID, "", 1)
	if err != nil || !reflect.DeepEqual(page.Items, []identity.Realm{active}) || page.Total != 2 || page.NextAfter != active.ID || !listRows.closed {
		t.Fatalf("realm page = %#v rowsClosed=%t, %v", page, listRows.closed, err)
	}

	baseInput := IdentityRealmTransitionInput{TenantID: tenantID, RealmID: active.ID, ExpectedRevision: 1, TargetState: identity.RealmSuspended, Operator: "operator", Reason: "tenant access shutdown", TenantDiscoveryDisabled: true, AcknowledgeOutbox: true}
	suspendTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{tenantRow, postgresRealmRow(t, active), countRow(0), countRow(0), updatedAtRow, auditAttributionRow, outboxIDRow}}
	suspended, err := transitionIdentityRealmTx(t.Context(), suspendTx, baseInput)
	if err != nil || suspended.Realm.State != identity.RealmSuspended || suspended.Realm.Revision != 2 || !suspended.TenantAccessSuspended || len(suspendTx.statements) != 3 {
		t.Fatalf("suspended realm = %#v statements=%d, %v", suspended, len(suspendTx.statements), err)
	}

	additionalRealmTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{tenantRow, postgresRealmRow(t, active), countRow(1), updatedAtRow, auditAttributionRow, outboxIDRow}}
	additionalRealmSuspension, err := transitionIdentityRealmTx(t.Context(), additionalRealmTx, baseInput)
	if err != nil || additionalRealmSuspension.Realm.State != identity.RealmSuspended || additionalRealmSuspension.TenantAccessSuspended || len(additionalRealmTx.statements) != 3 {
		t.Fatalf("additional realm suspension = %#v statements=%d, %v", additionalRealmSuspension, len(additionalRealmTx.statements), err)
	}

	alreadySuspendedTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{tenantRow, postgresRealmRow(t, suspended.Realm)}}
	alreadySuspendedInput := baseInput
	alreadySuspendedInput.ExpectedRevision = suspended.Realm.Revision
	if _, err := transitionIdentityRealmTx(t.Context(), alreadySuspendedTx, alreadySuspendedInput); err == nil || len(alreadySuspendedTx.statements) != 1 {
		t.Fatalf("duplicate realm suspension = %v, statements=%d", err, len(alreadySuspendedTx.statements))
	}

	discoveryEnabled := baseInput
	discoveryEnabled.TenantDiscoveryDisabled = false
	discoveryTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{tenantRow, postgresRealmRow(t, active), countRow(0)}}
	if _, err := transitionIdentityRealmTx(t.Context(), discoveryTx, discoveryEnabled); err == nil || len(discoveryTx.statements) != 1 {
		t.Fatalf("enabled-discovery realm suspension = %v, statements=%d", err, len(discoveryTx.statements))
	}

	activeSnapshotTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{tenantRow, postgresRealmRow(t, active), countRow(0), countRow(1)}}
	if _, err := transitionIdentityRealmTx(t.Context(), activeSnapshotTx, baseInput); err == nil || len(activeSnapshotTx.statements) != 1 {
		t.Fatalf("active-snapshot realm suspension = %v, statements=%d", err, len(activeSnapshotTx.statements))
	}

	reactivateInput := baseInput
	reactivateInput.ExpectedRevision = 2
	reactivateInput.TargetState = identity.RealmActive
	reactivateTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{tenantRow, postgresRealmRow(t, suspended.Realm), updatedAtRow, auditAttributionRow, outboxIDRow}}
	reactivated, err := transitionIdentityRealmTx(t.Context(), reactivateTx, reactivateInput)
	if err != nil || reactivated.Realm.State != identity.RealmActive || reactivated.Realm.Revision != 3 || reactivated.TenantAccessSuspended || len(reactivateTx.statements) != 3 {
		t.Fatalf("reactivated realm = %#v statements=%d, %v", reactivated, len(reactivateTx.statements), err)
	}

	staleInput := baseInput
	staleInput.ExpectedRevision = 2
	staleTx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{tenantRow, postgresRealmRow(t, active)}}
	if _, err := transitionIdentityRealmTx(t.Context(), staleTx, staleInput); err == nil || len(staleTx.statements) != 1 {
		t.Fatalf("stale realm transition = %v, statements=%d", err, len(staleTx.statements))
	}

	// Observe the existing successful transitions' exact update and evidence
	// handoff. SQL, session authentication and commit atomicity remain integration checks.
	for _, item := range []struct {
		tx       *identityScriptedTx
		result   IdentityRealmTransitionResult
		state    identity.RealmState
		revision int64
		action   string
	}{
		{suspendTx, suspended, identity.RealmSuspended, 2, "identity.realm.suspended"},
		{additionalRealmTx, additionalRealmSuspension, identity.RealmSuspended, 2, "identity.realm.suspended"},
		{reactivateTx, reactivated, identity.RealmActive, 3, "identity.realm.reactivated"},
	} {
		want := active
		want.State, want.Revision, want.UpdatedAt = item.state, item.revision, now
		if !reflect.DeepEqual(item.result.Realm, want) {
			t.Fatalf("retained realm identity changed: %+v", item.result.Realm)
		}
		// The last three row calls update time, obtain database attribution and
		// obtain the outbox identifier; the earlier calls enforce interlocks.
		updateArgs := item.tx.queryRowArguments[len(item.tx.queryRowArguments)-3]
		if !reflect.DeepEqual(updateArgs, []any{tenantID, active.ID, item.state, item.revision}) {
			t.Fatalf("realm update binding=%v", updateArgs)
		}
		auditArgs, outboxArgs := item.tx.arguments[1], item.tx.arguments[2]
		if auditArgs[2] != tenantID || auditArgs[3] != "catalog_migration" || auditArgs[4] != item.action || auditArgs[5] != "tenant_identity_realm" || auditArgs[6] != active.ID || auditArgs[7] != baseInput.Reason || auditArgs[12] != now {
			t.Fatalf("realm audit binding=%v", auditArgs)
		}
		raw, ok := auditArgs[11].([]byte)
		if !ok {
			t.Fatal("realm audit details are not bytes")
		}
		var details struct {
			Facts struct {
				State     string `json:"state"`
				Suspended bool   `json:"tenantAccessSuspended"`
			} `json:"facts"`
		}
		if err := json.Unmarshal(raw, &details); err != nil || details.Facts.State != string(item.state) || details.Facts.Suspended != item.result.TenantAccessSuspended {
			t.Fatalf("realm audit facts=%s %v", raw, err)
		}
		if outboxArgs[1] != tenantID || outboxArgs[3] != active.ID || outboxArgs[6] != now || !reflect.DeepEqual(outboxArgs[7], &now) {
			t.Fatalf("realm outbox binding=%v", outboxArgs)
		}
		if item.tx.commits != 0 || item.tx.rollbacks != 0 {
			t.Fatal("realm helper took transaction ownership")
		}
	}

	if _, err := ListIdentityRealms(t.Context(), nil, tenantID, "", 10); err == nil {
		t.Fatal("realm list accepted a missing operator pool")
	}
	if _, err := TransitionIdentityRealm(t.Context(), nil, IdentityRealmTransitionInput{}); err == nil {
		t.Fatal("realm transition accepted incomplete input")
	}
}
