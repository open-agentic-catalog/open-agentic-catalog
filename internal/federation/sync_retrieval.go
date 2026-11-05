// Synchronization retrieval retains request and response provenance across
// conditional fetches, nested documents and bounded pagination.

package federation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
)

const (
	maxSyncRequests      = 32
	maxSyncDuration      = 30 * time.Second
	maxSyncDecodedBytes  = 64 << 20
	maxSyncContextURLs   = 64
	maxRegistryListPages = maxSyncRequests
	registryListPageSize = 100
	maxRegistryPageToken = 2048
)

type syncRunContextKey struct{}

type syncRun struct {
	source        Source
	requests      int
	decodedBytes  int64
	contexts      map[string]contextResult
	retrievals    []RetrievalEvidence
	states        []RetrievalState
	invalidations map[string]struct{}
	partial       bool
}

type contextResult struct {
	body   []byte
	digest string
	err    error
}

// newSyncRun initializes one source revision's aggregate work budget, context
// cache and pending retrieval evidence.
func newSyncRun(source Source) *syncRun {
	return &syncRun{source: source, contexts: make(map[string]contextResult), invalidations: make(map[string]struct{})}
}

// syncRunFromContext returns the optional synchronization budget attached to
// ctx.
func syncRunFromContext(ctx context.Context) *syncRun {
	run, _ := ctx.Value(syncRunContextKey{}).(*syncRun)
	return run
}

// beforeFetch charges one outbound request and rejects work beyond the
// aggregate sync ceiling.
func (r *syncRun) beforeFetch() error {
	if r.requests >= maxSyncRequests {
		return catalog.E(catalog.CodeDependencyUnavailable, "source synchronization exhausted its aggregate request budget")
	}
	r.requests++
	return nil
}

// accountBytes charges decoded bytes, including cache reuse, against the
// aggregate synchronization budget.
func (r *syncRun) accountBytes(size int) error {
	r.decodedBytes += int64(size)
	if r.decodedBytes > maxSyncDecodedBytes {
		return catalog.E(catalog.CodeDependencyUnavailable, "source synchronization exhausted its aggregate decoded-byte budget")
	}
	return nil
}

// invalidate marks a nonempty conditional-request identity for removal at sync
// commit.
func (r *syncRun) invalidate(requestKey string) {
	if requestKey != "" {
		r.invalidations[requestKey] = struct{}{}
	}
}

// accept replaces reusable state for one request and clears a pending
// invalidation only after parsing acceptance.
func (r *syncRun) accept(state RetrievalState) {
	if state.RequestKey == "" {
		return
	}
	delete(r.invalidations, state.RequestKey)
	for index := range r.states {
		if r.states[index].RequestKey == state.RequestKey {
			r.states[index] = state
			return
		}
	}
	r.states = append(r.states, state)
}

// snapshotEvidence copies retrieval/state slices and sorts invalidation keys
// for deterministic snapshot retention.
func (r *syncRun) snapshotEvidence() ([]RetrievalEvidence, []RetrievalState, []string) {
	invalidations := make([]string, 0, len(r.invalidations))
	for requestKey := range r.invalidations {
		invalidations = append(invalidations, requestKey)
	}
	sort.Strings(invalidations)
	return append([]RetrievalEvidence(nil), r.retrievals...), append([]RetrievalState(nil), r.states...), invalidations
}

type retrievalAcceptance func(bool)

