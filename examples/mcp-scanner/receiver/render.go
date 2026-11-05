package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

func atomicFile(name string, raw []byte) error {
	f, err := os.CreateTemp(filepath.Dir(name), ".mcp-report-*")
	if err != nil {
		return errors.New("report_render_failed")
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), name)
	}
	if err != nil {
		return errors.New("report_render_failed")
	}
	return nil
}

func (s *postgresStore) render(ctx context.Context, configs []mapping, directory string) error {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return errors.New("report_directory_unavailable")
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return errors.New("private_report_directory_required")
	}
	ids := make([]string, len(configs))
	for i, m := range configs {
		ids[i] = m.ResourceID
	}
	rows, err := s.pool.Query(ctx, `SELECT o.resource_id,o.document,s.report FROM mcp_receiver_observations o LEFT JOIN mcp_receiver_scans s ON s.scan_key=o.scan_key WHERE o.resource_id=ANY($1) ORDER BY o.resource_id LIMIT 33`, ids)
	if err != nil {
		return errors.New("report_read_failed")
	}
	defer rows.Close()
	type entry struct {
		ResourceID string    `json:"resourceId"`
		State      string    `json:"state"`
		CheckedAt  time.Time `json:"checkedAt"`
		File       string    `json:"file"`
	}
	entries := []entry{}
	for rows.Next() {
		var id string
		var document, report []byte
		if rows.Scan(&id, &document, &report) != nil || !uuidPattern.MatchString(id) || len(entries) >= maxResources {
			return errors.New("invalid_saved_observation")
		}
		var o observation
		if json.Unmarshal(document, &o) != nil || o.ResourceID != id {
			return errors.New("invalid_saved_observation")
		}
		if len(report) > 0 {
			if len(report) > reportLimit || uniqueJSON(report) != nil {
				return errors.New("invalid_saved_report")
			}
			o.ScanFile = id + "-scan.json"
			o.ScanDigest = sha(report)
			if err = atomicFile(filepath.Join(directory, o.ScanFile), report); err != nil {
				return err
			}
		}
		data, err := json.MarshalIndent(o, "", "  ")
		if err != nil {
			return errors.New("report_render_failed")
		}
		if err = atomicFile(filepath.Join(directory, id+".json"), append(data, '\n')); err != nil {
			return err
		}
		entries = append(entries, entry{ResourceID: id, State: o.State, CheckedAt: o.CheckedAt, File: id + ".json"})
	}
	if rows.Err() != nil {
		return errors.New("report_read_failed")
	}
	var capacity, delivery *int64
	var reconciled *time.Time
	var reason string
	if err = s.pool.QueryRow(ctx, `SELECT capacity_loss,delivery_loss,reconciled_at,reconciliation_failure FROM mcp_receiver_settings WHERE singleton`).Scan(&capacity, &delivery, &reconciled, &reason); err != nil {
		return errors.New("report_read_failed")
	}
	index := struct {
		Label      string     `json:"label"`
		Items      []entry    `json:"items"`
		Capacity   *int64     `json:"capacityLossCount"`
		Delivery   *int64     `json:"deliveryLossCount"`
		Reconciled *time.Time `json:"lastCompleteReconciliationAt"`
		Failure    string     `json:"reconciliationFailure,omitempty"`
	}{"Last checked MCP tool definitions", entries, capacity, delivery, reconciled, reason}
	raw, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return errors.New("report_render_failed")
	}
	return atomicFile(filepath.Join(directory, "index.json"), append(raw, '\n'))
}
