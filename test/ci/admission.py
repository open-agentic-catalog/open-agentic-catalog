"""Finite contribution selection and exact SDK handoff. No remote operations."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess

ROOT = Path(__file__).resolve().parents[2]
LANGUAGES = ("go", "python", "typescript", "rust")
SHA = re.compile(r"[0-9a-f]{40}\Z")
RUN = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,100}\Z")
REPOSITORY = "open-agentic-catalog/open-agentic-catalog"


def release_intent(environment, version):
    """Validate event intent without Git, credentials or external operations."""
    if environment.get("ACT"):
        raise ValueError("Release handoff and delivery are unavailable under act")
    if environment.get("GITHUB_REF") != "refs/tags/v" + version:
        raise ValueError("Select the exact v<VERSION> tag, not a branch")
    event = environment.get("GITHUB_EVENT_NAME")
    if event == "push":
        requested = True
    elif event == "workflow_dispatch":
        if environment.get("INPUT_UPLOAD") != "true":
            raise ValueError("Release checks require upload_artifacts=true for job handoff")
        if environment.get("INPUT_PUBLISH") not in ("true", "false"):
            raise ValueError("Select an explicit boolean publish input")
        requested = environment["INPUT_PUBLISH"] == "true"
    else:
        raise ValueError("Unsupported release event")
    enabled = environment.get("RELEASE_PUBLISH_ENABLED") == "true"
    expected_repository = environment.get("GITHUB_REPOSITORY") == REPOSITORY
    return {"upload": True, "publish": requested and enabled and expected_repository}


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def source_identity(root):
    """Hash current tracked and new nonignored inputs, not mutable runner state."""
    commit = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
    if not SHA.fullmatch(commit):
        raise ValueError("Invalid source commit")
    names = subprocess.check_output(["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"], cwd=root).split(b"\0")
    values = {}
    for raw in sorted(set(names) - {b""}):
        name = raw.decode("utf-8")
        path = root / name
        if path.is_symlink():
            values[name] = "link:" + os.readlink(path)
        elif path.is_file():
            values[name] = digest(path)
        else:
            values[name] = "absent"
    return {"commit": commit, "sourceSha256": hashlib.sha256(json.dumps(values, sort_keys=True).encode()).hexdigest()}


def select(paths, projected=()):
    """Select existing expensive checks; the baseline is unconditional."""
    selected = set()
    ui = docs = False
    shared = ("api/", "internal/", "cmd/", "migrations/", "schemas/", "test/", ".github/", "config/", "deployments/", "samples/", ".agents/")
    mapping = {"sdk/" + name + "/": name for name in LANGUAGES}
    examples = {"examples/langgraph/": "python", "examples/rust-catalog-report/": "rust",
                "examples/publication-report/": "go", "examples/typescript-ard-list/": "typescript",
                "extensions/vscode/": "typescript", "examples/webhookreceiver/": "go",
                "examples/mcp-scanner/": "go", "examples/tutorials/local-oidc/": "go"}
    for name in paths:
        if not name or name.startswith("/") or ".." in Path(name).parts:
            raise ValueError("Invalid changed path")
        if name.startswith(shared) or name in {"VERSION", "Makefile", "go.mod", "go.sum", "Containerfile", ".dockerignore", ".golangci.yaml", "pyproject.toml", "uv.lock", "ruff.toml", ".pre-commit-config.yaml"}:
            selected.update(LANGUAGES)
            ui = docs = True
        elif name.startswith("sdk/"):
            matched = [value for prefix, value in mapping.items() if name.startswith(prefix)]
            selected.update(matched or LANGUAGES)
        for prefix, value in examples.items():
            if name.startswith(prefix):
                selected.add(value)
        if name.startswith("examples/") and not any(name.startswith(prefix) for prefix in examples):
            selected.update(LANGUAGES)
        ui |= name.startswith(("catalog-ui/", "test/functional/", "config/"))
        docs |= (name.startswith(("documentation/", "sdk/", "examples/", "extensions/vscode/"))
                 or name in projected or name in {".markdownlint.json", "README.md", "CONTRIBUTING.md", "SECURITY.md", "DESIGN.md", "CONTEXT.md", "DEVELOPING.md"})
    return {"ui": ui, "docs": docs, "languages": [name for name in LANGUAGES if name in selected]}


def languages(value):
    if value == "all":
        return list(LANGUAGES)
    if value in LANGUAGES:
        return [value]
    raise ValueError("Invalid SDK selection")


def outcomes(plan, results):
    required = {"baseline", "unit"}
    required.update(name for name in ("ui", "docs") if plan[name])
    if plan["languages"]:
        required.add("clients")
    for name in required:
        if results.get(name) != "success":
            raise ValueError("Incomplete required check: " + name)
    for name in {"ui", "docs", "clients"} - required:
        if results.get(name) not in {"skipped", "success"}:
            raise ValueError("Unexpected unselected check result: " + name)
    return {name: "passed" if name in required else "unselected" for name in sorted(required | {"ui", "docs", "clients"})}


def regular(root, relative):
    path = Path(relative)
    if path.is_absolute() or not path.parts or any(part in {"..", "."} for part in path.parts):
        raise ValueError("Unsafe handoff path")
    current = root
    for index, part in enumerate(path.parts):
        current /= part
        if current.is_symlink() or not (current.is_dir() if index < len(path.parts) - 1 else current.is_file()):
            raise ValueError("Missing or redirected handoff input")
    return current


def sdk_manifest(root, language):
    value = json.loads(regular(root, f"output/artifacts/sdk/{language}/manifest.json").read_text())
    if value["language"] != language or value["releaseBase"] != (root / "VERSION").read_text().strip() or value["openapiSha256"] != digest(root / "api/openapi/control-v1.yaml"):
        raise ValueError("SDK input identity mismatch")
    artifacts = value["artifacts"]
    suffixes = sorted(Path(name).suffix for name in artifacts)
    expected = {"go": [".gz"], "python": [".gz", ".whl"], "typescript": [".tgz"], "rust": [".crate"]}
    if suffixes != expected[language] or not value["sources"] or not value["tools"]:
        raise ValueError("Incomplete SDK output")
    for name, checksum in artifacts.items():
        if Path(name).name != name or not RUN.fullmatch(name) or digest(regular(root, f"output/artifacts/sdk/{language}/{name}")) != checksum:
            raise ValueError("SDK package changed")
    for name, checksum in value["sources"].items():
        if digest(regular(root, name)) != checksum:
            raise ValueError("SDK source changed")
    return value


def qualify_sdk(root, language, run_id, before):
    if not RUN.fullmatch(run_id) or language not in LANGUAGES or source_identity(root) != before:
        raise ValueError("SDK qualification source changed")
    sdk_manifest(root, language)
    directory = root / "output/artifacts/sdk" / language
    receipt = {"schemaVersion": 1, "language": language, "run": run_id, **before,
               "manifestSha256": digest(directory / "manifest.json"), "result": "success"}
    (directory / "qualification.json").write_text(json.dumps(receipt, sort_keys=True) + "\n")


def stage_sdk(root, language, run_id, destination):
    if language not in LANGUAGES or not RUN.fullmatch(run_id):
        raise ValueError("Invalid SDK handoff")
    value = sdk_manifest(root, language)
    source = root / "output/artifacts/sdk" / language
    receipt = json.loads(regular(root, f"output/artifacts/sdk/{language}/qualification.json").read_text())
    expected = {"schemaVersion": 1, "language": language, "run": run_id, **source_identity(root),
                "manifestSha256": digest(source / "manifest.json"), "result": "success"}
    if receipt != expected:
        raise ValueError("No successful current SDK qualification")
    relative = destination.relative_to(root)
    if relative.parts[:3] != ("output", "artifacts", "qualified-sdk"):
        raise ValueError("SDK staging must use output/artifacts/qualified-sdk")
    # Check existing ancestors before creating a new, never reused output.
    current = root
    for part in relative.parts[:-1]:
        current /= part
        if current.is_symlink():
            raise ValueError("Redirected SDK output")
        current.mkdir(exist_ok=True)
    destination.mkdir()
    try:
        for name in ["manifest.json", "qualification.json", *value["artifacts"]]:
            shutil.copyfile(regular(root, f"output/artifacts/sdk/{language}/{name}"), destination / name)
        if (digest(destination / "manifest.json") != receipt["manifestSha256"]
                or json.loads((destination / "qualification.json").read_text()) != receipt
                or any(digest(destination / name) != checksum for name, checksum in value["artifacts"].items())
                or source_identity(root) != {key: receipt[key] for key in ("commit", "sourceSha256")}):
            raise ValueError("SDK handoff changed during staging")

    except BaseException:
        shutil.rmtree(destination)
        raise


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    selected = sub.add_parser("select")
    selected.add_argument("--base", required=True)
    language = sub.add_parser("languages")
    language.add_argument("value")
    result = sub.add_parser("outcomes")
    result.add_argument("--plan", required=True)
    result.add_argument("--results", required=True)
    sdk = sub.add_parser("stage-sdk")
    sdk.add_argument("--language", choices=LANGUAGES, required=True)
    sdk.add_argument("--run", required=True)
    sdk.add_argument("--output", type=Path, required=True)
    sub.add_parser("release-policy")
    args = parser.parse_args()
    if args.command == "select":
        if not SHA.fullmatch(args.base):
            raise ValueError("Invalid base commit")
        # Treat a rename as a deletion and an addition. Both owning paths can
        # require checks, even when the destination has no matching selection.
        names = subprocess.check_output(["git", "diff", "--no-renames", "--name-only", "-z", args.base, "HEAD"], cwd=ROOT).decode().split("\0")
        entries = json.loads((ROOT / "documentation/content-map.json").read_text())
        value = select([name for name in names if name], [entry["source"] for entry in entries])
        print(json.dumps(value, separators=(",", ":")))
    elif args.command == "languages":
        print(json.dumps(languages(args.value), separators=(",", ":")))
    elif args.command == "outcomes":
        print(json.dumps(outcomes(json.loads(args.plan), json.loads(args.results)), sort_keys=True))
    elif args.command == "release-policy":
        value = release_intent(os.environ, (ROOT / "VERSION").read_text().strip())
        for key, enabled in value.items():
            print(key + "=" + str(enabled).lower())
    else:
        stage_sdk(ROOT, args.language, args.run, args.output.absolute())


if __name__ == "__main__":
    main()