// fetchForParsing adds conditional retrieval and exact response evidence during
// a sync run. The caller accepts a response only after successful parsing, so a
// malformed 200 response cannot replace reusable prior state.
func (m *Manager) fetchForParsing(ctx context.Context, access sourceAccess, endpoint, profileID string, request fetcher.Request, conditional bool, listTerminal bool) (fetcher.Result, retrievalAcceptance, error) {
	run := syncRunFromContext(ctx)
	if run == nil {
		result, err := m.fetchWithAccess(ctx, access, endpoint, request)
		return result, func(bool) {
			// Outside a sync run there is no retrieval state to accept.
		}, err
	}
	request.URL = endpoint
	requestKey, requestIdentity := retrievalRequestIdentity(endpoint, request)
	state, found, err := m.store.GetRetrievalState(ctx, run.source.TenantID, run.source.ID, run.source.Revision, requestKey)
	if err != nil {
		return fetcher.Result{}, nil, err
	}
	useState := conditional && found && catalog.ValidExternalHTTPSURL(endpoint) && catalog.ValidExternalHTTPSURL(state.FinalURL) && state.RequestIdentityDigest == requestIdentity && state.ProfileID == profileID && state.PolicyDigest == run.source.PolicyDigest && state.RequestedURL == endpoint && state.VarySelectionDigest == retrievalVarySelection(request, state.VaryNames) && (!listTerminal || state.ListTerminal)
	if useState && run.source.CredentialBinding == "" && state.CredentialRevision != "" || useState && run.source.CredentialBinding != "" && state.CredentialRevision == "" {
		useState = false
	}
	if useState {
		request.ETag = state.ETag
		request.LastModified = state.LastModified
		request.RejectOriginChanges = true
		request.ExpectedCredentialRevision = state.CredentialRevision
	}
	result, fetchErr := m.fetchWithAccess(ctx, access, endpoint, request)
	if errors.Is(fetchErr, fetcher.ErrCredentialChanged) && useState {
		run.invalidate(requestKey)
		request.ETag = ""
		request.LastModified = ""
		request.ExpectedCredentialRevision = ""
		result, fetchErr = m.fetchWithAccess(ctx, access, endpoint, request)
		useState = false
	}
	if fetchErr != nil {
		return fetcher.Result{}, nil, fetchErr
	}
	reused := false
	receivedBody := result.Status != http.StatusNotModified
	if result.Status == http.StatusNotModified {
		if !useState || len(state.Body) == 0 || state.RepresentationDigest == "" || result.FinalURL != state.FinalURL {
			m.recordRetrievalEvidence(run, requestKey, profileID, result, false, false)
			run.invalidate(requestKey)
			request.ETag = ""
			request.LastModified = ""
			request.ExpectedCredentialRevision = ""
			result, fetchErr = m.fetchWithAccess(ctx, access, endpoint, request)
			if fetchErr != nil {
				return fetcher.Result{}, nil, fetchErr
			}
			if result.Status == http.StatusNotModified {
				m.recordRetrievalEvidence(run, requestKey, profileID, result, false, false)
				return fetcher.Result{}, nil, catalog.E(catalog.CodeDependencyUnavailable, "source returned an unusable not-modified response")
			}
			receivedBody = true
		} else {
			if err := run.accountBytes(len(state.Body)); err != nil {
				return fetcher.Result{}, nil, err
			}
			result.Body = append([]byte(nil), state.Body...)
			result.BodyDigest = state.RepresentationDigest
			result.MediaType = state.MediaType
			result.RequestedURL = state.RequestedURL
			result.FinalURL = state.FinalURL
			if !validETag(result.ETag) {
				result.ETag = state.ETag
			}
			if !validLastModified(result.LastModified) {
				result.LastModified = state.LastModified
			}
			reused = true
			receivedBody = false
		}
	}
	if result.Status != http.StatusOK && result.Status != http.StatusNotModified {
		m.recordRetrievalEvidence(run, requestKey, profileID, result, receivedBody, reused)
		if found {
			run.invalidate(requestKey)
		}
		return result, func(bool) {
			// An unsuccessful response cannot become reusable retrieval state.
		}, nil
	}
	cacheable, varyNames := reusableResponse(result)
	if !catalog.ValidExternalHTTPSURL(endpoint) || !catalog.ValidExternalHTTPSURL(result.FinalURL) || normalizedOrigin(endpoint) != normalizedOrigin(result.FinalURL) {
		cacheable = false
	}
	if reused && len(result.Vary) == 0 {
		varyNames = append([]string(nil), state.VaryNames...)
	}
	if !cacheable && found {
		run.invalidate(requestKey)
	}
	credentialRevision := result.CredentialRevision
	if credentialRevision == "" && reused {
		credentialRevision = state.CredentialRevision
	}
	newState := RetrievalState{
		TenantID: run.source.TenantID, SourceID: run.source.ID, SourceRevision: run.source.Revision,
		RequestKey: requestKey, RequestedURL: endpoint, FinalURL: result.FinalURL, RequestIdentityDigest: requestIdentity,
		ProfileID: profileID, PolicyDigest: run.source.PolicyDigest, CredentialRevision: credentialRevision,
		RepresentationDigest: result.BodyDigest, MediaType: result.MediaType, Body: append([]byte(nil), result.Body...),
		ETag: validOrEmptyETag(result.ETag), LastModified: validOrEmptyLastModified(result.LastModified), VaryNames: varyNames,
		VarySelectionDigest: retrievalVarySelection(request, varyNames), ListTerminal: listTerminal, UpdatedAt: m.clock.Now(),
	}
	if newState.ETag == "" && newState.LastModified == "" {
		cacheable = false
	}
	m.recordRetrievalEvidence(run, requestKey, profileID, result, receivedBody, reused)
	if reused {
		result.Status = http.StatusOK
	}
	return result, func(valid bool) {
		if !valid {
			if found {
				run.invalidate(requestKey)
			}
			return
		}
		if cacheable {
			run.accept(newState)
		}
	}, nil
}

// recordRetrievalEvidence appends bounded response provenance and distinguishes
// received bytes from validated conditional reuse.
func (m *Manager) recordRetrievalEvidence(run *syncRun, requestKey, profileID string, result fetcher.Result, receivedBody, reused bool) {
	fetchedAt := result.FetchedAt
	if fetchedAt.IsZero() {
		fetchedAt = m.clock.Now()
	}
	idValue, err := m.ids.New(fetchedAt.Add(time.Duration(len(run.retrievals)+1) * time.Nanosecond))
	if err != nil {
		run.partial = true
		return
	}
	representationDigest := result.BodyDigest
	if !receivedBody && !reused {
		representationDigest = ""
	}
	run.retrievals = append(run.retrievals, RetrievalEvidence{
		ID: idValue, RequestKey: requestKey, RequestedURLDigest: digestText(result.RequestedURL), FinalURLDigest: digestText(result.FinalURL),
		ProfileID: profileID, Status: result.Status, ReceivedBody: receivedBody, Reused: reused, RepresentationDigest: representationDigest,
		ETagDigest: optionalDigest(result.ETag), LastModifiedDigest: optionalDigest(result.LastModified), FetchedAt: fetchedAt,
	})
}

// retrievalRequestIdentity fingerprints the exact endpoint and
// representation-selecting request inputs.
func retrievalRequestIdentity(endpoint string, request fetcher.Request) (string, string) {
	method := request.Method
	if method == "" {
		method = http.MethodGet
	}
	identity := strings.Join([]string{"source-retrieval-request-v1", method, endpoint, strings.Join(request.Accept, ","), "content-coding:transport-default"}, "\x00")
	digest := digestText(identity)
	return digest, digest
}

