// Smoke workflows exercise governed authoring and public discovery on an
// explicitly selected disposable API target.

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	diagnostic "github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/diagnostics"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

const smokeMaximumResponseBytes = 8 << 20

var smokeRunIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type smokeOptions struct {
	BaseURL          string
	AdminToken       string
	ContributorToken string
	ReviewerToken    string
	PublisherToken   string
	ReaderToken      string
	OtherTenantToken string
	RunID            string
	Timeout          time.Duration
	AllowRemote      bool
}

type smokeReport struct {
	Status           string          `json:"status"`
	BaseURL          string          `json:"baseUrl"`
	RunID            string          `json:"runId"`
	Checks           []string        `json:"checks"`
	Resources        []smokeResource `json:"resources"`
	SnapshotID       string          `json:"snapshotId"`
	SnapshotETag     string          `json:"snapshotEtag"`
	ARDEntries       int             `json:"ardEntries"`
	AICatalogEntries int             `json:"aiCatalogEntries"`
}

type smokeResource struct {
	Kind            catalog.Kind `json:"kind"`
	Identifier      string       `json:"identifier"`
	ResourceID      string       `json:"resourceId"`
	ListingID       string       `json:"listingId"`
	ReleaseID       string       `json:"releaseId"`
	DocumentID      string       `json:"documentId,omitempty"`
	documentContent []byte
}

type smokeFixture struct {
	Kind           catalog.Kind
	Name           string
	Segment        string
	Description    string
	Representation map[string]any
}

type smokeHTTPClient struct {
	baseURL      string
	client       *http.Client
	checks       []string
	demoProgress *demoProgress // Only the explicit local demo workflow journals commands.
	safeErrors   bool          // Developer tools can opt out of smoke's response excerpts.
}

type smokeHTTPResponse struct {
	Header http.Header
	Body   []byte
}

// newLocalWorkflowClient serves explicitly selected local developer workflows.
// Callers validate a literal loopback origin first; redirects and environment
// proxies must not turn that selection into a different destination.
func newLocalWorkflowClient() *http.Client {
	return &http.Client{
		Transport:     &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second, MaxResponseHeaderBytes: 32 << 10},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// smoke runs the validated mutating walkthrough within its whole-command deadline and prints its report.
func smoke(arguments []string) error {
	options, err := parseSmokeOptions(arguments)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), options.Timeout)
	defer cancel()
	report, err := executeSmoke(ctx, options)
	if err != nil {
		return err
	}
	return printJSON(report)
}

// parseSmokeOptions validates target authority, identities, run naming and deadlines before the walkthrough starts.
func parseSmokeOptions(arguments []string) (smokeOptions, error) {
	flags := diagnostic.Flags("catalogctl smoke")
	options := smokeOptions{}
	flags.StringVar(&options.BaseURL, "base-url", environmentOrDefault("CATALOG_SMOKE_BASE_URL", "http://127.0.0.1:8080"), "running Catalog service base URL")
	flags.StringVar(&options.AdminToken, "admin-token", environmentOrDefault("CATALOG_SMOKE_ADMIN_TOKEN", "local-admin"), "development tenant administrator token")
	flags.StringVar(&options.ContributorToken, "contributor-token", environmentOrDefault("CATALOG_SMOKE_CONTRIBUTOR_TOKEN", "local-contributor"), "development contributor token")
	flags.StringVar(&options.ReviewerToken, "reviewer-token", environmentOrDefault("CATALOG_SMOKE_REVIEWER_TOKEN", "local-reviewer"), "development reviewer token")
	flags.StringVar(&options.PublisherToken, "publisher-token", environmentOrDefault("CATALOG_SMOKE_PUBLISHER_TOKEN", "local-publisher"), "development publisher token")
	flags.StringVar(&options.ReaderToken, "reader-token", environmentOrDefault("CATALOG_SMOKE_READER_TOKEN", "local-reader"), "development reader token")
	flags.StringVar(&options.OtherTenantToken, "other-tenant-token", environmentOrDefault("CATALOG_SMOKE_OTHER_TENANT_TOKEN", "other-tenant-admin"), "development token for the negative tenant-isolation check")
	flags.StringVar(&options.RunID, "run-id", "", "optional lowercase identifier for the created walkthrough records")
	flags.DurationVar(&options.Timeout, "timeout", 2*time.Minute, "maximum duration for the complete walkthrough")
	flags.BoolVar(&options.AllowRemote, "allow-remote", false, "allow the walkthrough to create records on a non-loopback HTTPS service")
	if err := diagnostic.Parse(flags, arguments); err != nil {
		return smokeOptions{}, err
	}
	if flags.NArg() != 0 {
		return smokeOptions{}, safeerr.New("usage: catalogctl smoke [--base-url URL] [--run-id ID] [--timeout DURATION] [--allow-remote]")
	}
	if options.Timeout <= 0 || options.Timeout > 10*time.Minute {
		return smokeOptions{}, safeerr.New("smoke timeout must be greater than zero and at most 10m")
	}
	if options.RunID == "" {
		generated, err := newSmokeRunID()
		if err != nil {
			return smokeOptions{}, err
		}
		options.RunID = generated
	}
	options.RunID = strings.ToLower(strings.TrimSpace(options.RunID))
	if len(options.RunID) > 40 || !smokeRunIDPattern.MatchString(options.RunID) {
		return smokeOptions{}, safeerr.New("smoke run ID must be at most 40 lowercase letters, numbers, or single hyphen-separated words")
	}
	baseURL, err := validateSmokeBaseURL(options.BaseURL, options.AllowRemote)
	if err != nil {
		return smokeOptions{}, err
	}
	options.BaseURL = baseURL
	for name, token := range map[string]string{
		"admin": options.AdminToken, "contributor": options.ContributorToken, "reviewer": options.ReviewerToken,
		"publisher": options.PublisherToken, "reader": options.ReaderToken, "other tenant": options.OtherTenantToken,
	} {
		if strings.TrimSpace(token) == "" {
			return smokeOptions{}, fmt.Errorf("%s token is required", name)
		}
	}
	return options, nil
}

