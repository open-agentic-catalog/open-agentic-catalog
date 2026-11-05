package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"

	sdk "github.com/open-agentic-catalog/open-agentic-catalog/sdk/go"
)

// This is a narrow caller port for actual generated SDK operations.
type catalogReader interface {
	ListPublicationSnapshotsWithResponse(context.Context, *sdk.ListPublicationSnapshotsParams, ...sdk.RequestEditorFn) (*sdk.ListPublicationSnapshotsResponse, error)
	GetWebhookReconciliationWithResponse(context.Context, ...sdk.RequestEditorFn) (*sdk.GetWebhookReconciliationResponse, error)
}

type Snapshot struct {
	ID         string    `json:"id"`
	TenantID   string    `json:"tenantId"`
	Host       string    `json:"host"`
	State      string    `json:"state"`
	Generation int64     `json:"generation"`
	Audience   string    `json:"audience"`
	ETag       string    `json:"etag"`
	CreatedAt  time.Time `json:"createdAt"`
}

type Report struct {
	Label      string    `json:"label"`
	ObservedAt time.Time `json:"observedAt"`
	Tenant     string    `json:"tenant"`
	Host       string    `json:"host"`
	Active     *Snapshot `json:"active"`
	Trigger    *Event    `json:"trigger,omitempty"`
}

type Saved struct {
	LastSuccessful *Report    `json:"lastSuccessfulObservation"`
	LastAttemptAt  *time.Time `json:"lastAttemptAt,omitempty"`
	RefreshFailure string     `json:"refreshFailure,omitempty"`
	CapacityLoss   int64      `json:"capacityLossCount"`
	DeliveryLoss   int64      `json:"deliveryLossCount"`
	LossObservedAt *time.Time `json:"lossCountsObservedAt,omitempty"`
}

var etagPattern = regexp.MustCompile(`^"[a-f0-9]{64}"$`)

func readReport(ctx context.Context, c catalogReader, tenant, host string, trigger *Event, now time.Time) (Report, error) {
	state := sdk.ListPublicationSnapshotsParamsState("active")
	limit := sdk.Limit(2)
	r, err := c.ListPublicationSnapshotsWithResponse(ctx, &sdk.ListPublicationSnapshotsParams{Host: host, State: &state, Limit: &limit})
	if err != nil || r == nil || r.StatusCode() != http.StatusOK || r.JSON200 == nil {
		return Report{}, errors.New("catalog_read_failed")
	}
	if err := uniqueJSON(r.Body); err != nil {
		return Report{}, errors.New("invalid_catalog_response")
	}
	top, err := fields(r.Body, "items", "nextCursor")
	if err != nil {
		return Report{}, errors.New("invalid_catalog_response")
	}
	if _, exists := top["nextCursor"]; exists {
		return Report{}, errors.New("invalid_catalog_response")
	}
	var items []json.RawMessage
	if err := json.Unmarshal(top["items"], &items); err != nil || items == nil || len(items) > 1 {
		return Report{}, errors.New("invalid_catalog_response")
	}
	result := Report{Label: "Last observed publication state", ObservedAt: now.UTC(), Tenant: tenant, Host: host, Trigger: trigger}
	if len(items) == 1 {
		if _, err := fields(items[0], "id", "tenantId", "host", "audience", "generation", "ardProfile", "aiCatalogProfile", "state", "etag", "createdAt", "activatedAt"); err != nil {
			return Report{}, errors.New("invalid_catalog_response")
		}
		var s Snapshot
		if json.Unmarshal(items[0], &s) != nil {
			return Report{}, errors.New("invalid_catalog_response")
		}
		if !uuidPattern.MatchString(s.ID) || s.TenantID != tenant || s.Host != host || s.State != "active" || s.Generation < 1 || s.Audience != "anonymous" && s.Audience != "authenticated" || !etagPattern.MatchString(s.ETag) || s.CreatedAt.IsZero() {
			return Report{}, errors.New("invalid_catalog_response")
		}
		result.Active = &s
	}
	return result, nil
}

func readLoss(ctx context.Context, c catalogReader) (int64, int64, error) {
	r, err := c.GetWebhookReconciliationWithResponse(ctx)
	if err != nil || r == nil || r.StatusCode() != http.StatusOK || r.JSON200 == nil || uniqueJSON(r.Body) != nil {
		return 0, 0, errors.New("reconciliation_read_failed")
	}
	if _, err := fields(r.Body, "pendingEvents", "pendingEventLimit", "capacityLossCount", "lastCapacityLossAt", "deliveryLossCount", "lastDeliveryLossAt"); err != nil {
		return 0, 0, errors.New("reconciliation_read_failed")
	}
	var v struct {
		Capacity *int64 `json:"capacityLossCount"`
		Delivery *int64 `json:"deliveryLossCount"`
		Pending  *int64 `json:"pendingEvents"`
		Limit    *int64 `json:"pendingEventLimit"`
	}
	if json.Unmarshal(r.Body, &v) != nil || v.Capacity == nil || v.Delivery == nil || v.Pending == nil || v.Limit == nil || *v.Capacity < 0 || *v.Delivery < 0 || *v.Pending < 0 || *v.Pending > 1000 || *v.Limit != 1000 {
		return 0, 0, errors.New("reconciliation_read_failed")
	}
	return *v.Capacity, *v.Delivery, nil
}

func render(path string, s Saved) error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return errors.New("report_render_failed")
	}
	raw = append(raw, '\n')
	f, err := os.CreateTemp(filepath.Dir(path), ".publication-report-*")
	if err != nil {
		return errors.New("report_render_failed")
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err != nil {
		return errors.New("report_render_failed")
	}
	return nil
}
