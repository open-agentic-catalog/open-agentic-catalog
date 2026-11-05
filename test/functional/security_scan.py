"""Bounded ZAP qualification in the existing invocation-owned packaged fixture."""

import http.client
import json
import os
from pathlib import Path
import secrets
import re
import shutil
import sys
import time
from urllib.parse import urlencode, urlsplit

from runner import Failure, command, read_document, write_document

ROOT = Path(__file__).resolve().parents[2]
SOURCE = ROOT / "test/security"
TOOL = read_document(SOURCE / "tool.json")
ACTIVE_RULES = {"40012", "40018"}
# Rationale and revisit conditions belong to test/security/README.md.
DISPOSITIONS = {
    ("10096", "Low", "localhost", "/auth/session", "GET"),
    ("10109", "Informational", "localhost", "/catalog", "GET"),
    ("10109", "Informational", "localhost", "/explore", "GET"),
    ("10112", "Informational", "api", "/api/v1/catalog/resources", "GET"),
    ("10112", "Informational", "localhost", "/auth/login", "POST"),
}
CONTROLS = ["listNamespaces", "searchCatalog", "browseCatalog", "createNamespace", "getNamespace", "updateNamespace"]
REQUIRED_TRAFFIC = {
    ("api", "GET", "/api/v1/namespaces", 200),
    ("api", "POST", "/api/v1/namespaces", 201),
    ("api", "GET", "/api/v1/namespaces/{id}", 200),
    ("api", "PATCH", "/api/v1/namespaces/{id}", 200),
    ("api", "POST", "/api/v1/catalog/search", 200),
    ("api", "GET", "/api/v1/catalog/resources", 200),
    ("localhost", "GET", "/catalog", 200),
    ("localhost", "GET", "/explore", 200),
    ("localhost", "GET", "/auth/session", 200),
    ("localhost", "POST", "/auth/login", 200),
    ("localhost", "POST", "/auth/logout", 204),
    ("localhost", "POST", "/_catalog/api/v1/catalog/search", 200),
    ("localhost", "GET", "/_catalog/api/v1/catalog/resources", 200),
    ("localhost", "GET", "/assets/*", 200),
}


def setup():
    """Only supervisor-created paths and identities are accepted by this entry point."""
    directory = Path(os.environ["CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY"]) / "zap"
    directory.mkdir(mode=0o755)
    for name in ("active.yaml", "passive.yaml", "calibration.py"):
        shutil.copyfile(SOURCE / name, directory / name)
    ui = dict(listen="0.0.0.0:8081", environment="development", origin="http://localhost:8081", basePath="",
              upstream="http://api:8080", assets="/app/assets", cookieName="catalog-security", keysFile="/fixture/keys.json",
              sessionSeconds=3600, requestBytes=2097152, responseBytes=16777216, timeoutSeconds=10,
              authMode="development", developmentUsersFile="/fixture/users.json")
    for name, value in (("ui.json", ui), ("keys.json", [dict(hash=secrets.token_hex(64), block=secrets.token_hex(32))]),
                        ("users.json", [dict(alias="local-reader", label="local-reader", token="local-reader")])):
        # The parent fixture directory is private; leaves must be readable by
        # the non-root UI container. They are destroyed by supervisor cleanup.
        (directory / name).write_text(json.dumps(value))
    # ZAP's non-root user writes Automation Framework state here.
    directory.chmod(0o777)


