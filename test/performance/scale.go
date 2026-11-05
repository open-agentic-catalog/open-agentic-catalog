// Scale profiles derive expected search pages and facets from the fixed corpus,
// independently of the server response measured by the load driver.

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	fixture "github.com/open-agentic-catalog/open-agentic-catalog/test/performance/scalefixture"
)

var scaleCases = []string{"all", "common", "local", "indexed", "rare", "absent", "exact-local", "exact-indexed", "kind", "plugin", "plugin-local", "plugin-indexed", "tag", "publisher", "facets", "wide", "later", "terminal", "publisher-asc-first", "publisher-asc-third", "publisher-desc-first", "publisher-desc-third"}

// The closed profile carries only bounded fixture inputs, never arbitrary
// methods, origins or an expected server response supplied by an operator.
type scaleProfile struct {
	Items     int    `json:"items"`
	Case      string `json:"case"`
	Publisher string `json:"publisher"`
	Cursor    string `json:"cursor,omitempty"`
}

// validate accepts only fixed dataset sizes/cases and the cases that require cursors.
func (s *scaleProfile) validate() error {
	if !fixture.ValidSize(s.Items) || !slices.Contains(scaleCases, s.Case) || !identity.ValidTenantID(s.Publisher) || len(s.Cursor) > catalog.MaxCatalogCursorBytes || (s.Cursor != "") != (s.Case == "later" || s.Case == "terminal" || strings.HasSuffix(s.Case, "-third")) {
		return errors.New("invalid-scale-case")
	}
	return nil
}

// query derives the request, expected match count and page offset from the corpus;
// the server's own totals never supply this correctness oracle.
func (s *scaleProfile) query() (catalog.SearchQuery, int, int) {
	l, x := fixture.Count(s.Items, false), fixture.Count(s.Items, true)
	q := catalog.SearchQuery{Limit: 20, Cursor: s.Cursor}
	total, offset := s.Items, 0
	switch s.Case {
	case "common":
		q.Text = "commonterm"
		total = l + x
	case "local", "later", "publisher-asc-first", "publisher-asc-third", "publisher-desc-first", "publisher-desc-third":
		q.Text = "Scale Local"
		if strings.HasPrefix(s.Case, "publisher-asc-") {
			q.Order = catalog.SearchOrderPublisherAsc
		}
		if strings.HasPrefix(s.Case, "publisher-desc-") {
			q.Order = catalog.SearchOrderPublisherDesc
		}
		total = l
		if (s.Case == "later" || strings.HasSuffix(s.Case, "-third")) && s.Cursor != "" {
			offset = 40
		}
	case "indexed":
		q.Text = "Scale Indexed"
		total = x
	case "rare":
		q.Text = "rareterm"
		total = (l+99)/100 + (x+99)/100
	case "absent":
		q.Text = "scaleabsentterm"
		total = 0
	case "exact-local":
		q.Text = fixture.Identifier(0, false)
		total = 1
	case "exact-indexed":
		q.Text = fixture.Identifier(0, true)
		total = 1
	case "kind":
		q.Text = "Scale"
		q.Kinds = []catalog.Kind{catalog.KindSkill}
		total = (l+1)/4 + (x+1)/4
	case "plugin", "plugin-local", "plugin-indexed":
		q.Text, q.Kinds = "Scale", []catalog.Kind{catalog.KindPlugin}
		total = l/4 + x/4
		if s.Case == "plugin-local" {
			q.Origins, total = []catalog.SearchResultType{catalog.SearchResultLocal}, l/4
		}
		if s.Case == "plugin-indexed" {
			q.Origins, total = []catalog.SearchResultType{catalog.SearchResultIndexed}, x/4
		}
	case "tag":
		q.Text = "Scale"
		q.Tags = []string{"group-1"}
		total = (l+3)/5 + (x+3)/5
	case "publisher":
		q.Publisher = s.Publisher
		total = l
	case "facets":
		q.Text = "Scale"
		total = l
	case "wide":
		q.Limit = 100
	case "terminal":
		q.Text = "Scale Local"
		q.Tags = []string{"terminal"}
		q.Limit = 5
		total = 7
		if s.Cursor != "" {
			offset = 5
		}
	}
	return q, total, offset
}

