"""Independent membership and metadata expectations for the selected native crawler."""

SOURCE = "https://peer-a.test:8443/.well-known/ai-catalog.json"
SELF = "http://127.0.0.1:9010/api"
EXTENSION = "https://ai-catalog.org/extensions/metadata"
PHASES = ("setup", "seed", "initial", "confinement", "update", "withdrawal", "malformed", "outage", "restart", "recovery", "tls-refusal", "final-checks")


def entries_by_id(entries):
    """Refuse skipped, repeated or malformed members before comparing metadata."""
    assert isinstance(entries, list) and entries, "nonempty entry array required"
    result = {}
    for entry in entries:
        assert isinstance(entry, dict) and isinstance(entry.get("identifier"), str) and entry["identifier"], "entry identity missing"
        assert entry["identifier"] not in result, "duplicate identifier or multiple versions"
        result[entry["identifier"]] = entry
    return result


def check_readback(expected, page, search, snapshot):
    """Compare every original field; keep peer source and persisted origin distinct."""
    wanted = entries_by_id(expected)
    assert type(page.get("total")) is int and page["total"] == len(wanted), "native List total mismatch"
    assert "pageToken" in page and page["pageToken"] is None, "native terminal token changed; review reverse limitation"
    assert search.get("pageToken") is None, "unexpected Search continuation"
    for rows, scored in ((page["items"], False), (search["results"], True)):
        actual = entries_by_id(rows)
        assert actual.keys() == wanted.keys(), "native membership mismatch"
        for identifier, row in actual.items():
            assert row.get("source") == SELF, "native public source changed"
            if scored:
                assert type(row.get("score")) is int and 0 < row["score"] <= 100, "invalid native relevance score"
            original = {key: value for key, value in row.items() if key not in (("source", "score") if scored else ("source",))}
            assert original == wanted[identifier], "native entry metadata changed or field lost"
    rows = snapshot["entries"]
    assert isinstance(rows, list) and len(rows) == len(wanted), "snapshot membership mismatch"
    assert all(row.get("origin") == SOURCE for row in rows), "retained manifest origin changed"
    assert entries_by_id([row["entry"] for row in rows]) == wanted, "snapshot metadata changed"


def finish(report):
    """Incomplete phases, an expected-control run or failed cleanup cannot pass."""
    return (report.get("cleanup") == "complete" and not report.get("error") and report.get("control") is None
            and report.get("phases") == dict.fromkeys(PHASES, "passed"))
