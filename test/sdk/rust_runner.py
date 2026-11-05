"""Independent Rust SDK acquisition, deterministic generation and qualification."""

from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
import tomllib

from rust import generation

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
PROJECT = HERE / "rust"
SDK = ROOT / "sdk/rust"
REPORT = ROOT / "examples/rust-catalog-report"
TOOLS = HERE / ".tools/rust"
ADVISORIES = ROOT / "output/sdk-rust-advisories"
PINS = PROJECT / "tools.json"


def pins():
    return json.loads(PINS.read_text())


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def run(args, *, cwd=ROOT, env=None, capture=False, seconds=600):
    return subprocess.run(
        [str(a) for a in args],
        cwd=cwd,
        env=env,
        check=True,
        timeout=seconds,
        text=True,
        capture_output=capture,
    ).stdout


def toolchain(version):
    return TOOLS / "rustup/toolchains" / (version + "-" + pins()["host"]) / "bin"


def environment(version=None, *, offline=True, target=None):
    selected = version or pins()["rust"]
    env = {
        k: v
        for k, v in os.environ.items()
        if not k.startswith(("CARGO_", "RUST", "LLVM_", "GIT_"))
        and k
        not in (
            "LD_PRELOAD",
            "LD_LIBRARY_PATH",
            "DYLD_INSERT_LIBRARIES",
            "CC",
            "CXX",
            "AR",
        )
    }
    binary = toolchain(selected)
    env.update(
        RUSTUP_HOME=str(TOOLS / "rustup"),
        CARGO_HOME=str(TOOLS / "cargo"),
        RUSTC=str(binary / "rustc"),
        RUSTDOC=str(binary / "rustdoc"),
        RUSTUP_TOOLCHAIN=selected + "-" + pins()["host"],
        CARGO_BUILD_JOBS="4",
        CARGO_INCREMENTAL="0",
        CARGO_TERM_COLOR="never",
        CARGO_NET_OFFLINE="true" if offline else "false",
        CARGO_LLVM_COV_SETUP="no",
        CARGO_TARGET_DIR=str(target or TOOLS / ("build-" + selected)),
        CATALOG_RUST_ADVISORY_DIR=str(ADVISORIES),
        PATH=str(binary)
        + os.pathsep
        + str(TOOLS / "bin")
        + os.pathsep
        + env.get("PATH", ""),
    )
    return env


def inputs():
    paths = [
        PINS,
        SDK / "Cargo.toml",
        SDK / "Cargo.lock",
        PROJECT / "consumer/Cargo.toml",
        PROJECT / "consumer/Cargo.lock",
        REPORT / "Cargo.toml",
        REPORT / "Cargo.lock",
    ]
    paths += [PROJECT / "generator.lock"]
    for directory in (ROOT, ROOT / "sdk/go", ROOT / "test/clientcompat/go"):
        paths += [directory / name for name in ("go.mod", "go.sum")]
    return {str(p.relative_to(ROOT)): digest(p) for p in paths}


def binary_paths():
    config = pins()
    binaries = [TOOLS / "bin/openapi-to-rust"]
    for version in (config["rust"], config["msrv"]):
        binaries += [
            toolchain(version) / name for name in ("rustc", "cargo", "rustdoc")
        ]
    binaries += [
        toolchain(config["rust"]) / name
        for name in ("rustfmt", "cargo-clippy", "clippy-driver")
    ]
    llvm = toolchain(config["rust"]).parent / "lib/rustlib" / config["host"] / "bin"
    return binaries + [llvm / name for name in ("llvm-cov", "llvm-profdata")]


def owned_directory(path):
    """Reject redirected inputs before writing or extracting SDK-owned files."""
    path = Path(path)
    for entry in [path, *path.parents]:
        if entry == ROOT.parent or entry == Path(entry.anchor):
            break
        if entry.is_symlink() or (entry != ROOT and entry.is_mount()):
            raise RuntimeError("Redirected Rust SDK tool/output directory")
    path.mkdir(parents=True, exist_ok=True)


def extract(archive, destination, *, strip_root=False):
    owned_directory(destination)
    total = 0
    with tarfile.open(archive) as source:
        members = source.getmembers()
        if len(members) > 12000:
            raise RuntimeError("Rust archive file limit")
        for member in members:
            parts = Path(member.name).parts
            if (
                member.issym()
                or member.islnk()
                or not (member.isfile() or member.isdir())
                or Path(member.name).is_absolute()
                or ".." in parts
            ):
                raise RuntimeError("Unsafe Rust archive member")
            total += member.size
            if total > 512 << 20:
                raise RuntimeError("Rust archive byte limit")
            if strip_root:
                if len(parts) < 2:
                    continue
                member.name = str(Path(*parts[1:]))
            source.extract(member, destination, filter="data")


