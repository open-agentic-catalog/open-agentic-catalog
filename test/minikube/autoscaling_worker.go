//go:build minikube

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func (h *harness) hpaWorkerProgress(base, stage string, client *http.Client) error {
	ctx, cancel := context.WithTimeout(h.ctx, 30*time.Second)
	defer cancel()
	call := func(method, path, key, match string, body any) (httpResult, error) {
		return h.requestContext(ctx, method, base+path, h.tokens["admin"], key, match, body, client)
	}
	report := h.report.Autoscaling
	prefix := "hpa-worker-" + stage + "-" + h.suffix
	if report.SourceID == "" {
		inspected, e := call("POST", "/api/v1/federation/source-inspections", prefix+"-inspect", "", map[string]any{"url": "https://federation-fixture:8443/pressure/0/.well-known/ard.json", "networkProfile": "functional-fixture"})
		var inspection struct {
			ID string `json:"id"`
		}
		if e != nil || inspected.Status != 201 || json.Unmarshal(inspected.Body, &inspection) != nil || !identity.ValidTenantID(inspection.ID) {
			return errors.New("autoscaling worker source inspection failed")
		}
		created, e := call("POST", "/api/v1/federation/sources", prefix+"-connect", "", map[string]any{"inspectionId": inspection.ID, "displayName": prefix, "retrievalMode": federation.RetrievalIndexed, "scope": map[string]any{"kinds": []catalog.Kind{catalog.KindAgent}}, "updateMode": federation.UpdateManual, "networkProfile": "functional-fixture", "reason": "Verify worker progress during API autoscaling"})
		var source federation.Source
		if e != nil || created.Status != 201 || json.Unmarshal(created.Body, &source) != nil || !identity.ValidTenantID(source.ID) {
			return errors.New("autoscaling worker source connection failed")
		}
		report.SourceID = source.ID
	}
	path := "/api/v1/federation/sources/" + report.SourceID
	current, e := call("GET", path, "", "", nil)
	if e != nil || current.Status != 200 || current.Header.Get("ETag") == "" {
		return errors.New("autoscaling worker source read failed")
	}
	accepted, e := call("POST", path+":sync", prefix+"-sync", current.Header.Get("ETag"), map[string]any{})
	jobID, decodeErr := hpaAcceptedJob(accepted)
	if e != nil || decodeErr != nil {
		return errors.New("autoscaling sync Job was not accepted")
	}
	var snapshotID string
	if err := waitTopology(ctx, func() (bool, error) {
		response, e := call("GET", "/api/v1/jobs/"+jobID, "", "", nil)
		var job struct {
			ID      string `json:"id"`
			State   string `json:"state"`
			Attempt int    `json:"attempt"`
			Result  struct {
				SnapshotID string `json:"snapshotId"`
			} `json:"result"`
		}
		if e != nil || response.Status != 200 || json.Unmarshal(response.Body, &job) != nil || job.ID != jobID {
			return false, errors.New("autoscaling sync Job read failed")
		}
		switch job.State {
		case "succeeded":
			if job.Attempt != 1 || !identity.ValidTenantID(job.Result.SnapshotID) {
				return false, errors.New("autoscaling sync Job lacks first-attempt snapshot evidence")
			}
			snapshotID = job.Result.SnapshotID
			return true, nil
		case "queued", "running", "waiting":
			return false, nil
		default:
			return false, fmt.Errorf("autoscaling sync Job ended in %s", job.State)
		}
	}); err != nil {
		return err
	}
	snapshot, e := call("GET", "/api/v1/federation/snapshots/"+snapshotID, "", "", nil)
	var retained federation.Snapshot
	if e != nil || snapshot.Status != 200 || json.Unmarshal(snapshot.Body, &retained) != nil || retained.ID != snapshotID || retained.SourceID != report.SourceID || retained.State != federation.SnapshotComplete || !retained.EnumerationComplete {
		return errors.New("autoscaling sync lacks a retained complete source snapshot")
	}
	fresh, e := call("GET", path, "", "", nil)
	var source federation.Source
	if e != nil || fresh.Status != 200 || json.Unmarshal(fresh.Body, &source) != nil || source.LastCompleteSnapshotID != snapshotID {
		return errors.New("autoscaling sync snapshot is not current")
	}
	denied, e := h.requestContext(ctx, "GET", base+"/api/v1/jobs/"+jobID, h.tokens["other"], "", "", nil, client)
	if e != nil || !replicaProblem(denied, 404, catalog.CodeNotFound) {
		return errors.New("autoscaling sync Job crossed tenant boundary")
	}
	report.Jobs = append(report.Jobs, jobID)
	return nil
}

func hpaAcceptedJob(response httpResult) (string, error) {
	var envelope struct {
		Job struct {
			ID string `json:"id"`
		} `json:"job"`
		Existing bool `json:"existing"`
	}
	if response.Status != 202 || json.Unmarshal(response.Body, &envelope) != nil || !identity.ValidTenantID(envelope.Job.ID) || envelope.Existing {
		return "", errors.New("sync response lacks a newly accepted Job")
	}
	return envelope.Job.ID, nil
}