class Scanner:
    def __init__(self, host):
        self.host = host
        self.deadline = time.monotonic() + 480

    def request(self, method, url, body=None, headers=None):
        if time.monotonic() >= self.deadline:
            raise Failure("security-deadline")
        connection = http.client.HTTPConnection(self.host, 8090, timeout=10)
        try:
            connection.request(method, url, body, headers or {})
            reply = connection.getresponse()
            raw = reply.read(2 * 1024 * 1024 + 1)
            if len(raw) > 2 * 1024 * 1024:
                raise Failure("security-response-limit")
            return reply.status, dict(reply.getheaders()), raw
        finally:
            connection.close()

    def api(self, component, kind, operation, **params):
        status, _, raw = self.request("GET", f"/JSON/{component}/{kind}/{operation}/?" + urlencode(params))
        value = json.loads(raw)
        if status != 200 or "code" in value:
            raise Failure("security-tool-api-failed")
        return value

    def wait(self, condition, seconds=90):
        deadline = min(self.deadline, time.monotonic() + seconds)
        while time.monotonic() < deadline:
            if condition():
                return
            time.sleep(0.25)
        raise Failure("security-incomplete")

    def ready(self):
        try:
            return self.api("core", "view", "version")["version"] == TOOL["version"]
        except (OSError, http.client.HTTPException):
            return False

    def plan(self, name):
        plan = self.api("automation", "action", "runPlan", filePath="/zap/wrk/" + name)["planId"]
        self.wait(lambda: bool(self.api("automation", "view", "planProgress", planId=plan).get("finished")), 240)
        result = self.api("automation", "view", "planProgress", planId=plan)
        if result.get("error") != [] or result.get("warn") != [] or not result.get("started"):
            # Only inspected fixed tool diagnostics; raw plans and HTTP sessions
            # remain in the disposable container, never retained as reports.
            raise Failure("security-plan-failed")
        jobs = {"passive.yaml": ["passiveScan-wait"], "active.yaml": ["openapi", "activeScan", "passiveScan-wait"],
                "calibration.yaml": ["requestor", "activeScan", "passiveScan-wait"]}[name]
        if not all(any(line.startswith("Job " + job + " finished, time taken:") for line in result.get("info", [])) for job in jobs):
            raise Failure("security-plan-incomplete")
        if name == "active.yaml" and "Job openapi added 2 URLs" not in result["info"]:
            raise Failure("security-import-incomplete")
        return {"jobs": jobs, "started": result["started"], "finished": result["finished"], "errors": 0, "warnings": 0}

    def control(self, method, path, body=None, etag=None, expected=200, token="local-admin"):
        headers = {"Authorization": "Bearer " + token, "Content-Type": "application/json"}
        if method in ("POST", "PATCH"):
            headers["Idempotency-Key"] = secrets.token_hex(16)
        if etag:
            headers["If-Match"] = etag
        status, response_headers, raw = self.request(method, "http://api:8080" + path, None if body is None else json.dumps(body), headers)
        if status != expected:
            raise Failure("security-authenticated-control-failed-" + str(status))
        from response_contract import observe
        observe(method, path, status, response_headers, raw, (expected,))
        return json.loads(raw), {k.lower(): v for k, v in response_headers.items()}

    def history(self):
        """Read complete native history with bounded pages and no duplicated IDs."""
        count = int(self.api("core", "view", "numberOfMessages")["numberOfMessages"])
        if not 20 <= count < 2000:
            raise Failure("security-traffic-incomplete")
        messages = []
        # Core history uses one-based starts (0 aliases its first row).
        for offset in range(0, count, 25):
            messages.extend(self.api("core", "view", "messages", start=offset + 1, count=25)["messages"])
        if len(messages) != count or len({m["id"] for m in messages}) != count:
            raise Failure("security-history-incomplete")
        return messages

    def alerts(self):
        alerts = self.api("alert", "view", "alerts", start=0, count=1000)["alerts"]
        # A full page may hide later findings. Fail closed rather than review
        # an incomplete report as though it described the whole session.
        if len(alerts) >= 1000:
            raise Failure("security-alert-limit")
        return alerts


def summarize(alerts):
    """Never retain cookies, request headers, response bodies or attack evidence."""
    return sorted({(str(a["pluginId"]), a["risk"], urlsplit(a["url"]).hostname,
                    urlsplit(a["url"]).path, a.get("method", ""), a["name"]) for a in alerts})


def review(alerts):
    """All unexpected findings fail, including informational and low risks."""
    summaries = summarize(alerts)
    return summaries, [item for item in summaries if item[:5] not in DISPOSITIONS]