// retrievalVarySelection hashes the supported Accept and transport encoding
// choices named by Vary.
func retrievalVarySelection(request fetcher.Request, vary []string) string {
	values := []string{"source-retrieval-vary-v1"}
	for _, name := range vary {
		switch name {
		case "accept":
			values = append(values, name+"="+strings.Join(request.Accept, ","))
		case "accept-encoding":
			values = append(values, name+"=transport-default")
		}
	}
	return digestText(strings.Join(values, "\x00"))
}

// reusableResponse rejects no-store and unsupported Vary fields and requires a
// valid conditional validator.
func reusableResponse(result fetcher.Result) (bool, []string) {
	for _, directive := range strings.Split(result.CacheControl, ",") {
		name, _, _ := strings.Cut(strings.TrimSpace(directive), "=")
		if strings.EqualFold(strings.TrimSpace(name), "no-store") {
			return false, nil
		}
	}
	set := make(map[string]struct{})
	for _, header := range result.Vary {
		for _, item := range strings.Split(header, ",") {
			name := strings.ToLower(strings.TrimSpace(item))
			if name == "" {
				continue
			}
			if name != "accept" && name != "accept-encoding" {
				return false, nil
			}
			set[name] = struct{}{}
		}
	}
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return validETag(result.ETag) || validLastModified(result.LastModified), names
}

// validETag accepts a bounded quoted strong or weak entity tag without control
// characters or embedded quotes.
func validETag(value string) bool {
	if value == "" || len(value) > 512 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return false
	}
	opaque := strings.TrimPrefix(value, `W/`)
	if len(opaque) < 2 || opaque[0] != '"' || opaque[len(opaque)-1] != '"' {
		return false
	}
	return !strings.ContainsRune(opaque[1:len(opaque)-1], '"')
}

// validOrEmptyETag drops malformed conditional validators instead of retaining
// them for later requests.
func validOrEmptyETag(value string) string {
	if validETag(value) {
		return value
	}
	return ""
}

// validLastModified requires a bounded control-free HTTP date.
func validLastModified(value string) bool {
	if value == "" || len(value) > 128 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return false
	}
	_, err := http.ParseTime(value)
	return err == nil
}

// validOrEmptyLastModified retains only a valid Last-Modified date.
func validOrEmptyLastModified(value string) string {
	if validLastModified(value) {
		return value
	}
	return ""
}

// digestText hashes the exact UTF-8 bytes of a retained request or identity
// string.
func digestText(value string) string {
	return digestBytes([]byte(value))
}

// digestBytes returns the canonical sha256: fingerprint of exact bytes.
func digestBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

// optionalDigest preserves absence as empty and hashes a supplied value.
func optionalDigest(value string) string {
	if value == "" {
		return ""
	}
	return digestText(value)
}

// validateSnapshotRetrievals proves that retained conditional state came from
// this bounded run and is safe to reuse during a later sync of that revision.
func validateSnapshotRetrievals(snapshot Snapshot) error {
	if len(snapshot.Retrievals) > maxSyncRequests || len(snapshot.RetrievalStates) > maxSyncRequests || len(snapshot.RetrievalInvalidations) > maxSyncRequests {
		return catalog.E(catalog.CodeInvalidArgument, "source snapshot retrieval evidence exceeds its run bounds")
	}
	identifiers := make(map[string]struct{}, len(snapshot.Retrievals))
	retrievalsByRequest := make(map[string][]RetrievalEvidence, len(snapshot.Retrievals))
	for _, retrieval := range snapshot.Retrievals {
		if retrieval.ID == "" || !validPolicyDigest(retrieval.RequestKey) || !validPolicyDigest(retrieval.RequestedURLDigest) || !validPolicyDigest(retrieval.FinalURLDigest) || !safeRetrievalText(retrieval.ProfileID, 128, false) || retrieval.Status < 100 || retrieval.Status > 599 || retrieval.ReceivedBody && retrieval.RepresentationDigest == "" || retrieval.RepresentationDigest != "" && !validPolicyDigest(retrieval.RepresentationDigest) || retrieval.ETagDigest != "" && !validPolicyDigest(retrieval.ETagDigest) || retrieval.LastModifiedDigest != "" && !validPolicyDigest(retrieval.LastModifiedDigest) || retrieval.FetchedAt.IsZero() || retrieval.FetchedAt.Before(snapshot.StartedAt) || retrieval.FetchedAt.After(*snapshot.CompletedAt) || retrieval.Reused && (retrieval.ReceivedBody || retrieval.Status != http.StatusNotModified || retrieval.RepresentationDigest == "") || retrieval.Status == http.StatusNotModified && (retrieval.ReceivedBody || !retrieval.Reused && retrieval.RepresentationDigest != "") {
			return catalog.E(catalog.CodeInvalidArgument, "source snapshot retrieval evidence is invalid")
		}
		if _, exists := identifiers[retrieval.ID]; exists {
			return catalog.E(catalog.CodeInvalidArgument, "source snapshot contains duplicate retrieval evidence")
		}
		identifiers[retrieval.ID] = struct{}{}
		retrievalsByRequest[retrieval.RequestKey] = append(retrievalsByRequest[retrieval.RequestKey], retrieval)
	}
	states := make(map[string]struct{}, len(snapshot.RetrievalStates))
	for _, state := range snapshot.RetrievalStates {
		if snapshot.State != SnapshotComplete || state.TenantID != snapshot.TenantID || state.SourceID != snapshot.SourceID || state.SourceRevision != snapshot.SourceRevision || ValidateRetainedRetrievalState(state, snapshot.PolicyDigest, snapshot.CredentialRevision) != nil || state.UpdatedAt.Before(snapshot.StartedAt) || state.UpdatedAt.After(*snapshot.CompletedAt) {
			return catalog.E(catalog.CodeInvalidArgument, "source snapshot conditional-retrieval state is invalid")
		}
		matchedEvidence := false
		for _, retrieval := range retrievalsByRequest[state.RequestKey] {
			if retrieval.ProfileID == state.ProfileID && retrieval.RepresentationDigest == state.RepresentationDigest && retrieval.RequestedURLDigest == digestText(state.RequestedURL) && retrieval.FinalURLDigest == digestText(state.FinalURL) && (retrieval.Status == http.StatusOK || retrieval.Reused && retrieval.Status == http.StatusNotModified) {
				matchedEvidence = true
				break
			}
		}
		if !matchedEvidence {
			return catalog.E(catalog.CodeInvalidArgument, "source snapshot conditional-retrieval state has no matching retrieval evidence")
		}
		if _, exists := states[state.RequestKey]; exists {
			return catalog.E(catalog.CodeInvalidArgument, "source snapshot contains duplicate conditional-retrieval state")
		}
		states[state.RequestKey] = struct{}{}
	}
	invalidations := make(map[string]struct{}, len(snapshot.RetrievalInvalidations))
	for _, requestKey := range snapshot.RetrievalInvalidations {
		if !validPolicyDigest(requestKey) {
			return catalog.E(catalog.CodeInvalidArgument, "source snapshot retrieval invalidation is invalid")
		}
		if _, exists := invalidations[requestKey]; exists {
			return catalog.E(catalog.CodeInvalidArgument, "source snapshot contains duplicate retrieval invalidations")
		}
		if _, retained := states[requestKey]; retained {
			return catalog.E(catalog.CodeInvalidArgument, "source snapshot cannot retain and invalidate the same retrieval state")
		}
		invalidations[requestKey] = struct{}{}
	}
	return nil
}

