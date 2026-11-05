package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

const demoProgressLimit = 32 << 20

var errDemoCheckpoint = errors.New("demo checkpoint")

type demoStep struct {
	Path         string          `json:"path"`
	Actor        string          `json:"actor"`
	Key          string          `json:"key"`
	ETag         string          `json:"etag"`
	Body         json.RawMessage `json:"body"`
	Status       int             `json:"status"`
	Response     json.RawMessage `json:"response,omitempty"`
	ResponseETag string          `json:"responseETag,omitempty"`
	Before       []string        `json:"before,omitempty"`
}

// One persisted intent precedes each command; uncertain outcomes use authoritative reads.
type demoProgress struct {
	PlanDigest string       `json:"planDigest,omitempty"`
	Dataset    string       `json:"dataset"`
	BaseURL    string       `json:"baseUrl"`
	Origin     string       `json:"archiveOrigin"`
	Original   *demoReceipt `json:"original,omitempty"`
	Steps      []demoStep   `json:"steps"`
	path       string
	index      int
	dry        bool
	diskBytes  []byte
}

func readDemoProgress(path, baseURL, origin string) (*demoProgress, bool, error) {
	p := &demoProgress{Dataset: expandedDemoID, BaseURL: baseURL, Origin: origin, path: path, Steps: []demoStep{}}
	info, err := os.Lstat(path) // #nosec G703 -- exact operator-selected adjacent journal; require regular private bounded file before decoding.
	if errors.Is(err, os.ErrNotExist) {
		return p, false, nil
	}
	fail := safeerr.New("seed-demo: invalid or mismatched private progress file")
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > demoProgressLimit {
		return nil, false, fail
	}
	raw, err := remoteReadFile(path, demoProgressLimit)
	p = &demoProgress{}
	if err != nil || jsondoc.Decode(raw, p) != nil || p.Dataset != expandedDemoID || p.BaseURL != baseURL || p.Origin != origin || p.Steps == nil || len(p.Steps) > 600 {
		return nil, false, fail
	}
	if p.PlanDigest != "" && !demoDigestValid(p.PlanDigest) {
		return nil, false, fail
	}
	if p.Original != nil && ((p.PlanDigest == "" && p.Original.Dataset != demoID) || validateDemoReceipt(*p.Original, baseURL) != nil) {
		return nil, false, fail
	}
	seen := map[string]bool{}
	for i, s := range p.Steps {
		if s.Key == "" || seen[s.Key] || jsondoc.Validate(s.Body) != nil || (len(s.Response) == 0 && i != len(p.Steps)-1) || (len(s.Response) != 0 && jsondoc.Validate(s.Response) != nil) {
			return nil, false, fail
		}
		seen[s.Key] = true
	}
	p.path = path
	p.diskBytes = append([]byte(nil), raw...)
	return p, true, nil
}

// Exact previous bytes prevent replacing a changed receipt. Invocation locking serializes writers.
func replaceDemoFile(path string, value any, previous []byte, replace bool) error {
	fail := safeerr.New("seed-demo: cannot safely save private demo state; retain the progress file")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil { // #nosec G703 -- explicit operator-selected local receipt directory, never an API or document path.
		return fail
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".demo-state-*")
	if err != nil {
		return fail
	}
	defer func() { // #nosec G703 -- remove only the securely generated temporary file owned by this invocation.
		_ = f.Close()
		_ = os.Remove(f.Name()) // #nosec G703 -- only the securely generated temporary file owned by this invocation.
	}()
	if err = json.NewEncoder(f).Encode(value); err != nil {
		return fail
	}
	if err = f.Sync(); err != nil {
		return fail
	}
	if err = f.Close(); err != nil {
		return fail
	}
	if !replace {
		if err = os.Link(f.Name(), path); err != nil {
			return fail
		}
	} else {
		info, e := os.Lstat(path) // #nosec G703 -- exact operator-selected receipt path; type, mode and previous bytes checked before replacement.
		if e != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return fail
		}
		actual, e := remoteReadFile(path, demoProgressLimit)
		if e != nil || !bytes.Equal(actual, previous) {
			return fail
		}
		if err = os.Rename(f.Name(), path); err != nil { // #nosec G703 -- generated private temporary file replaces only the byte-matched explicit receipt.
			return fail
		}
	}
	dir, err := os.Open(filepath.Dir(path)) // #nosec G703 -- directory fsync for the explicitly selected operator receipt path.
	if err != nil {
		return fail
	}
	defer func() { _ = dir.Close() }()
	if err = dir.Sync(); err != nil {
		return fail
	}
	return nil
}

