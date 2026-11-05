import copy
import json
from pathlib import Path
import tempfile
from unittest.mock import Mock, patch
import unittest
import runner
import scale
from evidence_test_helpers import report, profile


class ScaleTests(unittest.TestCase):
    def test_correctness_failures_keep_only_fixed_http_diagnostics(self):
        for raw, expected in (
            (json.dumps({"code": "INVALID_ARGUMENT", "detail": "live search cursor is invalid or expired; restart the search", "instance": "private-sentinel"}), "400-INVALID_ARGUMENT-invalid-live-cursor"),
            (json.dumps({"code": "INVALID_ARGUMENT", "detail": "catalog cursor is stale"}), "400-INVALID_ARGUMENT-stale-generation"),
            (json.dumps({"code": "DEPENDENCY_UNAVAILABLE", "detail": "private-sentinel"}), "503-DEPENDENCY_UNAVAILABLE"),
            (json.dumps({"code": "private-sentinel"}), "400"),
            (json.dumps({"code": []}), "400"),
            ('{"code":"INVALID_ARGUMENT","code":"private-sentinel"}', "400"),
            ("invalid private-sentinel", "400"),
        ):
            status = 503 if expected.startswith("503") else 400
            http = Mock(request=Mock(return_value=(status, {}, raw.encode())), decode=runner.http.decode, ClientFailure=runner.http.ClientFailure)
            with self.subTest(expected=expected), self.assertRaises(runner.Failure) as caught:
                scale.request("http://127.0.0.1:1234", {}, http, runner.Failure)
            self.assertEqual("scale-correctness-read-failed:" + expected, str(caught.exception))
            self.assertNotIn("private-sentinel", str(caught.exception))
        http = Mock(request=Mock(return_value=(200, {}, b'{"items":[]}')), decode=runner.http.decode)
        self.assertEqual({"items": []}, scale.request("http://127.0.0.1:1234", {}, http, runner.Failure))

    def test_partial_seed_report_preserves_the_original_interruption(self):
        fixture = {"postgres": "a" * 64, "image": "sha256:" + "b" * 64,
                   "project": "open-agentic-catalog-perf-" + "c" * 24, "nonce": "d" * 24}
        for interrupted in (False, True):
            with self.subTest(interrupted=interrupted), tempfile.TemporaryDirectory() as temporary:
                directory = Path(temporary)
                (directory / "seed-result.json").write_text("")
                report = {}
                thread = Mock(is_alive=Mock(return_value=False))
                results = [b"", runner.Failure("interrupted-or-deadline") if interrupted else b"seed complete: bounded\n"]
                with (patch.object(runner.local, "command", side_effect=results),
                      patch.object(scale.threading, "Thread", return_value=thread)):
                    expected = "interrupted-or-deadline" if interrupted else "scale-seed-report-invalid"
                    with self.assertRaisesRegex(runner.Failure, "^" + expected + "$"):
                        scale.seed(runner.local, {}, directory, fixture, 1000, report, Mock(), Mock())
                self.assertEqual("unreadable", report["seedReportStatus"])
                self.assertNotIn("seedResult", report)
                thread.join.assert_called_once_with(30)

    def test_seed_uses_only_its_owned_image_network_and_evidence(self):
        fixture = {"postgres": "a" * 64, "image": "sha256:" + "b" * 64,
                   "project": "open-agentic-catalog-perf-" + "c" * 24, "nonce": "d" * 24}
        for count, public in ((1000, False), (10000, False), (100000, False), (1000, True)):
            with self.subTest(count=count, public=public):
                args = scale.seed_command(Path("/owned"), fixture, count, public=public)
                self.assertEqual(["docker", "run", "--rm", "--pull=never"], args[:4])
                self.assertEqual("container:" + fixture["postgres"], args[args.index("--network") + 1])
                self.assertIn("com.docker.compose.project=" + fixture["project"], args)
                self.assertIn("com.open.agentic.catalog.performance.owner=" + fixture["nonce"], args)
                self.assertIn("type=bind,src=/owned/scale-seed,dst=/seed,readonly", args)
                self.assertIn("type=bind,src=/owned,dst=/evidence", args)
                self.assertIn("--read-only", args)
                self.assertIn("--cap-drop=ALL", args)
                self.assertIn("no-new-privileges", args)
                self.assertIn("GOMEMLIMIT=256MiB", args)
                self.assertEqual([fixture["image"], "5432", str(count), str(scale.BUDGETS[count][0]),
                                  "/evidence/seed-result.json", *(["public"] if public else [])], args[-(6 if public else 5):])
        for changed in (fixture | {"postgres": "foreign"}, fixture | {"image": "remote:latest"},
                        fixture | {"project": "retained"}, fixture | {"nonce": "foreign"},
                        fixture | {"extra": "private"}, fixture | {"postgres": None}):
            with self.subTest(changed=changed), self.assertRaisesRegex(ValueError, "^invalid-seed-fixture$"):
                scale.seed_command(Path("/owned"), changed, 1000)
        for count, public in ((True, False), (1001, False), (10000, True)):
            with self.assertRaises(ValueError):
                scale.seed_command(Path("/owned"), fixture, count, public=public)

    def test_mixed_scale_report_binds_the_selected_population(self):
        selected = profile() | {"scenario": "mixed", "mixedScaleItems": 1000}
        value = report() | {"profile": selected, "baselineResults": 20}
        runner.validate_workload(value, selected)
        for changed in (selected | {"scenario": "local"}, selected | {"mixedScaleItems": 999},
                        selected | {"mixedScaleItems": "1000"}, selected | {"mixedScaleItems": True}):
            with self.assertRaises(runner.Failure):
                runner.validate_workload(value | {"profile": changed}, changed)
        with self.assertRaises(runner.Failure):
            runner.validate_workload(value | {"baselineResults": 3}, selected)

    def test_optional_plans_validate_control_and_bounded_closed_report(self):
        self.assertFalse(scale.plans_enabled({}, runner.Failure))
        self.assertTrue(scale.plans_enabled({"PERF_SQL_PLANS": "on"}, runner.Failure))
        for value in ("", "true", "ON", "arbitrary"):
            with self.assertRaises(runner.Failure):
                scale.plans_enabled({"PERF_SQL_PLANS": value}, runner.Failure)
        node = {"Node Type": "Aggregate", "Relation Name": "", "Actual Rows": 1, "Actual Loops": 1,
                "Shared Hit Blocks": 10, "Shared Read Blocks": 0, "Temp Written Blocks": 0, "Plans": None}
        rows = [{"repository": repository, "case": case, "mode": mode, "statement": statement, "status": "measured",
                 "planningMillis": 1, "executionMillis": 2, "plan": node}
                for repository in ("local", "indexed")
                for case in (("all", "common", "rare", "absent", "exact-" + repository, "kind") + (("local-first", "local-third") if repository == "local" else ()))
                for mode in ("force_custom_plan", "force_generic_plan") for statement in ("count", "page")]
        rows.extend({"repository": "local", "case": case, "mode": mode, "statement": "page", "status": "measured",
                     "planningMillis": 1, "executionMillis": 2, "plan": node}
                    for case in scale.ORDER_CASES for mode in ("force_custom_plan", "force_generic_plan"))
        rows.extend({"repository": "local", "case": "facets", "mode": mode, "statement": "facets", "status": "measured",
                     "planningMillis": 1, "executionMillis": 2, "plan": node}
                    for mode in ("force_custom_plan", "force_generic_plan"))
        rows.extend({"repository": scope, "case": case, "mode": mode, "statement": "summary", "status": "measured",
                     "planningMillis": 1, "executionMillis": 2, "plan": node}
                    for scope, cases in (("overview", ("resources", "releases", "indexed", "reviews-pending", "reviews-oldest", "reviews-decisions", "sources", "source-problems")),
                                         ("overview-sparse", ("resources", "reviews-pending")))
                    for case in cases for mode in ("force_custom_plan", "force_generic_plan"))
        def collect(value):
            raw = b"=== RUN\nscale-plans: " + json.dumps(value).encode() + b"\nPASS\n"
            with patch.object(runner.local.safety, "command", return_value=raw) as command:
                result = scale.collect_plans(runner.local, {}, Path("/owned"), 1234)
                self.assertEqual({"CATALOG_SCALE_PLAN_PORT": "1234"}, command.call_args.args[1])
                self.assertEqual(400, command.call_args.args[2])
                return result
        self.assertEqual("collected", collect(rows)["status"])
        compact = {"Node Type": "Result"}
        self.assertEqual(compact, collect([rows[0] | {"plan": compact}, *rows[1:]])["samples"][0]["plan"])
        # One root, three children and 316 leaves retain exactly 320 nodes.
        bounded = compact | {"Plans": [compact | {"Plans": [compact] * count} for count in (128, 128, 60)]}
        self.assertEqual(bounded, collect([rows[0] | {"plan": bounded}, *rows[1:]])["samples"][0]["plan"])
        excess = compact | {"Plans": [compact | {"Plans": [compact] * count} for count in (128, 128, 61)]}
        self.assertEqual("query-failed", collect([rows[0] | {"status": "query-failed"}, *rows[1:]])["samples"][0]["status"])
        deep = copy.deepcopy(node)
        for _ in range(33):
            deep = node | {"Plans": [deep]}
        for changed, code in [(rows[:-1], "inventory"), ([rows[0], *rows[:-1]], "sample-key"),
                              ([rows[0] | {"sql": "private"}, *rows[1:]], "sample-shape"),
                              ([rows[0] | {"executionMillis": -1}, *rows[1:]], "sample-timing"),
                              ([rows[0] | {"executionMillis": float("inf")}, *rows[1:]], "json"),
                              ([rows[0] | {"plan": node | {"Filter": "private"}}, *rows[1:]], "node-shape"),
                              ([rows[0] | {"plan": {}}, *rows[1:]], "node-shape"),
                              ([rows[0] | {"plan": node | {"Actual Rows": True}}, *rows[1:]], "node-statistics"),
                              ([rows[0] | {"plan": excess}, *rows[1:]], "node-limit"),
                              ([rows[0] | {"plan": deep}, *rows[1:]], "depth-limit"),
                              ([rows[0] | {"plan": node | {"Plans": [node] * 129}}, *rows[1:]], "children-limit")]:
            with self.subTest(code=code), self.assertRaisesRegex(runner.Failure, "^scale-plan-" + code + "$"):
                collect(changed)
        for raw, code in [(b"PASS\n", "envelope"), (b"x" * (512 * 1024 + 1), "output-limit"),
                          (b"scale-plans: invalid JSON\n", "json")]:
            with patch.object(runner.local.safety, "command", return_value=raw), self.assertRaisesRegex(runner.Failure, "^scale-plan-" + code + "$"):
                scale.collect_plans(runner.local, {}, Path("/owned"), 1234)

    def test_plan_failure_codes_do_not_expose_child_or_parser_errors(self):
        for error, code in [(runner.Failure("deadline"), "helper-deadline"),
                            (runner.Failure("child-output-limit"), "helper-output-limit"),
                            (runner.Failure("private DSN or SQL"), "helper-failed"),
                            (OSError("private path"), "helper-failed")]:
            with self.subTest(code=code), patch.object(runner.local.safety, "command", side_effect=error):
                with self.assertRaises(runner.Failure) as caught:
                    scale.collect_plans(runner.local, {}, Path("/owned"), 1234)
                self.assertEqual("scale-plan-" + code, str(caught.exception))
                self.assertEqual(str(caught.exception), scale.plan_failure(caught.exception))
        for error in (ValueError("private body"), runner.Failure("private DSN"),
                      runner.Failure("scale-plan-arbitrary-private-text")):
            self.assertEqual("scale-plan-unexpected-failure", scale.plan_failure(error))
        for raw, code in [(b"private output\nscale-plan-failure: preparation\n", "helper-preparation"),
                          (b"scale-plan-failure: private DSN\n", "helper-failed"),
                          (b"scale-plan-failure: reader\nscale-plan-failure: policy\n", "helper-failed")]:
            def failed(*_args, **kwargs):
                kwargs["failed_stdout"](raw)
                raise runner.Failure("child-failed")
            with patch.object(runner.local.safety, "command", side_effect=failed):
                with self.assertRaisesRegex(runner.Failure, "^scale-plan-" + code + "$"):
                    scale.collect_plans(runner.local, {}, Path("/owned"), 1234)
    def test_child_report_accepts_scale_shape_but_rejects_extra_inputs(self):
        selected = profile() | {"scenario": "scale", "scale": {"items": 100000, "case": "facets", "publisher": "00000000-0000-7000-8000-000000000010"}}
        value = report() | {"profile": selected, "baselineResults": 89997}
        runner.validate_workload(value, selected)
        for scale_value in [selected["scale"] | {"items": True}, selected["scale"] | {"case": "arbitrary"}, selected["scale"] | {"case": "later"}, selected["scale"] | {"cursor": "wrong-case"}, selected["scale"] | {"url": "arbitrary"}]:
            changed = selected | {"scale": scale_value}
            with self.assertRaises(runner.Failure):
                runner.validate_workload(value | {"profile": changed}, changed)
    def test_closed_sizes_and_whole_phase_budget(self):
        self.assertEqual(1000, scale.items({}, runner.Failure))
        for count in (1000, 10000, 100000):
            self.assertEqual(count, scale.items({"PERF_ITEMS": str(count)}, runner.Failure))
        for value in ("", "01000", "1000\n", "999", "1000000", "1e3"):
            with self.subTest(value=value), self.assertRaises(runner.Failure):
                scale.items({"PERF_ITEMS": value}, runner.Failure)
        self.assertEqual(30, runner.workload_settings("scale", {})["seconds"])
        with self.assertRaisesRegex(runner.Failure, "phase-budget"):
            runner.workload_settings("scale", {"PERF_DURATION_SECONDS": "180"})
    def test_container_state_rejects_restarts_oom_missing_and_replacement(self):
        identifiers = {"api": "a" * 64, "worker": "b" * 64, "postgres": "c" * 64}
        rows = [{"Id": identifier, "RestartCount": 0, "State": {"Running": True, "OOMKilled": False, "StartedAt": "start"}} for identifier in identifiers.values()]
        def inspect(value, baseline=None):
            with patch.object(runner.local, "command", return_value=json.dumps(value).encode()):
                return scale.container_state(runner.local, {}, identifiers, baseline)
        baseline = inspect(rows)
        self.assertEqual(baseline, inspect(rows, baseline))
        for field, value in [("Running", False), ("OOMKilled", True), ("StartedAt", "different")]:
            changed = copy.deepcopy(rows)
            changed[0]["State"][field] = value
            with self.subTest(field=field), self.assertRaises(runner.Failure):
                inspect(changed, baseline)
        for changed in [rows[:-1], [None, *rows[1:]], [rows[0] | {"State": None}, *rows[1:]], [rows[0], rows[0], rows[2]], [rows[0] | {"RestartCount": 1}, *rows[1:]], [rows[0] | {"Id": "foreign"}, *rows[1:]]]:
            with self.assertRaises(runner.Failure):
                inspect(changed)
    def test_capacity_never_substitutes_client_disk_for_remote_daemon(self):
        with patch.object(runner.local, "command", side_effect=[b"/var/lib/docker", b"ssh://remote"]), patch.object(scale.os, "statvfs") as stats:
            result = scale.preflight(runner.local, {}, 1000)
            self.assertEqual("unavailable", result["capacityCheck"])
            stats.assert_not_called()
        with patch.object(runner.local, "command", side_effect=[b"/var/lib/docker", b"unix:///var/run/docker.sock"]), patch.object(scale.os.path, "isdir", return_value=True), patch.object(scale.os, "statvfs") as stats:
            stats.return_value.f_bavail = 1
            stats.return_value.f_frsize = 4096
            with self.assertRaisesRegex(runner.Failure, "disk-budget"):
                scale.preflight(runner.local, {}, 1000)
    def test_prepare_rejects_wrong_size_and_cross_tenant_data(self):
        page = {"durableTotal": 1000, "items": [{}] * 20, "nextCursor": "cursor"}
        other = {"durableTotal": 0, "items": []}
        sentinel = {"items": [{"publisher": "publisher"}]}
        with patch.object(scale, "request", side_effect=[page, other, sentinel]):
            self.assertEqual({"items": 1000, "publisher": "publisher"}, scale.prepare("unused", 1000, None, runner.Failure))
        for responses in [[page | {"durableTotal": 999}], [page, other | {"items": [{}]}], [page, other, {}]]:
            with patch.object(scale, "request", side_effect=responses), self.assertRaises(runner.Failure):
                scale.prepare("unused", 1000, None, runner.Failure)
    def test_continuation_is_bounded_disjoint_and_generation_bound(self):
        first = {"generation": "g", "items": [{"id": str(i)} for i in range(20)], "nextCursor": "first"}
        second = {"generation": "g", "items": [{"id": str(i)} for i in range(20, 40)], "nextCursor": "second"}
        with patch.object(scale, "request", side_effect=[first, second]) as request:
            self.assertEqual("second", scale.later_cursor("unused", None, runner.Failure))
            self.assertEqual("first", request.call_args_list[1].args[1]["cursor"])
        for bad in [first, second | {"generation": "changed"}, second | {"nextCursor": ""}, second | {"items": []}]:
            with patch.object(scale, "request", side_effect=[first, bad]), self.assertRaises(runner.Failure):
                scale.later_cursor("unused", None, runner.Failure)
        with patch.object(scale, "request", return_value=first | {"items": first["items"][:5]}) as request:
            self.assertEqual("first", scale.later_cursor("unused", None, runner.Failure, terminal=True))
            self.assertEqual(["terminal"], request.call_args.args[1]["tags"])


