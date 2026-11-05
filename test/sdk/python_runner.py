"""Independent Python SDK tooling. Installation alone acquires locked packages."""
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import tarfile
import tomllib
import zipfile
import urllib.request

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
PROJECT = HERE / "python"
SDK = ROOT / "sdk/python"
TOOLS = HERE / ".tools/python"
PYTHON = TOOLS / "bin/python"
SERVICE_READ = ROOT / "examples/tutorials/local-oidc/service-read.py"


def run(args, *, cwd=ROOT, env=None, capture=False):
    return subprocess.run([str(x) for x in args], cwd=cwd, env=env, check=True,
                          text=True, capture_output=capture, timeout=600).stdout


def identity():
    paths = [PROJECT / "pyproject.toml", PROJECT / "uv.lock", SDK / "pyproject.toml"]
    for directory in (ROOT, ROOT / "sdk/go", ROOT / "test/clientcompat/go"):
        paths.extend(directory / name for name in ("go.mod", "go.sum"))
    return {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in paths}


def environment():
    env = dict(os.environ)
    for key in list(env):
        if key.startswith(("UV_", "PYTHON")) or key in ("VIRTUAL_ENV", "JAVA_TOOL_OPTIONS", "JDK_JAVA_OPTIONS", "_JAVA_OPTIONS"):
            env.pop(key)
    env.update(PYTHONPATH=str(SDK / "src"), PYTHONDONTWRITEBYTECODE="1", UV_OFFLINE="1")
    return env


def install():
    uv = os.environ.get("UV", "uv")
    if run([uv, "--version"], capture=True).split()[:2] != ["uv", "0.13.4"]:
        raise RuntimeError("uv 0.13.4 required; set UV to the installed pinned executable")
    stamp = TOOLS / "installed.json"
    stamp.unlink(missing_ok=True)
    env = environment()
    env.pop("UV_OFFLINE")
    env["UV_PROJECT_ENVIRONMENT"] = str(TOOLS)
    # No editable install: qualification imports source explicitly, consumers use wheels.
    cutoff = tomllib.loads((PROJECT / "pyproject.toml").read_text())["tool"]["uv"]["exclude-newer"]
    run([uv, "--no-config", "sync", "--no-python-downloads", "--exclude-newer", cutoff,
         "--project", PROJECT, "--locked", "--python", sys.executable], env=env)
    # Record a complete installation; ordinary checks use this qualified uv path.
    qualified_uv = Path(shutil.which(uv) or uv).resolve()
    metadata = json.loads(run([PYTHON, "-I", PROJECT / "inventory.py"], env=env, capture=True))
    wheelhouse = TOOLS / "wheels"
    wheelhouse.mkdir(exist_ok=True)
    wheels = json.loads(run([PYTHON, "-I", PROJECT / "inventory.py", PROJECT / "uv.lock"], env=env, capture=True))
    for wheel in wheels:
        target = wheelhouse / wheel["url"].rsplit("/", 1)[-1]
        expected = wheel["hash"].removeprefix("sha256:")
        if not target.is_file() or hashlib.sha256(target.read_bytes()).hexdigest() != expected:
            with urllib.request.urlopen(wheel["url"], timeout=30) as response:
                data = response.read(64 * 1024**2 + 1)
            if len(data) > 64 * 1024**2 or hashlib.sha256(data).hexdigest() != expected:
                raise RuntimeError("Locked runtime wheel digest/size mismatch")
            target.write_bytes(data)
    import runner as go_runner
    go_env = go_runner.environment()
    go = Path(run(["go", "env", "GOROOT"], env=go_env, capture=True).strip()) / "bin/go"
    for directory in (ROOT, go_runner.SDK, go_runner.COMPAT / "go"):
        run([go, "mod", "download"], cwd=directory, env=go_env)
    stamp.write_text(json.dumps({"inputs": identity(), "uv": str(qualified_uv),
                                "uv_hash": hashlib.sha256(qualified_uv.read_bytes()).hexdigest(),
                                "go": str(go), "go_hash": hashlib.sha256(go.read_bytes()).hexdigest(),
                                "generator_hash": hashlib.sha256(generator_jar().read_bytes()).hexdigest(),
                                "inventory": metadata}, sort_keys=True) + "\n")