def acquire_source(name, pin):
    archive = TOOLS / "downloads" / (name + ".tar.gz")
    owned_directory(archive.parent)
    expected = pin["archiveSha256"]
    if archive.is_symlink():
        raise RuntimeError("Redirected Rust source archive")
    if not archive.exists() or digest(archive) != expected:
        with archive.open("wb") as output:
            subprocess.run(
                [
                    "gh",
                    "api",
                    "repos/" + pin["repository"] + "/tarball/" + pin["revision"],
                ],
                stdout=output,
                check=True,
                timeout=180,
            )
    if digest(archive) != expected:
        raise RuntimeError("Pinned Rust source digest mismatch")
    destination = TOOLS / name
    if destination.exists():
        owned_directory(destination)
        shutil.rmtree(destination)
    extract(archive, destination, strip_root=True)
    return destination


def cargo(
    version,
    *args,
    manifest=None,
    offline=True,
    target=None,
    capture=False,
    extra_env=None,
):
    env = environment(version, offline=offline, target=target)
    env.update(extra_env or {})
    arguments = list(args)
    position = arguments.index("--") if "--" in arguments else len(arguments)
    arguments[position:position] = ["--manifest-path", manifest or SDK / "Cargo.toml"]
    return run([toolchain(version) / "cargo", *arguments], env=env, capture=capture)


def release_allowed(name, version, entry, *, cutoff=True):
    if not entry.get("pubtime") or entry.get("yanked"):
        return False
    config = pins()
    if not cutoff or entry["pubtime"] <= config["dependencyCutoff"]:
        return True
    return any(
        fix["name"] == name and fix["version"] == version
        and fix["checksum"] == entry.get("cksum")
        and fix["released"] == entry["pubtime"]
        for fix in config.get("securityFixes", [])
    )


def dependency_inventory(manifest, *, cutoff=True):
    value = json.loads(
        cargo(
            pins()["rust"],
            "metadata",
            "--frozen",
            "--format-version=1",
            manifest=manifest,
            capture=True,
        )
    )
    packages = []
    for package in value["packages"]:
        source = package["source"]
        if source is None:
            continue
        if source != "registry+https://github.com/rust-lang/crates.io-index":
            raise RuntimeError("Unapproved Rust dependency source")
        name, version = package["name"], package["version"]
        lowered = name.lower()
        prefix = (
            "1/" + lowered
            if len(lowered) == 1
            else "2/" + lowered
            if len(lowered) == 2
            else "3/" + lowered[0] + "/" + lowered
            if len(lowered) == 3
            else lowered[:2] + "/" + lowered[2:4] + "/" + lowered
        )
        matches = list((TOOLS / "cargo/registry/index").glob("*/.cache/" + prefix))
        if len(matches) != 1:
            raise RuntimeError("Missing pinned Rust registry metadata")
        records = [
            json.loads(x)
            for x in matches[0].read_bytes().split(b"\x00")
            if x.startswith(b"{")
        ]
        entry = next((r for r in records if r["vers"] == version), {})
        if not release_allowed(name, version, entry, cutoff=cutoff):
            raise RuntimeError(
                "Rust dependency release age/yank check failed: " + name + " " + version
            )
        manifest_path = Path(package["manifest_path"])
        license_files = {
            str(p.relative_to(manifest_path.parent)): digest(p)
            for p in manifest_path.parent.iterdir()
            if p.is_file()
            and p.name.lower().startswith(("license", "copying", "notice"))
        }
        packages.append(
            {
                "name": name,
                "version": version,
                "license": package["license"],
                "released": entry["pubtime"],
                "checksum": entry["cksum"],
                "licenseFiles": license_files,
            }
        )
    return sorted(packages, key=lambda p: (p["name"], p["version"]))


def policy(manifest, *, advisories=True):
    command = [
        TOOLS / "bin/cargo-deny",
        "--manifest-path",
        manifest,
        "--config",
        PROJECT / "deny.toml",
        "--frozen",
        "check",
        "licenses",
        "sources",
    ]
    if advisories:
        command.append("advisories")
    run(command, env=environment())


def tool_policy_text():
    # One informational exception for an unchanged, file-only generator. This
    # never applies to SDK/runtime dependencies or a future security advisory.
    return (
        (PROJECT / "deny.toml").read_text()
        + """
ignore = [{ id = "RUSTSEC-2024-0449", reason = "Generator-only unsafe-libyaml is unmaintained. This pinned CLI parses only the maintained local OpenAPI file; no remote or caller-supplied document is accepted. No known vulnerability is waived." }]
"""
    )


def tool_advisories():
    for name in ("generator", "deny-source"):
        policy(TOOLS / name / "Cargo.toml", advisories=False)
        config = (
            TOOLS / "generator-deny.toml"
            if name == "generator"
            else PROJECT / "deny.toml"
        )
        run(
            [
                TOOLS / "bin/cargo-deny",
                "--manifest-path",
                TOOLS / name / "Cargo.toml",
                "--config",
                config,
                "--exclude-dev",
                "--frozen",
                "check",
                "advisories",
            ],
            env=environment(),
        )


