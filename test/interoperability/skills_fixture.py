"""Fixed-origin Catalog requests inside the owned offline fixture; no Skill execution."""

import http.client
import json
from pathlib import Path


def main():
    request = json.loads(Path("/catalog/request.json").read_text())
    path = request["path"]
    assert path.startswith("/api/v1/") or path in ("/livez", "/readyz")
    assert "\\" not in path and "#" not in path and request["method"] in ("GET", "POST")
    headers = {"Accept": "application/json"}
    for field, header in (("token", "Authorization"), ("key", "Idempotency-Key"), ("etag", "If-Match")):
        if request[field]:
            headers[header] = ("Bearer " if field == "token" else "") + request[field]
    body = None if request["body"] is None else json.dumps(request["body"]).encode()
    if body is not None:
        headers["Content-Type"] = "application/json"
    connection = http.client.HTTPConnection("127.0.0.1", 8080, timeout=10)
    try:
        connection.request(request["method"], path, body=body, headers=headers)
        response = connection.getresponse()
        raw = response.read(2*1024*1024+1)
        assert len(raw) <= 2*1024*1024
        print(json.dumps({"status": response.status, "etag": response.getheader("ETag", ""), "body": json.loads(raw)}))
    finally:
        connection.close()


if __name__ == "__main__":
    main()
