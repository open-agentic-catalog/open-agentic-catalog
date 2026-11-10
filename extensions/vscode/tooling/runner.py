"""Pinned, installed-only extension builds and reproducible local VSIX packages."""
import argparse
import base64
from collections import Counter
import hashlib
import json
import os
from pathlib import Path
import shutil
import socket
import stat
import subprocess
import sys
import tempfile
import xml.etree.ElementTree as ET
import zipfile

ROOT = Path(__file__).resolve().parents[3]
EXTENSION = ROOT / "extensions/vscode"
HERE = Path(__file__).resolve().parent
TOOLS = EXTENSION / ".tools"
sys.path.insert(0, str(ROOT / "test/sdk"))
sys.path.insert(0, str(ROOT / "test/packaging"))
import node_runtime  # noqa: E402
import typescript_runner as sdk_provenance  # noqa: E402
import clean as cleanup  # noqa: E402

MEDIA = ("catalog.js", "catalog.css", "catalog.svg", "a2a.js", "catalog-preview.png", "agent-conversation.png", "mcp-selection-preview.png", "my-tools-installed.png", "catalog-icon.png")
PAYLOAD = {"extension/package.json": "package.json", "extension/dist/extension.cjs": "dist/extension.cjs",
           "extension/readme.md": "README.md", "extension/changelog.md": "CHANGELOG.md", "extension/LICENSE.txt": "LICENSE",
           "extension/NOTICE": "NOTICE", "extension/THIRD_PARTY_NOTICES.txt": "THIRD_PARTY_NOTICES.txt"} | {"extension/media/" + name: "media/" + name for name in MEDIA}
EXCLUDED_COVERAGE = ("extension", "views", "connection-ui", "mcp-host", "skills-host", "plugins-host", "a2a-host", "opinions-host", "package-credentials-host")


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def write_json(path, value):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(prefix=".complete-", dir=path.parent, delete=False) as output:
        temporary = Path(output.name)
        output.write((json.dumps(value, indent=2, sort_keys=True) + "\n").encode())
    try:
        temporary.replace(path)
    finally:
        temporary.unlink(missing_ok=True)


def sdk_sources():
    return sdk_provenance.package_sources()


def sdk_archive():
    directory = ROOT / "output/artifacts/sdk/typescript"
    try:
        manifest = json.loads((directory / "manifest.json").read_text())
        expected = {str(path.relative_to(ROOT)): digest(path) for path in sdk_sources()}
        version = (ROOT / "VERSION").read_text().strip()
        if manifest["sources"] != expected or manifest["releaseBase"] != version:
            raise ValueError()
        name = "open-agentic-catalog-client-" + version + ".tgz"
        archive = directory / name
        integrity = "sha512-" + base64.b64encode(hashlib.sha512(archive.read_bytes()).digest()).decode("ascii")
        lock = json.loads((EXTENSION / "package-lock.json").read_text())["packages"]["node_modules/@open-agentic-catalog/client"]
        if manifest["artifacts"].get(name) != digest(archive) or lock["integrity"] != integrity:
            raise ValueError()
    except (OSError, ValueError, KeyError, TypeError):
        raise RuntimeError("Build the current SDK with make sdk-package SDK_LANGUAGE=typescript; review and refresh the extension SDK lock if needed") from None
    return expected | {str(archive.relative_to(ROOT)): digest(archive)}


def inputs():
    paths = [EXTENSION / "package.json", EXTENSION / "package-lock.json", ROOT / "sdk/typescript/.nvmrc", ROOT / "test/sdk/node-runtimes.json", ROOT / "test/sdk/node_runtime.py", HERE / "runner.py"]
    return sdk_archive() | {str(path.relative_to(ROOT)): digest(path) for path in paths}


def environment(stamp=None, *, acquire=False):
    env = {key: value for key, value in os.environ.items() if not key.lower().startswith(("npm_config_", "node_", "playwright_"))}
    env.update(NPM_CONFIG_CACHE=str(TOOLS / "npm-cache"), NPM_CONFIG_USERCONFIG=os.devnull,
               NPM_CONFIG_GLOBALCONFIG=str(TOOLS / "empty.npmrc"), NPM_CONFIG_IGNORE_SCRIPTS="true",
               NPM_CONFIG_OFFLINE="false" if acquire else "true", NPM_CONFIG_AUDIT="false", NPM_CONFIG_FUND="false", NPM_CONFIG_UPDATE_NOTIFIER="false")
    if stamp:
        env["PATH"] = str(Path(stamp["node"]).parent) + os.pathsep + env.get("PATH", "")
    return env