def verify_freshness(stamp, now=None):
    now = now or datetime.now(timezone.utc)
    fetched = datetime.fromisoformat(stamp["advisory"]["fetchedAt"])
    age = (now - fetched).total_seconds()
    if fetched.tzinfo is None or age < -300 or age > pins()["advisoryMaxAgeSeconds"]:
        raise RuntimeError(
            "Rust advisory snapshot is stale or future-dated; rerun explicit installation"
        )
    repo = ADVISORIES / pins()["advisoryDirectory"]
    if not (repo / ".git/HEAD").is_file():
        raise RuntimeError("Rust advisory snapshot is missing")
    actual = run(
        ["git", "-C", repo, "rev-parse", "HEAD"], env=environment(), capture=True
    ).strip()
    if actual != stamp["advisory"]["revision"]:
        raise RuntimeError("Rust advisory snapshot revision changed")
    if run(
        ["git", "-C", repo, "status", "--porcelain"], env=environment(), capture=True
    ).strip():
        raise RuntimeError("Rust advisory snapshot contains local changes")


def compiler_installed(version):
    directory = toolchain(version).parent
    required = [p for p in binary_paths() if p.is_relative_to(directory)]
    if not required or not all(p.is_file() for p in required):
        return False
    return run([toolchain(version) / "rustc", "--version"], env=environment(version), capture=True).startswith("rustc " + version + " ")


def install():
    """Acquire compiler/generator prerequisites without installing the analyzer."""
    if sys.platform != "linux" or platform.machine() not in ("x86_64", "AMD64"):
        raise RuntimeError("Rust qualification currently requires Linux AMD64")
    owned_directory(TOOLS)
    (TOOLS / "installed.json").unlink(missing_ok=True)
    config = pins()
    for version in (config["rust"], config["msrv"]):
        if compiler_installed(version):
            continue
        args = ["rustup", "toolchain", "install", version, "--profile", "minimal", "--no-self-update"]
        if version == config["rust"]:
            args += ["--component", "rustfmt,clippy,llvm-tools-preview"]
        run(args, env=environment(version, offline=False))
    generator = acquire_source("generator", config["generator"])
    shutil.copyfile(PROJECT / "generator.lock", generator / "Cargo.lock")
    manifests = {"generator": generator / "Cargo.toml", "sdk": SDK / "Cargo.toml",
                 "consumer": PROJECT / "consumer/Cargo.toml", "report": REPORT / "Cargo.toml"}
    inventories = {}
    for name, manifest in manifests.items():
        cargo(config["rust"], "fetch", "--locked", manifest=manifest, offline=False)
        inventories[name] = dependency_inventory(manifest)
    cargo(config["rust"], "build", "--release", "--frozen", "--bin", "openapi-to-rust",
          manifest=generator / "Cargo.toml", target=TOOLS / "build-tools")
    owned_directory(TOOLS / "bin")
    shutil.copyfile(TOOLS / "build-tools/release/openapi-to-rust", TOOLS / "bin/openapi-to-rust")
    (TOOLS / "bin/openapi-to-rust").chmod(0o755)
    import runner as shared

    go_env = shared.environment()
    go = Path(run(["go", "env", "GOROOT"], env=go_env, capture=True).strip()) / "bin/go"
    for directory in (ROOT, shared.SDK, shared.COMPAT / "go"):
        run([go, "mod", "download"], cwd=directory, env=go_env)
    stamp = {"schema": "rust-sdk-tools/v2", "inputs": inputs(),
             "binaries": {str(p.relative_to(TOOLS)): digest(p) for p in binary_paths()},
             "go": str(go), "goSha256": digest(go), "inventories": inventories,
             "dependencyAudit": "not_run"}
    (TOOLS / "installed.json").write_text(json.dumps(stamp, indent=2, sort_keys=True) + "\n")
    print("Installed Rust compiler/generator tools; dependency audit is separate and has not run", flush=True)


def installed():
    try:
        stamp = json.loads((TOOLS / "installed.json").read_text())
        expected = {str(p.relative_to(TOOLS)): digest(p) for p in binary_paths()}
        if (stamp["schema"] != "rust-sdk-tools/v2" or stamp["inputs"] != inputs()
                or digest(stamp["go"]) != stamp["goSha256"] or stamp["binaries"] != expected
                or stamp["dependencyAudit"] != "not_run"):
            raise ValueError("stale")
    except (OSError, KeyError, TypeError, ValueError) as error:
        raise RuntimeError("Run make sdk-tools-install SDK_LANGUAGE=rust (missing or stale installation)") from error
    return stamp


def audit_inputs():
    return dict(inputs(), **{str(p.relative_to(ROOT)): digest(p)
                            for p in (PROJECT / "deny.lock", PROJECT / "deny.toml")})