def installed():
    try:
        stamp = json.loads((TOOLS / "installed.json").read_text())
        if (stamp["inputs"] != identity()
                or hashlib.sha256(Path(stamp["uv"]).read_bytes()).hexdigest() != stamp["uv_hash"]
                or hashlib.sha256(Path(stamp["go"]).read_bytes()).hexdigest() != stamp["go_hash"]
                or hashlib.sha256(generator_jar().read_bytes()).hexdigest() != stamp["generator_hash"]):
            raise ValueError()
    except (OSError, ValueError, KeyError, TypeError):
        raise RuntimeError("Run make sdk-tools-install SDK_LANGUAGE=python with pinned UV") from None
    if not PYTHON.is_file():
        raise RuntimeError("Python SDK environment missing")
    actual = json.loads(run([PYTHON, "-I", PROJECT / "inventory.py"], env=environment(), capture=True))
    if actual != stamp["inventory"]:
        raise RuntimeError("Python SDK installation drift; reinstall")
    wheels = json.loads(run([PYTHON, "-I", PROJECT / "inventory.py", PROJECT / "uv.lock"], env=environment(), capture=True))
    for wheel in wheels:
        path = TOOLS / "wheels" / wheel["url"].rsplit("/", 1)[-1]
        if not path.is_file() or hashlib.sha256(path.read_bytes()).hexdigest() != wheel["hash"].removeprefix("sha256:"):
            raise RuntimeError("Missing or changed locked runtime wheel; reinstall")
    return stamp


def generator_jar():
    matches = list(TOOLS.glob("lib/python*/site-packages/openapi_generator_cli/openapi-generator.jar"))
    if len(matches) != 1:
        raise RuntimeError("Python SDK generator missing or ambiguous; reinstall")
    return matches[0]