def run(args, *, stamp=None, capture=False, env=None, timeout=600, cwd=ROOT, acquire=False):
    return subprocess.run([str(arg) for arg in args], cwd=cwd, env=env if env is not None else environment(stamp, acquire=acquire), check=True, text=True, capture_output=capture, timeout=timeout).stdout


def tool_hashes():
    names = ("typescript/lib/typescript.js", "eslint/bin/eslint.js", "prettier/bin/prettier.cjs", "esbuild/lib/main.js", "@vscode/vsce/out/main.js")
    files = [EXTENSION / "node_modules" / name for name in names]
    files += list((EXTENSION / "node_modules/@esbuild").glob("*/bin/esbuild"))
    return {str(path.relative_to(EXTENSION)): digest(path) for path in files}


def inventory():
    lock = json.loads((EXTENSION / "package-lock.json").read_text())
    found = {}
    for name, data in lock["packages"].items():
        if not name or data.get("optional"):
            continue
        actual = json.loads((EXTENSION / name / "package.json").read_text())
        if actual["version"] != data["version"] or not data.get("integrity"):
            raise ValueError("Extension dependency drift")
        found[name] = {"version": actual["version"], "license": actual.get("license")}
    return found


def install():
    before = inputs()
    TOOLS.mkdir(parents=True, exist_ok=True)
    (TOOLS / "empty.npmrc").write_text("")
    marker = TOOLS / "installed.json"
    marker.unlink(missing_ok=True)
    node = node_runtime.install((ROOT / "sdk/typescript/.nvmrc").read_text().strip(), TOOLS / "node")
    npm_version = json.loads((EXTENSION / "package.json").read_text())["packageManager"].removeprefix("npm@")
    npm = node_runtime.install_npm(node, npm_version, TOOLS / "npm", env=environment(acquire=True))
    stamp = {"node": str(node), "npm": str(npm)}
    version = "v" + (ROOT / "sdk/typescript/.nvmrc").read_text().strip()
    if run([node, "--version"], stamp=stamp, capture=True).strip() != version or run([node, stamp["npm"], "--version"], stamp=stamp, capture=True).strip() != npm_version:
        raise RuntimeError("Pinned Node/npm required")
    run([node, stamp["npm"], "ci", "--ignore-scripts", "--no-audit", "--no-fund"], stamp=stamp, cwd=EXTENSION, acquire=True)
    if before != inputs():
        raise RuntimeError("Extension inputs changed during installation; review and reinstall")
    stamp.update(inputs=before, node_hash=digest(node), npm_hash=digest(stamp["npm"]), tool_hashes=tool_hashes(), inventory=inventory())
    write_json(marker, stamp)
    return stamp


def installed():
    try:
        stamp = json.loads((TOOLS / "installed.json").read_text())
        if stamp["inputs"] != inputs() or stamp["tool_hashes"] != tool_hashes() or stamp["inventory"] != inventory():
            raise ValueError()
        if any(digest(stamp[name]) != stamp[name + "_hash"] for name in ("node", "npm")):
            raise ValueError()
    except (OSError, ValueError, KeyError, TypeError):
        raise RuntimeError("Missing or stale extension build tools; run make -C extensions/vscode tools-install") from None
    return stamp


def source_hashes():
    sources = [*EXTENSION.glob("src/*.ts"), *EXTENSION.glob("media/*"), *EXTENSION.glob("test/*.mjs"), *HERE.glob("*.py"), *HERE.glob("*.mjs"), *EXTENSION.glob(".vscode/*.json"), *(EXTENSION / name for name in ("README.md", "CHANGELOG.md", "LICENSE", "NOTICE", "Makefile", "tsconfig.json", "eslint.config.mjs", ".vscodeignore")), ROOT / "Makefile", ROOT / "test/packaging/clean.py", ROOT / "test/sdk/typescript_runner.py", *sorted((ROOT / "test/vscode").glob("*.py")), *sorted((ROOT / "test/vscode").glob("*.mjs")), *sorted((ROOT / "test/vscode").glob("*.cjs")), *sorted((ROOT / "test/vscode").glob("*.go.txt")), ROOT / "test/clientcompat/go/fixture_functional_test.go"]
    return {str(path.relative_to(ROOT)): digest(path) for path in sorted(sources)}