// ValidateRetainedRetrievalState checks one current private conditional-request
// state against the immutable source revision that authorizes its reuse. It
// performs no retrieval and intentionally does not require its originating
// snapshot, whose immutable public-safe attempt rows are checked separately.
func ValidateRetainedRetrievalState(state RetrievalState, policyDigest, credentialRevision string) error {
	if state.TenantID == "" || state.SourceID == "" || state.SourceRevision < 1 || !validPolicyDigest(state.RequestKey) || state.RequestIdentityDigest != state.RequestKey || !safeRetrievalText(state.ProfileID, 128, false) || !catalog.ValidExternalHTTPSURL(state.RequestedURL) || !catalog.ValidExternalHTTPSURL(state.FinalURL) || normalizedOrigin(state.RequestedURL) != normalizedOrigin(state.FinalURL) || state.PolicyDigest != policyDigest || !safeRetrievalText(state.CredentialRevision, 128, true) || state.CredentialRevision != credentialRevision || !validPolicyDigest(state.RepresentationDigest) || len(state.Body) == 0 || len(state.Body) > maxSyncDecodedBytes || digestBytes(state.Body) != state.RepresentationDigest || !safeRetrievalText(state.MediaType, 255, false) || state.MediaType != strings.ToLower(state.MediaType) || state.ETag == "" && state.LastModified == "" || state.ETag != "" && !validETag(state.ETag) || state.LastModified != "" && !validLastModified(state.LastModified) || !validVaryNames(state.VaryNames) || !validPolicyDigest(state.VarySelectionDigest) || state.UpdatedAt.IsZero() {
		return catalog.E(catalog.CodeInvalidArgument, "retained source conditional-retrieval state is invalid")
	}
	return nil
}

// safeRetrievalText enforces the selected byte and empty-value bounds without
// permitting trim changes or control characters.
func safeRetrievalText(value string, limit int, allowEmpty bool) bool {
	if (!allowEmpty && value == "") || len(value) > limit || value != strings.TrimSpace(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return false
	}
	return true
}

// validVaryNames requires sorted unique supported representation-selection
// header names.
func validVaryNames(values []string) bool {
	if len(values) > 2 {
		return false
	}
	previous := ""
	for _, value := range values {
		if value != "accept" && value != "accept-encoding" || previous != "" && value <= previous {
			return false
		}
		previous = value
	}
	return true
}

// fetchContext fetches or reuses bounded JSON-LD context bytes and binds their
// digest to the current sync run.
func (m *Manager) fetchContext(ctx context.Context, access sourceAccess, uri string) ([]byte, string, error) {
	run := syncRunFromContext(ctx)
	if run != nil {
		if cached, ok := run.contexts[uri]; ok {
			if err := run.accountBytes(len(cached.body)); err != nil {
				run.partial = true
				return nil, "", err
			}
			return append([]byte(nil), cached.body...), cached.digest, cached.err
		}
		if len(run.contexts) >= maxSyncContextURLs {
			err := catalog.E(catalog.CodeDependencyUnavailable, "source synchronization exhausted its remote-context budget")
			run.partial = true
			return nil, "", err
		}
	}
	result, accept, err := m.fetchForParsing(ctx, access, uri, "jsonld-context-v1", fetcher.Request{Accept: []string{"application/ld+json", "application/json"}}, true, false)
	if err == nil && result.Status != http.StatusOK && result.Status != http.StatusNotModified {
		err = fmt.Errorf("JSON-LD context returned HTTP %d", result.Status)
	}
	if err == nil {
		err = jsondoc.Validate(result.Body)
	}
	if err == nil {
		var value any
		if decodeErr := json.Unmarshal(result.Body, &value); decodeErr != nil {
			err = decodeErr
		}
	}
	if accept != nil {
		accept(err == nil)
	}
	value := contextResult{body: append([]byte(nil), result.Body...), digest: result.BodyDigest, err: err}
	if run != nil {
		run.contexts[uri] = value
		if err != nil {
			run.partial = true
		}
	}
	return append([]byte(nil), value.body...), value.digest, value.err
}

