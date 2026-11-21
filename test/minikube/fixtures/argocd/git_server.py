"""Serve only Git upload-pack for one immutable, locally mounted fixture bundle."""

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import os
import ssl
import subprocess


class GitReader(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path != "/catalog.git/info/refs?service=git-upload-pack":
            self.send_error(404)
            return
        self.upload(advertise=True)

    def do_POST(self):
        if self.path != "/catalog.git/git-upload-pack":
            self.send_error(404)
            return
        self.upload(advertise=False)

    def upload(self, advertise):
        try:
            length = int(self.headers.get("Content-Length", "0"))
        except ValueError:
            self.send_error(400)
            return
        if not 0 <= length <= 2**20:
            self.send_error(413)
            return
        args = ["git", "upload-pack", "--stateless-rpc"]
        if advertise:
            args.append("--advertise-refs")
        version = self.headers.get("Git-Protocol", "")
        if version not in ("", "version=1", "version=2"):
            self.send_error(400)
            return
        result = subprocess.run(
            [*args, "/repository/catalog.git"],
            input=self.rfile.read(length),
            capture_output=True,
            env={**os.environ, "GIT_PROTOCOL": version},
            check=True,
            timeout=30,
        )
        body = (b"001e# service=git-upload-pack\n0000" if advertise else b"") + result.stdout
        self.send_response(200)
        self.send_header(
            "Content-Type", "application/x-git-upload-pack-" + ("advertisement" if advertise else "result")
        )
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


if __name__ == "__main__":
    server = ThreadingHTTPServer(("", 8443), GitReader)
    tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    tls.load_cert_chain("/tls/tls.crt", "/tls/tls.key")
    server.socket = tls.wrap_socket(server.socket, server_side=True)
    server.serve_forever()
