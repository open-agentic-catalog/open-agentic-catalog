"""Explicit Schemathesis acquisition and bounded packaged-profile execution."""

import hashlib
import importlib.metadata
import json
import os
from pathlib import Path
import re
import sys
import xml.etree.ElementTree as ET

from namespace_sequence import KINDS
from runner import Failure, command, read_document, write_document

ROOT = Path(__file__).resolve().parents[2]
TOOLS = ROOT / "test/apigenerated"
PYTHON = TOOLS / ".tools/bin/python"
OPERATIONS = ("listNamespaces", "createNamespace", "getNamespace", "updateNamespace", "searchCatalog", "browseCatalog", "listResources", "listResourceVersions")
# getNamespace has no variable input after binding its owned ID. Its positive
# control is meaningful; pretending to generate invalid bodies there is not.
CASES = tuple(f"test_generated[{name}-{mode}]" for name in OPERATIONS if name != "getNamespace" for mode in ("positive", "negative"))
MAX_REQUESTS = 1000
EXAMPLES = 20
SETUP = {"getResource": 3, "createResourceVersion": 1}
TOKENS = ("local-admin", "local-reader", "other-tenant-admin", "generated-no-reader")
CALIBRATIONS = ("test_valid", "test_invalid_acceptance", "test_response_defect", "test_authentication_loss", "test_transport_bounds", "test_schema_keywords", "test_query_serialization", "test_sequence_oracles", "test_list_oracles")
AUTHORING_OPERATIONS = ("createResource", "createResourceVersion")
AUTHORING_READS = ("getResource", "getResourceVersion", "getVersionRevision")
AUTHORING_CASES = tuple(f"test_authoring[{name}-{mode}]" for name in AUTHORING_OPERATIONS for mode in ("positive", "negative"))
AUTHORING_CALIBRATIONS = ("test_authoring_valid", "test_authoring_invalid_acceptance", "test_authoring_response_defect", "test_authoring_rejected_write", "test_authoring_wire_binding", "test_authoring_transport")


def prepare(environment):
    """An authenticated identity with no Catalog Reader, only in this fixture."""
    if environment.get("CATALOG_FUNCTIONAL_PROFILE") != "generated-api":
        raise Failure("generated-invalid-profile")
    config = read_document(ROOT / "config/postgres.json")
    config["auth"]["developmentTokens"]["generated-no-reader"] = {
        "subject": "generated-no-reader", "tenantId": "00000000-0000-7000-8000-000000000001", "roles": ["service_client"]}
    path = Path(environment["CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY"]) / "generated-config.json"
    write_document(path, config)
    path.chmod(0o444)


def retain_seed(directory, resources):
    seed = [{key: item.get(key) for key in ("resourceId", "kind", "identifier")} for item in resources]
    if (len(seed) != 3 or {item["kind"] for item in seed} != {"agent", "mcp_server", "skill"}
            or len({item["resourceId"] for item in seed}) != 3
            or any(not isinstance(item["resourceId"], str) or re.fullmatch(r"[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}", item["resourceId"]) is None
                   or not isinstance(item["identifier"], str) or not item["identifier"].startswith("urn:air:") for item in seed)):
        raise Failure("generated-seed-invalid")
    write_document(directory / "generated-seed.json", seed)


def pins():
    result = {}
    for line in (TOOLS / "tools.txt").read_text().splitlines():
        if not line.strip() or line.startswith("#"):
            continue
        match = re.fullmatch(r"([A-Za-z0-9_.-]+)==([A-Za-z0-9.+!-]+) --hash=sha256:[0-9a-f]{64}", line)
        if match is None:
            raise Failure("generated-tool-lock-invalid")
        name = re.sub(r"[-_.]+", "-", match[1]).lower()
        if name in result:
            raise Failure("generated-tool-lock-invalid")
        result[name] = match[2]
    if result.get("schemathesis") != "4.30.1":
        raise Failure("generated-tool-lock-invalid")
    return result


def installed():
    for package, version in pins().items():
        if importlib.metadata.version(package) != version:
            raise Failure("generated-tool-version-mismatch")


def report_bytes(path):
    with path.open("rb") as source:
        return source.read(1024 * 1024 + 1)


def junit(raw, expected):
    if not raw or len(raw) > 1024 * 1024 or b"<!" in raw:
        raise Failure("generated-report-invalid")
    root = ET.fromstring(raw)
    if any(node.get(key, "0") != "0" for node in root.iter("testsuite") for key in ("errors", "failures", "skipped")):
        raise Failure("generated-tests-incomplete")
    cases = root.findall(".//testcase")
    if sorted(case.get("name") for case in cases) != sorted(expected) or any(list(case) for case in cases):
        raise Failure("generated-tests-incomplete")
    return len(cases)


def valid_partition(selected, omitted, authority):
    """Use the Go contract inventory, never the generator's own denominator."""
    from operation_coverage import valid_ids
    try:
        valid_ids(authority)
        valid_ids(omitted)
    except Exception:
        return False
    return not set(selected).intersection(omitted) and set(selected).union(omitted) == set(authority)


