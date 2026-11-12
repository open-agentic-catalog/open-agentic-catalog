"""Explicit extension acquisition and installed-only unit/package/host verification."""
import argparse
import importlib.util
import base64
import json
import tempfile
from pathlib import Path
import subprocess
import sys
import tarfile
import shutil
import urllib.request

ROOT = Path(__file__).resolve().parents[2]
HERE = Path(__file__).resolve().parent
EXTENSION = ROOT / "extensions/vscode"
TOOLS = HERE / ".tools"
sys.path.insert(0, str(ROOT / "test/sdk"))
import typescript_runner as sdk  # noqa: E402


spec = importlib.util.spec_from_file_location("vscode_build", EXTENSION / "tooling/runner.py")
core = importlib.util.module_from_spec(spec)
spec.loader.exec_module(core)
sdk_archive = core.sdk_archive
inputs = core.inputs
source_hashes = core.source_hashes
assert_sources = core.assert_sources
package = core.package
inspect_package = core.inspect_package
check = core.check
build = core.build


REMOTE_TOOLS = {
    "remote-ssh-0.128.0.vsix": ("https://ms-vscode-remote.gallery.vsassets.io/_apis/public/gallery/publisher/ms-vscode-remote/extension/remote-ssh/0.128.0/assetbyname/Microsoft.VisualStudio.Services.VSIXPackage", "458bfdea562840a3a4d9cb246cfe9a212c67a0c9317a8fdd0d22aeaedc82bf2c"),
    "server-linux-x64-1.143.1.tar.gz": ("https://update.code.visualstudio.com/commit:e646868227c3e3a9e3f24277c8c7220be42c0be7/server-linux-x64/stable", "5cc911f00c7fd762afa1f978485d9604ceac5984f2b5e6607d981092f894326a"),
    "chrome-linux64-156.0.8078.47.zip": ("https://storage.googleapis.com/chrome-for-testing-public/156.0.8078.47/linux64/chrome-linux64.zip", "e6c94490f0b6b337225aeb2d2a5034858516b7017356980ba94f5176d8774a50"),
}


def install_remote():
    directory = TOOLS / "remote"
    directory.mkdir(parents=True, exist_ok=True)
    for name, (url, expected) in REMOTE_TOOLS.items():
        path = directory / name
        if path.is_file() and sdk.digest(path) == expected:
            continue
        staged = directory / (name + ".download")
        try:
            with urllib.request.urlopen(url, timeout=60) as source, staged.open("wb") as destination:
                size = 0
                while chunk := source.read(1024 * 1024):
                    size += len(chunk)
                    if size > 256 * 1024 * 1024:
                        raise RuntimeError("Remote qualification archive exceeds bound: " + name)
                    destination.write(chunk)
            if sdk.digest(staged) != expected:
                raise RuntimeError("Remote qualification archive checksum differs: " + name)
            staged.replace(path)
        finally:
            staged.unlink(missing_ok=True)


def tool_hashes(host):
    app = Path(host).parent / "resources/app"
    files = [app / name for name in ("package.json", "product.json", "out/main.js", "out/vs/workbench/api/node/extensionHostProcess.js")]
    files += [EXTENSION / "node_modules/@vscode/test-electron/out/index.js", HERE / "install.mjs", HERE / "runner.py"]
    files += [TOOLS / "remote" / name for name in REMOTE_TOOLS]
    return {str(path.relative_to(ROOT)): sdk.digest(path) for path in files}


def run(args, *, capture=False, env=None, timeout=600, cwd=ROOT):
    return subprocess.run([str(a) for a in args], cwd=cwd, env=env or sdk.environment(), check=True, text=True, capture_output=capture, timeout=timeout).stdout


def install_oras():
    """Explicit test-only acquisition. The extension never installs executables."""
    archive_name = "oras_1.3.6_linux_amd64.tar.gz"
    checksum = "66fb0ad7206f3f6fa77ed5b552049d364ccf4b50b1684554f731f76302b52ca0"
    directory = TOOLS / "oras"
    directory.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="download-", dir=directory) as work:
        run(["gh", "release", "download", "v1.3.6", "--repo", "oras-project/oras", "--pattern", archive_name, "--dir", work])
        archive = Path(work) / archive_name
        if sdk.digest(archive) != checksum:
            raise RuntimeError("ORAS archive checksum differs")
        with tarfile.open(archive) as bundle:
            for name in ("oras", "LICENSE"):
                member = bundle.getmember(name)
                if not member.isfile() or member.size > 64 * 1024 * 1024:
                    raise RuntimeError("Unexpected ORAS release layout")
                with bundle.extractfile(member) as source, (directory / name).open("wb") as target:
                    shutil.copyfileobj(source, target)
        (directory / "oras").chmod(0o700)
    return directory / "oras"


