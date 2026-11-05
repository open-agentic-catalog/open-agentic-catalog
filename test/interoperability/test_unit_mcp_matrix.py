"""Matrix report, checkpoint and cleanup guards with injected process edges."""

import copy
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from common import Result
import mcp_matrix as matrix
import mcp_runner
import mcp_tools
import peer_runner


def client_report():
    return dict(client="mcp@2.3.0", noSessions=True, peerSafe=True, childrenExited=True,
                matrix=[dict(transport=transport, protocol=version, cases=dict.fromkeys(matrix.CASES, True), replicas=[8080, 8081])
                        for transport, version in sorted(matrix.COMBINATIONS)])


class Admission(unittest.TestCase):
    def test_all_four_combinations_every_case_both_replicas_and_clean_exit_are_required(self):
        expected = client_report()
        self.assertEqual(matrix.validate_result(Result(0, json.dumps(expected), ""), ["private-token"]), expected)
        invalid = []
        for field, value in (("client", "different"), ("noSessions", False), ("peerSafe", False),
                             ("childrenExited", False), ("secret", "private-token")):
            invalid.append(dict(expected, **{field: value}))
        for mutate in (lambda rows: rows.pop(),
                       lambda rows: rows.__setitem__(1, copy.deepcopy(rows[0])),
                       lambda rows: rows[0]["cases"].pop("live"),
                       lambda rows: rows[0]["cases"].update(live=False),
                       lambda rows: rows[0]["cases"].update(unreviewed=True),
                       lambda rows: rows[0].update(replicas=[8080])):
            changed = copy.deepcopy(expected)
            mutate(changed["matrix"])
            invalid.append(changed)
        for data in invalid:
            with self.subTest(data=data), self.assertRaises(AssertionError):
                matrix.validate_result(Result(0, json.dumps(data), ""), ["private-token"])
        for result in (Result(1, json.dumps(expected), ""), Result(0, json.dumps(expected), "warning")):
            with self.assertRaises(AssertionError):
                matrix.validate_result(result, [])
        with self.assertRaises(ValueError):
            matrix.validate_result(Result(0, "not JSON", ""), [])

    def test_unrecognized_checkpoint_cannot_execute_a_restart(self):
        calls = []
        def docker(*args, **_):
            calls.append(args)
            if args[0] == "inspect":
                return Result(0, json.dumps([{"State": {"Running": True}}]), "")
            return Result(0, "client-supplied-command", "")
        with patch.object(matrix, "docker", side_effect=docker), self.assertRaisesRegex(AssertionError, "checkpoint"):
            matrix.run_client("owned-client", "owned-api")
        self.assertFalse(any(call[0] in ("stop", "restart") for call in calls))

    def test_failed_api_exit_cannot_acknowledge_a_checkpoint(self):
        calls = []
        def docker(*args, **_):
            calls.append(args)
            if args[0] == "inspect":
                state = {"Running": True} if args[1] == "owned-client" else {"Running": False, "ExitCode": 1}
                return Result(0, json.dumps([{"Id": "owned-id", "State": state}]), "")
            return Result(0, "http-2026-07-28", "")
        with patch.object(matrix, "docker", side_effect=docker), self.assertRaisesRegex(AssertionError, "drain"):
            matrix.run_client("owned-client", "owned-api")
        self.assertFalse(any("/tmp/continued" in " ".join(call) for call in calls))


class Cleanup(unittest.TestCase):
    def test_early_failure_and_interruption_require_confirmed_owned_cleanup(self):
        for failure in (RuntimeError("peer failure"), KeyboardInterrupt(), mcp_tools.CreationUnconfirmed("unconfirmed")):
            for cleanup_failure in (False, True):
                with self.subTest(failure=type(failure).__name__, cleanup=cleanup_failure), tempfile.TemporaryDirectory() as name:
                    directory = Path(name)
                    calls = []
                    def docker(*args, **_):
                        calls.append(args)
                        if args[:2] == ("network", "inspect"):
                            return Result(0, json.dumps([{"Internal": True}]), "")
                        if args[:2] == ("network", "rm") and cleanup_failure:
                            return Result(1, "", "owned network busy")
                        return Result(0, "", "")
                    with patch.object(matrix, "ROOT", directory), patch.object(mcp_tools, "OUTPUT", directory), \
                         patch.object(matrix, "local_daemon", return_value="amd64"), \
                         patch.object(matrix.mcp_catalog_tools, "installed", return_value={"image": "owned"}), \
                         patch.object(matrix, "fingerprint", return_value="source"), \
                         patch.object(mcp_runner, "build", return_value={}), \
                         patch.object(matrix.shutil, "copyfile"), patch.object(matrix, "run"), \
                         patch.object(matrix, "docker", side_effect=docker), \
                         patch.object(peer_runner, "addresses", return_value=["172.31.0.10"]), \
                         patch.object(mcp_tools, "create", side_effect=failure), \
                         patch.object(mcp_tools, "remove") as remove:
                        with self.assertRaises((RuntimeError, KeyboardInterrupt, AssertionError)):
                            matrix.check()
                    report = json.loads(next(directory.glob("matrix-*/report.json")).read_text())
                    self.assertFalse(report["success"])
                    self.assertRegex(report["network"]["name"], r"^catalog-mcp-matrix-[a-f0-9]{12}-network$")
                    self.assertEqual(report["cleanup"], not cleanup_failure and not isinstance(failure, mcp_tools.CreationUnconfirmed))
                    self.assertFalse(list(directory.glob("matrix-*/private")))
                    self.assertEqual(len(report["containers"]), 1)
                    remove.assert_called_once_with(report["containers"])
                    self.assertTrue(any(call[:2] == ("network", "rm") for call in calls))


class RefusalDiagnostics(unittest.TestCase):
    def test_refusal_text_never_enters_machine_evidence(self):
        from mcp_diagnostics import ToolRefusal, failure_evidence, read_failure
        for code in ("api_failure", "api_denied", "output_limit", "unclassified"):
            error = ToolRefusal([dict(type="text", text=code + ": private-token body")])
            evidence = failure_evidence(ExceptionGroup("private-token", [ValueError("private-token"), error]), "http/2025-11-25/restart-continuation")
            self.assertEqual(evidence["toolRefusal"], code)
            self.assertEqual(evidence["case"], "restart-continuation")
            self.assertNotIn("private-token", json.dumps(evidence) + str(error))
            self.assertEqual(read_failure(json.dumps(evidence)), evidence)
        for content in (None, [], [dict(type="image", text="api_failure: private-token")],
                        [dict(type="text", text="private-token: api_failure")],
                        [dict(type="text", text="api_failure: " + "x" * 2000)]):
            self.assertEqual(ToolRefusal(content).code, "unclassified")
        self.assertIsNone(failure_evidence(error, "http/private-token/restart-continuation"))
        for raw in ('private-token', '[]', json.dumps({**evidence, "toolRefusal":"private-token"}),
                    json.dumps({**evidence, "body":"private-token"}),
                    json.dumps({**evidence, "protocol": []}), ' ' * 1025):
            self.assertIsNone(read_failure(raw))
