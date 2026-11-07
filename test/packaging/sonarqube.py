"""Pinned, local-only SonarQube developer dashboard and source ingestion."""

import base64
import fcntl
import hashlib
import http.client
import json
import os
from pathlib import Path, PurePosixPath
import re
import secrets
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request

from application_coverage import encode
from sonarqube_coverage import lcov_lines, python_coverage, tooling_reports, validate_native_lcov
from unit_coverage import parse_profile

ROOT = Path(__file__).resolve().parents[2]
CONFIG = ROOT / "test/sonarqube"
STATE = ROOT / "output/sonarqube"
LABEL = "org.open-agentic-catalog.sonarqube.owner"
PROJECT = "open-agentic-catalog"
PROFILE = "Open Agentic Catalog"
MODULE = "github.com/open-agentic-catalog/open-agentic-catalog/"
COVERAGE_MOUNT = "/coverage"
EXTENSIONS = {".go", ".mod", ".sum", ".ts", ".tsx", ".mjs", ".js", ".py", ".css", ".json", ".yaml", ".yml", ".sh"}
EXCLUDED = ("schemas/upstream/", "specs/", "test/clientcompat/go/generated/", "documentation/src/content/docs/generated/")


class Failure(Exception):
    pass


def command(args, *, environment=None, data=None, seconds=60, allowed=(0,)):
    # Docker/Go processes are bounded; their raw errors can include local secrets.
    with tempfile.TemporaryFile() as output:
        try:
            result = subprocess.run(args, cwd=ROOT, env=environment, input=data, stdout=output,
                                    stderr=subprocess.STDOUT, timeout=seconds, check=False)
        except subprocess.TimeoutExpired:
            raise Failure("command deadline exceeded") from None
        output.seek(0)
        raw = output.read((8 << 20) + 1)
    if len(raw) > 8 << 20:
        raise Failure("command output limit exceeded")
    if result.returncode not in allowed:
        error = Failure("command failed: " + args[0] + " " + args[1])
        error.output = raw
        raise error
    return raw


def private_directory(path):
    if path.is_symlink():
        raise Failure("refusing symlink state")
    path.mkdir(mode=0o700, parents=True, exist_ok=True)
    path.chmod(0o700)


def write_json(path, value):
    if path.is_symlink():
        raise Failure("refusing symlink state")
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600)
    with os.fdopen(descriptor, "w") as target:
        json.dump(value, target, indent=2)
        target.write("\n")


def selected(name):
    path = PurePosixPath(name)
    return (not path.is_absolute() and ".." not in path.parts and not any(part.startswith(".") for part in path.parts)
            and not any(part in {"node_modules", "dist", "coverage", "output"} for part in path.parts)
            and not name.startswith(EXCLUDED) and path.suffix in EXTENSIONS
            and (path.parts[0] in {"api", "cmd", "internal", "migrations", "schemas", "catalog-ui", "test", "documentation", "deployments", "config"}
                 or name in {"go.mod", "go.sum"}))


def snapshot(destination):
    paths = command(["git", "ls-files", "--cached", "--others", "--exclude-standard", "-z"]).decode().split("\0")
    digest = hashlib.sha256()
    count = 0
    for name in sorted(set(filter(selected, filter(None, paths)))):
        source = ROOT / name
        if not source.exists():  # An unstaged deletion is absent from the current source.
            continue
        if source.is_symlink() or not source.is_file() or not source.resolve().is_relative_to(ROOT):
            raise Failure("source must be a regular repository file")
        raw = source.read_bytes()
        if len(raw) > 16 << 20:
            raise Failure("source file exceeds snapshot bound")
        target = destination / name
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(raw)
        digest.update(name.encode() + b"\0" + hashlib.sha256(raw).digest())
        count += 1
    if not count:
        raise Failure("empty source snapshot")
    return {"digest": "sha256:" + digest.hexdigest(), "files": count,
            "commit": command(["git", "rev-parse", "HEAD"]).decode().strip()}


