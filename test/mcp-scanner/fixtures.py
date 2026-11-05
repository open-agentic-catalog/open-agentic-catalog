"""Inert scanner fixtures and first-party imports; no upstream imports."""

import copy
import importlib.util
from pathlib import Path


HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
spec = importlib.util.spec_from_file_location("scanner_runner", HERE / "runner.py")
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)
core = runner.core


def tool(name="add_integers"):
    return {"name": name, "description": "Add two integers.", "inputSchema": {"type": "object", "properties": {
        "left": {"type": "integer"}, "right": {"type": "integer"}}, "required": ["left", "right"], "additionalProperties": False}}


def document():
    return {"tools": [tool()]}


def base(value=None):
    value = document() if value is None else value
    return core.report_base(value, core.encoded(value), b"{}", core.attribution("test capture", "2026-10-28T00:00:00Z"),
                            {"version": "4.8.7", "imageId": "sha256:" + "a" * 64, "rulesSha256": "b" * 64})


def native(value=None):
    value = document() if value is None else value
    report = base(value)
    return {"inputSha256": report["capture"]["inputSha256"], "contextSha256": report["contextSha256"],
            "rulesSha256": report["rulesSha256"], "configurationSha256": report["configurationSha256"], "scannerVersion": "4.8.7",
            "results": [{"tool_name": item["name"], "status": "completed", "analyzers": ["YARA"], "analyzer_errors": [], "findings": []}
                        for item in value["tools"]]}


def issue(content_type="description"):
    return {"severity": "HIGH", "summary": "Inert rule match", "analyzer": "YARA", "threat_category": "PROMPT INJECTION",
            "details": {"tool_name": "add_integers", "content_type": content_type, "raw_response": {"matches": ["test-rule"]}}}


def changed(value, **updates):
    result = copy.deepcopy(value)
    result.update(updates)
    return result
