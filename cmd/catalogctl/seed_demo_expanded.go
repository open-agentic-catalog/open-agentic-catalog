package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

const expandedDemoID = "catalog-demo-v2"

func expandedDemoFixtures(origin string) []demoFixture {
	return defaultDemoFixtures("expanded", origin)
}

func demoFixtureNamespace(r demoReceipt, f demoFixture) string {
	if r.Bindings != nil {
		return r.Bindings.Namespaces[f.namespace]
	}
	if f.namespace == "ide-plugins" {
		return r.PluginNamespaceID
	}
	return r.NamespaceID
}

func validateDemoArchiveOrigin(origin string) error {
	u, err := url.Parse(origin)
	if err == nil && len(origin) <= 256 && u.RawPath == "" && u.Scheme == "https" && u.User == nil && u.Path == "" && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" {
		if u.Port() != "" {
			port, e := strconv.Atoi(u.Port())
			if e != nil || port < 1 || port > 65535 {
				return safeerr.New("seed-demo: invalid fixture port")
			}
		}
		ip, e := netip.ParseAddr(u.Hostname())
		if e == nil && !ip.Is4In6() && ip.Zone() == "" && ip.String() == u.Hostname() && (ip.IsPrivate() || ip.IsLoopback()) {
			return nil
		}
	}
	return safeerr.New("seed-demo: archive origin must be literal private/loopback HTTPS without path or credentials")
}

func validateExpandedReceipt(r demoReceipt, baseURL string) error {
	fail := safeerr.New("seed-demo: invalid expanded dataset receipt")
	if r.BaseURL != baseURL || r.TenantID != demoTenant || len(r.Items) != 14 || validateDemoArchiveOrigin(r.ArchiveOrigin) != nil {
		return fail
	}
	ids := []string{r.NamespaceID, r.PluginNamespaceID, r.PublisherID, r.SnapshotID}
	for i, f := range expandedDemoFixtures(r.ArchiveOrigin) {
		item := r.Items[i]
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
	seen := map[string]bool{}
	for _, id := range ids {
		if !identity.ValidTenantID(id) || seen[id] {
			return fail
		}
		seen[id] = true
	}
	return nil
}

// An advisory file lock survives process death without requiring removal of stale progress.
func lockDemoState(path string) (*os.File, error) {
	fail := safeerr.New("seed-demo: private state is unavailable or another seed command is running")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil { // #nosec G703 -- exact operator-selected local state directory, not an API/document path.
		return nil, fail
	}
	lock := path + ".lock"
	info, err := os.Lstat(lock) // #nosec G703 -- inspect only the exact operator-selected adjacent lock; reject links/nonregular/insecure modes.
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fail
	}
	if err == nil && (!info.Mode().IsRegular() || info.Mode().Perm() != 0600) {
		return nil, fail
	}
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600) // #nosec G304 G703 -- exact selected adjacent lock; refuse final symlinks, verify regular private file identity before acquiring it.
	if err != nil {
		return nil, fail
	}
	actual, e := os.Lstat(lock) // #nosec G703 -- recheck only the selected adjacent lock, compare opened identity and require regular private mode.
	opened, se := f.Stat()
	if e != nil || se != nil || !actual.Mode().IsRegular() || !os.SameFile(actual, opened) || actual.Mode().Perm() != 0600 || syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		_ = f.Close()
		return nil, fail
	}
	return f, nil
}

