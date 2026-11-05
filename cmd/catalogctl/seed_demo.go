// Demo seeding loads reviewed local samples through governed APIs and verifies
// receipt-owned records before extension or read-only reruns.

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/artwork"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/demodata"
	diagnostic "github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/diagnostics"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

const demoID = "catalog-demo-v1"

var demoDomain = demodata.DefaultPlan("expanded", "").Publishers[0].Domains[0]

const demoTenant = "00000000-0000-7000-8000-000000000001"
const demoReceiptLimit = 128 << 10

type demoItem struct {
	Key               string       `json:"key"`
	Kind              catalog.Kind `json:"kind"`
	State             string       `json:"state"`
	Identifier        string       `json:"identifier"`
	ResourceID        string       `json:"resourceId"`
	VersionID         string       `json:"versionId"`
	VersionRevisionID string       `json:"versionRevisionId"`
	ListingID         string       `json:"listingId"`
	ListingRevisionID string       `json:"listingRevisionId"`
	SubmissionID      string       `json:"submissionId,omitempty"`
	ReleaseID         string       `json:"releaseId,omitempty"`
}

// demoReceipt contains lookup hints only. No saved content digest or status is
// trusted in place of comparing current API records with the built-in dataset.
type demoReceipt struct {
	Bindings          *demoBindings `json:"bindings,omitempty"`
	Dataset           string        `json:"dataset"`
	BaseURL           string        `json:"baseUrl"`
	TenantID          string        `json:"tenantId"`
	NamespaceID       string        `json:"namespaceId"`
	PublisherID       string        `json:"publisherId"`
	SnapshotID        string        `json:"snapshotId"`
	PluginNamespaceID string        `json:"pluginNamespaceId,omitempty"`
	ArchiveOrigin     string        `json:"archiveOrigin,omitempty"`
	Items             []demoItem    `json:"items"`
}

type demoReport struct {
	demoReceipt
	Status        string `json:"status"`
	Published     int    `json:"published"`
	Drafts        int    `json:"drafts"`
	PendingReview int    `json:"pendingReview"`
}

type demoFixture struct {
	smokeFixture
	state      string
	expanded   bool
	identifier string
	version    string
	listing    demodata.Listing
	namespace  string
	publisher  string
	bodyDigest string
}

// demoFixtures builds the fixed three-published, one-draft and one-pending-review synthetic dataset.
func demoFixtures() []demoFixture { return defaultDemoFixtures("minimal", "") }

func defaultDemoFixtures(profile, origin string) []demoFixture {
	plan := demodata.DefaultPlan(profile, origin)
	fixtures := make([]demoFixture, 0, len(plan.Resources))
	minimalKeys := map[string]bool{}
	for _, item := range demodata.DefaultPlan("minimal", "").Resources {
		minimalKeys[item.Resource.Key] = true
	}
	for _, item := range plan.Resources {
		raw, err := json.Marshal(item.Representation)
		if err != nil {
			return nil
		}
		var representation map[string]any
		if json.Unmarshal(raw, &representation) != nil {
			return nil
		}
		r := item.Resource
		fixtures = append(fixtures, demoFixture{smokeFixture: smokeFixture{Kind: r.Kind, Name: r.Name, Description: r.Description, Segment: r.Key, Representation: representation}, state: r.State, identifier: r.Identifier, version: r.Version, listing: r.Listing, namespace: r.Namespace, expanded: !minimalKeys[r.Key]})
	}
	return fixtures
}

// demoListingInput uses the same frozen default data as the representation input.
func demoListingInput(f demoFixture, publisherID, revisionID string) map[string]any {
	result := map[string]any{"versionRevisionId": revisionID, "displayName": f.Name, "description": f.Description, "tags": f.listing.Tags, "representativeQueries": f.listing.RepresentativeQueries, "publisherId": publisherID, "license": f.listing.License, "visibility": string(f.listing.Visibility)}
	if len(f.listing.Categories) > 0 {
		// Listing commands return canonical category terms. Send that form so
		// exact command-response and receipt checks use the same contract.
		categories := make([]string, 0, len(f.listing.Categories))
		for _, value := range f.listing.Categories {
			value = strings.ToLower(strings.TrimSpace(value))
			if !slices.Contains(categories, value) {
				categories = append(categories, value)
			}
		}
		slices.Sort(categories)
		result["categories"] = categories
	}
	if f.listing.DocumentationURL != "" {
		result["documentationUrl"] = f.listing.DocumentationURL
	}
	if f.listing.Artwork != nil {
		result["extensions"] = map[string]any{artwork.IRI: f.listing.Artwork}
	}
	return result
}

