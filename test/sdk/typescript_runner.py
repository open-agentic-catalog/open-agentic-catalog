"""Explicit SDK npm acquisition; installed-only deterministic local qualification."""
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
SDK = ROOT / "sdk/typescript"
PROJECT = HERE / "typescript"
TOOLS = HERE / ".tools/typescript"
ARD_EXAMPLE = ROOT / "examples/typescript-ard-list"
ARD_EXAMPLE_FILES = ("index.mts", "package.json", "README.md", "LICENSE", ".gitignore")


def ard_example_sources():
    return [*(ARD_EXAMPLE / name for name in ARD_EXAMPLE_FILES), HERE / "typescript_ard_functional_test.go.txt",
            *sorted((PROJECT / "ard-example").glob("*.mjs"))]


def qualify_ard_example(work, stamp, consumer):
    """Run the shipped entrypoint with the installed tarball and no source links."""
    tests = sorted((PROJECT / "ard-example").glob("*.unit.test.mjs"))
    if not tests:
        raise RuntimeError("ARD example unit tests are missing")
    example = consumer / "ard-example"
    example.mkdir()
    for name in ARD_EXAMPLE_FILES:
        shutil.copyfile(ARD_EXAMPLE / name, example / name)
    if (example / "LICENSE").read_bytes() != (ROOT / "LICENSE").read_bytes():
        raise RuntimeError("ARD example license differs from the project license")
    env = dict(environment(), CATALOG_ARD_EXAMPLE=str(example / "index.mts"),
               CATALOG_SDK_CONSUMER=str(consumer / "node_modules/@open-agentic-catalog/client/dist/index.js"))
    run([stamp["node"], SDK / "node_modules/typescript/bin/tsc", "--noEmit", "--strict",
         "--noUncheckedIndexedAccess", "--exactOptionalPropertyTypes", "--module", "NodeNext",
         "--moduleResolution", "NodeNext", "--target", "ES2022", "--types", "node",
         "--typeRoots", SDK / "node_modules/@types", example / "index.mts"])
    run([stamp["node"], "--test", "--test-timeout=30000", "--experimental-test-coverage",
         "--test-coverage-include=" + str(example / "index.mts"),
         *tests], env=env)
    catalog_work = work / "ard-catalog"
    catalog_work.mkdir()
    qualify_catalog(catalog_work, stamp, consumer, fixture=HERE / "typescript_ard_functional_test.go.txt",
                    pattern="^TestTypeScriptARDExample$", extra_env={"CATALOG_ARD_EXAMPLE": str(example / "index.mts")})


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def inputs():
    return {str(p.relative_to(ROOT)): digest(p) for p in (
        SDK / "package.json", SDK / "package-lock.json", SDK / ".nvmrc", HERE / "node-runtimes.json", ROOT / "go.mod", ROOT / "go.sum",
        ROOT / "sdk/go/go.mod", ROOT / "sdk/go/go.sum",
        ROOT / "test/clientcompat/go/go.mod", ROOT / "test/clientcompat/go/go.sum")}


def environment(base=None):
    env = {k: v for k, v in (os.environ if base is None else base).items() if not k.lower().startswith(("npm_config_", "node_", "playwright_"))}
    env.update(NPM_CONFIG_CACHE=str(TOOLS / "npm-cache"), NPM_CONFIG_USERCONFIG=os.devnull,
               NPM_CONFIG_GLOBALCONFIG=str(TOOLS / "empty.npmrc"), NPM_CONFIG_IGNORE_SCRIPTS="true",
               NPM_CONFIG_AUDIT="false", NPM_CONFIG_FUND="false",
               PLAYWRIGHT_BROWSERS_PATH=str(TOOLS / "chromium"))
    return env


def run(args, *, cwd=ROOT, env=None, capture=False):
    return subprocess.run([str(x) for x in args], cwd=cwd, env=env or environment(),
                          check=True, text=True, capture_output=capture, timeout=600).stdout


def validate_runtime(node_version, npm_version):
    expected = "v" + (SDK / ".nvmrc").read_text().strip()
    if node_version.strip() != expected or npm_version.strip() != "11.19.1":
        raise RuntimeError("Pinned Node 24.22.1 and npm 11.19.1 required")


