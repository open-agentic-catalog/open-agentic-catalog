// Semantic primitive tests cover immutable projection and ranking math without
// inference, database fixtures or assumptions about a model's natural-language quality.
package catalog

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

// TestSemanticProjection checks stable tags, field boundaries and independent copies.
func TestSemanticProjection(t *testing.T) {
	d := SemanticDocument{DisplayName: "Find tools", Description: "Reviewed metadata", Tags: []string{"z", "a"}, RepresentativeQueries: []string{"Find order tools"}}
	text, digest, err := d.Projection()
	want := "Name: Find tools\nDescription: Reviewed metadata\nTags: a, z\nRepresentative queries: Find order tools"
	if err != nil || !strings.HasPrefix(digest, "sha256:") || text != want || d.Tags[0] != "z" {
		t.Fatalf("projection %s %s %v", text, digest, err)
	}
	d.Tags = []string{"a", "z", "a"}
	_, again, _ := d.Projection()
	if again != digest {
		t.Fatal("tag order changed embedding")
	}
	d.Description += " changed"
	_, changed, _ := d.Projection()
	if changed == digest {
		t.Fatal("metadata change retained digest")
	}
	for _, bad := range []SemanticDocument{{}, {DisplayName: "\xff"}, {DisplayName: "x\x00y"}, {DisplayName: strings.Repeat("x", 641)}, {DisplayName: "x", Description: strings.Repeat("y", 16001)}, {DisplayName: "x", Tags: make([]string, 101)}, {DisplayName: "x", Tags: []string{strings.Repeat("y", 321)}}, {DisplayName: "x", RepresentativeQueries: make([]string, 6)}, {DisplayName: "x", RepresentativeQueries: []string{strings.Repeat("y", 961)}}} {
		if _, _, err := bad.Projection(); err == nil {
			t.Fatal("accepted invalid projection")
		}
	}
}

// TestHybridFusion verifies semantic-only admission, hard cutoff, exact precedence
// and unchanged scores across pages rather than page-relative confidence claims.
func TestHybridFusion(t *testing.T) {
	near, far := .8, .2
	candidates := []HybridCandidate{{ID: "semantic", SortName: "a", Similarity: &near}, {ID: "lexical", SortName: "b", LexicalScore: 70}, {ID: "both", SortName: "c", LexicalScore: 80, Similarity: &near}, {ID: "excluded", Similarity: &far}, {ID: "exact", Exact: true}}
	got, err := RankHybrid(candidates, .5)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(got))
	for _, r := range got {
		ids = append(ids, r.ID)
		if r.Score < 1 || r.Score > 100 {
			t.Fatal("display bounds")
		}
	}
	if !reflect.DeepEqual(ids, []string{"exact", "both", "semantic", "lexical"}) || got[0].Score != 100 || got[1].Rank <= got[2].Rank {
		t.Fatalf("ranking %+v", got)
	}
	for i, j := 0, len(candidates)-1; i < j; i, j = i+1, j-1 {
		candidates[i], candidates[j] = candidates[j], candidates[i]
	}
	again, err := RankHybrid(candidates, .5)
	if err != nil || !reflect.DeepEqual(got, again) {
		t.Fatal("input order changed ranking")
	}
	got, err = RankHybrid([]HybridCandidate{{ID: "boundary", Similarity: &near}}, near)
	if err != nil || len(got) != 1 {
		t.Fatal("cutoff equality")
	}
	got, err = RankHybrid(nil, .5)
	if err != nil || len(got) != 0 {
		t.Fatal("empty ranking")
	}
}

// TestHybridInvalidInput rejects broken internal eligibility/ranking projections.
func TestHybridInvalidInput(t *testing.T) {
	nan := math.NaN()
	tooLarge := 2.0
	for _, items := range [][]HybridCandidate{{{ID: "same"}, {ID: "same"}}, {{}}, {{ID: "x", LexicalScore: 101}}, {{ID: "x", Similarity: &nan}}, {{ID: "x", Similarity: &tooLarge}}} {
		if _, err := RankHybrid(items, .5); err == nil {
			t.Fatal("accepted invalid candidate")
		}
	}
	for _, cutoff := range []float64{math.NaN(), math.Inf(1), -2, 2} {
		if _, err := RankHybrid(nil, cutoff); err == nil {
			t.Fatal("accepted cutoff")
		}
	}
}

func TestSemanticProjectionAcceptsMaximumListingUnicode(t *testing.T) {
	document := SemanticDocument{DisplayName: strings.Repeat("🙂", 160), Description: strings.Repeat("🙂", 4000)}
	text, _, err := document.Projection()
	if err != nil || !strings.Contains(text, "Name: "+document.DisplayName+"\nDescription: "+document.Description+"\n") {
		t.Fatalf("valid listing metadata rejected or truncated: %v", err)
	}
	document.DisplayName += "x"
	if _, _, err := document.Projection(); err == nil {
		t.Fatal("projection name byte budget not enforced")
	}
	document.DisplayName = "valid"
	document.Description += "x"
	if _, _, err := document.Projection(); err == nil {
		t.Fatal("projection description byte budget not enforced")
	}
}