def audit_install():
    """Acquire the optional analyzer and advisory database, without claiming a clean scan."""
    installed()
    target = TOOLS / "audit-installed.json"
    target.unlink(missing_ok=True)
    config = pins()
    source = acquire_source("deny-source", config["deny"])
    shutil.copyfile(PROJECT / "deny.lock", source / "Cargo.lock")
    cargo(config["rust"], "fetch", "--locked", manifest=source / "Cargo.toml", offline=False)
    inventory = dependency_inventory(source / "Cargo.toml")
    cargo(config["rust"], "build", "--release", "--frozen", "--bin", "cargo-deny",
          manifest=source / "Cargo.toml", target=TOOLS / "build-tools")
    binary = TOOLS / "bin/cargo-deny"
    shutil.copyfile(TOOLS / "build-tools/release/cargo-deny", binary)
    binary.chmod(0o755)
    if run([binary, "--version"], env=environment(), capture=True).strip() != "cargo-deny " + config["deny"]["version"]:
        raise RuntimeError("Built Rust analyzer version mismatch")
    owned_directory(ADVISORIES)
    repo = ADVISORIES / config["advisoryDirectory"]
    if repo.exists():
        owned_directory(repo)
        shutil.rmtree(repo)
    run(["gh", "repo", "clone", "RustSec/advisory-db", repo, "--", "--depth", "1"])
    advisory = {"revision": run(["git", "-C", repo, "rev-parse", "HEAD"], env=environment(), capture=True).strip(),
                "fetchedAt": datetime.now(timezone.utc).isoformat()}
    (TOOLS / "generator-deny.toml").write_text(tool_policy_text())
    stamp = {"schema": "rust-sdk-audit/v1", "inputs": audit_inputs(),
             "binarySha256": digest(binary), "advisory": advisory, "inventory": inventory,
             "generatorPolicySha256": digest(TOOLS / "generator-deny.toml")}
    verify_freshness(stamp)
    target.write_text(json.dumps(stamp, indent=2, sort_keys=True) + "\n")
    print("Installed optional Rust audit tools; no clean audit is implied", flush=True)


def audit_installed():
    installed()
    try:
        stamp = json.loads((TOOLS / "audit-installed.json").read_text())
        if (stamp["schema"] != "rust-sdk-audit/v1" or stamp["inputs"] != audit_inputs()
                or stamp["binarySha256"] != digest(TOOLS / "bin/cargo-deny")
                or stamp["generatorPolicySha256"] != digest(TOOLS / "generator-deny.toml")
                or stamp["generatorPolicySha256"] != hashlib.sha256(tool_policy_text().encode()).hexdigest()):
            raise ValueError("stale")
        verify_freshness(stamp)
    except (OSError, KeyError, TypeError, ValueError, RuntimeError) as error:
        raise RuntimeError("Run make sdk-rust-audit-tools-install (missing, stale or changed audit tools/data)") from error
    return stamp


def audit():
    parent = ROOT / "output/sdk"
    owned_directory(parent)
    (parent / "rust-audit.json").unlink(missing_ok=True)
    stamp = audit_installed()
    checks = []
    with tempfile.TemporaryDirectory(prefix="rust-audit-", dir=parent) as directory:
        stages = [("sdk", lambda: policy(SDK / "Cargo.toml")),
                  ("consumer", lambda: policy(PROJECT / "consumer/Cargo.toml")),
                  ("report", lambda: policy(REPORT / "Cargo.toml")),
                  ("tools", tool_advisories),
                  ("refusal-controls", lambda: qualify_refusals(Path(directory), stamp))]
        for name, check in stages:
            try:
                check()
                checks.append({"scope": name, "status": "passed"})
            except (OSError, RuntimeError, subprocess.SubprocessError):
                checks.append({"scope": name, "status": "failed"})
    failed = [c["scope"] for c in checks if c["status"] != "passed"]
    report = {"status": "failed" if failed else "passed", "checks": checks, "tools": stamp}
    (parent / "rust-audit.json").write_text(json.dumps(report, indent=2, sort_keys=True) + "\n")
    if failed:
        raise RuntimeError("Rust dependency audit failed: " + ", ".join(failed))
    print("Rust dependency audit and native refusal controls passed", flush=True)