// normalizationInputDigest fingerprints the ordered context-input bindings that
// influenced semantic normalization.
func normalizationInputDigest(inputs map[string]string) string {
	if len(inputs) == 0 {
		return ""
	}
	keys := make([]string, 0, len(inputs))
	for key := range inputs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	values := []string{"catalog-normalization-input-v1"}
	for _, key := range keys {
		values = append(values, key, inputs[key])
	}
	return digestText(strings.Join(values, "\x00"))
}

// observationIdentityDigest binds exact entry bytes to context-sensitive
// normalization inputs when present.
func observationIdentityDigest(exactDigest, normalizationDigest string) string {
	if normalizationDigest == "" {
		return exactDigest
	}
	return digestText("catalog-observation-identity-v1\x00" + exactDigest + "\x00" + normalizationDigest)
}

// registryListEndpoint derives a safe unambiguous /agents endpoint from a
// supported Registry URL.
func registryListEndpoint(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" || strings.Contains(parsed.EscapedPath(), "%") {
		return ""
	}
	path := strings.TrimSuffix(parsed.Path, "/")
	path = strings.TrimSuffix(path, "/search")
	path = strings.TrimSuffix(path, "/agents")
	parsed.Path = path + "/agents"
	parsed.RawPath = ""
	if !catalog.ValidExternalHTTPSURL(parsed.String()) {
		return ""
	}
	return parsed.String()
}