func executeExpandedDemo(ctx context.Context, baseURL, stateFile, origin string, extend bool) (demoReport, error) {
	target, err := validateSmokeBaseURL(baseURL, false)
	if err != nil {
		return demoReport{}, safeerr.New("seed-demo: supply an owned literal loopback API origin")
	}
	u, err := url.Parse(target)
	if err != nil || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || strings.Contains(baseURL, "#") || u.Hostname() == "localhost" || validateDemoArchiveOrigin(origin) != nil {
		return demoReport{}, safeerr.New("seed-demo: invalid local API or archive origin")
	}
	ip, err := netip.ParseAddr(u.Hostname())
	if err != nil || !ip.IsLoopback() {
		return demoReport{}, safeerr.New("seed-demo: supply an owned literal loopback API origin")
	}
	lock, err := lockDemoState(stateFile)
	if err != nil {
		return demoReport{}, err
	}
	defer func() { _ = lock.Close() }()
	receipt, exists, err := readDemoReceipt(stateFile, target)
	if err != nil {
		return demoReport{}, err
	}
	var originalBytes []byte
	if exists {
		originalBytes, err = remoteReadFile(stateFile, demoReceiptLimit)
		if err != nil {
			return demoReport{}, err
		}
	}
	c := &smokeHTTPClient{baseURL: target, safeErrors: true, client: newLocalWorkflowClient()}
	defer c.client.CloseIdleConnections()
	if exists && receipt.Dataset == demoID && !extend {
		return executeDemo(ctx, target, stateFile)
	}
	if exists && receipt.Dataset == expandedDemoID {
		if receipt.ArchiveOrigin != origin {
			return demoReport{}, safeerr.New("seed-demo: archive origin differs from receipt")
		}
		if err = verifyExpandedDemo(ctx, c, receipt); err != nil {
			return demoReport{}, err
		}
		return demoReport{demoReceipt: receipt, Status: "already_matched", Published: 12, Drafts: 1, PendingReview: 1}, nil
	}
	if extend && !exists {
		return demoReport{}, safeerr.New("seed-demo: explicit extension requires the original receipt")
	}
	p, resumed, err := readDemoProgress(stateFile+".progress", target, origin)
	if err != nil {
		return demoReport{}, err
	}
	if resumed && ((exists && p.Original == nil) || (!exists && p.Original != nil) || (exists && !reflect.DeepEqual(*p.Original, receipt))) {
		return demoReport{}, safeerr.New("seed-demo: original receipt and progress disagree")
	}
	if !resumed {
		if exists {
			if !extend {
				return demoReport{}, safeerr.New("seed-demo: select explicit extension")
			}
			if err = verifyOriginalDemoScope(ctx, c, receipt, "active"); err != nil {
				return demoReport{}, err
			}
			p.Original = &receipt
		} else if err = demoInventory(ctx, c, u.Hostname(), demoReceipt{}, false); err != nil {
			return demoReport{}, err
		}
		if err = preflightDemoCollisions(ctx, c, p); err != nil {
			return demoReport{}, err
		}
		if err = p.save(); err != nil {
			return demoReport{}, err
		}
	}
	// Replay only the fixed plan before trusting journal lookup hints or issuing new commands.
	c.demoProgress = p
	p.dry = true
	_, err = populateExpandedDemo(ctx, c, p, u.Hostname())
	if err != nil && !errors.Is(err, errDemoCheckpoint) {
		return demoReport{}, err
	}
	if p.index < len(p.Steps)-1 {
		return demoReport{}, safeerr.New("seed-demo: progress is not a valid command prefix")
	}
	if len(p.Steps) > 0 && len(p.Steps[len(p.Steps)-1].Response) == 0 {
		pending := &p.Steps[len(p.Steps)-1]
		recovered, found, recoveryErr := reconcileDemoStep(ctx, c, *pending)
		if recoveryErr != nil {
			return demoReport{}, recoveryErr
		}
		if found {
			pending.Response, pending.ResponseETag = recovered.Body, recovered.Header.Get("ETag")
			if err = p.save(); err != nil {
				return demoReport{}, err
			}
			p.index = 0
			_, err = populateExpandedDemo(ctx, c, p, u.Hostname())
			if err != nil && !errors.Is(err, errDemoCheckpoint) {
				return demoReport{}, err
			}
		}
	}
	if p.Original != nil {
		state := "active"
		if len(p.Steps) > 0 && len(p.Steps[len(p.Steps)-1].Response) != 0 && strings.HasSuffix(p.Steps[len(p.Steps)-1].Path, ":activate") {
			state = "retired"
		}
		if err = verifyOriginalDemoScope(ctx, c, *p.Original, state); err != nil {
			return demoReport{}, err
		}
	}
	if err = preflightDemoCollisions(ctx, c, p); err != nil {
		return demoReport{}, err
	}
	if err = p.verifyCurrent(ctx, c); err != nil {
		return demoReport{}, err
	}
	p.dry, p.index = false, 0
	result, err := populateExpandedDemo(ctx, c, p, u.Hostname())
	if err != nil {
		return demoReport{}, fmt.Errorf("seed-demo: incomplete; keep receipt and progress and rerun the same command: %w", err)
	}
	c.demoProgress = nil
	if err = verifyExpandedDemo(ctx, c, result); err != nil {
		return demoReport{}, err
	}
	if err = replaceDemoFile(stateFile, result, originalBytes, exists); err != nil {
		return demoReport{}, err
	}
	if err = os.Remove(p.path); err != nil {
		return demoReport{}, safeerr.New("seed-demo: complete receipt saved; progress cleanup failed")
	}
	status := "created"
	if exists {
		status = "extended"
	} else if resumed {
		status = "resumed"
	}
	return demoReport{demoReceipt: result, Status: status, Published: 12, Drafts: 1, PendingReview: 1}, nil
}

