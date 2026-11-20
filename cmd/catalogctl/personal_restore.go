// Personal restore retains finite private progress and never blindly repeats a create.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/personal"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

const personalRestoreFormat = "oac-personal-restore-v1"
const personalSavePath = "/api/v1/personal/items"

var personalRequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// personalFailureCode retains only reviewed codes, never arbitrary upstream text.
func personalFailureCode(code string) string {
	switch code {
	case "INVALID_ARGUMENT", "NOT_FOUND", "CONFLICT", "PRECONDITION_FAILED", "UNAUTHENTICATED", "FORBIDDEN", "IDEMPOTENCY_KEY_REUSED", "DEPENDENCY_UNAVAILABLE", "INTERNAL", "WORK_LIMIT_EXCEEDED", "REQUEST_DEADLINE_EXCEEDED", "invalid_response", "redirect_refused":
		return code
	default:
		return "http_error"
	}
}

type personalRestoreState struct {
	Format      string                `json:"format"`
	Origin      string                `json:"origin"`
	Principal   importPrincipal       `json:"principal"`
	InputDigest string                `json:"inputDigest"`
	Selected    []bool                `json:"selected"`
	Steps       []personalRestoreStep `json:"steps"`
}

type personalRestoreFailure struct {
	Status    int    `json:"status"`
	Code      string `json:"code"`
	RequestID string `json:"requestId,omitempty"`
}

type personalRestoreStep struct {
	importStep
	Failure *personalRestoreFailure `json:"failure,omitempty"`
}

type personalRestoreFile struct {
	State  personalRestoreState `json:"state"`
	Digest string               `json:"digest"`
}

type personalRestoreRow struct {
	Index     int                     `json:"index"`
	State     string                  `json:"state"`
	ID        string                  `json:"id,omitempty"`
	RequestID string                  `json:"requestId,omitempty"`
	Failure   *personalRestoreFailure `json:"failure,omitempty"`
}

type personalRestoreReport struct {
	Status string               `json:"status"`
	Items  []personalRestoreRow `json:"items"`
}

// personalRestoreProgress omits private criteria, principals and replay keys from routine output.
func personalRestoreProgress(receipt personalRestoreState, status string) personalRestoreReport {
	report := personalRestoreReport{Status: status, Items: []personalRestoreRow{}}
	for i, step := range receipt.Steps {
		report.Items = append(report.Items, personalRestoreRow{i + 1, step.State, step.Ack.ID, step.RequestID, step.Failure})
	}
	return report
}

// newPersonalRestore binds exact bytes and explicit create/skip choices before any save.
func newPersonalRestore(file personalFile, raw []byte, selected []bool, principal importPrincipal) (personalRestoreState, error) {
	r := personalRestoreState{Format: personalRestoreFormat, Origin: file.Origin, Principal: principal, InputDigest: importDigest(raw), Selected: selected, Steps: make([]personalRestoreStep, len(file.Items))}
	for i, create := range selected {
		if !create {
			r.Steps[i].State = "skipped"
			continue
		}
		var key [24]byte
		if _, err := rand.Read(key[:]); err != nil {
			return personalRestoreState{}, safeerr.New("personal: cannot generate restore keys")
		}
		r.Steps[i].importStep = importStep{State: "ready", Key: "personal-" + hex.EncodeToString(key[:]), Method: http.MethodPost}
	}
	return r, nil
}

// savePersonalRestore uses the existing locked, fsynced, tamper-detecting private-file boundary.
func savePersonalRestore(state *importStateFile, receipt personalRestoreState) error {
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return safeerr.New("personal: cannot encode restore state")
	}
	raw, err := json.Marshal(personalRestoreFile{receipt, importDigest(encoded)})
	if err != nil {
		return safeerr.New("personal: cannot encode restore state")
	}
	return state.saveRaw(raw)
}

