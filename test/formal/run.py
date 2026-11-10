"""One source-bound Job arithmetic proof; no application execution or acquisition on check."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import signal
import sys
import tempfile
import time
from zipfile import ZipFile

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "interoperability"))
from common import run  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]
AREA = Path("test/formal")
TOOLS = AREA / ".tools"
SOURCE = Path("internal/jobs/jobs.go")
FLAGS = ["--overflow", "--backend", "SILICON", "--packageTimeout", "30s"]
LIMIT = 4 << 20
SIGNATURE = "func backoffCeiling(attempt int) time.Duration {"
NAMED = "func backoffCeiling(attempt int) (ceiling time.Duration) {"


def require(ok, message):
    if not ok:
        raise ValueError(message)


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def read(path, limit=LIMIT):
    require(not any(p.is_symlink() for p in (path, *path.parents)), "symlinked formal input/output")
    require(path.is_file() and path.stat().st_size <= limit, "missing or oversized formal input: " + path.name)
    return path.read_bytes()


def host():
    require(sys.platform == "linux" and platform.machine() == "x86_64", "formal tools require Linux amd64")


def tool_files(pin):
    files = pin.get("files")
    require(isinstance(files, dict) and set(files) == {"server/server.jar", "z3/bin/z3", "z3/LICENSE.txt"},
            "formal tool inventory must contain exactly the verifier, solver and license")
    require(all(isinstance(value, str) and re.fullmatch(r"[0-9a-f]{64}", value) for value in files.values()),
            "invalid formal tool checksum")
    return files


def environment():
    env = {k: v for k, v in os.environ.items() if k not in
           {"JAVA_TOOL_OPTIONS", "JDK_JAVA_OPTIONS", "_JAVA_OPTIONS", "CLASSPATH", "Z3_EXE", "BOOGIE_EXE"}}
    env.update(LC_ALL="C.UTF-8")
    return env


def function(source, name):
    matches = re.findall(r"^func " + name + r"\([^\n]*\n.*?^}", source, flags=re.M | re.S)
    require(len(matches) == 1, "missing or ambiguous production function: " + name)
    return matches[0]


def projection(source, contract, pin):
    """Only name the return value and insert comments; copy every executable body byte."""
    require(digest(contract) == pin["contractSHA256"], "formal contract drift")
    lines = contract.decode().splitlines()
    require(len(lines) == 4 and all(line.startswith(prefix) for line, prefix in zip(lines,
            ("//@ ensures ", "//@ ensures ", "//@ invariant ", "//@ decreases "), strict=True)), "invalid formal contract")
    require(not re.search(r"\b(requires|assume|inhale|trusted)\b", contract.decode()), "forbidden proof assumption")
    source = source.decode()
    caller = function(source, "backoff")
    require(digest(caller.encode()) == pin["callerSHA256"], "backoff caller drift: review safe consumer binding")
    helper = function(source, "backoffCeiling")
    require(helper.startswith(SIGNATURE + "\n") and not any(marker in helper for marker in ("//@", "/*@")),
            "unsupported helper signature or annotations")
    loop = "\tfor attempt > 1 {"
    require(helper.count(loop) == 1, "unsupported helper loop")
    body = helper.replace(SIGNATURE, NAMED, 1).replace(loop, "\n".join("\t" + line for line in lines[2:]) + "\n" + loop, 1)
    return 'package jobs\nimport "time"\n' + "\n".join(lines[:2]) + "\n" + body + "\n"


def validate(result, expected, commit):
    text = re.sub(r"\x1b\[[0-9;]*m", "", result.stdout + result.stderr)
    require(commit + "@" in text and "Verifying package" in text, "missing verifier identity or verification")
    errors = re.findall(r"Gobra found ([0-9]+) errors?\.", text)
    if expected is None:
        require(result.code == 0 and errors == ["0"] and "Error at:" not in text, "incomplete or failed formal proof")
    else:
        require(result.code == 1 and errors == ["1"] and text.count("Error at:") == 1 and expected in text,
                "negative control failed for an unintended reason")
    return text


def controls(proof):
    def change(old, new):
        require(proof.count(old) == 1, "negative control no longer matches source")
        return proof.replace(old, new, 1)
    return {
        "valid": (proof, None),
        "false-specification": (change("//@ ensures 0 <", "//@ ensures ceiling == 0\n//@ ensures 0 <"), "Postcondition might not hold"),
        "broken-arithmetic": (change("return time.Hour", "return 2 * time.Hour"), "Postcondition might not hold"),
        "nondecreasing-measure": (change("delay *= 3", "delay *= 1"), "Termination measure might not decrease"),
        # Tool calibration is explicitly distinct from the application proof.
        # These expose weak-contract success and qualify parser/overflow diagnostics.
        "calibration-missing-contract": ("package probe\nfunc unconstrained(x int64) int64 { return 0 }\n", None),
        "calibration-vacuous": ("package probe\n//@ requires x > 0 && x < 0\n//@ ensures result > x\nfunc vacuous(x int64) (result int64) { return 0 }\n", None),
        "calibration-unsupported": ("package probe\nfunc generic[T any](x T) T { return x }\n", "no viable alternative at input '['"),
        "calibration-overflow": ("package probe\nfunc overflow(x int64) int64 { return x + 1 }\n", "Expression may cause integer overflow"),
    }


def verify_tools(root, pin, env):
    host()
    for name, expected in tool_files(pin).items():
        require(digest(read(root / TOOLS / name, 256 << 20)) == expected, "formal tool checksum mismatch: " + name)
    java = run(["java", "-version"], env=env, timeout=10)
    java_version = java.stdout + java.stderr
    require(re.search(r'version "21\.', java_version), "Java 21 is required")
    z3 = run([str(root / TOOLS / "z3/bin/z3"), "-version"], env=env, timeout=10).stdout.strip()
    require(z3 == "Z3 version 4.16.0 - 64 bit", "unexpected Z3 version")
    return java_version.strip(), z3


def install(root=ROOT):
    host()
    pin = json.loads(read(root / AREA / "tool.json"))
    files = tool_files(pin)
    destination = root / TOOLS
    require(not any(p.is_symlink() for p in (destination, *destination.parents)), "symlinked formal tools")
    if destination.exists():
        verify_tools(root, pin, environment())
        print("Pinned formal tools already installed and verified.")
        return
    with tempfile.TemporaryDirectory(prefix="install-", dir=root / AREA) as temporary:
        work = Path(temporary)
        run(["gh", "release", "download", pin["release"], "--repo", "viperproject/gobra-ide", "--pattern",
             "GobraToolsLinux.zip", "--dir", str(work)], timeout=180, env=environment())
        archive = read(work / "GobraToolsLinux.zip", 256 << 20)
        require(digest(archive) == pin["archiveSHA256"], "formal archive checksum mismatch")
        stage = work / "tools"
        with ZipFile(work / "GobraToolsLinux.zip") as bundle:
            for name, expected in files.items():
                raw = bundle.read(name)
                require(digest(raw) == expected, "formal member checksum mismatch: " + name)
                target = stage / name
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes(raw)
        (stage / "z3/bin/z3").chmod(0o700)
        stage.rename(destination)
    verify_tools(root, pin, environment())
    print("Installed and verified pinned Gobra/Silicon/Z3 tools.")


def execute(root=ROOT):
    host()
    pin_raw = read(root / AREA / "tool.json")
    pin = json.loads(pin_raw)
    source, contract, library = (read(root / p) for p in (SOURCE, AREA / "contract.txt", AREA / "time.gobra"))
    require(digest(library) == pin["timeSHA256"], "reviewed time model drift")
    proof = projection(source, contract, pin)
    env = environment()
    java, z3 = verify_tools(root, pin, env)
    output = root / "output/formal"
    require(not any(p.is_symlink() for p in (output, *output.parents)), "symlinked formal output")
    output.mkdir(parents=True, exist_ok=True)
    evidence = Path(tempfile.mkdtemp(prefix="run-", dir=output))
    print("Formal evidence: " + str(evidence), flush=True)
    report = dict(status="incomplete", sourceSHA256=digest(source), contractSHA256=digest(contract),
                  proofSHA256=digest(proof.encode()), timeSHA256=digest(library), tool=pin,
                  java=java, z3=z3, flags=FLAGS, intModel="signed-64", cases=[])
    start = time.monotonic()
    try:
        with tempfile.TemporaryDirectory(prefix="formal-") as temporary:
            work = Path(temporary)
            (work / "lib/time").mkdir(parents=True)
            (work / "lib/time/time.gobra").write_bytes(library)
            env["Z3_EXE"] = str(root / TOOLS / "z3/bin/z3")
            for name, (code, expected) in controls(proof).items():
                path = work / (name + ".go")
                path.write_text(code)
                (evidence / (name + ".go.txt")).write_text(code)
                result = run(["java", "-Xmx2g", "-Xss16m", "-cp", str(root / TOOLS / "server/server.jar"),
                              "viper.gobra.GobraRunner", "-i", str(path), "-I", str(work / "lib"), *FLAGS],
                             env=env, timeout=90, limit=LIMIT, check=False)
                (evidence / (name + ".log")).write_text(result.stdout + result.stderr)
                validate(result, expected, pin["gobraCommit"])
                report["cases"].append(dict(name=name, kind="tool-calibration" if name.startswith("calibration-") else "application-proof-control",
                                            exitCode=result.code, expectedDiagnostic=expected))
        require(source == read(root / SOURCE) and contract == read(root / AREA / "contract.txt") and
                library == read(root / AREA / "time.gobra") and pin_raw == read(root / AREA / "tool.json"), "formal input changed during verification")
        verify_tools(root, pin, env)
        report["status"] = "passed"
    finally:
        report["seconds"] = round(time.monotonic() - start, 3)
        (evidence / "report.json").write_text(json.dumps(report, indent=2) + "\n")
    print("Formal proof, three diagnostic controls and four tool calibrations passed.", flush=True)
    return report


def interrupted(signum, _frame):
    # selectors retries InterruptedError; use a distinct exception so the shared
    # supervisor kills the verifier process group immediately on SIGTERM.
    raise RuntimeError("formal execution interrupted: " + str(signum))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("install", "check"))
    args = parser.parse_args()
    signal.signal(signal.SIGTERM, interrupted)
    try:
        (install if args.action == "install" else execute)()
    except (OSError, ValueError, RuntimeError, KeyboardInterrupt) as error:
        print("Formal check failed: " + str(error), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
