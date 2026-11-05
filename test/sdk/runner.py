#!/usr/bin/env python3
"""Explicit, pinned SDK generation and local qualification; never publishes."""

import argparse
import csv
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import tarfile
import zipfile

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
SDK = ROOT / "sdk/go"
COMPAT = ROOT / "test/clientcompat"
MODULE = "github.com/open-agentic-catalog/open-agentic-catalog"
SDK_MODULE = MODULE + "/sdk/go"
TOOLS = HERE / ".tools"
ANALYZERS = {
    "staticcheck": ("STATICCHECK_VERSION", "honnef.co/go/tools/cmd/staticcheck"),
    "golangci-lint": ("GOLANGCI_LINT_VERSION", "github.com/golangci/golangci-lint/v2/cmd/golangci-lint"),
    "go-licenses": ("GO_LICENSES_VERSION", "github.com/google/go-licenses"),
}


def make_value(name):
    match = re.search(r"^" + re.escape(name) + r" := (\S+)$", (ROOT / "Makefile").read_text(), re.MULTILINE)
    if not match:
        raise RuntimeError(f"Missing shared tool policy: {name}")
    return match[1]


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def check_archive_notices(archive, sdk, prefix):
    """Require the SDK's own exact notices in its package, without extraction."""
    expected = {prefix + name: (sdk / name).read_bytes() for name in ("LICENSE", "NOTICE")}
    if any(not text.strip() for text in expected.values()):
        raise RuntimeError("Empty SDK license or notice")
    if archive.suffix == ".whl":
        with zipfile.ZipFile(archive) as package:
            for name, text in expected.items():
                matches = [item for item in package.infolist() if item.filename == name]
                if len(matches) != 1 or package.read(matches[0]) != text:
                    raise RuntimeError("SDK package notice missing or changed: " + name)
    else:
        with tarfile.open(archive, "r:gz") as package:
            for name, text in expected.items():
                matches = [item for item in package.getmembers() if item.name == name]
                if len(matches) != 1 or not matches[0].isfile() or package.extractfile(matches[0]).read() != text:
                    raise RuntimeError("SDK package notice missing or changed: " + name)


def inputs():
    paths = [SDK / name for name in ("go.mod", "go.sum")]
    paths += [COMPAT / "tools" / name for name in ("go.mod", "go.sum")]
    paths += [ROOT / name for name in ("go.mod", "go.sum")]
    paths += [COMPAT / "go" / name for name in ("go.mod", "go.sum")]
    locked = {str(path.relative_to(ROOT)): digest(path) for path in paths}
    locked["analyzers"] = {name: make_value(pin) for name, (pin, _) in ANALYZERS.items()}
    return locked


def tool_identities():
    return {name: digest(TOOLS / name) for name in ("oapi-codegen", *ANALYZERS)}


def environment(offline=False):
    env = {key: value for key, value in os.environ.items() if key not in (
        "GOROOT", "GOFLAGS", "GOENV", "GOWORK", "GOTOOLCHAIN", "GOOS", "GOARCH", "CGO_ENABLED")}
    version = next(line.split()[1] for line in (ROOT / "go.mod").read_text().splitlines() if line.startswith("go "))
    env.update(GOWORK="off", GOENV="off", GOFLAGS="-mod=readonly", GOTOOLCHAIN="local" if offline else "go" + version, GOTELEMETRY="off")
    if offline:
        env.update(GOPROXY="off", GOSUMDB="off", GONOPROXY="none", GOPRIVATE="")
    return env


def run(args, cwd, env, *, capture=False):
    result = subprocess.run([str(arg) for arg in args], cwd=cwd, env=env,
                            check=True, timeout=300, text=True,
                            stdout=subprocess.PIPE if capture else None)
    return result.stdout


def install():
    TOOLS.mkdir(parents=True, exist_ok=True)
    stamp = TOOLS / "installed.json"
    stamp.unlink(missing_ok=True)
    before = inputs()
    env = environment()
    go = shutil.which("go")
    if not go:
        raise RuntimeError("Go is required")
    goroot = run([go, "env", "GOROOT"], ROOT, env, capture=True).strip()
    native_go = Path(goroot) / "bin/go"
    for directory in (COMPAT / "tools", SDK, COMPAT / "go"):
        run([native_go, "mod", "download"], directory, env)
    # Tidy also needs tests of imported dependencies, which download omits.
    # Acquire them now without changing locks; verification stays offline.
    run([native_go, "mod", "tidy", "-diff"], SDK, env)
    run([native_go, "build", "-trimpath", "-o", TOOLS / "oapi-codegen",
         "github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen"], COMPAT / "tools", env)
    for pin, package in ANALYZERS.values():
        run([native_go, "install", package + "@" + make_value(pin)], ROOT, dict(env, GOBIN=str(TOOLS)))
    if inputs() != before:
        raise RuntimeError("Installation changed module locks")
    stamp.write_text(json.dumps({"inputs": before, "go": str(native_go),
                                "binaries": tool_identities()}, indent=2) + "\n")
    print("Installed pinned Go SDK tools and qualification dependencies")


