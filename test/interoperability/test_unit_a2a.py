"""Pure A2A selection/result/traffic tests; no SDK imports or external tools."""

import copy
import unittest
from unittest.mock import patch

import a2a_contract as contract
import a2a_tools as tools
from common import Result


def valid_result():
    card = {"name": "fixture", "description": "owned", "version": "1", "capabilities": {},
            "supportedInterfaces": [{"url": contract.URL, "protocolBinding": "JSONRPC", "protocolVersion": "1.0"}],
            "defaultInputModes": ["text/plain"], "defaultOutputModes": ["text/plain"], "skills": [{"id": "echo"}]}
    task = {"id": "native-task", "contextId": contract.CONTEXT_ID, "status": {"state": "TASK_STATE_COMPLETED"},
            "artifacts": [{"artifactId": "native-artifact", "parts": [{"text": contract.EXPECTED, "mediaType": "text/plain"}]}],
            "history": [{"messageId": contract.MESSAGE_ID, "contextId": contract.CONTEXT_ID, "role": "ROLE_USER",
                         "parts": [{"text": contract.MESSAGE, "mediaType": "text/plain"}]},
                        {"messageId": "working-message", "role": "ROLE_AGENT", "parts": [{"text": "Processing request..."}]}]}
    return {"card": card, "card_sha256": contract.select_card(card), "send": task,
            "readback": copy.deepcopy(dict(task, history=task["history"][-1:])),
            "selection_refusals": ["version", "binding", "endpoint", "ambiguous"]}


def observations():
    common = {"authorization": False, "cookie": False, "query": False, "version": "1.0", "status": 200}
    send = {"message": {"messageId": contract.MESSAGE_ID, "contextId": contract.CONTEXT_ID, "role": "ROLE_USER",
                        "parts": [{"text": contract.MESSAGE, "mediaType": "text/plain"}]},
            "configuration": {"acceptedOutputModes": ["text/plain"], "historyLength": 2}}
    return [dict(common, method="GET", path=contract.CARD_PATH, rpc=None),
            dict(common, method="POST", path="/", rpc="SendMessage", jsonrpc="2.0", request_id="send-id", params=send),
            dict(common, method="POST", path="/", rpc="GetTask", jsonrpc="2.0", request_id="get-id", params={"id": "native-task", "historyLength": 1})]


class Contract(unittest.TestCase):
    def test_card_selection_refuses_fallback_and_unsafe_endpoints(self):
        card = valid_result()["card"]
        contract.select_card(card)
        for key, value in (("url", "http://127.0.0.1:9443/"), ("url", "https://external.invalid/"),
                           ("protocolBinding", "HTTP+JSON"), ("protocolVersion", "0.3")):
            with self.subTest(key=key, value=value):
                changed = copy.deepcopy(card)
                changed["supportedInterfaces"][0][key] = value
                with self.assertRaises(ValueError):
                    contract.select_card(changed)
        for key, value in (("supportedInterfaces", []), ("supportedInterfaces", card["supportedInterfaces"] * 2),
                           ("capabilities", {"streaming": True}), ("capabilities", {"extendedAgentCard": True}),
                           ("securitySchemes", {"secret": {}}), ("defaultInputModes", ["text"]), ("name", "")):
            with self.subTest(key=key, value=value):
                with self.assertRaises(ValueError):
                    contract.select_card(dict(card, **{key: value}))

    def test_native_result_corruptions_are_not_success(self):
        contract.check_result(valid_result())
        for case in ("empty task", "wrong context", "working", "wrong text", "extra artifact", "wrong role", "wrong request", "changed readback", "unbounded history", "oldest history", "missing history", "digest", "missing refusal"):
            with self.subTest(case=case):
                result = valid_result()
                task = result["send"]
                if case == "empty task":
                    task["id"] = ""
                elif case == "wrong context":
                    task["contextId"] = "other"
                elif case == "working":
                    task["status"]["state"] = "TASK_STATE_WORKING"
                elif case == "wrong text":
                    task["artifacts"][0]["parts"][0]["text"] = "Hello, World!"
                elif case == "extra artifact":
                    task["artifacts"].append(copy.deepcopy(task["artifacts"][0]))
                elif case == "wrong role":
                    task["history"][0]["role"] = "ROLE_AGENT"
                elif case == "wrong request":
                    task["history"][0]["messageId"] = "other"
                elif case == "changed readback":
                    result["readback"]["id"] = "other"
                elif case == "unbounded history":
                    result["readback"]["history"] = copy.deepcopy(task["history"])
                elif case == "oldest history":
                    result["readback"]["history"] = copy.deepcopy(task["history"][:1])
                elif case == "missing history":
                    del result["readback"]["history"]
                elif case == "digest":
                    result["card_sha256"] = "wrong"
                else:
                    result["selection_refusals"].pop()
                if case in ("empty task", "wrong context", "working", "wrong text", "extra artifact", "wrong role", "wrong request"):
                    # Corrupt both native responses consistently. An unrelated
                    # send/readback mismatch must not mask a missing content guard.
                    result["readback"] = copy.deepcopy(dict(task, history=task["history"][-1:]))
                with self.assertRaises(AssertionError):
                    contract.check_result(result)

    def test_real_wire_oracle_rejects_unexpected_calls_and_credentials(self):
        result = valid_result()
        contract.check_traffic(observations(), result)
        for key, value in (("authorization", True), ("cookie", True), ("query", True), ("version", "0.3"),
                           ("rpc", "message/send"), ("jsonrpc", "1.0"), ("status", 500), ("request_id", "get-id")):
            with self.subTest(key=key):
                actual = observations()
                actual[1][key] = value
                with self.assertRaises(AssertionError):
                    contract.check_traffic(actual, result)
        for case in ("poll", "refetch", "wrong readback", "wrong history bound", "async", "wrong input"):
            with self.subTest(case=case):
                actual = observations()
                if case in ("poll", "refetch"):
                    actual.append(copy.deepcopy(actual[2 if case == "poll" else 0]))
                elif case == "wrong readback":
                    actual[2]["params"]["id"] = "other"
                elif case == "wrong history bound":
                    actual[2]["params"]["historyLength"] = 2
                elif case == "async":
                    actual[1]["params"]["configuration"]["returnImmediately"] = True
                else:
                    actual[1]["params"]["message"]["parts"][0]["text"] = "other"
                with self.assertRaises(AssertionError):
                    contract.check_traffic(actual, result)

    def test_incomplete_or_failed_runs_cannot_pass(self):
        report = {"scope": "direct-control", "cleanup": True, "cases": dict.fromkeys(contract.CASES, True),
                  "native_exit": 0, "result": valid_result(), "observations": observations()}
        contract.complete(report)
        for key, value in (("cleanup", False), ("creation_unconfirmed", True), ("cases", {}), ("native_exit", 2), ("scope", "governed")):
            with self.subTest(key=key):
                with self.assertRaises(AssertionError):
                    contract.complete(dict(report, **{key: value}))

    def test_cleanup_attempts_every_owned_name_and_keeps_failures(self):
        with patch.object(tools, "docker", side_effect=[Result(1, "", "daemon failed"), Result(1, "", "No such container")]) as docker:
            with self.assertRaisesRegex(RuntimeError, "cleanup failed"):
                tools.remove(["one", "two"])
            self.assertEqual([call.args[:3] for call in docker.call_args_list], [("rm", "-f", "two"), ("rm", "-f", "one")])