// request encodes the reviewed Search or Explore case without caller-selected routes.
func (s *scaleProfile) request() (string, []byte, error) {
	q, _, _ := s.query()
	path := "/api/v1/catalog/search"
	var value any = q
	if s.Case == "facets" {
		path = "/api/v1/catalog/explore"
		value = map[string]any{"query": map[string]string{"text": q.Text}, "facets": []any{map[string]any{"field": "kind", "limit": 2}, map[string]any{"field": "tag", "limit": 2}}}
	}
	raw, err := json.Marshal(value)
	return path, raw, err
}

// decodeScale rejects duplicate/unknown members and trailing JSON before assertions.
func decodeScale(body []byte, value any) error {
	if err := jsondoc.Validate(body); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing scale response")
	}
	return nil
}

// validateScale checks fixture-derived counts, filters, ordering, tenant scope and
// continuation semantics; facet expectations are computed independently of SQL.
func validateScale(body []byte, s *scaleProfile) (int, error) {
	q, total, offset := s.query()
	if s.Case == "facets" {
		var v catalog.ExploreResponse
		if err := decodeScale(body, &v); err != nil {
			return 0, err
		}
		if v.Matched != total || len(v.Facets) != 2 {
			return 0, errors.New("invalid scale facets")
		}
		// Compute the small bucket oracle from fixture indices, independently
		// of SQL ranking/counting. This runs outside the timed request loop.
		expected := map[string]map[string]int{"kind": {}, "tag": {}}
		for i := 0; i < total; i++ {
			expected["kind"][string(fixture.Kind(i))]++
			for _, tag := range fixture.Tags(i) {
				expected["tag"][tag]++
			}
		}
		for field, counts := range expected {
			buckets := make([]catalog.FacetBucket, 0, len(counts))
			sum := 0
			for value, count := range counts {
				buckets = append(buckets, catalog.FacetBucket{Value: value, Count: count})
				sum += count
			}
			slices.SortFunc(buckets, func(a, b catalog.FacetBucket) int {
				if a.Count != b.Count {
					return b.Count - a.Count
				}
				return strings.Compare(a.Value, b.Value)
			})
			actual := v.Facets[field]
			if !slices.Equal(actual.Buckets, buckets[:2]) || actual.OtherCount != sum-buckets[0].Count-buckets[1].Count {
				return 0, errors.New("wrong scale facet counts")
			}
		}
		return v.Matched, nil
	}
	var page catalog.CatalogSearchResponse
	if err := decodeScale(body, &page); err != nil {
		return 0, err
	}
	if page.Partial || page.LiveCount != 0 || page.Generation == "" || page.Profile != catalog.CoreSearchProfile || page.Tier != "core" || page.DurableTotal != total || len(page.Items) != min(q.Limit, max(0, total-offset)) || (page.NextCursor != "") != (total > offset+q.Limit) || len(page.NextCursor) > catalog.MaxCatalogCursorBytes {
		return 0, errors.New("wrong scale page")
	}
	var expectedType catalog.SearchResultType
	switch s.Case {
	case "local", "exact-local", "later", "terminal", "plugin-local", "publisher-asc-first", "publisher-asc-third", "publisher-desc-first", "publisher-desc-third":
		expectedType = catalog.SearchResultLocal
	case "indexed", "exact-indexed", "plugin-indexed":
		expectedType = catalog.SearchResultIndexed
	}
	seen := map[string]bool{}
	for position, item := range page.Items {
		if seen[item.ID] || item.ID == "" || item.Live != nil {
			return 0, errors.New("invalid scale identity")
		}
		seen[item.ID] = true
		if expectedType != "" && item.Type != expectedType {
			return 0, errors.New("wrong scale result population")
		}
		if position > 0 {
			previous := page.Items[position-1]
			left, right := catalog.CatalogSearchSortName(previous.DisplayName), catalog.CatalogSearchSortName(item.DisplayName)
			if previous.Score < item.Score || previous.Score == item.Score && (left > right || left == right && previous.ID >= item.ID) {
				return 0, errors.New("wrong scale result ordering")
			}
		}
		if s.Case == "kind" && item.Kind != catalog.KindSkill || s.Case == "tag" && !slices.Contains(item.Tags, "group-1") || (s.Case == "rare" || s.Case == "common") && !strings.Contains(item.Description, q.Text) {
			return 0, errors.New("wrong scale filter match")
		}
		if strings.HasPrefix(s.Case, "plugin") && item.Kind != catalog.KindPlugin {
			return 0, errors.New("wrong scale Plugin match")
		}
		if s.Case == "publisher" && (item.Local == nil || item.Publisher != s.Publisher) {
			return 0, errors.New("wrong scale publisher match")
		}
		if (s.Case == "later" || s.Case == "terminal" || strings.HasSuffix(s.Case, "-third") || s.Case == "local" || s.Case == "indexed" || q.Order != "") && item.Identifier != fixture.Identifier(offset+position, s.Case == "indexed") {
			return 0, errors.New("wrong scale order or continuation")
		}
		if q.Order != "" && (item.Publisher != s.Publisher || item.Local == nil || item.Local.Display == nil || item.Local.Display.PublisherName != fixture.Publisher) {
			return 0, errors.New("wrong scale publisher sort population")
		}
		if item.Type == catalog.SearchResultLocal {
			if item.Local == nil || item.Indexed != nil || item.Local.Resource.TenantID != fixture.Tenant {
				return 0, errors.New("scale tenant violation")
			}
		} else {
			if item.Type != catalog.SearchResultIndexed || item.Indexed == nil || item.Local != nil || item.Indexed.SourceCount != scaleSources(item.Identifier) || len(item.Indexed.SourceIDs) != scaleSources(item.Identifier) || item.Indexed.Conflict || item.Indexed.SameSourceConflict || item.Indexed.SourcesTruncated {
				return 0, errors.New("invalid scale provenance")
			}
			for index, source := range item.Indexed.SourceIDs {
				if source == "" || slices.Contains(item.Indexed.SourceIDs[:index], source) {
					return 0, errors.New("invalid scale provenance")
				}
			}
		}
		if s.Case == "exact-local" && item.Identifier != fixture.Identifier(0, false) || s.Case == "exact-indexed" && item.Identifier != fixture.Identifier(0, true) {
			return 0, errors.New("wrong exact identity")
		}
	}
	return len(page.Items), nil
}

