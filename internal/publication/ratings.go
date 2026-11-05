package publication

import (
	"context"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/ratings"
	"net/url"
	"time"
)

func (m *Manager) sharedRatings(ctx context.Context, tenant string, records []catalog.CatalogRecord, measured time.Time) (map[string]*ratings.Summary, error) {
	out := map[string]*ratings.Summary{}
	if !m.ratingsSharingEnabled {
		return out, nil
	}
	choice, err := m.ratings.GetSharing(ctx, tenant, m.host, string(m.audience))
	if catalog.ErrorCodeOf(err) == catalog.CodeNotFound {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	if !choice.Enabled {
		return out, nil
	}
	targets := make([]ratings.Target, len(records))
	for i, record := range records {
		targets[i] = publicationRatingTarget(record)
	}
	totals, err := m.ratings.Aggregates(ctx, tenant, targets)
	if err != nil {
		return nil, err
	}
	for _, target := range targets {
		aggregate := totals[ratings.Key(target)]
		if !aggregate.Valid() {
			return nil, catalog.E(catalog.CodeInternal, "invalid rating sharing aggregate")
		}
		if aggregate.SharedCount < int64(m.ratingsMinimumSharedCount) {
			continue
		}
		origin, err := url.Parse(m.baseURL)
		if err != nil || origin.Hostname() != m.host {
			return nil, catalog.E(catalog.CodePublicationNotReady, "rating reporter does not match publication host")
		}
		out[ratings.Key(target)] = &ratings.Summary{Reporter: "https://" + origin.Host, Identifier: target.Identifier, VersionPresent: true, Version: target.Version, Minimum: 1, Maximum: 5, Count: aggregate.SharedCount, Sum: aggregate.SharedSum, MeasuredAt: measured.UTC()}
	}
	return out, nil
}
func publicationRatingTarget(r catalog.CatalogRecord) ratings.Target {
	return ratings.Target{Origin: "local", ResourceID: r.Resource.ID, VersionRevisionID: r.VersionRevision.ID, Identifier: r.Resource.CanonicalIdentifier, Version: r.Version.Version, VersionPresent: true, Kind: r.Resource.Kind}
}
func attachRatingSummary(entry *Entry, summary *ratings.Summary) {
	if summary == nil {
		return
	}
	entry.Ratings = summary
	contexts := []any{entry.Context}
	if existing, ok := entry.Context.([]any); ok {
		contexts = append([]any(nil), existing...)
	}
	contexts = append(contexts, map[string]any{ratings.IRI: map[string]any{"@id": ratings.IRI, "@type": "@json"}})
	entry.Context = contexts
}
