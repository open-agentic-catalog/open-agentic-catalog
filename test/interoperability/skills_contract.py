"""Independent native-output and complete-report assertions; no processes or tools."""

import json
from pathlib import PurePosixPath
import xml.etree.ElementTree as ET

from common import Result
from skills_git import DESCRIPTION, NAME

DIRECT = {"valid", "missing-document", "malformed", "missing-field", "parent-mismatch", "lowercase",
          "trim-name", "nfkc-name", "empty-compatibility", "embedded-delimiter"}
REFUSALS = {"missing-commit", "missing-directory", "altered-file", "missing-file", "extra-file", "executable-mode",
            "missing", "invalid", "wrong-tenant"}
NATIVE_REFUSALS = {
    "missing-document": "Missing required file: SKILL.md",
    "malformed": "Invalid YAML in frontmatter: While scanning",
    "missing-field": "Missing required field in frontmatter: name",
    "parent-mismatch": "Directory name 'wrong-parent' must match skill name 'owned-skill'",
    "embedded-delimiter": "Invalid YAML in frontmatter: while scanning a quoted scalar",
}


def check_native(result, command, path, *, valid=True):
    if not valid:
        assert result.code == 1 and result.stderr.strip() and not result.stdout.strip(), "incorrect native refusal"
        reason = NATIVE_REFUSALS[PurePosixPath(path).parent.name]
        assert command == "validate" and result.stderr.startswith("Validation failed for " + path + ":\n  - " + reason), "unexpected native refusal reason"
        return
    assert result.code == 0 and not result.stderr.strip(), "native reference command failed"
    if command == "validate":
        assert result.stdout.strip() == "Valid skill: " + path
    elif command == "read-properties":
        assert json.loads(result.stdout) == {"name": NAME, "description": DESCRIPTION, "license": "Apache-2.0", "metadata": {"author": "owned-fixture"}}
    elif command == "to-prompt":
        root = ET.fromstring(result.stdout)
        assert root.tag == "available_skills" and len(root) == 1 and root[0].tag == "skill"
        assert [e.tag for e in root[0]] == ["name", "description", "location"]
        assert [(e.text or "").strip() for e in root[0]] == [NAME, DESCRIPTION, path + "/SKILL.md"]
        assert "&amp;" in result.stdout and "&lt;results&gt;" in result.stdout
    else:
        raise ValueError("unknown native command")


def complete(report):
    assert report["cleanup"] and report["source_unchanged"] and report["isolation"]
    assert set(report["direct"]) == DIRECT and all(report["direct"].values())
    assert set(report["refusals"]) == REFUSALS and all(report["refusals"].values())
    assert report["governed"] and report["immutable"] and report["no_execution"] and report["retained"]
    assert all(report[key] for key in ("source", "tools", "binaries", "selection", "native"))
    expected = {}
    for case in DIRECT:
        name = "wrong-parent" if case == "parent-mismatch" else NAME
        path = "/packages/" + case + "/" + name
        expected[("validate", path)] = case not in NATIVE_REFUSALS
        if case in {"valid", "parent-mismatch"}:
            expected[("read-properties", path)] = True
            expected[("to-prompt", path)] = True
    for command in ("validate", "read-properties", "to-prompt"):
        expected[(command, "/packages/governed/" + NAME)] = True
    assert len(report["native"]) == len(expected), "incomplete native outputs"
    for row in report["native"]:
        key = (row["command"], row["path"])
        assert key in expected, "unexpected or duplicate native result"
        check_native(Result(row["exit"], row["stdout"], row["stderr"]), *key, valid=expected.pop(key))
    assert not expected


def check_refusal(case, error, before, after):
    assert before == after, "refusal occurred after native consumption"
    if case == "missing-commit":
        assert isinstance(error, RuntimeError) and str(error).strip() == "git failed (128): fatal: git cat-file: could not get object info"
    elif case == "missing-directory":
        assert isinstance(error, RuntimeError) and str(error).startswith("git failed (128): fatal: Not a valid object name ") and str(error).strip().endswith(":absent/" + NAME)
    else:
        assert isinstance(error, AssertionError) and str(error).startswith("retained "), "unexpected inventory failure"