def installed():
    try:
        stamp = json.loads((TOOLS / "installed.json").read_text())
        if stamp["inputs"] != inputs() or stamp["binaries"] != tool_identities() or not Path(stamp["go"]).is_file():
            raise ValueError("stale")
        return Path(stamp["go"])
    except (OSError, ValueError, KeyError, TypeError):
        raise RuntimeError("Run make sdk-tools-install SDK_LANGUAGE=go (missing or stale installation)") from None


def diagnostic_template(source):
    """Change only error returns, through oapi-codegen's supported templates."""
    if "return nil, err" not in source:
        raise RuntimeError("Generator error-return template changed; review the override")
    source = source.replace("return nil, err", "return nil, safeError(err)").replace("return err\n", "return safeError(err)\n")
    # genResponseUnmarshal emits its own Go error returns. Sanitize the complete
    # parse result, including that generated body and future parser branches.
    signature = "func Parse{{genResponseTypeName $opid | ucFirst}}(rsp *http.Response) (*{{genResponseTypeName $opid}}, error) {"
    if signature in source:
        source = source.replace(signature, "func Parse{{genResponseTypeName $opid | ucFirst}}(rsp *http.Response) (result *{{genResponseTypeName $opid}}, err error) {\n    defer func() { err = safeResponseError(err, rsp) }()")
    return source


def generate(go, destination, work, env):
    module_dir = Path(run([go, "list", "-m", "-f", "{{.Dir}}", "github.com/oapi-codegen/oapi-codegen/v2"], COMPAT / "tools", env, capture=True).strip())
    templates = work / "templates"
    templates.mkdir(exist_ok=True)
    for name in ("client.tmpl", "client-with-responses.tmpl"):
        original = (module_dir / "pkg/codegen/templates" / name).read_text()
        (templates / name).write_text(diagnostic_template(original))
    run([TOOLS / "oapi-codegen", "-config", COMPAT / "generate.yaml", "-templates", templates,
         "-o", destination, ROOT / "api/openapi/control-v1.yaml"], ROOT, env)


def qualify(go, work, env):
    # Reuse the existing suite with the SDK import and safe error wording.
    # Keep its independent schema, workflow and negative assertions intact.
    # Application dependencies remain in this separate test module.
    consumer = work / "compatibility"
    shutil.copytree(COMPAT / "go", consumer, ignore=shutil.ignore_patterns("generated"))
    mod = consumer / "go.mod"
    old = f"replace {MODULE} => ../../.."
    if mod.read_text().count(old) != 1:
        raise RuntimeError("Expected one Catalog fixture replacement")
    mod.write_text(mod.read_text().replace(old, f"replace {MODULE} => {json.dumps(str(ROOT))}"))
    for source in consumer.glob("*.go"):
        source.write_text(source.read_text().replace(MODULE + "/test/clientcompat/go/generated", SDK_MODULE).replace("Catalog request failed", "Catalog SDK: request_or_decode_failed"))
    shutil.copyfile(HERE / "consumer_functional_test.go.txt", consumer / "sdk_functional_test.go")
    run([go, "mod", "edit", "-require=" + SDK_MODULE + "@v0.0.0", "-replace=" + SDK_MODULE + "=" + str(SDK)], consumer, env)
    run([go, "test", "-race", "-shuffle=on", "-count=1", "-timeout=90s", "./..."], consumer, env)
    run([go, "test", "-tags=functional", "-race", "-shuffle=on", "-count=1", "-timeout=90s", "./..."], consumer, env)
    run([go, "vet", "-tags=functional", "./..."], consumer, env)
    with tempfile.TemporaryDirectory(prefix="catalog-sdk-go-consumer-") as directory:
        external = Path(directory)
        (external / "go.mod").write_text("module sdk-consumer.example/test\n\ngo 1.27.3\n")
        shutil.copyfile(SDK / "go.sum", external / "go.sum")
        shutil.copyfile(HERE / "external_test.go.txt", external / "consumer_test.go")
        blocks = re.findall(r"```go\n(.*?)\n```", (SDK / "README.md").read_text(), re.DOTALL)
        for helper in ("reportSDKFailure", "searchLocalPages"):
            examples = [block for block in blocks if "func " + helper + "(" in block]
            if len(examples) != 1:
                raise RuntimeError("Expected one Go README example for " + helper)
            (external / ("readme_" + helper + "_test.go")).write_text("package consumer_test\n\n" + examples[0] + "\n")
        run([go, "mod", "edit", "-require=" + SDK_MODULE + "@v0.0.0", "-replace=" + SDK_MODULE + "=" + str(SDK)], external, env)
        # The only local replacement is the SDK. No application module or download.
        run([go, "mod", "tidy"], external, dict(env, GOFLAGS=""))
        run([go, "test", "-race", "-count=1", "./..."], external, env)
    dependencies = run([go, "list", "-deps", "-f", "{{if .Module}}{{.Module.Path}}{{end}}", "./..."], SDK, env, capture=True)
    if MODULE in dependencies.splitlines():
        raise RuntimeError("SDK imports the application module")


