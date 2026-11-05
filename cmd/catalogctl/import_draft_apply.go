package main

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

// Format 3 is one reusable file and only its real commands. Format 2 remains
// the native manifest receipt, including its original four-command items.
type importDraftBinding struct {
	NamespaceID    string `json:"namespaceId"`
	ResourceID     string `json:"resourceId,omitempty"`
	ListingID      string `json:"listingId,omitempty"`
	ListingETag    string `json:"listingETag,omitempty"`
	IncludeListing bool   `json:"includeListing"`
}

func (b importDraftBinding) kinds() []int {
	kinds := []int{1}
	if b.ResourceID == "" {
		kinds = []int{0, 1}
	}
	if b.IncludeListing {
		if b.ListingID == "" {
			kinds = append(kinds, 2)
		}
		kinds = append(kinds, 3)
	}
	return kinds
}

func validImportReceiptShape(receipt importReceipt) bool {
	if receipt.Format == 2 {
		return receipt.Draft == nil && len(receipt.Steps) >= 4 && len(receipt.Steps) <= importMaxSteps && len(receipt.Steps)%4 == 0
	}
	if receipt.Format != 3 || receipt.Draft == nil {
		return false
	}
	b := receipt.Draft
	return validImportID(b.NamespaceID) && (b.ResourceID == "" || validImportID(b.ResourceID)) &&
		(b.ListingID == "" && b.ListingETag == "" || validImportID(b.ListingID) && b.ResourceID != "" && b.IncludeListing && strongImportETag(b.ListingETag)) && len(receipt.Steps) == len(b.kinds())
}

func strongImportETag(value string) bool {
	if !strings.HasPrefix(value, `"rev-`) || !strings.HasSuffix(value, `"`) {
		return false
	}
	n, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(value, `"rev-`), `"`), 10, 64)
	return err == nil && importETag(value, n)
}

type importCommandBindings struct{ ResourceID, ListingID, VersionRevisionID, ListingETag string }

func importReceiptBindings(receipt importReceipt) importCommandBindings {
	var b importCommandBindings
	if len(receipt.Steps) > 0 {
		b.ResourceID = receipt.Steps[0].Ack.ID
	}
	if len(receipt.Steps) > 1 {
		b.VersionRevisionID = receipt.Steps[1].Ack.RevisionID
	}
	if len(receipt.Steps) > 2 {
		b.ListingID, b.ListingETag = receipt.Steps[2].Ack.ID, receipt.Steps[2].Ack.ETag
	}
	return b
}

func importDraftCommandBindings(receipt importReceipt) importCommandBindings {
	b := importCommandBindings{ResourceID: receipt.Draft.ResourceID, ListingID: receipt.Draft.ListingID, ListingETag: receipt.Draft.ListingETag}
	for index, kind := range receipt.Draft.kinds() {
		if receipt.Steps[index].State != "acknowledged" {
			break
		}
		ack := receipt.Steps[index].Ack
		switch kind {
		case 0:
			b.ResourceID = ack.ID
		case 1:
			b.VersionRevisionID = ack.RevisionID
		case 2:
			b.ListingID, b.ListingETag = ack.ID, ack.ETag
		}
	}
	return b
}

func importDraftProgress(plan importBatchPlan, receipt importReceipt, disposition string) importOutcome {
	item := importItemOutcome{Key: "draft", Disposition: "unattempted", Findings: []catalog.Finding{}}
	if len(plan.Items) == 1 {
		item.Key = plan.Items[0].Preview.Key
	}
	item.Steps = slices.Clone(receipt.Steps)
	acked, blocked := 0, false
	for index, step := range item.Steps {
		item.Findings = append(item.Findings, step.Ack.Findings...)
		item.Steps[index].Ack.Findings = nil
		if step.State == "acknowledged" {
			acked++
		}
		if step.Ack.Blocked {
			blocked = true
		}
		if step.State == "uncertain" || step.State == "inflight" {
			item.Disposition = "uncertain"
		}
		if step.State == "failed" {
			item.Disposition = "failed"
		}
	}
	if acked > 0 && item.Disposition == "unattempted" {
		item.Disposition = "incomplete"
	}
	if acked > 0 && acked == len(item.Steps) {
		item.Disposition = "complete"
	}
	if blocked {
		item.Disposition = "blocked"
	}
	if disposition != "complete" && item.Disposition == "complete" {
		item.Disposition = disposition
	}
	return importOutcome{Format: 1, Disposition: disposition, Items: []importItemOutcome{item}}
}

