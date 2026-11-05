// Operator evidence derives attribution from the live database session before
// constructing closed audit and outbox records.

package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/events"
)

// newDatabaseAuditRecord obtains a UUID and actual session/current-user
// attribution inside the operator transaction before validating the audit record.
func newDatabaseAuditRecord(ctx context.Context, tx pgx.Tx, tenantID, action, targetType, targetID, reason, operatorLabel string, facts map[string]any, at time.Time) (catalog.AuditEvent, error) {
	var id, sessionUser, currentUser string
	if err := tx.QueryRow(ctx, `SELECT uuidv7()::text,session_user,current_user`).Scan(&id, &sessionUser, &currentUser); err != nil {
		return catalog.AuditEvent{}, fmt.Errorf("create database audit attribution: %w", err)
	}
	record, err := audit.NewRecord(id, tenantID, audit.Principal{ID: sessionUser, Type: audit.PrincipalDatabaseLogin}, action, targetType, targetID, reason, audit.Execution{CurrentUser: currentUser, OperatorLabel: operatorLabel}, facts, at)
	if err != nil {
		return catalog.AuditEvent{}, fmt.Errorf("construct database audit event: %w", err)
	}
	return record, nil
}

// newDatabasePublicOutbox constructs the same closed reference envelope used
// by runtime mutations while keeping identifier generation in the
// schema-owner transaction.
func newDatabasePublicOutbox(ctx context.Context, tx pgx.Tx, event catalog.AuditEvent, acknowledge bool) (catalog.OutboxEvent, error) {
	var id string
	if err := tx.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&id); err != nil {
		return catalog.OutboxEvent{}, fmt.Errorf("create database outbox identifier: %w", err)
	}
	data, err := events.MarshalReference(event.Action, event.TargetType, event.TargetID, event.AfterDigest)
	if err != nil {
		return catalog.OutboxEvent{}, fmt.Errorf("construct database outbox event: %w", err)
	}
	outbox := catalog.OutboxEvent{ID: id, TenantID: event.TenantID, Type: events.CanonicalType(event.Action), Subject: event.TargetID, Data: data, CreatedAt: event.At}
	if acknowledge {
		publishedAt := event.At
		outbox.PublishedAt = &publishedAt
	}
	return outbox, nil
}
