package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

type importItemOutcome struct {
	Key         string            `json:"key"`
	Disposition string            `json:"disposition"`
	Steps       []importStep      `json:"steps"`
	Findings    []catalog.Finding `json:"findings"`
}

type importOutcome struct {
	Format      int                 `json:"format"`
	Disposition string              `json:"disposition"`
	Items       []importItemOutcome `json:"items"`
	CurrentKey  string              `json:"currentKey,omitempty"`
}

func importProgress(plan importBatchPlan, receipt importReceipt, disposition string) importOutcome {
	if plan.Draft != nil {
		return importDraftProgress(plan, receipt, disposition)
	}
	report := importOutcome{Format: 1, Disposition: disposition, Items: make([]importItemOutcome, len(plan.Items))}
	for index, item := range plan.Items {
		outcome := importItemOutcome{Key: item.Preview.Key, Disposition: "unattempted", Findings: []catalog.Finding{}}
		start := index * importStepsPerResource
		if start+importStepsPerResource <= len(receipt.Steps) {
			outcome.Steps = slices.Clone(receipt.Steps[start : start+importStepsPerResource])
			acknowledged := 0
			for stepIndex, step := range outcome.Steps {
				outcome.Findings = append(outcome.Findings, step.Ack.Findings...)
				// Emit diagnostics once per item. Repeating them inside every
				// acknowledgement could exhaust the report budget at 64 items.
				outcome.Steps[stepIndex].Ack.Findings = nil
				switch step.State {
				case "acknowledged":
					acknowledged++
				case "inflight", "uncertain":
					outcome.Disposition = "uncertain"
				case "failed":
					outcome.Disposition = "failed"
				}
				if step.Ack.Blocked {
					outcome.Disposition = "blocked"
				}
			}
			if acknowledged == importStepsPerResource {
				outcome.Disposition = "complete"
			} else if acknowledged > 0 && outcome.Disposition == "unattempted" {
				outcome.Disposition = "incomplete"
			}
		}
		report.Items[index] = outcome
	}
	return report
}

func importItemReceipt(receipt importReceipt, index int) importReceipt {
	receipt.Steps = receipt.Steps[index*importStepsPerResource : (index+1)*importStepsPerResource]
	return receipt
}

func validImportID(value string) bool { return identity.ValidTenantID(value) }

func importResponse(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(value); err != nil {
		return safeerr.New("import: successful API response has invalid consumed fields; outcome is uncertain")
	}
	return nil
}

func importGET(ctx context.Context, session *remoteSession, options remoteOptions, path string) (remoteEnvelope, error) {
	options.path, options.body, options.expectedStatus = path, nil, http.StatusOK
	options.idempotencyKey, options.ifMatch = "", ""
	return exchangeRemote(ctx, session.client, options, session.origin, session.token)
}

func importPreflight(ctx context.Context, session *remoteSession, options remoteOptions, namespace string, kind catalog.Kind) (importPrincipal, int64, error) {
	return importPermissionPreflight(ctx, session, options, namespace, kind, []identity.Permission{"resource.create", "resource.edit"})
}

func importPermissionPreflight(ctx context.Context, session *remoteSession, options remoteOptions, namespace string, kind catalog.Kind, required []identity.Permission) (importPrincipal, int64, error) {
	var self struct {
		importPrincipal
		Permissions      []identity.Permission                `json:"permissions"`
		NamespaceGrants  []identity.NamespacePermissionAccess `json:"namespaceGrants"`
		ScopeEnforcement *bool                                `json:"scopeEnforcement"`
		Scopes           []string                             `json:"scopes"`
	}
	fail := safeerr.New("import: preflight requires a resolved principal and catalog.read/catalog.write scopes when enforced")
	response, err := importGET(ctx, session, options, "/api/v1/me?namespaceId="+url.QueryEscape(namespace))
	if err != nil {
		return self.importPrincipal, 0, err
	}
	if importResponse(response.Data, &self) != nil || !validImportID(self.TenantID) || !validImportID(self.RealmID) || !validImportID(self.MembershipID) || self.Subject == "" || len(self.Subject) > 512 || !utf8.ValidString(self.Subject) || self.ScopeEnforcement == nil {
		return self.importPrincipal, 0, fail
	}
	permissions := slices.Clone(self.Permissions)
	for _, grant := range self.NamespaceGrants {
		if grant.NamespaceID == namespace {
			permissions = append(permissions, grant.Permissions...)
		}
	}
	for _, permission := range required {
		if !slices.Contains(permissions, permission) {
			return self.importPrincipal, 0, safeerr.New("import: missing destination Namespace permission: " + string(permission))
		}
	}
	if *self.ScopeEnforcement && (!slices.Contains(self.Scopes, "catalog.read") || !slices.Contains(self.Scopes, "catalog.write")) {
		return self.importPrincipal, 0, fail
	}
	var caps struct {
		ResourceKinds []catalog.Kind `json:"resourceKinds"`
		Limits        struct {
			RequestBodyBytes int64 `json:"requestBodyBytes"`
		} `json:"limits"`
	}
	response, err = importGET(ctx, session, options, "/api/v1/capabilities")
	if err != nil {
		return self.importPrincipal, 0, err
	}
	if importResponse(response.Data, &caps) != nil || caps.Limits.RequestBodyBytes < 1 || !slices.Contains(caps.ResourceKinds, kind) {
		return self.importPrincipal, 0, safeerr.New("import: API did not advertise the required resource kind and request budget")
	}
	return self.importPrincipal, caps.Limits.RequestBodyBytes, nil
}