def tool_inventory():
    lock = json.loads((SDK / "package-lock.json").read_text())
    found = {}
    for path, data in lock["packages"].items():
        if not path:
            continue
        actual = json.loads((SDK / path / "package.json").read_text())
        if actual["version"] != data["version"] or not data.get("integrity"):
            raise RuntimeError("SDK npm installation drift; reinstall")
        found[path] = {"version": actual["version"], "license": actual.get("license")}
    return found


def tool_hashes():
    names = ("openapi-typescript/bin/cli.js", "openapi-typescript/dist/index.mjs",
             "typescript/lib/typescript.js", "eslint/bin/eslint.js", "prettier/bin/prettier.cjs",
             "playwright/cli.js", "openapi-fetch/dist/index.mjs")
    return {name: digest(SDK / "node_modules" / name) for name in names}


def runtime_licenses():
    for name in ("openapi-fetch", "openapi-typescript-helpers"):
        package = SDK / "node_modules" / name
        if json.loads((package / "package.json").read_text()).get("license") != "MIT" or not (package / "LICENSE").is_file():
            raise RuntimeError("Unqualified TypeScript SDK runtime license")


def install():
    TOOLS.mkdir(parents=True, exist_ok=True)
    (TOOLS / "empty.npmrc").write_text("")
    import node_runtime
    runtimes = {version: node_runtime.install(version, TOOLS / "node") for version in json.loads((HERE / "node-runtimes.json").read_text())}
    node = runtimes[(SDK / ".nvmrc").read_text().strip()]
    npm = node.parent.parent / "lib/node_modules/npm/bin/npm-cli.js"
    validate_runtime(run([node, "--version"], capture=True), run([node, npm, "--version"], capture=True))
    TOOLS.mkdir(parents=True, exist_ok=True)
    stamp = TOOLS / "installed.json"
    stamp.unlink(missing_ok=True)
    before = inputs()
    run([node, npm, "ci", "--ignore-scripts", "--no-audit", "--no-fund"], cwd=SDK)
    # npm ci caches tarballs but not the package manifests a clean consumer needs.
    lock = json.loads((SDK / "package-lock.json").read_text())
    runtime = [p.removeprefix("node_modules/") + "@" + data["version"]
               for p, data in lock["packages"].items() if p and not data.get("dev", False)]
    run([node, npm, "cache", "add", *runtime], cwd=SDK)
    run([node, SDK / "node_modules/playwright/cli.js", "install", "chromium"])
    import runner as go_runner
    env = go_runner.environment()
    go = Path(run(["go", "env", "GOROOT"], env=env, capture=True).strip()) / "bin/go"
    for directory in (ROOT, go_runner.SDK, go_runner.COMPAT / "go"):
        run([go, "mod", "download"], cwd=directory, env=env)
    if before != inputs():
        raise RuntimeError("SDK installation changed locks")
    stamp.write_text(json.dumps({"inputs": before, "inventory": tool_inventory(), "tool_hashes": tool_hashes(),
                                "node": str(node), "node_hash": digest(node),
                                "npm": str(npm), "npm_hash": digest(npm),
                                "runtimes": {version: {"node": str(path), "hash": digest(path)} for version, path in runtimes.items()},
                                "go": str(go), "go_hash": digest(go)}, sort_keys=True) + "\n")


def installed():
    try:
        stamp = json.loads((TOOLS / "installed.json").read_text())
        if stamp["inputs"] != inputs() or stamp["inventory"] != tool_inventory() or stamp["tool_hashes"] != tool_hashes():
            raise ValueError()
        for name in ("node", "npm", "go"):
            if digest(Path(stamp[name])) != stamp[name + "_hash"]:
                raise ValueError()
        if set(stamp["runtimes"]) != set(json.loads((HERE / "node-runtimes.json").read_text())):
            raise ValueError()
        for runtime in stamp["runtimes"].values():
            if digest(Path(runtime["node"])) != runtime["hash"]:
                raise ValueError()
    except (OSError, ValueError, KeyError, TypeError):
        raise RuntimeError("Run make sdk-tools-install SDK_LANGUAGE=typescript") from None
    runtime_licenses()
    return stamp


def generate(node, target):
    run([node, SDK / "node_modules/openapi-typescript/bin/cli.js", ROOT / "api/openapi/control-v1.yaml", "-o", target])