def coverage(raw, kind, sources):
    """Normalize explicit existing reports to the same snapshot paths, never invent hits."""
    if kind == "go":
        try:
            blocks = parse_profile(raw.decode())
        except ValueError:
            raise Failure("invalid atomic Go coverage profile") from None
        normalized = {}
        for (filename, position), counts in blocks.items():
            path = filename.removeprefix(MODULE)
            if path.startswith("internal/a2apb/"):
                continue
            if not selected(path) or not (sources / path).is_file():
                raise Failure("Go coverage source is outside the scan snapshot")
            key = (path, position)
            if key in normalized:
                raise Failure("duplicate normalized Go coverage path")
            normalized[key] = counts
        if not normalized:
            raise Failure("empty Go coverage")
        return encode(normalized)
    else:
        try:
            lines = lcov_lines(raw.decode())
        except ValueError:
            raise Failure("invalid LCOV line framing") from None
        normalized = []
        files = 0
        in_record = False
        rows = re.compile(r"(?:DA:[1-9][0-9]*,[0-9]+(?:,[^,]+)?|BRDA:[1-9][0-9]*,[0-9]+,[0-9]+,(?:[0-9]+|-)|FN:[1-9][0-9]*,.+|FNDA:[0-9]+,.+|(?:FNF|FNH|LF|LH|BRF|BRH):[0-9]+)")
        for line in lines:
            if line.startswith("SF:"):
                if in_record:
                    raise Failure("unterminated LCOV record")
                in_record = True
                path = line[3:]
                if path.startswith(str(ROOT) + "/"):
                    path = path[len(str(ROOT)) + 1:]
                if path.startswith("src/"):
                    path = "catalog-ui/" + path
                if not selected(path) or not (sources / path).is_file():
                    raise Failure("LCOV source is outside the scan snapshot")
                line = "SF:" + path
                files += 1
            elif line == "end_of_record":
                if not in_record:
                    raise Failure("unexpected LCOV record end")
                in_record = False
            elif line.startswith("TN:") and not in_record:
                pass
            elif not in_record or not rows.fullmatch(line):
                raise Failure("invalid LCOV row")
            normalized.append(line)
        if not files or in_record or not normalized or normalized[-1] != "end_of_record":
            raise Failure("invalid or empty LCOV")
    return "\n".join(normalized) + "\n"


def coverage_inputs(sources, artifacts):
    """Prepare explicitly selected reports before issuing a scanner token."""
    if artifacts.resolve().is_relative_to(sources.resolve()):
        raise Failure("coverage reports must remain outside the analyzed source snapshot")
    evidence, paths, javascript_sources = {}, {}, set()
    for variable, kind, property_name in [("SONAR_GO_COVERAGE", "go", "sonar.go.coverage.reportPaths"),
                                          ("SONAR_JS_COVERAGE", "js", "sonar.javascript.lcov.reportPaths")]:
        value = os.environ.get(variable)
        if not value:
            continue
        path = (ROOT / value).resolve()
        if not path.is_relative_to(ROOT) or not path.is_file() or path.stat().st_size > (256 << 20 if kind == "go" else 32 << 20):
            raise Failure("select a bounded coverage report inside this checkout")
        raw = path.read_bytes()
        name = "coverage-" + kind + (".out" if kind == "go" else ".info")
        normalized = coverage(raw, kind, sources)
        (artifacts / name).write_text(normalized)
        if kind == "js":
            javascript_sources.update(line[3:] for line in lcov_lines(normalized) if line.startswith("SF:"))
        paths.setdefault(property_name, []).append(COVERAGE_MOUNT + "/" + name)
        evidence[kind] = {"path": str(path.relative_to(ROOT)), "sha256": hashlib.sha256(raw).hexdigest(),
                          "freshness": "caller-selected; regenerate against current source"}
    try:
        tooling = tooling_reports(os.environ.get("SONAR_TOOLING_REPORTS"), ROOT, sources)
        python_selected = False
        for index, item in enumerate(tooling):
            if item["kind"] == "python":
                if python_selected:
                    raise ValueError("select one native Python coverage receipt per scan")
                python_selected = True
                normalized = python_coverage(item["raw"], ROOT, sources, item["declaredSources"])
                property_name, suffix = "sonar.python.coverage.reportPaths", ".xml"
            else:
                normalized = coverage(item["raw"], "js", sources)
                validate_native_lcov(normalized, sources, item["declaredSources"])
                observed = {line[3:] for line in lcov_lines(normalized) if line.startswith("SF:")}
                if observed & javascript_sources:
                    raise ValueError("native Node coverage sources overlap; select one profile for those files")
                javascript_sources.update(observed)
                property_name, suffix = "sonar.javascript.lcov.reportPaths", ".info"
            name = "coverage-tooling-" + str(index) + suffix
            (artifacts / name).write_text(normalized)
            paths.setdefault(property_name, []).append(COVERAGE_MOUNT + "/" + name)
            item_evidence = {key: value for key, value in item.items() if key not in {"raw", "declaredSources"}}
            item_evidence["freshness"] = "successful native receipt; exact source inventory and hashes match the snapshot"
            evidence.setdefault("tooling", []).append(item_evidence)
    except ValueError as error:
        raise Failure(str(error)) from None
    return "".join(key + "=" + ",".join(values) + "\n" for key, values in paths.items()), evidence


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise Failure("SonarQube redirects are not allowed")


