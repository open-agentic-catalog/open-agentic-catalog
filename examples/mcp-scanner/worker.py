"""Offline scanner process inside the isolated tool image."""

import argparse
import asyncio
from contextlib import redirect_stdout
from importlib import metadata
import os
from pathlib import Path
import re
import sys

import catalog_mcp_scan as core


RULES = Path("/opt/rules")


def rules_digest(directory=RULES):
    files = sorted(directory.iterdir())
    rules = []
    for path in files:
        if path.suffix in {".yar", ".yara"}:
            if path.is_symlink() or not path.is_file():
                raise ValueError("invalid rules file")
            rules.append({"name": path.name, "sha256": core.digest(path.read_bytes())})
    if not rules:
        raise ValueError("missing YARA rules")
    return core.digest(core.encoded(rules))


def identity():
    packages = {}
    for dist in metadata.distributions():
        name = re.sub(r"[-_.]+", "-", dist.metadata["Name"]).lower()
        packages[name] = {"version": dist.version, "license": dist.metadata.get("License-Expression") or dist.metadata.get("License") or "unspecified"}
    return {"version": metadata.version("cisco-ai-mcp-scanner"), "rulesSha256": rules_digest(), "packages": packages}


async def scan(path, context_path):
    # Docker supplies no credentials or host configuration. These switches also
    # prevent package initialization from loading dotenv or remote cost data.
    os.environ["PYTHON_DOTENV_DISABLED"] = "1"
    os.environ["LITELLM_LOCAL_MODEL_COST_MAP"] = "true"
    content = path.read_bytes()
    context_bytes = context_path.read_bytes()
    value = core.capture(content)
    core.context(context_bytes)
    core.check_schemas(value)
    observed = identity()

    from mcpscanner.core.analyzers.static_analyzer import StaticAnalyzer
    from mcpscanner.core.analyzers.yara_analyzer import YaraAnalyzer

    analyzer = StaticAnalyzer(analyzers=[YaraAnalyzer(rules_dir=RULES)])
    native = await analyzer.scan_tools_file(path)
    results = []
    for item in native:
        results.append({
            "tool_name": item["tool_name"], "status": item["status"],
            "analyzers": item["analyzers"], "analyzer_errors": item["analyzer_errors"],
            "findings": [{"severity": finding.severity, "summary": finding.summary,
                          "analyzer": finding.analyzer, "threat_category": finding.threat_category,
                          "details": finding.details} for finding in item["findings"]],
        })
    return {"scannerVersion": observed["version"], "rulesSha256": observed["rulesSha256"],
            "configurationSha256": core.digest(core.encoded(core.configuration(observed["rulesSha256"]))),
            "inputSha256": core.digest(content), "contextSha256": core.digest(context_bytes), "results": results}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--identity", action="store_true")
    parser.add_argument("--input", type=Path)
    parser.add_argument("--context", type=Path)
    args = parser.parse_args()
    try:
        with redirect_stdout(sys.stderr):
            result = identity() if args.identity else asyncio.run(scan(args.input, args.context))
        content = core.encoded(result)
        if len(content) + 1 > core.REPORT_LIMIT:
            raise ValueError("scanner report exceeds its byte limit")
        sys.stdout.buffer.write(content + b"\n")
        return 0
    except Exception:
        # Upstream exceptions can contain scanned data. Keep diagnostics fixed.
        print("Offline scanner failed; inspect the supplied input and tool installation.", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