def qualify_catalog(work, stamp, runtime=None, *, workflow=None, fixture=None, pattern="^TestTypeScriptSDKCatalogWorkflow$", timeout="75s", extra_env=None):
    import runner as go_runner
    env = environment(go_runner.environment(offline=True))
    env.update(CATALOG_SDK_NODE=stamp["node"], CATALOG_SDK_WORKFLOW=str(workflow or PROJECT / "catalog_workflow.mjs"),
               CATALOG_SDK_CONSUMER=str((runtime or work / "consumer") / "node_modules/@open-agentic-catalog/client/dist/index.js"))
    env.update(extra_env or {})
    consumer = work / "catalog"
    shutil.copytree(go_runner.COMPAT / "go", consumer, ignore=shutil.ignore_patterns("generated"))
    mod = consumer / "go.mod"
    old = "replace " + go_runner.MODULE + " => ../../.."
    if mod.read_text().count(old) != 1:
        raise RuntimeError("Catalog fixture replacement changed")
    mod.write_text(mod.read_text().replace(old, "replace " + go_runner.MODULE + " => " + json.dumps(str(ROOT))))
    for source in consumer.glob("*.go"):
        source.write_text(source.read_text().replace(go_runner.MODULE + "/test/clientcompat/go/generated", go_runner.SDK_MODULE))
    shutil.copyfile(fixture or HERE / "typescript_consumer_functional_test.go.txt", consumer / "typescript_functional_test.go")
    go = Path(stamp["go"])
    run([go, "mod", "edit", "-require=" + go_runner.SDK_MODULE + "@v0.0.0", "-replace=" + go_runner.SDK_MODULE + "=" + str(go_runner.SDK)], cwd=consumer, env=env)
    run([go, "test", "-tags=functional", "-race", "-run=" + pattern, "-count=1", "-timeout=" + timeout, "-v", "./..."], cwd=consumer, env=env)


def build_package(work, stamp):
    manifest = json.loads((SDK / "package.json").read_text())
    if manifest["version"] != (ROOT / "VERSION").read_text().strip():
        raise RuntimeError("SDK version differs from VERSION")
    package = work / "package"
    package.mkdir()
    for name in ("package.json", "README.md", "LICENSE", "NOTICE"):
        shutil.copyfile(SDK / name, package / name)
    run([stamp["node"], SDK / "node_modules/typescript/bin/tsc", "-p", SDK / "tsconfig.json", "--outDir", package / "dist"])
    shutil.copyfile(SDK / "src/schema.d.ts", package / "dist/schema.d.ts")
    packed = json.loads(run([stamp["node"], stamp["npm"], "pack", "--offline", "--ignore-scripts", "--json", "--pack-destination", work], cwd=package, capture=True))
    archive = work / packed[0]["filename"]
    import runner as shared
    shared.check_archive_notices(archive, SDK, "package/")
    return package, archive


def package_sources():
    return [ROOT / "VERSION", ROOT / "api/openapi/control-v1.yaml", *sorted(SDK.glob("src/*.ts")), *(SDK / name for name in ("package.json", "package-lock.json", "tsconfig.json", "README.md", "LICENSE", "NOTICE"))]


def package():
    stamp = installed()
    parent = ROOT / "output/sdk"
    parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="typescript-package-", dir=parent) as directory:
        before = inputs()
        _, archive = build_package(Path(directory), stamp)
        if inputs() != before:
            raise RuntimeError("TypeScript packaging changed locked inputs")
        import runner as shared
        return shared.retain("typescript", [archive], stamp, source_files=package_sources())


def check(action):
    stamp = installed()
    runtimes = stamp["runtimes"] if action == "check" else {"primary": {"node": stamp["node"]}}
    for version, runtime in runtimes.items():
        print("SDK qualification Node " + version, flush=True)
        check_runtime(action, dict(stamp, node=runtime["node"]))


