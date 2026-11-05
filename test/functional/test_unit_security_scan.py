"""Scanner evidence must prove traffic, selected rules and completion, without tools."""

import copy
import unittest
from unittest import mock

import runner
import security_scan as scan


def message(target="http://api:8080/api/v1/catalog/resources", method="GET", status=200, kind="1"):
    return dict(requestHeader=f"{method} {target} HTTP/1.1\r\nCookie: private\r\n\r\n",
                responseHeader=f"HTTP/1.1 {status} ignored\r\n\r\n", requestBody="", responseBody="",
                type=kind, rtt="0")


def evidence():
    return dict(status="passed", mode="security-active", tool=scan.TOOL, authenticationCalibration="passed", calibration="passed", browser="passed",
                controls=scan.CONTROLS, messages=200, traffic=list(scan.REQUIRED_TRAFFIC),
                drain=dict(jobs=["passiveScan-wait"], errors=0, warnings=0, finished="done"), alerts=[], automation=dict(errors=0, warnings=0, finished="done", jobs=["openapi", "activeScan", "passiveScan-wait"]), rules={"40012": 70, "40018": 106})


class SecurityTests(unittest.TestCase):
    def test_native_history_distinguishes_placeholders_and_real_transport_failures(self):
        placeholder = message("http://api:8080/api", status=0, kind="0")
        good = message(target="http://api:8080/api/v1/catalog/resources?cursor=private")
        self.assertEqual(scan.traffic([placeholder, good]), [("api", "GET", "/api/v1/catalog/resources", 200)])
        self.assertEqual(scan.traffic([message("http://api:8080/12345", status=404, kind="14")]),
                         [("api", "GET", "/{zap-not-found-control}", 404)])
        for data in (message("http://api:8080/12345", status=200, kind="14"), message(status=0), message(status=401), message(status=403), message(status=500),
                     message("http://outside.test/"), message(method="DELETE"), message("http://api:8080/api/v1/sources")):
            with self.subTest(data=data), self.assertRaises(runner.Failure):
                scan.traffic([data])

    def test_rules_must_complete_and_send_attacks(self):
        value = {"scanProgress": ["http://api:8080", {"HostProcess": [
            {"Plugin": ["XSS", "40012", "release", "Complete", "100", "70", "0"]},
            {"Plugin": ["SQL", "40018", "release", "Complete", "100", "106", "0"]}]}]}
        self.assertEqual(scan.completed_rules(value), {"40012": 70, "40018": 106})
        for index, replacement in ((1, "unselected"), (3, "Time Limit"), (5, "0")):
            broken = copy.deepcopy(value)
            broken["scanProgress"][1]["HostProcess"][0]["Plugin"][index] = replacement
            with self.subTest(index=index), self.assertRaises(runner.Failure):
                scan.completed_rules(broken)
        with self.assertRaises(runner.Failure):
            scan.completed_rules(value, "http://localhost:8082")
        with self.assertRaises(runner.Failure):
            scan.completed_rules({"scanProgress": []})

    def test_incomplete_zero_authentication_cleanup_and_child_errors_cannot_pass(self):
        self.assertTrue(scan.valid_evidence(evidence(), "security-active"))
        for field, bad in (("controls", []), ("traffic", []), ("messages", 0), ("browser", "failed"),
                           ("calibration", "failed"), ("authenticationCalibration", "failed"), ("drain", {}), ("rules", {}), ("tool", {}), ("automation", {})):
            value = evidence()
            value[field] = bad
            with self.subTest(field=field):
                self.assertFalse(scan.valid_evidence(value, "security-active"))
        base = dict(profile="security-active", security=evidence(), images={"api": "owned"}, servedVersion={"version": "functional"}, smoke={"resourceCount": 3})
        steps = list(runner.selected_steps("security-active"))
        for completed, failure, cleanup in ((steps[:-1], None, True), (steps, "deadline", True), (steps, None, False)):
            self.assertFalse(runner.finalize(copy.deepcopy(base), completed, failure, cleanup))

    def test_exact_dispositions_do_not_hide_new_routes_risks_or_rules(self):
        alert = dict(pluginId="10096", risk="Low", url="http://localhost:8081/auth/session", method="GET", name="Timestamp", evidence="private", attack="private")
        summarized, unexpected = scan.review([alert])
        self.assertFalse(unexpected)
        self.assertNotIn("private", str(summarized))
        for field, value in (("risk", "Medium"), ("pluginId", "40012"), ("url", "http://api:8080/auth/session"), ("method", "POST")):
            self.assertTrue(scan.review([{**alert, field: value}])[1])

    def test_full_alert_page_cannot_hide_later_findings(self):
        scanner = scan.Scanner("127.0.0.1")
        for count in (0, 999):
            alerts = [{"pluginId": "10096"}] * count
            with self.subTest(count=count), mock.patch.object(scanner, "api", return_value={"alerts": alerts}):
                self.assertEqual(scanner.alerts(), alerts)
        with mock.patch.object(scanner, "api", return_value={"alerts": [{}] * 1000}), self.assertRaisesRegex(runner.Failure, "security-alert-limit"):
            scanner.alerts()

    def test_plan_waits_for_nonempty_completion_and_checks_job_results(self):
        scanner = scan.Scanner("127.0.0.1")
        started = {"started": "now", "finished": "", "error": [], "warn": [], "info": []}
        complete = {**started, "finished": "later", "info": ["Job passiveScan-wait finished, time taken: 00:00:01"]}
        with mock.patch.object(scanner, "api", side_effect=[{"planId": "0"}, started, complete, complete]), mock.patch.object(scan.time, "sleep"):
            self.assertEqual(scanner.plan("passive.yaml")["jobs"], ["passiveScan-wait"])
        for result in ({**complete, "error": ["failed"]}, {**complete, "warn": ["timeout"]}, {**complete, "info": []}):
            with mock.patch.object(scanner, "api", side_effect=[{"planId": "0"}, result, result]), self.assertRaises(runner.Failure):
                scanner.plan("passive.yaml")

    def test_history_pagination_is_complete_unique_and_bounded(self):
        scanner = scan.Scanner("127.0.0.1")
        messages = [{"id": str(n)} for n in range(1, 28)]
        def native(_component, _kind, operation, **params):
            if operation == "numberOfMessages":
                return {"numberOfMessages": "27"}
            start = max(1, params["start"]) - 1
            return {"messages": messages[start:start + params["count"]]}
        with mock.patch.object(scanner, "api", side_effect=native):
            self.assertEqual(scanner.history(), messages)
        for total in (0, 2000):
            with mock.patch.object(scanner, "api", return_value={"numberOfMessages": str(total)}), self.assertRaises(runner.Failure):
                scanner.history()
        with mock.patch.object(scanner, "api", side_effect=[{"numberOfMessages": "27"}, {"messages": messages[:25]}, {"messages": messages[24:26]}]), self.assertRaises(runner.Failure):
            scanner.history()

    def test_pinned_image_and_isolated_network_are_shared_with_compose(self):
        compose = (scan.SOURCE / "compose.yaml").read_text()
        self.assertIn("image: " + scan.TOOL["image"], compose)
        self.assertIn("internal: true", compose)
        self.assertIn("callhome.tel.enabled=false", compose)
        self.assertIn("pull_policy: never", compose)