class Local:
    def __init__(self):
        self.owner = hashlib.sha256(str(ROOT).encode()).hexdigest()[:12]
        self.name = PROJECT + "-sonarqube-" + self.owner
        self.images = json.loads((CONFIG / "images.json").read_text())
        self.env = {key: value for key, value in os.environ.items() if key in ("PATH", "HOME", "XDG_RUNTIME_DIR", "DOCKER_CONFIG")}
        private_directory(STATE)
        self.credentials = STATE / "credentials.json"
        if self.credentials.is_symlink():
            raise Failure("refusing symlink credentials")
        self.state = json.loads(self.credentials.read_text()) if self.credentials.exists() else None
        if self.state and self.credentials.stat().st_mode & 0o077:
            raise Failure("credentials must be private (chmod 600)")
        raw_port = os.environ.get("SONAR_PORT") or str(self.state["port"] if self.state else 19000)
        if not raw_port.isdecimal() or not 1024 <= int(raw_port) <= 65535:
            raise Failure("SONAR_PORT must be 1024..65535")
        self.port = int(raw_port)
        self.base = "http://127.0.0.1:" + str(self.port)
        self.env.update(SONAR_OWNER=self.owner, SONAR_NAME=self.name, SONAR_PORT=str(self.port), SONAR_IMAGE=self.images["server"])

    def docker(self, *args, **kwargs):
        return command(["docker", *args], environment=self.env, **kwargs)

    def compose(self, *args):
        return self.docker("compose", "--env-file", "/dev/null", "-f", str(CONFIG / "compose.yaml"), "-p", self.name, *args, seconds=180)

    def inspect(self, kind, name):
        raw = self.docker(kind, "inspect", name, allowed=(0, 1))
        try:
            # Docker prints [] followed by an error for a missing named object.
            values, _ = json.JSONDecoder().raw_decode(raw.decode().lstrip())
        except ValueError:
            raise Failure("Docker inspection failed") from None
        return values[0] if values else None

    def ownership(self):
        for kind, names in [("container", [self.name]), ("volume", [self.name + "_" + suffix for suffix in ("data", "logs", "extensions")]),
                            ("network", [self.name + "_default"])]:
            for name in names:
                item = self.inspect(kind, name)
                if item and (item.get("Config", item).get("Labels") or {}).get(LABEL) != self.owner:
                    raise Failure("refusing an unowned SonarQube resource")

    def api(self, path, fields=None, password=None):
        headers = {}
        if password is not None:
            headers["Authorization"] = "Basic " + base64.b64encode(("admin:" + password).encode()).decode()
        body = urllib.parse.urlencode(fields).encode() if fields is not None else None
        request = urllib.request.Request(self.base + path, data=body, headers=headers)
        with urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect()).open(request, timeout=10) as response:
            raw = response.read((2 << 20) + 1)
            if len(raw) > 2 << 20:
                raise Failure("SonarQube response exceeds bound")
            return json.loads(raw) if raw else {}

    def ready(self):
        item = self.inspect("container", self.name)
        binding = [{"HostIp": "127.0.0.1", "HostPort": str(self.port)}]
        if (not item or not item["State"]["Running"] or item["Config"]["Image"] != self.images["server"]
                or item["HostConfig"]["PortBindings"].get("9000/tcp") != binding):
            raise Failure("start the owned server with make sonarqube-up")
        status = self.api("/api/system/status")
        if status.get("status") != "UP" or status.get("version") != self.images["serverVersion"]:
            raise Failure("SonarQube is not ready with the pinned version")
        return status

    def show_credentials(self):
        if not self.state or not self.state.get("initialized") or not self.state.get("password"):
            raise Failure("initialize credentials with make sonarqube-up")
        print("Dashboard: " + self.base + "/dashboard?id=" + PROJECT)
        print("Username: admin")
        print("Password: " + self.state["password"])

    def up(self):
        if self.state is None:
            if self.inspect("volume", self.name + "_data"):
                raise Failure("retained SonarQube data requires its original credentials.json")
            # Keep 256 random bits and guarantee every server-required class.
            password = secrets.token_urlsafe(32) + "aA0!"
            self.state = {"password": password, "port": self.port, "initialized": False}
            write_json(self.credentials, self.state)
        self.compose("up", "-d", "--pull", "never")
        deadline = time.monotonic() + 240
        while True:
            try:
                self.ready()
                break
            except (Failure, OSError, http.client.HTTPException, ValueError):
                if time.monotonic() >= deadline:
                    raise Failure("SonarQube startup deadline; inspect the owned container logs") from None
                time.sleep(2)
        password = self.state["password"]
        if not self.state["initialized"]:
            if not self.api("/api/authentication/validate", password=password).get("valid"):
                self.api("/api/users/change_password", {"login": "admin", "previousPassword": "admin", "password": password}, password="admin")
            self.state["initialized"] = True
        if not self.api("/api/authentication/validate", password=password).get("valid"):
            raise Failure("retained SonarQube credentials no longer authenticate")
        self.state["port"] = self.port
        write_json(self.credentials, self.state)
        print("SonarQube: " + self.base + " (login admin; password in " + str(self.credentials.relative_to(ROOT)) + ")")

    def quality_rule(self, profile, rule, parameters, password):
        path = "/api/rules/show?" + urllib.parse.urlencode({"key": rule, "actives": "true"})
        detail = self.api(path, password=password)
        definition = detail["rule"]
        if (definition["type"] != "CODE_SMELL"
                or {item["softwareQuality"] for item in definition["impacts"]} != {"MAINTAINABILITY"}
                or not parameters.keys() <= {item["key"] for item in definition["params"]}):
            raise Failure("unexpected maintainability rule contract: " + rule)
        active = next((item for item in detail["actives"] if item["qProfile"] == profile), None)
        if active is None:
            raise Failure("expected inherited rule is inactive: " + rule)
        current = {item["key"]: item["value"] for item in active["params"]}
        expected = {**current, **parameters}
        if current != expected:
            self.api("/api/qualityprofiles/activate_rule", {
                "key": profile, "rule": rule,
                "params": ";".join(key + "=" + value for key, value in expected.items()),
            }, password=password)
            detail = self.api(path, password=password)
            active = next((item for item in detail["actives"] if item["qProfile"] == profile), None)
            if active is None or {item["key"]: item["value"] for item in active["params"]} != expected:
                raise Failure("SonarQube rule parameters were not applied: " + rule)

    def quality_profiles(self, password):
        """Override only the selected parameters; inherit all remaining Sonar way rules."""
        policy = json.loads((CONFIG / "quality-profiles.json").read_text())
        applied = {}
        for language, rules in policy.items():
            selector = {"language": language, "qualityProfile": PROFILE}
            profiles = self.api("/api/qualityprofiles/search?" + urllib.parse.urlencode({"language": language}), password=password)["profiles"]
            parent = next((item for item in profiles if item["name"] == "Sonar way" and item["isBuiltIn"]), None)
            if parent is None:
                raise Failure("missing Sonar way profile: " + language)
            profile = next((item for item in profiles if item["name"] == PROFILE), None)
            if profile is None:
                profile = self.api("/api/qualityprofiles/create", {"language": language, "name": PROFILE}, password=password)["profile"]
            if profile.get("isBuiltIn") or profile.get("parentKey") not in (None, parent["key"]):
                raise Failure("unexpected parent for Catalog quality profile: " + language)
            if not profile.get("parentKey"):
                self.api("/api/qualityprofiles/change_parent", {**selector, "parentQualityProfile": "Sonar way"}, password=password)
            for rule, parameters in rules.items():
                self.quality_rule(profile["key"], rule, parameters, password)
            self.api("/api/qualityprofiles/add_project", {**selector, "project": PROJECT}, password=password)
            applied[language] = {"key": profile["key"], "name": PROFILE, "parent": "Sonar way", "overrides": rules}
        return applied

    def scan(self):
        self.ready()
        if not self.state or not self.state["initialized"]:
            raise Failure("initialize with make sonarqube-up")
        modules = ROOT / "catalog-ui/node_modules"
        if not modules.is_dir() or modules.is_symlink():
            raise Failure("install the locked UI dependencies with make ui-install")
        password = self.state["password"]
        try:
            self.api("/api/components/show?component=" + PROJECT, password=password)
        except urllib.error.HTTPError as error:
            if error.code != 404:
                raise
            self.api("/api/projects/create", {"project": PROJECT, "name": "Open Agentic Catalog", "visibility": "private"}, password=password)
        directory = Path(tempfile.mkdtemp(prefix="scan-", dir=STATE))
        source, work, artifacts = directory / "source", directory / "work", directory / "coverage"
        source.mkdir()
        work.mkdir()
        artifacts.mkdir()
        report = {"status": "incomplete", "images": self.images, "coverage": {}}
        token_name = "catalog-scan-" + secrets.token_hex(8)
        scanner = self.name + "-scan-" + secrets.token_hex(6)
        token = None
        try:
            report["qualityProfiles"] = self.quality_profiles(password)
            report["source"] = snapshot(source)
            # Docker cannot create a nested mountpoint inside a read-only bind.
            (source / "catalog-ui/node_modules").mkdir(exist_ok=True)
            properties = (CONFIG / "sonar-project.properties").read_text()
            coverage_properties, report["coverage"] = coverage_inputs(source, artifacts)
            properties += coverage_properties
            report["configurationSha256"] = hashlib.sha256(properties.encode()).hexdigest()
            (source / "sonar-project.properties").write_text(properties)
            token = self.api("/api/user_tokens/generate", {"name": token_name, "type": "PROJECT_ANALYSIS_TOKEN", "projectKey": PROJECT}, password=password)["token"]
            environment = ("SONAR_HOST_URL=http://127.0.0.1:9000\nSONAR_TOKEN=" + token
                           + "\nSONAR_SCANNER_JAVA_OPTS=-Xmx6g\nSONAR_USER_HOME=/tmp/sonar-home\nSCANNER_WORKDIR_PATH=/tmp/sonar-work\n").encode()
            raw = self.docker("run", "--rm", "--pull", "never", "--platform", self.images["platform"], "--name", scanner, "--label", LABEL + "=" + self.owner,
                              "--network", "container:" + self.name, "--memory", "8g", "--cpus", "2", "--user", str(os.getuid()) + ":" + str(os.getgid()),
                              "--security-opt", "no-new-privileges:true", "--cap-drop", "ALL", "--env-file", "/dev/stdin",
                              "-v", str(source) + ":/usr/src:ro", "-v", str(work) + ":/tmp/sonar-work",
                              "-v", str(artifacts) + ":" + COVERAGE_MOUNT + ":ro",
                              "-v", str(modules) + ":/usr/src/catalog-ui/node_modules:ro", self.images["scanner"], data=environment, seconds=600)
            (directory / "scanner.log").write_bytes(raw.replace(token.encode(), b"[redacted]"))
            task = dict(line.split("=", 1) for line in (work / "report-task.txt").read_text().splitlines() if "=" in line)
            identifier = task.get("ceTaskId", "")
            if not re.fullmatch(r"[A-Za-z0-9_-]{1,128}", identifier):
                raise Failure("scanner did not produce a valid compute task")
            deadline = time.monotonic() + 180
            while True:
                result = self.api("/api/ce/task?id=" + identifier, password=password)["task"]
                if result["status"] == "SUCCESS":
                    break
                if result["status"] not in {"PENDING", "IN_PROGRESS"} or time.monotonic() >= deadline:
                    raise Failure("SonarQube ingestion failed or exceeded deadline")
                time.sleep(2)
            report["qualityGate"] = self.api("/api/qualitygates/project_status?" + urllib.parse.urlencode({"analysisId": result["analysisId"]}), password=password)["projectStatus"]
            report["measures"] = self.api("/api/measures/component?component=" + PROJECT + "&metricKeys=ncloc,coverage,duplicated_lines_density,bugs,vulnerabilities,security_hotspots,code_smells", password=password)["component"]["measures"]
            report["analysisId"] = result["analysisId"]
            report["analyzers"] = [{"key": item["key"], "version": item["version"]}
                                   for item in self.api("/api/plugins/installed", password=password)["plugins"]]
            report["status"] = "passed-analysis"
        except Failure as error:
            if token and hasattr(error, "output"):
                (directory / "scanner.log").write_bytes(error.output.replace(token.encode(), b"[redacted]"))
            raise
        finally:
            cleaned = True
            try:
                item = self.inspect("container", scanner)
                if item:
                    if (item["Config"].get("Labels") or {}).get(LABEL) != self.owner:
                        raise Failure("refusing an unowned scanner container")
                    self.docker("rm", "-f", scanner)
            except (Failure, OSError):
                cleaned = False
            try:
                if token:
                    self.api("/api/user_tokens/revoke", {"name": token_name}, password=password)
            except (Failure, OSError, ValueError, http.client.HTTPException):
                cleaned = False
            report["cleanup"] = "passed" if cleaned else "failed"
            if not cleaned:
                report["status"] = "incomplete"
            write_json(directory / "report.json", report)
            shutil.rmtree(source)
            shutil.rmtree(work)
            shutil.rmtree(artifacts)
            print("SonarQube report: " + str((directory / "report.json").relative_to(ROOT)))
        if report.get("cleanup") != "passed":
            raise Failure("scanner cleanup failed")
        print("Dashboard: " + self.base + "/dashboard?id=" + PROJECT)
        print("Analysis ingested; quality gate is " + report["qualityGate"]["status"] + " (report-only).")