A2A_SAMPLE = TOOLS / "a2a-sample.ts"
A2A_SAMPLE_HASH = "675a5e7d5d5263e34947eca33501fa321c74f77c824e93e1feb0a0ed5286e51f"


def install_a2a_sample():
    """Acquire the unchanged Apache-2.0 SDK Hello World executor, only explicitly."""
    if not A2A_SAMPLE.exists() or sdk.digest(A2A_SAMPLE) != A2A_SAMPLE_HASH:
        content = run(["gh", "api", "repos/a2aproject/a2a-js/contents/src/samples/agents/sample-agent/agent_executor.ts?ref=11cbb295a7933ecb459d4e2af0740bf4570e71b2", "--jq", ".content"], capture=True)
        data = base64.b64decode(content)
        if core.hashlib.sha256(data).hexdigest() != A2A_SAMPLE_HASH:
            raise RuntimeError("A2A sample source checksum differs")
        A2A_SAMPLE.write_bytes(data)


def install():
    stamp = core.install()
    sdk.installed()
    before = inputs()
    TOOLS.mkdir(parents=True, exist_ok=True)
    marker = TOOLS / "installed.json"
    marker.unlink(missing_ok=True)
    output = run([stamp["node"], HERE / "install.mjs", HERE / "runner.py"], capture=True)
    print(output)
    host = Path(next(line.split("=", 1)[1] for line in output.splitlines() if line.startswith("CATALOG_VSCODE_HOST=")))
    oras = install_oras()
    install_remote()
    install_a2a_sample()
    uvx = Path(shutil.which("uvx") or "")
    if not uvx.is_file() or run([uvx, "--version"], capture=True).split()[:2] != ["uvx", "0.13.6"]:
        raise RuntimeError("Install uvx 0.13.6 explicitly for offline native MCP qualification")
    if before != inputs():
        raise RuntimeError("Extension inputs changed during installation; review and reinstall")
    core.write_json(marker, {"uvx": str(uvx.resolve()), "uvx_hash": sdk.digest(uvx), "oras": str(oras), "oras_hash": sdk.digest(oras), "inputs": before, "host": str(host), "host_hash": sdk.digest(host), "tool_hashes": tool_hashes(host), "node_hash": sdk.digest(Path(stamp["node"]))})


def installed():
    build_stamp = core.installed()
    sdk_stamp = sdk.installed()
    try:
        state = json.loads((TOOLS / "installed.json").read_text())
        if state["inputs"] != inputs() or state["host_hash"] != sdk.digest(Path(state["host"])) or state["node_hash"] != sdk.digest(Path(build_stamp["node"])) or state["tool_hashes"] != tool_hashes(state["host"]):
            raise ValueError()
        if state["uvx_hash"] != sdk.digest(Path(state["uvx"])) or sdk.digest(A2A_SAMPLE) != A2A_SAMPLE_HASH or state["oras_hash"] != sdk.digest(Path(state["oras"])):
            raise ValueError()
    except (OSError, ValueError, TypeError, KeyError):
        raise RuntimeError("Missing or stale editor test tools; run make -C extensions/vscode test-tools-install") from None
    return build_stamp | {"go": sdk_stamp["go"], "go_hash": sdk_stamp["go_hash"]}, state