// Check all nine extension identifiers and the additional namespace before the first write.
func preflightDemoCollisions(ctx context.Context, c *smokeHTTPClient, p *demoProgress) error {
	allowed := map[string]string{}
	for _, step := range p.Steps {
		if len(step.Response) == 0 {
			continue
		}
		var body, result map[string]any
		if json.Unmarshal(step.Body, &body) != nil || json.Unmarshal(step.Response, &result) != nil {
			return safeerr.New("seed-demo: invalid progress")
		}
		if step.Path == "/api/v1/resources" {
			allowed[fmt.Sprint(body["canonicalIdentifier"])] = fmt.Sprint(result["id"])
		}
		if step.Path == "/api/v1/namespaces" {
			allowed[fmt.Sprint(body["slug"])] = fmt.Sprint(result["id"])
		}
	}
	steps := []demoStep{{Path: "/api/v1/namespaces", Body: json.RawMessage(`{"slug":"ide-plugins"}`)}}
	fixtures := expandedDemoFixtures(p.Origin)
	if p.Original != nil {
		fixtures = fixtures[5:]
	}
	for _, f := range fixtures {
		raw, err := json.Marshal(map[string]any{"canonicalIdentifier": demoIdentifier(f)})
		if err != nil {
			return err
		}
		steps = append(steps, demoStep{Path: "/api/v1/resources", Body: raw})
	}
	for _, step := range steps {
		ids, err := demoStepIDs(ctx, c, step)
		if err != nil {
			return err
		}
		var body map[string]any
		if json.Unmarshal(step.Body, &body) != nil {
			return safeerr.New("seed-demo: invalid fixture")
		}
		binding, _ := body["canonicalIdentifier"].(string)
		if binding == "" {
			binding, _ = body["slug"].(string)
		}
		if len(ids) > 1 || len(ids) == 1 && ids[0] != allowed[binding] {
			return safeerr.New("seed-demo: expanded fixture collision; refusing writes")
		}
	}
	return nil
}

