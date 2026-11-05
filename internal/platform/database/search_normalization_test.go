package database

import (
	"fmt"
	"strings"
	"testing"
	"unicode"

	"github.com/open-agentic-catalog/open-agentic-catalog/migrations"
)

// The SQL class is a frozen copy of Go's existing Letter | Number semantics,
// not an independently maintained interpretation of Unicode character classes.
func TestSearchNormalizationUnicodeClassMatchesGo(t *testing.T) {
	t.Parallel()
	escape := func(r rune) string {
		if r <= 0xffff {
			return fmt.Sprintf("\\%04X", r)
		}
		return fmt.Sprintf("\\+%06X", r)
	}
	var class strings.Builder
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if !unicode.IsLetter(r) && !unicode.IsNumber(r) {
			continue
		}
		first := r
		for r < unicode.MaxRune && (unicode.IsLetter(r+1) || unicode.IsNumber(r+1)) {
			r++
		}
		class.WriteString(escape(first))
		if first != r {
			class.WriteByte('-')
			class.WriteString(escape(r))
		}
	}
	data, err := migrations.Files.ReadFile("000027_search_normalization.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "U&'[^"+class.String()+"]+'") {
		t.Fatalf("SQL token class differs from Go Unicode %s; review the toolchain/profile and add a forward migration, never edit an applied migration", unicode.Version)
	}
}
