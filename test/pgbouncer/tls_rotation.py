"""Operator actions and evidence checks for the owned database TLS fixture."""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import re
import socket
import sys
import time


PHASES = ("baseline", "same-ca", "overlap", "successor", "retired")
PROOFS = ("sameCAFileOnlyNoActivation", "successorFileOnlyNoActivation",
          "overlapOldClientAccepted", "overlapOldUpstreamAccepted",
          "retiredClientRefused", "retiredUpstreamRefused", "originalInputsRestored")


def plan(phase):
    """Select fixed fixture inputs; callers cannot supply paths or services."""
    selections = {
        "same-ca": ("-renewed", ("ca",), True),
        "overlap": ("-renewed", ("ca", "ca-next"), False),
        "successor": ("-next", ("ca", "ca-next"), True),
        "retired": ("-next", ("ca-next",), False),
        "restore": ("", ("ca",), True),
        "stage-same-ca": ("-renewed", ("ca",), False),
        "stage-successor": ("-next", ("ca", "ca-next"), False),
    }
    if phase not in selections:
        raise ValueError("unknown owned TLS phase")
    return selections[phase]


def fixture(environment):
    """Reject ambient or mismatched scope before any container action."""
    project = environment.get("CATALOG_TEST_PGBOUNCER_PROJECT", "")
    if not re.fullmatch(r"oac-pgbouncer-[0-9a-f]{12}", project):
        raise ValueError("invalid owned TLS project")
    root = Path(environment["CATALOG_TEST_PGBOUNCER_TLS_DIRECTORY"]).resolve()
    compose = Path(environment["CATALOG_TEST_PGBOUNCER_COMPOSE"]).resolve()
    marker = Path(environment["CATALOG_TEST_PGBOUNCER_WRITER_FILE"]).resolve()
    if compose != root / "compose.json" or marker != root / "writer" or not root.name.startswith(project + "-"):
        raise ValueError("mismatched owned TLS inputs")
    if marker.read_text(encoding="utf-8") != "standby":
        raise ValueError("TLS fixture requires the promoted owned writer")
    return root, ["docker", "compose", "--env-file", "/dev/null", "-f", str(compose), "-p", project]


def validate_evidence(value, expected):
    """Require served identities, new backends and useful work in every phase."""
    if not isinstance(value, dict) or set(value) != {"phases", *PROOFS} or any(value[k] is not True for k in PROOFS):
        raise ValueError("incomplete TLS renewal proofs")
    phases = value["phases"]
    if not isinstance(phases, list) or len(phases) != len(PHASES):
        raise ValueError("incomplete TLS renewal phases")
    seen = {key: set() for key in ("poolerContainer", "apiInstance", "workerInstance", "jobID", "apiBackend", "workerBackend")}
    for name, phase in zip(PHASES, phases, strict=True):
        keys = {"name", "poolerContainer", "poolerLeaf", "postgresLeaf", "postmasterStart",
                "apiInstance", "workerInstance", "jobID", "jobState", "apiBackend", "workerBackend"}
        if not isinstance(phase, dict) or set(phase) != keys or phase["name"] != name or phase["jobState"] != "succeeded":
            raise ValueError("invalid TLS renewal phase")
        suffix = "" if name == "baseline" else "-renewed" if name in ("same-ca", "overlap") else "-next"
        if any(phase[role + "Leaf"] != expected[role + suffix] for role in ("pooler", "postgres")):
            raise ValueError("TLS phase did not serve its selected leaves")
        if not isinstance(phase["poolerContainer"], str) or not re.fullmatch(r"[0-9a-f]{64}", phase["poolerContainer"]):
            raise ValueError("invalid pooler identity")
        if not isinstance(phase["postmasterStart"], str) or not 1 <= len(phase["postmasterStart"]) <= 128:
            raise ValueError("missing server identity")
        for key in ("apiInstance", "workerInstance", "jobID"):
            if not isinstance(phase[key], str) or not re.fullmatch(r"[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}", phase[key]):
                raise ValueError("invalid process or Job identity")
        for role in ("api", "worker"):
            backend = phase[role + "Backend"]
            if (not isinstance(backend, dict) or set(backend) != {"pid", "startedAt", "postmasterStart", "role", "tls"}
                    or type(backend["pid"]) is not int or backend["pid"] < 1
                    or not isinstance(backend["startedAt"], str) or not 1 <= len(backend["startedAt"]) <= 128
                    or backend["role"] != "catalog_" + role or backend["tls"] is not True
                    or backend["postmasterStart"] != phase["postmasterStart"]):
                raise ValueError("invalid fresh backend evidence")
        for key in seen:
            item = phase[key]
            identity = (item["postmasterStart"], item["pid"], item["startedAt"]) if key.endswith("Backend") else item
            if identity in seen[key]:
                raise ValueError("TLS renewal reused a process, backend or Job")
            seen[key].add(identity)
    if phases[0]["postmasterStart"] == phases[1]["postmasterStart"] or phases[2]["postmasterStart"] == phases[3]["postmasterStart"]:
        raise ValueError("changed server leaf lacks a restarted server")
    if phases[1]["postmasterStart"] != phases[2]["postmasterStart"] or phases[3]["postmasterStart"] != phases[4]["postmasterStart"]:
        raise ValueError("trust-only phase unexpectedly replaced the writer")