func populateExpandedDemo(ctx context.Context, c *smokeHTTPClient, p *demoProgress, host string) (demoReceipt, error) {
	r := demoReceipt{Dataset: expandedDemoID, BaseURL: c.baseURL, TenantID: demoTenant, ArchiveOrigin: p.Origin}
	if p.Original != nil {
		r.NamespaceID, r.PublisherID = p.Original.NamespaceID, p.Original.PublisherID
		r.Items = append([]demoItem(nil), p.Original.Items...)
	} else {
		ns, err := demoExchange[catalog.Namespace](ctx, c, "POST", "/api/v1/namespaces", "local-admin", expandedDemoID+"-namespace", "", map[string]any{"slug": demoID, "displayName": demoNamespaceName(demoID)}, 201)
		if err != nil {
			return r, err
		}
		r.NamespaceID = ns.ID
		publisher, etag, err := demoExchangeETag[catalog.Publisher](ctx, c, "POST", "/api/v1/publishers", "local-admin", expandedDemoID+"-publisher", "", map[string]any{"displayName": demoPublisherName(), "domains": []string{demoDomain}}, 201)
		if err != nil {
			return r, err
		}
		r.PublisherID = publisher.ID
		if _, err = demoExchange[catalog.Publisher](ctx, c, "POST", "/api/v1/publishers/"+publisher.ID+"/publication-domains/"+demoDomain+":approve", "local-admin", expandedDemoID+"-domain", etag, map[string]any{"reason": "Approve the local demo publication domain"}, 200); err != nil {
			return r, err
		}
	}
	ns, err := demoExchange[catalog.Namespace](ctx, c, "POST", "/api/v1/namespaces", "local-admin", expandedDemoID+"-plugins-namespace", "", map[string]any{"slug": "ide-plugins", "displayName": demoNamespaceName("ide-plugins")}, 201)
	if err != nil {
		return r, err
	}
	r.PluginNamespaceID = ns.ID
	fixtures := expandedDemoFixtures(p.Origin)
	for _, f := range fixtures[len(r.Items):] {
		item, e := createDemoItem(ctx, c, r, f)
		if e != nil {
			return r, fmt.Errorf("%s: %w", f.Segment, e)
		}
		r.Items = append(r.Items, item)
	}
	if !p.dry {
		if err := verifyExpandedRecords(ctx, c, r); err != nil {
			return r, err
		}
	}
	snapshot, etag, err := demoExchangeETag[publication.Snapshot](ctx, c, "POST", "/api/v1/publication/snapshots", "local-publisher", expandedDemoID+"-snapshot", "", map[string]any{"host": host}, 201)
	if err != nil {
		return r, err
	}
	r.SnapshotID = snapshot.ID
	// Verify every owned release is present without dropping unrelated eligible releases.
	if err = verifyExpandedSnapshot(snapshot, r); err != nil {
		return r, err
	}
	_, err = demoExchange[publication.Snapshot](ctx, c, "POST", "/api/v1/publication/snapshots/"+snapshot.ID+":activate", "local-publisher", expandedDemoID+"-activate", etag, map[string]any{"host": host, "reason": "Publish twelve independently reviewed demo resources"}, 200)
	return r, err
}

func verifyOriginalDemoScope(ctx context.Context, c *smokeHTTPClient, r demoReceipt, state string) error {
	if err := validateDemoReceipt(r, c.baseURL); err != nil {
		return err
	}
	if err := verifyDemoParents(ctx, c, r); err != nil {
		return err
	}
	for i, f := range demoFixtures() {
		if err := verifyDemoItem(ctx, c, r, r.Items[i], f); err != nil {
			return err
		}
		if err := verifyDemoItemHistory(ctx, c, r.Items[i]); err != nil {
			return err
		}
	}
	u, _ := url.Parse(c.baseURL)
	return demoExpect(ctx, c, "/api/v1/publication/snapshots/"+r.SnapshotID+"?host="+url.QueryEscape(u.Hostname()), map[string]any{"id": r.SnapshotID, "tenantId": demoTenant, "host": u.Hostname(), "audience": "anonymous", "state": state})
}

func verifyDemoParents(ctx context.Context, c *smokeHTTPClient, r demoReceipt) error {
	if err := demoExpect(ctx, c, "/api/v1/namespaces/"+r.NamespaceID, map[string]any{"id": r.NamespaceID, "tenantId": demoTenant, "slug": demoID, "displayName": demoNamespaceName(demoID), "revision": 1}); err != nil {
		return err
	}
	return demoExpect(ctx, c, "/api/v1/publishers/"+r.PublisherID, map[string]any{"id": r.PublisherID, "tenantId": demoTenant, "displayName": demoPublisherName(), "domains": []string{demoDomain}, "approvedPublicationDomains": []string{demoDomain}, "revision": 2})
}

func verifyExpandedSnapshot(snapshot publication.Snapshot, r demoReceipt) error {
	for _, item := range r.Items {
		ard := slices.ContainsFunc(snapshot.ARD.Entries, func(e publication.Entry) bool { return e.Identifier == item.Identifier })
		ai := slices.ContainsFunc(snapshot.AICatalog.Entries, func(e publication.AICatalogEntry) bool { return e.Identifier == item.Identifier })
		if ard != (item.State == "published") || ai != ard {
			return safeerr.New("seed-demo: snapshot membership differs from reviewed demo release: " + item.Key)
		}
	}
	return nil
}