func importDraftResource(ctx context.Context, session *remoteSession, options remoteOptions, item importPlan, id, namespace, tenant string) (catalog.Resource, error) {
	fail := safeerr.New("import: exact Resource does not match the draft kind, canonical identifier, tenant or Namespace")
	response, err := importGET(ctx, session, options, "/api/v1/resources/"+id)
	if err != nil {
		return catalog.Resource{}, err
	}
	var resource catalog.Resource
	var desired catalog.CreateResourceInput
	if importResponse(item.Bodies[0], &desired) != nil || importResponse(response.Data, &resource) != nil || resource.ID != id || !validImportID(resource.NamespaceID) || !validImportID(resource.TenantID) || resource.Kind != desired.Kind || resource.CanonicalIdentifier != desired.CanonicalIdentifier || namespace != "" && resource.NamespaceID != namespace || tenant != "" && resource.TenantID != tenant || !importETag(response.Meta.ETag, resource.Revision) {
		return resource, fail
	}
	return resource, nil
}

func importDraftListing(ctx context.Context, session *remoteSession, options remoteOptions, id string, b importDraftBinding, tenant string) (string, error) {
	response, err := importGET(ctx, session, options, "/api/v1/catalog-listings/"+id)
	if err != nil {
		return "", err
	}
	var listing catalog.CatalogListing
	if importResponse(response.Data, &listing) != nil || listing.ID != id || listing.ResourceID != b.ResourceID || listing.NamespaceID != b.NamespaceID || listing.TenantID != tenant || !importETag(response.Meta.ETag, listing.Revision) {
		return "", safeerr.New("import: exact Listing has a mismatched parent, tenant, Namespace or ETag")
	}
	return response.Meta.ETag, nil
}

func importDraftFindListing(ctx context.Context, session *remoteSession, options remoteOptions, b importDraftBinding, tenant string) (string, error) {
	response, err := importGET(ctx, session, options, "/api/v1/resources/"+b.ResourceID+"/listings?limit=2")
	if err != nil {
		return "", err
	}
	var page struct {
		Items      *[]catalog.CatalogListing `json:"items"`
		NextCursor json.RawMessage           `json:"nextCursor"`
		Partial    bool                      `json:"partial"`
	}
	var next string
	if importResponse(response.Data, &page) != nil || page.Items == nil || len(*page.Items) > 1 || page.Partial || len(page.NextCursor) > 0 && jsondoc.Decode(page.NextCursor, &next) != nil || next != "" {
		return "", safeerr.New("import: Listing lookup must return a complete, unambiguous zero-or-one result; supply --listing-id when appropriate")
	}
	if len(*page.Items) == 0 {
		return "", nil
	}
	listing := (*page.Items)[0]
	if !validImportID(listing.ID) || listing.ResourceID != b.ResourceID || listing.NamespaceID != b.NamespaceID || listing.TenantID != tenant {
		return "", safeerr.New("import: Listing lookup returned a mismatched parent, tenant or Namespace")
	}
	return listing.ID, nil
}