def assert_sources(sources, input_state):
    if sources != source_hashes() or input_state != inputs():
        raise RuntimeError("Extension inputs changed during verification; rerun the selected target")


def build(stamp, *, destination=None, mode="development"):
    run([stamp["node"], HERE / "build.mjs", mode, destination or EXTENSION / "dist"], stamp=stamp, timeout=None if mode == "watch" else 120)


def validate_coverage_reports(directory, tests, modules):
    """Admit the pinned Node reports, including their executed/file inventory."""
    try:
        junit = ET.parse(directory / "tests.xml").getroot()
        cases = junit.findall(".//testcase")
        expected_tests = {Path(path).resolve() for path in tests}
        if junit.tag != "testsuites" or not cases or not expected_tests:
            raise ValueError()
        identities = set()
        seen_tests = set()
        parents = {child: parent for parent in junit.iter() for child in parent}
        for case in cases:
            if not case.get("file") or not case.get("name") or list(case):
                # Node emits skipped/todo/failure as testcase children. Passing
                # cases are empty; do not accept an unfamiliar result shape.
                raise ValueError()
            file = (ROOT / case.attrib["file"]).resolve()
            if (ROOT / case.attrib["name"]).resolve() == file:
                # Node emits an implicit passing file case when it discovers no
                # actual test(). That is not execution of selected assertions.
                raise ValueError()
            ancestry = []
            parent = parents.get(case)
            while parent is not None and parent is not junit:
                if parent.tag != "testsuite" or not parent.get("name"):
                    raise ValueError()
                ancestry.append(parent.attrib["name"])
                parent = parents.get(parent)
            # A child label may legitimately repeat in different parents.
            # Preserve boundaries: dotted parent names are not path separators.
            identity = (file, tuple(reversed(ancestry)), case.attrib["name"])
            if identity in identities or file not in expected_tests:
                raise ValueError()
            identities.add(identity)
            seen_tests.add(file)
        if seen_tests != expected_tests:
            raise ValueError()
        expected_modules = {Path(path).resolve() for path in modules}
        seen_modules = set()
        totals = dict.fromkeys(("LF", "LH", "FNF", "FNH", "BRF", "BRH"), 0)
        record = None
        for line in (directory / "lcov.info").read_text().splitlines():
            if line.startswith("SF:"):
                file = (ROOT / line[3:]).resolve()
                if record is not None or file in seen_modules or file not in expected_modules:
                    raise ValueError()
                seen_modules.add(file)
                record = {"lines": {}, "declarations": set(), "functions": [], "branches": {}, "counts": {}}
            elif line == "end_of_record":
                if record is None or set(record["counts"]) != set(totals):
                    raise ValueError()
                if Counter(name for _, name in record["declarations"]) != Counter(name for _, name in record["functions"]):
                    raise ValueError()
                observed = {"LF": len(record["lines"]), "LH": sum(n > 0 for n in record["lines"].values()),
                            "FNF": len(record["functions"]), "FNH": sum(n > 0 for n, _ in record["functions"]),
                            "BRF": len(record["branches"]), "BRH": sum(n > 0 for n in record["branches"].values())}
                if record["counts"] != observed or not observed["LF"]:
                    raise ValueError()
                for key, value in observed.items():
                    totals[key] += value
                record = None
            elif line.startswith(("DA:", "FN:", "FNDA:", "BRDA:")):
                if record is None:
                    raise ValueError()
                kind, value = line.split(":", 1)
                if kind == "DA":
                    number, count = value.split(",", 1)
                    number, count = int(number), int(count)
                    if number <= 0 or count < 0 or number in record["lines"]:
                        raise ValueError()
                    record["lines"][number] = count
                elif kind in ("FN", "FNDA"):
                    number, name = value.split(",", 1)
                    number = int(number)
                    if not name or number < (1 if kind == "FN" else 0):
                        raise ValueError()
                    if kind == "FN":
                        if (number, name) in record["declarations"]:
                            raise ValueError()
                        record["declarations"].add((number, name))
                    else:
                        record["functions"].append((number, name))
                else:
                    number, block, branch, count = value.split(",")
                    identity = (int(number), int(block), int(branch))
                    count = 0 if count == "-" else int(count)
                    if identity[0] < 1 or min(identity[1:]) < 0 or count < 0 or identity in record["branches"]:
                        raise ValueError()
                    record["branches"][identity] = count
            elif line.split(":", 1)[0] in totals:
                key, value = line.split(":", 1)
                if record is None or key in record["counts"] or int(value) < 0:
                    raise ValueError()
                record["counts"][key] = int(value)
            elif line and not line.startswith("TN:"):
                raise ValueError()
        if record is not None or seen_modules != expected_modules or not seen_modules:
            raise ValueError()
        return {"tests": len(cases), "testFiles": sorted(str(path.relative_to(ROOT)) for path in seen_tests),
                "modules": sorted(str(path.relative_to(ROOT)) for path in seen_modules), "counts": totals}
    except (OSError, ValueError, KeyError, IndexError, ET.ParseError):
        raise RuntimeError("Invalid or incomplete extension coverage/test reports") from None