def generate(destination, work):
    jar = generator_jar()
    templates = work / "templates"
    shutil.copytree(PROJECT / "templates", templates)
    with zipfile.ZipFile(jar) as archive:
        source = archive.read("python/api.mustache").decode()
        if "@validate_call" not in source:
            raise RuntimeError("Unsupported API diagnostic template")
        source = source.replace("@validate_call", "@validate_call(config={'hide_input_in_errors': True})")
        (templates / "api.mustache").write_text(source)
        partial = archive.read("python/partial_api.mustache").decode()
        partial = partial.replace("timeout setting for this request. If one\n                                 number provided, it will be total request\n                                 timeout. It can also be a pair (tuple) of\n                                 (connection, read) timeouts.", "Optional whole-call deadline in seconds, capped by Config.timeout.")
        partial = partial.replace("int, tuple(int, int), optional", "float, optional")
        partial = partial.replace("set to override the auth_settings for an a single\n                              request; this effectively ignores the\n                              authentication in the spec for a single request.", "Ignored by the managed credential boundary; configure Client explicitly.")
        partial = partial.replace("set to override the headers for a single\n                         request; this effectively ignores the headers\n                         in the spec for a single request.", "Additional headers. Managed credentials, origin and encoding take precedence.")
        (templates / "partial_api.mustache").write_text(partial)
        # The generator cannot represent this required/not conditional schema.
        # Keep its exact JSON object instead of inventing union types or fields.
        original = archive.read("python/model_oneof.mustache").decode().replace("from __future__ import annotations", "")
        fallback = """from __future__ import annotations
if "{{classname}}" == "TrustAssessmentRequest":
    from catalog_sdk.conditional import ConditionalObject as TrustAssessmentRequest
elif "{{classname}}" == "CatalogSearchResult":
    from catalog_sdk.conditional import ConditionalObject as CatalogSearchResult
elif "{{classname}}" == "AuditDetailsV1":
    from catalog_sdk.conditional import ConditionalObject as AuditDetailsV1
else:
"""
        (templates / "model_oneof.mustache").write_text(fallback + "\n".join("    " + line for line in original.splitlines()) + "\n")
        original = archive.read("python/model_generic.mustache").decode().replace("from __future__ import annotations", "")
        # This generator leaves allVars empty on several composed models while
        # vars correctly defines their fields. These models have no inheritance.
        original = original.replace("{{#allVars}}", "{{#vars}}").replace("{{/allVars}}", "{{/vars}}")
        fallback = """from __future__ import annotations
if "{{classname}}" == "RepresentationInput":
    from catalog_sdk.conditional import ConditionalObject as RepresentationInput
else:
"""
        (templates / "model_generic.mustache").write_text(fallback + "\n".join("    " + line for line in original.splitlines()) + "\n")
    result = run([TOOLS / "bin/openapi-generator-cli", "generate", "-g", "python", "-i",
                  ROOT / "api/openapi/control-v1.yaml", "-c", PROJECT / "generate.json",
                  "--additional-properties", "packageVersion=" + package_version(),
                  "--global-property", "apiDocs=false,modelDocs=false,apiTests=false,modelTests=false",
                  "-t", templates, "-o", destination], env=environment(), capture=True)
    # The generator reports some closed composed schemas as open. Restrict the
    # repair to actual open objects, including ARDEntry's implicit openness.
    # Use Pydantic's extra storage so a literal additional_properties key cannot
    # become the generator's synthetic bucket and change the wire document.
    for name in ("create_listing_revision_extensions", "listing_revision_extensions",
                 "ard_search_result", "ard_search_result_trust_manifest",
                 "ard_search_response_referrals_inner", "ard_entry"):
        path = destination / "catalog_sdk/generated/models" / (name + ".py")
        source = path.read_text()
        config_marker = "        model_config = ConfigDict(\n"
        dump_marker = "                exclude_none=True,\n            )"
        field_marker = "        additional_properties: Dict[str, Any] = {}\n"
        excluded_marker = '                "additional_properties",\n'
        store_marker = "            # store additional fields in additional_properties\n"
        markers = {config_marker:1, dump_marker:1, field_marker:1, excluded_marker:1,
                   store_marker:1, "self.additional_properties":3, "_obj.additional_properties":1}
        if any(source.count(marker) != count for marker, count in markers.items()):
            raise RuntimeError("Unsupported Python open-object template: " + name)
        source = source.replace(config_marker, config_marker + '            extra="allow",\n')
        source = source.replace(field_marker, "").replace(excluded_marker, "")
        source = source.replace("self.additional_properties", "self.model_extra")
        source = source.replace("_obj.additional_properties", "_obj.model_extra")
        source = source.replace("# puts key-value pairs in additional_properties in the top level",
                                "# Preserve additional JSON values, including nulls.")
        # The generated from_dict supplies None for absent nullable fields.
        # Retain actual presence so an absent @context is not emitted as null.
        source = source.replace(store_marker, """            _obj.model_fields_set.intersection_update(
                name for name, field in cls.model_fields.items()
                if (field.alias or name) in obj
            )
            # Store additional values, including nulls, in Pydantic's extra map.
""")
        path.write_text(source)
    run([TOOLS / "bin/ruff", "format", "--config", ROOT / "ruff.toml",
         destination / "catalog_sdk/generated"], env=environment(), capture=True)
    (work / "generation.log").write_text(result)


def tree(path):
    return {str(p.relative_to(path)): hashlib.sha256(p.read_bytes()).hexdigest()
            for p in path.rglob("*.py")}


def package_version():
    version = (ROOT / "VERSION").read_text().strip().replace("-rc.", "rc")
    if tomllib.loads((SDK / "pyproject.toml").read_text())["project"]["version"] != version:
        raise RuntimeError("Python package version differs from VERSION; run version-check")
    return version


def build_packages(work, env):
    version = package_version()
    for kind in ("wheel", "sdist"):
        run([PYTHON, "-m", "build", "--" + kind, "--no-isolation", "--outdir", work / "dist", SDK], env=env)
    files = sorted((work / "dist").iterdir())
    import runner as shared
    for archive in files:
        prefix = f"open_agentic_catalog-{version}.dist-info/licenses/" if archive.suffix == ".whl" else f"open_agentic_catalog-{version}/"
        shared.check_archive_notices(archive, SDK, prefix)
    return files


def package_sources():
    return [ROOT / "VERSION", ROOT / "api/openapi/control-v1.yaml", *sorted((SDK / "src").rglob("*.py")),
            SDK / "src/catalog_sdk/py.typed", *(SDK / name for name in ("pyproject.toml", "README.md", "LICENSE", "NOTICE"))]


