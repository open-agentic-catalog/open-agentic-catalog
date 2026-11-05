"""Owned namespace entrypoint; no Catalog discovery campaign runs in Batch 1."""

import json
import os
from pathlib import Path
import resource
import socket
import subprocess
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

STATE = Path("/state")


def isolation():
    if not os.statvfs("/").f_flag & os.ST_RDONLY:
        raise RuntimeError("Pi root filesystem is writable")
    if os.getuid() == 0 or any(Path(p).exists() for p in ("/home", "/root", "/var/run/docker.sock")):
        raise RuntimeError("Pi has unexpected host authority")
    if any(k.lower().endswith("proxy") or "TOKEN" in k or "KEY" in k for k in os.environ):
        raise RuntimeError("Pi inherited credentials or proxy settings")
    # A fresh namespace can contain an inactive kernel-created tunl0 device.
    # Routes and the required ENETUNREACH control establish the network boundary.
    if len(Path("/proc/net/route").read_text().splitlines()) != 1:
        raise RuntimeError("Pi namespace has an external route")
    with socket.socket() as connection:
        connection.settimeout(1)
        try:
            connection.connect(("198.51.100.1", 443))
        except OSError as error:
            if error.errno != 101:
                raise RuntimeError("external control failed for the wrong reason") from error
        else:
            raise RuntimeError("Pi can reach outside its namespace")
    group = Path("/sys/fs/cgroup") / Path("/proc/self/cgroup").read_text().strip().split("::", 1)[1].lstrip("/")
    values = {
        key: (group / filename).read_text().strip()
        for key, filename in (
            ("memoryMax", "memory.max"),
            ("tasksMax", "pids.max"),
            ("swapMax", "memory.swap.max"),
        )
    }
    quota, period = (group / "cpu.max").read_text().split()
    if values != {"memoryMax": "2147483648", "tasksMax": "256", "swapMax": "0"} or int(quota) / int(period) != 2:
        raise RuntimeError("Pi cgroup limits differ")
    resource.setrlimit(resource.RLIMIT_NOFILE, (256, 256))
    resource.setrlimit(resource.RLIMIT_FSIZE, (8388608, 8388608))
    resource.setrlimit(resource.RLIMIT_CORE, (0, 0))
    return {
        "readOnlyRoot": True,
        "network": "loopback-only",
        "externalConnectErrno": 101,
        "memoryMax": 2147483648,
        "cpuCores": 2,
        "tasksMax": 256,
        "swapMax": 0,
        "fileLimit": 8388608,
        "openFiles": 256,
    }


def main(mode):
    evidence = isolation()
    if mode == "campaign":
        from campaign import run

        run(evidence)
        return
    destination = STATE / "agent/pi-ard"
    destination.mkdir(parents=True)
    if mode != "missing-config":
        (destination / "config.json").write_text(
            json.dumps(
                {
                    "registries": [
                        {
                            "name": "owned",
                            "searchUrl": "http://127.0.0.1:8080/ard/v1/search",
                        }
                    ],
                    "trustedDomains": [],
                }
            )
        )
    observations = []

    class Control(BaseHTTPRequestHandler):
        def do_GET(self):
            observations.append(self.path)
            body = b"ready" if self.path == "/ready" else b"not permitted"
            self.send_response(200 if self.path == "/ready" else 403)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *_args):
            pass

    server = ThreadingHTTPServer(("127.0.0.1", 0), Control)
    thread = threading.Thread(target=server.serve_forever)
    thread.start()
    try:
        environment = dict(
            os.environ,
            PI_OFFLINE="1",
            PI_ARD_CONFIG=str(destination / "config.json"),
            PI_CONTROL_PORT=str(server.server_port),
        )
        result = subprocess.run(
            [
                "/node/bin/node",
                "--experimental-strip-types",
                "/harness/session.mjs",
                mode,
            ],
            env=environment,
            timeout=170,
            check=False,
        )
        if result.returncode:
            raise RuntimeError("native Pi preparation refused: " + mode)
        if mode == "prepare":
            if observations != ["/ready", "/ready"]:
                raise RuntimeError("unexpected Pi control traffic")
            evidence["native"] = json.loads((STATE / "native.json").read_text())
            (STATE / "result.json").write_text(json.dumps(evidence) + "\n")
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=3)
        if thread.is_alive():
            raise RuntimeError("Pi control listener remains active")


if __name__ == "__main__":
    main(sys.argv[1])