def main():
    from runner import command, private_file
    parser = argparse.ArgumentParser()
    parser.add_argument("phase")
    phase = parser.parse_args().phase
    suffix, authorities, restart_writer = plan(phase)
    root, launcher = fixture(os.environ)
    stage_only = phase.startswith("stage-")
    before = command([*launcher, "ps", "--all", "-q", "pooler"])
    if not stage_only:
        command([*launcher, "stop", "pooler"], timeout=40)
        if restart_writer:
            command([*launcher, "stop", "standby"], timeout=40)
    for role, destination in (("pooler", root / "pooler"), ("postgres", root / "postgres")):
        for extension in ("pem", "key"):
            private_file(destination / f"{role}.{extension}", (root / f"{role}{suffix}.{extension}").read_text(encoding="utf-8"))
    if not stage_only:
        bundle = "".join((root / (authority + ".pem")).read_text(encoding="utf-8") for authority in authorities)
        private_file(root / "runtime-ca.pem", bundle)
        private_file(root / "pooler" / "ca.pem", bundle)
        if restart_writer:
            command([*launcher, "start", "standby"], timeout=40)
            end = time.monotonic() + 30
            while time.monotonic() < end:
                try:
                    if command([*launcher, "exec", "-T", "standby", "psql", "-XAt", "-U", "catalog", "-d", "catalog_test", "-c", "SELECT NOT pg_is_in_recovery()"], timeout=3) == "t":
                        break
                except RuntimeError:
                    pass
                time.sleep(0.1)
            else:
                raise RuntimeError("owned writer startup deadline")
        command([*launcher, "up", "-d", "--force-recreate", "--no-deps", "pooler"], timeout=45)
        address = command([*launcher, "port", "pooler", "6432"])
        host, port = address.split(":")
        if host != "127.0.0.1" or not port.isdigit():
            raise ValueError("invalid owned pooler listener")
        end = time.monotonic() + 15
        while time.monotonic() < end:
            try:
                with socket.create_connection((host, int(port)), timeout=0.5):
                    break
            except OSError:
                time.sleep(0.05)
        else:
            raise RuntimeError("owned pooler startup deadline")
    after = command([*launcher, "ps", "-q", "pooler"])
    if not re.fullmatch(r"[0-9a-f]{64}", before) or not re.fullmatch(r"[0-9a-f]{64}", after):
        raise ValueError("invalid owned pooler identity")
    if (before == after) != stage_only:
        raise ValueError("pooler replacement does not match the selected action")
    print(json.dumps({"phase": phase, "poolerContainer": after}))


if __name__ == "__main__":
    try:
        main()
    except Exception:
        print("owned database TLS transition failed", file=sys.stderr)
        raise SystemExit(1) from None