def package():
    stamp = installed()
    parent = ROOT / "output/sdk"
    parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="python-package-", dir=parent) as directory:
        work = Path(directory)
        original = identity()
        files = build_packages(work, environment())
        if identity() != original:
            raise RuntimeError("Python packaging changed locked inputs")
        import runner as shared
        return shared.retain("python", files, stamp, source_files=package_sources())


def check(action):
    stamp = installed()
    package_version()
    before = identity()
    watched = [ROOT / "api/openapi/control-v1.yaml", ROOT / "VERSION", *SDK.rglob("*.py"), *PROJECT.rglob("*.py"), *PROJECT.rglob("*.mustache"), PROJECT / "generate.json", Path(__file__), HERE / "python_consumer_functional_test.go.txt", HERE / "python_service_read_functional_test.go.txt", SERVICE_READ]
    original = {str(p):hashlib.sha256(p.read_bytes()).hexdigest() for p in watched}
    parent = ROOT / "output/sdk"
    parent.mkdir(parents=True, exist_ok=True)
    work = Path(tempfile.mkdtemp(prefix="python-", dir=parent))
    print("Python SDK output: " + str(work), flush=True)
    try:
        generate(work / "first", work / "first-work")
        generated = work / "first/catalog_sdk/generated"
        if action == "generate":
            target = SDK / "src/catalog_sdk/generated"
            if target.exists():
                shutil.rmtree(target)
            shutil.copytree(generated, target)
        else:
            generate(work / "second", work / "second-work")
            if tree(generated) != tree(work / "second/catalog_sdk/generated"):
                raise RuntimeError("Non-deterministic Python generation")
            if tree(generated) != tree(SDK / "src/catalog_sdk/generated"):
                raise RuntimeError("Python generation drift; run sdk-generate SDK_LANGUAGE=python")
            env = environment()
            tests = PROJECT / "tests"
            run([PYTHON, "-B", "-m", "coverage", "run", "--data-file=" + str(work / "unit.coverage"),
                 "--source=catalog_sdk", "--omit=*/generated/*", "-m", "unittest", "discover", "-s", tests, "-p", "test_unit_*.py"], env=env)
            print("Isolated unit handwritten Python SDK coverage:", flush=True)
            run([PYTHON, "-m", "coverage", "report", "--data-file=" + str(work / "unit.coverage"), "--show-missing"], env=env)
            run([TOOLS / "bin/ruff", "check", "--config", ROOT / "ruff.toml", SDK, PROJECT, HERE / "python_runner.py", SERVICE_READ], env=env)
            run([PYTHON, "-m", "compileall", "-q", str(SDK / "src")], env=dict(env, PYTHONPYCACHEPREFIX=str(work / "bytecode")))
            maintained = sorted((SDK / "src/catalog_sdk").glob("*.py"))
            run([TOOLS / "bin/mypy", "--follow-imports=silent", "--ignore-missing-imports", *maintained], env=env)
            shutil.copyfile(work / "unit.coverage", work / "local.coverage")
            run([PYTHON, "-m", "coverage", "run", "--append", "--data-file=" + str(work / "local.coverage"),
                 "--source=catalog_sdk", "--omit=*/generated/*", "-m", "unittest", "discover", "-s", tests, "-p", "test_functional_*.py"], env=env)
            print("Unit plus local HTTP/TLS handwritten Python SDK coverage:", flush=True)
            run([PYTHON, "-m", "coverage", "report", "--data-file=" + str(work / "local.coverage"), "--show-missing"], env=env)
            files = build_packages(work, env)
            with tempfile.TemporaryDirectory(prefix="catalog-sdk-python-consumers-") as directory:
                consumers = Path(directory)
                qualify_package(work, env, consumers)
                qualify_catalog(work, Path(stamp["go"]), consumers / "wheel", "catalog")
                qualify_catalog(work, Path(stamp["go"]), consumers / "sdist", "catalog-sdist")
        if action != "generate" and original != {str(p):hashlib.sha256(p.read_bytes()).hexdigest() for p in watched}:
            raise RuntimeError("Python SDK check changed source inputs")
        if before != identity():
            raise RuntimeError("Python SDK input drift")
        if action != "generate":
            import runner as shared
            shared.retain("python", files, dict(stamp, rebuiltWheelSha256=hashlib.sha256(next((work / "rebuilt").glob("*.whl")).read_bytes()).hexdigest()), source_files=package_sources())
    except BaseException:
        print("Failed Python SDK output retained: " + str(work), file=sys.stderr)
        raise
    shutil.rmtree(work)
    print("Python SDK " + action + " passed; owned output cleaned", flush=True)