def generate(destination, work):
    destination = Path(destination)
    config = work / "generator.toml"
    config.write_text(
        "[generator]\nspec_path = "
        + json.dumps(str(ROOT / "api/openapi/control-v1.yaml"))
        + "\noutput_dir = "
        + json.dumps(str(destination))
        + '\nmodule_name = "generated"\n'
        'bindings_metadata = "bindings.json"\neffective_spec = "effective.json"\n'
        "[features]\nenable_async_client = true\nenable_sse_client = false\nenable_specta = false\n"
        "[http_client]\ntimeout_seconds = 30\nmax_response_body_bytes = 8388608\n"
        "[http_client.retry]\nmax_retries = 0\n[http_client.tracing]\nenabled = false\n"
    )
    run(
        [TOOLS / "bin/openapi-to-rust", "generate", "--config", config],
        env=environment(),
    )
    generation.repair(destination, ROOT / "api/openapi/control-v1.yaml")
    for source in destination.glob("*.rs"):
        run(
            [toolchain(pins()["rust"]) / "rustfmt", "--edition", "2024", source],
            env=environment(),
        )
    expected = generation.contract_operations(
        (ROOT / "api/openapi/control-v1.yaml").read_text()
    )
    operations = json.loads((destination / "operations.json").read_text())
    if set(expected) != {o["operation_id"] for o in operations} or any(
        expected[o["operation_id"]] != (o["source_method"], o["source_path"])
        for o in operations
    ):
        raise RuntimeError(
            "Rust bindings omit or duplicate authoritative API operation IDs"
        )


def source_files():
    # Never inventory Cargo target caches or caller-created files in the crate.
    return [
        ROOT / "VERSION",
        ROOT / "api/openapi/control-v1.yaml",
        ROOT / "api/examples/ard-manifest.json",
        *sorted((SDK / "src").rglob("*.rs")),
        *sorted((SDK / "examples").rglob("*.rs")),
        *[
            SDK / name
            for name in ("Cargo.toml", "Cargo.lock", "README.md", "LICENSE", "NOTICE")
        ],
        SDK / "src/generated/operations.json",
        HERE / "rust_runner.py",
        HERE / "runner.py",
        HERE / "rust_consumer_functional_test.go.txt",
        HERE / "rust_report_functional_test.go.txt",
        ROOT / "cmd/catalogctl/rust_report_functional_test.go",
        *sorted((REPORT / "src").glob("*.rs")),
        *[REPORT / name for name in ("Cargo.toml", "Cargo.lock", "README.md")],
        PROJECT / "generation.py",
        PINS,
        PROJECT / "deny.toml",
        *sorted(PROJECT.glob("*.lock")),
        *[
            PROJECT / "consumer" / name
            for name in ("Cargo.toml", "Cargo.lock", "src/main.rs")
        ],
    ]


def verify_generator_notice(notice, generator):
    expected = (
        "Generated bindings use openapi-to-rust " + generator["version"] + " under the MIT License.",
        "Source: https://github.com/" + generator["repository"] + "/tree/" + generator["revision"],
    )
    actual = tuple(
        line for line in notice.splitlines()
        if line.startswith(("Generated bindings use openapi-to-rust ", "Source: "))
    )
    if actual != expected:
        raise RuntimeError("Rust SDK generator notice version/source drift")


def build_package(work):
    verify_generator_notice((SDK / "NOTICE").read_text(), pins()["generator"])
    version = (ROOT / "VERSION").read_text().strip()
    crate = tomllib.loads((SDK / "Cargo.toml").read_text())
    consumer = tomllib.loads((PROJECT / "consumer/Cargo.toml").read_text())
    locks = [
        tomllib.loads(p.read_text())
        for p in (SDK / "Cargo.lock", PROJECT / "consumer/Cargo.lock")
    ]
    if (
        crate["package"]["version"] != version
        or consumer["dependencies"]["open-agentic-catalog"]["version"] != version
        or any(
            next(
                p["version"]
                for p in lock["package"]
                if p["name"] == "open-agentic-catalog"
            )
            != version
            for lock in locks
        )
    ):
        raise RuntimeError("Rust package version drift; run make version-check")
    target = work / "package-build"
    cargo(pins()["rust"], "package", "--frozen", "--allow-dirty", target=target)
    archives = list((target / "package").glob("*.crate"))
    if len(archives) != 1:
        raise RuntimeError("Rust package inventory changed")
    import runner as shared

    shared.check_archive_notices(archives[0], SDK, f"open-agentic-catalog-{version}/")
    return archives[0]


def package():
    stamp = installed()
    import runner as shared

    parent = ROOT / "output/sdk"
    owned_directory(parent)
    with tempfile.TemporaryDirectory(prefix="rust-package-", dir=parent) as directory:
        artifact = build_package(Path(directory))
        return shared.retain("rust", [artifact], stamp, source_files=source_files())


