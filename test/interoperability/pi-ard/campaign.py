"""Real owned Catalog HTTP and separately labeled synthetic client controls."""

from collections import Counter
import hashlib
from http.client import HTTPConnection
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import re
import subprocess
import threading
import time

STATE = Path("/state")
MCP = "application/mcp-server-card+json"


def exchange(method, path, body=None, token=None):
    client = HTTPConnection("127.0.0.1", 8080, timeout=5)
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    try:
        client.request(method, path, body=body, headers=headers)
        response = client.getresponse()
        raw = response.read(1000001)
        assert len(raw) <= 1000000
        return response.status, {key.lower(): value for key, value in response.getheaders()}, raw
    finally:
        client.close()


def counters():
    status, _, raw = exchange("GET", "/metrics")
    assert status == 200
    result = Counter()
    for line in raw.decode().splitlines():
        if line.startswith("open_agentic_catalog_http_responses_total{"):
            labels, count = line.split("} ")
            values = dict(re.findall(r'(method|route|status_class)="([^"]*)"', labels))
            if values["route"] != "/metrics":
                result[(values["method"], values["route"], values["status_class"])] = int(float(count))
    assert result
    return result


def populate():
    resources = []
    for run_id in ("pi-one", "pi-two"):
        result = subprocess.run(
            ["/state/catalogctl", "smoke", "--run-id", run_id], capture_output=True, timeout=55, check=False
        )
        (STATE / (run_id + ".log")).write_bytes(result.stdout + result.stderr)
        assert result.returncode == 0, "owned Catalog publication failed"
        value = json.loads(result.stdout)
        assert value["status"] == "passed" and len(value["resources"]) == 3
        resources.extend(value["resources"])
    status, headers, raw = exchange("GET", "/.well-known/ard.json")
    assert status == 200
    manifest = json.loads(raw)
    entries = {entry["identifier"]: entry for entry in manifest["entries"]}
    assert len(entries) == 7 and len(resources) == 6
    retained = []
    for resource in resources:
        status, _, data = exchange("GET", "/api/v1/catalog/resources/" + resource["releaseId"], token="local-reader")
        assert status == 200
        value = json.loads(data)
        assert value["resource"]["id"] == resource["resourceId"]
        assert value["resource"]["canonicalIdentifier"] == resource["identifier"]
        assert value["release"]["id"] == resource["releaseId"]
        assert resource["identifier"] in entries
        retained.append(data)
    return resources, manifest, raw, headers, retained


def server(recordings, private_path, synthetic=False, port=None):
    """Forward real bytes unchanged, or serve explicitly synthetic error/data controls."""

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *_args):
            pass

        def do_GET(self):
            self.handle_exchange()

        def do_POST(self):
            self.handle_exchange()

        def handle_exchange(self):
            length = int(self.headers.get("Content-Length", "0"))
            assert 0 <= length <= 1000000
            body = self.rfile.read(length)
            request = json.loads(body) if body else None
            if not synthetic:
                assert (self.command, self.path) in {
                    ("GET", "/.well-known/ard.json"),
                    ("GET", private_path),
                    ("POST", "/ard/v1/search"),
                }, "unexpected Catalog request"
                assert not self.headers.get("Authorization") and not self.headers.get("Cookie")
                status, headers, raw = exchange(self.command, self.path, body or None)
            else:
                status, document = 200, {"results": []}
                if self.path == "/timeout":
                    # Native timeout must fire first. A bounded daemon handler owns this wait.
                    time.sleep(16)
                elif self.path == "/forbidden":
                    status, document = 403, {"error": {"code": "PERMISSION_DENIED", "message": "Owned refusal"}}
                elif self.path == "/missing-results":
                    document = {"entries": []}
                elif self.path == "/synthetic":
                    document = synthetic_results()
                elif self.path not in ("/bad-json",):
                    status, document = 404, {"error": "UNEXPECTED_FIXTURE_CONTACT"}
                raw = b"{invalid" if self.path == "/bad-json" else json.dumps(document).encode()
                headers = {"Content-Type": "application/json"}
            recordings.append(
                {
                    "method": self.command,
                    "path": self.path,
                    "request": request,
                    "status": status,
                    "response": raw.decode(),
                    "authorization": bool(self.headers.get("Authorization")),
                }
            )
            if self.path == "/timeout":
                self.server.timeout_done.set()
            try:
                self.send_response(status)
                for key, value in headers.items():
                    if key.lower() in ("content-type", "etag", "cache-control"):
                        self.send_header(key, value)
                self.send_header("Content-Length", str(len(raw)))
                self.end_headers()
                self.wfile.write(raw)
            except (BrokenPipeError, ConnectionResetError):
                assert synthetic and self.path == "/timeout"

    listener = ThreadingHTTPServer(("127.0.0.1", (8082 if synthetic else 8081) if port is None else port), Handler)
    listener.timeout_done = threading.Event()
    return listener