def main():
    if len(sys.argv) != 2 or sys.argv[1] not in {"install", "up", "scan", "status", "credentials", "down"}:
        raise Failure("use the sonarqube Make targets")
    local = Local()
    if sys.argv[1] == "credentials":
        local.show_credentials()
        return
    descriptor = os.open(STATE / "workflow.lock", os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    with os.fdopen(descriptor, "w") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise Failure("another SonarQube workflow is running for this checkout") from None
        execute(local, sys.argv[1])


def execute(local, action):
    endpoint = json.loads(local.docker("context", "inspect", "--format", "{{json .Endpoints.docker.Host}}"))
    if not endpoint.startswith("unix://"):
        raise Failure("SonarQube requires a local Unix-socket Docker context")
    local.ownership()
    if action == "install":
        for key in ("server", "scanner"):
            local.docker("pull", "--platform", local.images["platform"], local.images[key], seconds=600)
        print("Pinned SonarQube Community Build and scanner images installed.")
    elif action == "up":
        local.up()
    elif action == "scan":
        local.scan()
    elif action == "status":
        print(json.dumps(local.ready()))
        print("Dashboard: " + local.base + "/dashboard?id=" + PROJECT)
    else:
        local.compose("down", "--timeout", "60")
        print("Owned SonarQube stopped; analysis volumes and credentials retained.")


if __name__ == "__main__":
    try:
        main()
    except (Failure, OSError, ValueError, KeyError, http.client.HTTPException) as error:
        print("SonarQube: " + (str(error) if isinstance(error, Failure) else "local tool or API request failed (" + type(error).__name__ + "); no credentials logged"), file=sys.stderr)
        raise SystemExit(1) from None