func verifyExpandedDemo(ctx context.Context, c *smokeHTTPClient, r demoReceipt) error {
	if err := validateExpandedReceipt(r, c.baseURL); err != nil {
		return err
	}
	if err := verifyExpandedRecords(ctx, c, r); err != nil {
		return err
	}
	u, _ := url.Parse(c.baseURL)
	snapshot, err := demoGet[publication.Snapshot](ctx, c, "/api/v1/publication/snapshots/"+r.SnapshotID+"?host="+url.QueryEscape(u.Hostname()))
	if err != nil {
		return err
	}
	if snapshot.ID != r.SnapshotID || snapshot.State != publication.SnapshotActive || snapshot.TenantID != demoTenant || snapshot.Host != u.Hostname() {
		return safeerr.New("seed-demo: active demo snapshot differs")
	}
	if err = verifyExpandedSnapshot(snapshot, r); err != nil {
		return err
	}
	manifest, err := demoExchange[publication.ARDManifest](ctx, c, "GET", "/.well-known/ard.json", "", "", "", nil, 200)
	if err != nil {
		return err
	}
	ai, err := demoExchange[publication.AICatalog](ctx, c, "GET", "/.well-known/ai-catalog.json", "", "", "", nil, 200)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(manifest, snapshot.ARD) || !reflect.DeepEqual(ai, snapshot.AICatalog) {
		return safeerr.New("seed-demo: public documents differ from active snapshot")
	}
	search, err := demoExchange[catalog.CatalogSearchResponse](ctx, c, "POST", "/api/v1/catalog/search", "local-reader", "", "", map[string]any{"tags": []string{demoID}, "limit": 20}, 200)
	if err != nil {
		return err
	}
	if len(search.Items) != 12 || search.NextCursor != "" || search.Partial {
		return safeerr.New("seed-demo: Search must contain twelve reviewed demo items")
	}
	for _, item := range r.Items {
		found := slices.ContainsFunc(search.Items, func(result catalog.CatalogSearchResult) bool {
			return result.Local != nil && result.Local.Release.ID == item.ReleaseID && result.Identifier == item.Identifier && result.Kind == item.Kind
		})
		if found != (item.State == "published") {
			return safeerr.New("seed-demo: Search identity differs")
		}
	}
	return nil
}

// History is scoped to owned parents, never a prohibition on unrelated tenant resources.
func verifyDemoItemHistory(ctx context.Context, c *smokeHTTPClient, item demoItem) error {
	collections := []struct {
		path string
		ids  []string
	}{
		{"/api/v1/resources/" + item.ResourceID + "/versions", []string{item.VersionID}},
		{"/api/v1/resources/" + item.ResourceID + "/listings", []string{item.ListingID}},
		{"/api/v1/resource-versions/" + item.VersionID + "/revisions", []string{item.VersionRevisionID}},
		{"/api/v1/catalog-listings/" + item.ListingID + "/revisions", []string{item.ListingRevisionID}},
		{"/api/v1/review-submissions?listingId=" + item.ListingID, nil},
		{"/api/v1/catalog-listings/" + item.ListingID + "/releases", nil},
	}
	if item.SubmissionID != "" {
		collections[4].ids = []string{item.SubmissionID}
	}
	if item.ReleaseID != "" {
		collections[5].ids = []string{item.ReleaseID}
	}
	for _, collection := range collections {
		actual, err := demoPageIDs(ctx, c, collection.path, len(collection.ids))
		if err != nil {
			return err
		}
		if !slices.Equal(actual, collection.ids) {
			return safeerr.New("seed-demo: owned history differs; refusing writes")
		}
	}
	return nil
}

func verifyExpandedRecords(ctx context.Context, c *smokeHTTPClient, r demoReceipt) error {
	if err := verifyDemoParents(ctx, c, r); err != nil {
		return err
	}
	if err := demoExpect(ctx, c, "/api/v1/namespaces/"+r.PluginNamespaceID, map[string]any{"id": r.PluginNamespaceID, "tenantId": demoTenant, "slug": "ide-plugins", "displayName": demoNamespaceName("ide-plugins"), "revision": 1}); err != nil {
		return err
	}
	for i, f := range expandedDemoFixtures(r.ArchiveOrigin) {
		if err := verifyDemoItem(ctx, c, r, r.Items[i], f); err != nil {
			return fmt.Errorf("%s: %w", f.Segment, err)
		}
		if err := verifyDemoItemHistory(ctx, c, r.Items[i]); err != nil {
			return err
		}
	}

	return nil
}
