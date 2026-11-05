"""Matched, report-only benchstat comparison for the two existing Go benchmarks."""

import json
import math
import os
from pathlib import Path
import platform
import re
import secrets
import sys

ROOT = Path(__file__).resolve().parents[2]
PIN = "v0.0.0-20261009192801-be2c69fb417e"
MODULE = "golang.org/x/perf"
TOOL = ROOT / "test/performance/.tools/benchstat"
ARGS = ["go", "test", "./internal/platform/observability", "-run", "^$", "-bench", "^BenchmarkRuntime(Collection|Exposition)$", "-benchmem", "-count=10", "-timeout=2m"]
FIELDS = {"go", "system", "machine", "cpu", "affinity", "cpuLimit", "memoryLimit", "args"}


def metadata(command, environment):
    cpu = next((line.split(":", 1)[1].strip() for line in Path("/proc/cpuinfo").read_text().splitlines() if line.startswith("model name")), "unknown")
    def limit(name):
        path = Path("/sys/fs/cgroup") / name
        return path.read_text().strip() if path.is_file() else "unavailable"
    return {"go": json.loads(command(["go", "env", "-json", "GOVERSION", "GOOS", "GOARCH", "GOAMD64", "CGO_ENABLED", "GOEXPERIMENT"], environment, 15, True)),
            "system": platform.system() + " " + platform.release(), "machine": platform.machine(), "cpu": cpu,
            "affinity": sorted(os.sched_getaffinity(0)), "cpuLimit": limit("cpu.max"), "memoryLimit": limit("memory.max"), "args": ARGS}


def samples(raw):
    counts = {"Collection": 0, "Exposition": 0}
    suffixes = set()
    for line in raw.splitlines():
        if not line.startswith("BenchmarkRuntime"):
            continue
        match = re.fullmatch(r"BenchmarkRuntime(Collection|Exposition)(?:-(\d+))?\s+(\d+)\s+(\S+) ns/op\s+(\S+) B/op\s+(\S+) allocs/op", line)
        if not match or int(match[2] or "1") < 1 or int(match[3]) < 1:
            raise ValueError("invalid-benchmark-sample")
        for raw_number in match.group(4, 5, 6):
            number = float(raw_number)
            if not math.isfinite(number) or number < 0:
                raise ValueError("invalid-benchmark-measurement")
        counts[match[1]] += 1
        # Go omits the GOMAXPROCS suffix when it is one.
        suffixes.add(int(match[2] or "1"))
    if set(counts.values()) != {10} or len(suffixes) != 1:
        raise ValueError("benchmark-samples-incomplete")
    return suffixes.pop()


def validate(report):
    if not isinstance(report, dict) or report.get("version") != 2 or report.get("status") != "passed-development-diagnostic":
        raise ValueError("benchmark-report-incomplete")
    source = report.get("source", {})
    if not isinstance(source, dict) or not re.fullmatch(r"[0-9a-f]{40}", source.get("commit", "")) or not re.fullmatch(r"sha256:[0-9a-f]{64}", source.get("sourceDigest", "")):
        raise ValueError("benchmark-source-invalid")
    env = report.get("environment")
    if not isinstance(env, dict) or set(env) != FIELDS or env["args"] != ARGS or not all(env.values()) or env["cpu"] == "unknown":
        raise ValueError("benchmark-environment-missing")
    go = env["go"]
    if not isinstance(go, dict) or set(go) != {"GOVERSION", "GOOS", "GOARCH", "GOAMD64", "CGO_ENABLED", "GOEXPERIMENT"} or not all(isinstance(v, str) for v in go.values()) or not all(go[k] for k in ["GOVERSION", "GOOS", "GOARCH"]):
        raise ValueError("benchmark-toolchain-missing")
    raw = report.get("output")
    if not isinstance(raw, str) or len(raw) > 1024 * 1024 or not re.search(r"^PASS$", raw, re.MULTILINE) or re.search(r"^(FAIL|--- FAIL)", raw, re.MULTILINE):
        raise ValueError("benchmark-output-incomplete")
    samples(raw)
    return report


def compatible(before, after):
    validate(before)
    validate(after)
    if before["environment"] != after["environment"] or samples(before["output"]) != samples(after["output"]):
        raise ValueError("benchmark-environments-differ")
    # Different source revisions are expected when evaluating a proposed change.


def installed(command, environment):
    if not TOOL.is_file() or TOOL.is_symlink():
        raise ValueError("install-pinned-benchstat-first")
    info = command(["go", "version", "-m", str(TOOL)], environment, 15, True).decode()
    if "\tpath\t" + MODULE + "/cmd/benchstat\n" not in info or not re.search(r"\tmod\t" + re.escape(MODULE) + r"\t" + re.escape(PIN) + r"\t", info):
        raise ValueError("benchstat-version-mismatch")


def run(mode, local):
    environment = local.isolated_environment()
    if mode == "install":
        local.private_directory(TOOL.parent)
        local.command(["go", "install", MODULE + "/cmd/benchstat@" + PIN], {**environment, "GOBIN": str(TOOL.parent)}, 180)
        installed(local.command, environment)
        print("Installed benchstat " + PIN)
        return
    if mode != "compare":
        raise ValueError("invalid-benchmark-invocation")
    installed(local.command, environment)
    reports = []
    for name in ["BENCH_BASELINE", "BENCH_CANDIDATE"]:
        path = Path(os.environ.get(name, ""))
        if not path.is_file() or path.is_symlink() or path.stat().st_size > 2 * 1024 * 1024:
            raise ValueError("select-two-bounded-benchmark-reports")
        reports.append(local.safety.read_document(path))
    compatible(*reports)
    directory = local.private_directory(ROOT / "coverage/performance" / ("comparison-" + secrets.token_hex(12)))
    result = {"status": "incomplete", "tool": MODULE + "@" + PIN, "sources": [r["source"] for r in reports], "environment": reports[0]["environment"]}
    try:
        paths = []
        for name, report in zip(["baseline.txt", "candidate.txt"], reports, strict=True):
            path = directory / name
            path.write_text(report["output"])
            paths.append(str(path))
        output = local.command([str(TOOL), *paths], environment, 30, True).decode()
        if not all(name in output for name in ["RuntimeCollection", "RuntimeExposition", "sec/op", "B/op", "allocs/op"]):
            raise ValueError("benchstat-output-incomplete")
        result.update(status="passed-report-only", output=output)
        print(output)
    finally:
        local.safety.write_document(directory / "report.json", result)
        print("Benchmark comparison: " + str(directory / "report.json"))


if __name__ == "__main__":
    import runner
    try:
        if len(sys.argv) != 2:
            raise ValueError("invalid-benchmark-invocation")
        run(sys.argv[1], runner.local)
    except (ValueError, OSError, runner.Failure) as error:
        print("Benchmark comparison failed: " + str(error), file=sys.stderr)
        raise SystemExit(1) from None
