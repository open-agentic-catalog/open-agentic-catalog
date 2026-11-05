"""Bounded subprocess execution shared by the host supervisor and client probe."""

import os
import selectors
import signal
import subprocess
import time
from dataclasses import dataclass


@dataclass
class Result:
    """Retain bounded stdout/stderr independently of command success."""

    code: int
    stdout: str | bytes
    stderr: str


def run(argv, *, timeout=120, limit=8 * 1024 * 1024, env=None, check=True, binary=False):
    """Kill the whole local process group on timeout, output overflow or interruption."""
    with subprocess.Popen(argv, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                          env=env, start_new_session=True) as child:
        chunks = {"out": bytearray(), "err": bytearray()}
        total = 0
        deadline = time.monotonic() + timeout
        try:
            with selectors.DefaultSelector() as selector:
                selector.register(child.stdout, selectors.EVENT_READ, "out")
                selector.register(child.stderr, selectors.EVENT_READ, "err")
                while selector.get_map():
                    left = deadline - time.monotonic()
                    if left <= 0:
                        raise RuntimeError("subprocess deadline exceeded")
                    for key, _ in selector.select(min(left, 0.1)):
                        data = os.read(key.fileobj.fileno(), 65536)
                        if not data:
                            selector.unregister(key.fileobj)
                            continue
                        total += len(data)
                        if total > limit:
                            raise RuntimeError("subprocess output limit exceeded")
                        chunks[key.data].extend(data)
                child.wait(timeout=max(0.01, deadline - time.monotonic()))
        except BaseException as error:
            try:
                os.killpg(child.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            child.wait()
            if isinstance(error, subprocess.TimeoutExpired):
                raise RuntimeError("subprocess deadline exceeded") from error
            raise
        result = Result(child.returncode, bytes(chunks["out"]) if binary else chunks["out"].decode(errors="replace"),
                        chunks["err"].decode(errors="replace"))
        if check and result.code:
            # Never include argv: a synthetic token may occupy an argument.
            raise RuntimeError(f"{os.path.basename(argv[0])} failed ({result.code}): "
                               + result.stderr[-2000:])
        return result