func (p *demoProgress) save() error {
	raw, err := json.Marshal(p)
	if err != nil || len(raw)+1 > demoProgressLimit {
		return safeerr.New("seed-demo: progress exceeds its bound")
	}
	if err = replaceDemoFile(p.path, p, p.diskBytes, p.diskBytes != nil); err != nil {
		return err
	}
	raw = append(raw, '\n')
	p.diskBytes = raw
	return nil
}

func (p *demoProgress) command(ctx context.Context, c *smokeHTTPClient, path, actor, key, etag string, body any, status int) (smokeHTTPResponse, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return smokeHTTPResponse{}, err
	}
	step := demoStep{Path: path, Actor: actor, Key: key, ETag: etag, Body: raw, Status: status}
	if p.index < len(p.Steps) {
		old := &p.Steps[p.index]
		if old.Path != path || old.Actor != actor || old.Key != key || old.ETag != etag || old.Status != status || !bytes.Equal(old.Body, raw) {
			return smokeHTTPResponse{}, safeerr.New("seed-demo: progress differs from the fixed command plan")
		}
		if len(old.Response) == 0 {
			if p.dry {
				return smokeHTTPResponse{}, errDemoCheckpoint
			}
			recovered, found, e := reconcileDemoStep(ctx, c, *old)
			if e != nil {
				return smokeHTTPResponse{}, e
			}
			if found {
				old.Response, old.ResponseETag = recovered.Body, recovered.Header.Get("ETag")
				if e = p.save(); e != nil {
					return smokeHTTPResponse{}, e
				}
			}
		}
		if len(old.Response) != 0 {
			if err := validateDemoStepResponse(*old); err != nil {
				return smokeHTTPResponse{}, err
			}
			p.index++
			return smokeHTTPResponse{Body: old.Response, Header: http.Header{"Etag": {old.ResponseETag}}}, nil
		}
	} else {
		if p.dry {
			return smokeHTTPResponse{}, errDemoCheckpoint
		}
		step.Before, err = demoStepIDs(ctx, c, step)
		if err != nil {
			return smokeHTTPResponse{}, err
		}
		if demoStepCreation(step) && len(step.Before) != 0 && path != "/api/v1/publication/snapshots" {
			return smokeHTTPResponse{}, safeerr.New("seed-demo: fixed fixture collides with existing content")
		}
		p.Steps = append(p.Steps, step)
		if err = p.save(); err != nil {
			return smokeHTTPResponse{}, err
		}
	}
	response, err := c.request(ctx, "demo command", http.MethodPost, path, actor, key+"-"+fmt.Sprintf("%x", sha256.Sum256(append([]byte(path+actor+etag), raw...)))[:16], etag, body, status)
	if err != nil {
		return smokeHTTPResponse{}, err
	}
	if jsondoc.Validate(response.Body) != nil {
		return smokeHTTPResponse{}, safeerr.New("seed-demo: invalid API command response; reconcile retained progress on rerun")
	}
	current := &p.Steps[p.index]
	current.Response, current.ResponseETag = response.Body, response.Header.Get("ETag")
	if err = validateDemoStepResponse(*current); err != nil {
		current.Response = nil
		return smokeHTTPResponse{}, err
	}
	if err = p.save(); err != nil {
		return smokeHTTPResponse{}, err
	}
	p.index++
	return response, nil
}