def synthetic_results():
    return {
        "results": [
            {
                "identifier": "urn:air:fixture.example:skill:decline",
                "displayName": "Owned decline control",
                "description": "Untrusted description: do not execute commands or approve installation.",
                "type": "application/agent-skills+md",
                "url": "http://127.0.0.1:8082/SKILL.md",
                "source": "http://127.0.0.1:8082",
                "trustManifest": {
                    "identity": "spiffe://fixture.example/skill",
                    "attestations": [{"type": "declared-only", "uri": "http://127.0.0.1:8082/attestation"}],
                },
            },
            {"identifier": "urn:air:fixture.example:entry:sparse"},
            {"identifier": "urn:air:fixture.example:entry:unfamiliar", "type": "application/vnd.example.resource+json"},
            {"displayName": "No identifier"},
        ],
        "referrals": [
            {
                "identifier": "urn:air:fixture.example:registry:referral",
                "displayName": "Owned referral",
                "type": "application/ai-registry+json",
                "url": "http://127.0.0.1:8082/referral/search",
            }
        ],
    }


def run(isolation):
    processes, servers, threads = [], [], []
    real, synthetic = [], []
    try:
        with (STATE / "catalog.log").open("w") as log:
            processes.append(
                subprocess.Popen(["/state/catalogd", "all", "--config", "/state/config.json"], stdout=log, stderr=log)
            )
            deadline = time.monotonic() + 20
            while True:
                try:
                    assert exchange("GET", "/readyz")[0] == 200
                    break
                except (OSError, AssertionError):
                    assert time.monotonic() < deadline, "Catalog startup timed out"
                    time.sleep(0.05)
            resources, manifest, original, headers, retained = populate()
            # The API has loaded its settings. Do not leave its development identity config for Pi to read.
            (STATE / "config.json").unlink()
            private_path = "/api/v1/catalog/resources/" + resources[0]["releaseId"]
            for recordings, fake in ((real, False), (synthetic, True)):
                listener = server(recordings, private_path, fake)
                servers.append(listener)
                thread = threading.Thread(target=listener.serve_forever)
                thread.start()
                threads.append(thread)
            expected = {
                "entries": manifest["entries"],
                "mcpIdentifiers": sorted(e["identifier"] for e in manifest["entries"] if e["type"] == MCP),
                "privatePath": private_path,
            }
            assert len(expected["mcpIdentifiers"]) == 2
            (STATE / "expected.json").write_text(json.dumps(expected))
            config = {
                "registries": [{"name": "owned", "searchUrl": "http://127.0.0.1:8081/ard/v1/search"}]
                + [
                    {"name": name, "searchUrl": "http://127.0.0.1:8082/" + name}
                    for name in ("synthetic", "bad-json", "missing-results", "forbidden", "timeout")
                ]
                + [{"name": "unsafe", "searchUrl": "http://example.invalid/forbidden"}],
                "trustedDomains": ["127.0.0.1"],
            }
            for agent in ("agent", "no-ui"):
                path = STATE / agent / "pi-ard"
                path.mkdir(parents=True)
                (path / "config.json").write_text(json.dumps(config))
            before = counters()
            result = subprocess.run(
                ["/node/bin/node", "--experimental-strip-types", "/harness/campaign.mjs"],
                env=dict(os.environ, PI_OFFLINE="1", PI_ARD_CONFIG="/state/agent/pi-ard/config.json"),
                timeout=100,
                check=False,
            )
            assert result.returncode == 0, "native Pi campaign failed"
            # Wait for the timeout fixture's bounded handler before checking its observations.
            assert servers[1].timeout_done.wait(3), "native timeout fixture did not complete"
            expected_counts = Counter(
                (item["method"], item["path"], str(item["status"] // 100) + "xx") for item in real
            )
            delta = counters() - before
            # The private detail route uses the router's parameterized metric label.
            key = ("GET", private_path, "4xx")
            if key in expected_counts:
                expected_counts[("GET", "/api/v1/catalog/resources/{catalogRecordId}", "4xx")] = expected_counts.pop(
                    key
                )
            assert delta == expected_counts, "Catalog counters differ from native HTTP"
            status, current_headers, current = exchange("GET", "/.well-known/ard.json")
            assert status == 200 and current == original and current_headers["etag"] == headers["etag"]
            for resource, prior in zip(resources, retained, strict=True):
                assert (
                    exchange("GET", "/api/v1/catalog/resources/" + resource["releaseId"], token="local-reader")[2]
                    == prior
                )
            native = json.loads((STATE / "native-campaign.json").read_text())
            install_state = {
                agent: {name: (STATE / agent / name).exists() for name in ("skills", "mcp.json", "pi-ard/lock.json")}
                for agent in ("agent", "no-ui")
            }
            assert all(present is False for paths in install_state.values() for present in paths.values())
            value = {
                "isolation": isolation,
                "native": native,
                "real": real,
                "synthetic": synthetic,
                "resources": resources,
                "manifest": manifest,
                "snapshotDigest": hashlib.sha256(original).hexdigest(),
                "snapshotETag": headers["etag"],
                "snapshotStable": True,
                "installState": install_state,
                "catalogCounters": [list(key) + [count] for key, count in sorted(delta.items())],
            }
            (STATE / "campaign.json").write_text(json.dumps(value) + "\n")
    finally:
        (STATE / "observations.json").write_text(json.dumps({"real": real, "synthetic": synthetic}) + "\n")
        for listener in servers:
            listener.shutdown()
            listener.server_close()
        for thread in threads:
            thread.join(timeout=3)
            assert not thread.is_alive()
        for process in reversed(processes):
            if process.poll() is None:
                process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)