// Only the expiring opaque cursor is excluded. Every result field, generation,
// total and continuation shape still participates in static phase comparison.
func scaleDigest(body []byte, s *scaleProfile) ([32]byte, error) {
	if s.Case == "facets" {
		return sha256.Sum256(body), nil
	}
	var page catalog.CatalogSearchResponse
	if err := decodeScale(body, &page); err != nil {
		return [32]byte{}, err
	}
	q, total, offset := s.query()
	if page.DurableTotal != total || len(page.Items) != min(q.Limit, max(0, total-offset)) || (page.NextCursor != "") != (total > offset+q.Limit) || len(page.NextCursor) > catalog.MaxCatalogCursorBytes {
		return [32]byte{}, errors.New("wrong scale continuation")
	}
	page.NextCursor = ""
	raw, err := json.Marshal(page)
	return sha256.Sum256(raw), err
}

// Only the first seven synthetic indexed identities have a duplicate source.
func scaleSources(identifier string) int {
	const prefix = "urn:air:scale.example:scale:indexed-"
	if strings.HasPrefix(identifier, prefix) {
		n, err := strconv.Atoi(strings.TrimPrefix(identifier, prefix))
		if err == nil && n >= 0 && n < 7 && identifier == fixture.Identifier(n, true) {
			return 2
		}
	}
	return 1
}