def functional(*, scope="all"):
    if scope not in ("all", "runtime"):
        raise ValueError("Unknown VS Code test scope")
    report = {"schemaVersion": 1, "status": "running", "scope": scope,
              "phases": {"toolingFunctional": "not_run" if scope == "all" else "not_selected", "localFunctional": "not_run", "packagedDesktop": "not_run", "linuxRemoteAndBrowser": "not_run", "linuxMcpRunners": "not_run", "cleanup": "not_confirmed"},
              "unqualified": ["retained_os_url_associations", "native_windows_macos", "os_keychain"]}
    report_path = ROOT / "output/vscode" / ("qualification.json" if scope == "all" else "runtime-qualification.json")
    report_path.parent.mkdir(parents=True, exist_ok=True)
    report_path.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n")
    runtime = None
    try:
        test_stamp, state = installed()
        corpus_files = [ROOT / "test/clientcompat/go/upstream_fixture_functional_test.go", *sorted((ROOT / "internal/adapter/testdata/upstream").rglob("*"))]
        corpus_files = [path for path in corpus_files if path.is_file()]
        report["reviewedUpstreamInputs"] = {str(path.relative_to(ROOT)): sdk.digest(path) for path in corpus_files}
        # Electron needs writable IPC storage before extension activation. A
        # short private path also stays within Unix-domain socket path limits.
        # Create it after the initial report so setup failure cannot retain an
        # old passing result. Never change the host's desktop runtime path.
        runtime = tempfile.TemporaryDirectory(prefix="oac-")
        stamp = {key: value for key, value in test_stamp.items() if key not in ("go", "go_hash")}
        report["testToolchain"] = {"editor": state, "go": test_stamp.get("go"), "go_hash": test_stamp.get("go_hash")}
        build(stamp, mode="test")
        env = dict(core.environment(stamp), XDG_RUNTIME_DIR=runtime.name, CATALOG_VSCODE_LIB=str(EXTENSION / "dist/test/testlib.mjs"), CATALOG_VSCODE_HOST=state["host"], CATALOG_VSCODE_ORAS=state["oras"], CATALOG_VSCODE_UVX=state["uvx"], CATALOG_VSCODE_GO=test_stamp["go"], CATALOG_VSCODE_VSIX=str(package(stamp)))
        env["CATALOG_VSCODE_REMOTE_TOOLS"] = str(TOOLS / "remote")
        manifest = json.loads((ROOT / "output/artifacts/vscode/manifest.json").read_text())
        report["package"] = manifest
        if scope == "all":
            report["phases"]["toolingFunctional"] = "running"
            run([sys.executable, "-B", "-m", "unittest", "discover", "-s", HERE, "-p", "test_functional_tooling.py"], env=core.environment(stamp), timeout=600)
            report["phases"]["toolingFunctional"] = "passed"
        report["phases"]["localFunctional"] = "running"
        core.coverage(stamp, "functional", env=env)
        report["phases"]["localFunctional"] = "passed"
        report["phases"]["packagedDesktop"] = "running"
        with tempfile.TemporaryDirectory(prefix="vscode-catalog-", dir=ROOT / "output/vscode") as directory:
            sdk.qualify_catalog(Path(directory), test_stamp, workflow=HERE / "host.mjs", fixture=HERE / "catalog_host_functional_test.go.txt", pattern="^TestVSCodeCatalogHost$", timeout="450s", extra_env={key: value for key, value in dict(env, CATALOG_VSCODE_RUN_DIR=directory).items() if key.startswith("CATALOG_VSCODE_") or key == "XDG_RUNTIME_DIR"})
        report["phases"]["packagedDesktop"] = "passed"
        report["phases"]["linuxRemoteAndBrowser"] = "running"
        (ROOT / "output/vscode/remote-qualification.json").unlink(missing_ok=True)
        with tempfile.TemporaryDirectory(prefix="vscode-remote-catalog-", dir=ROOT / "output/vscode") as directory:
            sdk.qualify_catalog(Path(directory), test_stamp, workflow=HERE / "remote.mjs", fixture=HERE / "catalog_host_functional_test.go.txt", pattern="^TestVSCodeRemoteCatalogHost$", timeout="240s", extra_env={key: value for key, value in dict(env, CATALOG_VSCODE_RUN_DIR=directory).items() if key.startswith("CATALOG_VSCODE_") or key == "XDG_RUNTIME_DIR"})
        remote = json.loads((ROOT / "output/vscode/remote-qualification.json").read_text())
        if remote.get("status") != "passed" or not remote.get("passed") or remote.get("cleanup") != "passed" or remote.get("packageSHA256") != manifest["sha256"]:
            raise RuntimeError("Remote/browser result is missing, stale or incomplete")
        report["remoteAndBrowser"] = remote
        report["phases"]["linuxRemoteAndBrowser"] = "passed"
        report["phases"]["linuxMcpRunners"] = "running"
        (ROOT / "output/vscode/runner-qualification.json").unlink(missing_ok=True)
        run(["xvfb-run", "-a", stamp["node"], HERE / "runners.mjs"], env=env, timeout=180)
        runners = json.loads((ROOT / "output/vscode/runner-qualification.json").read_text())
        if runners.get("status") != "passed" or runners.get("cleanup") != "passed" or runners.get("packageSHA256") != manifest["sha256"]:
            raise RuntimeError("Native MCP runner result is missing, stale or incomplete")
        report["linuxMcpRunners"] = runners
        report["phases"]["linuxMcpRunners"] = "passed"
        report["phases"]["cleanup"] = "passed"
        assert_sources(manifest["sources"], manifest["inputs"])
        current_corpus = [ROOT / "test/clientcompat/go/upstream_fixture_functional_test.go", *sorted((ROOT / "internal/adapter/testdata/upstream").rglob("*"))]
        if report["reviewedUpstreamInputs"] != {str(path.relative_to(ROOT)): sdk.digest(path) for path in current_corpus if path.is_file()}:
            raise RuntimeError("Reviewed upstream inputs changed during VS Code qualification")
        if sdk.digest(Path(env["CATALOG_VSCODE_VSIX"])) != manifest["sha256"]:
            raise RuntimeError("VSIX changed during qualification; rerun test-vscode")
        if installed() != (test_stamp, state):
            raise RuntimeError("VS Code qualification tools changed during verification")
        report["status"] = "passed"
    except BaseException:
        report["status"] = "failed"
        report["phases"] = {key: "failed" if value == "running" else value for key, value in report["phases"].items()}
        raise
    finally:
        try:
            if runtime is not None:
                runtime.cleanup()
        except BaseException:
            report["status"] = "failed"
            report["phases"]["cleanup"] = "failed"
            raise
        finally:
            report_path.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n")