def check(action):
    stamp = installed()
    parent = ROOT / "output/sdk"
    owned_directory(parent)
    work = Path(tempfile.mkdtemp(prefix="rust-", dir=parent))
    watched = {str(p): digest(p) for p in source_files()}
    try:
        generate(work / "generated", work)
        if action == "generate":
            shutil.copytree(
                work / "generated", SDK / "src/generated", dirs_exist_ok=True
            )
        else:
            generate(work / "repeat", work)
            for source in (work / "generated").iterdir():
                if digest(source) != digest(work / "repeat" / source.name) or digest(
                    source
                ) != digest(SDK / "src/generated" / source.name):
                    raise RuntimeError("Rust SDK generation drift or nondeterminism")
            cargo(pins()["rust"], "fmt", "--check")
            cargo(
                pins()["rust"],
                "fmt",
                "--check",
                manifest=PROJECT / "consumer/Cargo.toml",
            )
            cargo(pins()["rust"], "fmt", "--check", manifest=REPORT / "Cargo.toml")
            cargo(pins()["rust"], "clippy", "--frozen", "--all-targets", "--", "-D", "warnings", manifest=REPORT / "Cargo.toml")
            cargo(
                pins()["rust"],
                "clippy",
                "--frozen",
                "--all-targets",
                "--",
                "-D",
                "warnings",
            )
            cargo(
                pins()["rust"],
                "clippy",
                "--frozen",
                "--all-targets",
                "--",
                "-D",
                "warnings",
                manifest=PROJECT / "consumer/Cargo.toml",
            )
            for version in (pins()["rust"], pins()["msrv"]):
                cargo(version, "test", "--frozen", "--lib")
                cargo(version, "test", "--frozen", "--doc")
                cargo(version, "check", "--frozen", "--examples")
                cargo(version, "test", "--frozen", manifest=REPORT / "Cargo.toml")
                cargo(version, "build", "--frozen", manifest=REPORT / "Cargo.toml")
            cargo(
                pins()["rust"], "doc", "--frozen", "--no-deps", target=work / "rustdoc"
            )
            artifact = build_package(work)
            qualify_package(work, artifact, stamp)
            qualify_report_curated(stamp)
            measure_coverage(work, stamp)
            if watched != {str(p): digest(p) for p in source_files()}:
                raise RuntimeError("Rust SDK check changed source inputs")
            import runner as shared

            shared.retain("rust", [artifact], stamp, source_files=source_files())
        if inputs() != stamp["inputs"]:
            raise RuntimeError("Rust SDK input drift")
    except BaseException:
        print("Failed Rust SDK output retained: " + str(work), file=sys.stderr)
        raise
    shutil.rmtree(work)
    print("Rust SDK " + action + " passed; dependency audit not run (use make sdk-rust-audit); owned staging output cleaned", flush=True)


def qualify_refusals(work, stamp):
    """Calibrate the installed scanner, not a mock of its return value."""
    fixture = work / "license-control"
    (fixture / "src").mkdir(parents=True)
    (fixture / "src/lib.rs").write_text("// Local license-policy control.\n")
    (fixture / "Cargo.toml").write_text(
        '[package]\nname="catalog-license-control"\nversion="0.0.0"\nedition="2024"\nlicense="MPL-2.0"\npublish=false\n'
    )
    cargo(
        pins()["rust"],
        "generate-lockfile",
        "--offline",
        manifest=fixture / "Cargo.toml",
    )
    result = subprocess.run(
        [
            str(TOOLS / "bin/cargo-deny"),
            "--manifest-path",
            str(fixture / "Cargo.toml"),
            "--config",
            str(PROJECT / "deny.toml"),
            "--frozen",
            "check",
            "licenses",
        ],
        env=environment(),
        capture_output=True,
        text=True,
        timeout=60,
        check=False,
    )
    if (
        result.returncode == 0
        or "license" not in result.stderr.lower()
        or "MPL-2.0" not in result.stderr
    ):
        raise RuntimeError(
            "Unpublished unapproved-license control did not fail as expected"
        )
    missing_env = environment()
    missing_env["CATALOG_RUST_ADVISORY_DIR"] = str(work / "missing-advisory-db")
    missing = subprocess.run(
        [
            str(TOOLS / "bin/cargo-deny"),
            "--manifest-path",
            str(SDK / "Cargo.toml"),
            "--config",
            str(PROJECT / "deny.toml"),
            "--frozen",
            "check",
            "advisories",
        ],
        env=missing_env,
        capture_output=True,
        text=True,
        timeout=60,
        check=False,
    )
    if missing.returncode == 0 or not any(
        word in missing.stderr.lower() for word in ("advisory", "database")
    ):
        raise RuntimeError("Missing advisory database control did not fail as expected")
    stale = dict(
        stamp, advisory=dict(stamp["advisory"], fetchedAt="2000-01-01T00:00:00+00:00")
    )
    try:
        verify_freshness(stale)
    except RuntimeError as error:
        if "stale" not in str(error):
            raise
    else:
        raise RuntimeError("Stale advisory control did not fail")
    print(
        "Rust unpublished-license and missing/stale-advisory refusal controls passed",
        flush=True,
    )