func importBoundRequest(plan importPlan, receipt importReceipt, index int) (string, []byte, string, error) {
	return boundImportCommand(plan, importReceiptBindings(receipt), index)
}

func boundImportCommand(plan importPlan, binding importCommandBindings, index int) (string, []byte, string, error) {
	path := plan.Preview.Commands[index].Path
	path = strings.ReplaceAll(path, "{resourceId}", binding.ResourceID)
	path = strings.ReplaceAll(path, "{listingId}", binding.ListingID)
	body := []byte(plan.Bodies[index])
	etag := ""
	if index >= 2 {
		value, err := importJSONObject(body)
		if err != nil {
			return "", nil, "", err
		}
		if index == 2 {
			value["resourceId"] = binding.ResourceID
		} else {
			value["versionRevisionId"] = binding.VersionRevisionID
			etag = binding.ListingETag
		}
		body, err = json.Marshal(value)
		if err != nil {
			return "", nil, "", safeerr.New("import: cannot reconstruct request")
		}
	}
	return path, body, etag, nil
}

func importETag(value string, revision int64) bool {
	return revision > 0 && value == `"rev-`+strconv.FormatInt(revision, 10)+`"`
}

func importAcknowledge(plan importPlan, receipt importReceipt, index int, response remoteEnvelope) (importAcknowledgement, error) {
	return acknowledgeImportCommand(plan, receipt.Principal, importReceiptBindings(receipt), index, response)
}