// executeSmoke performs the destructive, disposable-service readiness workflow:
// it creates governed records, publishes them, and verifies authenticated,
// public, conditional, and cross-tenant behavior through HTTP only.
func executeSmoke(ctx context.Context, options smokeOptions) (smokeReport, error) {
	baseURL, err := validateSmokeBaseURL(options.BaseURL, options.AllowRemote)
	if err != nil {
		return smokeReport{}, err
	}
	if options.Timeout <= 0 {
		options.Timeout = 2 * time.Minute
	}
	client := &smokeHTTPClient{
		baseURL: baseURL,
		client: &http.Client{
			Timeout: options.Timeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		checks: make([]string, 0, 72),
	}

	if _, err := client.request(ctx, "liveness", http.MethodGet, "/livez", "", "", "", nil, http.StatusOK); err != nil {
		return smokeReport{}, err
	}
	if _, err := client.request(ctx, "dependency readiness", http.MethodGet, "/readyz", "", "", "", nil, http.StatusOK); err != nil {
		return smokeReport{}, err
	}
	metrics, err := client.request(ctx, "Prometheus metrics", http.MethodGet, "/metrics", "", "", "", nil, http.StatusOK)
	if err != nil {
		return smokeReport{}, err
	}
	if !bytes.Contains(metrics.Body, []byte("open_agentic_catalog_http_requests_total")) {
		return smokeReport{}, safeerr.New("prometheus metrics check failed: expected request counter is absent")
	}
	capabilities, err := client.request(ctx, "authenticated capabilities", http.MethodGet, "/api/v1/capabilities", options.AdminToken, "", "", nil, http.StatusOK)
	if err != nil {
		return smokeReport{}, err
	}
	if err := validateSmokeCapabilities(capabilities); err != nil {
		return smokeReport{}, err
	}
	if err := smokeExerciseMembershipAdministration(ctx, client, options); err != nil {
		return smokeReport{}, err
	}

	namespaceResponse, err := client.request(ctx, "create namespace", http.MethodPost, "/api/v1/namespaces", options.AdminToken, options.RunID+"-namespace", "", map[string]any{"slug": options.RunID, "displayName": "Catalog readiness " + options.RunID}, http.StatusCreated)
	if err != nil {
		return smokeReport{}, err
	}
	var namespace catalog.Namespace
	if err := decodeSmokeResponse(namespaceResponse, &namespace); err != nil {
		return smokeReport{}, fmt.Errorf("create namespace check: invalid response: %w", err)
	}
	if namespace.ID == "" || namespaceResponse.Header.Get("ETag") == "" {
		return smokeReport{}, safeerr.New("create namespace check: response ID or ETag is absent")
	}
	namespaceResponse, err = client.request(ctx, "update namespace metadata", http.MethodPatch, "/api/v1/namespaces/"+url.PathEscape(namespace.ID), options.AdminToken, options.RunID+"-namespace-update", namespaceResponse.Header.Get("ETag"), map[string]any{"displayName": "Catalog readiness namespace " + options.RunID, "description": "Disposable namespace used by the complete Catalog readiness workflow.", "reason": "Exercise optimistic namespace administration"}, http.StatusOK)
	if err != nil {
		return smokeReport{}, err
	}
	if err := decodeSmokeResponse(namespaceResponse, &namespace); err != nil {
		return smokeReport{}, fmt.Errorf("update namespace metadata check: invalid response: %w", err)
	}
	if namespace.Revision != 2 || namespaceResponse.Header.Get("ETag") != `"rev-2"` {
		return smokeReport{}, fmt.Errorf("update namespace metadata check: unexpected namespace revision %#v", namespace)
	}

	domain := options.RunID + ".catalog.example.com"
	publisherResponse, err := client.request(ctx, "create publisher", http.MethodPost, "/api/v1/publishers", options.AdminToken, options.RunID+"-publisher", "", map[string]any{"displayName": "Catalog readiness publisher", "domains": []string{domain}}, http.StatusCreated)
	if err != nil {
		return smokeReport{}, err
	}
	var publisher catalog.Publisher
	if err := decodeSmokeResponse(publisherResponse, &publisher); err != nil {
		return smokeReport{}, fmt.Errorf("create publisher check: invalid response: %w", err)
	}
	if publisher.ID == "" || publisherResponse.Header.Get("ETag") == "" {
		return smokeReport{}, safeerr.New("create publisher check: response ID or ETag is absent")
	}
	publisherResponse, err = client.request(ctx, "update publisher metadata", http.MethodPatch, "/api/v1/publishers/"+url.PathEscape(publisher.ID), options.AdminToken, options.RunID+"-publisher-update", publisherResponse.Header.Get("ETag"), map[string]any{"displayName": "Catalog readiness publisher " + options.RunID, "domains": []string{strings.ToUpper(domain) + ".", domain}, "reason": "Exercise optimistic Publisher administration"}, http.StatusOK)
	if err != nil {
		return smokeReport{}, err
	}
	if err := decodeSmokeResponse(publisherResponse, &publisher); err != nil {
		return smokeReport{}, fmt.Errorf("update publisher metadata check: invalid response: %w", err)
	}
	if publisher.Revision != 2 || len(publisher.Domains) != 1 || publisher.Domains[0] != domain || publisherResponse.Header.Get("ETag") != `"rev-2"` {
		return smokeReport{}, fmt.Errorf("update publisher metadata check: unexpected Publisher %#v", publisher)
	}
	approvedPublisher, err := client.request(ctx, "approve publisher publication domain", http.MethodPost, "/api/v1/publishers/"+url.PathEscape(publisher.ID)+"/publication-domains/"+url.PathEscape(domain)+":approve", options.AdminToken, options.RunID+"-approve-publication-domain", publisherResponse.Header.Get("ETag"), map[string]any{"reason": "Catalog readiness walkthrough"}, http.StatusOK)
	if err != nil {
		return smokeReport{}, err
	}
	if approvedPublisher.Header.Get("ETag") == "" || approvedPublisher.Header.Get("ETag") == publisherResponse.Header.Get("ETag") {
		return smokeReport{}, safeerr.New("approve publisher publication domain check: publisher revision did not advance")
	}
	var publisherApproval catalog.Publisher
	if err := decodeSmokeResponse(approvedPublisher, &publisherApproval); err != nil {
		return smokeReport{}, fmt.Errorf("approve publisher publication domain check: invalid response: %w", err)
	}
	if len(publisherApproval.ApprovedPublicationDomains) != 1 || publisherApproval.ApprovedPublicationDomains[0] != domain {
		return smokeReport{}, fmt.Errorf("approve publisher publication domain check: unexpected approval projection %#v", publisherApproval.ApprovedPublicationDomains)
	}

	fixtures := smokeFixtures(options.RunID)
	created := make([]smokeResource, 0, len(fixtures))
	identifiers := make([]string, 0, len(fixtures))
	for _, fixture := range fixtures {
		createdResource, createErr := smokePublishFixture(ctx, client, options, namespace.ID, publisher.ID, domain, fixture)
		if createErr != nil {
			return smokeReport{}, createErr
		}
		created = append(created, createdResource)
		identifiers = append(identifiers, createdResource.Identifier)
	}

	catalogSearch, err := client.request(ctx, "governed Catalog search", http.MethodPost, "/api/v1/catalog/search", options.ReaderToken, "", "", map[string]any{"text": options.RunID, "limit": 100}, http.StatusOK)
	if err != nil {
		return smokeReport{}, err
	}
	var catalogRecords catalog.CatalogSearchResponse
	if err := decodeSmokeResponse(catalogSearch, &catalogRecords); err != nil {
		return smokeReport{}, fmt.Errorf("governed Catalog search check: %w", err)
	}
	governedIdentifiers := make([]string, 0, len(catalogRecords.Items))
	for _, result := range catalogRecords.Items {
		if result.Type != catalog.SearchResultLocal || result.Local == nil {
			continue
		}
		governedIdentifiers = append(governedIdentifiers, result.Local.Resource.CanonicalIdentifier)
	}
	if catalogRecords.Profile != catalog.CoreSearchProfile || catalogRecords.Tier != "core" || catalogRecords.Generation == "" {
		return smokeReport{}, safeerr.New("governed Catalog search check: profile or generation metadata is incomplete")
	}
	if missing := missingSmokeIdentifiers(governedIdentifiers, identifiers); len(missing) > 0 {
		return smokeReport{}, fmt.Errorf("governed Catalog search check: missing identifiers %s", strings.Join(missing, ", "))
	}

	discoveryURL, err := url.Parse(options.BaseURL)
	if err != nil || discoveryURL.Hostname() == "" {
		return smokeReport{}, safeerr.New("build public discovery snapshot check: base URL has no discovery host")
	}
	publicationTarget := map[string]any{"host": strings.ToLower(discoveryURL.Hostname())}
	snapshotResponse, err := client.request(ctx, "build public discovery snapshot", http.MethodPost, "/api/v1/publication/snapshots", options.PublisherToken, options.RunID+"-snapshot", "", publicationTarget, http.StatusCreated)
	if err != nil {
		return smokeReport{}, err
	}
	var snapshot publication.Snapshot
	if err := decodeSmokeResponse(snapshotResponse, &snapshot); err != nil {
		return smokeReport{}, fmt.Errorf("build public discovery snapshot check: invalid response: %w", err)
	}
	if snapshot.ID == "" || snapshot.ETag == "" {
		return smokeReport{}, safeerr.New("build public discovery snapshot check: response ID or ETag is absent")
	}
	snapshotIdentifiers := make([]string, 0, len(snapshot.ARD.Entries))
	for _, entry := range snapshot.ARD.Entries {
		snapshotIdentifiers = append(snapshotIdentifiers, entry.Identifier)
	}
	if missing := missingSmokeIdentifiers(snapshotIdentifiers, identifiers); len(missing) > 0 {
		return smokeReport{}, fmt.Errorf("build public discovery snapshot check: missing identifiers %s", strings.Join(missing, ", "))
	}
	activationTarget := map[string]any{"host": publicationTarget["host"], "reason": "Activate the reviewed smoke-test snapshot"}
	if _, err := client.request(ctx, "activate public discovery snapshot", http.MethodPost, "/api/v1/publication/snapshots/"+url.PathEscape(snapshot.ID)+":activate", options.PublisherToken, options.RunID+"-activate-snapshot", snapshot.ETag, activationTarget, http.StatusOK); err != nil {
		return smokeReport{}, err
	}
	for _, resource := range created {
		if resource.DocumentID == "" {
			continue
		}
		publicDocument, err := client.request(ctx, "read active public "+string(resource.Kind)+" document", http.MethodGet, "/public/v1/artifacts/"+url.PathEscape(resource.DocumentID), "", "", "", nil, http.StatusOK)
		if err != nil {
			return smokeReport{}, err
		}
		if err := validateSmokeDocument(publicDocument, resource.DocumentID, resource.documentContent, true); err != nil {
			return smokeReport{}, fmt.Errorf("read active public %s document check: %w", resource.Kind, err)
		}
		conditionalDocument, err := client.requestWithHeaders(ctx, "conditionally read active public "+string(resource.Kind)+" document", http.MethodGet, "/public/v1/artifacts/"+url.PathEscape(resource.DocumentID), "", "", "", nil, map[string]string{"If-None-Match": `"` + resource.DocumentID + `"`}, http.StatusNotModified)
		if err != nil {
			return smokeReport{}, err
		}
		if conditionalDocument.Header.Get("ETag") != `"`+resource.DocumentID+`"` || conditionalDocument.Header.Get("Cache-Control") == "" {
			return smokeReport{}, fmt.Errorf("conditionally read active public %s document check: cache validators are incomplete", resource.Kind)
		}
	}

	ardResponse, err := client.request(ctx, "read active ARD manifest", http.MethodGet, "/.well-known/ard.json", "", "", "", nil, http.StatusOK)
	if err != nil {
		return smokeReport{}, err
	}
	if ardResponse.Header.Get("ETag") != snapshot.ETag || ardResponse.Header.Get("Content-Digest") == "" {
		return smokeReport{}, safeerr.New("read active ARD manifest check: immutable publication headers are incomplete")
	}
	var ard publication.ARDManifest
	if err := decodeSmokeResponse(ardResponse, &ard); err != nil {
		return smokeReport{}, fmt.Errorf("read active ARD manifest check: %w", err)
	}
	ardIdentifiers := make([]string, 0, len(ard.Entries))
	for _, entry := range ard.Entries {
		ardIdentifiers = append(ardIdentifiers, entry.Identifier)
	}
	if missing := missingSmokeIdentifiers(ardIdentifiers, identifiers); len(missing) > 0 {
		return smokeReport{}, fmt.Errorf("read active ARD manifest check: missing identifiers %s", strings.Join(missing, ", "))
	}
	if _, err := client.requestWithHeaders(ctx, "conditional ARD manifest read", http.MethodGet, "/.well-known/ard.json", "", "", "", nil, map[string]string{"If-None-Match": snapshot.ETag}, http.StatusNotModified); err != nil {
		return smokeReport{}, err
	}

	aiResponse, err := client.request(ctx, "read active AI Catalog", http.MethodGet, "/.well-known/ai-catalog.json", "", "", "", nil, http.StatusOK)
	if err != nil {
		return smokeReport{}, err
	}
	if aiResponse.Header.Get("ETag") == "" || aiResponse.Header.Get("ETag") == snapshot.ETag || aiResponse.Header.Get("Content-Digest") == "" {
		return smokeReport{}, safeerr.New("read active AI Catalog check: profile-specific immutable publication headers are incomplete")
	}
	var aiCatalog publication.AICatalog
	if err := decodeSmokeResponse(aiResponse, &aiCatalog); err != nil {
		return smokeReport{}, fmt.Errorf("read active AI Catalog check: %w", err)
	}
	aiIdentifiers := make([]string, 0, len(aiCatalog.Entries))
	for _, entry := range aiCatalog.Entries {
		aiIdentifiers = append(aiIdentifiers, entry.Identifier)
	}
	if missing := missingSmokeIdentifiers(aiIdentifiers, identifiers); len(missing) > 0 {
		return smokeReport{}, fmt.Errorf("read active AI Catalog check: missing identifiers %s", strings.Join(missing, ", "))
	}

	registrySearch, err := client.request(ctx, "search public ARD Registry", http.MethodPost, "/ard/v1/search", "", "", "", map[string]any{"query": map[string]any{"text": options.RunID}, "federation": "none", "pageSize": 100}, http.StatusOK)
	if err != nil {
		return smokeReport{}, err
	}
	var search publication.SearchResponse
	if err := decodeSmokeResponse(registrySearch, &search); err != nil {
		return smokeReport{}, fmt.Errorf("search public ARD Registry check: %w", err)
	}
	searchIdentifiers := make([]string, 0, len(search.Results))
	for _, result := range search.Results {
		searchIdentifiers = append(searchIdentifiers, result.Identifier)
	}
	if missing := missingSmokeIdentifiers(searchIdentifiers, identifiers); len(missing) > 0 {
		return smokeReport{}, fmt.Errorf("search public ARD Registry check: missing identifiers %s", strings.Join(missing, ", "))
	}

	exploreResponse, err := client.request(ctx, "explore public ARD Registry", http.MethodPost, "/ard/v1/explore", "", "", "", map[string]any{"query": map[string]any{"text": options.RunID}, "resultType": map[string]any{"facets": []map[string]any{{"field": "type"}, {"field": "publisher"}}}}, http.StatusOK)
	if err != nil {
		return smokeReport{}, err
	}
	var explore publication.ExploreResponse
	if err := decodeSmokeResponse(exploreResponse, &explore); err != nil {
		return smokeReport{}, fmt.Errorf("explore public ARD Registry check: invalid facet response: %w", err)
	}
	if len(explore.Facets) != 2 {
		return smokeReport{}, safeerr.New("explore public ARD Registry check: expected type and publisher facets")
	}

	listResponse, err := client.request(ctx, "list public ARD Registry", http.MethodGet, "/ard/v1/agents?pageSize=100&orderBy=displayName", "", "", "", nil, http.StatusOK)
	if err != nil {
		return smokeReport{}, err
	}
	var list publication.ListResponse
	if err := decodeSmokeResponse(listResponse, &list); err != nil {
		return smokeReport{}, fmt.Errorf("list public ARD Registry check: %w", err)
	}
	listIdentifiers := make([]string, 0, len(list.Items))
	for _, entry := range list.Items {
		listIdentifiers = append(listIdentifiers, entry.Identifier)
	}
	if missing := missingSmokeIdentifiers(listIdentifiers, identifiers); len(missing) > 0 {
		return smokeReport{}, fmt.Errorf("list public ARD Registry check: missing identifiers %s", strings.Join(missing, ", "))
	}

	if _, err := client.request(ctx, "reject removal of approved publisher domain", http.MethodPatch, "/api/v1/publishers/"+url.PathEscape(publisher.ID), options.AdminToken, options.RunID+"-publisher-remove-approved", approvedPublisher.Header.Get("ETag"), map[string]any{"domains": []string{"replacement." + domain}, "reason": "Prove an approved domain cannot be removed"}, http.StatusConflict); err != nil {
		return smokeReport{}, err
	}
	if err := smokeExerciseDraftDeletion(ctx, client, options, namespace.ID); err != nil {
		return smokeReport{}, err
	}

	return smokeReport{
		Status: "passed", BaseURL: baseURL, RunID: options.RunID, Checks: append([]string(nil), client.checks...),
		Resources: created, SnapshotID: snapshot.ID, SnapshotETag: snapshot.ETag,
		ARDEntries: len(ard.Entries), AICatalogEntries: len(aiCatalog.Entries),
	}, nil
}

// validateSmokeCapabilities checks the response subset required by the smoke
// workflow, allowing additional capabilities and any array ordering.
func validateSmokeCapabilities(response smokeHTTPResponse) error {
	var capabilityDocument struct {
		ResourceKinds []catalog.Kind `json:"resourceKinds"`
		Operations    []string       `json:"operations"`
		Governance    struct {
			PublicationAuthorization string                   `json:"publicationAuthorization"`
			PublisherAuthority       catalog.TrustState       `json:"publisherAuthority"`
			LegalRights              catalog.LegalRightsState `json:"legalRights"`
		} `json:"governance"`
		Search struct {
			Tier     string `json:"tier"`
			Profile  string `json:"profile"`
			Semantic string `json:"semantic"`
			Degraded bool   `json:"degraded"`
		} `json:"search"`
		Limits struct {
			CatalogDocumentBytes       int64 `json:"catalogDocumentBytes"`
			PublicationRecords         int   `json:"publicationRecords"`
			PublicationProjectionBytes int   `json:"publicationProjectionBytes"`
			PublicationDocumentBytes   int   `json:"publicationDocumentBytes"`
			PublicationSnapshotBytes   int   `json:"publicationSnapshotBytes"`
			PublicationWorkUnits       int   `json:"publicationWorkUnits"`
			PublicationConstructions   int   `json:"publicationConcurrentConstructions"`
		} `json:"limits"`
	}
	if err := decodeSmokeResponse(response, &capabilityDocument); err != nil {
		return fmt.Errorf("authenticated capabilities check: %w", err)
	}
	if capabilityDocument.Governance.PublicationAuthorization != "administrator_approved" || capabilityDocument.Governance.PublisherAuthority != catalog.TrustNotAssessed || capabilityDocument.Governance.LegalRights != catalog.LegalRightsNotAssessed {
		return fmt.Errorf("authenticated capabilities check: unexpected governance state %#v", capabilityDocument.Governance)
	}
	if !validSmokeSearchState(capabilityDocument.Search.Tier, capabilityDocument.Search.Profile, capabilityDocument.Search.Semantic, capabilityDocument.Search.Degraded) {
		return fmt.Errorf("authenticated capabilities check: unexpected search state %#v", capabilityDocument.Search)
	}
	if capabilityDocument.Limits.CatalogDocumentBytes != catalog.MaxCatalogDocumentBytes {
		return fmt.Errorf("authenticated capabilities check: bounded document limit is %d, expected %d", capabilityDocument.Limits.CatalogDocumentBytes, catalog.MaxCatalogDocumentBytes)
	}
	if capabilityDocument.Limits.PublicationRecords != catalog.MaxPublicationRecords || capabilityDocument.Limits.PublicationProjectionBytes != catalog.MaxPublicationProjectionBytes || capabilityDocument.Limits.PublicationDocumentBytes != publication.MaxPublicationDocumentBytes || capabilityDocument.Limits.PublicationSnapshotBytes != publication.MaxPublicationSnapshotBytes || capabilityDocument.Limits.PublicationWorkUnits != publication.MaxPublicationWorkUnits || capabilityDocument.Limits.PublicationConstructions != publication.MaxConcurrentConstructions {
		return fmt.Errorf("authenticated capabilities check: publication work limits are inconsistent: %#v", capabilityDocument.Limits)
	}
	for _, kind := range []catalog.Kind{catalog.KindAgent, catalog.KindMCPServer, catalog.KindSkill} {
		if !slices.Contains(capabilityDocument.ResourceKinds, kind) {
			return fmt.Errorf("authenticated capabilities check: resource kind %q is absent", kind)
		}
	}
	for _, operation := range []string{"catalog.lifecycle", "catalog.search", "publication.ard", "publication.ai_catalog", "registry.search", "registry.explore", "registry.list"} {
		if !slices.Contains(capabilityDocument.Operations, operation) {
			return fmt.Errorf("authenticated capabilities check: operation %q is absent", operation)
		}
	}
	return nil
}

// validSmokeSearchState accepts optional degraded inference without weakening
// the core smoke workflow or accepting an unknown capability combination.
func validSmokeSearchState(tier, profile, semantic string, degraded bool) bool {
	if semantic == "disabled" {
		return tier == "core" && profile == catalog.CoreSearchProfile && !degraded
	}
	if semantic != "enabled" || tier != "hybrid" || !strings.HasPrefix(profile, "sha256:") {
		return false
	}
	digest, err := hex.DecodeString(strings.TrimPrefix(profile, "sha256:"))
	return err == nil && len(digest) == sha256.Size
}

// smokeExerciseMembershipAdministration creates a disposable reader membership and verifies paging, lifecycle revisions and cross-tenant denial.
func smokeExerciseMembershipAdministration(ctx context.Context, client *smokeHTTPClient, options smokeOptions) error {
	subject := options.RunID + "-temporary-reader"
	assignmentResponse, err := client.request(ctx, "grant disposable membership role", http.MethodPost, "/api/v1/role-assignments", options.AdminToken, options.RunID+"-membership-grant", "", map[string]any{"subject": subject, "actorType": identity.ActorHuman, "role": identity.RoleReader, "reason": "Exercise membership administration"}, http.StatusCreated)
	if err != nil {
		return err
	}
	var assignment identity.RoleAssignment
	if err := decodeSmokeResponse(assignmentResponse, &assignment); err != nil {
		return fmt.Errorf("grant disposable membership role check: invalid response: %w", err)
	}
	if assignment.MembershipID == "" || assignment.Subject != subject || assignment.Revision != 1 {
		return fmt.Errorf("grant disposable membership role check: unexpected assignment %#v", assignment)
	}

	firstPageResponse, err := client.request(ctx, "list bounded memberships", http.MethodGet, "/api/v1/memberships?limit=1", options.AdminToken, "", "", nil, http.StatusOK)
	if err != nil {
		return err
	}
	var firstPage identity.MembershipPage
	if err := decodeSmokeResponse(firstPageResponse, &firstPage); err != nil {
		return fmt.Errorf("list bounded memberships check: invalid response: %w", err)
	}
	if len(firstPage.Items) != 1 || firstPage.NextCursor == "" {
		return fmt.Errorf("list bounded memberships check: unexpected page %#v", firstPage)
	}
	secondPageResponse, err := client.request(ctx, "continue bounded memberships", http.MethodGet, "/api/v1/memberships?limit=1&cursor="+url.QueryEscape(firstPage.NextCursor), options.AdminToken, "", "", nil, http.StatusOK)
	if err != nil {
		return err
	}
	var secondPage identity.MembershipPage
	if err := decodeSmokeResponse(secondPageResponse, &secondPage); err != nil {
		return fmt.Errorf("continue bounded memberships check: invalid response: %w", err)
	}
	if len(secondPage.Items) != 1 || secondPage.Items[0].ID == firstPage.Items[0].ID {
		return fmt.Errorf("continue bounded memberships check: unexpected page %#v", secondPage)
	}

	membershipPath := "/api/v1/memberships/" + url.PathEscape(assignment.MembershipID)
	membershipResponse, err := client.request(ctx, "read exact membership", http.MethodGet, membershipPath, options.AdminToken, "", "", nil, http.StatusOK)
	if err != nil {
		return err
	}
	var membership identity.Membership
	if err := decodeSmokeResponse(membershipResponse, &membership); err != nil {
		return fmt.Errorf("read exact membership check: invalid response: %w", err)
	}
	if membership.ID != assignment.MembershipID || membership.Subject != subject || membershipResponse.Header.Get("ETag") == "" {
		return fmt.Errorf("read exact membership check: unexpected membership %#v", membership)
	}
	filteredResponse, err := client.request(ctx, "filter the exact disposable membership", http.MethodGet, "/api/v1/memberships?limit=1&subject="+url.QueryEscape(subject)+"&state="+url.QueryEscape(string(membership.State))+"&actorType=human&realmId="+url.QueryEscape(membership.RealmID), options.AdminToken, "", "", nil, http.StatusOK)
	if err != nil {
		return err
	}
	var filtered identity.MembershipPage
	if err := decodeSmokeResponse(filteredResponse, &filtered); err != nil {
		return fmt.Errorf("filter exact membership check: invalid response: %w", err)
	}
	if len(filtered.Items) != 1 || filtered.Items[0].ID != membership.ID || filtered.NextCursor != "" {
		return errors.New("filtered membership does not match the selected exact identity")
	}
	contributionsResponse, err := client.request(ctx, "inspect disposable membership contributions", http.MethodGet, membershipPath+"/access", options.AdminToken, "", "", nil, http.StatusOK)
	if err != nil {
		return err
	}
	var contributions identity.MemberAccessPage
	if err := decodeSmokeResponse(contributionsResponse, &contributions); err != nil {
		return fmt.Errorf("membership contributions check: invalid response: %w", err)
	}
	if contributions.Membership.ID != membership.ID || contributions.GroupCoverage != "current_realm" || len(contributions.Items) != 1 || contributions.NextCursor != "" || contributions.Items[0].Effective != (membership.State == identity.MembershipActive && contributions.Items[0].Assignment.State == identity.AssignmentActive) || contributions.Items[0].Assignment.ID != assignment.ID || contributions.Items[0].Assignment.MembershipID != membership.ID || contributions.Items[0].Assignment.GroupID != "" || contributions.Items[0].Assignment.NamespaceID != "" || contributions.Items[0].Role.Key != "reader" {
		return errors.New("membership contribution projection does not match its direct grant")
	}
	if _, err := client.request(ctx, "deny cross-tenant membership contributions", http.MethodGet, membershipPath+"/access", options.OtherTenantToken, "", "", nil, http.StatusNotFound); err != nil {
		return err
	}

	transitions := []struct {
		name     string
		suffix   string
		key      string
		ifMatch  string
		state    identity.MembershipState
		revision int64
		revoked  int
	}{
		{name: "suspend disposable membership", suffix: ":suspend", key: "membership-suspend", ifMatch: `"rev-1"`, state: identity.MembershipSuspended, revision: 2},
		{name: "reactivate disposable membership", suffix: ":reactivate", key: "membership-reactivate", ifMatch: `"rev-2"`, state: identity.MembershipActive, revision: 3},
		{name: "offboard disposable membership", suffix: ":offboard", key: "membership-offboard", ifMatch: `"rev-3"`, state: identity.MembershipOffboarded, revision: 4, revoked: 1},
	}
	for _, transition := range transitions {
		response, requestErr := client.request(ctx, transition.name, http.MethodPost, membershipPath+transition.suffix, options.AdminToken, options.RunID+"-"+transition.key, transition.ifMatch, map[string]any{"reason": "Exercise " + transition.key}, http.StatusOK)
		if requestErr != nil {
			return requestErr
		}
		var result identity.MembershipLifecycleResult
		if err := decodeSmokeResponse(response, &result); err != nil {
			return fmt.Errorf("%s check: invalid response: %w", transition.name, err)
		}
		if result.Membership.State != transition.state || result.Membership.Revision != transition.revision || result.RevokedRoleAssignments != transition.revoked || response.Header.Get("ETag") != fmt.Sprintf(`"rev-%d"`, transition.revision) {
			return fmt.Errorf("%s check: unexpected result %#v", transition.name, result)
		}
	}

	finalResponse, err := client.request(ctx, "read offboarded membership", http.MethodGet, membershipPath, options.AdminToken, "", "", nil, http.StatusOK)
	if err != nil {
		return err
	}
	if err := decodeSmokeResponse(finalResponse, &membership); err != nil {
		return fmt.Errorf("read offboarded membership check: invalid response: %w", err)
	}
	if membership.State != identity.MembershipOffboarded || membership.Revision != 4 {
		return fmt.Errorf("read offboarded membership check: unexpected membership %#v", membership)
	}
	if _, err := client.request(ctx, "deny cross-tenant membership read", http.MethodGet, membershipPath, options.OtherTenantToken, "", "", nil, http.StatusNotFound); err != nil {
		return err
	}
	return nil
}

// smokeExerciseDraftDeletion deletes an unpublished fixture and verifies hidden descendants plus reserved identifier reuse.
func smokeExerciseDraftDeletion(ctx context.Context, client *smokeHTTPClient, options smokeOptions, namespaceID string) error {
	identifier := "urn:example:open-agentic-catalog:" + options.RunID + ":discarded-skill"
	resourceResponse, err := client.request(ctx, "create disposable draft Resource", http.MethodPost, "/api/v1/resources", options.ContributorToken, options.RunID+"-draft-resource", "", map[string]any{"namespaceId": namespaceID, "canonicalIdentifier": identifier, "kind": catalog.KindSkill}, http.StatusCreated)
	if err != nil {
		return err
	}
	var resource catalog.Resource
	if err := decodeSmokeResponse(resourceResponse, &resource); err != nil {
		return fmt.Errorf("create disposable draft Resource check: invalid response: %w", err)
	}
	if resource.ID == "" || resourceResponse.Header.Get("ETag") != `"rev-1"` {
		return fmt.Errorf("create disposable draft Resource check: unexpected Resource %#v", resource)
	}
	representation := map[string]any{"role": "primary", "mediaType": adapter.MediaSkillMarkdown, "content": "---\nname: discarded-" + options.RunID + "\ndescription: Disposable draft used to verify safe deletion.\nlicense: Apache-2.0\n---\n\n# Instructions\n\nThis unique draft belongs to smoke run " + options.RunID + " and must remain inert.\n"}
	versionResponse, err := client.request(ctx, "create disposable draft version", http.MethodPost, "/api/v1/resources/"+url.PathEscape(resource.ID)+"/versions", options.ContributorToken, options.RunID+"-draft-version", "", map[string]any{"version": "1.0.0", "representations": []map[string]any{representation}}, http.StatusCreated)
	if err != nil {
		return err
	}
	var envelope struct {
		Revision catalog.VersionRevision `json:"revision"`
	}
	if err := decodeSmokeResponse(versionResponse, &envelope); err != nil {
		return fmt.Errorf("create disposable draft version check: invalid response: %w", err)
	}
	if envelope.Revision.ID == "" || envelope.Revision.ResourceVersionID == "" || len(envelope.Revision.Representations) != 1 || envelope.Revision.Representations[0].ArtifactID == "" {
		return fmt.Errorf("create disposable draft version check: unexpected revision %#v", envelope.Revision)
	}
	documentID := envelope.Revision.Representations[0].ArtifactID
	resourcePath := "/api/v1/resources/" + url.PathEscape(resource.ID)
	if _, err := client.request(ctx, "delete disposable unpublished draft", http.MethodPost, resourcePath+":delete", options.ContributorToken, options.RunID+"-draft-delete", resourceResponse.Header.Get("ETag"), map[string]any{"reason": "Discard unused local draft"}, http.StatusNoContent); err != nil {
		return err
	}
	for _, check := range []struct {
		name string
		path string
	}{
		{name: "hide deleted draft Resource", path: resourcePath},
		{name: "hide deleted draft version", path: "/api/v1/resource-versions/" + url.PathEscape(envelope.Revision.ResourceVersionID)},
		{name: "hide deleted draft revision", path: "/api/v1/version-revisions/" + url.PathEscape(envelope.Revision.ID)},
		{name: "hide deleted draft document", path: "/api/v1/artifacts/" + url.PathEscape(documentID) + "/content"},
	} {
		if _, err := client.request(ctx, check.name, http.MethodGet, check.path, options.ReaderToken, "", "", nil, http.StatusNotFound); err != nil {
			return err
		}
	}
	if _, err := client.request(ctx, "reserve deleted draft canonical identifier", http.MethodPost, "/api/v1/resources", options.ContributorToken, options.RunID+"-draft-reuse", "", map[string]any{"namespaceId": namespaceID, "canonicalIdentifier": identifier, "kind": catalog.KindSkill}, http.StatusConflict); err != nil {
		return err
	}
	return nil
}

// smokePublishFixture drives one resource through draft, validation, review,
// approval, and release so every supported kind reaches the public snapshot.
func smokePublishFixture(ctx context.Context, client *smokeHTTPClient, options smokeOptions, namespaceID, publisherID, domain string, fixture smokeFixture) (smokeResource, error) {
	prefix := options.RunID + "-" + fixture.Segment
	identifier := "urn:air:" + domain + ":readiness:" + fixture.Segment
	authorToken := options.ContributorToken
	if fixture.Kind == catalog.KindAgent {
		authorToken = options.AdminToken
	}
	resourceResponse, err := client.request(ctx, "create "+string(fixture.Kind)+" Resource", http.MethodPost, "/api/v1/resources", authorToken, prefix+"-resource", "", map[string]any{"namespaceId": namespaceID, "canonicalIdentifier": identifier, "kind": fixture.Kind}, http.StatusCreated)
	if err != nil {
		return smokeResource{}, err
	}
	var resource catalog.Resource
	if err := decodeSmokeResponse(resourceResponse, &resource); err != nil {
		return smokeResource{}, fmt.Errorf("create %s Resource check: invalid response: %w", fixture.Kind, err)
	}
	if resource.ID == "" {
		return smokeResource{}, fmt.Errorf("create %s Resource check: response ID is absent", fixture.Kind)
	}
	replay, err := client.request(ctx, "replay "+string(fixture.Kind)+" Resource command", http.MethodPost, "/api/v1/resources", authorToken, prefix+"-resource", "", map[string]any{"namespaceId": namespaceID, "canonicalIdentifier": identifier, "kind": fixture.Kind}, http.StatusCreated)
	if err != nil {
		return smokeResource{}, err
	}
	if replay.Header.Get("Idempotency-Replayed") != "true" {
		return smokeResource{}, fmt.Errorf("replay %s Resource command check: replay header is absent", fixture.Kind)
	}
	if _, err := client.request(ctx, "deny cross-tenant "+string(fixture.Kind)+" read", http.MethodGet, "/api/v1/resources/"+url.PathEscape(resource.ID), options.OtherTenantToken, "", "", nil, http.StatusNotFound); err != nil {
		return smokeResource{}, err
	}

	versionResponse, err := client.request(ctx, "create "+string(fixture.Kind)+" version", http.MethodPost, "/api/v1/resources/"+url.PathEscape(resource.ID)+"/versions", authorToken, prefix+"-version", "", map[string]any{"version": "1.0.0", "representations": []map[string]any{fixture.Representation}}, http.StatusCreated)
	if err != nil {
		return smokeResource{}, err
	}
	var versionEnvelope struct {
		Revision catalog.VersionRevision `json:"revision"`
		Evidence catalog.EvidenceSet     `json:"evidence"`
	}
	if err := decodeSmokeResponse(versionResponse, &versionEnvelope); err != nil {
		return smokeResource{}, fmt.Errorf("create %s version check: invalid response: %w", fixture.Kind, err)
	}
	if versionEnvelope.Revision.ID == "" || versionEnvelope.Evidence.ValidationProfile != "catalog-validation-v7" || versionEnvelope.Evidence.LegalRights.State != catalog.LegalRightsNotAssessed || versionEnvelope.Evidence.Trust.State != catalog.TrustNotAssessed {
		return smokeResource{}, fmt.Errorf("create %s version check: unexpected revision or evidence %#v", fixture.Kind, versionEnvelope)
	}
	initialEvidenceID := versionEnvelope.Evidence.ID
	contentSetDigest := versionEnvelope.Revision.ContentSetDigest
	validationResponse, err := client.request(ctx, "revalidate "+string(fixture.Kind)+" version", http.MethodPost, "/api/v1/version-revisions/"+url.PathEscape(versionEnvelope.Revision.ID)+":validate", authorToken, prefix+"-validation", "", map[string]any{}, http.StatusCreated)
	if err != nil {
		return smokeResource{}, err
	}
	if err := decodeSmokeResponse(validationResponse, &versionEnvelope); err != nil {
		return smokeResource{}, fmt.Errorf("revalidate %s version check: invalid response: %w", fixture.Kind, err)
	}
	if versionEnvelope.Revision.ContentSetDigest != contentSetDigest || versionEnvelope.Revision.EvidenceSetID != versionEnvelope.Evidence.ID || versionEnvelope.Evidence.ID == initialEvidenceID || versionEnvelope.Evidence.State != catalog.EvidenceComplete || versionEnvelope.Evidence.ValidationProfile != "catalog-validation-v7" || !versionEnvelope.Evidence.ExpiresAt.IsZero() {
		return smokeResource{}, fmt.Errorf("revalidate %s version check: unexpected revision or evidence %#v", fixture.Kind, versionEnvelope)
	}
	var documentID string
	var documentContent []byte
	if content, ok := fixture.Representation["content"].(string); ok {
		if len(versionEnvelope.Revision.Representations) != 1 || versionEnvelope.Revision.Representations[0].ArtifactID == "" {
			return smokeResource{}, fmt.Errorf("create %s version check: bounded document identifier is absent", fixture.Kind)
		}
		documentID = versionEnvelope.Revision.Representations[0].ArtifactID
		documentContent = []byte(content)
		authenticatedDocument, err := client.request(ctx, "read authenticated "+string(fixture.Kind)+" document", http.MethodGet, "/api/v1/artifacts/"+url.PathEscape(documentID)+"/content", options.ReaderToken, "", "", nil, http.StatusOK)
		if err != nil {
			return smokeResource{}, err
		}
		if err := validateSmokeDocument(authenticatedDocument, documentID, documentContent, false); err != nil {
			return smokeResource{}, fmt.Errorf("read authenticated %s document check: %w", fixture.Kind, err)
		}
	}

	listingResponse, err := client.request(ctx, "create "+string(fixture.Kind)+" listing", http.MethodPost, "/api/v1/catalog-listings", authorToken, prefix+"-listing", "", map[string]any{"namespaceId": namespaceID, "resourceId": resource.ID}, http.StatusCreated)
	if err != nil {
		return smokeResource{}, err
	}
	var listing catalog.CatalogListing
	if err := decodeSmokeResponse(listingResponse, &listing); err != nil {
		return smokeResource{}, fmt.Errorf("create %s listing check: invalid response: %w", fixture.Kind, err)
	}
	if listing.ID == "" {
		return smokeResource{}, fmt.Errorf("create %s listing check: response ID is absent", fixture.Kind)
	}

	listingRevisionResponse, err := client.request(ctx, "create "+string(fixture.Kind)+" listing revision", http.MethodPost, "/api/v1/catalog-listings/"+url.PathEscape(listing.ID)+"/revisions", authorToken, prefix+"-listing-revision", listingResponse.Header.Get("ETag"), map[string]any{
		"versionRevisionId":     versionEnvelope.Revision.ID,
		"displayName":           fixture.Name + " " + options.RunID,
		"description":           fixture.Description,
		"tags":                  []string{"catalog-readiness", options.RunID, string(fixture.Kind)},
		"categories":            []string{"developer tools"},
		"representativeQueries": []string{"Find the " + fixture.Name + " for " + options.RunID, "Use " + options.RunID + " to validate Catalog discovery"},
		"publisherId":           publisherID,
		"license":               "Apache-2.0",
		"visibility":            "public",
	}, http.StatusCreated)
	if err != nil {
		return smokeResource{}, err
	}
	if listingRevisionResponse.Header.Get("ETag") == "" {
		return smokeResource{}, fmt.Errorf("create %s listing revision check: ETag is absent", fixture.Kind)
	}

	submissionResponse, err := client.request(ctx, "submit "+string(fixture.Kind)+" listing", http.MethodPost, "/api/v1/catalog-listings/"+url.PathEscape(listing.ID)+"/submissions", authorToken, prefix+"-submission", "", map[string]any{}, http.StatusCreated)
	if err != nil {
		return smokeResource{}, err
	}
	var submission catalog.ReviewSubmission
	if err := decodeSmokeResponse(submissionResponse, &submission); err != nil {
		return smokeResource{}, fmt.Errorf("submit %s listing check: invalid response: %w", fixture.Kind, err)
	}
	if submission.ID == "" || submissionResponse.Header.Get("ETag") == "" {
		return smokeResource{}, fmt.Errorf("submit %s listing check: response ID or ETag is absent", fixture.Kind)
	}
	selfApprovalStatus := http.StatusForbidden
	if fixture.Kind == catalog.KindAgent {
		selfApprovalStatus = http.StatusConflict
	}
	selfApproval, err := client.request(ctx, "reject submitter self-approval for "+string(fixture.Kind), http.MethodPost, "/api/v1/review-submissions/"+url.PathEscape(submission.ID)+":approve", authorToken, prefix+"-self-approval", submissionResponse.Header.Get("ETag"), map[string]any{"reason": "This actor must not approve its own submission"}, selfApprovalStatus)
	if err != nil {
		return smokeResource{}, err
	}
	if fixture.Kind == catalog.KindAgent && !bytes.Contains(selfApproval.Body, []byte("SEPARATION_OF_DUTIES")) {
		return smokeResource{}, safeerr.New("reject submitter self-approval for agent check: separation-of-duties evidence is absent")
	}
	if _, err := client.request(ctx, "approve "+string(fixture.Kind)+" submission independently", http.MethodPost, "/api/v1/review-submissions/"+url.PathEscape(submission.ID)+":approve", options.ReviewerToken, prefix+"-approval", submissionResponse.Header.Get("ETag"), map[string]any{"reason": "Independent Catalog readiness review"}, http.StatusOK); err != nil {
		return smokeResource{}, err
	}

	publishResponse, err := client.request(ctx, "publish "+string(fixture.Kind)+" listing", http.MethodPost, "/api/v1/catalog-listings/"+url.PathEscape(listing.ID)+":publish", options.PublisherToken, prefix+"-publish", listingRevisionResponse.Header.Get("ETag"), map[string]any{"submissionId": submission.ID}, http.StatusCreated)
	if err != nil {
		return smokeResource{}, err
	}
	var publishEnvelope struct {
		Release catalog.ListingRelease `json:"release"`
	}
	if err := decodeSmokeResponse(publishResponse, &publishEnvelope); err != nil {
		return smokeResource{}, fmt.Errorf("publish %s listing check: invalid response: %w", fixture.Kind, err)
	}
	if publishEnvelope.Release.ID == "" {
		return smokeResource{}, fmt.Errorf("publish %s listing check: release ID is absent", fixture.Kind)
	}
	return smokeResource{Kind: fixture.Kind, Identifier: identifier, ResourceID: resource.ID, ListingID: listing.ID, ReleaseID: publishEnvelope.Release.ID, DocumentID: documentID, documentContent: documentContent}, nil
}

// validateSmokeDocument checks exact retained bytes, digest/attachment headers and public cache validators when applicable.
func validateSmokeDocument(response smokeHTTPResponse, documentID string, expected []byte, public bool) error {
	digest := sha256.Sum256(response.Body)
	if !bytes.Equal(response.Body, expected) || hex.EncodeToString(digest[:]) != documentID {
		return safeerr.New("response bytes do not match the retained content identifier")
	}
	expectedDigest := "sha-256=:" + base64.StdEncoding.EncodeToString(digest[:]) + ":"
	if response.Header.Get("Content-Length") != strconv.Itoa(len(expected)) || response.Header.Get("Content-Digest") != expectedDigest || response.Header.Get("Content-Type") == "" || !strings.Contains(response.Header.Get("Content-Disposition"), `filename="`+documentID+`"`) {
		return safeerr.New("exact content headers are incomplete")
	}
	if public && (response.Header.Get("ETag") != `"`+documentID+`"` || response.Header.Get("Cache-Control") == "") {
		return safeerr.New("public cache validators are incomplete")
	}
	return nil
}

// smokeFixtures returns inert Agent, MCP Server Card and Skill metadata with run-specific identifiers.
func smokeFixtures(runID string) []smokeFixture {
	// Reuse native inputs while preserving each smoke run's independent identities and assertions.
	result := make([]smokeFixture, 0, 3)
	for _, fixture := range demoFixtures() {
		if fixture.state != "published" {
			continue
		}
		fixture.Segment += "-" + runID
		result = append(result, fixture.smokeFixture)
	}
	return result
}

// request uses the shared bounded exchange without additional conditional-response test headers.
func (c *smokeHTTPClient) request(ctx context.Context, name, method, path, token, idempotencyKey, ifMatch string, body any, status int) (smokeHTTPResponse, error) {
	return c.requestWithHeaders(ctx, name, method, path, token, idempotencyKey, ifMatch, body, nil, status)
}

// requestWithHeaders sends one explicit workflow request, bounds its response and records only the expected status as passed.
func (c *smokeHTTPClient) requestWithHeaders(ctx context.Context, name, method, path, token, idempotencyKey, ifMatch string, body any, headers map[string]string, status int) (smokeHTTPResponse, error) {
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			return smokeHTTPResponse{}, fmt.Errorf("%s check: encode request: %w", name, err)
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(raw)) // #nosec G704 -- workflow callers validate an explicitly selected origin; demo requires literal loopback, paths are code-owned routes/verified IDs, redirects/proxies are refused.
	if err != nil {
		return smokeHTTPResponse{}, fmt.Errorf("%s check: create request: %w", name, err)
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	if ifMatch != "" {
		request.Header.Set("If-Match", ifMatch)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := c.client.Do(request) // #nosec G704 -- request remains on the caller-validated explicit workflow origin; demo transport refuses redirects and environment proxies.
	if err != nil {
		if c.safeErrors {
			return smokeHTTPResponse{}, fmt.Errorf("%s: request failed or deadline exceeded", name)
		}
		return smokeHTTPResponse{}, fmt.Errorf("%s check: %w", name, err)
	}
	defer func() { _ = response.Body.Close() }()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, smokeMaximumResponseBytes+1))
	if err != nil {
		return smokeHTTPResponse{}, fmt.Errorf("%s check: read response: %w", name, err)
	}
	if len(responseBody) > smokeMaximumResponseBytes {
		return smokeHTTPResponse{}, fmt.Errorf("%s check: response exceeds %d bytes", name, smokeMaximumResponseBytes)
	}
	if response.StatusCode != status {
		if c.safeErrors {
			return smokeHTTPResponse{}, fmt.Errorf("%s: HTTP %d, expected %d", name, response.StatusCode, status)
		}
		detail := strings.Join(strings.Fields(string(responseBody)), " ")
		if len(detail) > 800 {
			detail = detail[:800] + "..."
		}
		return smokeHTTPResponse{}, fmt.Errorf("%s check: %s %s returned %d, expected %d: %s", name, method, path, response.StatusCode, status, detail)
	}
	c.checks = append(c.checks, name)
	return smokeHTTPResponse{Header: response.Header.Clone(), Body: responseBody}, nil
}