def lint_and_licenses(go, env):
    # Analyzers launch `go` internally. Select the installed native toolchain,
    # rather than the host's potentially older executable on PATH.
    env = dict(env, PATH=str(go.parent) + os.pathsep + env.get("PATH", ""))
    run([go, "mod", "tidy", "-diff"], SDK, env)
    run([TOOLS / "staticcheck", "-tags=functional", "./..."], SDK, env)
    run([TOOLS / "golangci-lint", "run", "--config", ROOT / ".golangci.yaml", "--build-tags=functional", "./..."], SDK, env)
    report = run([TOOLS / "go-licenses", "report", "--include_tests", "--ignore=" + SDK_MODULE, "./..."], SDK, env, capture=True)
    allowed = set(make_value("GO_LICENSE_ALLOWLIST").split(","))
    rows = list(csv.reader(report.splitlines(), strict=True))
    if not rows or any(len(row) != 3 or not all(row) or row[2] not in allowed for row in rows) or len({row[0] for row in rows}) != len(rows):
        raise RuntimeError("SDK dependency license report is incomplete or unapproved")
    print(report, end="")


def report_coverage(go, profile, label, env):
    lines = profile.read_text().splitlines()
    retained = [line for line in lines if "/client.gen.go:" not in line]
    blocks = [line.rsplit(" ", 2) for line in retained[1:]]
    total = sum(int(block[1]) for block in blocks)
    covered = sum(int(block[1]) for block in blocks if int(block[2]) > 0)
    if not total:
        raise RuntimeError("Missing handwritten SDK coverage")
    handwritten = profile.with_name(profile.stem + "-handwritten.out")
    handwritten.write_text("\n".join(retained) + "\n")
    print(f"{label} handwritten SDK coverage: {covered}/{total} statements ({100 * covered / total:.1f}%)", flush=True)
    run([go, "tool", "cover", "-func=" + str(handwritten)], SDK, env)


def check(action):
    go = installed()
    env = environment(offline=True)
    before = inputs()
    watched = [ROOT / "api/openapi/control-v1.yaml", COMPAT / "generate.yaml", SDK / "README.md", *SDK.glob("*.go"), *HERE.glob("*.txt"), *HERE.glob("*.py")]
    original = {path: digest(path) for path in watched}
    parent = ROOT / "output/sdk"
    parent.mkdir(parents=True, exist_ok=True)
    work = Path(tempfile.mkdtemp(prefix="go-", dir=parent))
    print(f"Go SDK output: {work}", flush=True)
    try:
        fresh = work / "generated.txt"
        generate(go, fresh, work, env)
        if action == "generate":
            shutil.copyfile(fresh, SDK / "client.gen.go")
            original.pop(SDK / "client.gen.go", None)
        else:
            generate(go, work / "repeat.txt", work, env)
            if digest(fresh) != digest(work / "repeat.txt"):
                raise RuntimeError("SDK generation is not deterministic")
            if digest(fresh) != digest(SDK / "client.gen.go"):
                raise RuntimeError("SDK generation drift; run make sdk-generate SDK_LANGUAGE=go")
            if run([go.parent / "gofmt", "-l", *sorted(SDK.glob("*.go")), *sorted(HERE.glob("*.go.txt"))], SDK, env, capture=True):
                raise RuntimeError("SDK formatting drift")
            run([go, "test", "-race", "-shuffle=on", "-count=1", "-coverprofile=" + str(work / "unit.out"), "./..."], SDK, env)
            report_coverage(go, work / "unit.out", "Isolated unit", env)
            run([go, "test", "-tags=functional", "-race", "-shuffle=on", "-count=1", "-timeout=60s", "-coverprofile=" + str(work / "local.out"), "./..."], SDK, env)
            report_coverage(go, work / "local.out", "Unit plus local HTTP/TLS functional", env)
            run([go, "vet", "-tags=functional", "./..."], SDK, env)
            lint_and_licenses(go, env)
            qualify(go, work, env)
            for pattern in ("test_unit_*.py", "test_functional_*.py"):
                run([sys.executable, "-B", "-m", "unittest", "discover", "-s", HERE, "-p", pattern], ROOT, env)
        if inputs() != before or any(digest(path) != value for path, value in original.items()):
            raise RuntimeError("SDK check changed its inputs")
    except BaseException:
        print(f"Failed SDK output retained: {work}", file=sys.stderr)
        raise
    if action == "check":
        retain("go", [], {"goSha256": digest(go), "inputs": before},
               source_files=[*sorted(SDK.glob("*.go")), *(SDK / n for n in ("go.mod", "go.sum", "README.md", "LICENSE", "NOTICE"))])
    shutil.rmtree(work)
    print("Go SDK " + action + " passed; owned output cleaned")