func acknowledgeImportCommand(plan importPlan, principal importPrincipal, binding importCommandBindings, index int, response remoteEnvelope) (importAcknowledgement, error) {
	fail := safeerr.New("import: successful API response has inconsistent identity or evidence; outcome is uncertain")
	if response.Meta.RequestID == "" {
		return importAcknowledgement{}, fail
	}
	tenant := principal.TenantID
	var desired struct {
		NamespaceID, CanonicalIdentifier string
		Kind                             catalog.Kind
	}
	if importResponse(plan.Bodies[0], &desired) != nil {
		return importAcknowledgement{}, fail
	}
	ack := importAcknowledgement{ETag: response.Meta.ETag}
	switch index {
	case 0:
		var resource catalog.Resource
		if importResponse(response.Data, &resource) != nil || !validImportID(resource.ID) || resource.TenantID != tenant || resource.NamespaceID != desired.NamespaceID || resource.CanonicalIdentifier != desired.CanonicalIdentifier || resource.Kind != desired.Kind || resource.CreatedBy != principal.Subject || !importETag(ack.ETag, resource.Revision) {
			return ack, fail
		}
		ack.ID = resource.ID
	case 1:
		var result struct {
			Version  catalog.ResourceVersion `json:"version"`
			Revision catalog.VersionRevision `json:"revision"`
			Evidence catalog.EvidenceSet     `json:"evidence"`
		}
		var input struct {
			Version string `json:"version"`
		}
		_ = importResponse(plan.Bodies[1], &input)
		if importResponse(response.Data, &result) != nil || !validImportID(result.Version.ID) || result.Version.TenantID != tenant || result.Version.ResourceID != binding.ResourceID || result.Version.Version != input.Version || !importETag(ack.ETag, result.Version.Revision) || !importVersionEvidence(result.Revision, result.Evidence, tenant, result.Version.ID) || result.Version.CandidateRevisionID != result.Revision.ID {
			return ack, fail
		}
		ack.ID, ack.RevisionID, ack.EvidenceID, ack.Digest = result.Version.ID, result.Revision.ID, result.Evidence.ID, result.Revision.ContentSetDigest
		ack.Blocked = result.Evidence.State == catalog.EvidenceBlocked
		// Native messages can quote private input. Persist bounded fixed summaries
		// only; the exact evidence ID gives authorized readers the full findings.
		collector := catalog.NewFindingCollector(catalog.DefaultDiagnosticLimits())
		for _, finding := range result.Evidence.Findings {
			if finding.Severity != catalog.SeverityError && finding.Severity != catalog.SeverityWarning && finding.Severity != catalog.SeverityInfo {
				return ack, fail
			}
			collector.Add(catalog.Finding{Code: "SERVER_VALIDATION_FINDING", Severity: finding.Severity, Message: "Read the exact VersionRevision for the retained diagnostic."})
		}
		ack.Findings = collector.Findings()
	case 2:
		var listing catalog.CatalogListing
		if importResponse(response.Data, &listing) != nil || !validImportID(listing.ID) || listing.TenantID != tenant || listing.NamespaceID != desired.NamespaceID || listing.ResourceID != binding.ResourceID || !importETag(ack.ETag, listing.Revision) {
			return ack, fail
		}
		ack.ID = listing.ID
	case 3:
		var result struct {
			Listing  catalog.CatalogListing  `json:"listing"`
			Revision catalog.ListingRevision `json:"revision"`
		}
		if importResponse(response.Data, &result) != nil || result.Listing.ID != binding.ListingID || result.Listing.TenantID != tenant || result.Listing.ResourceID != binding.ResourceID || result.Listing.NamespaceID != desired.NamespaceID || result.Listing.CandidateRevisionID != result.Revision.ID || !importETag(ack.ETag, result.Listing.Revision) || !validImportID(result.Revision.ID) || result.Revision.TenantID != tenant || result.Revision.ListingID != result.Listing.ID || result.Revision.VersionRevisionID != binding.VersionRevisionID || !importDigestValid(result.Revision.Digest) {
			return ack, fail
		}
		ack.ID, ack.Digest = result.Revision.ID, result.Revision.Digest
	default:
		return ack, fail
	}
	return ack, nil
}