def check_runtime(action, stamp):
    node, npm = Path(stamp["node"]), Path(stamp["npm"])
    manifest = json.loads((SDK / "package.json").read_text())
    if manifest["version"] != (ROOT / "VERSION").read_text().strip():
        raise RuntimeError("SDK version differs from VERSION")
    watched = [ROOT / "api/openapi/control-v1.yaml", ROOT / "VERSION", *(SDK / name for name in ("README.md", "LICENSE", "NOTICE")), SDK / "package.json", SDK / "package-lock.json", SDK / "tsconfig.json", SDK / "eslint.config.mjs", *SDK.glob("src/*.ts"), *PROJECT.glob("*.mjs"), *PROJECT.glob("*.ts"), Path(__file__), HERE / "typescript_consumer_functional_test.go.txt"]
    watched.extend(p for p in ard_example_sources() if p.is_file())
    original = {p: digest(p) for p in watched}
    parent = ROOT / "output/sdk"
    parent.mkdir(parents=True, exist_ok=True)
    work = Path(tempfile.mkdtemp(prefix="typescript-", dir=parent))
    print("TypeScript SDK output: " + str(work), flush=True)
    try:
        generate(node, work / "first.d.ts")
        if action == "generate":
            shutil.copyfile(work / "first.d.ts", SDK / "src/schema.d.ts")
        else:
            generate(node, work / "second.d.ts")
            if digest(work / "first.d.ts") != digest(work / "second.d.ts") or digest(work / "first.d.ts") != digest(SDK / "src/schema.d.ts"):
                raise RuntimeError("Non-deterministic or stale SDK generation")
            package, archive = build_package(work, stamp)
            # Test output resolves the installed SDK-only dependencies, never UI packages.
            (package / "node_modules").symlink_to(SDK / "node_modules", target_is_directory=True)
            env = dict(environment(), CATALOG_SDK_ENTRY=str(package / "dist/index.js"), CATALOG_SDK_WORK=str(work))
            run([node, SDK / "node_modules/prettier/bin/prettier.cjs", "--check", SDK / "src/index.ts", SDK / "eslint.config.mjs", SDK / "package.json", SDK / "tsconfig.json", *sorted(PROJECT.glob("*.mjs")), *sorted(PROJECT.glob("*.ts"))])
            run([node, SDK / "node_modules/eslint/bin/eslint.js", "--config", SDK / "eslint.config.mjs", "--no-config-lookup", "--max-warnings=0", SDK / "src/index.ts", SDK / "eslint.config.mjs", *sorted(PROJECT.glob("*.mjs"))])
            run([node, SDK / "node_modules/prettier/bin/prettier.cjs", "--check", ARD_EXAMPLE / "index.mts", ARD_EXAMPLE / "package.json", *sorted((PROJECT / "ard-example").glob("*.mjs"))])
            run([node, SDK / "node_modules/eslint/bin/eslint.js", "--config", SDK / "eslint.config.mjs", "--no-config-lookup", "--max-warnings=0", ARD_EXAMPLE / "index.mts", *sorted((PROJECT / "ard-example").glob("*.mjs"))])
            run([node, "--test", "--test-timeout=30000", "--experimental-test-coverage", "--test-coverage-include=" + str(package / "dist/index.js"), *sorted(PROJECT.glob("*.unit.test.mjs"))], env=env)
            run([node, "--test", "--test-timeout=30000", *sorted(PROJECT.glob("*.functional.test.mjs"))], env=env)
            run([node, PROJECT / "contract.mjs"], env=env)
            run([node, PROJECT / "typecheck.mjs"], env=env)
            with tempfile.TemporaryDirectory(prefix="catalog-sdk-npm-consumer-") as directory:
                consumer = Path(directory)
                (consumer / "package.json").write_text(json.dumps({"private": True, "type": "module", "dependencies": {manifest["name"]: "file:" + str(archive)}, "overrides": {"openapi-typescript-helpers": json.loads((SDK / "package-lock.json").read_text())["packages"]["node_modules/openapi-typescript-helpers"]["version"]}}))
                run([node, npm, "install", "--offline", "--ignore-scripts", "--omit=dev", "--no-audit", "--no-fund"], cwd=consumer)
                run([node, PROJECT / "consumer.mjs", consumer], env=env)
                qualify_catalog(work, stamp, consumer)
                qualify_ard_example(work, stamp, consumer)
                run([node, PROJECT / "browser.functional.mjs", consumer], env=env)
            if any(digest(p) != value for p, value in original.items()):
                raise RuntimeError("SDK check changed inputs")
        if action != "generate":
            import runner as shared
            shared.retain("typescript", [archive], stamp, source_files=package_sources())
        print("TypeScript SDK " + action + " passed", flush=True)
    except BaseException:
        print("Failed TypeScript SDK output retained: " + str(work), file=sys.stderr)
        raise
    shutil.rmtree(work)
    print("Owned TypeScript SDK output cleaned", flush=True)


if __name__ == "__main__":
    sys.exit("Use make sdk-* SDK_LANGUAGE=typescript")