def qualify_package(work, artifact, stamp):
    with tempfile.TemporaryDirectory(prefix="catalog-sdk-rust-consumer-") as directory:
        external = Path(directory)
        extract(artifact, external / "archive")
        roots = list((external / "archive").iterdir())
        if len(roots) != 1:
            raise RuntimeError("Unexpected Rust package roots")
        package = roots[0]
        metadata = tomllib.loads((package / "Cargo.toml").read_text())
        if (
            metadata["package"]["name"] != "open-agentic-catalog"
            or metadata["package"]["publish"] is not False
        ):
            raise RuntimeError("Rust package metadata mismatch")
        if any(
            "path" in dependency or "git" in dependency
            for dependency in metadata["dependencies"].values()
            if isinstance(dependency, dict)
        ):
            raise RuntimeError("Rust package has a repository or Git dependency")
        for name in (
            "src/lib.rs",
            "src/transport.rs",
            "src/generated/client.rs",
            "src/generated/types.rs",
            "LICENSE",
            "NOTICE",
            "README.md",
        ):
            if digest(package / name) != digest(SDK / name):
                raise RuntimeError("Rust package source/notice mismatch")
        consumer = external / "consumer"
        shutil.copytree(
            PROJECT / "consumer",
            consumer,
            ignore=shutil.ignore_patterns(
                "target", ".cargo", "*.profraw", "*.profdata"
            ),
        )
        manifest = consumer / "Cargo.toml"
        old = 'path = "../../../../sdk/rust"'
        if manifest.read_text().count(old) != 1:
            raise RuntimeError("Rust consumer fixture path changed")
        manifest.write_text(
            manifest.read_text().replace(old, "path = " + json.dumps(str(package)))
        )
        for version in (pins()["rust"], pins()["msrv"]):
            target = TOOLS / ("consumer-" + version)
            cargo(version, "build", "--frozen", manifest=manifest, target=target)
            # The extracted archive has normalized source timestamps. Keep
            # example executables invocation-owned instead of accepting a
            # discover binary left in the persistent consumer target cache.
            example_target = work / ("examples-" + version)
            cargo(
                version,
                "build",
                "--frozen",
                "--examples",
                manifest=package / "Cargo.toml",
                target=example_target,
            )
            qualify_catalog(
                work / ("fixture-" + version),
                Path(stamp["go"]),
                target / "debug/catalog-rust-consumer",
                extra_env={
                    "CATALOG_SDK_RUST_EXAMPLE": str(example_target / "debug/examples/discover"),
                    "CATALOG_RUST_REPORT": str(TOOLS / ("build-" + version) / "debug/catalog-rust-report"),
                },
            )


def qualify_catalog(destination, go, binary, *, extra_env=None):
    import runner as shared

    env = shared.environment(offline=True)
    env.update(extra_env or {})
    env["CATALOG_SDK_RUST"] = str(binary)
    shutil.copytree(
        shared.COMPAT / "go", destination, ignore=shutil.ignore_patterns("generated")
    )
    mod = destination / "go.mod"
    old = "replace " + shared.MODULE + " => ../../.."
    if mod.read_text().count(old) != 1:
        raise RuntimeError("Rust Catalog fixture replacement changed")
    mod.write_text(
        mod.read_text().replace(
            old, "replace " + shared.MODULE + " => " + json.dumps(str(ROOT))
        )
    )
    for source in destination.glob("*.go"):
        source.write_text(
            source.read_text().replace(
                shared.MODULE + "/test/clientcompat/go/generated", shared.SDK_MODULE
            )
        )
    shutil.copyfile(
        HERE / "rust_consumer_functional_test.go.txt",
        destination / "rust_functional_test.go",
    )
    if env.get("CATALOG_RUST_REPORT"):
        shutil.copyfile(HERE / "rust_report_functional_test.go.txt", destination / "rust_report_functional_test.go")
    run(
        [
            go,
            "mod",
            "edit",
            "-require=" + shared.SDK_MODULE + "@v0.0.0",
            "-replace=" + shared.SDK_MODULE + "=" + str(shared.SDK),
        ],
        cwd=destination,
        env=env,
    )
    selection = (
        "^TestRustSDK"
        if env.get("CATALOG_SDK_RUST_EXAMPLE")
        else "^TestRustSDK(CatalogWorkflow|Transport|Report.*)$"
        if env.get("CATALOG_RUST_REPORT")
        else "^TestRustSDK(CatalogWorkflow|Transport)$"
    )
    run(
        [
            go,
            "test",
            "-tags=functional",
            "-race",
            "-run=" + selection,
            "-count=1",
            "-timeout=180s" if env.get("CATALOG_RUST_REPORT") else "-timeout=90s",
            "-v",
            "./...",
        ],
        cwd=destination,
        env=env,
    )


def qualify_report_curated(stamp):
    import runner as shared

    env = shared.environment(offline=True)
    env["CATALOG_RUST_REPORT"] = str(TOOLS / ("build-" + pins()["rust"]) / "debug/catalog-rust-report")
    run([stamp["go"], "test", "-tags=functional", "-race", "./cmd/catalogctl",
         "-run=^TestRustReportCurated$", "-count=1", "-timeout=120s", "-v"], env=env)


