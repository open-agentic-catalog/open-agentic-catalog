"""Fresh-cluster assertions with in-memory responses; no processes or listeners."""

import contextlib
import io
import hashlib
import copy
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch

import agntcy_fresh as f
from agntcy_lab import read_json
from common import Result


class Pagination(unittest.TestCase):
    def test_walk_requires_complete_unique_membership_and_camelcase_tokens(self):
        api = Mock()
        api.document.side_effect = [({"results": [{"identifier": "a", "type": "agent"}], "nextPageToken": "second", "totalCount": 2}, {}),
                                    ({"results": [{"identifier": "b", "type": "mcp"}], "nextPageToken": "", "totalCount": 2}, {})]
        rows, tokens = f.walk(api, {"a": "agent", "b": "mcp"})
        self.assertEqual((set(rows), tokens), ({"a", "b"}, ["second", ""]))
        self.assertIn("pageSize=1", api.document.call_args_list[0].args[1])
        self.assertIn("pageToken=second", api.document.call_args_list[1].args[1])

    def test_missing_duplicate_wrong_type_cycles_and_envelope_changes_fail(self):
        first = {"results": [{"identifier": "a", "type": "agent"}], "nextPageToken": "next", "totalCount": 2}
        second = {"results": [{"identifier": "b", "type": "mcp"}], "nextPageToken": "", "totalCount": 2}
        for change in (lambda row: row.update(totalCount=True), lambda row: row.update(totalCount=1),
                       lambda row: row.update(nextPageToken="next"), lambda row: row.update(nextPageToken=123),
                       lambda row: row.update(results=[]), lambda row: row["results"][0].update(identifier="a"),
                       lambda row: row["results"][0].update(type="agent"), lambda row: row.update(items=row.pop("results"))):
            altered = copy.deepcopy(second)
            change(altered)
            api = Mock()
            api.document.side_effect = [(copy.deepcopy(first), {}), (altered, {})]
            with self.subTest(change=change), self.assertRaises(AssertionError):
                f.walk(api, {"a": "agent", "b": "mcp"})
        api.document.side_effect = [(dict(results=[], nextPageToken="", totalCount=0), {})]
        self.assertEqual(f.walk(api, {}, "type = 'absent'"), ({}, [""]))
        self.assertIn("filter=type+%3D+%27absent%27", api.document.call_args.args[1])


