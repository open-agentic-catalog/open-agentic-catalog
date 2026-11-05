// Semantic primitives define the reviewed metadata projection and deterministic
// rank fusion. Embeddings are derived search data, never evidence of capability,
// authorization, trust or permission to execute a resource.
package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// SemanticTemplate versions the vector input independently of ranking settings.
	SemanticTemplate = "catalog-metadata-v1"
	// SemanticRRFConstant smooths rank fusion without mixing raw lexical/cosine scores.
	SemanticRRFConstant = 60
	// SemanticMaxInputBytes bounds one metadata projection before provider prefixing.
	SemanticMaxInputBytes = 32768
)

// SemanticDocument includes only deliberately searchable metadata, not identity
// claims, provider URLs, artifacts, feedback, approval or private context maps.
type SemanticDocument struct {
	DisplayName           string   `json:"name"`
	Description           string   `json:"description"`
	Tags                  []string `json:"tags"`
	RepresentativeQueries []string `json:"queries"`
}

// Projection returns the evaluated, bounded text template and its digest without truncating fields.
// String ordering inside fields remains meaningful; tag order is canonicalized.
func (d SemanticDocument) Projection() (string, string, error) {
	// A schema-valid 160-character name can occupy 640 UTF-8 bytes.
	if len(d.DisplayName) > 160*utf8.UTFMax || len(d.Description) > 16000 || len(d.Tags) > 100 || len(d.RepresentativeQueries) > 5 {
		return "", "", errors.New("semantic metadata exceeds its bounds")
	}
	for _, value := range append(append([]string{d.DisplayName, d.Description}, d.Tags...), d.RepresentativeQueries...) {
		if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return "", "", errors.New("semantic metadata contains invalid text")
		}
	}
	if strings.TrimSpace(d.DisplayName) == "" && strings.TrimSpace(d.Description) == "" {
		return "", "", errors.New("semantic metadata is empty")
	}
	d.Tags = append([]string{}, d.Tags...)
	sort.Strings(d.Tags)
	d.Tags = slices.Compact(d.Tags)
	d.RepresentativeQueries = append([]string{}, d.RepresentativeQueries...)
	for _, tag := range d.Tags {
		if len(tag) > 320 {
			return "", "", errors.New("semantic metadata tag exceeds its bound")
		}
	}
	for _, query := range d.RepresentativeQueries {
		if len(query) > 960 {
			return "", "", errors.New("semantic representative query exceeds its bound")
		}
	}
	text := strings.Join([]string{
		"Name: " + strings.TrimSpace(d.DisplayName),
		"Description: " + strings.TrimSpace(d.Description),
		"Tags: " + strings.Join(d.Tags, ", "),
		"Representative queries: " + strings.Join(d.RepresentativeQueries, "; "),
	}, "\n")
	if len(text) > SemanticMaxInputBytes {
		return "", "", errors.New("semantic metadata projection exceeds its bound")
	}
	sum := sha256.Sum256([]byte(text))
	return text, "sha256:" + hex.EncodeToString(sum[:]), nil
}

// HybridCandidate holds one already-authorized, deduplicated search identity.
// Similarity is absent for missing or ineligible derived vectors.
type HybridCandidate struct {
	ID           string
	SortName     string
	LexicalScore int
	Similarity   *float64
	Exact        bool
}

// HybridRank separates stable high-precision pagination from the public 0-100 score.
type HybridRank struct {
	ID    string
	Rank  int64
	Score int
}

// RankHybrid matches lexical hits OR above-cutoff semantic hits, then fuses their
// independently ordered ranks. Callers must apply filters and identity resolution
// before ranking, and use the complete matched set for totals and facets.
func RankHybrid(candidates []HybridCandidate, cutoff float64) ([]HybridRank, error) {
	if math.IsNaN(cutoff) || math.IsInf(cutoff, 0) || cutoff < -1 || cutoff > 1 {
		return nil, errors.New("invalid semantic cutoff")
	}
	byID := make(map[string]HybridCandidate, len(candidates))
	lexical := make([]HybridCandidate, 0)
	semantic := make([]HybridCandidate, 0)
	for _, c := range candidates {
		if c.ID == "" {
			return nil, errors.New("hybrid identity is empty")
		}
		if _, exists := byID[c.ID]; exists {
			return nil, errors.New("duplicate hybrid identity")
		}
		byID[c.ID] = c
		if c.LexicalScore < 0 || c.LexicalScore > 100 {
			return nil, errors.New("invalid lexical score")
		}
		if c.Exact {
			c.LexicalScore = 100
		}
		if c.LexicalScore > 0 || c.Exact {
			lexical = append(lexical, c)
		}
		if c.Similarity != nil {
			v := *c.Similarity
			if math.IsNaN(v) || math.IsInf(v, 0) || v < -1.00001 || v > 1.00001 {
				return nil, errors.New("invalid semantic similarity")
			}
			if v >= cutoff {
				semantic = append(semantic, c)
			}
		}
	}
	less := func(a, b HybridCandidate) bool {
		if a.SortName != b.SortName {
			return a.SortName < b.SortName
		}
		return a.ID < b.ID
	}
	sort.Slice(lexical, func(i, j int) bool {
		if lexical[i].LexicalScore != lexical[j].LexicalScore {
			return lexical[i].LexicalScore > lexical[j].LexicalScore
		}
		return less(lexical[i], lexical[j])
	})
	sort.Slice(semantic, func(i, j int) bool {
		if *semantic[i].Similarity != *semantic[j].Similarity {
			return *semantic[i].Similarity > *semantic[j].Similarity
		}
		return less(semantic[i], semantic[j])
	})
	fused := make(map[string]float64, len(lexical)+len(semantic))
	for _, items := range [][]HybridCandidate{lexical, semantic} {
		for i, c := range items {
			fused[c.ID] += 1 / float64(SemanticRRFConstant+i+1)
		}
	}
	result := make([]HybridRank, 0, len(fused))
	for id, score := range fused {
		rank := int64(math.Round(score * 1e12))
		display := min(99, max(1, int(math.Round(score*float64(SemanticRRFConstant+1)*50))))
		if byID[id].Exact {
			rank = 1e12
			display = 100
		}
		result = append(result, HybridRank{ID: id, Rank: rank, Score: display})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Rank != result[j].Rank {
			return result[i].Rank > result[j].Rank
		}
		return less(byID[result[i].ID], byID[result[j].ID])
	})
	return result, nil
}