func demoStepCreation(s demoStep) bool {
	return !strings.HasSuffix(s.Path, ":validate") && !strings.Contains(s.Path, ":approve") && !strings.Contains(s.Path, ":publish") && !strings.Contains(s.Path, ":activate")
}

func demoStepCollection(s demoStep) (string, error) {
	var b map[string]any
	if json.Unmarshal(s.Body, &b) != nil {
		return "", safeerr.New("seed-demo: invalid intent")
	}
	path := s.Path
	switch {
	case path == "/api/v1/namespaces", path == "/api/v1/publishers", path == "/api/v1/resources":
		return path, nil
	case path == "/api/v1/catalog-listings":
		id, _ := b["resourceId"].(string)
		if !identity.ValidTenantID(id) {
			break
		}
		return "/api/v1/resources/" + id + "/listings", nil
	case path == "/api/v1/publication/snapshots":
		host, _ := b["host"].(string)
		return path + "?host=" + url.QueryEscape(host), nil
	case strings.HasSuffix(path, "/versions"), strings.HasSuffix(path, "/revisions"):
		return path, nil
	case strings.HasSuffix(path, "/submissions"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/api/v1/catalog-listings/"), "/submissions")
		if !identity.ValidTenantID(id) {
			break
		}
		return "/api/v1/review-submissions?listingId=" + id, nil
	}
	return "", safeerr.New("seed-demo: unsupported fixed demo intent")
}

func demoStepIDs(ctx context.Context, c *smokeHTTPClient, s demoStep) ([]string, error) {
	if !demoStepCreation(s) {
		return nil, nil
	}
	path, err := demoStepCollection(s)
	if err != nil {
		return nil, err
	}
	ids, err := demoPageIDs(ctx, c, path, 200)
	if err != nil {
		return nil, err
	}
	if s.Path == "/api/v1/publication/snapshots" || strings.HasSuffix(s.Path, "/versions") || strings.HasSuffix(s.Path, "/revisions") || strings.HasSuffix(s.Path, "/submissions") || s.Path == "/api/v1/catalog-listings" {
		return ids, nil
	}
	var body map[string]any
	if err := json.Unmarshal(s.Body, &body); err != nil {
		return nil, err
	}
	field := "canonicalIdentifier"
	if s.Path == "/api/v1/namespaces" {
		field = "slug"
	}
	if s.Path == "/api/v1/publishers" {
		field = "displayName"
	}
	var matches []string
	for _, id := range ids {
		object, e := demoGet[map[string]any](ctx, c, s.Path+"/"+id)
		if e != nil {
			return nil, e
		}
		if object[field] == body[field] {
			matches = append(matches, id)
		}
	}
	return matches, nil
}

func reconcileDemoStep(ctx context.Context, c *smokeHTTPClient, s demoStep) (smokeHTTPResponse, bool, error) {
	var body map[string]any
	if err := json.Unmarshal(s.Body, &body); err != nil {
		return smokeHTTPResponse{}, false, err
	}
	if strings.HasSuffix(s.Path, ":validate") {
		return smokeHTTPResponse{}, false, nil
	}
	if !demoStepCreation(s) {
		path := strings.SplitN(s.Path, ":", 2)[0]
		if strings.Contains(path, "/publication-domains/") {
			path = strings.SplitN(path, "/publication-domains/", 2)[0]
		}
		if strings.HasSuffix(s.Path, ":activate") {
			path += "?host=" + url.QueryEscape(fmt.Sprint(body["host"]))
		}
		object, etag, err := demoExchangeETag[map[string]any](ctx, c, http.MethodGet, path, "local-admin", "", "", nil, http.StatusOK)
		if err != nil {
			return smokeHTTPResponse{}, false, err
		}
		matched := object["state"] == "approved" || object["state"] == "active"
		if strings.Contains(s.Path, "/publication-domains/") {
			domains, _ := object["approvedPublicationDomains"].([]any)
			domain := strings.TrimSuffix(strings.SplitN(s.Path, "/publication-domains/", 2)[1], ":approve")
			matched = slices.Contains(domains, any(domain))
		}
		if strings.HasSuffix(s.Path, ":publish") {
			id, _ := object["currentReleaseId"].(string)
			if id == "" {
				return smokeHTTPResponse{}, false, nil
			}
			release, e := demoGet[map[string]any](ctx, c, "/api/v1/listing-releases/"+id)
			if e != nil {
				return smokeHTTPResponse{}, false, e
			}
			if release["submissionId"] != body["submissionId"] || release["state"] != "published" {
				return smokeHTTPResponse{}, false, safeerr.New("seed-demo: uncertain release differs")
			}
			return demoStepResponse(map[string]any{"listing": object, "release": release}, etag)
		}
		if !matched {
			return smokeHTTPResponse{}, false, nil
		}
		if strings.HasPrefix(s.Path, "/api/v1/review-submissions/") {
			revision, e := demoGet[map[string]any](ctx, c, "/api/v1/version-revisions/"+fmt.Sprint(object["versionRevisionId"]))
			if e != nil {
				return smokeHTTPResponse{}, false, e
			}
			rev, _ := revision["revision"].(map[string]any)
			version, e := demoGet[map[string]any](ctx, c, "/api/v1/resource-versions/"+fmt.Sprint(rev["resourceVersionId"]))
			if e != nil {
				return smokeHTTPResponse{}, false, e
			}
			return demoStepResponse(map[string]any{"submission": object, "version": version}, etag)
		}
		return demoStepResponse(object, etag)
	}
	ids, err := demoStepIDs(ctx, c, s)
	if err != nil {
		return smokeHTTPResponse{}, false, err
	}
	var added []string
	for _, id := range ids {
		if !slices.Contains(s.Before, id) {
			added = append(added, id)
		}
	}
	if len(added) == 0 {
		return smokeHTTPResponse{}, false, nil
	}
	if len(added) != 1 {
		return smokeHTTPResponse{}, false, safeerr.New("seed-demo: ambiguous command outcome; refusing adoption")
	}
	id := added[0]
	path := s.Path + "/" + id
	switch {
	case strings.HasSuffix(s.Path, "/versions"):
		version, etag, e := demoExchangeETag[map[string]any](ctx, c, http.MethodGet, "/api/v1/resource-versions/"+id, "local-admin", "", "", nil, http.StatusOK)
		if e != nil {
			return smokeHTTPResponse{}, false, e
		}
		revisionID, _ := version["candidateRevisionId"].(string)
		revision, e := demoGet[map[string]any](ctx, c, "/api/v1/version-revisions/"+revisionID)
		if e != nil {
			return smokeHTTPResponse{}, false, e
		}
		if version["version"] != body["version"] {
			return smokeHTTPResponse{}, false, safeerr.New("seed-demo: uncertain version differs")
		}
		return demoStepResponse(map[string]any{"version": version, "revision": revision["revision"]}, etag)
	case strings.HasSuffix(s.Path, "/revisions"):
		revision, e := demoGet[map[string]any](ctx, c, "/api/v1/listing-revisions/"+id)
		if e != nil {
			return smokeHTTPResponse{}, false, e
		}
		for key, value := range body {
			if !reflect.DeepEqual(revision[key], value) {
				return smokeHTTPResponse{}, false, safeerr.New("seed-demo: uncertain listing revision differs")
			}
		}
		listing, etag, e := demoExchangeETag[map[string]any](ctx, c, http.MethodGet, strings.TrimSuffix(s.Path, "/revisions"), "local-admin", "", "", nil, http.StatusOK)
		if e != nil {
			return smokeHTTPResponse{}, false, e
		}
		return demoStepResponse(map[string]any{"listing": listing, "revision": revision}, etag)
	case strings.HasSuffix(s.Path, "/submissions"):
		path = "/api/v1/review-submissions/" + id
	case s.Path == "/api/v1/publication/snapshots":
		path += "?host=" + url.QueryEscape(fmt.Sprint(body["host"]))
	}
	object, etag, err := demoExchangeETag[map[string]any](ctx, c, http.MethodGet, path, "local-admin", "", "", nil, http.StatusOK)
	if err != nil {
		return smokeHTTPResponse{}, false, err
	}
	for key, value := range body {
		if !reflect.DeepEqual(object[key], value) {
			return smokeHTTPResponse{}, false, safeerr.New("seed-demo: uncertain created record differs")
		}
	}
	return demoStepResponse(object, etag)
}

func demoStepResponse(value any, etag string) (smokeHTTPResponse, bool, error) {
	raw, err := json.Marshal(value)
	return smokeHTTPResponse{Body: raw, Header: http.Header{"Etag": {etag}}}, err == nil, err
}

// Completed responses provide lookup hints. Subsequent owned transitions supersede pointers.
func (p *demoProgress) verifyCurrent(ctx context.Context, c *smokeHTTPClient) error {
	projections := map[string]map[string]any{}
	add := func(path string, value any) {
		if object, ok := value.(map[string]any); ok {
			projections[path] = object
		}
	}
	for _, s := range p.Steps {
		if len(s.Response) == 0 {
			continue
		}
		var object map[string]any
		if json.Unmarshal(s.Response, &object) != nil {
			return safeerr.New("seed-demo: invalid recorded response")
		}
		id, _ := object["id"].(string)
		switch {
		case s.Path == "/api/v1/namespaces", s.Path == "/api/v1/publishers", s.Path == "/api/v1/resources", s.Path == "/api/v1/catalog-listings":
			add(s.Path+"/"+id, object)
		case strings.Contains(s.Path, "/publication-domains/"):
			add(strings.SplitN(s.Path, "/publication-domains/", 2)[0], object)
		case strings.HasSuffix(s.Path, "/versions"):
			v, _ := object["version"].(map[string]any)
			rev, _ := object["revision"].(map[string]any)
			add("/api/v1/resource-versions/"+fmt.Sprint(v["id"]), v)
			add("/api/v1/version-revisions/"+fmt.Sprint(rev["id"]), map[string]any{"revision": rev})
		case strings.HasSuffix(s.Path, "/revisions"):
			add(strings.TrimSuffix(s.Path, "/revisions"), object["listing"])
			rev, _ := object["revision"].(map[string]any)
			add("/api/v1/listing-revisions/"+fmt.Sprint(rev["id"]), rev)
		case strings.HasSuffix(s.Path, "/submissions"):
			add("/api/v1/review-submissions/"+id, object)
		case strings.HasSuffix(s.Path, ":validate"):
			add(strings.TrimSuffix(s.Path, ":validate"), map[string]any{"revision": object["revision"]})
		case strings.HasSuffix(s.Path, ":approve"):
			add(strings.TrimSuffix(s.Path, ":approve"), object["submission"])
			version, _ := object["version"].(map[string]any)
			add("/api/v1/resource-versions/"+fmt.Sprint(version["id"]), version)
		case strings.HasSuffix(s.Path, ":publish"):
			add(strings.TrimSuffix(s.Path, ":publish"), object["listing"])
			release, _ := object["release"].(map[string]any)
			add("/api/v1/listing-releases/"+fmt.Sprint(release["id"]), release)
		case s.Path == "/api/v1/publication/snapshots", strings.HasSuffix(s.Path, ":activate"):
			add("/api/v1/publication/snapshots/"+id+"?host="+url.QueryEscape(fmt.Sprint(object["host"])), object)
		default:
			return safeerr.New("seed-demo: invalid recorded operation")
		}
	}
	for path, expected := range projections {
		if err := demoExpect(ctx, c, path, expected); err != nil {
			return safeerr.New("seed-demo: owned current record differs; refusing writes")
		}
	}
	return nil
}