class Native(unittest.TestCase):
    def test_projected_a2a_rejection_preserves_the_original_wrapper(self):
        old = read_json(f.ASSETS / "corpus/a2a.json")["modules"][0]["data"]["card_data"]
        current = read_json(f.ASSETS / "record.json")["modules"][0]["data"]
        mcp = read_json(f.ASSETS / "corpus/mcp.json")["modules"][0]["data"]
        for fault in (None, "retained-payload", "validation-code"):
            lab, api = Mock(), Mock()
            lab.native.return_value = Result(0, json.dumps(old), "")
            replies = [({"data": mcp}, {}), ({"data": current}, {}), ({"id": "namespace"}, {})]
            for index, (data, code) in enumerate(((old, "A2A_PROTOJSON_INVALID"), (current, "A2A_PROTOJSON_INVALID"), (mcp, "MCP_SERVER_CARD_SCHEMA_INVALID"))):
                revision = dict(id="revision-" + str(index), representations=[dict(data=copy.deepcopy(data), validationState="invalid")])
                result = dict(revision=revision, evidence=dict(state="blocked", findings=[dict(code=code)]))
                retained = copy.deepcopy(result)
                if index == 1 and fault == "retained-payload":
                    retained["revision"]["representations"][0]["data"] = current["card_data"]
                if index == 1 and fault == "validation-code":
                    result["evidence"]["findings"][0]["code"] = "UNRELATED_FAILURE"
                replies.extend([({"id": "resource-" + str(index)}, {}), (result, {}), (retained, {})])
            api.document.side_effect = replies
            with self.subTest(fault=fault):
                if fault:
                    with self.assertRaises(AssertionError):
                        f.rejected_artifacts(api, lab)
                else:
                    result = f.rejected_artifacts(api, lab)
                    self.assertEqual(result["agent"]["rejected_projection"]["codes"], ["A2A_PROTOJSON_INVALID"])
                    writes = [call.args[2] for call in api.document.call_args_list if call.args[1].endswith("/versions")]
                    self.assertEqual([row["representations"][0]["data"] for row in writes], [old, current, mcp])

    def test_native_cli_and_cache_are_exact_and_private(self):
        settings = dict(certificate="/owned/ca", private="/owned/private", grpc="127.0.0.1:1234")
        with patch.object(f, "run", return_value=Result(0, "native", "")) as run:
            lab = f.FreshLab(settings)
            lab.native("pull", "synthetic", check=False)
        args = run.call_args.args[0]
        self.assertEqual(args[0], str(f.agntcy_tools.TOOLS / "dirctl-linux-amd64"))
        self.assertEqual(args[1:], ["--server-addr", "127.0.0.1:1234", "--auth-mode", "insecure", "pull", "synthetic"])
        self.assertFalse(run.call_args.kwargs["check"])
        for key in ("TMPDIR", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME"):
            self.assertTrue(Path(lab.env[key]).is_relative_to("/owned/private"))
        for origin in ("http://localhost:1234", "http://192.168.1.1:1234", "https://127.0.0.1:1234", "http://user@127.0.0.1:1234", "http://127.0.0.1:1234/path"):
            with self.subTest(origin=origin), self.assertRaises(ValueError):
                f.loopback(origin, "http")

    def test_corpus_preserves_six_records_five_projections_and_existing_seed(self):
        corpus = read_json(f.ASSETS / "corpus.json")
        self.assertEqual(len(corpus), 6)
        self.assertEqual(len({item["cid"] for item in corpus}), 6)
        self.assertEqual(sum(item["type"] is not None for item in corpus), 5)
        current = next(item for item in corpus if item["name"] == "current-a2a")
        self.assertEqual(current["file"], "record.json")
        self.assertEqual(current["cid"], read_json(f.ASSETS / "lock.json")["cid"])
        for item in corpus:
            self.assertTrue((f.ASSETS / item["file"]).is_file())

    def test_native_cid_or_content_drift_stops_before_later_cases(self):
        first = read_json(f.ASSETS / "corpus.json")[0]
        for results in ([Result(0, "wrong-cid", "")], [Result(0, first["cid"], ""), Result(0, "{}", "")]):
            lab, api = Mock(), Mock()
            lab.native.side_effect = results
            with self.subTest(results=results), self.assertRaises(AssertionError):
                f.inventory(api, lab)
            api.document.assert_not_called()

    def test_actual_raw_schema_errors_for_each_projected_type(self):
        from agntcy_contract import raw_entry_evidence, ENTRY_ERRORS
        schema = read_json(f.mvp.ARD / "spec/schemas/ard-entry.schema.json")
        for media in (f.A2A, f.MCP, "application/ai-catalog+json"):
            entry = dict(identifier="urn:ai:org.agntcy:cid:synthetic", type=media, displayName="Test",
                         data={}, metadata={"agntcy.dir.trust.v1.Status": {}, "agntcy.dir.usage.v1.Metrics": {}})
            self.assertEqual(raw_entry_evidence(entry, schema, "synthetic", media)["errors"], ENTRY_ERRORS)


class Evidence(unittest.TestCase):
    def test_import_and_later_trust_failures_preserve_completed_case_status(self):
        for interrupt_import in (True, False):
            settings = dict(origin=f.ORIGIN, token="private-token", private="/unused/private", peer="https://127.0.0.1:1234", trust="true")

            def qualify(_api, _lab, _private, _output, evidence, save):
                states = ["running", "unrun", "unrun"] if interrupt_import else ["pass"] * 3
                evidence.update(status="running" if interrupt_import else "pass", cases=[dict(kind=kind, status=status)
                    for kind, status in zip(("a2a", "mcp", "agent-skill"), states, strict=True)])
                save()
                if interrupt_import:
                    raise KeyboardInterrupt("owned native import interrupted")

            with self.subTest(interrupt_import=interrupt_import), tempfile.TemporaryDirectory() as directory:
                with patch.object(f, "FreshLab") as lab, patch.object(f, "Captures") as api, \
                        patch.object(f, "inventory", return_value={}), patch.object(f.mvp, "native_artifact", return_value=({}, True)), \
                        patch.object(f, "rejected_artifacts", return_value={}), patch.object(f, "all_raw_entries", return_value={}), \
                        patch.object(f.mvp, "artifact", return_value={}), patch.object(f.mvp, "discovery", return_value={}), \
                        patch.object(f.mvp, "search", return_value={}), patch.object(f.mvp, "conformance", return_value={}), \
                        patch.object(f, "check_cases"), patch.object(f.agntcy_imports, "qualify", side_effect=qualify):
                    api.return_value.captures = []
                    api.return_value.document.side_effect = RuntimeError("trust observer unavailable")
                    lab.return_value.lock = {**read_json(f.ASSETS / "deployment.json"), "cid": "synthetic"}
                    with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                        self.assertEqual(f.execute(settings, Path(directory)), 1)
                    if interrupt_import:
                        api.return_value.document.assert_not_called()
                    else:
                        api.return_value.document.assert_called_once_with("peer", "/_qualification/requests")
                report = json.loads((Path(directory) / "qualification.json").read_text())
                self.assertEqual(report["status"], "fail")
                self.assertEqual([case["status"] for case in report["cases"]], ["pass"] * 4)
                self.assertEqual(report["native_imports"]["status"], "fail" if interrupt_import else "pass")
                self.assertEqual([case["status"] for case in report["native_imports"]["cases"]], ["fail", "unrun", "unrun"] if interrupt_import else ["pass"] * 3)

    def test_setup_and_group_failures_leave_remaining_groups_unrun(self):
        settings = dict(origin=f.ORIGIN, token="private-token", peer="https://127.0.0.1:1234")
        for stage in ("inventory", "artifact", "discovery", "search", "conformance"):
            with tempfile.TemporaryDirectory() as directory:
                with patch.object(f, "FreshLab") as lab, patch.object(f, "Captures") as api, patch.object(f, "inventory") as inventory, \
                        patch.object(f.mvp, "native_artifact", return_value=({}, True)), patch.object(f, "rejected_artifacts", return_value={}), \
                        patch.object(f.mvp, "artifact") as artifact, patch.object(f.mvp, "discovery") as discovery, \
                        patch.object(f.mvp, "search") as search, patch.object(f.mvp, "conformance") as conformance:
                    for operation in (inventory, artifact, discovery, search, conformance):
                        operation.return_value = {}
                    api.return_value.captures = []
                    lab.return_value.lock = {**read_json(f.ASSETS / "deployment.json"), "cid": "synthetic"}
                    {"inventory": inventory, "artifact": artifact, "discovery": discovery, "search": search, "conformance": conformance}[stage].side_effect = RuntimeError("failure private-token")
                    with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                        self.assertEqual(f.execute(settings, Path(directory)), 1)
                report = json.loads((Path(directory) / "qualification.json").read_text())
                self.assertEqual(report["status"], "fail")
                self.assertNotIn("private-token", report["error"])
                states = [case["status"] for case in report["cases"]]
                if stage == "inventory":
                    self.assertEqual(states, ["unrun"] * 4)
                else:
                    index = ("artifact", "discovery", "search", "conformance").index(stage)
                    self.assertEqual(states, ["pass"] * index + ["fail"] + ["unrun"] * (3 - index))


class CapturedBytes(unittest.TestCase):
    def test_bytes_survive_without_json_roundtrip_and_catalog_credentials_are_not_captured(self):
        with tempfile.TemporaryDirectory() as directory:
            api = f.Captures.__new__(f.Captures)
            api.output, api.captures = Path(directory), []
            raw = b'{ "entries" : [] }\n'
            with patch.object(f.LocalHTTP, "request", return_value=(raw, {"content-type": "application/json"})):
                api.request("peer", "/.well-known/ai-catalog.json")
                api.request("catalog", "/api/v1/me")
            self.assertEqual(len(api.captures), 1)
            record = api.captures[0]
            self.assertEqual((api.output / record["file"]).read_bytes(), raw)
            self.assertEqual(record["sha256"], hashlib.sha256(raw).hexdigest())
            self.assertNotIn("token", json.dumps(record))
            api.captures = [{}] * 128
            with patch.object(f.LocalHTTP, "request", return_value=(raw, {})), self.assertRaisesRegex(ValueError, "count exceeded"):
                api.request("peer", "/large")
