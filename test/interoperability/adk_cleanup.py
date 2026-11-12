"""Repeat native ADK success, metadata-failure and actual SIGTERM cleanup controls."""

import argparse
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import tempfile
import time

import adk_runner as native
from adk_contract import CASES, require

SCRIPT = Path(__file__).with_name("adk_runner.py")
MODES = ("valid", "metadata-mismatch", "interrupt")


def read_json(path):
    """Bound local native evidence independently of process success."""
    with path.open() as stream:
        raw = stream.read(2*1024*1024 + 1)
    require(len(raw) <= 2*1024*1024, "ADK cleanup evidence exceeds its bound")
    return json.loads(raw)


def report_from_log(log):
    """Locate only the report advertised by this child, not other running tests."""
    with log.open() as stream:
        raw = stream.read(16385)
    require(len(raw) <= 16384, "ADK cleanup child log exceeds its bound")
    lines = [line.removeprefix("ADK report: ") for line in raw.splitlines() if line.startswith("ADK report: ")]
    require(len(lines) <= 1, "ADK child advertised multiple reports")
    if not lines:
        return None
    directory = (native.ROOT / lines[0]).resolve()
    require(directory.parent == native.tools.OUTPUT.resolve() and re.fullmatch(r"run-[a-z0-9_]+", directory.name), "ADK child report is outside its owned output")
    return directory / "report.json"


def monitor(child, log, mode):
    """Signal only our known supervisor PID at its fixed revocation checkpoint."""
    deadline, report, sent = time.monotonic() + 720, None, False
    while child.poll() is None:
        require(time.monotonic() < deadline, "ADK cleanup child deadline")
        report = report_from_log(log)
        if mode == "interrupt" and report and not sent:
            ready = report.parent / "private/progress/interrupt-ready"
            if ready.exists():
                with ready.open("rb") as stream:
                    require(stream.read(17) == b"interrupt-ready\n", "invalid ADK interruption checkpoint")
                child.send_signal(signal.SIGTERM)
                sent = True
        time.sleep(.02)
    return report or report_from_log(log), sent


def stop(child):
    """Let the owned supervisor clean up before considering a forced stop failed."""
    if child is None or child.poll() is not None:
        return
    mask = signal.pthread_sigmask(signal.SIG_BLOCK, {signal.SIGINT, signal.SIGTERM})
    try:
        child.terminate()
        try:
            child.wait(timeout=90)
        except subprocess.TimeoutExpired as error:
            child.kill()
            child.wait(timeout=10)
            raise RuntimeError("ADK supervisor cleanup unconfirmed after forced stop") from error
    finally:
        signal.pthread_sigmask(signal.SIG_SETMASK, mask)


def confirm_cleanup(value, path):
    """Check private-state and exact container absence without broad Docker cleanup."""
    names = value["containers"]
    require(len(names) <= 3 and len(set(names)) == len(names) and
            all(re.fullmatch(r"catalog-adk-[0-9a-f]{12}-(catalog|tls|client)", name) for name in names) and
            (not names or len({name.rsplit("-", 1)[0] for name in names}) == 1), "ADK child ownership is incomplete")
    require(value["cleanup"] is True and not (path.parent / "private").exists(), "ADK child cleanup did not qualify")
    for name in names:
        found = native.docker("container", "ls", "--all", "--quiet", "--filter", "name=^/" + name + "$", timeout=10).stdout.strip()
        require(not found, "ADK child container remains")


def validate(mode, code, value, source, image, sent):
    """An unrelated native error, incomplete control or changed source cannot pass."""
    require(value["source"] == source and value["installation"]["image"] == image, "ADK cleanup identity changed")
    require(value["control"] == ("none" if mode == "valid" else mode), "ADK cleanup control changed")
    require(len(value["containers"]) == 3, "ADK native control did not launch its three fixtures")
    outcomes = value["caseOutcomes"]
    require(set(outcomes) == CASES and value["cleanup"] is True, "ADK cleanup case evidence incomplete")
    expected = dict.fromkeys(CASES, "passed")
    if mode == "valid":
        require(code == 0 and value["success"] is True and value["phase"] == "complete" and not sent, "valid ADK cleanup control failed")
    elif mode == "metadata-mismatch":
        expected = dict.fromkeys(CASES, "unrun")
        expected.update(inventory="passed", schemas="passed", **{"capabilities-access": "passed", "known-search": "failed"})
        require(code == 2 and value["success"] is False and value["error"] == "AssertionError" and value["nativePhase"] == "known-search" and not sent, "ADK metadata mismatch failed for another reason")
        require(value["traffic"]["unexpected"] == 0 and value["traffic"]["tools"] == {"catalog_control_capabilities": 1, "catalog_control_access": 1, "catalog_control_search": 1}, "ADK metadata failure lacks real native calls")
    else:
        require(mode == "interrupt", "unknown ADK cleanup mode")
        expected["revoked-access"] = "incomplete"
        require(sent and code == 2 and value["success"] is False and value["error"] == "KeyboardInterrupt" and value["nativePhase"] == "revoked-access", "ADK supervisor was not interrupted at the selected checkpoint")
    require(outcomes == expected, "ADK cleanup outcomes differ from the selected control")