def run():
    env = dict(os.environ)
    directory = Path(env["CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY"]) / "zap"
    state = Path(env["CATALOG_FUNCTIONAL_STATE_DIRECTORY"])
    scanner = Scanner(env["CATALOG_SECURITY_PROXY_HOST"])
    evidence = {"status": "incomplete", "mode": env["CATALOG_FUNCTIONAL_PROFILE"], "tool": TOOL, "controls": [], "alerts": []}
    try:
        scanner.wait(scanner.ready)
        addons = scanner.api("autoupdate", "view", "installedAddons")["installedAddons"]
        if {a["id"]: a["version"] for a in addons} != TOOL["addons"]:
            raise Failure("security-addon-mismatch")
        # Global scanner exclusions and an isolated network supplement the
        # context. No crawler, URL-fetch operation or callback rule is enabled.
        scanner.api("core", "action", "excludeFromProxy", regex=r"^https?://(?!(api:8080|localhost:808[12])(/|$)).*")
        # A valid control must fail when its real API identity is lost. Clear
        # this deliberate calibration exchange before application accounting.
        scanner.control("GET", "/api/v1/namespaces")
        try:
            scanner.control("GET", "/api/v1/namespaces", token="scanner-invalid-control")
        except Failure as error:
            if str(error) != "security-authenticated-control-failed-401":
                raise
        else:
            raise Failure("security-authentication-calibration-failed")
        evidence["authenticationCalibration"] = "passed"
        scanner.api("core", "action", "newSession", overwrite="true")
        calibrate(scanner, directory, evidence)
        scanner.api("core", "action", "newSession", overwrite="true")
        scanner.api("replacer", "action", "addRule", description="owned-api-identity", enabled="true", matchType="REQ_HEADER",
                    matchRegex="false", matchString="Authorization", replacement="Bearer local-admin", url=r"http://api:8080/.*")
        for name, method, path, body in (
                ("listNamespaces", "GET", "/api/v1/namespaces", None),
                ("searchCatalog", "POST", "/api/v1/catalog/search", {"text": "", "limit": 2}),
                ("browseCatalog", "GET", "/api/v1/catalog/resources?limit=2", None)):
            data, _ = scanner.control(method, path, body)
            if name != "listNamespaces" and (len(data["items"]) != 2 or not data.get("nextCursor")):
                raise Failure("security-seeded-read-control-failed")
            evidence["controls"].append(name)
        data, headers = scanner.control("POST", "/api/v1/namespaces", {"displayName": "Security control", "slug": "security-control"}, expected=201)
        namespace = data["id"]
        evidence["controls"].append("createNamespace")
        _, headers = scanner.control("GET", "/api/v1/namespaces/" + namespace)
        evidence["controls"].append("getNamespace")
        scanner.control("PATCH", "/api/v1/namespaces/" + namespace, {"displayName": "Security updated", "reason": "Owned scanner control"}, headers["etag"])
        evidence["controls"].append("updateNamespace")
        command(["node", "catalog-ui/scripts/security-test.mjs", "http://" + scanner.host + ":8090"], env, 90)
        evidence["browser"] = "passed"
        if evidence["mode"] == "security-active":
            evidence["automation"] = scanner.plan("active.yaml")
            evidence["rules"] = completed_rules(scanner.api("ascan", "view", "scanProgress", scanId="0"))
        else:
            evidence["automation"] = scanner.plan("passive.yaml")
        if scanner.api("pscan", "view", "recordsToScan")["recordsToScan"] != "0":
            raise Failure("security-passive-incomplete")
        scanner.control("GET", "/api/v1/namespaces")
        evidence["drain"] = scanner.plan("passive.yaml")
        messages = scanner.history()
        evidence["messages"] = len(messages)
        evidence["traffic"] = traffic(messages)
        if not REQUIRED_TRAFFIC.issubset({tuple(row) for row in evidence["traffic"]}):
            raise Failure("security-required-route-missing")
        evidence["alerts"], unexpected = review(scanner.alerts())
        if unexpected:
            raise Failure("security-unreviewed-findings")
        evidence["status"] = "passed"
    except Failure as error:
        evidence["failure"] = str(error)
        raise
    finally:
        write_document(state / "security.json", evidence)


def calibrate(scanner, directory, evidence):
    """The same real rules must detect insecure controls and clear fixed controls."""
    plan = {"env": {"contexts": [{"name": "calibration", "urls": ["http://localhost:8082"]}],
                    "parameters": {"failOnError": True, "failOnWarning": True}}, "jobs": [
        {"type": "requestor", "requests": [{"url": "http://localhost:8082/" + path + "?q=control", "responseCode": 200} for path in ("bad", "good", "sql")]},
        {"type": "activeScan", "parameters": {"context": "calibration", "threadPerHost": 1, "maxScanDurationInMins": 2, "maxRuleDurationInMins": 1},
         "policyDefinition": {"defaultThreshold": "Off", "defaultStrength": "Low", "rules": [{"id": int(rule), "threshold": "Medium", "strength": "Low"} for rule in sorted(ACTIVE_RULES)]}},
        {"type": "passiveScan-wait", "parameters": {"maxDuration": 1}}]}
    # JSON is valid YAML; the maintained application plans remain YAML files.
    (directory / "calibration.yaml").write_text(json.dumps(plan))
    scanner.plan("calibration.yaml")
    completed_rules(scanner.api("ascan", "view", "scanProgress", scanId="0"), "http://localhost:8082")
    alerts = scanner.alerts()
    found = {(str(a["pluginId"]), urlsplit(a["url"]).path) for a in alerts}
    if not {("10020", "/bad"), ("40012", "/bad"), ("40018", "/sql")}.issubset(found):
        evidence["calibrationAlerts"] = summarize(alerts)
        raise Failure("security-calibration-missing-detection")
    if any(rule in {"10020", *ACTIVE_RULES} and path == "/good" for rule, path in found):
        raise Failure("security-calibration-fixed-control-failed")
    evidence["calibration"] = "passed"


