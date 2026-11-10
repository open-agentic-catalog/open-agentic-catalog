"""Explicit example installation and isolated/local-HTTP qualification."""

import argparse
import importlib.util
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[2]
HERE = Path(__file__).resolve().parent
EXAMPLE = ROOT / "examples/langgraph"
PYTHON = EXAMPLE / ".venv/bin/python"


def environment():
    env = {
        k: v
        for k, v in os.environ.items()
        if not k.startswith(("CATALOG_", "PYTHON", "LANGSMITH_", "LANGCHAIN_"))
        and k not in ("VIRTUAL_ENV",)
    }
    env.update(
        PYTHONDONTWRITEBYTECODE="1",
        PYTHONPATH=str(EXAMPLE),
        LANGSMITH_TRACING="false",
        LANGCHAIN_TRACING_V2="false",
        UV_PYTHON_DOWNLOADS="never",
    )
    return env


def command(args, *, cwd=ROOT, env=None, seconds=420):
    subprocess.run(
        [str(a) for a in args],
        cwd=cwd,
        env=env or environment(),
        timeout=seconds,
        check=True,
    )


def functional(model_origin=None, model="qwen2.5:7b"):
    # Reuse the same owned real-service fixture as generated SDK qualification.
    source = ROOT / "test/sdk/runner.py"
    spec = importlib.util.spec_from_file_location("catalog_sdk_fixture_tools", source)
    sdk = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(sdk)
    env = sdk.environment(offline=True)
    env = {
        k: v
        for k, v in env.items()
        if not k.startswith(("CATALOG_", "PYTHON", "LANGSMITH_", "LANGCHAIN_"))
    }
    env.update(environment())
    # Keep the SDK helper's offline Go policy after sanitizing Python inputs.
    env.update(
        GOWORK="off",
        GOENV="off",
        GOFLAGS="-mod=readonly",
        GOPROXY="off",
        GOSUMDB="off",
        GONOPROXY="none",
        GOPRIVATE="",
        GOTELEMETRY="off",
    )
    env.update(
        CATALOG_LANGGRAPH_PYTHON=str(PYTHON),
        CATALOG_LANGGRAPH_WORKFLOW=str(HERE / "functional.py"),
    )
    if model_origin:
        env.update(
            CATALOG_LANGGRAPH_MODEL_ORIGIN=model_origin, CATALOG_LANGGRAPH_MODEL=model
        )
    stamp = json.loads((EXAMPLE / ".venv/installed.json").read_text())
    native_go = Path(stamp["go"])
    if hashlib.sha256(native_go.read_bytes()).hexdigest() != stamp["go_hash"]:
        raise RuntimeError("Installed Go identity changed; reinstall example tools")
    env["GOTOOLCHAIN"] = "local"
    with tempfile.TemporaryDirectory(prefix="catalog-langgraph-") as directory:
        work = Path(directory) / "consumer"
        shutil.copytree(
            ROOT / "test/clientcompat/go",
            work,
            ignore=shutil.ignore_patterns("generated"),
        )
        mod = work / "go.mod"
        old = "replace " + sdk.MODULE + " => ../../.."
        if mod.read_text().count(old) != 1:
            raise RuntimeError("Catalog fixture replacement changed")
        mod.write_text(
            mod.read_text().replace(
                old, "replace " + sdk.MODULE + " => " + json.dumps(str(ROOT))
            )
        )
        for path in work.glob("*.go"):
            path.write_text(
                path.read_text().replace(
                    sdk.MODULE + "/test/clientcompat/go/generated", sdk.SDK_MODULE
                )
            )
        shutil.copyfile(
            HERE / "catalog_functional_test.go.txt",
            work / "langgraph_functional_test.go",
        )
        command(
            [
                native_go,
                "mod",
                "edit",
                "-require=" + sdk.SDK_MODULE + "@v0.0.0",
                "-replace=" + sdk.SDK_MODULE + "=" + str(ROOT / "sdk/go"),
            ],
            cwd=work,
            env=env,
        )
        command(
            [
                native_go,
                "test",
                "-tags=functional",
                "-race",
                "-run=^TestLangGraphSDKExample$",
                "-count=1",
                "-timeout=16m" if model_origin else "-timeout=7m",
                "-v",
                "./...",
            ],
            cwd=work,
            env=env,
            seconds=990 if model_origin else 450,
        )
    print("Owned Catalog fixture/process/temp-directory cleanup: passed")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["install", "unit", "check", "model"])
    parser.add_argument("--uv", default="uv")
    parser.add_argument("--python", default=sys.executable)
    parser.add_argument(
        "--model-origin", default=os.environ.get("LANGGRAPH_MODEL_ORIGIN")
    )
    parser.add_argument(
        "--model", default=os.environ.get("LANGGRAPH_MODEL") or "qwen2.5:7b"
    )
    args = parser.parse_args()
    if args.action == "install":
        version = subprocess.check_output([args.uv, "--version"], text=True).split()
        if version[:2] != ["uv", "0.13.6"]:
            raise RuntimeError("uv 0.13.6 required; select its installed path with UV")
        command(
            [
                args.uv,
                "sync",
                "--project",
                EXAMPLE,
                "--locked",
                "--no-python-downloads",
                "--python",
                args.python,
            ]
        )
        command([PYTHON, HERE / "inventory.py"])
        env = environment()
        version = next(
            line.split()[1]
            for line in (ROOT / "go.mod").read_text().splitlines()
            if line.startswith("go ")
        )
        env.update(GOTOOLCHAIN="go" + version, GOWORK="off", GOENV="off")
        native_go = (
            Path(
                subprocess.check_output(
                    ["go", "env", "GOROOT"], cwd=ROOT, env=env, text=True
                ).strip()
            )
            / "bin/go"
        )
        (EXAMPLE / ".venv/installed.json").write_text(
            json.dumps(
                {
                    "go": str(native_go),
                    "go_hash": hashlib.sha256(native_go.read_bytes()).hexdigest(),
                    "version": version,
                }
            )
        )
        return
    if not PYTHON.is_file():
        raise RuntimeError("Install the example tools explicitly first")
    command([PYTHON, HERE / "inventory.py"])
    if args.action in ("unit", "check"):
        output = ROOT / "output/langgraph"
        output.mkdir(parents=True, exist_ok=True)
        command(
            [
                PYTHON,
                "-m",
                "coverage",
                "run",
                "--data-file",
                output / "unit.coverage",
                "--source",
                EXAMPLE,
                "-m",
                "unittest",
                "discover",
                "-s",
                HERE,
                "-p",
                "test_unit_*.py",
            ]
        )
        command(
            [
                PYTHON,
                "-m",
                "coverage",
                "report",
                "--data-file",
                output / "unit.coverage",
                "-m",
            ]
        )
    if args.action == "model":
        if not args.model_origin:
            raise RuntimeError("Explicit owned local model origin required")
        functional(args.model_origin, args.model)
    elif args.action == "check":
        functional()


if __name__ == "__main__":
    main()