def qualify_catalog(work, go, runtime=None, label="catalog"):
    import runner as go_runner
    selected = {key:value for key,value in go_runner.environment(offline=True).items()
                if not key.startswith("PYTHON") and key != "VIRTUAL_ENV"}
    selected.update(PYTHONDONTWRITEBYTECODE="1", CATALOG_SDK_PYTHON=str((runtime or work / "consumer") / "bin/python"),
                    CATALOG_SDK_WORKFLOW=str(PROJECT / "catalog_workflow.py"), CATALOG_SDK_SERVICE_READ=str(SERVICE_READ))
    consumer = work / label
    shutil.copytree(go_runner.COMPAT / "go", consumer, ignore=shutil.ignore_patterns("generated"))
    mod = consumer / "go.mod"
    old = "replace " + go_runner.MODULE + " => ../../.."
    if mod.read_text().count(old) != 1:
        raise RuntimeError("Catalog fixture replacement changed")
    mod.write_text(mod.read_text().replace(old, "replace " + go_runner.MODULE + " => " + json.dumps(str(ROOT))))
    for source in consumer.glob("*.go"):
        source.write_text(source.read_text().replace(go_runner.MODULE + "/test/clientcompat/go/generated", go_runner.SDK_MODULE))
    shutil.copyfile(HERE / "python_consumer_functional_test.go.txt", consumer / "python_functional_test.go")
    shutil.copyfile(HERE / "python_service_read_functional_test.go.txt", consumer / "python_service_read_functional_test.go")
    run([go, "mod", "edit", "-require=" + go_runner.SDK_MODULE + "@v0.0.0", "-replace=" + go_runner.SDK_MODULE + "=" + str(go_runner.SDK)], cwd=consumer, env=selected)
    run([go, "test", "-tags=functional", "-race", "-run=^(TestPythonSDKCatalogWorkflow|TestPythonSDKServiceRead)$", "-count=1", "-timeout=75s", "-v", "./..."], cwd=consumer, env=selected)


def qualify_package(work, env, consumers):
    # Rebuild independently from the sdist, using only the installed build backend.
    source = work / "sdist"
    source.mkdir()
    with tarfile.open(next((work / "dist").glob("*.tar.gz"))) as archive:
        if any(member.issym() or member.islnk() for member in archive.getmembers()):
            raise RuntimeError("SDK sdist contains links")
        archive.extractall(source, filter="data")
    roots = list(source.iterdir())
    if len(roots) != 1 or not (roots[0] / "pyproject.toml").is_file():
        raise RuntimeError("SDK sdist is incomplete")
    run([PYTHON, "-m", "build", "--wheel", "--no-isolation", "--outdir", work / "rebuilt", roots[0]], env=env)
    wheel = next((work / "dist").glob("*.whl"))
    rebuilt = next((work / "rebuilt").glob("*.whl"))
    def contents(path):
        with zipfile.ZipFile(path) as archive:
            return {name: archive.read(name) for name in archive.namelist()}
    if contents(wheel) != contents(rebuilt):
        raise RuntimeError("Direct and sdist-rebuilt wheel contents differ")
    stamp = installed()
    uv = stamp["uv"]
    constraints = work / "runtime-constraints.txt"
    constraints.write_text("\n".join(name + "==" + version for name, version in stamp["inventory"]["runtime"].items() if name != "open-agentic-catalog") + "\n")
    clean = dict(env)
    clean.pop("PYTHONPATH", None)
    expected = work / "runtime.json"
    expected.write_text(json.dumps(stamp["inventory"]["runtime"], sort_keys=True))
    for label, artifact in (("wheel", wheel), ("sdist", rebuilt)):
        consumer = consumers / label
        run([sys.executable, "-m", "venv", "--without-pip", consumer], env=clean)
        run([uv, "--no-config", "pip", "install", "--offline", "--no-index", "--find-links", TOOLS / "wheels", "--python", consumer / "bin/python", "--constraint", constraints, artifact], env=clean)
        run([consumer / "bin/python", "-I", PROJECT / "consumer.py", expected], cwd=consumers, env=clean)
