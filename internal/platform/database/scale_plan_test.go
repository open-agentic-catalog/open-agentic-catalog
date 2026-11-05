package database

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// Keep the scale report within its byte budget without discarding measured
// nodes. Omitted statistics are zero; other diagnostics keep their own format.
type scalePlanNode struct {
	Type     string          `json:"Node Type"`
	Relation string          `json:"Relation Name,omitempty"`
	Rows     float64         `json:"Actual Rows,omitempty"`
	Loops    float64         `json:"Actual Loops,omitempty"`
	Hits     int             `json:"Shared Hit Blocks,omitempty"`
	Reads    int             `json:"Shared Read Blocks,omitempty"`
	Temp     int             `json:"Temp Written Blocks,omitempty"`
	Plans    []scalePlanNode `json:"Plans,omitempty"`
}

func TestScalePlanEncodingPreservesMeasurements(t *testing.T) {
	plan := scalePlanNode{Type: "Aggregate", Rows: 1, Loops: 1, Hits: 10, Plans: []scalePlanNode{
		{Type: "Seq Scan", Relation: "resources", Rows: 900, Loops: 2, Hits: 7, Reads: 3, Temp: 4},
		{Type: "Result"},
	}}
	// Independent PostgreSQL field names catch a mistaken shared JSON tag.
	const input = `{"Node Type":"Aggregate","Actual Rows":1,"Actual Loops":1,"Shared Hit Blocks":10,"Plans":[
		{"Node Type":"Seq Scan","Relation Name":"resources","Actual Rows":900,"Actual Loops":2,"Shared Hit Blocks":7,"Shared Read Blocks":3,"Temp Written Blocks":4,"Filter":"private expression"},
		{"Node Type":"Result","Actual Rows":0,"Actual Loops":0,"Shared Hit Blocks":0,"Shared Read Blocks":0,"Temp Written Blocks":0,"Relation Name":"","Plans":null}]}`
	var projected scalePlanNode
	if err := json.Unmarshal([]byte(input), &projected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(projected, plan) {
		t.Fatal("scale projection lost PostgreSQL fields or child nodes")
	}
	raw, err := json.Marshal(projected)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private expression") || strings.Contains(string(raw), "Filter") {
		t.Fatal("scale projection retained private expressions")
	}
	var decoded scalePlanNode
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, plan) {
		t.Fatal("scale plan encoding lost measured fields or child nodes")
	}
	zero, err := json.Marshal(decoded.Plans[1])
	if err != nil || string(zero) != `{"Node Type":"Result"}` {
		t.Fatal("zero statistics and empty fields must be omitted, not repeated for every node")
	}
}

// These SQL literals are exclusively the closed synthetic scale cases. Keep
// each argument separate so quoted continuation values cannot affect arity.
func scalePlanArguments(tenant, repository, text, kinds string) []string {
	args := []string{fmt.Sprintf("'%s'", tenant), fmt.Sprintf("'%s'", text), fmt.Sprintf("'%s'", catalog.CatalogSearchExactIdentifier(text)), kinds, "NULL", "''"}
	if repository == "local" {
		args = append(args, "''", "''", "''", "NULL") // visibility, namespace, license and categories
	} else {
		args = append(args, "''", "NULL") // indexed license and categories
	}
	return args
}

func validateScalePlanArguments(parameters int, args []string) error {
	if parameters != len(args) {
		return fmt.Errorf("scale plan argument count disagrees with prepared statement")
	}
	return nil
}

func TestScalePlanArguments(t *testing.T) {
	for _, repository := range []string{"local", "indexed"} {
		base := scalePlanArguments("00000000-0000-7000-8000-000000000001", repository, "Scale", "ARRAY['skill']")
		otherTenant := "00000000-0000-7000-8000-000000000801"
		other := scalePlanArguments(otherTenant, repository, "Scale", "ARRAY['skill']")
		if other[0] != "'"+otherTenant+"'" || !slices.Equal(base[1:], other[1:]) {
			t.Fatal("diagnostic arguments must select their own fixture tenant")
		}
		count := 8
		if repository == "local" {
			count = 10
			if !slices.Equal(base[6:], []string{"''", "''", "''", "NULL"}) {
				t.Fatal("local visibility, namespace, license and category filters must all be supplied")
			}
		}
		for _, suffix := range [][]string{nil, {"false", "0", "''", "''", "21"}, {"true", "60", "'quoted, name'", "'local:id'", "21"}} {
			args := append(slices.Clone(base), suffix...)
			if err := validateScalePlanArguments(count+len(suffix), args); err != nil {
				t.Fatal(err)
			}
			if validateScalePlanArguments(count+len(suffix), args[:len(args)-1]) == nil || validateScalePlanArguments(count+len(suffix), append(slices.Clone(args), "''")) == nil {
				t.Fatal("missing or extra argument must fail setup rather than become a query-failed sample")
			}
		}
		if repository == "local" {
			if err := validateScalePlanArguments(14, append(base, "1", "2", "1", "2")); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// Compare the diagnostic's arguments with the real statement placeholders.
// A helper-only count assertion cannot detect new production filters.
func TestScalePlanArgumentsMatchSearchStatements(t *testing.T) {
	type probe struct {
		sql  string
		args []string
	}
	parameters := regexp.MustCompile(`\$([0-9]+)`)
	for _, repository := range []string{"local", "indexed"} {
		for _, text := range []string{"", "Scale"} {
			records, page := catalogSearchStatements(catalog.SearchQuery{Text: text})
			countFrom := "matched"
			if repository == "indexed" {
				records = indexedCatalogSearchRecordsCTE
				page = records + indexedCatalogSearchPageSQL
				countFrom = "selected_identities"
			}
			base := scalePlanArguments("00000000-0000-7000-8000-000000000001", repository, text, "NULL")
			statements := []probe{
				{records + "SELECT count(*) FROM " + countFrom, base},
				{page, append(slices.Clone(base), "false", "0", "''", "''", "21")},
			}
			if repository == "local" {
				facets, _, err := catalogFacetStatement(catalog.SearchQuery{Text: text}, []catalog.FacetRequest{{Field: "kind", Limit: 2}, {Field: "tag", Limit: 2}})
				if err != nil {
					t.Fatal(err)
				}
				statements = append(statements, probe{facets, append(slices.Clone(base), "1", "2", "1", "2")})
			}
			for index, statement := range statements {
				maximum := 0
				for _, match := range parameters.FindAllStringSubmatch(statement.sql, -1) {
					value, err := strconv.Atoi(match[1])
					if err != nil {
						t.Fatal(err)
					}
					maximum = max(maximum, value)
				}
				if err := validateScalePlanArguments(maximum, statement.args); err != nil {
					t.Errorf("%s text=%q statement=%d: %v (parameters=%d arguments=%d)", repository, text, index, err, maximum, len(statement.args))
				}
			}
		}
	}
}
