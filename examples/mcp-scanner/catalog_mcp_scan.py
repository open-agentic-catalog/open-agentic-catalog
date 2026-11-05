"""Bounded capture validation and private reports; no scanner imports or I/O."""

from datetime import datetime
import hashlib
import json
import re


INPUT_LIMIT = 1 << 20
CONTEXT_LIMIT = 16 << 10
REPORT_LIMIT = 4 << 20
TOOL_LIMIT = 128
SCAN_SECONDS = 30
ANALYZER = "YARA"


def digest(content):
    return hashlib.sha256(content).hexdigest()


def encoded(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True, allow_nan=False).encode("ascii")


def decode(content, limit):
    if not isinstance(content, bytes) or len(content) > limit:
        raise ValueError("JSON input exceeds its byte limit")

    def pairs(items):
        result = {}
        for key, value in items:
            if key in result:
                raise ValueError("duplicate JSON key")
            result[key] = value
        return result

    def constant(_value):
        raise ValueError("non-finite JSON number")

    try:
        value = json.loads(content.decode("utf-8", errors="strict"), object_pairs_hook=pairs, parse_constant=constant)
        pending = [(value, 0)]
        while pending:
            item, depth = pending.pop()
            if depth > 64:
                raise ValueError("JSON input exceeds its nesting limit")
            if isinstance(item, str):
                item.encode("utf-8", errors="strict")
            elif isinstance(item, dict):
                pending.extend((v, depth + 1) for pair in item.items() for v in pair)
            elif isinstance(item, list):
                pending.extend((v, depth + 1) for v in item)
            elif isinstance(item, float):
                encoded(item)  # Reject overflow to infinity as well as explicit NaN.
        return value
    except (UnicodeError, RecursionError, json.JSONDecodeError) as error:
        raise ValueError("invalid UTF-8 or JSON input") from error


def capture(content):
    value = decode(content, INPUT_LIMIT)
    if not isinstance(value, dict) or not isinstance(value.get("tools"), list):
        raise ValueError("expected a tools/list result object with a tools array")
    if any(key in value for key in ("jsonrpc", "result", "method", "serverInfo", "serverCard")):
        raise ValueError("JSON-RPC envelopes and Server Cards are not tools/list captures")
    if "resultType" in value and value["resultType"] != "complete":
        raise ValueError("expected a complete tools/list result page")
    if len(value["tools"]) > TOOL_LIMIT:
        raise ValueError("capture exceeds 128 tools")
    if "nextCursor" in value and (not isinstance(value["nextCursor"], str) or not 1 <= len(value["nextCursor"]) <= 4096):
        raise ValueError("nextCursor must be a nonempty bounded string")
    names = set()
    for tool in value["tools"]:
        if not isinstance(tool, dict) or not isinstance(tool.get("name"), str) or not re.fullmatch(r"[A-Za-z0-9_.-]{1,128}", tool["name"]):
            raise ValueError("tool name must contain 1-128 ASCII letters, digits, periods, underscores or hyphens")
        if tool["name"] in names:
            raise ValueError("duplicate tool name")
        names.add(tool["name"])
        if "description" in tool and not isinstance(tool["description"], str):
            raise ValueError("tool description must be a string")
        if "title" in tool and not isinstance(tool["title"], str):
            raise ValueError("tool title must be a string")
        for field in ("annotations", "_meta"):
            if field in tool and not isinstance(tool[field], dict):
                raise ValueError("tool annotations and metadata must be objects")
        if "icons" in tool:
            if not isinstance(tool["icons"], list):
                raise ValueError("tool icons must be an array")
            for icon in tool["icons"]:
                if not isinstance(icon, dict) or not isinstance(icon.get("src"), str) or not icon["src"]:
                    raise ValueError("tool icon must include its source string")
        if "annotations" in tool:
            for field in ("readOnlyHint", "destructiveHint", "idempotentHint", "openWorldHint"):
                if field in tool["annotations"] and not isinstance(tool["annotations"][field], bool):
                    raise ValueError("tool annotation hints must be booleans")
        for field in ("inputSchema", "outputSchema"):
            if field == "outputSchema" and field not in tool:
                continue
            schema = tool.get(field)
            if not isinstance(schema, dict) or (field == "inputSchema" and schema.get("type") != "object"):
                raise ValueError("inputSchema must describe an object; outputSchema must be a JSON Schema object")
    return value


def check_schemas(value):
    # The isolated worker provides this dependency. Importing this module never
    # imports the independent scanner, its providers or its dotenv loader.
    from jsonschema import Draft202012Validator
    from jsonschema.exceptions import SchemaError

    try:
        for tool in value["tools"]:
            for field in ("inputSchema", "outputSchema"):
                if field in tool:
                    if tool[field].get("$schema", "https://json-schema.org/draft/2020-12/schema") != "https://json-schema.org/draft/2020-12/schema":
                        raise ValueError("this example supports JSON Schema draft 2020-12")
                    Draft202012Validator.check_schema(tool[field])
    except SchemaError as error:
        raise ValueError("invalid tool JSON Schema") from error


def context(content):
    value = decode(content, CONTEXT_LIMIT)
    if not isinstance(value, dict):
        raise ValueError("caller context must be a JSON object")
    return value


