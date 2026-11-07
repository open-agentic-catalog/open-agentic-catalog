"""Bounded native tooling coverage imports for the local Sonar snapshot."""

import hashlib
import json
from pathlib import PurePosixPath
from collections import Counter
import re
import shlex
import xml.etree.ElementTree as ET


PROFILES = {"python-unit": ("python", {"test"}),
            "node-ui-unit": ("javascript", {"catalog-ui/scripts", "documentation/scripts"}),
            "node-docs-unit": ("javascript", {"catalog-ui/scripts", "documentation/scripts"}),
            "node-compose": ("javascript", {"catalog-ui/scripts", "documentation/scripts"})}


def lcov_lines(text):
    """Split physical LF/CRLF records without splitting opaque Unicode payloads."""
    framed = text.split("\n")
    last = len(framed) - 1
    lines = []
    for index, line in enumerate(framed):
        if index == last and not line:
            break
        if index < last:
            line = line.removesuffix("\r")
        if "\r" in line:
            raise ValueError("invalid LCOV line framing")
        lines.append(line)
    return lines


def relative_path(name):
    if not isinstance(name, str) or not name or len(name) > 4096 or any(ord(char) < 32 for char in name):
        raise ValueError("invalid tooling coverage path")
    path = PurePosixPath(name)
    if path.is_absolute() or str(path) != name or any(part in {"..", "."} for part in path.parts):
        raise ValueError("tooling coverage paths must be relative and canonical")
    return path


def read_bounded(root, name, maximum):
    relative_path(name)
    path = root / name
    aliases = any((root.joinpath(*PurePosixPath(name).parts[:index])).is_symlink()
                  for index in range(1, len(PurePosixPath(name).parts) + 1))
    if aliases or not path.is_file() or not path.resolve().is_relative_to(root.resolve()):
        raise ValueError("tooling coverage input must be a regular checkout file")
    with path.open("rb") as source:
        raw = source.read(maximum + 1)
    if len(raw) > maximum:
        raise ValueError("tooling coverage input exceeds bound")
    return raw


def unique_object(pairs):
    value = {}
    for key, item in pairs:
        if key in value:
            raise ValueError("duplicate tooling coverage field")
        value[key] = item
    return value


def tooling_reports(value, root, snapshot):
    """Require successful native receipts whose full source inventory still matches."""
    if not value:
        return []
    if len(value) > 16384:
        raise ValueError("too many tooling coverage inputs")
    names = shlex.split(value)
    if not names or len(names) > 16 or len(names) != len(set(names)):
        raise ValueError("select unique bounded tooling coverage receipts")
    imported = []
    report_paths = set()
    for name in names:
        raw = read_bounded(root, name, 2 << 20)
        receipt = json.loads(raw.decode("utf-8"), object_pairs_hook=unique_object)
        if (not isinstance(receipt, dict) or type(receipt.get("schemaVersion")) is not int
                or receipt.get("schemaVersion") != 1 or receipt.get("status") != "passed"):
            raise ValueError("tooling coverage requires a successful native receipt")
        profile = receipt.get("profile")
        kind, roots = PROFILES.get(profile, (None, set())) if isinstance(profile, str) else (None, set())
        declared_roots = receipt.get("sourceRoots")
        if (kind is None or receipt.get("kind") != kind or not isinstance(declared_roots, list)
                or not all(isinstance(item, str) for item in declared_roots)
                or len(declared_roots) != len(roots) or set(declared_roots) != roots):
            raise ValueError("unexpected tooling coverage selection")
        suffix = ".py" if kind == "python" else ".mjs"
        expected = {path.relative_to(snapshot).as_posix()
                    for directory in roots for path in (snapshot / directory).rglob("*" + suffix) if path.is_file()}
        sources = receipt.get("sourceFiles")
        if not isinstance(sources, dict) or not expected or len(sources) > 10000 or set(sources) != expected:
            raise ValueError("incomplete tooling coverage source inventory")
        for path, digest in sources.items():
            relative_path(path)
            if not isinstance(digest, str) or not re.fullmatch(r"[a-f0-9]{64}", digest):
                raise ValueError("invalid tooling coverage source hash")
            if hashlib.sha256((snapshot / path).read_bytes()).hexdigest() != digest:
                raise ValueError("tooling coverage source changed; regenerate the native report")
        reports = receipt.get("reports")
        if not isinstance(reports, list) or len(reports) != 1 or not isinstance(reports[0], dict):
            raise ValueError("expected one native tooling coverage report")
        report = reports[0]
        if report.get("format") != ("cobertura" if kind == "python" else "lcov"):
            raise ValueError("unexpected native tooling coverage format")
        report_name = report.get("path")
        raw_report = read_bounded(root, report_name, 32 << 20)
        digest = hashlib.sha256(raw_report).hexdigest()
        if report.get("sha256") != digest or report_name in report_paths:
            raise ValueError("changed or duplicate native tooling coverage report")
        report_paths.add(report_name)
        imported.append({"kind": kind, "profile": profile, "path": report_name, "sha256": digest,
                         "receiptPath": name, "receiptSha256": hashlib.sha256(raw).hexdigest(),
                         "sourceFiles": len(sources),
                         "sourceManifestSha256": hashlib.sha256(json.dumps(sources, sort_keys=True).encode()).hexdigest(),
                         "tool": receipt.get("tool"), "raw": raw_report, "declaredSources": set(sources)})
    return imported