func applyImportDraft(ctx context.Context, plan importBatchPlan, options remoteOptions, state *importStateFile, resume bool, session *remoteSession) (report importOutcome, resultErr error) {
	var receipt importReceipt
	disposition := "failed"
	defer func() { report = importDraftProgress(plan, receipt, disposition) }()
	item := plan.Items[0]
	destination := plan.Draft
	binding := importDraftBinding{NamespaceID: destination.NamespaceID, ResourceID: destination.ResourceID, IncludeListing: len(item.Bodies) == 4}
	var saved importReceipt
	var err error
	if resume {
		saved, err = decodeImportReceipt(state.previous)
		if err != nil {
			return report, err
		}
		if saved.Format != 3 || saved.Draft == nil || saved.Origin != session.origin {
			return report, safeerr.New("import: receipt is not for this reusable file and origin")
		}
		binding = *saved.Draft // Never repeat a zero-or-one lookup or rebase its ETag.
		if binding.ResourceID != destination.ResourceID || destination.NamespaceID != "" && binding.NamespaceID != destination.NamespaceID || destination.ListingID != "" && binding.ListingID != destination.ListingID || binding.IncludeListing != (len(item.Bodies) == 4) {
			return report, safeerr.New("import: destination inputs changed; refuse resume")
		}
	}
	var existing catalog.Resource
	if binding.ResourceID != "" {
		existing, err = importDraftResource(ctx, session, options, item, binding.ResourceID, binding.NamespaceID, "")
		if err != nil {
			return report, err
		}
		binding.NamespaceID = existing.NamespaceID
	}
	required := []identity.Permission{"resource.edit", "resource.read", "resource.read_content"}
	if binding.ResourceID == "" {
		required = append(required, "resource.create")
	}
	if binding.IncludeListing && binding.ResourceID != "" && destination.ListingID == "" && !resume {
		required = append(required, "resource.read_history")
	}
	principal, budget, err := importPermissionPreflight(ctx, session, options, binding.NamespaceID, item.Preview.Kind, required)
	if err != nil {
		return report, err
	}
	if existing.ID != "" && existing.TenantID != principal.TenantID {
		return report, safeerr.New("import: Resource tenant does not match the current principal")
	}
	// Bind the resolved Namespace in the existing Resource template. It is used
	// for acknowledgement checks, never sent as a new Resource in Version mode.
	desired, err := importJSONObject(item.Bodies[0])
	if err != nil {
		return report, err
	}
	desired["namespaceId"] = binding.NamespaceID
	item.Bodies = slices.Clone(item.Bodies)
	item.Bodies[0], err = json.Marshal(desired)
	if err != nil {
		return report, safeerr.New("import: cannot encode the resolved Namespace")
	}
	if binding.IncludeListing {
		listing, err := importJSONObject(item.Bodies[2])
		if err != nil {
			return report, err
		}
		listing["namespaceId"] = binding.NamespaceID
		item.Bodies[2], err = json.Marshal(listing)
		if err != nil {
			return report, safeerr.New("import: cannot encode the resolved Listing Namespace")
		}
	}
	plan.Items = []importPlan{item}
	if !resume && binding.IncludeListing && binding.ResourceID != "" {
		binding.ListingID = destination.ListingID
		if binding.ListingID == "" {
			binding.ListingID, err = importDraftFindListing(ctx, session, options, binding, principal.TenantID)
			if err != nil {
				return report, err
			}
		}
		if binding.ListingID != "" {
			binding.ListingETag, err = importDraftListing(ctx, session, options, binding.ListingID, binding, principal.TenantID)
			if err != nil {
				return report, err
			}
		}
	}
	if resume {
		if saved.Principal != principal || saved.IntentDigest != importIntent(plan) {
			return report, safeerr.New("import: resolved principal or prepared inputs changed; refuse resume")
		}
		receipt = saved
	} else {
		receipt, err = createImportSteps(importReceipt{Format: 3, Origin: session.origin, Principal: principal, IntentDigest: importIntent(plan), Draft: &binding}, len(binding.kinds()))
		if err != nil {
			return report, err
		}
		if err = state.save(receipt); err != nil {
			return report, err
		}
	}
	kinds := binding.kinds()
	if binding.ListingID != "" {
		etag, err := importDraftListing(ctx, session, options, binding.ListingID, binding, principal.TenantID)
		if err != nil {
			return report, err
		}
		// An uncertain revision can itself have changed the current ETag. Replay
		// its original key and If-Match; never replace them with this new ETag.
		if receipt.Steps[len(kinds)-1].State == "ready" && etag != binding.ListingETag {
			return report, safeerr.New("import: Listing ETag changed before the revision command; refuse stale preparation")
		}
	}
	for index, kind := range kinds {
		step := receipt.Steps[index]
		if step.State == "acknowledged" {
			if err = verifyImportCommand(ctx, session, options, item, principal, importDraftCommandBindings(receipt), kind, step); err != nil {
				return report, err
			}
		} else if int64(len(item.Bodies[kind])) > budget {
			return report, safeerr.New("import: prepared requests exceed the advertised server byte limit")
		}
	}
	for index, kind := range kinds {
		step := &receipt.Steps[index]
		if step.State != "acknowledged" {
			bindings := importDraftCommandBindings(receipt)
			path, body, etag, err := boundImportCommand(item, bindings, kind)
			if err != nil {
				return report, err
			}
			disposition, err = sendImportCommand(ctx, session, options, state, &receipt, step, path, body, etag, item.Preview.MaxRequestBytes, budget, func(response remoteEnvelope) (importAcknowledgement, error) {
				return acknowledgeImportCommand(item, principal, bindings, kind, response)
			})
			if err != nil {
				return report, err
			}
		}
		if step.Ack.Blocked {
			disposition = "blocked"
			return report, safeerr.New("import: Version was committed with blocked validation evidence; no Listing revision was created")
		}
	}
	for index, kind := range kinds {
		if err = verifyImportCommand(ctx, session, options, item, principal, importDraftCommandBindings(receipt), kind, receipt.Steps[index]); err != nil {
			disposition = "uncertain"
			return report, err
		}
	}
	disposition = "complete"
	return report, nil
}
