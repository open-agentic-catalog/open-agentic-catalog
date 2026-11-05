package main

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/demodata"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

// The receipt binds inputs, never substitutes for current-authority API readback.
type demoBindings struct {
	Format        int               `json:"format"`
	LegacyProfile string            `json:"legacyProfile,omitempty"`
	Inputs        map[string]string `json:"inputs"`
	Parents       map[string]string `json:"parents"`
	Namespaces    map[string]string `json:"namespaces"`
	Publishers    map[string]string `json:"publishers"`
}

func demoHash(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
}
func demoPlanBinding(plan demodata.Plan) string {
	resources := map[string]string{}
	for _, r := range plan.Resources {
		resources[r.Resource.Key] = r.Digest
	}
	return demoHash(struct {
		Dataset    string
		Resources  map[string]string
		Namespaces []demodata.Namespace
		Publishers []demodata.Publisher
	}{plan.Dataset, resources, plan.Namespaces, plan.Publishers})
}
func sortDemoPlan(plan *demodata.Plan) {
	slices.SortFunc(plan.Resources, func(a, b demodata.PreparedResource) int { return strings.Compare(a.Resource.Key, b.Resource.Key) })
	slices.SortFunc(plan.Namespaces, func(a, b demodata.Namespace) int { return strings.Compare(a.Key, b.Key) })
	for i := range plan.Publishers {
		plan.Publishers[i].Domains = slices.Clone(plan.Publishers[i].Domains)
		slices.Sort(plan.Publishers[i].Domains)
	}
	slices.SortFunc(plan.Publishers, func(a, b demodata.Publisher) int { return strings.Compare(a.Key, b.Key) })
}

// Known N-88 entries are preserved explicitly when adopting their exact receipt.
// Subsequent YAML edits still cannot remove or change any receipt-bound new entry.
func retainedDemoPlan(plan demodata.Plan, receipt demoReceipt) (demodata.Plan, error) {
	legacy := ""
	switch {
	case receipt.Bindings != nil:
		legacy = receipt.Bindings.LegacyProfile
	case receipt.Dataset == demoID:
		legacy = "minimal"
	case receipt.Dataset == expandedDemoID:
		legacy = "expanded"
	}
	if legacy != "" {
		old := demodata.DefaultPlan(legacy, receipt.ArchiveOrigin)
		if receipt.ArchiveOrigin == "" {
			old = demodata.DefaultPlan(legacy, "")
		}
		if plan.Dataset != "catalog-demo" {
			return plan, safeerr.New("seed-demo: N-88 adoption requires the catalog-demo dataset")
		}
		for _, item := range old.Resources {
			found := false
			for _, current := range plan.Resources {
				if current.Resource.Key == item.Resource.Key {
					found = true
					if current.Digest != item.Digest {
						return plan, safeerr.New("seed-demo: prior sample changed: " + item.Resource.Key)
					}
				}
			}
			if !found {
				plan.Resources = append(plan.Resources, item)
			}
		}
		for _, ns := range old.Namespaces {
			found := false
			for _, current := range plan.Namespaces {
				if current.Key == ns.Key {
					found = true
					if !reflect.DeepEqual(current, ns) {
						return plan, safeerr.New("seed-demo: prior namespace changed: " + ns.Key)
					}
				}
			}
			if !found {
				plan.Namespaces = append(plan.Namespaces, ns)
			}
		}
		for _, pub := range old.Publishers {
			found := false
			for _, current := range plan.Publishers {
				if current.Key == pub.Key {
					found = true
					if !reflect.DeepEqual(current, pub) {
						return plan, safeerr.New("seed-demo: prior publisher changed: " + pub.Key)
					}
				}
			}
			if !found {
				plan.Publishers = append(plan.Publishers, pub)
			}
		}
	}
	sortDemoPlan(&plan)
	if len(plan.Resources) > demodata.MaxResources {
		return plan, safeerr.New("seed-demo: retained plus selected sample bound exceeded")
	}
	return plan, nil
}

func configuredDemoFixtures(plan demodata.Plan) []demoFixture {
	result := make([]demoFixture, 0, len(plan.Resources))
	for _, item := range plan.Resources {
		raw, err := json.Marshal(item.Representation)
		if err != nil {
			return nil
		}
		var rep map[string]any
		if json.Unmarshal(raw, &rep) != nil {
			return nil
		}
		r := item.Resource
		result = append(result, demoFixture{smokeFixture: smokeFixture{Kind: r.Kind, Name: r.Name, Description: r.Description, Segment: r.Key, Representation: rep}, state: r.State, identifier: r.Identifier, version: r.Version, listing: r.Listing, namespace: r.Namespace, publisher: r.Publisher, bodyDigest: item.BodyDigest})
	}
	return result
}