// decodePersonalRestore refuses changed intent and impossible progress before any remote call.
func decodePersonalRestore(raw []byte, file personalFile, input []byte, selected []bool) (personalRestoreState, error) {
	var stored personalRestoreFile
	fail := safeerr.New("personal: progress is invalid or intent changed; preserve the original file and progress for investigation")
	if len(raw) > importMaxReceiptBytes || jsondoc.Decode(raw, &stored) != nil {
		return personalRestoreState{}, fail
	}
	r := stored.State
	encoded, err := json.Marshal(r)
	if err != nil || importDigest(encoded) != stored.Digest || r.Format != personalRestoreFormat || r.Origin != file.Origin || r.InputDigest != importDigest(input) || !reflect.DeepEqual(r.Selected, selected) || len(r.Steps) != len(file.Items) || r.Principal.TenantID != file.TenantID || !validImportID(r.Principal.RealmID) || !validImportID(r.Principal.MembershipID) || r.Principal.Subject == "" {
		return personalRestoreState{}, fail
	}
	keys, stopped := map[string]bool{}, false
	for i, step := range r.Steps {
		if !selected[i] {
			if !reflect.DeepEqual(step, personalRestoreStep{importStep: importStep{State: "skipped"}}) {
				return personalRestoreState{}, fail
			}
			continue
		}
		if len(step.Key) != 57 || !strings.HasPrefix(step.Key, "personal-") || keys[step.Key] || step.Method != http.MethodPost || step.IfMatch != "" {
			return personalRestoreState{}, fail
		}
		if _, err := hex.DecodeString(step.Key[9:]); err != nil {
			return personalRestoreState{}, fail
		}
		keys[step.Key] = true
		if stopped && step.State != "ready" {
			return personalRestoreState{}, fail
		}
		if step.State == "ready" {
			if !reflect.DeepEqual(step, personalRestoreStep{importStep: importStep{State: "ready", Key: step.Key, Method: http.MethodPost}}) {
				return personalRestoreState{}, fail
			}
		} else {
			body, err := json.Marshal(file.Items[i])
			if err != nil {
				return personalRestoreState{}, fail
			}
			if step.Path != personalSavePath || step.BodyDigest != importDigest(body) || step.FirstAttempt.IsZero() {
				return personalRestoreState{}, fail
			}
			switch step.State {
			case "acknowledged", "already-followed":
				if (step.State == "already-followed" && file.Items[i].Type != "follow") || !personalAckMatchesSelection(r.Steps, file.Items, i, step.Ack.ID, step.State == "already-followed") {
					return personalRestoreState{}, fail
				}
				if step.Failure != nil || !validImportID(step.Ack.ID) || (!strongImportETag(step.Ack.ETag) || step.State == "acknowledged" && step.Ack.ETag != `"rev-1"`) || !personalRequestIDPattern.MatchString(step.RequestID) || !reflect.DeepEqual(step.Ack, importAcknowledgement{ID: step.Ack.ID, ETag: step.Ack.ETag}) {
					return personalRestoreState{}, fail
				}
			case "inflight", "uncertain", "failed":
				if step.RequestID != "" || !reflect.DeepEqual(step.Ack, importAcknowledgement{}) {
					return personalRestoreState{}, fail
				}
				if step.Failure != nil && (step.Failure.Status < 200 || step.Failure.Status > 599 || personalFailureCode(step.Failure.Code) != step.Failure.Code || step.Failure.RequestID != "" && !personalRequestIDPattern.MatchString(step.Failure.RequestID)) {
					return personalRestoreState{}, fail
				}
			default:
				return personalRestoreState{}, fail
			}
		}
		stopped = step.State != "acknowledged" && step.State != "already-followed"
	}
	return r, nil
}

// restorePersonal locks progress before authentication and never takes its destination from a file.
func restorePersonal(ctx context.Context, options remoteOptions, file personalFile, raw []byte, selected []bool, filename string, resume bool) (report personalRestoreReport, resultErr error) {
	report = personalRestoreProgress(personalRestoreState{}, "refused")
	state, err := openImportState(filename, resume)
	if err != nil {
		return report, err
	}
	defer state.close()
	var receipt personalRestoreState
	if resume {
		receipt, err = decodePersonalRestore(state.previous, file, raw, selected)
		if err != nil {
			return report, err
		}
	}
	session, err := newRemoteSession(ctx, options)
	if err != nil {
		return report, err
	}
	defer session.close()
	principal, err := personalPrincipal(ctx, session, options, true)
	if err != nil {
		return report, err
	}
	if session.origin != file.Origin || principal.TenantID != file.TenantID || resume && principal != receipt.Principal {
		return report, safeerr.New("personal: current origin, tenant, realm or human membership differs from the restore binding")
	}
	if !resume {
		receipt, err = newPersonalRestore(file, raw, selected, principal)
		if err != nil {
			return report, err
		}
		if err = savePersonalRestore(state, receipt); err != nil {
			return report, err
		}
	}
	status := "incomplete"
	defer func() { report = personalRestoreProgress(receipt, status) }()
	for i := range receipt.Steps {
		step := &receipt.Steps[i]
		if step.State == "acknowledged" || step.State == "already-followed" || step.State == "skipped" {
			continue
		}
		if step.State != "ready" && (step.State == "failed" || !importReplayAllowed(step.FirstAttempt, time.Now())) {
			return report, safeerr.New("personal: restore cannot safely resume this command; preserve its original key and first-attempt time for investigation")
		}
		current, err := personalPrincipal(ctx, session, options, true)
		if err != nil {
			return report, err
		}
		if principal != current {
			return report, safeerr.New("personal: human identity changed; no further save was sent")
		}
		if err = savePersonalItem(ctx, session, options, state, &receipt, i, file.Items); err != nil {
			return report, err
		}
	}
	status = "complete"
	return report, nil
}

