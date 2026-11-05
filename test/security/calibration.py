"""Harmless disposable responses qualify scanner detection, never application code."""

from html import escape
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import parse_qs, urlsplit


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        url = urlsplit(self.path)
        query = parse_qs(url.query).get("q", ["control"])[0]
        good = url.path == "/good"
        body = "<html><head><title>Owned calibration</title></head><body>"
        if url.path == "/sql" and "'" in query:
            body += 'ERROR: unterminated quoted string at or near "test" PostgreSQL'
        else:
            body += escape(query) if good else query
        body += "</body></html>"
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        if good:
            self.send_header("X-Frame-Options", "DENY")
            self.send_header("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
            self.send_header("X-Content-Type-Options", "nosniff")
        self.end_headers()
        self.wfile.write(body.encode())

    def log_message(self, *_args):
        pass


if __name__ == "__main__":
    HTTPServer(("127.0.0.1", 8082), Handler).serve_forever()