class PublisherOrderTests(unittest.TestCase):
    def test_continuation_uses_the_selected_order_and_opaque_position(self):
        pages = [{"generation": "g", "items": [{"id": str(i)} for i in range(start, start + 20)], "nextCursor": cursor}
                 for start, cursor in ((0, "first"), (20, "second"))]
        for order in ("publisher_asc", "publisher_desc"):
            with patch.object(scale, "request", side_effect=pages) as request:
                self.assertEqual("second", scale.later_cursor("unused", None, runner.Failure, order=order))
                self.assertEqual(order, request.call_args.args[1]["order"])
                self.assertEqual("first", request.call_args.args[1]["cursor"])
        with self.assertRaises(runner.Failure):
            scale.later_cursor("unused", None, runner.Failure, order="name_desc")

    def test_sparse_order_traversal_rejects_wrong_membership_totals_and_generation(self):
        expected = ["urn:air:scale.example:scale:local-%06d" % i for i in range(897) if i % 5 == 4]
        def reply(api, query, http, failure, token):
            if token == "second-reader":
                return {"durableTotal": 0, "items": []}
            offset = int(query.get("cursor", "0"))
            return {"durableTotal": 179, "generation": "g", "items": [
                {"identifier": identifier, "type": "local", "local": {"resource": {"namespaceId": "sparse"}}} for identifier in expected[offset:offset + 20]],
                "nextCursor": str(offset + 20) if offset + 20 < 179 else ""}
        with patch.object(scale, "request", side_effect=reply) as request:
            self.assertEqual({"orders": 3, "sparseResults": 179, "otherTenantResults": 0},
                             scale.publisher_access("unused", None, runner.Failure, 1000, "sparse"))
            self.assertEqual(30, request.call_count)
        for fault in ("total", "namespace", "duplicate", "generation", "cursor", "tenant"):
            def bad(api, query, http, failure, token):
                page = reply(api, query, http, failure, token)
                if fault == "tenant" and token == "second-reader":
                    page["durableTotal"] = 1
                elif token != "second-reader":
                    if fault == "total":
                        page["durableTotal"] = 180
                    if fault == "namespace":
                        page["items"][0]["local"]["resource"]["namespaceId"] = "other"
                    if fault == "duplicate":
                        page["items"][1] = page["items"][0]
                    if fault == "generation" and query.get("cursor"):
                        page["generation"] = "changed"
                    if fault == "cursor":
                        page["nextCursor"] = ""
                return page
            with self.subTest(fault=fault), patch.object(scale, "request", side_effect=bad), self.assertRaises(runner.Failure):
                scale.publisher_access("unused", None, runner.Failure, 1000, "sparse")


class PublisherTimingSelection(unittest.TestCase):
    def test_closed_timing_selection_keeps_default_and_fixed_experiment(self):
        self.assertEqual(scale.CASES, scale.selected_cases({}, runner.Failure))
        selected = {"PERF_SCALE_WORKLOAD": "publisher-sort"}
        self.assertEqual(("local", "later", *scale.ORDER_CASES), scale.selected_cases(selected, runner.Failure))
        self.assertEqual(30, runner.workload_settings("scale", selected)["seconds"])
        for bad in ({"PERF_SCALE_WORKLOAD": ""}, {"PERF_SCALE_WORKLOAD": "arbitrary"},
                    selected | {"PERF_ITEMS": "100000"}, selected | {"PERF_WORKERS": "3"},
                    selected | {"PERF_WARMUP_SECONDS": "0"}, selected | {"PERF_DURATION_SECONDS": "60"}):
            with self.subTest(environment=bad), self.assertRaises(runner.Failure):
                scale.selected_cases(bad, runner.Failure)