def measure_coverage(work, stamp):
    """Use the pinned compiler's native tools; merge actual unit/HTTP profiles."""
    target = TOOLS / "build-coverage"
    unit = work / "coverage/unit"
    functional = work / "coverage/functional"
    build_profiles = work / "coverage/build"
    for directory in (unit, functional, build_profiles):
        directory.mkdir(parents=True)
    profile_roots = (SDK, PROJECT / "consumer")
    before_profiles = {p for root in profile_roots for p in root.glob("*.profraw")}
    flags = {
        "RUSTFLAGS": "-C instrument-coverage",
        "LLVM_PROFILE_FILE": str(build_profiles / "%p-%m.profraw"),
    }

    output = cargo(
        pins()["rust"],
        "test",
        "--frozen",
        "--lib",
        "--no-run",
        "--message-format=json",
        target=target,
        capture=True,
        extra_env=flags,
    )
    artifacts = [
        json.loads(line) for line in output.splitlines() if line.startswith("{")
    ]
    binaries = [
        Path(a["executable"])
        for a in artifacts
        if a.get("executable") and a.get("profile", {}).get("test")
    ]
    if len(binaries) != 1:
        raise RuntimeError("Rust unit coverage executable is missing or ambiguous")
    env = environment(target=target)
    env["LLVM_PROFILE_FILE"] = str(unit / "%p-%m.profraw")
    result = run([binaries[0]], env=env, capture=True)
    print(result, flush=True)
    match = re.search(
        r"test result: ok\. (\d+) passed; (\d+) failed; (\d+) ignored; (\d+) measured; (\d+) filtered out",
        result,
    )
    if not match or int(match[1]) == 0 or any(int(match[i]) for i in range(2, 6)):
        raise RuntimeError("Rust unit coverage selection is incomplete")
    llvm = toolchain(pins()["rust"]).parent / "lib/rustlib" / pins()["host"] / "bin"
    sources = sorted(p for p in (SDK / "src").glob("*.rs") if p.name != "unit.rs")

    def export(label, profiles, objects):
        if not profiles or any(p.stat().st_size == 0 for p in profiles):
            raise RuntimeError("Missing Rust " + label + " coverage contribution")
        profile = work / (label + ".profdata")
        run(
            [llvm / "llvm-profdata", "merge", "-sparse", "-o", profile, *profiles],
            env=env,
        )
        data = json.loads(
            run(
                [
                    llvm / "llvm-cov",
                    "export",
                    "--instr-profile=" + str(profile),
                    objects[0],
                    *["--object=" + str(p) for p in objects[1:]],
                    *sources,
                ],
                env=env,
                capture=True,
            )
        )
        files = data["data"][0]["files"]
        totals = {"count": 0, "covered": 0}
        for file in files:
            if Path(file["filename"]).resolve() not in sources:
                raise RuntimeError("Rust coverage includes an unselected source")
            for key in totals:
                totals[key] += file["summary"]["lines"][key]
        if totals["count"] == 0:
            raise RuntimeError("Rust handwritten coverage is empty")
        totals["percent"] = 100 * totals["covered"] / totals["count"]
        print(
            f"Rust {label} handwritten line coverage: {totals['covered']}/{totals['count']} ({totals['percent']:.2f}%)",
            flush=True,
        )
        return totals

    unit_profiles = sorted(unit.glob("*.profraw"))
    units = export("unit", unit_profiles, binaries)
    cargo(
        pins()["rust"],
        "build",
        "--frozen",
        manifest=PROJECT / "consumer/Cargo.toml",
        target=target,
        extra_env=flags,
    )
    consumer = target / "debug/catalog-rust-consumer"
    qualify_catalog(
        work / "coverage-fixture",
        Path(stamp["go"]),
        consumer,
        extra_env={"LLVM_PROFILE_FILE": str(functional / "%p-%m.profraw")},
    )
    functional_profiles = sorted(functional.glob("*.profraw"))
    if len(functional_profiles) < 2:
        raise RuntimeError("Rust functional coverage contributions are incomplete")
    combined = export(
        "unit-plus-local-functional",
        unit_profiles + functional_profiles,
        binaries + [consumer],
    )
    report = {
        "unit": units,
        "unitPlusLocalFunctional": combined,
        "source": {str(p.relative_to(ROOT)): digest(p) for p in sources},
        "rust": pins()["rust"],
        "llvmCovSha256": digest(llvm / "llvm-cov"),
        "llvmProfdataSha256": digest(llvm / "llvm-profdata"),
    }
    if {p for root in profile_roots for p in root.glob("*.profraw")} != before_profiles:
        raise RuntimeError(
            "Rust coverage leaked build profiles into SDK or consumer source"
        )
    (ROOT / "output/sdk/rust-coverage.json").write_text(
        json.dumps(report, indent=2, sort_keys=True) + "\n"
    )
