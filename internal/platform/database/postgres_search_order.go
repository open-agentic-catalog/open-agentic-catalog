package database

import (
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// Column ordering stays inside bounded SQL selection. Only reviewed expressions
// enter SQL; request values remain bound parameters.
func searchColumnOrder(order catalog.SearchOrder, prefix, cursorKey, cursorName, cursorID string) (predicate, ordering string) {
	key := prefix + "sort_value"
	if order == catalog.SearchOrderNameAsc || order == catalog.SearchOrderNameDesc {
		key, cursorKey = prefix+"sort_name", cursorName
	}
	direction, comparison := "ASC", ">"
	if strings.HasSuffix(string(order), "_desc") {
		direction, comparison = "DESC", "<"
	}
	name, id := prefix+"sort_name", prefix+"result_id"
	tie := name + ">" + cursorName + ` COLLATE "C" OR (` + name + "=" + cursorName + ` COLLATE "C" AND ` + id + ">" + cursorID + ")"
	predicate = key + comparison + cursorKey + ` COLLATE "C" OR (` + key + "=" + cursorKey + ` COLLATE "C" AND (` + tie + "))"
	ordering = key + ` COLLATE "C" ` + direction + "," + name + ` COLLATE "C",` + id
	return predicate, ordering
}

func localColumnOrderSQL(statement string, order catalog.SearchOrder) string {
	if order == "" {
		return statement
	}
	key := `kind COLLATE "C"`
	if order == catalog.SearchOrderPublisherAsc || order == catalog.SearchOrderPublisherDesc {
		key = `(SELECT catalog_core_sort_name_v1(coalesce(nullif(publisher.display_name,''),records.publisher_id::text)) FROM publishers publisher WHERE publisher.tenant_id=$1 AND publisher.id=records.publisher_id) COLLATE "C"`
	}
	predicate, ordering := searchColumnOrder(order, "", "$12::text", "$13", "$14")
	_, outerOrder := searchColumnOrder(order, "page.", "$12::text", "$13", "$14")
	return strings.NewReplacer(
		"SELECT release_id,sort_name,result_id,", "SELECT release_id,sort_name,result_id,"+key+" AS sort_value,",
		"SELECT release_id,score,sort_name,result_id FROM matched", "SELECT release_id,score,sort_name,result_id,sort_value FROM matched",
		`($11=false OR score<$12 OR (score=$12 AND (sort_name>$13 COLLATE "C" OR (sort_name=$13 COLLATE "C" AND result_id>$14))))`, `($12::text IS NOT NULL AND ($11=false OR (`+predicate+`)))`,
		`ORDER BY score DESC,sort_name COLLATE "C",result_id`, "ORDER BY "+ordering,
		`ORDER BY page.score DESC,page.sort_name COLLATE "C",page.result_id`, "ORDER BY "+outerOrder,
	).Replace(statement)
}

func indexedColumnOrderSQL(statement string, order catalog.SearchOrder) string {
	if order == "" {
		return statement
	}
	key := `member.kind COLLATE "C"`
	if order == catalog.SearchOrderPublisherAsc || order == catalog.SearchOrderPublisherDesc {
		key = `catalog_core_sort_name_v1(member.search_publisher) COLLATE "C"`
	}
	predicate, ordering := searchColumnOrder(order, "", "$10::text", "$11", "$12")
	_, outerOrder := searchColumnOrder(order, "selected.", "$10::text", "$11", "$12")
	return strings.NewReplacer(
		", selected AS (", ", ordered_members AS (SELECT member.identifier,member.version,member.version_present,member.kind,member.normalized_entry_digest,member.search_display_name,member.search_description,member.search_tags,member.search_publisher,member.sort_name,member.result_id,member.score,member.member_rank,"+key+" AS sort_value FROM ranked_members member), selected AS (",
		"member.sort_name,member.result_id,member.score\n    FROM ranked_members", "member.sort_name,member.result_id,member.score,member.sort_value\n    FROM ordered_members",
		`($9=false OR score<$10 OR (score=$10 AND (sort_name>$11 COLLATE "C" OR (sort_name=$11 COLLATE "C" AND result_id>$12))))`, `($10::text IS NOT NULL AND ($9=false OR (`+predicate+`)))`,
		`ORDER BY score DESC,sort_name COLLATE "C",result_id`, "ORDER BY "+ordering,
		`ORDER BY selected.score DESC,selected.sort_name COLLATE "C",selected.result_id`, "ORDER BY "+outerOrder,
	).Replace(statement)
}