def line_counters(item, maximum):
    numbers = set()
    covered = branches = covered_branches = 0
    for line in item.findall("./lines/line"):
        number, hits = line.get("number", ""), line.get("hits", "")
        if not re.fullmatch(r"[1-9][0-9]{0,9}", number) or not re.fullmatch(r"[0-9]{1,20}", hits):
            raise ValueError("invalid Python coverage counter")
        if int(number) > maximum or number in numbers:
            raise ValueError("duplicate or outside-source Python coverage line")
        numbers.add(number)
        covered += int(hits) > 0
        branch = line.get("branch", "false")
        condition = line.get("condition-coverage")
        if branch not in {"true", "false"} or (branch == "false" and condition is not None):
            raise ValueError("invalid Python coverage branch flag")
        if branch == "true":
            match = re.fullmatch(r"([0-9]{1,3}(?:\.[0-9]{1,6})?)% \(([0-9]{1,10})/([0-9]{1,10})\)", condition or "")
            if match is None:
                raise ValueError("invalid Python coverage branch counter")
            percent, hit, total = float(match[1]), int(match[2]), int(match[3])
            if not total or hit > total or percent > 100 or (not int(hits) and hit):
                raise ValueError("contradictory Python coverage branch counter")
            if abs(percent - 100 * hit / total) > 1:
                raise ValueError("contradictory Python coverage branch percentage")
            branches += total
            covered_branches += hit
    return len(numbers), covered, branches, covered_branches


def python_coverage(raw, root, snapshot, declared):
    """Retain native Cobertura counters; normalize only exact declared source paths."""
    text = raw.decode("utf-8")
    if "<!doctype" in text.lower() or "<!entity" in text.lower():
        raise ValueError("coverage XML declarations are not supported")
    try:
        document = ET.fromstring(text)
    except ET.ParseError:
        raise ValueError("invalid native Python coverage XML") from None
    if document.tag != "coverage":
        raise ValueError("expected native Python coverage XML")
    roots = []
    for source in document.findall("./sources/source"):
        value = source.text or ""
        if value == str(root):
            roots.append("")
        elif value.startswith(str(root) + "/"):
            roots.append(value[len(str(root)) + 1:])
        elif value in {"", ".", "test"}:
            roots.append("" if value == "." else value)
        else:
            raise ValueError("Python coverage root is outside this checkout")
    seen = set()
    totals = [0, 0, 0, 0]
    for item in document.findall("./packages/package/classes/class"):
        filename = item.get("filename", "")
        if filename.startswith(str(root) + "/"):
            filename = filename[len(str(root)) + 1:]
        relative_path(filename)
        candidates = {filename, *(prefix + "/" + filename for prefix in roots if prefix)} & declared
        if len(candidates) != 1:
            raise ValueError("Python coverage source is outside the declared snapshot")
        path = candidates.pop()
        if path in seen or not path.endswith(".py"):
            raise ValueError("duplicate Python coverage source")
        seen.add(path)
        item.set("filename", path)
        maximum = len((snapshot / path).read_bytes().splitlines())
        totals = [left + right for left, right in zip(totals, line_counters(item, maximum), strict=True)]
    if not seen or not totals[0]:
        raise ValueError("empty native Python coverage report")
    for attribute, actual in zip(("lines-valid", "lines-covered", "branches-valid", "branches-covered"), totals, strict=True):
        value = document.get(attribute, "")
        if not re.fullmatch(r"[0-9]{1,10}", value) or int(value) != actual:
            raise ValueError("incomplete or contradictory Python coverage totals")
    element = document.find("sources")
    if len(document.findall("sources")) > 1:
        raise ValueError("duplicate Python coverage sources")
    if element is not None:
        document.remove(element)
    element = ET.SubElement(document, "sources")
    ET.SubElement(element, "source").text = "."
    return ET.tostring(document, encoding="unicode") + "\n"