def validate_native_failure(output):
    """Require the intended assertion, not another failure in the same Search group."""
    failure = json.loads(output["stdout"])
    require(output["exit"] == 1 and failure.get("failure") == "AssertionError" and failure.get("failureCheck") == "search-display" and
            failure.get("phase") == "known-search" and failure.get("closed") is True, "ADK metadata failure lacks the intended native assertion/closure evidence")


def campaign(mode, directory, entry):
    """Run the existing native supervisor and always retain its final cleanup evidence."""
    child = None
    log = directory / (mode + ".log")
    args = [sys.executable, "-B", str(SCRIPT), "check"]
    if mode != "valid":
        args += ["--control", mode]
    entry["log"] = str(log.relative_to(native.ROOT))
    with log.open("w") as stream:
        try:
            mask = signal.pthread_sigmask(signal.SIG_BLOCK, {signal.SIGINT, signal.SIGTERM})
            try:
                child = subprocess.Popen(args, cwd=native.ROOT, env=dict(os.environ, PYTHONOPTIMIZE="0"),
                                         stdout=stream, stderr=subprocess.STDOUT, start_new_session=True)
            finally:
                signal.pthread_sigmask(signal.SIG_SETMASK, mask)
            path, sent = monitor(child, log, mode)
            entry["signalSent"] = sent
            require(path is not None and path.exists(), "ADK child did not retain its advertised report")
        finally:
            try:
                stop(child)
            finally:
                if child is not None:
                    entry["exitCode"] = child.poll()
                path = report_from_log(log)
                if path is not None and path.exists():
                    entry["report"] = str(path.relative_to(native.ROOT))
                    value = read_json(path)
                    confirm_cleanup(value, path)
                    entry.update(cleanup=True, childSuccess=value["success"], caseOutcomes=value["caseOutcomes"])
    return value


def check():
    """Require one valid run and both reproducible lifecycle controls, sequentially."""
    _, installed = native.prerequisites()
    inputs = [*native.CATALOG_INPUTS, native.ROOT / "Makefile", native.tools.HERE]
    source = native.fingerprint(inputs)
    directory = Path(tempfile.mkdtemp(prefix="cleanup-", dir=native.tools.OUTPUT)).resolve()
    report = {"scope": "adk-native-cleanup-controls", "source": source, "image": installed["image"], "cases": {}, "success": False, "cleanup": False}
    print("ADK cleanup report: " + str(directory.relative_to(native.ROOT)), flush=True)
    try:
        binaries = None
        for mode in MODES:
            entry = {"verified": False, "cleanup": False}
            report["cases"][mode] = entry
            value = campaign(mode, directory, entry)
            validate(mode, entry["exitCode"], value, source, installed["image"], entry["signalSent"])
            if binaries is None:
                binaries = value["binaries"]
            require(value["binaries"] == binaries, "ADK calibration binary identities changed")
            if mode == "metadata-mismatch":
                output = read_json((native.ROOT / entry["report"]).parent / "native.json")
                validate_native_failure(output)
            entry["verified"] = True
        require(source == native.fingerprint(inputs), "ADK inputs changed during cleanup controls")
        require(set(report["cases"]) == set(MODES) and all(case["verified"] and case["cleanup"] for case in report["cases"].values()), "ADK cleanup controls are incomplete")
        report["binaries"] = binaries
        report["success"] = True
    except BaseException as error:
        report["error"] = type(error).__name__
        raise
    finally:
        report["cleanup"] = bool(report["cases"]) and all(case["cleanup"] for case in report["cases"].values())
        (directory / "report.json").write_text(json.dumps(report, indent=2) + "\n")
    print("Passed native ADK success, metadata-failure and SIGTERM controls; owned cleanup passed.")


def main():
    """Expose only the fixed local controls; acquisition stays in adk-tools-install."""
    if not __debug__:
        raise RuntimeError("ADK cleanup qualification requires Python assertions")
    argparse.ArgumentParser(description=__doc__).parse_args()
    signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(KeyboardInterrupt()))
    check()


if __name__ == "__main__":
    try:
        main()
    except (AssertionError, RuntimeError, OSError, ValueError, KeyError, TypeError, KeyboardInterrupt) as error:
        print("ADK cleanup qualification failed: " + type(error).__name__, file=sys.stderr)
        sys.exit(2)