def coverage(stamp, kind, *, env=None):
    if kind not in ("unit", "functional"):
        raise ValueError("Unknown extension coverage scope")
    directory = ROOT / "output/vscode" / (kind + "-coverage")
    directory.mkdir(parents=True, exist_ok=True)
    floors = (85, 80, 90) if kind == "unit" else (60, 60, 70)
    report = {"status": "running", "scope": "handwritten core; native adapters and renderer excluded", "floors": dict(zip(("lines", "branches", "functions"), floors, strict=True))}
    write_json(directory / "manifest.json", report)
    try:
        report.update(sources=source_hashes(), inputs=inputs())
        tests = sorted((EXTENSION / "test").glob("*." + kind + ".test.mjs"))
        excluded = set(EXCLUDED_COVERAGE) | ({"help"} if kind == "functional" else set())
        modules = [EXTENSION / "dist/test" / (path.stem + ".js") for path in sorted((EXTENSION / "src").glob("*.ts")) if path.stem not in excluded]
        if not tests or not modules:
            raise RuntimeError("Empty extension test or core source selection")
        with tempfile.TemporaryDirectory(prefix="." + kind + "-coverage-", dir=directory.parent) as work:
            stage = Path(work)
            args = [stamp["node"], "--test", "--test-timeout=120000", "--experimental-test-coverage", "--test-coverage-include=" + str(EXTENSION / "dist/test/*.js")]
            args += ["--test-coverage-exclude=" + str(EXTENSION / "dist/test" / (name + ".js")) for name in excluded]
            args += ["--test-coverage-" + name + "=" + str(value) for name, value in zip(("lines", "branches", "functions"), floors, strict=True)]
            args += ["--test-reporter=spec", "--test-reporter-destination=stdout", "--test-reporter=junit", "--test-reporter-destination=" + str(stage / "tests.xml"), "--test-reporter=lcov", "--test-reporter-destination=" + str(stage / "lcov.info"), *tests]
            run(args, stamp=stamp, env=env or dict(environment(stamp), CATALOG_VSCODE_LIB=str(EXTENSION / "dist/test/testlib.mjs")))
            report["results"] = validate_coverage_reports(stage, tests, modules)
            assert_sources(report["sources"], report["inputs"])
            report["reports"] = {name: digest(stage / name) for name in ("tests.xml", "lcov.info")}
            for name in report["reports"]:
                (stage / name).replace(directory / name)
        # The passing manifest is the last completion marker. Partial report
        # replacement or cleanup failure can never retain a passing receipt.
        report["status"] = "passed"
    except BaseException:
        report["status"] = "failed"
        raise
    finally:
        write_json(directory / "manifest.json", report)


def check(stamp):
    node = stamp["node"]
    run([node, EXTENSION / "node_modules/typescript/bin/tsc", "-p", EXTENSION / "tsconfig.json", "--noEmit"], stamp=stamp)
    scripts = [*sorted(HERE.glob("*.mjs")), *sorted((ROOT / "test/vscode").glob("*.mjs")), *sorted((ROOT / "test/vscode").glob("*.cjs"))]
    run([node, EXTENSION / "node_modules/prettier/bin/prettier.cjs", "--check", "src", "test", "media", ".vscode", "package.json", "tsconfig.json", "eslint.config.mjs", *scripts], stamp=stamp, cwd=EXTENSION)
    run([node, EXTENSION / "node_modules/eslint/bin/eslint.js", "--no-config-lookup", "--config", EXTENSION / "eslint.config.mjs", "--max-warnings=0", EXTENSION / "src", EXTENSION / "test", EXTENSION / "media", *scripts], stamp=stamp)
    build(stamp, mode="test")
    coverage(stamp, "unit")


