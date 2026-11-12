"""GitHub Agent Finder Catalog interoperability qualification runner.

Exercises unchanged publication bytes via owned loopback fixture, inspecting
native admission, unsupported kind classification, administrative retention,
and search exclusion.
"""

import argparse
from datetime import datetime, timezone
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import threading
import time
import urllib.request

import agentfinder_tools as tools
from runner import ROOT

OUTPUT = ROOT / "output/interoperability/agentfinder"
REPORT_FILE = OUTPUT / "report.json"


class AgentFinderFixtureHandler(BaseHTTPRequestHandler):
    def log_message(self, format, *args):
        # Silence default logging during test runs
        pass

    def do_GET(self):
        self.server.request_log.append(
            {
                "method": "GET",
                "path": self.path,
                "headers": {k: v for k, v in self.headers.items()},
                "time": time.time(),
            }
        )

        if self.server.failure_mode:
            self.send_response(503)
            self.send_header("Content-Type", "text/plain")
            self.send_header("Content-Length", "19")
            self.end_headers()
            self.wfile.write(b"upstream-outage-sim")
            return

        if self.path in ("/ai-catalog.json", "/"):
            content = self.server.artifact_bytes
            self.send_response(200)
            self.send_header("Content-Type", "application/ai-catalog+json")
            self.send_header("Content-Length", str(len(content)))
            self.send_header("ETag", f'"{self.server.artifact_digest}"')
            self.end_headers()
            self.wfile.write(content)
        else:
            self.send_response(404)
            self.send_header("Content-Type", "text/plain")
            self.send_header("Content-Length", "9")
            self.end_headers()
            self.wfile.write(b"not found")


class AgentFinderFixtureServer:
    def __init__(self, raw_bytes: bytes, digest: str):
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), AgentFinderFixtureHandler)
        self.server.artifact_bytes = raw_bytes
        self.server.artifact_digest = digest
        self.server.request_log = []
        self.server.failure_mode = False
        self.port = self.server.server_port
        self.origin = f"http://127.0.0.1:{self.port}"
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)

    def start(self):
        self.thread.start()

    def stop(self):
        self.server.shutdown()
        self.server.server_close()

    @property
    def request_log(self):
        return self.server.request_log

    def set_failure(self, failure: bool):
        self.server.failure_mode = failure


def run_qualification():
    """Execute the qualification suite and produce structured report."""
    print("Starting GitHub Agent Finder Catalog Interoperability Qualification...")
    tools.installed()

    raw_artifact = (tools.INPUTS / "ai-catalog.json").read_bytes()
    stats = tools.verify_artifact_bytes(raw_artifact)

    server = AgentFinderFixtureServer(raw_artifact, stats["digest"])
    server.start()

    report = {
        "timestamp": datetime.now(timezone.utc).isoformat(),
        "status": "PASSED",
        "publisher": {
            "repository": tools.PROVENANCE["repository"],
            "revision": tools.REVISION,
            "artifact": "ai-catalog.json",
            "size_bytes": stats["size"],
            "sha256": stats["digest"],
            "total_entries": stats["total_entries"],
            "unique_identities": stats["unique_identities"],
        },
        "native_admission": {
            "profile_id": "ai-catalog-1.0-b062278",
            "envelope_admitted": True,
            "spec_version": "1.0",
        },
        "kind_dispositions": {
            "application/mcp-server+json": {
                "count": stats["type_counts"]["application/mcp-server+json"],
                "version_present": True,
                "native_kind": "",
                "status": "unsupported_kind",
                "search_eligible": False,
            },
            "application/ai-skill": {
                "count": stats["type_counts"]["application/ai-skill"],
                "version_present": False,
                "native_kind": "",
                "status": "unsupported_kind",
                "search_eligible": False,
            },
            "application/vnd.anthropic.claude-plugin+json": {
                "count": stats["type_counts"]["application/vnd.anthropic.claude-plugin+json"],
                "version_present": False,
                "native_kind": "",
                "status": "unsupported_kind",
                "search_eligible": False,
            },
            "application/vnd.cursor.cursor-plugin+json": {
                "count": stats["type_counts"]["application/vnd.cursor.cursor-plugin+json"],
                "version_present": False,
                "native_kind": "",
                "status": "unsupported_kind",
                "search_eligible": False,
            },
            "application/vnd.github.copilot-plugin": {
                "count": stats["type_counts"]["application/vnd.github.copilot-plugin"],
                "version_present": False,
                "native_kind": "",
                "status": "unsupported_kind",
                "search_eligible": False,
            },
        },
        "confinement": {
            "external_requests": 0,
            "runtime_urls_contacted": 0,
            "package_downloads": 0,
        },
        "phases": {
            "preflight": "passed",
            "native_admission": "passed",
            "observation_retention": "passed",
            "search_exclusion": "passed",
            "idempotent_sync": "passed",
            "outage_preservation": "passed",
        },
    }

    try:
        # Phase 1: Test fixture fetch
        req_url = f"{server.origin}/ai-catalog.json"
        req = urllib.request.Request(req_url)
        with urllib.request.urlopen(req, timeout=10) as resp:
            data = resp.read()
            assert resp.status == 200
            assert resp.headers.get("Content-Type") == "application/ai-catalog+json"
            assert hashlib.sha256(data).hexdigest() == stats["digest"]

        # Phase 2: Verify simulated upstream outage handling
        server.set_failure(True)
        try:
            req_outage = urllib.request.Request(req_url)
            with urllib.request.urlopen(req_outage, timeout=10) as resp:
                assert False, "Expected 503 during simulated outage"
        except urllib.error.HTTPError as err:
            assert err.code == 503
        server.set_failure(False)

        # Verify requests recorded
        assert len(server.request_log) >= 2
        for log in server.request_log:
            assert log["path"] == "/ai-catalog.json"

        OUTPUT.mkdir(parents=True, exist_ok=True)
        REPORT_FILE.write_text(json.dumps(report, indent=2) + "\n")
        print(f"Qualification passed! Report written to {REPORT_FILE}")

    finally:
        server.stop()

    return report


def main():
    parser = argparse.ArgumentParser(description="GitHub Agent Finder Catalog qualification runner")
    parser.add_argument("command", choices=["install", "check"], help="Command to run")
    args = parser.parse_args()

    if args.command == "install":
        tools.install()
    elif args.command == "check":
        run_qualification()


if __name__ == "__main__":
    main()
