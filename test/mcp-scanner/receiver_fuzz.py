"""A separately selected, bounded Go event-admission fuzz campaign."""

import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[2]
MODULE = Path("examples/mcp-scanner/receiver")
TARGET = "FuzzEventAdmission"
spec = importlib.util.spec_from_file_location("receiver_fuzz_corpus", ROOT / "test/ci/fuzz_corpus.py")
corpus = importlib.util.module_from_spec(spec)
spec.loader.exec_module(corpus)


def budget(value):
    if not re.fullmatch(r"(?:[1-9]|[1-5][0-9]|60)s", value):
        raise ValueError("Receiver fuzz time must be between 1s and 60s")
    return value


def baseline_complete(raw):
    """Native Go events must name and complete the selected fuzz seed owner."""
    events = [json.loads(line) for line in raw.splitlines()]
    selected = [e for e in events if e.get("Test") == TARGET]
    if ([e.get("Action") for e in selected if e.get("Action") in {"run", "pass", "fail", "skip"}]
            != ["run", "pass"]):
        raise ValueError("Receiver fuzz seed owner did not execute successfully")
    if any(e.get("Action") in {"fail", "skip"} for e in events):
        raise ValueError("Receiver fuzz seed selection failed or skipped")
    packages = [e for e in events if "Test" not in e and e.get("Action") == "pass"]
    if len(packages) != 1:
        raise ValueError("Receiver fuzz seed selection omitted package completion")


def retained_paths(root):
    result = subprocess.run(["git", "ls-files", "--others", "--exclude-standard", "-z", "--", str(MODULE / "testdata/fuzz" / TARGET)],
                            cwd=root, capture_output=True, check=True, timeout=30)
    return [os.fsdecode(p) for p in result.stdout.split(b"\0") if p]


def run(go, seconds, *, root=ROOT):
    seconds = budget(seconds)
    binary = shutil.which(go)
    if not binary:
        raise ValueError("Select the explicitly installed Go toolchain")
    binary = Path(binary).resolve()
    env = dict(os.environ, GOWORK="off", GOENV="off", GOFLAGS="-mod=readonly", GOTOOLCHAIN="local",
               GOPROXY="off", GOSUMDB="off", GOTELEMETRY="off", GOMAXPROCS="2")
    parent = root / "output/mcp-scanner-receiver-fuzz"
    corpus.checked_directory(parent)
    parent.mkdir(parents=True, exist_ok=True)
    report = Path(tempfile.mkdtemp(prefix="run-", dir=parent))
    inputs = sorted((root / MODULE).rglob("*.go")) + [root / MODULE / n for n in ("go.mod", "go.sum", "schema.sql")]
    inputs += sorted((root / "sdk/go").rglob("*.go")) + [root / "sdk/go/go.mod", root / "sdk/go/go.sum"]
    inputs += [root / "test/mcp-scanner/receiver_fuzz.py", root / "test/ci/fuzz_corpus.py"]
    source = {str(p.relative_to(root)): hashlib.sha256(p.read_bytes()).hexdigest() for p in inputs}
    tool = subprocess.run([str(binary), "version"], env=env, text=True, capture_output=True, check=True, timeout=10).stdout.strip()
    receipt = {"status": "failed", "source": source, "go": tool, "goSha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
               "target": TARGET, "fuzzTime": seconds, "corpus": [], "cleanup": "passed"}
    before = set(retained_paths(root))
    code = 1
    try:
        seed = subprocess.run([str(binary), "test", "-json", "-count=1", "-run=^" + TARGET + "$", "."],
                              cwd=root / MODULE, env=env, capture_output=True, timeout=120)
        (report / "seed.jsonl").write_bytes(seed.stdout)
        (report / "seed.stderr").write_bytes(seed.stderr)
        if seed.returncode:
            raise ValueError("Receiver fuzz seed baseline failed")
        baseline_complete(seed.stdout)
        campaign = subprocess.run([str(binary), "test", "-json", "-count=1", "-run=^$", "-fuzz=^" + TARGET + "$",
                                   "-fuzztime=" + seconds, "-parallel=2", "."],
                                  cwd=root / MODULE, env=env, capture_output=True, timeout=120 + int(seconds[:-1]))
        (report / "campaign.jsonl").write_bytes(campaign.stdout)
        (report / "campaign.stderr").write_bytes(campaign.stderr)
        code = campaign.returncode
        receipt["exitCode"] = code
        if code:
            # Preserve the original failure status even if safe collection fails.
            try:
                receipt["corpus"] = corpus.collect(root, sorted(set(retained_paths(root)) - before), report / "corpus", packages=(MODULE.as_posix(),))
            except (OSError, ValueError, subprocess.SubprocessError) as error:
                receipt["collectionError"] = type(error).__name__
        else:
            baseline_complete(campaign.stdout)
            if source != {p: hashlib.sha256((root / p).read_bytes()).hexdigest() for p in source}:
                raise ValueError("Receiver fuzz source changed during measurement")
            receipt["status"] = "passed"
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        receipt["error"] = str(error) if isinstance(error, ValueError) else type(error).__name__
        code = code or 1
    finally:
        (report / "result.json").write_text(json.dumps(receipt, indent=2) + "\n")
    print("Receiver event fuzz campaign " + receipt["status"] + ": " + str(report.relative_to(root)))
    return code


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go")
    parser.add_argument("--time", default="10s")
    args = parser.parse_args()
    return run(args.go, args.time)


if __name__ == "__main__":
    sys.exit(main())
