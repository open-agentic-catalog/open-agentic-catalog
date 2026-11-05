"""Synthetic assertions of the actual dashboard expressions, without a second copy."""
import re


def expression(raw, deployment="local", role="api|worker"):
    """Replace only supported dashboard variables with fixed test scope values."""
    if not re.fullmatch(r"[a-z0-9_-]{1,64}", deployment) or role not in {"api", "worker", "api|worker"}:
        raise ValueError("invalid-query-scope")
    result = raw.replace("${deployment:regex}", deployment).replace("${role:regex}", role).replace("$__rate_interval", "1m")
    if "$" in result:
        raise ValueError("unknown-dashboard-variable")
    return result


def fixture(dashboard):
    """Build promtool cases from shipped queries, including absent and stale samples.

    Panel IDs and target shapes are checked so a dashboard edit cannot silently
    skip assertions or test a separately maintained copy of its expressions.
    """
    panels = {panel["id"]: panel for panel in dashboard["panels"]}
    if len(panels) != len(dashboard["panels"]) or set(panels) != set(range(1, 33)):
        raise ValueError("dashboard-panel-inventory")
    def query(identifier, expected, role="api|worker"):
        """Bind one panel's actual expression to its expected samples at two minutes."""
        targets = panels[identifier]["targets"]
        if len(targets) != 1 or targets[0]["refId"] != "A":
            raise ValueError("dashboard-query-inventory")
        return {"expr": expression(targets[0]["expr"], role=role), "eval_time": "2m", "exp_samples": expected}
    def series(name, values):
        """Describe synthetic samples using promtool's input-series notation."""
        return {"series": name, "values": values}
    def sample(labels, value):
        """Describe one expected PromQL result with its complete label set."""
        return {"labels": labels, "value": value}
    api = 'catalog_deployment="local",catalog_role="api",job="catalog-api",instance="api:8080"'
    other = api.replace('catalog_deployment="local"', 'catalog_deployment="other"').replace('instance="api:8080"', 'instance="other:8080"')
    worker = 'catalog_deployment="local",catalog_role="worker",job="catalog-worker",instance="worker:9090"'
    worker2 = worker.replace('instance="worker:9090"', 'instance="worker2:9090"')
    route = ',method="POST",route="/api/v1/catalog/search",status_class="2xx"'
    inputs = [series('up{' + api + '}', '1+0x12'), series('up{' + other + '}', '1+0x12'),
              series('open_agentic_catalog_http_responses_total{' + api + route + '}', '0+10x12'),
              series('open_agentic_catalog_http_responses_total{' + other + route + '}', '0+20x12')]
    scoped = '{catalog_deployment="local",catalog_role="api"}'
    tests = [{"name": "deployment isolation and observed zero errors", "input_series": inputs,
              "promql_expr_test": [query(3, [sample(scoped, 1)]), query(4, [sample(scoped, 0)])]}]
    replica = api.replace('instance="api:8080"', 'instance="api2:8080"')
    tests.append({"name": "a healthy replica cannot validate a failed replica rate", "input_series": inputs + [series('up{' + replica + '}', '1+0x11 0'), series('open_agentic_catalog_http_responses_total{' + replica + route + '}', '0+30x12')],
                  "promql_expr_test": [query(3, [sample(scoped, 1)])]})
    for name, values, expected in [("histogram with observations", '0+10x12', [sample(scoped, .095)]), ("zero histogram has no percentile", '0+0x12', [])]:
        hist = [series('up{' + api + '}', '1+0x12')]
        hist += [series('open_agentic_catalog_http_request_duration_seconds_bucket{' + api + route + ',le="' + upper + '"}', values) for upper in ("0.1", "+Inf")]
        hist += [series('open_agentic_catalog_http_request_duration_seconds_count{' + api + route + '}', values)]
        tests.append({"name": name, "input_series": hist, "promql_expr_test": [query(5, expected)]})
    for name, traffic, up, expected in [
        ("counter reset", "0 10 20 30 40 50 0 10 20 30 40 50 60", "1+0x12", [sample(scoped, 1)]),
        ("observed idle", "0+0x12", "1+0x12", [sample(scoped, 0)]),
        ("failed scrape", "0+10x12", "1+0x11 0", []),
        ("stale target", "0+10x12", "1+0x11 stale", []),
        ("never emitted traffic", "_ _ _ _ _ _ _ _ _ _ _ _ _", "1+0x12", []),
    ]:
        tests.append({"name": name, "input_series": [series('up{' + api + '}', up), series('open_agentic_catalog_http_responses_total{' + api + route + '}', traffic)],
                      "promql_expr_test": [query(3, expected)]})
    backlog = 'open_agentic_catalog_jobs{' + worker + ',state="queued"}'
    backlog2 = 'open_agentic_catalog_jobs{' + worker2 + ',state="queued"}'
    inputs = [series('up{' + labels + '}', '1+0x12') for labels in [worker, worker2]]
    inputs += [series('open_agentic_catalog_operational_snapshot_available{' + labels + '}', '1+0x12') for labels in [worker, worker2]]
    inputs += [series(backlog, '7+0x12'), series(backlog2, '7+0x12')]
    tests.append({"name": "role selection and duplicated database-global snapshots", "input_series": inputs,
                  "promql_expr_test": [query(11, [], "api"), query(11, [sample(backlog, 7), sample(backlog2, 7)], "worker")]})
    tests.append({"name": "HTTP 200 is not a successful database snapshot", "input_series": [series('up{' + worker + '}', '1+0x12'),
                  series('open_agentic_catalog_operational_snapshot_available{' + worker + '}', '0+0x12'), series(backlog, '7+0x12')],
                  "promql_expr_test": [query(11, [])]})
    tests.append({"name": "no targets is unavailable", "input_series": [], "promql_expr_test": [query(i, []) for i in [1, 2, 3, 4, 9, 10, 11, 12]]})
    allocated = 'open_agentic_catalog_go_allocated_bytes_total{' + api + '}'
    availability = 'open_agentic_catalog_go_metric_available{' + api + ',metric="open_agentic_catalog_go_allocated_bytes_total"}'
    for name, available, healthy, expected in [
        ("runtime rate available", "1+0x12", "1+0x12", [sample('{' + api + '}', 1)]),
        ("HTTP 200 with failed runtime sample", "1+0x11 0", "1+0x12", []),
        ("stale runtime sample", "1+0x12", "1+0x11 stale", []),
    ]:
        tests.append({"name": name, "input_series": [series(allocated, '0+10x12'), series(availability, available), series('up{' + api + '}', healthy)],
                      "promql_expr_test": [query(15, expected)]})
    embedding_count = 'open_agentic_catalog_embedding_request_duration_seconds_count{' + api + ',operation="query",outcome="success"}'
    embedding_sum = 'open_agentic_catalog_embedding_request_duration_seconds_sum{' + api + ',operation="query",outcome="success"}'
    pending = 'open_agentic_catalog_semantic_pending_jobs{' + worker + '}'
    tests.append({"name": "optional search metrics use observed attempts and scoped global backlog", "input_series": [
        series('up{' + api + '}', '1+0x12'), series('up{' + worker + '}', '1+0x12'),
        series(embedding_count, '0+10x12'), series(embedding_sum, '0+2x12'),
        series(embedding_count.replace('success', 'admission'), '0+10x12'),
        series(embedding_sum.replace('success', 'admission'), '0+8x12'),
        series('open_agentic_catalog_search_retrieval_total{' + api + ',operation="search",outcome="hybrid"}', '0+10x12'),
        series('open_agentic_catalog_operational_snapshot_available{' + worker + '}', '1+0x12'), series(pending, '5+0x12')],
        "promql_expr_test": [
            query(23, [sample('{catalog_deployment="local",catalog_role="api",operation="query",outcome="success"}', 1), sample('{catalog_deployment="local",catalog_role="api",operation="query",outcome="admission"}', 1)]),
            query(24, [sample('{catalog_deployment="local",catalog_role="api",operation="search",outcome="hybrid"}', 1)]),
            query(25, [sample(pending, 5)]),
            query(26, [sample('{catalog_deployment="local",catalog_role="api",operation="query"}', 0.5)])]})
    for prefix, rate_panel, mean_panel, labels in [
        ("job", 27, 28, ',kind="source_sync"'), ("webhook", 29, 30, ""),
    ]:
        metric = "open_agentic_catalog_" + prefix + "_retry_delay_seconds"
        result = '{catalog_deployment="local",catalog_role="worker"' + labels + '}'
        other_worker = worker.replace('catalog_deployment="local"', 'catalog_deployment="other"')
        for name, healthy, counts, sums, rate_expected, mean_expected in [
            ("persisted", "1+0x12", "0+10x12", "0+55x12", [sample(result, 1)], [sample(result, 5.5)]),
            ("idle", "1+0x12", "1+0x12", "5.5+0x12", [sample(result, 0)], []),
            ("failed scrape", "1+0x11 0", "0+10x12", "0+55x12", [], []),
            ("stale scrape", "1+0x11 stale", "0+10x12", "0+55x12", [], []),
        ]:
            tests.append({"name": prefix + " retry " + name, "input_series": [
                series('up{' + worker + '}', healthy),
                series(metric + '_count{' + worker + labels + '}', counts),
                series(metric + '_sum{' + worker + labels + '}', sums),
                series('up{' + other_worker + '}', '1+0x12'),
                series(metric + '_count{' + other_worker + labels + '}', '0+100x12'),
                series(metric + '_sum{' + other_worker + labels + '}', '0+500x12')],
                "promql_expr_test": [query(rate_panel, rate_expected), query(mean_panel, mean_expected),
                                     query(rate_panel, [], "api"), query(mean_panel, [], "api")]})
        # Reset the first worker inside the evaluated interval. Apply rate per
        # process before summing; a mean of worker means would give 12.75, not
        # the observation-weighted 16.375 seconds required for healthy replicas.
        for name, healthy, expected_rate, expected_mean in [
            ("healthy replica", "1+0x12", 4, 16.375),
            ("failed replica", "1+0x11 0", 1, 5.5),
            ("stale replica", "1+0x11 stale", 1, 5.5),
        ]:
            tests.append({"name": prefix + " retry counter reset and " + name, "input_series": [
                series('up{' + worker + '}', '1+0x12'),
                series(metric + '_count{' + worker + labels + '}', '100 110 120 130 140 150 160 170 180 10 20 30 40'),
                series(metric + '_sum{' + worker + labels + '}', '550 605 660 715 770 825 880 935 990 55 110 165 220'),
                series('up{' + worker2 + '}', healthy),
                series(metric + '_count{' + worker2 + labels + '}', '0+30x12'),
                series(metric + '_sum{' + worker2 + labels + '}', '0+600x12')],
                "promql_expr_test": [query(rate_panel, [sample(result, expected_rate)]),
                                     query(mean_panel, [sample(result, expected_mean)])]})
    for name, counts, healthy, rate_expected in [
        ("active", "0+10x12", "1+0x12", 1),
        ("reset", "100 110 120 130 140 150 160 170 180 10 20 30 40", "1+0x12", 1),
        ("idle", "0+0x12", "1+0x12", 0),
        ("failed scrape", "0+10x12", "1+0x11 0", None),
        ("stale scrape", "0+10x12", "1+0x11 stale", None),
        ("not emitted", "_ _ _ _ _ _ _ _ _ _ _ _ _", "1+0x12", None),
    ]:
        refusal = "open_agentic_catalog_security_refusals_total"
        samples = [series('up{' + labels + '}', healthy if labels == api else '1+0x12') for labels in [api, other, worker]]
        for labels in [api, other, worker]:
            samples += [series(refusal + '{' + labels + ',reason="' + reason + '"}', counts if labels == api else '0+100x12') for reason in ["unauthenticated", "forbidden"]]
        expected = [] if rate_expected is None else [sample('{catalog_deployment="local",catalog_role="api",reason="' + reason + '"}', rate_expected) for reason in ["unauthenticated", "forbidden"]]
        tests.append({"name": "security refusals " + name, "input_series": samples,
                      "promql_expr_test": [query(31, expected), query(31, [], "worker")]})
    for name, batches, failures, healthy, fraction in [
        ("failures", "0+10x12", "0+2x12", "1+0x12", .2),
        ("observed zero failures", "0+10x12", "0+0x12", "1+0x12", 0),
        ("no submissions", "0+0x12", "0+0x12", "1+0x12", None),
        ("counter reset", "100 110 120 130 140 150 160 170 180 10 20 30 40", "20 22 24 26 28 30 32 34 36 2 4 6 8", "1+0x12", .2),
        ("failed scrape", "0+10x12", "0+2x12", "1+0x11 0", None),
        ("stale scrape", "0+10x12", "0+2x12", "1+0x11 stale", None),
        ("not emitted", "_ _ _ _ _ _ _ _ _ _ _ _ _", "_ _ _ _ _ _ _ _ _ _ _ _ _", "1+0x12", None),
    ]:
        samples = [series('up{' + labels + '}', healthy if labels == api else '1+0x12') for labels in [api, other, worker]]
        for labels in [api, other, worker]:
            samples += [series('open_agentic_catalog_trace_export_batches_total{' + labels + '}', batches if labels == api else '0+100x12'),
                        series('open_agentic_catalog_trace_export_failures_total{' + labels + '}', failures if labels == api else '0+100x12')]
        expected = [] if fraction is None else [sample(scoped, fraction)]
        tests.append({"name": "trace export " + name, "input_series": samples,
                      "promql_expr_test": [query(32, expected, "api"), query(32, [sample('{catalog_deployment="local",catalog_role="worker"}', 1)], "worker")]})
    for test in tests:
        test["interval"] = "10s"
    # Parse/evaluate every maintained query as well as the behavioral cases.
    tests.append({"name": "all expressions accept an empty unavailable fixture", "interval": "10s", "input_series": [],
                  "promql_expr_test": [query(i, []) for i in panels]})
    return {"evaluation_interval": "10s", "tests": tests}