def omitted_operations(loaded, selected):
    from operation_coverage import valid_ids
    valid_ids(sorted(loaded))
    assert set(selected).issubset(loaded), "selected operation missing from inventory"
    return sorted(set(loaded).difference(selected))


def require_inventory(loaded, authority):
    from operation_coverage import valid_ids
    valid_ids(authority)
    assert sorted(loaded) == authority, "Schemathesis operation inventory differs from OpenAPI"


def valid_evidence(value, authority):
    return (isinstance(value, dict) and value.get("tool") == "schemathesis-4.30.1"
            and value.get("operations") == list(OPERATIONS) and value.get("controls") == list(OPERATIONS)
            and value.get("tests") == len(CASES) + 2 and value.get("calibrations") == len(CALIBRATIONS)
            and type(value.get("requests")) is int and len(CASES) * EXAMPLES + len(OPERATIONS) + sum(SETUP.values()) <= value["requests"] <= MAX_REQUESTS
            and value.get("setup") == SETUP and all(type(n) is int for n in value["setup"].values())
            and isinstance(value.get("sequences"), dict) and value["sequences"].get("completed") == 20
            and isinstance(value["sequences"].get("steps"), dict) and set(value["sequences"]["steps"]) == set(KINDS)
            and all(type(n) is int and 20 <= n <= 60 for n in value["sequences"]["steps"].values())
            and 140 <= sum(value["sequences"]["steps"].values()) <= 180
            and isinstance(value.get("counts"), dict) and set(value["counts"]) == set(CASES)
            and all(type(n) is int and n == EXAMPLES for n in value["counts"].values())
            and isinstance(value.get("lockDigest"), str) and re.fullmatch(r"[0-9a-f]{64}", value["lockDigest"]) is not None
            and isinstance(value.get("omittedOperations"), list) and bool(value["omittedOperations"])
            and all(isinstance(name, str) and re.fullmatch(r"[A-Za-z][A-Za-z0-9_]{0,99}", name) for name in value["omittedOperations"])
            and value["omittedOperations"] == sorted(set(value["omittedOperations"]))
            and valid_partition(OPERATIONS, value["omittedOperations"], authority)
            and type(value.get("seed")) is int and 0 <= value["seed"] <= 2**32-1)


def valid_authoring(value, authority):
    return (isinstance(value, dict) and value.get("operations") == list(AUTHORING_OPERATIONS)
            and value.get("controls") == list(AUTHORING_OPERATIONS)
            and value.get("tests") == len(AUTHORING_CASES) + 1
            and value.get("calibrations") == len(AUTHORING_CALIBRATIONS)
            and value.get("counts") == dict.fromkeys(AUTHORING_CASES, EXAMPLES)
            and all(type(n) is int for n in value["counts"].values())
            and type(value.get("mutations")) is int and value["mutations"] == len(AUTHORING_CASES) * EXAMPLES
            and type(value.get("requests")) is int and 154 <= value["requests"] <= MAX_REQUESTS
            and type(value.get("stateChecks")) is int and value["stateChecks"] == 2 * (EXAMPLES * 2 + 2) + 16
            and type(value.get("eventChecks")) is int and value["eventChecks"] == 4
            and value.get("sqlCalibrations") == ["side-table-rejected-write", "audit-side-only", "outbox-side-only", "worker-bookkeeping"]
            and isinstance(value.get("readbacks"), dict) and set(value["readbacks"]) == set(AUTHORING_READS)
            and all(type(n) is int and n == EXAMPLES + 2 for name, n in value["readbacks"].items())
            and isinstance(value.get("omittedOperations"), list) and bool(value["omittedOperations"])
            and all(isinstance(name, str) and re.fullmatch(r"[A-Za-z][A-Za-z0-9_]{0,99}", name) for name in value["omittedOperations"])
            and value["omittedOperations"] == sorted(set(value["omittedOperations"]))
            and valid_partition(AUTHORING_OPERATIONS, value["omittedOperations"], authority))


def child_env():
    result = dict(os.environ)
    for key in list(result):
        if key.startswith(("PYTHON", "PYTEST", "HYPOTHESIS")):
            del result[key]
    result.update(PYTHONNOUSERSITE="1", PYTHONDONTWRITEBYTECODE="1", PYTEST_DISABLE_PLUGIN_AUTOLOAD="1")
    return result


def pytest_file(name, report, env):
    command([str(PYTHON), "-B", "-m", "pytest", "-q", "-p", "no:cacheprovider", "--tb=short",
             "-o", "addopts=", "-c", "/dev/null", str(TOOLS / name), "--junitxml=" + str(report)],
            env, 180, capture=True)


