import copy
import unittest

from fixtures import base, changed, core, document, issue, native, tool


class CaptureTests(unittest.TestCase):
    def test_valid_capture_preserves_exact_bytes_and_caller_context(self):
        content = b'{ "tools": [] }\n'
        context_bytes = b'{"resourceId":"caller-claim"}'
        result = core.report_base(core.capture(content), content, context_bytes, core.attribution("saved file", "2026-10-28T01:00:00+01:00"),
                                  {"version": "4.8.7", "imageId": "local-image", "rulesSha256": "b" * 64})
        self.assertEqual(result["capture"]["inputSha256"], core.digest(content))
        self.assertNotEqual(result["capture"]["inputSha256"], core.digest(core.encoded({"tools": []})))
        self.assertEqual(result["callerContext"], {"resourceId": "caller-claim"})
        self.assertTrue(result["capture"]["callerProvided"])
        self.assertNotIn("catalogVerified", result)

    def test_json_decoding_refuses_duplicate_keys_invalid_unicode_and_numbers(self):
        for content in (b'{"tools":[],"tools":[]}', b'{"a":{"x":1,"x":2}}', b'"\xff"', b'"\\ud800"',
                        b'{"\\udfff":0}', b'NaN', b'Infinity', b'1e999', b'{}{}', b'[' * 70 + b'0' + b']' * 70):
            with self.subTest(content=content[:50]), self.assertRaises(ValueError):
                core.decode(content, core.INPUT_LIMIT)
        with self.assertRaises(ValueError):
            core.decode(b'{}', 1)

    def test_top_level_wrappers_and_malformed_tool_shapes_are_refused(self):
        cases = [None, [], {}, {"tools": None}, {"tools": [None]}, {"tools": ["tool"]}, {"tools": [tool(), tool()]},
                 {"tools": [changed(tool(), name="bad name")]}, {"tools": [changed(tool(), name="a" * 129)]},
                 {"tools": [changed(tool(), description=None)]}, {"tools": [changed(tool(), title=[])]},
                 {"tools": [changed(tool(), inputSchema={"type": "array"})]}, {"tools": [{"name": "x"}]},
                 {"tools": [changed(tool(), outputSchema=None)]}, {"tools": [changed(tool(), annotations=[])]},
                 {"tools": [changed(tool(), _meta=1)]}, {"tools": [changed(tool(), icons=[1])]},
                 {"tools": [changed(tool(), icons=[{}])]}, {"tools": [changed(tool(), annotations={"readOnlyHint": "true"})]},
                 changed(document(), resultType="partial"), changed(document(), nextCursor=""), changed(document(), nextCursor=1)]
        cases += [changed(document(), **{key: {}}) for key in ("jsonrpc", "result", "method", "serverInfo", "serverCard")]
        for value in cases:
            with self.subTest(value=value), self.assertRaises(ValueError):
                core.capture(core.encoded(value))

    def test_exact_capture_bounds_and_pagination(self):
        value = {"tools": [tool("tool_" + str(i)) for i in range(core.TOOL_LIMIT)]}
        self.assertEqual(len(core.capture(core.encoded(value))["tools"]), 128)
        value["tools"].append(tool("overflow"))
        with self.assertRaises(ValueError):
            core.capture(core.encoded(value))
        content = core.encoded(document())
        self.assertEqual(core.capture(content + b' ' * (core.INPUT_LIMIT - len(content))), document())
        with self.assertRaises(ValueError):
            core.capture(content + b' ' * core.INPUT_LIMIT)
        partial = changed(document(), nextCursor="another-page")
        self.assertEqual(core.capture(core.encoded(partial)), partial)

    def test_schema_validation_uses_the_draft_2020_12_metaschema(self):
        core.check_schemas(document())
        output = {"tools": [changed(tool(), outputSchema={"type": "array", "items": {"type": "string"}})]}
        core.check_schemas(core.capture(core.encoded(output)))
        for invalid in ({"type": "object", "required": "left"}, {"type": "object", "properties": {"left": 1}}):
            with self.assertRaises(ValueError):
                core.check_schemas({"tools": [changed(tool(), inputSchema=invalid)]})
        with self.assertRaises(ValueError):
            core.check_schemas({"tools": [changed(tool(), inputSchema={"type": "object", "$schema": "https://example.invalid/schema"})]})

    def test_context_and_attribution_limits(self):
        self.assertEqual(core.context(b'{}'), {})
        for content in (b'[]', b'null', b'{}' + b' ' * core.CONTEXT_LIMIT):
            with self.assertRaises(ValueError):
                core.context(content)
        for source, when in (("", "2026-10-28T00:00:00Z"), ("x\n", "2026-10-28T00:00:00Z"),
                             ("x", "2026-10-28"), ("x", "2026-10-28T00:00:00"), ("x", "2026-99-28T00:00:00Z")):
            with self.subTest(source=source, when=when), self.assertRaises(ValueError):
                core.attribution(source, when)