def validate_native_lcov(text, snapshot, declared):
    """Bind native LCOV locations and summaries without merging profile counters."""
    seen = set()
    record = None
    for line in lcov_lines(text):
        if line.startswith("SF:"):
            path = line[3:]
            if path not in declared or path in seen or record is not None:
                raise ValueError("unexpected or duplicate native LCOV source")
            seen.add(path)
            source = (snapshot / path).read_bytes()
            maximum = len(source.split(b"\n")) - source.endswith(b"\n")
            record = {"maximum": maximum,
                      "lines": {}, "branches": {}, "functions": [], "functionHits": [], "totals": {}}
        elif line == "end_of_record":
            if record is None:
                raise ValueError("unexpected native LCOV end")
            validate_lcov_totals(record)
            record = None
        elif line.startswith("TN:"):
            if record is not None:
                raise ValueError("unexpected native LCOV test name")
        else:
            if record is None:
                raise ValueError("native LCOV row outside source")
            tag, value = line.split(":", 1)
            if tag in {"DA", "BRDA", "FN"}:
                location = value.split(",", 1)[0]
                if len(location) > 10 or not 1 <= int(location) <= record["maximum"]:
                    raise ValueError("native LCOV location is outside source")
            if tag == "DA":
                number, hits = value.split(",")
                if number in record["lines"]:
                    raise ValueError("duplicate native LCOV line")
                record["lines"][number] = int(hits) > 0
            elif tag == "BRDA":
                number, block, branch, hits = value.split(",")
                identity = (number, block, branch)
                if identity in record["branches"]:
                    raise ValueError("duplicate native LCOV branch")
                record["branches"][identity] = hits != "-" and int(hits) > 0
            elif tag == "FN":
                record["functions"].append(value.split(",", 1)[1])
            elif tag == "FNDA":
                hits, name = value.split(",", 1)
                record["functionHits"].append((name, int(hits) > 0))
            elif tag in {"FNF", "FNH", "LF", "LH", "BRF", "BRH"}:
                if tag in record["totals"] or len(value) > 10:
                    raise ValueError("duplicate or invalid native LCOV summary")
                record["totals"][tag] = int(value)
            else:
                raise ValueError("unexpected native LCOV counter")
    if record is not None or not seen:
        raise ValueError("empty or incomplete native LCOV")


def validate_lcov_totals(record):
    totals = record["totals"]
    if set(totals) != {"FNF", "FNH", "LF", "LH", "BRF", "BRH"}:
        raise ValueError("incomplete native LCOV summaries")
    if Counter(record["functions"]) != Counter(name for name, _hit in record["functionHits"]):
        raise ValueError("inconsistent native LCOV function inventory")
    lines, hit_lines = len(record["lines"]), sum(record["lines"].values())
    # Ignored native lines are omitted from DA and count as covered in LF/LH.
    if (totals["LF"] != record["maximum"] or not 0 <= totals["LH"] <= totals["LF"]
            or totals["LF"] < lines or totals["LH"] < hit_lines
            or totals["LF"] - lines != totals["LH"] - hit_lines):
        raise ValueError("contradictory native LCOV line summary")
    for found, hit, count, covered in (
            ("BRF", "BRH", len(record["branches"]), sum(record["branches"].values())),
            ("FNF", "FNH", len(record["functions"]), sum(hit for _name, hit in record["functionHits"]))):
        # Ignored function/branch ranges remain in detail with zero counts but
        # can count as covered in the summary. Preserve both native observations.
        if not covered <= totals[hit] <= totals[found] == count:
            raise ValueError("contradictory native LCOV summary")
    if not record["lines"]:
        raise ValueError("native LCOV line inventory is outside source")