func validateConfiguredReceipt(r demoReceipt, base string) error {
	fail := safeerr.New("seed-demo: invalid configured sample receipt")
	b := r.Bindings
	if b == nil || b.Format != 1 || !catalog.ValidSkillName(r.Dataset) || r.BaseURL != base || r.TenantID != demoTenant || len(r.Items) == 0 || len(r.Items) > demodata.MaxResources || (r.ArchiveOrigin != "" && validateDemoArchiveOrigin(r.ArchiveOrigin) != nil) || len(b.Inputs) != len(r.Items) || len(b.Namespaces) == 0 || len(b.Namespaces) > 16 || len(b.Publishers) == 0 || len(b.Publishers) > 16 || len(b.Parents) != len(b.Namespaces)+len(b.Publishers) || (b.LegacyProfile != "" && b.LegacyProfile != "minimal" && b.LegacyProfile != "expanded") {
		return fail
	}
	ids := []string{r.SnapshotID}
	keys := map[string]bool{}
	for _, collection := range []map[string]string{b.Namespaces, b.Publishers} {
		for key, id := range collection {
			if !catalog.ValidSkillName(key) {
				return fail
			}
			ids = append(ids, id)
		}
	}
	for key, digest := range b.Parents {
		if (strings.HasPrefix(key, "namespace:") && b.Namespaces[strings.TrimPrefix(key, "namespace:")] == "") || (strings.HasPrefix(key, "publisher:") && b.Publishers[strings.TrimPrefix(key, "publisher:")] == "") || !(strings.HasPrefix(key, "namespace:") || strings.HasPrefix(key, "publisher:")) || !demoDigestValid(digest) {
			return fail
		}
	}
	for _, item := range r.Items {
		if (item.Kind == catalog.KindPlugin && r.ArchiveOrigin == "") || keys[item.Key] || !catalog.ValidSkillName(item.Key) || !item.Kind.Valid() || !demoDigestValid(b.Inputs[item.Key]) || !slices.Contains([]string{"published", "draft", "pending_review"}, item.State) || (item.SubmissionID == "") != (item.State == "draft") || (item.ReleaseID == "") != (item.State != "published") {
			return fail
		}
		if _, err := catalog.ParseAIRIdentifier(item.Identifier); err != nil {
			return fail
		}
		keys[item.Key] = true
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
	if !demoMapContains(b.Namespaces, r.NamespaceID) {
		return fail
	}
	if !demoMapContains(b.Publishers, r.PublisherID) {
		return fail
	}
	if r.PluginNamespaceID != "" && b.Namespaces["ide-plugins"] != r.PluginNamespaceID {
		return fail
	}
	return nil
}
func demoDigestValid(s string) bool {
	if len(s) != 71 || !strings.HasPrefix(s, "sha256:") {
		return false
	}
	for _, c := range s[7:] {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}
func demoMapContains(values map[string]string, id string) bool {
	for _, value := range values {
		if value == id {
			return true
		}
	}
	return false
}

func checkDemoBindings(plan demodata.Plan, r demoReceipt) (int, error) {
	if r.Bindings == nil {
		return len(plan.Resources) - len(r.Items), nil
	}
	if r.Dataset != plan.Dataset {
		return 0, safeerr.New("seed-demo: dataset differs from receipt")
	}
	current := map[string]demodata.PreparedResource{}
	for _, item := range plan.Resources {
		current[item.Resource.Key] = item
	}
	for _, item := range r.Items {
		value, ok := current[item.Key]
		if !ok || r.Bindings.Inputs[item.Key] != value.Digest || item.Kind != value.Resource.Kind || item.State != value.Resource.State || item.Identifier != value.Resource.Identifier {
			return 0, safeerr.New("seed-demo: prior sample missing or changed: " + item.Key)
		}
	}
	parents := map[string]string{}
	for _, ns := range plan.Namespaces {
		parents["namespace:"+ns.Key] = demoHash(ns)
	}
	for _, pub := range plan.Publishers {
		parents["publisher:"+pub.Key] = demoHash(pub)
	}
	for key, binding := range r.Bindings.Parents {
		if parents[key] != binding {
			return 0, safeerr.New("seed-demo: prior parent missing or changed")
		}
	}
	return len(plan.Resources) - len(r.Items), nil
}

func executeConfiguredDemo(ctx context.Context, base, state, origin, trustFile string, extend bool, plan demodata.Plan) (demoReport, error) {
	if len(plan.Resources) == 0 || configuredDemoFixtures(plan) == nil {
		return demoReport{}, safeerr.New("seed-demo: invalid frozen sample plan")
	}
	target, err := validateSmokeBaseURL(base, false)
	if err != nil {
		return demoReport{}, safeerr.New("seed-demo: supply an owned literal loopback API origin")
	}
	u, _ := url.Parse(target)
	ip, e := netip.ParseAddr(u.Hostname())
	if e != nil || !ip.IsLoopback() || u.ForceQuery || strings.Contains(base, "#") || validateDemoArchiveOrigin(origin) != nil {
		return demoReport{}, safeerr.New("seed-demo: invalid local API or archive origin")
	}
	lock, err := lockDemoState(state)
	if err != nil {
		return demoReport{}, err
	}
	defer func() { _ = lock.Close() }()
	r, exists, err := readDemoReceipt(state, target)
	if err != nil {
		return demoReport{}, err
	}
	var original []byte
	if exists {
		original, err = remoteReadFile(state, demoReceiptLimit)
		if err != nil {
			return demoReport{}, err
		}
		if r.ArchiveOrigin != "" && r.ArchiveOrigin != origin {
			return demoReport{}, safeerr.New("seed-demo: archive origin differs from receipt")
		}
		plan, err = retainedDemoPlan(plan, r)
		if err != nil {
			return demoReport{}, err
		}
	}
	sortDemoPlan(&plan)
	additions := len(plan.Resources)
	if exists {
		additions, err = checkDemoBindings(plan, r)
		if err != nil {
			return demoReport{}, err
		}
		if r.Bindings == nil && !extend {
			return demoReport{}, safeerr.New("seed-demo: select --extend to adopt the verified N-88 receipt")
		}
		if additions > 0 && !extend {
			return demoReport{}, safeerr.New("seed-demo: new sample keys require --extend")
		}
	}
	c := &smokeHTTPClient{baseURL: target, safeErrors: true, client: newLocalWorkflowClient()}
	defer c.client.CloseIdleConnections()
	if _, err = demoExchange[map[string]any](ctx, c, http.MethodGet, "/readyz", "", "", "", nil, 200); err != nil {
		return demoReport{}, err
	}
	if exists && additions == 0 && r.Bindings != nil {
		if err = verifyConfiguredDemo(ctx, c, r, plan, "active"); err != nil {
			return demoReport{}, err
		}
		// A crash after receipt replacement but before journal removal is complete,
		// not a second extension. Remove only its verified full command prefix.
		p, pending, e := readDemoProgress(state+".progress", target, origin)
		if e != nil {
			return demoReport{}, e
		}
		if pending {
			if p.PlanDigest != demoPlanBinding(plan) {
				return demoReport{}, safeerr.New("seed-demo: completed receipt has mismatched progress")
			}
			p.dry = true
			c.demoProgress = p
			completed, e := populateConfiguredDemo(ctx, c, p, plan, u.Hostname())
			c.demoProgress = nil
			if e != nil || p.index != len(p.Steps) || !reflect.DeepEqual(completed, r) {
				return demoReport{}, safeerr.New("seed-demo: completed receipt has incomplete or mismatched progress")
			}
			if e = os.Remove(p.path); e != nil { // #nosec G703 -- remove only the exact private journal whose complete command prefix and receipt were verified under its exclusive lock.
				return demoReport{}, safeerr.New("seed-demo: complete receipt verified; progress cleanup failed")
			}
		}
		return configuredDemoReport(r, "already_matched"), nil
	}

	for _, actor := range []struct {
		token      string
		permission identity.Permission
	}{{"local-admin", "access.manage"}, {"local-contributor", "resource.create"}, {"local-reviewer", "review.decide"}, {"local-publisher", "release.publish"}, {"local-reader", "resource.read"}} {
		self, e := demoExchange[struct {
			TenantID    string                `json:"tenantId"`
			Subject     string                `json:"subject"`
			Permissions []identity.Permission `json:"permissions"`
		}](ctx, c, "GET", "/api/v1/me", actor.token, "", "", nil, 200)
		if e != nil {
			return demoReport{}, e
		}
		if self.TenantID != demoTenant || self.Subject != actor.token || !slices.Contains(self.Permissions, actor.permission) {
			return demoReport{}, safeerr.New("seed-demo: required development identity/permission preflight failed")
		}
	}

	if _, err = demoExchange[map[string]any](ctx, c, "GET", "/api/v1/publication/snapshots?host="+url.QueryEscape(u.Hostname())+"&limit=1", "local-publisher", "", "", nil, 200); err != nil {
		return demoReport{}, err
	}

	p, resumed, err := readDemoProgress(state+".progress", target, origin)
	if err != nil {
		return demoReport{}, err
	}
	binding := demoPlanBinding(plan)
	if resumed && (p.PlanDigest != binding || !reflect.DeepEqual(p.Original, func() *demoReceipt {
		if exists {
			return &r
		}
		return nil
	}())) {
		return demoReport{}, safeerr.New("seed-demo: resume exactly the same YAML inputs, target and receipt")
	}
	p.PlanDigest = binding
	if !resumed {
		if exists {
			if err = verifyConfiguredDemo(ctx, c, r, plan, "active"); err != nil {
				return demoReport{}, err
			}
			p.Original = &r
		}
		if err = preflightConfiguredCollisions(ctx, c, p, plan); err != nil {
			return demoReport{}, err
		}
		if err = preflightDemoArchives(ctx, plan, origin, trustFile); err != nil {
			return demoReport{}, err
		}
		if err = p.save(); err != nil {
			return demoReport{}, err
		}
	}
	c.demoProgress = p
	p.dry = true
	_, err = populateConfiguredDemo(ctx, c, p, plan, u.Hostname())
	if err != nil && !errors.Is(err, errDemoCheckpoint) {
		return demoReport{}, err
	}
	if p.index < len(p.Steps)-1 {
		return demoReport{}, safeerr.New("seed-demo: progress is not a valid command prefix")
	}
	if len(p.Steps) > 0 && len(p.Steps[len(p.Steps)-1].Response) == 0 {
		step := &p.Steps[len(p.Steps)-1]
		recovered, found, e := reconcileDemoStep(ctx, c, *step)
		if e != nil {
			return demoReport{}, e
		}
		if found {
			step.Response, step.ResponseETag = recovered.Body, recovered.Header.Get("ETag")
			if err = p.save(); err != nil {
				return demoReport{}, err
			}
			p.index = 0
			_, err = populateConfiguredDemo(ctx, c, p, plan, u.Hostname())
			if err != nil && !errors.Is(err, errDemoCheckpoint) {
				return demoReport{}, err
			}
		}
	}
	if p.Original != nil {
		oldState := "active"
		for _, step := range p.Steps {
			if strings.HasSuffix(step.Path, ":activate") && len(step.Response) > 0 {
				oldState = "retired"
			}
		}
		if err = verifyConfiguredDemo(ctx, c, *p.Original, plan, oldState); err != nil {
			return demoReport{}, err
		}
	}
	if err = preflightConfiguredCollisions(ctx, c, p, plan); err != nil {
		return demoReport{}, err
	}
	if err = p.verifyCurrent(ctx, c); err != nil {
		return demoReport{}, err
	}
	if err = preflightDemoArchives(ctx, plan, origin, trustFile); err != nil {
		return demoReport{}, err
	}
	p.dry, p.index = false, 0
	result, err := populateConfiguredDemo(ctx, c, p, plan, u.Hostname())
	if err != nil {
		return demoReport{}, fmt.Errorf("seed-demo: incomplete; retain receipt/progress and rerun the same inputs: %w", err)
	}
	c.demoProgress = nil
	if err = verifyConfiguredDemo(ctx, c, result, plan, "active"); err != nil {
		return demoReport{}, err
	}
	if err = replaceDemoFile(state, result, original, exists); err != nil {
		return demoReport{}, err
	}
	if err = os.Remove(p.path); err != nil { // #nosec G703 -- remove only the exact locked private journal after successful API readback and atomic receipt replacement.
		return demoReport{}, safeerr.New("seed-demo: complete receipt saved; progress cleanup failed")
	}
	status := "created"
	if exists {
		status = "extended"
	} else if resumed {
		status = "resumed"
	}
	return configuredDemoReport(result, status), nil
}

func configuredDemoReport(r demoReceipt, status string) demoReport {
	result := demoReport{demoReceipt: r, Status: status}
	for _, item := range r.Items {
		switch item.State {
		case "published":
			result.Published++
		case "draft":
			result.Drafts++
		case "pending_review":
			result.PendingReview++
		}
	}
	return result
}

func preflightConfiguredCollisions(ctx context.Context, c *smokeHTTPClient, p *demoProgress, plan demodata.Plan) error {
	allowed := map[string]string{}
	if p.Original != nil {
		for _, item := range p.Original.Items {
			allowed[item.Identifier] = item.ResourceID
		}
		if p.Original.Bindings != nil {
			for key, id := range p.Original.Bindings.Namespaces {
				allowed["namespace:"+key] = id
			}
			for key, id := range p.Original.Bindings.Publishers {
				allowed["publisher:"+key] = id
			}
		} else {
			allowed["namespace:"+demoID] = p.Original.NamespaceID
			allowed["publisher:demo"] = p.Original.PublisherID
			if p.Original.PluginNamespaceID != "" {
				allowed["namespace:ide-plugins"] = p.Original.PluginNamespaceID
			}
		}
	}
	for _, step := range p.Steps {
		if len(step.Response) == 0 {
			continue
		}
		var body, result map[string]any
		_ = json.Unmarshal(step.Body, &body)
		_ = json.Unmarshal(step.Response, &result)
		switch step.Path {
		case "/api/v1/resources":
			allowed[fmt.Sprint(body["canonicalIdentifier"])] = fmt.Sprint(result["id"])
		case "/api/v1/namespaces":
			allowed["namespace:"+fmt.Sprint(body["slug"])] = fmt.Sprint(result["id"])
		case "/api/v1/publishers":
			allowed["publisher:"+fmt.Sprint(body["displayName"])] = fmt.Sprint(result["id"])
		}
	}
	capacity := len(plan.Namespaces) + len(plan.Publishers) + len(plan.Resources)
	steps := make([]demoStep, 0, capacity)
	bindings := make([]string, 0, capacity)
	for _, ns := range plan.Namespaces {
		raw, err := json.Marshal(map[string]string{"slug": ns.Key})
		if err != nil {
			return safeerr.New("seed-demo: invalid namespace preflight")
		}
		steps = append(steps, demoStep{Path: "/api/v1/namespaces", Body: raw})
		bindings = append(bindings, "namespace:"+ns.Key)
	}
	for _, pub := range plan.Publishers {
		raw, err := json.Marshal(map[string]string{"displayName": pub.DisplayName})
		if err != nil {
			return safeerr.New("seed-demo: invalid publisher preflight")
		}
		steps = append(steps, demoStep{Path: "/api/v1/publishers", Body: raw})
		bindings = append(bindings, "publisher:"+pub.Key)
		if allowed["publisher:"+pub.DisplayName] != "" {
			allowed["publisher:"+pub.Key] = allowed["publisher:"+pub.DisplayName]
		}
	}
	for _, item := range plan.Resources {
		raw, err := json.Marshal(map[string]string{"canonicalIdentifier": item.Resource.Identifier})
		if err != nil {
			return safeerr.New("seed-demo: invalid resource preflight")
		}
		steps = append(steps, demoStep{Path: "/api/v1/resources", Body: raw})
		bindings = append(bindings, item.Resource.Identifier)
	}
	for i, step := range steps {
		ids, err := demoStepIDs(ctx, c, step)
		if err != nil {
			return err
		}
		if len(ids) > 1 || len(ids) == 1 && ids[0] != allowed[bindings[i]] {
			return safeerr.New("seed-demo: unowned sample or parent collision; refusing writes")
		}
	}
	return nil
}

func populateConfiguredDemo(ctx context.Context, c *smokeHTTPClient, p *demoProgress, plan demodata.Plan, host string) (demoReceipt, error) {
	r := demoReceipt{Dataset: plan.Dataset, BaseURL: c.baseURL, TenantID: demoTenant, Bindings: &demoBindings{Format: 1, Inputs: map[string]string{}, Parents: map[string]string{}, Namespaces: map[string]string{}, Publishers: map[string]string{}}}
	for _, item := range plan.Resources {
		if item.Resource.Kind == catalog.KindPlugin {
			r.ArchiveOrigin = p.Origin
			break
		}
	}
	if p.Original != nil {
		r.Items = slices.Clone(p.Original.Items)
		if p.Original.Bindings != nil {
			r.Bindings.LegacyProfile = p.Original.Bindings.LegacyProfile
			for k, v := range p.Original.Bindings.Namespaces {
				r.Bindings.Namespaces[k] = v
			}
			for k, v := range p.Original.Bindings.Publishers {
				r.Bindings.Publishers[k] = v
			}
		} else {
			r.Bindings.Namespaces[demoID] = p.Original.NamespaceID
			r.Bindings.Publishers["demo"] = p.Original.PublisherID
			if p.Original.PluginNamespaceID != "" {
				r.Bindings.Namespaces["ide-plugins"] = p.Original.PluginNamespaceID
			}
			r.Bindings.LegacyProfile = "minimal"
			if p.Original.Dataset == expandedDemoID {
				r.Bindings.LegacyProfile = "expanded"
			}
		}
	}
	key := "sample-" + p.PlanDigest[7:23]
	for _, ns := range plan.Namespaces {
		r.Bindings.Parents["namespace:"+ns.Key] = demoHash(ns)
		if r.Bindings.Namespaces[ns.Key] != "" {
			continue
		}
		value, err := demoExchange[catalog.Namespace](ctx, c, "POST", "/api/v1/namespaces", "local-admin", key+"-ns-"+ns.Key, "", map[string]any{"slug": ns.Key, "displayName": ns.DisplayName}, 201)
		if err != nil {
			return r, err
		}
		r.Bindings.Namespaces[ns.Key] = value.ID
	}
	for _, pub := range plan.Publishers {
		r.Bindings.Parents["publisher:"+pub.Key] = demoHash(pub)
		if r.Bindings.Publishers[pub.Key] != "" {
			continue
		}
		value, etag, err := demoExchangeETag[catalog.Publisher](ctx, c, "POST", "/api/v1/publishers", "local-admin", key+"-pub-"+pub.Key, "", map[string]any{"displayName": pub.DisplayName, "domains": pub.Domains}, 201)
		if err != nil {
			return r, err
		}
		r.Bindings.Publishers[pub.Key] = value.ID
		for _, domain := range pub.Domains {
			if _, etag, err = demoExchangeETag[catalog.Publisher](ctx, c, "POST", "/api/v1/publishers/"+value.ID+"/publication-domains/"+domain+":approve", "local-admin", key+"-domain-"+pub.Key+"-"+domain, etag, map[string]any{"reason": "Approve the local sample publication domain"}, 200); err != nil {
				return r, err
			}
		}
	}
	r.NamespaceID = r.Bindings.Namespaces[plan.Namespaces[0].Key]
	r.PublisherID = r.Bindings.Publishers[plan.Publishers[0].Key]
	r.PluginNamespaceID = r.Bindings.Namespaces["ide-plugins"]
	// Refresh exactly retained Plugin revisions only during actual extension. No API
	// exposes the current outbound-policy digest; preflight verifies bytes/trust first.
	if p.Original != nil {
		for _, item := range p.Original.Items {
			if item.Kind != catalog.KindPlugin || item.State != "published" {
				continue
			}
			if _, err := demoExchange[map[string]any](ctx, c, "POST", "/api/v1/version-revisions/"+item.VersionRevisionID+":validate", "local-contributor", key+"-validate-"+item.Key, "", map[string]any{}, 201); err != nil {
				return r, err
			}
		}
	}
	for _, fixture := range configuredDemoFixtures(plan) {
		for _, prepared := range plan.Resources {
			if prepared.Resource.Key == fixture.Segment {
				r.Bindings.Inputs[fixture.Segment] = prepared.Digest
				break
			}
		}
		if slices.ContainsFunc(r.Items, func(item demoItem) bool { return item.Key == fixture.Segment }) {
			continue
		}
		scope := r
		scope.PublisherID = r.Bindings.Publishers[fixture.publisher]
		item, err := createDemoItem(ctx, c, scope, fixture)
		if err != nil {
			return r, fmt.Errorf("sample %s: %w", fixture.Segment, err)
		}
		r.Items = append(r.Items, item)
	}
	if !p.dry {
		if err := verifyConfiguredRecords(ctx, c, r, plan); err != nil {
			return r, err
		}
	}
	snapshot, etag, err := demoExchangeETag[publication.Snapshot](ctx, c, "POST", "/api/v1/publication/snapshots", "local-publisher", key+"-snapshot", "", map[string]any{"host": host}, 201)
	if err != nil {
		return r, err
	}
	r.SnapshotID = snapshot.ID
	if err = verifyConfiguredSnapshot(snapshot, r, plan); err != nil {
		return r, err
	}
	_, err = demoExchange[publication.Snapshot](ctx, c, "POST", "/api/v1/publication/snapshots/"+snapshot.ID+":activate", "local-publisher", key+"-activate", etag, map[string]any{"host": host, "reason": "Publish independently reviewed local samples"}, 200)
	return r, err
}

func verifyConfiguredRecords(ctx context.Context, c *smokeHTTPClient, r demoReceipt, plan demodata.Plan) error {
	if r.Bindings == nil {
		if r.Dataset == expandedDemoID {
			return verifyExpandedRecords(ctx, c, r)
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
		return nil
	}
	for _, ns := range plan.Namespaces {
		if id := r.Bindings.Namespaces[ns.Key]; id != "" {
			if err := demoExpect(ctx, c, "/api/v1/namespaces/"+id, map[string]any{"id": id, "tenantId": demoTenant, "slug": ns.Key, "displayName": ns.DisplayName, "revision": 1}); err != nil {
				return err
			}
		}
	}
	for _, pub := range plan.Publishers {
		if id := r.Bindings.Publishers[pub.Key]; id != "" {
			if err := demoExpect(ctx, c, "/api/v1/publishers/"+id, map[string]any{"id": id, "tenantId": demoTenant, "displayName": pub.DisplayName, "domains": pub.Domains, "approvedPublicationDomains": pub.Domains, "revision": 1 + len(pub.Domains)}); err != nil {
				return err
			}
		}
	}
	fixtures := configuredDemoFixtures(plan)
	for _, item := range r.Items {
		index := slices.IndexFunc(fixtures, func(f demoFixture) bool { return f.Segment == item.Key })
		if index < 0 {
			return safeerr.New("seed-demo: retained sample is missing")
		}
		f := fixtures[index]
		scope := r
		scope.PublisherID = r.Bindings.Publishers[f.publisher]
		if err := verifyDemoItem(ctx, c, scope, item, f); err != nil {
			return fmt.Errorf("sample %s: %w", item.Key, err)
		}
		if err := verifyDemoItemHistory(ctx, c, item); err != nil {
			return err
		}
	}
	return nil
}
func verifyConfiguredDemo(ctx context.Context, c *smokeHTTPClient, r demoReceipt, plan demodata.Plan, state string) error {
	if err := validateDemoReceipt(r, c.baseURL); err != nil {
		return err
	}
	if err := verifyConfiguredRecords(ctx, c, r, plan); err != nil {
		return err
	}
	u, _ := url.Parse(c.baseURL)
	snapshot, err := demoGet[publication.Snapshot](ctx, c, "/api/v1/publication/snapshots/"+r.SnapshotID+"?host="+url.QueryEscape(u.Hostname()))
	if err != nil {
		return err
	}
	if snapshot.ID != r.SnapshotID || string(snapshot.State) != state || snapshot.TenantID != demoTenant || snapshot.Host != u.Hostname() {
		return safeerr.New("seed-demo: owned snapshot state differs")
	}
	if err = verifyConfiguredSnapshot(snapshot, r, plan); err != nil {
		return err
	}
	if state == "active" {
		manifest, e := demoExchange[publication.ARDManifest](ctx, c, "GET", "/.well-known/ard.json", "", "", "", nil, 200)
		if e != nil {
			return e
		}
		ai, e := demoExchange[publication.AICatalog](ctx, c, "GET", "/.well-known/ai-catalog.json", "", "", "", nil, 200)
		if e != nil {
			return e
		}
		if !reflect.DeepEqual(manifest, snapshot.ARD) || !reflect.DeepEqual(ai, snapshot.AICatalog) {
			return safeerr.New("seed-demo: public documents differ from active snapshot")
		}
	}
	return verifyConfiguredSearch(ctx, c, r)
}

func preflightDemoArchives(ctx context.Context, plan demodata.Plan, origin, trustFile string) error {
	var archives []demodata.PreparedResource
	for _, item := range plan.Resources {
		if len(item.Archive) > 0 {
			archives = append(archives, item)
		}
	}
	if len(archives) == 0 {
		return nil
	}
	fail := safeerr.New("seed-demo: archive preflight failed; verify private trust, readiness and unchanged prepared bytes before any mutation")
	trust, err := remoteReadFile(trustFile, 1<<20)
	if err != nil {
		return fail
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(trust) {
		return fail
	}
	u, _ := url.Parse(origin)
	ip, err := netip.ParseAddr(u.Hostname())
	if err != nil {
		return fail
	}
	f, err := fetcher.New(fetcher.Policy{Timeout: 5 * time.Second, MaxResponseBytes: 64 << 10, MaxRedirects: 0, AllowTestAddresses: true, AllowedOrigins: map[string]struct{}{origin: {}}, AllowedAddressPrefixes: []netip.Prefix{netip.PrefixFrom(ip, ip.BitLen())}, RootCAs: roots}, nil)
	if err != nil {
		return fail
	}
	for _, item := range archives {
		result, err := f.Fetch(ctx, fetcher.Request{URL: origin + "/" + item.Resource.Key + ".zip", Method: http.MethodGet, Accept: []string{catalog.PluginMediaZIP}, IdentityEncoding: true, RejectRedirects: true})
		if err != nil || result.Status != 200 || result.MediaType != catalog.PluginMediaZIP || result.BodyDigest != item.BodyDigest || len(result.Body) != len(item.Archive) {
			return fail
		}
	}
	return nil
}

func demoWireDigest(f demoFixture) (string, error) {
	if f.bodyDigest != "" && (f.Kind == catalog.KindPlugin || f.Representation["packageData"] != nil) {
		return f.bodyDigest, nil
	}
	if encoded, ok := f.Representation["packageData"].(string); ok {
		raw, err := base64.StdEncoding.DecodeString(encoded)
		return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), err
	}
	if content, ok := f.Representation["content"].(string); ok {
		return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(content))), nil
	}
	raw, err := json.Marshal(f.Representation["data"])
	return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), err
}