class ReportTests(unittest.TestCase):
    def result(self, raw, value=None):
        value = document() if value is None else value
        return core.normalize(core.encoded(raw), value, base(value))

    def test_success_requires_complete_evidence_and_preserves_findings(self):
        raw = native()
        clean = self.result(raw)
        self.assertEqual((clean["status"], clean["completedTools"], core.exit_status(clean)), ("completed", 1, 0))
        raw["results"][0]["findings"] = [issue()]
        matched = self.result(raw)
        self.assertEqual((matched["status"], core.exit_status(matched)), ("completed", 1))
        self.assertEqual(matched["results"][0]["findings"], [issue()])
        self.assertNotIn("is_safe", matched)

    def test_all_identity_digests_and_scanner_version_must_agree(self):
        for key in ("inputSha256", "contextSha256", "rulesSha256", "configurationSha256", "scannerVersion"):
            raw = native()
            raw[key] = "wrong"
            with self.subTest(key=key), self.assertRaises(ValueError):
                self.result(raw)

    def test_exact_names_and_counts_are_required(self):
        value = {"tools": [tool("one"), tool("two")]}
        raw = native(value)
        cases = [changed(raw, results=[]), changed(raw, results=raw["results"] * 2)]
        for name in ("wrong", "one", [], None):
            other = copy.deepcopy(raw)
            other["results"][1]["tool_name"] = name
            cases.append(other)
        for malformed in cases:
            with self.subTest(malformed=malformed), self.assertRaises(ValueError):
                self.result(malformed, value)

    def test_partial_failed_and_skipped_are_not_clean(self):
        error = {"analyzer": "YARA", "content_type": "parameters", "message": "Analysis failed"}
        for status, analyzers, errors, issues, expected in (
                ("partial", ["YARA"], [error], [issue()], "partial"),
                ("failed", [], [error], [], "failed"),
                ("failed", [], [error], [issue()], "failed"),
                ("skipped", [], [], [], "skipped")):
            raw = native()
            raw["results"][0].update(status=status, analyzers=analyzers, analyzer_errors=errors, findings=issues)
            result = self.result(raw)
            self.assertEqual(result["status"], expected)
            self.assertEqual(result["completedTools"], 0)
            self.assertEqual(result["results"][0]["findings"], issues)
            self.assertEqual(core.exit_status(result), 2)
        empty = {"tools": []}
        self.assertEqual(self.result(native(empty), empty)["status"], "skipped")
        partial = changed(document(), nextCursor="next")
        result = self.result(native(partial), partial)
        self.assertEqual((result["status"], result["completedTools"]), ("partial", 1))

    def test_inconsistent_completion_and_unselected_analyzers_are_refused(self):
        for updates in ({"status": []}, {"status": "unknown"}, {"analyzers": []}, {"analyzers": ["LLM"]},
                        {"status": "failed"}, {"status": "partial"}, {"status": "skipped"}, {"analyzer_errors": None}, {"findings": {}}):
            raw = native()
            raw["results"][0].update(updates)
            with self.subTest(updates=updates), self.assertRaises(ValueError):
                self.result(raw)

    def test_findings_require_the_exact_tool_analyzer_and_content_type(self):
        cases = [None, changed(issue(), analyzer="LLM"), changed(issue(), severity=[]), changed(issue(), severity="critical"),
                 changed(issue(), summary=None), changed(issue(), threat_category=1), changed(issue(), details={}),
                 changed(issue(), details={"tool_name": "another", "content_type": "description"}),
                 changed(issue(), details={"tool_name": "add_integers", "content_type": []})]
        for finding in cases:
            raw = native()
            raw["results"][0]["findings"] = [finding]
            with self.subTest(finding=finding), self.assertRaises(ValueError):
                self.result(raw)

    def test_truncated_malformed_or_oversized_output_is_refused(self):
        for content in (b'', b'{', b'null', core.encoded(native())[:-1], b' ' * (core.REPORT_LIMIT + 1)):
            with self.subTest(size=len(content)), self.assertRaises(ValueError):
                core.normalize(content, document(), base())


if __name__ == "__main__":
    unittest.main()