def inspect_package(path, stage):
    """Check the exact staged payload and bounded VSCE-generated metadata."""
    stage = Path(stage)
    with zipfile.ZipFile(path) as archive:
        names = archive.namelist()
        expected = set(PAYLOAD) | {"extension.vsixmanifest", "[Content_Types].xml"}
        if set(names) != expected or len(names) != len(expected):
            raise RuntimeError("VSIX contains missing, duplicate or unexpected members")
        for info in archive.infolist():
            mode = info.external_attr >> 16
            if info.flag_bits & 1 or info.is_dir() or (stat.S_IFMT(mode) not in (0, stat.S_IFREG)) or info.compress_type not in (zipfile.ZIP_STORED, zipfile.ZIP_DEFLATED):
                raise RuntimeError("VSIX has unsupported archive metadata")
            if info.filename in PAYLOAD:
                expected_bytes = (stage / PAYLOAD[info.filename]).read_bytes()
                if info.file_size != len(expected_bytes) or archive.read(info) != expected_bytes:
                    raise RuntimeError("VSIX payload differs from staged build: " + info.filename)
            elif info.file_size > 64 * 1024:
                raise RuntimeError("VSIX metadata exceeds bound")
        metadata = json.loads(archive.read("extension/package.json"))
        if metadata["main"] != "./dist/extension.cjs" or metadata["version"] != (ROOT / "VERSION").read_text().strip():
            raise RuntimeError("VSIX entrypoint or release differs")
        assets = [metadata["main"], metadata.get("icon")]
        contributes = metadata.get("contributes", {})
        for entries in contributes.get("viewsContainers", {}).values():
            assets.extend(entry.get("icon") for entry in entries)
        for command in contributes.get("commands", []):
            icon = command.get("icon")
            if icon is not None and not (isinstance(icon, str) and icon.startswith("$(") and icon.endswith(")")):
                assets.extend(icon.values() if isinstance(icon, dict) else [icon])
        if any(not isinstance(asset, str) or "extension/" + asset.removeprefix("./") not in expected for asset in assets):
            raise RuntimeError("VSIX declares a missing runtime or contributed asset")
        for name in ("extension.vsixmanifest", "[Content_Types].xml"):
            data = archive.read(name)
            if b"<!" in data:
                raise RuntimeError("VSIX metadata declarations refused")
            document = ET.fromstring(data)
            if name == "extension.vsixmanifest":
                identity = document.find("{*}Metadata/{*}Identity")
                if identity is None or any(identity.get(key) != metadata[field] for key, field in (("Id", "name"), ("Publisher", "publisher"), ("Version", "version"))):
                    raise RuntimeError("VSIX identity differs from package")
                for asset in document.findall("{*}Assets/{*}Asset"):
                    if asset.get("Path") not in expected:
                        raise RuntimeError("VSIX declares a missing asset")


def normalize(path):
    target = path.with_suffix(".normalized")
    try:
        with zipfile.ZipFile(path) as source, zipfile.ZipFile(target, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=9) as output:
            for name in sorted(source.namelist()):
                info = zipfile.ZipInfo(name, (1980, 1, 1, 0, 0, 0))
                info.create_system = 3
                info.external_attr = (stat.S_IFREG | 0o644) << 16
                info.compress_type = zipfile.ZIP_DEFLATED
                output.writestr(info, source.read(name), compresslevel=9)
        target.replace(path)
    finally:
        target.unlink(missing_ok=True)