// Inspected Skill archives have authenticated metadata/handoff, no public delivery URL.
func verifyConfiguredSnapshot(snapshot publication.Snapshot, r demoReceipt, plan demodata.Plan) error {
	for _, item := range r.Items {
		i := slices.IndexFunc(plan.Resources, func(p demodata.PreparedResource) bool { return p.Resource.Key == item.Key })
		if i < 0 {
			return safeerr.New("seed-demo: missing snapshot input")
		}
		packageOnly := len(plan.Resources[i].Representation.PackageData) > 0
		want := item.State == "published" && !packageOnly && plan.Resources[i].Resource.Listing.Visibility == catalog.VisibilityPublic
		ard := slices.ContainsFunc(snapshot.ARD.Entries, func(e publication.Entry) bool { return e.Identifier == item.Identifier })
		ai := slices.ContainsFunc(snapshot.AICatalog.Entries, func(e publication.AICatalogEntry) bool { return e.Identifier == item.Identifier })
		if ard != want || ai != want {
			return safeerr.New("seed-demo: snapshot delivery differs for sample: " + item.Key)
		}
		if item.State == "published" && packageOnly && plan.Resources[i].Resource.Listing.Visibility == catalog.VisibilityPublic && !slices.ContainsFunc(snapshot.Exclusions, func(e publication.Exclusion) bool {
			return e.ResourceID == item.ResourceID && slices.Contains(e.Reasons, "PUBLIC_DELIVERY_FORM_REQUIRED")
		}) {
			return safeerr.New("seed-demo: missing package-only exclusion")
		}
	}
	return nil
}
func verifyConfiguredSearch(ctx context.Context, c *smokeHTTPClient, r demoReceipt) error {
	found := map[string]bool{}
	cursor := ""
	// Read bounded pages, preserve unrelated releases and fail closed at the local inventory ceiling.
	for page := 0; page < 10; page++ {
		body := map[string]any{"limit": 20}
		if cursor != "" {
			body["cursor"] = cursor
		}
		response, err := demoExchange[catalog.CatalogSearchResponse](ctx, c, "POST", "/api/v1/catalog/search", "local-reader", "", "", body, 200)
		if err != nil {
			return err
		}
		if response.Partial {
			return safeerr.New("seed-demo: incomplete local discovery")
		}
		for _, result := range response.Items {
			for _, item := range r.Items {
				if result.Identifier == item.Identifier {
					if item.State != "published" || result.Kind != item.Kind || result.Local == nil || result.Local.Release.ID != item.ReleaseID || found[item.Key] {
						return safeerr.New("seed-demo: local discovery identity differs")
					}
					found[item.Key] = true
				}
			}
		}
		if response.NextCursor == "" {
			for _, item := range r.Items {
				if found[item.Key] != (item.State == "published") {
					return safeerr.New("seed-demo: local discovery is missing sample: " + item.Key)
				}
			}
			return nil
		}
		if response.NextCursor == cursor {
			return safeerr.New("seed-demo: repeated discovery cursor")
		}
		cursor = response.NextCursor
	}
	return safeerr.New("seed-demo: local inventory exceeds verification bound")
}