def attribution(source, captured_at):
    if not isinstance(source, str) or not 1 <= len(source) <= 1024 or any(ord(c) < 32 for c in source):
        raise ValueError("capture source must be a bounded label without control characters")
    try:
        source.encode("utf-8", errors="strict")
    except UnicodeError as error:
        raise ValueError("capture source must contain valid Unicode") from error
    if not isinstance(captured_at, str) or len(captured_at) > 64:
        raise ValueError("capture time must be an RFC 3339 timestamp")
    try:
        value = datetime.fromisoformat(captured_at.replace("Z", "+00:00"))
    except ValueError as error:
        raise ValueError("capture time must be an RFC 3339 timestamp") from error
    if value.tzinfo is None or not re.fullmatch(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})", captured_at):
        raise ValueError("capture time must include a timezone")
    return {"source": source, "capturedAt": captured_at, "callerProvided": True}


def configuration(rules_digest):
    return {"analyzers": [ANALYZER], "rulesSha256": rules_digest, "scope": "tool_descriptions_and_parameters"}


def report_base(value, input_bytes, context_bytes, origin, identity):
    return {
        "schemaVersion": 1,
        "status": "failed",
        "capture": {**origin, "inputSha256": digest(input_bytes), "hasNextCursor": "nextCursor" in value, "scope": "supplied_page"},
        "callerContext": context(context_bytes),
        "contextSha256": digest(context_bytes),
        "scanner": {"name": "cisco-ai-mcp-scanner", "version": identity["version"], "imageId": identity["imageId"]},
        "rulesSha256": identity["rulesSha256"],
        "configurationSha256": digest(encoded(configuration(identity["rulesSha256"]))),
        "plannedTools": len(value["tools"]),
        "completedTools": 0,
        "results": [],
    }


def normalize(raw_bytes, value, base):
    raw = decode(raw_bytes, REPORT_LIMIT)
    if not isinstance(raw, dict):
        raise ValueError("scanner output must be a JSON object")
    expected = {"inputSha256": base["capture"]["inputSha256"], "contextSha256": base["contextSha256"],
                "rulesSha256": base["rulesSha256"], "configurationSha256": base["configurationSha256"], "scannerVersion": base["scanner"]["version"]}
    if any(raw.get(k) != v for k, v in expected.items()):
        raise ValueError("scanner input, context or tool identity does not match")
    results = raw.get("results")
    if not isinstance(results, list) or len(results) != base["plannedTools"]:
        raise ValueError("scanner result count does not match the capture")
    wanted = {tool["name"] for tool in value["tools"]}
    observed = set()
    completed = 0
    incomplete = False
    for item in results:
        if not isinstance(item, dict) or not isinstance(item.get("tool_name"), str) or item["tool_name"] not in wanted or item["tool_name"] in observed:
            raise ValueError("scanner tool identity is missing, duplicated or unexpected")
        observed.add(item["tool_name"])
        status = item.get("status")
        analyzers, errors, issues = item.get("analyzers"), item.get("analyzer_errors"), item.get("findings")
        if not isinstance(status, str) or status not in {"completed", "partial", "failed", "skipped"} or not isinstance(errors, list) or not isinstance(issues, list):
            raise ValueError("scanner completion evidence is missing")
        if analyzers not in ([], [ANALYZER]):
            raise ValueError("scanner reports an unselected analyzer")
        if ((status == "completed" and (analyzers != [ANALYZER] or errors))
                or (status == "partial" and (analyzers != [ANALYZER] or not errors))
                or (status == "failed" and (analyzers or not errors))
                or (status == "skipped" and (analyzers or errors or issues))):
            raise ValueError("scanner completion evidence is inconsistent")
        for error in errors:
            if (not isinstance(error, dict) or error.get("analyzer") != ANALYZER or not isinstance(error.get("message"), str)
                    or not isinstance(error.get("content_type"), str) or error["content_type"] not in {"description", "parameters"}):
                raise ValueError("invalid analyzer error evidence")
        for finding in issues:
            if not isinstance(finding, dict) or finding.get("analyzer") != ANALYZER:
                raise ValueError("invalid finding attribution")
            if (not isinstance(finding.get("severity"), str) or finding["severity"] not in {"HIGH", "MEDIUM", "LOW", "INFO", "SAFE", "UNKNOWN"}
                    or not isinstance(finding.get("summary"), str) or not isinstance(finding.get("threat_category"), str)):
                raise ValueError("invalid finding fields")
            details = finding.get("details")
            if (not isinstance(details, dict) or details.get("tool_name") != item["tool_name"]
                    or not isinstance(details.get("content_type"), str) or details["content_type"] not in {"description", "parameters"}):
                raise ValueError("finding does not identify the selected tool and content type")
        completed += status == "completed"
        incomplete |= status != "completed"
    result = {**base, "completedTools": completed, "results": results}
    if not results or all(item["status"] == "skipped" for item in results):
        result["status"] = "skipped"
    elif incomplete:
        result["status"] = "partial" if completed or any(item["status"] == "partial" for item in results) else "failed"
    else:
        result["status"] = "partial" if base["capture"]["hasNextCursor"] else "completed"
    if len(encoded(result)) > REPORT_LIMIT:
        raise ValueError("normalized report exceeds its byte limit")
    return result


def exit_status(report):
    if report["status"] != "completed":
        return 2
    return 1 if any(item["findings"] for item in report["results"]) else 0