func importDigestValid(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != 71 {
		return false
	}
	for _, c := range value[7:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func importVersionEvidence(revision catalog.VersionRevision, evidence catalog.EvidenceSet, tenant, version string) bool {
	return validImportID(revision.ID) && revision.TenantID == tenant && revision.ResourceVersionID == version && validImportID(evidence.ID) && evidence.TenantID == tenant && revision.EvidenceSetID == evidence.ID && evidence.SubjectDigest == revision.ContentSetDigest && importDigestValid(revision.ContentSetDigest) && (evidence.State == catalog.EvidenceComplete || evidence.State == catalog.EvidenceBlocked)
}

// Exact immutable revisions qualify retained work. Mutable candidate pointers,
// later approvals and current ETags never turn completed steps into new writes.
func importVerifyAcknowledged(ctx context.Context, session *remoteSession, options remoteOptions, plan importPlan, receipt importReceipt) error {
	for index, step := range receipt.Steps {
		if step.State != "acknowledged" {
			break
		}
		if err := verifyImportCommand(ctx, session, options, plan, receipt.Principal, importReceiptBindings(receipt), index, step); err != nil {
			return err
		}
	}
	return nil
}

func verifyImportCommand(ctx context.Context, session *remoteSession, options remoteOptions, plan importPlan, principal importPrincipal, binding importCommandBindings, index int, step importStep) error {
	fail := safeerr.New("import: exact retained draft reads failed or disagree; preserve the receipt and investigate")
	storedPath, body, etag, err := boundImportCommand(plan, binding, index)
	if err != nil || step.Path != storedPath || step.BodyDigest != importDigest(body) || step.IfMatch != etag {
		return fail
	}
	var path string
	switch index {
	case 0:
		path = "/api/v1/resources/" + step.Ack.ID
	case 1:
		path = "/api/v1/resource-versions/" + step.Ack.ID
	case 2:
		path = "/api/v1/catalog-listings/" + step.Ack.ID
	case 3:
		path = "/api/v1/listing-revisions/" + step.Ack.ID
	}
	response, err := importGET(ctx, session, options, path)
	if err != nil {
		return fail
	}
	switch index {
	case 0:
		ack, err := acknowledgeImportCommand(plan, principal, binding, 0, response)
		if err != nil || ack.ID != step.Ack.ID {
			return fail
		}
	case 1:
		var version catalog.ResourceVersion
		var desired struct {
			Version string `json:"version"`
		}
		_ = importResponse(plan.Bodies[1], &desired)
		if importResponse(response.Data, &version) != nil || version.ID != step.Ack.ID || version.TenantID != principal.TenantID || version.ResourceID != binding.ResourceID || version.Version != desired.Version {
			return fail
		}
		response, err = importGET(ctx, session, options, "/api/v1/version-revisions/"+step.Ack.RevisionID)
		if err != nil {
			return fail
		}
		var result struct {
			Revision catalog.VersionRevision `json:"revision"`
			Evidence catalog.EvidenceSet     `json:"evidence"`
		}
		if importResponse(response.Data, &result) != nil || !importVersionEvidence(result.Revision, result.Evidence, principal.TenantID, step.Ack.ID) || result.Revision.ID != step.Ack.RevisionID || result.Evidence.ID != step.Ack.EvidenceID || result.Revision.ContentSetDigest != step.Ack.Digest {
			return fail
		}
	case 2:
		var listing catalog.CatalogListing
		var desired struct {
			NamespaceID string `json:"namespaceId"`
		}
		_ = importResponse(plan.Bodies[0], &desired)
		if importResponse(response.Data, &listing) != nil || listing.ID != step.Ack.ID || listing.TenantID != principal.TenantID || listing.ResourceID != binding.ResourceID || listing.NamespaceID != desired.NamespaceID {
			return fail
		}
	case 3:
		var revision catalog.ListingRevision
		if importResponse(response.Data, &revision) != nil || revision.ID != step.Ack.ID || revision.TenantID != principal.TenantID || revision.ListingID != binding.ListingID || revision.VersionRevisionID != binding.VersionRevisionID || revision.Digest != step.Ack.Digest {
			return fail
		}
	}
	return nil
}

func applyImport(ctx context.Context, plan importBatchPlan, options remoteOptions, filename string, resume bool) (importOutcome, error) {
	report := importProgress(plan, importReceipt{}, "failed")
	if !plan.Preview.LocallyValid {
		return report, safeerr.New("import: local validation failed; run preview before apply")
	}
	state, err := openImportState(filename, resume)
	if err != nil {
		return report, err
	}
	defer state.close()
	session, err := newRemoteSession(ctx, options)
	if err != nil {
		return report, err
	}
	defer session.close()
	return applyPreparedImport(ctx, plan, options, state, resume, session)
}

// All local preparation and every namespace preflight precede the first write.
// One origin-bound session and one durable receipt cover the entire manifest.
func applyPreparedImport(ctx context.Context, plan importBatchPlan, options remoteOptions, state *importStateFile, resume bool, session *remoteSession) (report importOutcome, resultErr error) {
	if plan.Draft != nil {
		return applyImportDraft(ctx, plan, options, state, resume, session)
	}
	var receipt importReceipt
	disposition := "failed"
	current := -1
	defer func() {
		report = importProgress(plan, receipt, disposition)
		if resultErr != nil && current >= 0 {
			report.CurrentKey = plan.Items[current].Preview.Key
			if report.Items[current].Disposition == "complete" || report.Items[current].Disposition == "incomplete" {
				report.Items[current].Disposition = disposition
			}
		}
	}()
	type admitted struct {
		principal importPrincipal
		budget    int64
	}
	admissions := map[string]admitted{}
	var principal importPrincipal
	var budget int64
	for index, item := range plan.Items {
		current = index
		var resource struct {
			NamespaceID string `json:"namespaceId"`
		}
		if importResponse(item.Bodies[0], &resource) != nil {
			return report, safeerr.New("import: cannot read prepared namespace")
		}
		key := resource.NamespaceID + ":" + string(item.Preview.Kind)
		admission, exists := admissions[key]
		if !exists {
			var err error
			admission.principal, admission.budget, err = importPreflight(ctx, session, options, resource.NamespaceID, item.Preview.Kind)
			if err != nil {
				return report, err
			}
			admissions[key] = admission
		}
		if index == 0 {
			principal, budget = admission.principal, admission.budget
		}
		if principal != admission.principal {
			return report, safeerr.New("import: resolved principal changed during namespace preflight")
		}
		budget = min(budget, admission.budget)
	}
	current = -1
	var err error
	if resume {
		var saved importReceipt
		saved, err = decodeImportReceipt(state.previous)
		if err != nil {
			return report, err
		}
		if saved.Format != 2 || saved.Origin != session.origin || saved.Principal != principal || saved.IntentDigest != importIntent(plan) || len(saved.Steps) != len(plan.Items)*importStepsPerResource {
			return report, safeerr.New("import: origin, resolved principal or prepared inputs changed; refuse resume")
		}
		// Only attach saved progress after its whole-manifest binding matches.
		// Otherwise reordered inputs could label another item's acknowledgements.
		receipt = saved
	} else {
		receipt, err = newImportReceipt(plan, session.origin, principal)
		if err != nil {
			return report, err
		}
		if err = state.save(receipt); err != nil {
			return report, err
		}
	}
	for index, item := range plan.Items {
		current = index
		for step, body := range item.Bodies {
			if receipt.Steps[index*importStepsPerResource+step].State != "acknowledged" && int64(len(body)) > budget {
				return report, safeerr.New("import: prepared requests exceed the advertised server byte limit")
			}
		}
		if err = importVerifyAcknowledged(ctx, session, options, item, importItemReceipt(receipt, index)); err != nil {
			return report, err
		}
	}
	for index, item := range plan.Items {
		current = index
		disposition, err = applyImportItem(ctx, item, options, state, &receipt, index, session, budget)
		if err != nil {
			return report, err
		}
	}
	disposition = "complete"
	return report, nil
}

func applyImportItem(ctx context.Context, plan importPlan, options remoteOptions, state *importStateFile, receipt *importReceipt, itemIndex int, session *remoteSession, budget int64) (string, error) {
	item := importItemReceipt(*receipt, itemIndex)
	for index := range item.Steps {
		step := &item.Steps[index]
		if step.State == "acknowledged" {
			if step.Ack.Blocked {
				return "blocked", safeerr.New("import: retained validation evidence is blocked; no listing was created")
			}
			continue
		}
		path, body, etag, err := importBoundRequest(plan, item, index)
		if err != nil {
			return "failed", err
		}
		result, err := sendImportCommand(ctx, session, options, state, receipt, step, path, body, etag, plan.Preview.MaxRequestBytes, budget, func(response remoteEnvelope) (importAcknowledgement, error) {
			return importAcknowledge(plan, item, index, response)
		})
		if err != nil {
			return result, err
		}
		if step.Ack.Blocked {
			return "blocked", safeerr.New("import: version was committed with blocked validation evidence; no listing was created")
		}
	}
	if err := importVerifyAcknowledged(ctx, session, options, plan, item); err != nil {
		return "uncertain", err
	}
	return "complete", nil
}

// Native and reusable files share one durable transition, replay window and
// error classification. Callers select a finite plan; there is no retry loop.
func sendImportCommand(ctx context.Context, session *remoteSession, options remoteOptions, state *importStateFile, receipt *importReceipt, step *importStep, path string, body []byte, etag string, requestLimit int, budget int64, acknowledge func(remoteEnvelope) (importAcknowledgement, error)) (string, error) {
	var err error
	if int64(len(body)) > budget || len(body) > requestLimit {
		return "failed", safeerr.New("import: encoded request exceeds the smaller client/server byte limit")
	}
	if step.State != "ready" {
		if step.State == "failed" || step.Path != path || step.BodyDigest != importDigest(body) || step.IfMatch != etag || !importReplayAllowed(step.FirstAttempt, time.Now()) {
			return "uncertain", safeerr.New("import: command cannot safely resume; investigate its original key and retained drafts")
		}
	} else {
		step.Path, step.BodyDigest, step.IfMatch, step.FirstAttempt = path, importDigest(body), etag, time.Now().UTC()
	}
	step.State = "inflight"
	if err = state.save(*receipt); err != nil {
		return "uncertain", err
	}
	request := options
	request.path, request.body, request.expectedStatus, request.ifMatch, request.idempotencyKey = path, body, http.StatusCreated, etag, step.Key
	response, err := exchangeRemote(ctx, session.client, request, session.origin, session.token)
	if err == nil {
		step.Ack, err = acknowledge(response)
	}
	if err != nil {
		step.State = "uncertain"
		var apiError remoteAPIError
		if errors.As(err, &apiError) && apiError.Status >= 400 && apiError.Status < 500 && apiError.Status != http.StatusUnauthorized && apiError.Status != http.StatusForbidden && apiError.Code != "invalid_response" {
			step.State = "failed"
		}
		if saveErr := state.save(*receipt); saveErr != nil {
			return "uncertain", saveErr
		}
		return step.State, safeerr.New("import: command was not acknowledged; inspect its receipt before recovery")
	}
	step.State, step.RequestID = "acknowledged", response.Meta.RequestID
	if err = state.save(*receipt); err != nil {
		return "uncertain", err
	}
	return "acknowledged", nil
}