// seedDemo validates local seeding arguments and runs the receipt-bound workflow under signal and timeout cancellation.
func seedDemo(arguments []string) error {
	flags := diagnostic.Flags("catalogctl seed-demo")
	baseURL := flags.String("base-url", "", "required owned loopback API origin")
	stateFile := flags.String("state-file", "output/demo/catalog-demo-v1.json", "local receipt to retain across reruns")
	timeout := flags.Duration("timeout", 5*time.Minute, "whole command deadline, maximum 10m")
	extend := flags.Bool("extend", false, "explicitly add new stable keys to the verified receipt")
	sampleSet := flags.String("sample-set", "curated", "selected manifest profile; curated is the developer default")
	dataFile := flags.String("data-file", "", "local YAML/native assets; empty uses embedded defaults")
	archiveTrust := flags.String("archive-trust-file", "output/demo/archives/cert.pem", "public CA for fixed-origin archive preflight before writes")
	archiveOrigin := flags.String("archive-origin", "https://127.0.0.1:18443", "selected development ZIP fixture HTTPS origin")
	if err := diagnostic.Parse(flags, arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *sampleSet == "" || (*sampleSet == "minimal" && *extend && *dataFile == "") {
		return safeerr.New("seed-demo: select a nonempty sample-set; embedded minimal does not support extension")
	}
	if flags.NArg() != 0 || *timeout <= 0 || *timeout > 10*time.Minute || strings.TrimSpace(*stateFile) == "" {
		return safeerr.New("seed-demo: expected --base-url URL [--state-file PATH] [--timeout DURATION up to 10m]")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	var report demoReport
	var err error
	switch {
	case *sampleSet == "minimal" && *dataFile == "":
		report, err = executeMinimalDemo(ctx, *baseURL, *stateFile)
	case *sampleSet == "expanded" && *dataFile == "":
		report, err = executeExpandedDemo(ctx, *baseURL, *stateFile, *archiveOrigin, *extend)
	default:
		plan, e := demodata.Load(*dataFile, *sampleSet, *archiveOrigin)
		if e != nil {
			return safeerr.New("seed-demo: invalid YAML/profile/assets; no API mutation attempted")
		}
		report, err = executeConfiguredDemo(ctx, *baseURL, *stateFile, *archiveOrigin, *archiveTrust, *extend, plan)
	}
	if err != nil {
		return err
	}
	return printJSON(report)
}

// executeMinimalDemo reuses native samples for focused fixtures; it never converts an expanded receipt.
func executeMinimalDemo(ctx context.Context, baseURL, stateFile string) (demoReport, error) {
	lock, err := lockDemoState(stateFile)
	if err != nil {
		return demoReport{}, err
	}
	defer func() { _ = lock.Close() }()
	receipt, exists, err := readDemoReceipt(stateFile, baseURL)
	if err != nil {
		return demoReport{}, err
	}
	if exists && receipt.Dataset != demoID {
		return demoReport{}, safeerr.New("seed-demo: minimal sample set cannot use an expanded receipt")
	}
	if _, err := os.Lstat(stateFile + ".progress"); !errors.Is(err, os.ErrNotExist) {
		return demoReport{}, safeerr.New("seed-demo: minimal sample set cannot use expanded progress")
	}
	return executeDemo(ctx, baseURL, stateFile)
}

// executeDemo intentionally uses only the maintained local development tokens.
// It is neither a remote importer nor an alternative identity bootstrap path.
func executeDemo(ctx context.Context, baseURL, stateFile string) (demoReport, error) {
	if strings.Contains(baseURL, "#") {
		return demoReport{}, safeerr.New("seed-demo: use a literal loopback address without a query or fragment")
	}
	baseURL, err := validateSmokeBaseURL(baseURL, false)
	if err != nil {
		return demoReport{}, safeerr.New("seed-demo: supply an owned literal loopback API origin")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || net.ParseIP(parsed.Hostname()) == nil || !net.ParseIP(parsed.Hostname()).IsLoopback() || parsed.ForceQuery || strings.Contains(baseURL, "#") {
		return demoReport{}, safeerr.New("seed-demo: use a literal loopback address without a query or fragment")
	}
	receipt, exists, err := readDemoReceipt(stateFile, baseURL)
	if err != nil {
		return demoReport{}, err
	}
	client := &smokeHTTPClient{baseURL: baseURL, safeErrors: true, client: newLocalWorkflowClient()}
	defer client.client.CloseIdleConnections()
	if _, err := demoExchange[map[string]any](ctx, client, http.MethodGet, "/readyz", "", "", "", nil, 200); err != nil {
		return demoReport{}, err
	}
	if err := demoInventory(ctx, client, parsed.Hostname(), receipt, exists); err != nil {
		return demoReport{}, err
	}
	status := "already_matched"
	if !exists {
		// Probe local receipt creation before mutating the service. The final
		// hard link publishes complete bytes without replacing another receipt.
		if err := os.MkdirAll(filepath.Dir(stateFile), 0700); err != nil {
			return demoReport{}, safeerr.New("seed-demo: cannot create receipt directory")
		}
		file, err := os.CreateTemp(filepath.Dir(stateFile), ".demo-*")
		if err != nil {
			return demoReport{}, safeerr.New("seed-demo: cannot prepare receipt file")
		}
		defer func() { _ = os.Remove(file.Name()) }()
		defer func() { _ = file.Close() }()
		receipt, err = createDemo(ctx, client, parsed.Hostname())
		if err != nil {
			return demoReport{}, fmt.Errorf("seed-demo: setup incomplete; inspect or reset only the owned demo instance: %w", err)
		}
		if err := verifyDemo(ctx, client, receipt); err != nil {
			return demoReport{}, err
		}
		if err := json.NewEncoder(file).Encode(receipt); err != nil {
			return demoReport{}, safeerr.New("seed-demo: setup completed but receipt write failed")
		}
		if err := file.Sync(); err != nil {
			return demoReport{}, safeerr.New("seed-demo: setup completed but receipt sync failed")
		}
		if err := file.Close(); err != nil {
			return demoReport{}, safeerr.New("seed-demo: setup completed but receipt close failed")
		}
		if err := os.Link(file.Name(), stateFile); err != nil {
			return demoReport{}, safeerr.New("seed-demo: setup completed but receipt could not be saved without replacing an existing file")
		}
		status = "created"
	} else if err := verifyDemo(ctx, client, receipt); err != nil {
		return demoReport{}, err
	}
	return demoReport{demoReceipt: receipt, Status: status, Published: 3, Drafts: 1, PendingReview: 1}, nil
}

// readDemoReceipt reads a bounded regular receipt and validates its dataset/target binding before any API write.
func readDemoReceipt(path, baseURL string) (demoReceipt, bool, error) {
	var receipt demoReceipt
	info, err := os.Lstat(path) // #nosec G703 -- exact operator-selected local receipt; regular type and private mode checked before bounded reading, not a request/document path.
	if errors.Is(err, os.ErrNotExist) {
		return receipt, false, nil
	}
	fail := safeerr.New("seed-demo: receipt is invalid or belongs to another dataset/instance; keep it with its owned demo catalog")
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > demoReceiptLimit {
		return receipt, false, fail
	}
	raw, err := remoteReadFile(path, demoReceiptLimit)
	if err != nil || jsondoc.Validate(raw) != nil {
		return receipt, false, fail
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&receipt) != nil || validateDemoReceipt(receipt, baseURL) != nil {
		return receipt, false, fail
	}
	return receipt, true, nil
}

// validateDemoReceipt requires exact fixture order, lifecycle-specific references and distinct UUIDs for the selected instance.
func validateDemoReceipt(receipt demoReceipt, baseURL string) error {
	fail := safeerr.New("seed-demo: invalid dataset, target or record IDs")
	if receipt.Bindings != nil {
		return validateConfiguredReceipt(receipt, baseURL)
	}
	if receipt.Dataset == expandedDemoID {
		return validateExpandedReceipt(receipt, baseURL)
	}
	if receipt.PluginNamespaceID != "" || receipt.ArchiveOrigin != "" || receipt.Dataset != demoID || receipt.BaseURL != baseURL || receipt.TenantID != demoTenant || len(receipt.Items) != 5 {
		return fail
	}
	ids := []string{receipt.NamespaceID, receipt.PublisherID, receipt.SnapshotID}
	for i, f := range demoFixtures() {
		item := receipt.Items[i]
		if item.Key != f.Segment || item.Kind != f.Kind || item.State != f.state || item.Identifier != demoIdentifier(f) || (item.SubmissionID == "") != (f.state == "draft") || (item.ReleaseID == "") != (f.state != "published") {
			return fail
		}
		ids = append(ids, item.ResourceID, item.VersionID, item.VersionRevisionID, item.ListingID, item.ListingRevisionID)
		if item.SubmissionID != "" {
			ids = append(ids, item.SubmissionID)
		}
		if item.ReleaseID != "" {
			ids = append(ids, item.ReleaseID)
		}
	}
	seen := make(map[string]bool)
	for _, value := range ids {
		if !identity.ValidTenantID(value) || seen[value] {
			return fail
		}
		seen[value] = true
	}
	return nil
}

// demoIdentifier places the fixture segment in the demo dataset's fixed AIR identifier namespace.
func demoIdentifier(f demoFixture) string { return f.identifier }

// demoExchange shares smoke's bounded request mechanics, not its destructive
// workflow. It refuses ambiguous JSON and never returns a response body in errors.
func demoExchange[T any](ctx context.Context, client *smokeHTTPClient, method, path, token, key, etag string, body any, status int) (T, error) {
	result, _, err := demoExchangeETag[T](ctx, client, method, path, token, key, etag, body, status)
	return result, err
}

// demoExchangeETag decodes unambiguous bounded API JSON while retaining the response ETag for later commands.
func demoExchangeETag[T any](ctx context.Context, client *smokeHTTPClient, method, path, token, key, etag string, body any, status int) (T, string, error) {
	var result T
	var response smokeHTTPResponse
	var err error
	if client.demoProgress != nil && method == http.MethodPost && path != "/api/v1/catalog/search" {
		response, err = client.demoProgress.command(ctx, client, path, token, key, etag, body, status)
	} else {
		response, err = client.request(ctx, "demo "+method+" "+strings.SplitN(path, "?", 2)[0], method, path, token, key, etag, body, status)
	}
	if err != nil {
		return result, "", err
	}
	if jsondoc.Validate(response.Body) != nil || decodeSmokeResponse(response, &result) != nil {
		return result, "", safeerr.New("seed-demo: invalid API JSON response")
	}
	return result, response.Header.Get("ETag"), nil
}

// demoGet performs a read using the fixed local administrator identity through the bounded demo transport.
func demoGet[T any](ctx context.Context, c *smokeHTTPClient, path string) (T, error) {
	return demoExchange[T](ctx, c, http.MethodGet, path, "local-admin", "", "", nil, 200)
}

// A five-item fixture needs bounded enumeration, not a whole-tenant scan. Stop
// on the first extra row; repeated or overlong cursors cannot extend this bound.
func demoPageIDs(ctx context.Context, c *smokeHTTPClient, path string, maximum int) ([]string, error) {
	var ids []string
	cursor := ""
	for pageNumber := 0; pageNumber <= maximum; pageNumber++ {
		separator := "?"
		if strings.Contains(path, "?") {
			separator = "&"
		}
		query := separator + "limit=6"
		if cursor != "" {
			query += "&cursor=" + url.QueryEscape(cursor)
		}
		page, err := demoGet[struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
			NextCursor string `json:"nextCursor"`
		}](ctx, c, path+query)
		if err != nil {
			return nil, err
		}
		if page.Items == nil {
			return nil, safeerr.New("seed-demo: inventory response must contain an items array")
		}
		if len(page.Items) > maximum-len(ids) {
			return nil, safeerr.New("seed-demo: unrelated or partial demo content; refusing writes")
		}
		for _, item := range page.Items {
			if !identity.ValidTenantID(item.ID) || slices.Contains(ids, item.ID) {
				return nil, safeerr.New("seed-demo: invalid or repeated inventory ID")
			}
			ids = append(ids, item.ID)
		}
		if page.NextCursor == "" {
			return ids, nil
		}
		if len(page.Items) == 0 || len(page.NextCursor) > 2048 || page.NextCursor == cursor {
			break
		}
		cursor = page.NextCursor
	}
	return nil, safeerr.New("seed-demo: inventory pagination exceeded the demo bound")
}

// demoInventory requires each bounded collection to match either an empty target or the receipt's exact identifiers.
func demoInventory(ctx context.Context, c *smokeHTTPClient, host string, r demoReceipt, populated bool) error {
	wantedResources := []string{}
	if populated {
		for _, item := range r.Items {
			wantedResources = append(wantedResources, item.ResourceID)
		}
	}
	for _, collection := range []struct {
		path string
		ids  []string
	}{
		{"/api/v1/publication/snapshots?host=" + url.QueryEscape(host), []string{r.SnapshotID}},
		{"/api/v1/resources", wantedResources}, {"/api/v1/namespaces", []string{r.NamespaceID}},
		{"/api/v1/publishers", []string{r.PublisherID}}, {"/api/v1/federation/sources", nil},
	} {
		want := collection.ids
		if !populated {
			want = nil
		}
		actual, err := demoPageIDs(ctx, c, collection.path, len(want))
		if err != nil {
			return err
		}
		slices.Sort(actual)
		slices.Sort(want)
		if !slices.Equal(actual, want) {
			return safeerr.New("seed-demo: receipt does not match catalog inventory; refusing writes")
		}
	}
	return nil
}

// createDemo creates the fixed governed dataset and activates its three published resources on the selected host.
func createDemo(ctx context.Context, c *smokeHTTPClient, host string) (demoReceipt, error) {
	r := demoReceipt{Dataset: demoID, BaseURL: c.baseURL, TenantID: demoTenant}
	ns, err := demoExchange[catalog.Namespace](ctx, c, "POST", "/api/v1/namespaces", "local-admin", demoID+"-namespace", "", map[string]any{"slug": demoID, "displayName": demoNamespaceName(demoID)}, 201)
	if err != nil {
		return r, err
	}
	r.NamespaceID = ns.ID
	publisher, publisherETag, err := demoExchangeETag[catalog.Publisher](ctx, c, "POST", "/api/v1/publishers", "local-admin", demoID+"-publisher", "", map[string]any{"displayName": demoPublisherName(), "domains": []string{demoDomain}}, 201)
	if err != nil {
		return r, err
	}
	r.PublisherID = publisher.ID
	if _, err := demoExchange[catalog.Publisher](ctx, c, "POST", "/api/v1/publishers/"+url.PathEscape(publisher.ID)+"/publication-domains/"+demoDomain+":approve", "local-admin", demoID+"-domain", publisherETag, map[string]any{"reason": "Approve the local demo publication domain"}, 200); err != nil {
		return r, err
	}
	for _, f := range demoFixtures() {
		item, err := createDemoItem(ctx, c, r, f)
		if err != nil {
			return r, fmt.Errorf("%s: %w", f.Segment, err)
		}
		r.Items = append(r.Items, item)
	}
	snapshot, snapshotETag, err := demoExchangeETag[publication.Snapshot](ctx, c, "POST", "/api/v1/publication/snapshots", "local-publisher", demoID+"-snapshot", "", map[string]any{"host": host}, 201)
	if err != nil {
		return r, err
	}
	r.SnapshotID = snapshot.ID
	_, err = demoExchange[publication.Snapshot](ctx, c, "POST", "/api/v1/publication/snapshots/"+url.PathEscape(snapshot.ID)+":activate", "local-publisher", demoID+"-activate", snapshotETag, map[string]any{"host": host, "reason": "Publish the three reviewed demo resources"}, 200)
	return r, err
}

// createDemoItem advances one fixture only as far as its declared draft, pending-review or published state.
func createDemoItem(ctx context.Context, c *smokeHTTPClient, r demoReceipt, f demoFixture) (demoItem, error) {
	item := demoItem{Key: f.Segment, Kind: f.Kind, State: f.state, Identifier: demoIdentifier(f)}
	key := demoID + "-" + f.Segment
	if r.Bindings != nil {
		key = "sample-" + demoHash([]string{r.Dataset, f.identifier})[7:23] + "-" + f.Segment
	}
	namespaceID := demoFixtureNamespace(r, f)
	if f.expanded {
		key = expandedDemoID + "-" + f.Segment
	}

	resource, err := demoExchange[catalog.Resource](ctx, c, "POST", "/api/v1/resources", "local-contributor", key+"-resource", "", map[string]any{"namespaceId": namespaceID, "canonicalIdentifier": item.Identifier, "kind": f.Kind}, 201)
	if err != nil {
		return item, err
	}
	item.ResourceID = resource.ID
	version, err := demoExchange[struct {
		Revision catalog.VersionRevision `json:"revision"`
	}](ctx, c, "POST", "/api/v1/resources/"+url.PathEscape(resource.ID)+"/versions", "local-contributor", key+"-version", "", map[string]any{"version": f.version, "representations": []map[string]any{f.Representation}}, 201)
	if err != nil {
		return item, err
	}
	item.VersionID, item.VersionRevisionID = version.Revision.ResourceVersionID, version.Revision.ID
	listing, listingETag, err := demoExchangeETag[catalog.CatalogListing](ctx, c, "POST", "/api/v1/catalog-listings", "local-contributor", key+"-listing", "", map[string]any{"namespaceId": namespaceID, "resourceId": resource.ID}, 201)
	if err != nil {
		return item, err
	}
	item.ListingID = listing.ID
	lr, listingRevisionETag, err := demoExchangeETag[struct {
		Listing  catalog.CatalogListing  `json:"listing"`
		Revision catalog.ListingRevision `json:"revision"`
	}](ctx, c, "POST", "/api/v1/catalog-listings/"+url.PathEscape(listing.ID)+"/revisions", "local-contributor", key+"-listing-revision", listingETag, demoListingInput(f, r.PublisherID, item.VersionRevisionID), 201)
	if err != nil {
		return item, err
	}
	item.ListingRevisionID = lr.Revision.ID
	if f.state == "draft" {
		return item, nil
	}
	submission, submissionETag, err := demoExchangeETag[catalog.ReviewSubmission](ctx, c, "POST", "/api/v1/catalog-listings/"+url.PathEscape(listing.ID)+"/submissions", "local-contributor", key+"-submit", "", map[string]any{}, 201)
	if err != nil {
		return item, err
	}
	item.SubmissionID = submission.ID
	if f.state == "pending_review" {
		return item, nil
	}
	if _, err := demoExchange[catalog.ReviewSubmission](ctx, c, "POST", "/api/v1/review-submissions/"+url.PathEscape(submission.ID)+":approve", "local-reviewer", key+"-approve", submissionETag, map[string]any{"reason": "Independent review of synthetic demo metadata"}, 200); err != nil {
		return item, err
	}
	result, err := demoExchange[struct {
		Release catalog.ListingRelease `json:"release"`
	}](ctx, c, "POST", "/api/v1/catalog-listings/"+url.PathEscape(listing.ID)+":publish", "local-publisher", key+"-publish", listingRevisionETag, map[string]any{"submissionId": submission.ID}, 201)
	item.ReleaseID = result.Release.ID
	return item, err
}

// demoExpect compares a fixed projection against authoritative reads, ignoring
// timestamps and audit history, never ignoring current pointers or fixture data.
func demoExpect(ctx context.Context, c *smokeHTTPClient, path string, expected map[string]any) error {
	actual, err := demoGet[map[string]any](ctx, c, path)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(expected)
	if err != nil {
		return err
	}
	var normalized map[string]any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return err
	}
	for key, value := range normalized {
		if !reflect.DeepEqual(actual[key], value) {
			return fmt.Errorf("seed-demo: fixture field %s differs; refusing writes", key)
		}
	}
	return nil
}

// verifyDemo compares current governed state, public manifests and reader search with the retained fixture receipt.
func verifyDemo(ctx context.Context, c *smokeHTTPClient, r demoReceipt) error {
	if r.Dataset == expandedDemoID {
		return verifyExpandedDemo(ctx, c, r)
	}
	if err := validateDemoReceipt(r, c.baseURL); err != nil {
		return err
	}
	parsed, _ := url.Parse(c.baseURL)
	if err := demoInventory(ctx, c, parsed.Hostname(), r, true); err != nil {
		return err
	}
	if err := demoExpect(ctx, c, "/api/v1/namespaces/"+r.NamespaceID, map[string]any{"id": r.NamespaceID, "tenantId": demoTenant, "slug": demoID, "displayName": demoNamespaceName(demoID), "revision": 1}); err != nil {
		return err
	}
	if err := demoExpect(ctx, c, "/api/v1/publishers/"+r.PublisherID, map[string]any{"id": r.PublisherID, "tenantId": demoTenant, "displayName": demoPublisherName(), "domains": []string{demoDomain}, "approvedPublicationDomains": []string{demoDomain}, "revision": 2}); err != nil {
		return err
	}
	for i, f := range demoFixtures() {
		if err := verifyDemoItem(ctx, c, r, r.Items[i], f); err != nil {
			return fmt.Errorf("%s: %w", f.Segment, err)
		}
	}
	if err := demoExpect(ctx, c, "/api/v1/publication/snapshots/"+r.SnapshotID+"?host="+url.QueryEscape(parsed.Hostname()), map[string]any{"id": r.SnapshotID, "tenantId": demoTenant, "host": parsed.Hostname(), "state": "active", "audience": "anonymous"}); err != nil {
		return err
	}
	manifest, err := demoExchange[publication.ARDManifest](ctx, c, "GET", "/.well-known/ard.json", "", "", "", nil, 200)
	if err != nil {
		return err
	}
	if len(manifest.Entries) != 4 || !slices.ContainsFunc(manifest.Entries, func(entry publication.Entry) bool { return entry.Type == "application/ai-registry+json" }) {
		return safeerr.New("seed-demo: public manifest must contain three Resources and the Registry")
	}
	aiCatalog, err := demoExchange[publication.AICatalog](ctx, c, "GET", "/.well-known/ai-catalog.json", "", "", "", nil, 200)
	if err != nil {
		return err
	}
	if len(aiCatalog.Entries) != 3 {
		return safeerr.New("seed-demo: AI Catalog must contain three published Resources")
	}
	for _, item := range r.Items {
		found := false
		for _, entry := range manifest.Entries {
			if entry.Identifier == item.Identifier {
				found = true
			}
		}
		aiFound := slices.ContainsFunc(aiCatalog.Entries, func(entry publication.AICatalogEntry) bool { return entry.Identifier == item.Identifier })
		if found != (item.State == "published") || aiFound != found {
			return safeerr.New("seed-demo: public discovery does not match demo lifecycle")
		}
	}
	search, err := demoExchange[catalog.CatalogSearchResponse](ctx, c, "POST", "/api/v1/catalog/search", "local-reader", "", "", map[string]any{"tags": []string{demoID}, "limit": 6}, 200)
	if err != nil {
		return err
	}
	if len(search.Items) != 3 || search.NextCursor != "" || search.Partial {
		return safeerr.New("seed-demo: governed Search does not contain exactly three published items")
	}
	for _, item := range r.Items[:3] {
		if !slices.ContainsFunc(search.Items, func(found catalog.CatalogSearchResult) bool {
			return found.Local != nil && found.Local.Release.ID == item.ReleaseID && found.Identifier == item.Identifier && found.Kind == item.Kind
		}) {
			return safeerr.New("seed-demo: governed Search identities differ")
		}
	}
	return nil
}

// verifyDemoItem checks exact parent links, current pointers, lifecycle state and representation digests against built-in data.
func verifyDemoItem(ctx context.Context, c *smokeHTTPClient, r demoReceipt, item demoItem, f demoFixture) error {
	if err := demoExpect(ctx, c, "/api/v1/resources/"+item.ResourceID, map[string]any{"id": item.ResourceID, "tenantId": demoTenant, "namespaceId": demoFixtureNamespace(r, f), "canonicalIdentifier": demoIdentifier(f), "kind": f.Kind}); err != nil {
		return err
	}
	version, err := demoGet[catalog.ResourceVersion](ctx, c, "/api/v1/resource-versions/"+item.VersionID)
	if err != nil {
		return err
	}
	sealed := ""
	if f.state == "published" {
		sealed = item.VersionRevisionID
	}
	if version.ID != item.VersionID || version.TenantID != demoTenant || version.ResourceID != item.ResourceID || version.Version != f.version || version.CandidateRevisionID != item.VersionRevisionID || version.SealedRevisionID != sealed {
		return safeerr.New("seed-demo: current version differs")
	}
	revision, err := demoGet[struct {
		Revision catalog.VersionRevision `json:"revision"`
	}](ctx, c, "/api/v1/version-revisions/"+item.VersionRevisionID)
	if err != nil {
		return err
	}
	v := revision.Revision
	if v.ID != item.VersionRevisionID || v.TenantID != demoTenant || v.ResourceVersionID != item.VersionID || len(v.Representations) != 1 {
		return safeerr.New("seed-demo: version evidence does not match fixture")
	}
	representation := v.Representations[0]
	var content []byte
	if f.Kind == catalog.KindPlugin && f.bodyDigest == "" {
		var ok bool
		content, _, ok = demodata.Archive("/" + f.Segment + ".zip")
		if !ok || representation.Plugin == nil || representation.URL != f.Representation["url"] {
			return safeerr.New("seed-demo: plugin evidence differs")
		}
	} else if markdown, ok := f.Representation["content"].(string); ok {
		content = []byte(markdown)
	} else {
		content, err = json.Marshal(f.Representation["data"])
		if err != nil {
			return err
		}
	}
	digest := sha256.Sum256(content)
	expectedDigest := "sha256:" + hex.EncodeToString(digest[:])
	if f.bodyDigest != "" {
		expectedDigest, err = demoWireDigest(f)
		if err != nil {
			return err
		}
	}
	if f.Kind == catalog.KindPlugin && (representation.Plugin == nil || representation.URL != f.Representation["url"]) {
		return safeerr.New("seed-demo: plugin evidence differs")
	}
	if f.Representation["packageData"] != nil && (representation.Package == nil || !reflect.DeepEqual(representation.Handoff, func() *catalog.SkillHandoff {
		raw, _ := json.Marshal(f.Representation["handoff"])
		var handoff catalog.SkillHandoff
		_ = json.Unmarshal(raw, &handoff)
		return &handoff
	}())) {
		return safeerr.New("seed-demo: Skill package/handoff differs")
	}
	if representation.ContentDigest != expectedDigest || representation.MediaType != f.Representation["mediaType"] {
		return safeerr.New("seed-demo: representation content differs")
	}
	want := demoListingInput(f, r.PublisherID, item.VersionRevisionID)
	want["id"], want["tenantId"], want["listingId"] = item.ListingRevisionID, demoTenant, item.ListingID
	for _, field := range []string{"supportUrl", "termsUrl", "relationships"} {
		want[field] = nil
	}
	if len(f.listing.Categories) == 0 {
		want["categories"] = nil
	}
	if f.listing.Artwork == nil {
		want["extensions"] = nil
	} else {
		revision, getErr := demoGet[catalog.ListingRevision](ctx, c, "/api/v1/listing-revisions/"+item.ListingRevisionID)
		if getErr != nil {
			return getErr
		}
		if !demoListingExtensionsMatch(want["extensions"], revision.Extensions) {
			return safeerr.New("seed-demo: listing extensions differ")
		}
		delete(want, "extensions")
	}
	if f.listing.DocumentationURL == "" {
		want["documentationUrl"] = nil
	}
	if err := demoExpect(ctx, c, "/api/v1/listing-revisions/"+item.ListingRevisionID, want); err != nil {
		return err
	}
	listing, err := demoGet[catalog.CatalogListing](ctx, c, "/api/v1/catalog-listings/"+item.ListingID)
	if err != nil {
		return err
	}
	if listing.ID != item.ListingID || listing.TenantID != demoTenant || listing.ResourceID != item.ResourceID || listing.NamespaceID != demoFixtureNamespace(r, f) || listing.CandidateRevisionID != item.ListingRevisionID || listing.CurrentReleaseID != item.ReleaseID {
		return safeerr.New("seed-demo: current listing differs")
	}
	if item.SubmissionID != "" {
		state := "submitted"
		if f.state == "published" {
			state = "approved"
		}
		if err := demoExpect(ctx, c, "/api/v1/review-submissions/"+item.SubmissionID, map[string]any{"id": item.SubmissionID, "tenantId": demoTenant, "listingId": item.ListingID, "versionRevisionId": item.VersionRevisionID, "listingRevisionId": item.ListingRevisionID, "state": state}); err != nil {
			return err
		}
	}
	if item.ReleaseID != "" {
		if err := demoExpect(ctx, c, "/api/v1/listing-releases/"+item.ReleaseID, map[string]any{"id": item.ReleaseID, "tenantId": demoTenant, "listingId": item.ListingID, "submissionId": item.SubmissionID, "versionRevisionId": item.VersionRevisionID, "listingRevisionId": item.ListingRevisionID, "state": "published"}); err != nil {
			return err
		}
	}
	return nil
}

func demoNamespaceName(key string) string {
	for _, ns := range demodata.DefaultPlan("expanded", "").Namespaces {
		if ns.Key == key {
			return ns.DisplayName
		}
	}
	return ""
}
func demoPublisherName() string {
	return demodata.DefaultPlan("expanded", "").Publishers[0].DisplayName
}