// decodeSmokeResponse requires one nonempty JSON value and rejects any trailing response content.
func decodeSmokeResponse(response smokeHTTPResponse, target any) error {
	if len(response.Body) == 0 {
		return safeerr.New("response body is empty")
	}
	decoder := json.NewDecoder(bytes.NewReader(response.Body))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode JSON response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return safeerr.New("response contains trailing JSON")
	}
	return nil
}

// validateSmokeBaseURL normalizes an origin and requires explicit opt-in plus HTTPS for non-loopback mutation targets.
func validateSmokeBaseURL(value string, allowRemote bool) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", safeerr.New("smoke base URL must be an absolute HTTP or HTTPS origin")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", safeerr.New("smoke base URL must use HTTP or HTTPS")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" || parsed.Path != "" && parsed.Path != "/" {
		return "", safeerr.New("smoke base URL must be an origin without credentials, path, query, or fragment")
	}
	hostname := strings.ToLower(parsed.Hostname())
	loopback := hostname == "localhost"
	if address := net.ParseIP(hostname); address != nil {
		loopback = address.IsLoopback()
	}
	if !loopback && !allowRemote {
		return "", safeerr.New("smoke creates governed records; use --allow-remote to target a non-loopback service")
	}
	if !loopback && parsed.Scheme != "https" {
		return "", safeerr.New("remote smoke targets must use HTTPS")
	}
	return strings.TrimSuffix(parsed.String(), "/"), nil
}

// newSmokeRunID allocates a short random suffix for disposable walkthrough record and command identities.
func newSmokeRunID() (string, error) {
	var value [5]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("create smoke run ID: %w", err)
	}
	return "smoke-" + hex.EncodeToString(value[:]), nil
}

// missingSmokeIdentifiers reports expected fixture identifiers absent from discovery results in expected order.
func missingSmokeIdentifiers(actual, expected []string) []string {
	missing := make([]string, 0)
	for _, identifier := range expected {
		if !slices.Contains(actual, identifier) {
			missing = append(missing, identifier)
		}
	}
	return missing
}

// environmentOrDefault uses a nonempty trimmed workflow override or the caller's explicit development fallback.
func environmentOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
