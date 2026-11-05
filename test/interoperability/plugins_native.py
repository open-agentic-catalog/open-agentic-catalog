"""Explicit native loader probe inside isolation; ordinary tests never import the SDK."""

import http.client
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import sys
import threading
import time


def load(path):
    requests = []

    class Observer(BaseHTTPRequestHandler):
        def parse_request(self):
            accepted = super().parse_request()
            if accepted:
                # Count unsupported methods too; e.g. DELETE must not evade the observer.
                requests.append(self.path)
            return accepted

        def do_GET(self):
            self.send_response(204)
            self.end_headers()

        do_POST = do_GET

        def log_message(self, *_args):
            pass

    server = ThreadingHTTPServer(("127.0.0.1", 17654), Observer)
    thread = threading.Thread(target=server.serve_forever)
    thread.start()
    try:
        for method, control_path, expected in (
            ("GET", "/observer-control", 204),
            ("HEAD", "/observer-unsupported-control", 501),
        ):
            connection = http.client.HTTPConnection("127.0.0.1", 17654, timeout=2)
            try:
                connection.request(method, control_path)
                assert connection.getresponse().status == expected
            finally:
                connection.close()
        assert requests == ["/observer-control", "/observer-unsupported-control"]
        requests.clear()
        start = time.monotonic()
        from openhands.sdk import Plugin

        try:
            plugin = Plugin.load(path)
        except ValueError as error:
            result = dict(error_type="ValueError", error=str(error))
        else:
            result = dict(
                name=plugin.name,
                version=plugin.version,
                path=plugin.path,
                manifest=plugin.manifest.model_dump(mode="json"),
                skills=[
                    dict(
                        name=s.name,
                        description=s.description,
                        content=s.content,
                        source=s.source,
                        resources=s.resources.model_dump() if s.resources else None,
                    )
                    for s in plugin.skills
                ],
                mcp={
                    name: value.model_dump(
                        mode="json", exclude_none=True, exclude_defaults=True
                    )
                    for name, value in plugin.mcp_config.items()
                },
                hooks=plugin.hooks is not None,
                agents=len(plugin.agents),
                commands=len(plugin.commands),
                entry=plugin.entry_slash_command,
            )
        result.update(
            seconds=time.monotonic() - start,
            requests=requests,
            observer_control=True,
            executed=Path("/tmp/plugin-executed").exists(),
        )
        print(json.dumps(result))
        return 1 if "error" in result else 0
    finally:
        server.shutdown()
        thread.join(timeout=5)
        server.server_close()
        assert not thread.is_alive()


if __name__ == "__main__":
    raise SystemExit(load(sys.argv[1]))