// savePersonalItem persists inflight before one request and preserves uncertainty after lost or invalid replies.
func savePersonalItem(ctx context.Context, session *remoteSession, options remoteOptions, state *importStateFile, receipt *personalRestoreState, index int, inputs []personal.Input) error {
	input := inputs[index]
	step := &receipt.Steps[index]
	body, err := json.Marshal(input)
	if err != nil || len(body) > personal.MaxBodyBytes {
		return safeerr.New("personal: save exceeds the 16 KiB request limit")
	}
	if step.State == "ready" {
		step.FirstAttempt = time.Now().UTC()
	} else if (step.State != "inflight" && step.State != "uncertain") || step.Path != personalSavePath || step.BodyDigest != importDigest(body) || !importReplayAllowed(step.FirstAttempt, time.Now()) {
		return safeerr.New("personal: original command cannot safely replay; preserve progress for investigation")
	}
	step.Path, step.BodyDigest, step.State = personalSavePath, importDigest(body), "inflight"
	step.Failure = nil
	if err = savePersonalRestore(state, *receipt); err != nil {
		return err
	}
	request := options
	request.path, request.body, request.expectedStatus, request.idempotencyKey = personalSavePath, body, http.StatusCreated, step.Key
	if input.Type == "follow" {
		request.alternateStatus = http.StatusOK
	}
	response, err := exchangeRemote(ctx, session.client, request, session.origin, session.token)
	var ack struct {
		ID       string `json:"id"`
		Revision int64  `json:"revision"`
	}
	existing := input.Type == "follow" && response.Status == http.StatusOK
	if err == nil && (importResponse(response.Data, &ack) != nil || !validImportID(ack.ID) || ack.Revision < 1 || !existing && ack.Revision != 1 || !importETag(response.Meta.ETag, ack.Revision) || !personalRequestIDPattern.MatchString(response.Meta.RequestID)) {
		err = safeerr.New("personal: save response could not be verified")
	}
	if err == nil && !personalAckMatchesSelection(receipt.Steps, inputs, index, ack.ID, existing) {
		err = safeerr.New("personal: save response reused another item's identity")
	}
	if err != nil {
		step.State = "uncertain"
		var failure remoteAPIError
		if errors.As(err, &failure) {
			requestID := failure.RequestID
			if !personalRequestIDPattern.MatchString(requestID) {
				requestID = ""
			}
			step.Failure = &personalRestoreFailure{failure.Status, personalFailureCode(failure.Code), requestID}
		}
		if errors.As(err, &failure) && failure.Status >= 400 && failure.Status < 500 && failure.Status != 401 && failure.Status != 403 && failure.Code != "invalid_response" {
			step.State = "failed"
		}
		if saveErr := savePersonalRestore(state, *receipt); saveErr != nil {
			return saveErr
		}
		return safeerr.New("personal: save was not acknowledged; inspect per-item progress before explicit recovery")
	}
	step.State, step.RequestID = "acknowledged", response.Meta.RequestID
	if existing {
		step.State = "already-followed"
	}
	step.Failure = nil
	step.Ack = importAcknowledgement{ID: ack.ID, ETag: response.Meta.ETag}
	return savePersonalRestore(state, *receipt)
}

// Only a duplicate follow may refer to an item already acknowledged in this restore.
func personalAckMatchesSelection(steps []personalRestoreStep, inputs []personal.Input, index int, id string, existing bool) bool {
	for i, step := range steps[:index] {
		if (step.State == "acknowledged" || step.State == "already-followed") && strings.EqualFold(step.Ack.ID, id) {
			if !existing || inputs[i].Type != "follow" || !strings.EqualFold(string(inputs[i].ListingID), string(inputs[index].ListingID)) {
				return false
			}
		}
	}
	return true
}