def package(stamp):
    sources, input_state = source_hashes(), inputs()
    parent = ROOT / "output/vscode"
    report = {"status": "running", "sources": sources, "inputs": input_state}
    write_json(parent / "package-attempt.json", report)
    try:
        staging = parent / "staging"
        staging.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory(prefix="package-", dir=staging) as directory:
            stage = Path(directory) / "extension"
            stage.mkdir()
            for relative in ("package.json", ".vscodeignore", "README.md", "CHANGELOG.md", "LICENSE", "NOTICE", *("media/" + name for name in MEDIA)):
                target = stage / relative
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(EXTENSION / relative, target)
            build(stamp, destination=stage, mode="production")
            artifact = Path(directory) / "package.vsix"
            run([stamp["node"], EXTENSION / "node_modules/@vscode/vsce/vsce", "package", "--no-dependencies", "--allow-missing-repository", "--no-rewrite-relative-links", "--out", artifact], stamp=stamp, cwd=stage)
            inspect_package(artifact, stage)
            normalize(artifact)
            inspect_package(artifact, stage)
            assert_sources(sources, input_state)
            output = ROOT / "output/artifacts/vscode"
            output.mkdir(parents=True, exist_ok=True)
            target = output / ("open-agentic-catalog-" + (ROOT / "VERSION").read_text().strip() + ".vsix")
            manifest = {"releaseBase": (ROOT / "VERSION").read_text().strip(), "artifact": target.name, "sha256": digest(artifact), "inputs": input_state, "sources": sources, "toolchain": stamp}
            artifact.replace(target)
            write_json(output / "manifest.json", manifest)
            report.update(status="passed", artifact=str(target.relative_to(ROOT)), sha256=manifest["sha256"])
            print("VSIX: " + str(target))
            print("SHA-256: " + manifest["sha256"])
            return target
    except BaseException:
        report["status"] = "failed"
        raise
    finally:
        write_json(parent / "package-attempt.json", report)


def verify(stamp):
    try:
        directory = ROOT / "output/artifacts/vscode"
        manifest = json.loads((directory / "manifest.json").read_text())
        name = "open-agentic-catalog-" + (ROOT / "VERSION").read_text().strip() + ".vsix"
        if manifest["artifact"] != name or manifest["sha256"] != digest(directory / name) or manifest["toolchain"] != stamp:
            raise ValueError()
        assert_sources(manifest["sources"], manifest["inputs"])
    except (OSError, ValueError, KeyError, TypeError):
        raise RuntimeError("Missing, stale or replaced VSIX; run the package target and verify its new manifest") from None
    print("Verified current local VSIX: " + str(directory / name))
    print("SHA-256: " + manifest["sha256"])
    return manifest


def launch_editor(editor, stamp, profile, *, port=9229, extra_args=()):
    """Start the explicit local development editor in separate owned storage."""
    profile = Path(profile)
    env = environment(stamp)
    env.pop("ELECTRON_RUN_AS_NODE", None)
    run([editor, "--inspect-extensions=" + str(port), "--extensionDevelopmentPath=" + str(EXTENSION),
         "--user-data-dir=" + str(profile / "user"), "--extensions-dir=" + str(profile / "extensions"),
         "--shared-data-dir=" + str(profile / "shared"), "--disable-telemetry", "--disable-updates", *extra_args], stamp=stamp, env=env, timeout=None)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("install", "check", "build", "watch", "package", "verify", "debug", "clean", "clean-dry-run"))
    parser.add_argument("--editor", default=os.environ.get("CATALOG_VSCODE_EXECUTABLE"), help="absolute local VS Code executable for the development host")
    args = parser.parse_args()
    action = args.action
    if action == "debug":
        if not args.editor or not Path(args.editor).is_absolute() or not Path(args.editor).is_file():
            raise RuntimeError("Select the local editor: make debug CATALOG_VSCODE_EXECUTABLE=/absolute/path/to/code")
        with socket.socket() as probe:
            try:
                probe.bind(("127.0.0.1", 9229))
            except OSError:
                raise RuntimeError("Development debugger port 9229 is occupied; close the previous owned debug host") from None
        with cleanup.checkout_guard(ROOT):
            stamp = installed()
            build(stamp)
        profile = ROOT / "output/vscode/development"
        print("Start F5: Catalog: Attach to development host (127.0.0.1:9229)", flush=True)
        launch_editor(args.editor, stamp, profile)
        return
    if action.startswith("clean"):
        cleanup.clean(ROOT, dry_run=action == "clean-dry-run", scope="vscode")
        return
    with cleanup.checkout_guard(ROOT):
        if action == "install":
            install()
        else:
            stamp = installed()
            if action in ("build", "watch"):
                build(stamp, mode="watch" if action == "watch" else "development")
            else:
                {"check": check, "package": package, "verify": verify}[action](stamp)


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        sys.exit(130)
    except (RuntimeError, ValueError) as error:
        sys.exit(str(error))
    except (OSError, KeyError, subprocess.SubprocessError, zipfile.BadZipFile):
        sys.exit("VS Code build failed; check the selected prerequisites and local reports")