def run_suite():
    directory = Path(os.environ["CATALOG_FUNCTIONAL_STATE_DIRECTORY"])
    env = child_env()
    authority = json.loads(command([env["CATALOG_FUNCTIONAL_CONTRACT_DRIVER"], "--operations"], env, 15, capture=True, limit=65536))
    require_inventory(read_document(directory / "operations.json"), authority)
    seed = env.get("SCHEMATHESIS_SEED", "1")
    if not re.fullmatch(r"[0-9]{1,10}", seed) or int(seed) > 2**32-1:
        raise Failure("generated-seed-invalid")
    env["SCHEMATHESIS_SEED"] = seed
    pytest_file("calibration.py", directory / "generated-calibration.xml", {**env, "CATALOG_FUNCTIONAL_OPERATION_COVERAGE": "off"})
    junit(report_bytes(directory / "generated-calibration.xml"), CALIBRATIONS)
    pytest_file("pilot.py", directory / "generated.xml", env)
    count = junit(report_bytes(directory / "generated.xml"), ["test_controls", *CASES, "test_sequences"])
    evidence = read_document(directory / "generated-counts.json")
    evidence.update(tool="schemathesis-4.30.1", operations=list(OPERATIONS), controls=list(OPERATIONS), tests=count,
                    calibrations=len(CALIBRATIONS), seed=int(seed), lockDigest=hashlib.sha256((TOOLS / "tools.txt").read_bytes()).hexdigest())
    if not valid_evidence(evidence, authority):
        raise Failure("generated-evidence-incomplete")
    pytest_file("authoring_calibration.py", directory / "generated-authoring-calibration.xml", {**env, "CATALOG_FUNCTIONAL_OPERATION_COVERAGE": "off"})
    junit(report_bytes(directory / "generated-authoring-calibration.xml"), AUTHORING_CALIBRATIONS)
    pytest_file("authoring.py", directory / "generated-authoring.xml", env)
    authoring = read_document(directory / "generated-authoring-counts.json")
    authoring.update(tests=junit(report_bytes(directory / "generated-authoring.xml"), ["test_authoring_controls", *AUTHORING_CASES]),
                     calibrations=len(AUTHORING_CALIBRATIONS))
    if not valid_authoring(authoring, authority):
        raise Failure("generated-authoring-evidence-incomplete")
    evidence["authoring"] = authoring
    write_document(directory / "generated-api.json", evidence)


def retain(directory, output):
    """Preserve native outcomes and diagnostics with owned credentials redacted."""
    for name in ("generated.xml", "generated-calibration.xml", "generated-authoring.xml", "generated-authoring-calibration.xml"):
        path = directory / name
        if path.exists():
            raw = report_bytes(path)
            if len(raw) > 1024 * 1024 or b"<!" in raw:
                raise Failure("generated-report-invalid")
            root = ET.fromstring(raw)
            for node in root.iter():
                node.attrib.pop("hostname", None)
                if node.tag in {"failure", "error", "skipped", "system-out", "system-err"}:
                    # This profile has only fixed synthetic development credentials.
                    # Retain native failure diagnostics after removing every token;
                    # never use this policy for production or OIDC fixtures.
                    for token in TOKENS:
                        node.text = (node.text or "").replace(token, "REDACTED")
                        for key in node.attrib:
                            node.attrib[key] = node.attrib[key].replace(token, "REDACTED")
            (output / name).write_bytes(ET.tostring(root, encoding="utf-8"))
    path = directory / "generated-authoring-reproducer.json"
    if path.exists():
        value = read_document(path)
        if (not isinstance(value, dict) or set(value) != {"operation", "mode", "body"}
                or value["operation"] not in AUTHORING_OPERATIONS or value["mode"] not in {"positive", "negative"}):
            raise Failure("generated-reproducer-invalid")
        write_document(output / path.name, value)
    path = directory / "generated-reproducer.json"
    if path.exists():
        value = read_document(path)
        if (not isinstance(value, dict) or set(value) != {"operation", "mode", "body", "query", "status"}
                or value["operation"] not in OPERATIONS or value["mode"] not in {"positive", "negative"}
                or type(value["status"]) is not int or not 100 <= value["status"] <= 599
                or value["query"] is not None and not isinstance(value["query"], dict)):
            raise Failure("generated-reproducer-invalid")
        write_document(output / path.name, value)
    path = directory / "generated-sequence-reproducer.json"
    if path.exists():
        value = read_document(path)
        if (not isinstance(value, dict) or set(value) != {"actions", "label"}
                or not isinstance(value["actions"], list) or not 7 <= len(value["actions"]) <= 9
                or any(action not in KINDS for action in value["actions"]) or not set(KINDS).issubset(value["actions"])
                or not isinstance(value["label"], str) or not re.fullmatch(r"[a-z0-9]{1,20}", value["label"])):
            raise Failure("generated-sequence-reproducer-invalid")
        write_document(output / path.name, value)



def main():
    action = sys.argv[1:]
    if action == ["install"]:
        command([sys.executable, "-m", "venv", str(TOOLS / ".tools")], child_env(), 60)
        command([str(PYTHON), "-m", "pip", "--isolated", "install", "--disable-pip-version-check", "--require-hashes",
                 "--only-binary=:all:", "--no-deps", "-r", str(TOOLS / "tools.txt")], child_env(), 180)
    elif action == ["versions"]:
        installed()
    elif action == ["check"]:
        command([str(PYTHON), "-B", str(Path(__file__).resolve()), "versions"], child_env(), 15)
    elif action == ["run"]:
        run_suite()
    else:
        raise Failure("generated-invalid-command")


if __name__ == "__main__":
    main()
