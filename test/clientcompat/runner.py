#!/usr/bin/env python3
"""Local, explicitly installed Go-client compatibility checks; never publishes."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
MODULE = "github.com/open-agentic-catalog/open-agentic-catalog"


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def inputs():
    """Dependency identity, independent of API or documentation edits."""
    paths = [ROOT / "go.mod", ROOT / "go.sum"]
    paths += [HERE / part / name for part in ("tools", "go") for name in ("go.mod", "go.sum")]
    return {str(path.relative_to(ROOT)): digest(path) for path in paths}


def environment(offline=False):
    env = dict(os.environ)
    # This execution-only harness always builds/runs for the host, independently
    # of release cross-compilation settings in the invoking shell.
    for key in ("GOROOT", "GOFLAGS", "GOENV", "GOWORK", "GOTOOLCHAIN", "GOOS", "GOARCH", "CGO_ENABLED"):
        env.pop(key, None)
    version = next(line.split()[1] for line in (ROOT / "go.mod").read_text().splitlines() if line.startswith("go "))
    env.update(GOWORK="off", GOENV="off", GOFLAGS="", GOTOOLCHAIN="local" if offline else "go" + version, GOTELEMETRY="off")
    if offline:
        # GOPRIVATE/GONOPROXY must not turn the offline proxy into direct Git.
        env.update(GOPROXY="off", GOSUMDB="off", GONOPROXY="none")
    return env


def run(args, cwd, env, log=None):
    argv = [str(arg) for arg in args]
    if log is None:
        subprocess.run(argv, cwd=cwd, env=env, check=True, timeout=300)
        return
    # Keep compiler/test diagnostics with failed generated output, without
    # capturing environment variables or deployment credentials.
    with log.open("ab") as output:
        output.write(("$ " + " ".join(argv) + "\n").encode())
        output.flush()
        result = subprocess.run(argv, cwd=cwd, env=env, stdout=output, stderr=subprocess.STDOUT, timeout=300)
    if result.returncode:
        print(log.read_text(errors="replace"), file=sys.stderr)
        result.check_returncode()


def install():
    env = environment()
    tool_dir = HERE / ".tools"
    tool_dir.mkdir(exist_ok=True)
    stamp = tool_dir / "installed.json"
    stamp.unlink(missing_ok=True)
    before = inputs()
    go = shutil.which("go")
    if not go:
        raise RuntimeError("Go is required")
    goroot = subprocess.check_output([go, "env", "GOROOT"], cwd=ROOT, env=env, text=True, timeout=300).strip()
    local_go = Path(goroot) / "bin/go"
    for part in ("tools", "go"):
        run([local_go, "mod", "download"], HERE / part, env)
    run([local_go, "build", "-mod=readonly", "-trimpath", "-o", tool_dir / "oapi-codegen", "github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen"], HERE / "tools", env)
    if inputs() != before:
        raise RuntimeError("Installation changed module locks; review and reconcile them before retrying")
    stamp.write_text(json.dumps({"inputs": before, "go": str(local_go), "binary": digest(tool_dir / "oapi-codegen")}, indent=2) + "\n")
    print("Installed locked Go-client tools and dependencies")


def installed():
    try:
        stamp = json.loads((HERE / ".tools/installed.json").read_text())
        if stamp["inputs"] != inputs() or stamp["binary"] != digest(HERE / ".tools/oapi-codegen") or not Path(stamp["go"]).is_file():
            raise ValueError("stale installation")
        return Path(stamp["go"])
    except (OSError, ValueError, KeyError, TypeError):
        raise RuntimeError("Run make client-go-tools-install first (missing or stale installation)") from None


def generate(destination, env):
    destination.parent.mkdir(parents=True, exist_ok=True)
    run([HERE / ".tools/oapi-codegen", "-config", HERE / "generate.yaml", "-o", destination, ROOT / "api/openapi/control-v1.yaml"], ROOT, env)


def check(action):
    go = installed()
    env = environment(offline=True)
    before = inputs()
    watched = [ROOT / "api/openapi/control-v1.yaml", HERE / "generate.yaml", *sorted((HERE / "go").glob("*.go"))]
    source = {str(path): digest(path) for path in watched}
    if action == "generate":
        generate(HERE / "go/generated/client.gen.go", env)
    else:
        parent = ROOT / "output/clientcompat"
        parent.mkdir(parents=True, exist_ok=True)
        work = Path(tempfile.mkdtemp(prefix="run-", dir=parent))
        print(f"Compatibility output: {work}", flush=True)
        # Each invocation owns this complete subtree. Failed output is retained.
        try:
            consumer = work / "consumer"
            shutil.copytree(HERE / "go", consumer, ignore=shutil.ignore_patterns("generated"))
            mod = consumer / "go.mod"
            old = f"replace {MODULE} => ../../.."
            content = mod.read_text()
            if content.count(old) != 1:
                raise RuntimeError("Expected one local Catalog module replacement")
            mod.write_text(content.replace(old, f"replace {MODULE} => {json.dumps(str(ROOT))}"))
            consumer_locks = {name: digest(consumer / name) for name in ("go.mod", "go.sum")}
            generate(consumer / "generated/client.gen.go", env)
            # Outside the nested consumer module, a .go file would become an
            # input to root `go mod tidy`, even in an ignored output directory.
            generate(work / "repeat.txt", env)
            if digest(work / "repeat.txt") != digest(consumer / "generated/client.gen.go"):
                raise RuntimeError("Go generation is not deterministic")
            log = work / "check.log"
            run([go, "test", "-mod=readonly", "-race", "-shuffle=on", "-count=1", "-timeout=90s", "./..."], consumer, env, log)
            run([go, "test", "-tags=functional", "-mod=readonly", "-race", "-shuffle=on", "-count=1", "-timeout=90s", "./..."], consumer, env, log)
            run([go, "vet", "-tags=functional", "-mod=readonly", "./..."], consumer, env, log)
            run([sys.executable, "-m", "unittest", "discover", "-s", HERE, "-p", "test_*.py"], HERE, env, log)
            print(log.read_text(errors="replace"), end="")
            if any(digest(consumer / name) != value for name, value in consumer_locks.items()):
                raise RuntimeError("Compatibility check changed temporary module locks")
            if inputs() != before or any(digest(Path(path)) != value for path, value in source.items()):
                raise RuntimeError("Compatibility inputs changed during the check")
        except BaseException:
            print(f"Failed compatibility output retained: {work}", file=sys.stderr)
            raise
        shutil.rmtree(work)
    if inputs() != before or any(digest(Path(path)) != value for path, value in source.items()):
        raise RuntimeError("Compatibility inputs changed during generation")
    print("Go-client compatibility " + action + " passed")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("install", "generate", "check"))
    args = parser.parse_args()
    try:
        install() if args.action == "install" else check(args.action)
    except (OSError, RuntimeError, subprocess.SubprocessError) as error:
        print(f"Go-client compatibility: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