def screenshots():
    """Capture benign guide images separately; never replace maintained assets."""
    output = ROOT / "output/vscode/guide-screenshots"
    output.mkdir(parents=True, exist_ok=True)
    report = {"schemaVersion": 1, "status": "running", "cleanup": "not_confirmed"}
    report_path = output / "manifest.json"
    core.write_json(report_path, report)
    try:
        test_stamp, state = installed()
        stamp = {key: value for key, value in test_stamp.items() if key not in ("go", "go_hash")}
        artifact = package(stamp)
        manifest = json.loads((ROOT / "output/artifacts/vscode/manifest.json").read_text())
        report["package"] = manifest
        report["testToolchain"] = {"editor": state, "go": test_stamp.get("go"), "go_hash": test_stamp.get("go_hash")}
        env = {"CATALOG_VSCODE_HOST": state["host"], "CATALOG_VSCODE_ORAS": state["oras"], "CATALOG_VSCODE_VSIX": str(artifact), "CATALOG_VSCODE_SCREENSHOTS": str(output)}
        with tempfile.TemporaryDirectory(prefix="capture-", dir=output) as work, tempfile.TemporaryDirectory(prefix="oac-") as runtime:
            env["XDG_RUNTIME_DIR"] = runtime
            env["CATALOG_VSCODE_WORKSPACE"] = str(Path(runtime) / "Catalog workshop")
            sdk.qualify_catalog(Path(work), test_stamp, workflow=HERE / "screenshots.mjs", fixture=HERE / "catalog_host_functional_test.go.txt", pattern="^TestVSCodeGuideScreenshots$", timeout="180s", extra_env=env | {"CATALOG_VSCODE_RUN_DIR": work})
        report["cleanup"] = "passed"
        assert_sources(manifest["sources"], manifest["inputs"])
        if sdk.digest(artifact) != manifest["sha256"] or installed() != (test_stamp, state):
            raise RuntimeError("Screenshot source, package or tools changed during capture")
        report["images"] = {name: sdk.digest(output / name) for name in ("catalog-preview.png", "agent-conversation.png", "mcp-selection-preview.png", "my-tools-installed.png")}
        report["status"] = "passed"
    except BaseException:
        report["status"] = "failed"
        raise
    finally:
        core.write_json(report_path, report)



def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("action", choices=["install", "check", "package", "test", "screenshots"])
    parser.add_argument("--scope", choices=["all", "runtime"], default="all", help="test selection: runtime excludes Make/tooling fixtures, including cleanup")
    args = parser.parse_args()
    if args.action != "test" and args.scope != "all":
        parser.error("--scope applies only to test")
    with core.cleanup.checkout_guard(ROOT):
        if args.action == "install":
            install()
        elif args.action == "test":
            functional(scope=args.scope)
        elif args.action == "screenshots":
            screenshots()
        else:
            stamp = core.installed()
            {"check": check, "package": package}[args.action](stamp)



if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, ValueError) as error:
        sys.exit(str(error))
    except (OSError, ValueError, KeyError, subprocess.SubprocessError):
        sys.exit("VS Code verification failed; inspect the owned local report and installed prerequisites")