def completed_rules(value, expected_host="http://api:8080"):
    """A completed job is insufficient if a rule timed out or sent no attacks."""
    try:
        progress = value["scanProgress"]
        if [entry for entry in progress if isinstance(entry, str)] != [expected_host]:
            raise Failure("security-rule-target-mismatch")
        rows = [entry["Plugin"] for group in progress if isinstance(group, dict) for entry in group["HostProcess"]]
        if {row[1] for row in rows} != ACTIVE_RULES or len(rows) != 2:
            raise Failure("security-rule-selection-mismatch")
        if any(row[3] != "Complete" or not 0 < int(row[5]) < 2000 for row in rows):
            raise Failure("security-rule-incomplete")
        return {row[1]: int(row[5]) for row in rows}
    except (KeyError, IndexError, TypeError, ValueError) as error:
        raise Failure("security-rule-report-invalid") from error


def traffic(messages):
    """Reduce native HTTP history to safe route/status evidence and reject scope loss."""
    result = set()
    for message in messages:
        method, target, _ = message["requestHeader"].splitlines()[0].split(" ", 2)
        url = urlsplit(target)
        status = int(message["responseHeader"].splitlines()[0].split(" ")[1])
        # Type 0, zero-duration, empty responses are ZAP's unsent parent
        # nodes. Do not confuse them with proxied/scanned transport failures.
        if (message.get("type") == "0" and message.get("rtt") == "0" and status == 0
                and method == "GET" and not message.get("requestBody") and not message.get("responseBody")
                and url.netloc in ("api:8080", "localhost:8081")):
            continue
        if not 100 <= status <= 599:
            raise Failure("security-transport-incomplete")
        path = re.sub(r"/namespaces/[0-9a-f-]{36}$", "/namespaces/{id}", url.path)
        if url.scheme != "http" or url.netloc not in ("api:8080", "localhost:8081") or status in (401, 403) or status >= 500:
            raise Failure("security-unexpected-traffic")
        # The pinned scanner obtains root and numeric not-found baselines.
        # This temporary GET is not an imported operation or an injection hit.
        if (message.get("type") in ("0", "14") and url.netloc == "api:8080" and method == "GET"
                and status == 404 and (path in ("", "/") or re.fullmatch(r"/-?[0-9]{1,20}", path))):
            result.add(("api", "GET", "/{zap-not-found-control}", 404))
            continue
        if url.hostname == "api" and (method, path) not in {(r[1], r[2]) for r in REQUIRED_TRAFFIC if r[0] == "api"}:
            raise Failure("security-operation-out-of-scope")
        # Asset names are build-dependent; no body, cookie or URL query survives.
        if path.startswith("/assets/"):
            path = "/assets/*"
        result.add((url.hostname, method, path, status))
    return sorted(result)


def valid_evidence(value, mode):
    """The supervisor independently rejects missing scanner and journey evidence."""
    if not isinstance(value, dict) or mode not in ("security-passive", "security-active"):
        return False
    try:
        valid = (value.get("status") == "passed" and value.get("mode") == mode and value.get("tool") == TOOL
                 and value.get("authenticationCalibration") == "passed" and value.get("calibration") == "passed" and value.get("browser") == "passed"
                 and value.get("controls") == CONTROLS and type(value.get("messages")) is int and 20 <= value["messages"] < 2000
                 and REQUIRED_TRAFFIC.issubset({tuple(row) for row in value["traffic"]})
                 and value["automation"]["errors"] == 0 and value["automation"]["warnings"] == 0
                 and bool(value["automation"]["finished"])
                 and value["drain"]["jobs"] == ["passiveScan-wait"] and value["drain"]["errors"] == 0
                 and value["drain"]["warnings"] == 0 and bool(value["drain"]["finished"])
                 and all(tuple(row[:5]) in DISPOSITIONS for row in value["alerts"])
                 and value["automation"]["jobs"] == (["openapi", "activeScan", "passiveScan-wait"] if mode == "security-active" else ["passiveScan-wait"]))
        if mode == "security-active":
            valid = valid and set(value["rules"]) == ACTIVE_RULES and all(type(n) is int and 0 < n < 2000 for n in value["rules"].values())
        return bool(valid)
    except (KeyError, TypeError, ValueError):
        return False


def main():
    action = sys.argv[1:]
    if action == ["install"]:
        command(["docker", "pull", TOOL["image"]], os.environ, 300)
    elif action == ["check"]:
        command(["docker", "image", "inspect", TOOL["image"]], os.environ, 10, capture=True)
    elif action == ["setup"]:
        setup()
    elif action == ["run"]:
        run()
    else:
        raise Failure("security-command-invalid")


if __name__ == "__main__":
    main()
