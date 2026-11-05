"""Fixed owned loopback control/HTTPS download; never follow a discovered external URL."""

import base64
import http.client
import json
from pathlib import Path
import ssl
import sys

ORIGIN = "https://127.0.0.1:9443"


def request():
    value = json.loads(Path("/catalog/request.json").read_text())
    path = value["path"]
    assert path.startswith("/api/v1/") or path in (
        "/.well-known/ard.json",
        "/livez",
        "/readyz",
    )
    assert "\\" not in path and "#" not in path and value["method"] in ("GET", "POST")
    headers = {"Accept": "application/json"}
    for field, header in (
        ("token", "Authorization"),
        ("key", "Idempotency-Key"),
        ("etag", "If-Match"),
    ):
        if value[field]:
            headers[header] = ("Bearer " if field == "token" else "") + value[field]
    body = None if value["body"] is None else json.dumps(value["body"]).encode()
    if body is not None:
        headers["Content-Type"] = "application/json"
    connection = http.client.HTTPConnection("127.0.0.1", 8080, timeout=10)
    try:
        connection.request(value["method"], path, body=body, headers=headers)
        response = connection.getresponse()
        raw = response.read(2 * 1024 * 1024 + 1)
        assert len(raw) <= 2 * 1024 * 1024
        print(
            json.dumps(
                dict(
                    status=response.status,
                    etag=response.getheader("ETag", ""),
                    body=json.loads(raw),
                )
            )
        )
    finally:
        connection.close()


def archive(url):
    assert url in (
        ORIGIN + "/original-plugin.zip",
        ORIGIN + "/later-plugin.zip",
        ORIGIN + "/observations",
    )
    connection = http.client.HTTPSConnection(
        "127.0.0.1",
        9443,
        context=ssl.create_default_context(cafile="/trust/cert.pem"),
        timeout=10,
    )
    try:
        connection.request(
            "GET", url.removeprefix(ORIGIN), headers={"Accept-Encoding": "identity"}
        )
        response = connection.getresponse()
        raw = response.read(65537)
        assert (
            len(raw) <= 65536
            and response.status == 200
            and response.getheader("Content-Encoding", "identity") == "identity"
        )
        if url.endswith(".zip"):
            assert response.getheader("Content-Type") == "application/agent-plugins+zip"
            print(base64.b64encode(raw).decode())
        else:
            print(raw.decode())
    finally:
        connection.close()


if __name__ == "__main__":
    if sys.argv[1] == "request":
        request()
    else:
        archive(sys.argv[1])