def retain(language, files, tools, *, source_files=()):
    """Retain local artifacts and exact inputs, without publication or timestamps."""
    target = ROOT / "output/artifacts/sdk" / language
    target.mkdir(parents=True, exist_ok=True)
    (target / "qualification.json").unlink(missing_ok=True)
    artifacts = {}
    for path in files:
        shutil.copyfile(path, target / path.name)
        artifacts[path.name] = digest(target / path.name)
    value = {"language": language, "releaseBase": (ROOT / "VERSION").read_text().strip(),
             "openapiSha256": digest(ROOT / "api/openapi/control-v1.yaml"),
             "artifacts": artifacts, "tools": tools,
             "sources": {str(p.relative_to(ROOT)): digest(p) for p in source_files}}
    (target / "manifest.json").write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")
    print("Local SDK manifest: " + str(target / "manifest.json"), flush=True)
    return value


def package():
    go = installed()
    env = environment(offline=True)
    run([go, "build", "./..."], SDK, env)
    # Go remains a source module. Do not manufacture a binary distribution.
    return retain("go", [], {"goSha256": digest(go), "inputs": inputs()},
                  source_files=[*sorted(SDK.glob("*.go")), *(SDK / n for n in ("go.mod", "go.sum", "README.md", "LICENSE", "NOTICE"))])


def selected(action, language):
    if action in ("audit-install", "audit"):
        if language != "rust":
            raise RuntimeError("Separate dependency audit requires --language rust")
        import rust_runner

        return rust_runner.audit_install() if action == "audit-install" else rust_runner.audit()
    if language == "go":
        return install() if action == "install" else package() if action == "package" else check(action)
    module = __import__(language + "_runner")
    return module.install() if action == "install" else module.package() if action == "package" else module.check(action)


def qualification_context(language):
    """Select the shared handoff helper only for a real SDK check."""
    import importlib.util
    import uuid

    # Preserve the existing missing-tool refusal before requiring Git or the
    # shared repository handoff helper. This check never acquires tools.
    if language == "go":
        installed()
    else:
        __import__(language + "_runner").installed()
    spec = importlib.util.spec_from_file_location("sdk_admission", HERE.parent / "ci/admission.py")
    admission = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(admission)
    return admission, os.environ.get("SDK_RUN_ID", uuid.uuid4().hex), admission.source_identity(ROOT)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("install", "generate", "check", "package", "audit-install", "audit"))
    parser.add_argument("--language", choices=("go", "python", "typescript", "rust", "all"), default="all", help="Select an independently installed SDK or all four")
    args = parser.parse_args()
    try:
        if args.action in ("audit-install", "audit") and args.language != "rust":
            raise RuntimeError("Separate dependency audit requires --language rust")
        languages = ("go", "python", "typescript", "rust") if args.language == "all" else (args.language,)
        if args.action == "package" and args.language == "all":
            (ROOT / "output/artifacts/sdk/manifest.json").unlink(missing_ok=True)
        reports = {}
        qualification = None
        for language in languages:
            print(f"SDK {args.action}: {language}", flush=True)
            if args.action == "check" and qualification is None:
                qualification = qualification_context(language)
            if qualification:
                (ROOT / "output/artifacts/sdk" / language / "qualification.json").unlink(missing_ok=True)
                (ROOT / "output/artifacts/sdk" / language / "manifest.json").unlink(missing_ok=True)
            reports[language] = selected(args.action, language)
            if qualification:
                admission, run_id, before = qualification
                admission.qualify_sdk(ROOT, language, run_id, before)
        if args.action == "package" and args.language == "all":
            (ROOT / "output/artifacts/sdk/manifest.json").write_text(json.dumps(reports, indent=2, sort_keys=True) + "\n")
        print("SDK " + args.action + " completed: " + ", ".join(languages), flush=True)
    except (OSError, RuntimeError, ValueError, subprocess.SubprocessError) as error:
        print(f"SDK workflow: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