// appendRegistryPageToken adds a bounded continuation token to the approved
// Registry List endpoint.
func appendRegistryPageToken(endpoint, token string) (string, error) {
	if token != "" && (len(token) > maxRegistryPageToken || strings.IndexFunc(token, unicode.IsControl) >= 0) {
		return "", errors.New("invalid Registry List page token")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.RawQuery != "" {
		return "", errors.New("invalid Registry List endpoint")
	}
	query := parsed.Query()
	query.Set("pageSize", fmt.Sprintf("%d", registryListPageSize))
	if token != "" {
		query.Set("pageToken", token)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

type registryListPage struct {
	Items        []json.RawMessage
	Total        *int
	PageToken    string
	TokenPresent bool
}

// decodeRegistryListPage checks bounded List items, total and continuation
// fields while permitting transport-defined extension members.
func decodeRegistryListPage(body []byte, requestedPageSize int) (registryListPage, error) {
	if err := jsondoc.Validate(body); err != nil {
		return registryListPage{}, errors.New("ambiguous Registry List response JSON")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return registryListPage{}, errors.New("source Registry List response must be an object")
	}
	itemsRaw, ok := fields["items"]
	if !ok || string(itemsRaw) == "null" {
		return registryListPage{}, errors.New("source Registry List response requires a non-null items array")
	}
	var page registryListPage
	if err := json.Unmarshal(itemsRaw, &page.Items); err != nil || page.Items == nil {
		return registryListPage{}, errors.New("source Registry List items must be an array")
	}
	if len(page.Items) > requestedPageSize {
		return registryListPage{}, errors.New("source Registry List response exceeds the requested page size")
	}
	if raw, present := fields["total"]; present {
		var decoded any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&decoded); err != nil {
			return registryListPage{}, errors.New("source Registry List total must be a non-negative integer")
		}
		total, ok := decoded.(json.Number)
		if !ok {
			return registryListPage{}, errors.New("source Registry List total must be a non-negative integer")
		}
		value, err := total.Int64()
		if err != nil || value < 0 || value > int64(^uint(0)>>1) {
			return registryListPage{}, errors.New("source Registry List total must be a bounded non-negative integer")
		}
		converted := int(value)
		page.Total = &converted
	}
	if raw, present := fields["pageToken"]; present {
		page.TokenPresent = true
		if err := json.Unmarshal(raw, &page.PageToken); err != nil || page.PageToken == "" || len(page.PageToken) > maxRegistryPageToken || strings.IndexFunc(page.PageToken, unicode.IsControl) >= 0 {
			return registryListPage{}, errors.New("invalid Registry List page token")
		}
	}
	return page, nil
}

// probeRegistryList tests a candidate interface through source access policy
// and returns a bounded capability finding.
func (m *Manager) probeRegistryList(ctx context.Context, access sourceAccess, candidate string) (string, catalog.Finding) {
	endpoint, err := appendRegistryPageToken(candidate, "")
	if err != nil {
		return "", catalog.Finding{Code: "ARD_REGISTRY_LIST_INVALID", Severity: catalog.SeverityWarning, Message: "The Registry descriptor does not expose a usable List endpoint."}
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", catalog.Finding{Code: "ARD_REGISTRY_LIST_INVALID", Severity: catalog.SeverityWarning, Message: "The Registry List probe URL is invalid."}
	}
	query := parsed.Query()
	query.Set("pageSize", "1")
	parsed.RawQuery = query.Encode()
	result, err := m.fetchWithAccess(ctx, access, parsed.String(), fetcher.Request{Accept: []string{"application/json"}, RejectOriginChanges: true, RedactQueryInErrors: true, MaxResponseBytes: 1 << 20})
	if err != nil {
		return "", catalog.Finding{Code: "ARD_REGISTRY_LIST_UNVERIFIED", Severity: catalog.SeverityWarning, Message: "The advertised Registry List interface could not be verified within the inspection bounds."}
	}
	if result.Status != http.StatusOK || !jsonResponseMediaType(result.MediaType) {
		return "", catalog.Finding{Code: "ARD_REGISTRY_LIST_UNVERIFIED", Severity: catalog.SeverityWarning, Message: "The advertised Registry List interface did not return a valid JSON 200 response."}
	}
	if _, err := decodeRegistryListPage(result.Body, 1); err != nil {
		return "", catalog.Finding{Code: "ARD_REGISTRY_LIST_UNVERIFIED", Severity: catalog.SeverityWarning, Message: "The advertised Registry List interface returned an invalid bounded page."}
	}
	finalURL, err := url.Parse(result.FinalURL)
	if err != nil {
		return "", catalog.Finding{Code: "ARD_REGISTRY_LIST_UNVERIFIED", Severity: catalog.SeverityWarning, Message: "The Registry List probe resolved an invalid endpoint."}
	}
	finalQuery, err := url.ParseQuery(finalURL.RawQuery)
	if err != nil {
		return "", catalog.Finding{Code: "ARD_REGISTRY_LIST_UNVERIFIED", Severity: catalog.SeverityWarning, Message: "The Registry List probe resolved an invalid endpoint."}
	}
	if values, present := finalQuery["pageSize"]; present {
		if len(values) != 1 || values[0] != "1" {
			return "", catalog.Finding{Code: "ARD_REGISTRY_LIST_UNVERIFIED", Severity: catalog.SeverityWarning, Message: "The Registry List probe resolved an invalid endpoint."}
		}
		delete(finalQuery, "pageSize")
	}
	if len(finalQuery) != 0 || finalURL.ForceQuery {
		return "", catalog.Finding{Code: "ARD_REGISTRY_LIST_UNVERIFIED", Severity: catalog.SeverityWarning, Message: "The Registry List probe resolved an invalid endpoint."}
	}
	finalURL.RawQuery = ""
	finalURL.ForceQuery = false
	normalized := finalURL.String()
	if registryListEndpoint(normalized) != normalized {
		return "", catalog.Finding{Code: "ARD_REGISTRY_LIST_UNVERIFIED", Severity: catalog.SeverityWarning, Message: "The Registry List probe resolved an invalid endpoint."}
	}
	return normalized, catalog.Finding{}
}

// enumerateRegistryList follows Registry pagination within page, member, byte,
// and token-cycle bounds. Only a terminal, internally consistent traversal is
// allowed to withdraw observations missing from the new snapshot.
func (m *Manager) enumerateRegistryList(ctx context.Context, source Source) ([]sourceEntry, string, string, bool, []catalog.Finding, error) {
	if source.RegistryListURL == "" {
		return nil, "", source.CredentialRevision, false, nil, catalog.E(catalog.CodePreconditionFailed, "Registry source has no inspected List endpoint")
	}
	access := accessForSource(source, true)
	entries := make([]sourceEntry, 0)
	findings := make([]catalog.Finding, 0)
	seenTokens := make(map[string]struct{})
	seenExact := make(map[string]struct{})
	seenCanonical := make(map[string]struct{})
	seenNormalized := make(map[string]struct{})
	pageDigests := make([]string, 0)
	token := ""
	observedMembers := 0
	var expectedTotal *int
	complete := true
	for pageIndex := 0; pageIndex < maxRegistryListPages; pageIndex++ {
		pageURL, err := appendRegistryPageToken(source.RegistryListURL, token)
		if err != nil {
			return nil, digestListPages(pageDigests), currentCredentialRevision(access, source.CredentialRevision), false, findings, catalog.E(catalog.CodeDependencyUnavailable, "Registry List pagination token was invalid")
		}
		firstPage := pageIndex == 0
		result, accept, err := m.fetchForParsing(ctx, access, pageURL, source.ProfileID, fetcher.Request{Accept: []string{"application/json"}, RejectOriginChanges: true, RedactQueryInErrors: true}, firstPage, firstPage)
		if err != nil {
			return nil, digestListPages(pageDigests), currentCredentialRevision(access, source.CredentialRevision), false, findings, err
		}
		if result.FinalURL != pageURL {
			accept(false)
			return nil, digestListPages(pageDigests), currentCredentialRevision(access, source.CredentialRevision), false, findings, catalog.E(catalog.CodePreconditionFailed, "Registry List interface changed; inspect and review it again before synchronizing")
		}
		if result.Status != http.StatusOK || !jsonResponseMediaType(result.MediaType) {
			return nil, digestListPages(pageDigests), currentCredentialRevision(access, source.CredentialRevision), false, findings, syncHTTPError(result)
		}
		page, err := decodeRegistryListPage(result.Body, registryListPageSize)
		if err != nil {
			accept(false)
			return nil, digestListPages(pageDigests), currentCredentialRevision(access, source.CredentialRevision), false, findings, catalog.E(catalog.CodeInvalidArgument, err.Error())
		}
		terminal := !page.TokenPresent
		if firstPage && !terminal {
			if run := syncRunFromContext(ctx); run != nil {
				requestKey, _ := retrievalRequestIdentity(pageURL, fetcher.Request{Accept: []string{"application/json"}, RejectOriginChanges: true, RedactQueryInErrors: true})
				run.invalidate(requestKey)
			}
			accept(false)
		} else {
			accept(firstPage && terminal)
		}
		pageDigests = append(pageDigests, result.BodyDigest)
		if page.Total != nil {
			if expectedTotal == nil {
				value := *page.Total
				expectedTotal = &value
			} else if *expectedTotal != *page.Total {
				complete = false
				findings = appendBoundedFinding(findings, catalog.Finding{Code: "REGISTRY_LIST_TOTAL_CHANGED", Severity: catalog.SeverityWarning, Message: "Registry List changed its total while pagination was in progress."})
			}
			if *page.Total < observedMembers+len(page.Items) {
				complete = false
				findings = appendBoundedFinding(findings, catalog.Finding{Code: "REGISTRY_LIST_TOTAL_INVALID", Severity: catalog.SeverityWarning, Message: "Registry List returned fewer total members than it enumerated."})
			}
		}
		for itemIndex, raw := range page.Items {
			if observedMembers >= m.maxEntries {
				complete = false
				findings = appendBoundedFinding(findings, catalog.Finding{Code: "SOURCE_ENTRY_BUDGET_EXHAUSTED", Severity: catalog.SeverityWarning, Message: "The source entry budget was exhausted before Registry List completed."})
				break
			}
			entryLocation := fmt.Sprintf("/items/%d", itemIndex)
			observedMembers++
			exact := digestBytes(raw)
			normalized, ok := normalizeJSONValue(raw)
			equivalent := ""
			if ok {
				equivalent = digestBytes(normalized)
			}
			if _, duplicate := seenExact[exact]; duplicate || equivalent != "" && containsDigest(seenCanonical, equivalent) {
				complete = false
				findings = appendBoundedFinding(findings, catalog.Finding{Code: "REGISTRY_LIST_DUPLICATE_MEMBER", Severity: catalog.SeverityWarning, Message: "Registry List repeated a member across its bounded pages."})
				continue
			}
			seenExact[exact] = struct{}{}
			if equivalent != "" {
				seenCanonical[equivalent] = struct{}{}
			}
			envelope, marshalErr := json.Marshal(map[string]any{"entries": []json.RawMessage{raw}})
			if marshalErr != nil {
				return nil, digestListPages(pageDigests), currentCredentialRevision(access, source.CredentialRevision), false, findings, marshalErr
			}
			document, parseErr := m.parseDocumentWithAccess(ctx, envelope, "application/ard+json", access)
			if parseErr != nil || len(document.entries) != 1 {
				complete = false
				findings = appendBoundedFinding(findings, catalog.Finding{Code: "REGISTRY_LIST_MEMBER_INVALID", Severity: catalog.SeverityWarning, Message: "Registry List contained an entry that could not be retained safely."})
				continue
			}
			entry := document.entries[0]
			normalizedDigest := normalizedSourceEntryDigest(entry, source.ProfileID)
			if normalizedDigest != "" {
				if _, duplicate := seenNormalized[normalizedDigest]; duplicate {
					complete = false
					findings = appendBoundedFinding(findings, catalog.Finding{Code: "REGISTRY_LIST_DUPLICATE_MEMBER", Severity: catalog.SeverityWarning, Message: "Registry List repeated a member across its bounded pages."})
					continue
				}
				seenNormalized[normalizedDigest] = struct{}{}
			}
			entry.RetrievalDigest = result.BodyDigest
			entry.EntryLocation = entryLocation
			entries = append(entries, entry)
		}
		if observedMembers >= m.maxEntries && !terminal && complete {
			complete = false
			findings = appendBoundedFinding(findings, catalog.Finding{Code: "SOURCE_ENTRY_BUDGET_EXHAUSTED", Severity: catalog.SeverityWarning, Message: "The source entry budget was exhausted before Registry List completed."})
		}
		if page.TokenPresent && expectedTotal != nil && observedMembers >= *expectedTotal {
			complete = false
			findings = appendBoundedFinding(findings, catalog.Finding{Code: "REGISTRY_LIST_CONTINUATION_INVALID", Severity: catalog.SeverityWarning, Message: "Registry List advertised another page after reaching its declared total."})
			return entries, digestListPages(pageDigests), currentCredentialRevision(access, source.CredentialRevision), complete, findings, nil
		}
		if observedMembers >= m.maxEntries || terminal {
			if terminal && expectedTotal != nil && *expectedTotal != observedMembers {
				complete = false
				findings = appendBoundedFinding(findings, catalog.Finding{Code: "REGISTRY_LIST_TOTAL_MISMATCH", Severity: catalog.SeverityWarning, Message: "Registry List terminal membership did not match its declared total."})
			}
			return entries, digestListPages(pageDigests), currentCredentialRevision(access, source.CredentialRevision), complete, findings, nil
		}
		if _, duplicate := seenTokens[page.PageToken]; duplicate {
			complete = false
			findings = appendBoundedFinding(findings, catalog.Finding{Code: "REGISTRY_LIST_TOKEN_REPEATED", Severity: catalog.SeverityWarning, Message: "Registry List repeated a pagination token."})
			return entries, digestListPages(pageDigests), currentCredentialRevision(access, source.CredentialRevision), complete, findings, nil
		}
		seenTokens[page.PageToken] = struct{}{}
		token = page.PageToken
	}
	findings = appendBoundedFinding(findings, catalog.Finding{Code: "REGISTRY_LIST_PAGE_BUDGET_EXHAUSTED", Severity: catalog.SeverityWarning, Message: "Registry List exceeded the bounded page count."})
	return entries, digestListPages(pageDigests), currentCredentialRevision(access, source.CredentialRevision), false, findings, nil
}

// jsonResponseMediaType reports whether the received content type is acceptable
// JSON for the Registry interface.
func jsonResponseMediaType(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "application/json" || strings.HasPrefix(value, "application/") && strings.HasSuffix(value, "+json")
}

// containsDigest checks whether a page digest was already observed during
// bounded enumeration.
func containsDigest(values map[string]struct{}, candidate string) bool {
	_, ok := values[candidate]
	return ok
}

// appendBoundedFinding adds diagnostic evidence only while the fixed finding
// limit permits.
func appendBoundedFinding(findings []catalog.Finding, finding catalog.Finding) []catalog.Finding {
	if len(findings) < 100 {
		return append(findings, finding)
	}
	if len(findings) == 100 {
		return append(findings, catalog.Finding{Code: "SOURCE_FINDINGS_TRUNCATED", Severity: catalog.SeverityWarning, Message: "Additional source findings were omitted from this bounded run summary."})
	}
	return findings
}

// currentCredentialRevision uses the revision observed during this access
// operation, falling back when none was captured.
func currentCredentialRevision(access sourceAccess, fallback string) string {
	if access.credentialRevision != nil && *access.credentialRevision != "" {
		return *access.credentialRevision
	}
	return fallback
}

// digestListPages binds ordered page digests into the complete enumeration
// fingerprint.
func digestListPages(digests []string) string {
	if len(digests) == 0 {
		return ""
	}
	return digestText("registry-list-snapshot-v1\x00" + strings.Join(digests, "\x00"))
}

// syncHTTPError classifies a non-success response for durable sync retry
// without retaining unrestricted response content.
func syncHTTPError(result fetcher.Result) error {
	switch result.Status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return &SyncError{Class: SyncFailureAuthentication, Safe: "Source authentication failed."}
	case http.StatusTooManyRequests:
		return &SyncError{Class: SyncFailureRateLimited, Safe: "Source rate limited synchronization.", RetryAfter: fetcher.RetryAfterDelay(result.RetryAfter, result.FetchedAt)}
	case http.StatusServiceUnavailable:
		return &SyncError{Class: SyncFailureTransient, Safe: "Source is temporarily unavailable.", RetryAfter: fetcher.RetryAfterDelay(result.RetryAfter, result.FetchedAt)}
	case http.StatusRequestTimeout, http.StatusInternalServerError, http.StatusBadGateway, http.StatusGatewayTimeout:
		return &SyncError{Class: SyncFailureTransient, Safe: "Source is temporarily unavailable."}
	default:
		if result.Status >= 500 && result.Status <= 599 && result.Status != http.StatusNotImplemented && result.Status != http.StatusHTTPVersionNotSupported {
			return &SyncError{Class: SyncFailureTransient, Safe: "Source is temporarily unavailable."}
		}
		return &SyncError{Class: SyncFailurePermanent, Safe: fmt.Sprintf("Source returned unsupported HTTP status %d.", result.Status)}
	}
}

type SyncFailureClass string

const (
	SyncFailureInvalidInput   SyncFailureClass = "invalid_input"
	SyncFailurePolicy         SyncFailureClass = "policy_denied"
	SyncFailureAuthentication SyncFailureClass = "authentication"
	SyncFailureRateLimited    SyncFailureClass = "rate_limited"
	SyncFailureTransient      SyncFailureClass = "transient_upstream"
	SyncFailurePermanent      SyncFailureClass = "permanent_upstream"
	SyncFailureDependency     SyncFailureClass = "dependency"
	SyncFailureInternal       SyncFailureClass = "internal"
	SyncFailureCancelled      SyncFailureClass = "cancelled"
)

type SyncError struct {
	Class      SyncFailureClass
	Safe       string
	RetryAfter time.Duration
	Cause      error
}

// Error includes the underlying diagnostic cause when available, otherwise the
// classified sync failure.
func (e *SyncError) Error() string {
	if e.Safe != "" {
		return e.Safe
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return "source synchronization failed"
}

// Unwrap exposes the cause for transport and dependency classification.
func (e *SyncError) Unwrap() error { return e.Cause }

// ClassifySyncError maps sync, cancellation and dependency failures to the
// fixed retry class and optional retry delay.
func ClassifySyncError(err error) (SyncFailureClass, time.Duration) {
	if err == nil {
		return "", 0
	}
	var classified *SyncError
	if errors.As(err, &classified) {
		return classified.Class, classified.RetryAfter
	}
	if errors.Is(err, context.Canceled) {
		return SyncFailureCancelled, 0
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return SyncFailureTransient, 0
	}
	if errors.Is(err, fetcher.ErrPolicyChanged) {
		return SyncFailurePolicy, 0
	}
	switch catalog.ErrorCodeOf(err) {
	case catalog.CodeInvalidArgument, catalog.CodeUnsupportedProfile, catalog.CodePreconditionFailed, catalog.CodeNotFound, catalog.CodeWorkLimitExceeded:
		return SyncFailureInvalidInput, 0
	case catalog.CodeSourcePolicyDenied, catalog.CodeForbidden:
		return SyncFailurePolicy, 0
	case catalog.CodeDependencyUnavailable:
		return SyncFailureDependency, 0
	case catalog.CodeInternal, catalog.CodeAuditExportRangeTooLarge, catalog.CodeWebhookTestBusy:
		return SyncFailureInternal, 0
	case catalog.CodeMethodNotAllowed, catalog.CodeConflict, catalog.CodeUnauthenticated, catalog.CodeValidationFailed, catalog.CodeVersionSealed, catalog.CodeSeparationOfDuties, catalog.CodeInvalidTransition, catalog.CodeIdempotencyKeyReused, catalog.CodePublicationNotReady, catalog.CodeRequestDeadlineExceeded:
		return SyncFailureTransient, 0
	default:
		return SyncFailureTransient, 0
	}
}
