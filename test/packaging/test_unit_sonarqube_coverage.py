"""Source-bound native coverage imports use inert files, not scanner services."""

import copy
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock
import xml.etree.ElementTree as ET

import sonarqube as sonar
import sonarqube_coverage as native


PYTHON_XML = b'''<coverage lines-valid="2" lines-covered="2" branches-valid="2" branches-covered="1">
<sources><source>.</source></sources><packages><package><classes>
<class filename="test/helper.py"><lines><line number="1" hits="3"/>
<line number="2" hits="1" branch="true" condition-coverage="50% (1/2)"/>
</lines></class></classes></package></packages></coverage>'''
NODE_LCOV = """TN:
SF:catalog-ui/scripts/helper.mjs
FN:1,same
FN:3,same
FNDA:2,same
FNDA:0,same
FNF:2
FNH:1
BRDA:2,0,0,1
BRDA:3,1,0,0
BRF:2
BRH:1
DA:1,1
DA:2,1
DA:3,0
DA:4,0
LH:2
LF:4
end_of_record
"""


def digest(value):
    return hashlib.sha256(value).hexdigest()


class NativeCoverageTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name).resolve()
        self.snapshot = self.root / "snapshot"
        self.artifacts = self.root / "artifacts"
        self.artifacts.mkdir()
        self.files = {
            "test/helper.py": b"value = 1\nif value:\n    value += 1\nprint(value)\n",
            "test/unobserved.py": b"raise RuntimeError('never execute inventory')\n",
            "catalog-ui/scripts/helper.mjs": (
                b"const first = { same() { return 1; } };\n"
                b"if (first.same()) first.same();\n"
                b"const second = { same() { return 2; } };\n"
                b"void second;\n"),
            "documentation/scripts/unobserved.mjs": b"throw new Error('never execute inventory');\n",
        }
        for name, value in self.files.items():
            path = self.snapshot / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(value)
        self.declared_python = {"test/helper.py", "test/unobserved.py"}
        self.declared_node = {"catalog-ui/scripts/helper.mjs", "documentation/scripts/unobserved.mjs"}

    def receipt(self, kind="python"):
        python = kind == "python"
        raw = PYTHON_XML if python else NODE_LCOV.encode()
        path = "reports/python.xml" if python else "reports/node.info"
        target = self.root / path
        target.parent.mkdir(exist_ok=True)
        target.write_bytes(raw)
        names = self.declared_python if python else self.declared_node
        return {"schemaVersion": 1, "status": "passed", "kind": kind,
                "profile": "python-unit" if python else "node-ui-unit",
                "sourceRoots": ["test"] if python else ["catalog-ui/scripts", "documentation/scripts"],
                "sourceFiles": {name: digest(self.files[name]) for name in names},
                "reports": [{"format": "cobertura" if python else "lcov", "path": path, "sha256": digest(raw)}],
                "tool": {"name": "coverage.py" if python else "node", "version": "synthetic"}}

    def publish(self, receipt, name="receipt.json"):
        (self.root / name).write_text(json.dumps(receipt))
        return native.tooling_reports(name, self.root, self.snapshot)

    def xml(self, root):
        return native.python_coverage(ET.tostring(root), self.root, self.snapshot, self.declared_python)

    def lcov(self, text=NODE_LCOV, declared=None):
        # The native validator consumes the existing normalizer's supported grammar.
        normalized = sonar.coverage(text.encode(), "js", self.snapshot)
        native.validate_native_lcov(normalized, self.snapshot, self.declared_node if declared is None else declared)
        return normalized

    def test_valid_receipts_bind_all_sources_without_inventing_unobserved_measurements(self):
        for kind in ("python", "javascript"):
            with self.subTest(kind=kind):
                receipt = self.receipt(kind)
                item, = self.publish(receipt)
                raw = (self.root / receipt["reports"][0]["path"]).read_bytes()
                self.assertEqual(item["raw"], raw)
                self.assertEqual(item["sha256"], digest(raw))
                self.assertEqual(item["receiptSha256"], digest((self.root / "receipt.json").read_bytes()))
                self.assertEqual(item["sourceManifestSha256"], digest(json.dumps(receipt["sourceFiles"], sort_keys=True).encode()))
                self.assertEqual(item["declaredSources"], set(receipt["sourceFiles"]))
                self.assertEqual(item["sourceFiles"], 2)
                if kind == "python":
                    normalized = native.python_coverage(raw, self.root, self.snapshot, item["declaredSources"])
                    self.assertEqual(ET.fromstring(normalized).find(".//class").get("filename"), "test/helper.py")
                else:
                    self.assertEqual(self.lcov(raw.decode(), item["declaredSources"]), NODE_LCOV)
                self.assertNotIn("unobserved", raw.decode())
        self.assertEqual(native.tooling_reports(None, self.root, self.snapshot), [])
        for profile in ("node-ui-unit", "node-docs-unit", "node-compose"):
            with self.subTest(profile=profile):
                receipt = self.receipt("javascript")
                receipt["profile"] = profile
                self.assertEqual(self.publish(receipt)[0]["profile"], profile)

    def test_source_inventory_must_be_exact_and_hashes_must_match_current_snapshot(self):
        first = self.receipt()
        inventories = [[], {}, {"test/helper.py": first["sourceFiles"]["test/helper.py"]},
                       {**first["sourceFiles"], "test/extra.py": "0" * 64}]
        for sources in inventories:
            with self.subTest(sources=sources):
                changed = copy.deepcopy(first)
                changed["sourceFiles"] = sources
                with self.assertRaisesRegex(ValueError, "source inventory"):
                    self.publish(changed)
        for wrong in (None, [], "A" * 64, "0" * 63, "0" * 64):
            with self.subTest(hash=wrong):
                changed = copy.deepcopy(first)
                changed["sourceFiles"]["test/helper.py"] = wrong
                with self.assertRaisesRegex(ValueError, "source hash|source changed"):
                    self.publish(changed)
        (self.snapshot / "test/helper.py").write_bytes(b"changed source\n")
        with self.assertRaisesRegex(ValueError, "source changed"):
            self.publish(first)

    def test_receipt_types_status_profile_and_selection_are_checked_before_use(self):
        first = self.receipt()
        changes = [("schemaVersion", value) for value in (None, True, 1.0, "1", 2)]
        changes += [("status", value) for value in (None, "failed", "incomplete", [])]
        changes += [("profile", value) for value in (None, [], {}, "node-ui-unit", "unknown")]
        changes += [("kind", value) for value in (None, "javascript", {})]
        changes += [("sourceRoots", value) for value in (None, "test", [{}], ["test", "test"], ["elsewhere"])]
        changes += [("reports", value) for value in (None, [], [None], [{}, {}])]
        for field, value in changes:
            with self.subTest(field=field, value=value):
                changed = copy.deepcopy(first)
                changed[field] = value
                with self.assertRaises(ValueError):
                    self.publish(changed)
        for value in ([], None, True, 1):
            with self.subTest(root=value), self.assertRaises(ValueError):
                self.publish(value)

    def test_report_format_path_hash_and_duplicate_inputs_are_refused(self):
        first = self.receipt()
        for field, value in (("format", "lcov"), ("path", None), ("path", []),
                             ("path", "../python.xml"), ("sha256", None), ("sha256", "0" * 64)):
            with self.subTest(field=field, value=value):
                changed = copy.deepcopy(first)
                changed["reports"][0][field] = value
                with self.assertRaises(ValueError):
                    self.publish(changed)
        self.publish(first, "first.json")
        self.publish(first, "second.json")
        for selected in ("first.json first.json", "first.json second.json", "x " * 17, "x" * 16385):
            with self.subTest(selected=selected[:80]), self.assertRaises(ValueError):
                native.tooling_reports(selected, self.root, self.snapshot)
        (self.root / first["reports"][0]["path"]).write_bytes(b"changed report")
        with self.assertRaisesRegex(ValueError, "changed or duplicate"):
            native.tooling_reports("first.json", self.root, self.snapshot)

    def test_json_duplicate_fields_invalid_encoding_and_trailing_values_are_refused(self):
        raw = json.dumps(self.receipt())
        invalid = [raw.replace('"schemaVersion": 1', '"schemaVersion": 1, "schemaVersion": 1'),
                   raw.replace('"format": "cobertura"', '"format": "cobertura", "format": "cobertura"'),
                   raw + "{}", "{", b"\xff"]
        for value in invalid:
            with self.subTest(value=str(value)[:80]):
                (self.root / "receipt.json").write_bytes(value.encode() if isinstance(value, str) else value)
                with self.assertRaises(ValueError):
                    native.tooling_reports("receipt.json", self.root, self.snapshot)

    def test_regular_file_and_canonical_path_bounds_include_symlink_ancestors(self):
        receipt = self.receipt()
        self.publish(receipt)
        (self.root / "receipt-link.json").symlink_to(self.root / "receipt.json")
        with self.assertRaisesRegex(ValueError, "regular checkout"):
            native.tooling_reports("receipt-link.json", self.root, self.snapshot)
        (self.root / "reports/link.xml").symlink_to(self.root / "reports/python.xml")
        (self.root / "alias").symlink_to(self.root / "reports", target_is_directory=True)
        for path in ("reports/link.xml", "alias/python.xml"):
            with self.subTest(path=path):
                changed = copy.deepcopy(receipt)
                changed["reports"][0]["path"] = path
                with self.assertRaisesRegex(ValueError, "regular checkout"):
                    self.publish(changed)
        for path in ("/absolute", "../outside", "./receipt.json", "reports//python.xml", "reports/../receipt.json", "a\nb"):
            with self.subTest(path=path), self.assertRaises(ValueError):
                native.read_bounded(self.root, path, 100)
        (self.root / "bounded").write_bytes(b"1234")
        self.assertEqual(native.read_bounded(self.root, "bounded", 4), b"1234")
        with self.assertRaisesRegex(ValueError, "exceeds bound"):
            native.read_bounded(self.root, "bounded", 3)

    def test_python_source_roots_normalize_only_declared_files_and_preserve_counters(self):
        cases = [(".", "test/helper.py"), ("test", "helper.py"),
                 (str(self.root), "test/helper.py"), (str(self.root / "test"), "helper.py"),
                 (str(self.root), str(self.root / "test/helper.py"))]
        original = ET.fromstring(PYTHON_XML)
        counters = [dict(line.attrib) for line in original.findall(".//line")]
        for source, filename in cases:
            with self.subTest(source=source, filename=filename):
                root = ET.fromstring(PYTHON_XML)
                root.find("./sources/source").text = source
                root.find(".//class").set("filename", filename)
                normalized = ET.fromstring(self.xml(root))
                self.assertEqual([node.text for node in normalized.findall("./sources/source")], ["."])
                self.assertEqual(normalized.find(".//class").get("filename"), "test/helper.py")
                self.assertEqual([dict(line.attrib) for line in normalized.findall(".//line")], counters)
                self.assertEqual(normalized.attrib, original.attrib)

    def test_python_xml_refuses_entities_outside_paths_duplicate_roots_and_classes(self):
        for prefix in (b'<!DOCTYPE coverage [<!ENTITY x "inert">]>', b'<!ENTITY x SYSTEM "https://unfetched.invalid/evidence">'):
            with self.subTest(prefix=prefix), self.assertRaisesRegex(ValueError, "declarations"):
                native.python_coverage(prefix + PYTHON_XML, self.root, self.snapshot, self.declared_python)
        roots = []
        for source, filename in (("/outside", "test/helper.py"), (".", "/outside.py"),
                                 (".", "../helper.py"), (".", "test/missing.py")):
            root = ET.fromstring(PYTHON_XML)
            root.find("./sources/source").text = source
            root.find(".//class").set("filename", filename)
            roots.append(root)
        root = ET.fromstring(PYTHON_XML)
        root.append(copy.deepcopy(root.find("sources")))
        roots.append(root)
        root = ET.fromstring(PYTHON_XML)
        root.find(".//classes").append(copy.deepcopy(root.find(".//class")))
        roots.append(root)
        for index, root in enumerate(roots):
            with self.subTest(case=index), self.assertRaises(ValueError):
                self.xml(root)
        for raw in (b"<invalid/>", b"<coverage>"):
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                native.python_coverage(raw, self.root, self.snapshot, self.declared_python)

    def test_python_line_and_branch_guards_reject_the_intended_invalid_counter(self):
        changes = [("number", value) for value in ("0", "-1", "5", "1", "1" * 11)]
        changes += [("hits", value) for value in ("-1", "1" * 21)]
        changes += [("branch", "maybe"), ("branch", "false"), ("hits", "0")]
        changes += [("condition-coverage", value) for value in (None, "NaN", "100% (3/2)", "0% (0/0)", "101% (2/2)", "0% (1/2)")]
        for field, value in changes:
            with self.subTest(field=field, value=value):
                root = ET.fromstring(PYTHON_XML)
                line = root.findall(".//line")[1]
                if value is None:
                    line.attrib.pop(field)
                else:
                    line.set(field, value)
                with self.assertRaisesRegex(ValueError, "counter|outside-source|branch|percentage"):
                    self.xml(root)
        root = ET.fromstring(PYTHON_XML)
        root.find(".//lines").append(copy.deepcopy(root.findall(".//line")[0]))
        with self.assertRaisesRegex(ValueError, "duplicate"):
            self.xml(root)

    def test_python_report_totals_and_nonempty_line_data_are_required(self):
        for field in ("lines-valid", "lines-covered", "branches-valid", "branches-covered"):
            for value in (None, "999", "-1"):
                with self.subTest(field=field, value=value):
                    root = ET.fromstring(PYTHON_XML)
                    if value is None:
                        root.attrib.pop(field)
                    else:
                        root.set(field, value)
                    with self.assertRaisesRegex(ValueError, "totals"):
                        self.xml(root)
        root = ET.fromstring(PYTHON_XML)
        root.find(".//lines").clear()
        for field in root.attrib:
            root.set(field, "0")
        with self.assertRaisesRegex(ValueError, "empty"):
            self.xml(root)

    def test_lcov_physical_frames_preserve_opaque_names_and_reject_bare_carriage_returns(self):
        for name in ("scope\u0085name", "vertical\vform\ffile\x1cgroup\x1drecord\x1e",
                     "line\u2028paragraph\u2029", "opaque\u0085SF:documentation/scripts/unobserved.mjs"):
            text = NODE_LCOV.replace("same", name)
            for separator in ("\n", "\r\n"):
                for terminated in (False, True):
                    raw = text.removesuffix("\n").replace("\n", separator) + (separator if terminated else "")
                    with self.subTest(name=name, separator=separator, terminated=terminated):
                        self.assertEqual(self.lcov(raw), text)
                        native.validate_native_lcov(raw, self.snapshot, self.declared_node)
        for text in (NODE_LCOV.replace("FN:1,same", "FN:1,same\rinside"),
                     NODE_LCOV.replace("FNDA:2,same", "FNDA:2,same\rinside"),
                     NODE_LCOV.replace("FN:1,same", "FN:1,same\ninside"),
                     NODE_LCOV.replace("FNDA:2,same", "FNDA:2,same\ninside"),
                     NODE_LCOV.replace("\n", "\r"), NODE_LCOV.removesuffix("\n") + "\r",
                     NODE_LCOV + "\n"):
            with self.subTest(invalid=text), self.assertRaisesRegex(sonar.Failure, "invalid LCOV (row|line framing)"):
                self.lcov(text)

    def test_native_lcov_repeated_function_names_and_ignored_counters_remain_native(self):
        self.assertEqual(self.lcov(), NODE_LCOV)
        name = "catalog-ui/scripts/helper.mjs"
        # Ignore the final expression; it is absent from DA but covered in LF/LH.
        (self.snapshot / name).write_bytes(self.files[name].replace(
            b"return 2; } };\n", b"return 2; } }; /* node:coverage ignore next */\n"))
        ignored_line = NODE_LCOV.replace("DA:4,0\n", "").replace("LH:2", "LH:3")
        self.assertEqual(self.lcov(ignored_line), ignored_line)
        # An ignored function/range remains in FNDA/BRDA with zero raw hits.
        (self.snapshot / name).write_bytes(self.files[name].replace(
            b"if (first.same()) first.same();\n", b"if (first.same()) first.same(); /* node:coverage ignore next */\n"))
        ignored_ranges = (NODE_LCOV.replace("DA:3,0\n", "").replace("LH:2", "LH:3")
                          .replace("FNH:1", "FNH:2").replace("BRH:1", "BRH:2"))
        self.assertEqual(self.lcov(ignored_ranges), ignored_ranges)
        self.assertIn("FNDA:0,same", ignored_ranges)
        self.assertIn("BRDA:3,1,0,0", ignored_ranges)
        self.assertNotIn("\nDA:3,", ignored_ranges)

    def test_native_lcov_locations_include_exact_final_line_without_an_eof_slot(self):
        name = "catalog-ui/scripts/helper.mjs"
        for source in (self.files[name], self.files[name].rstrip(b"\n"), self.files[name].replace(b"\n", b"\r\n")):
            with self.subTest(ending=source[-2:]):
                (self.snapshot / name).write_bytes(source)
                self.assertEqual(self.lcov(), NODE_LCOV)
                for old, new in (("DA:4,0", "DA:5,0"), ("FN:3,same", "FN:5,same"),
                                 ("BRDA:3,1,0,0", "BRDA:5,1,0,0")):
                    with self.subTest(location=new), self.assertRaisesRegex(ValueError, "outside source"):
                        self.lcov(NODE_LCOV.replace(old, new))

    def test_native_lcov_duplicate_rows_records_and_inconsistent_summaries_are_refused(self):
        changes = [("DA:1,1\n", "DA:1,1\nDA:1,1\n"),
                   ("BRDA:2,0,0,1\n", "BRDA:2,0,0,1\nBRDA:2,0,0,1\n"),
                   ("FNF:2\n", "FNF:2\nFNF:2\n"), ("FNH:1\n", ""),
                   ("FNDA:0,same", "FNDA:0,unknown"),
                   ("LF:4", "LF:3"), ("LH:2", "LH:999"),
                   ("FNF:2", "FNF:3"), ("FNH:1", "FNH:3"), ("FNH:1", "FNH:0"),
                   ("BRF:2", "BRF:3"), ("BRH:1", "BRH:3"), ("BRH:1", "BRH:0")]
        for old, new in changes:
            with self.subTest(old=old, new=new), self.assertRaises(ValueError):
                self.lcov(NODE_LCOV.replace(old, new))
        with self.assertRaisesRegex(ValueError, "duplicate native LCOV source"):
            self.lcov(NODE_LCOV + NODE_LCOV)
        with self.assertRaisesRegex(ValueError, "unexpected.*source"):
            self.lcov(declared={"documentation/scripts/unobserved.mjs"})

    def test_native_lcov_requires_observed_lines_beyond_consistent_summaries(self):
        zero = ("TN:\nSF:documentation/scripts/unobserved.mjs\nFNF:0\nFNH:0\n"
                "BRF:0\nBRH:0\nDA:1,0\nLH:0\nLF:1\nend_of_record\n")
        self.assertEqual(self.lcov(zero), zero)
        covered = zero.replace("DA:1,0", "DA:1,1").replace("LH:0", "LH:1")
        self.assertEqual(self.lcov(covered), covered)
        # Removing the only measurement leaves all six scalar summaries intact.
        summaries_only = covered.replace("DA:1,1\n", "")
        with self.assertRaisesRegex(ValueError, "line inventory is outside source"):
            self.lcov(summaries_only)

    def test_scan_preparation_preserves_legacy_and_disjoint_native_coverage(self):
        python = self.receipt()
        self.publish(python, "python.json")
        node = self.receipt("javascript")
        self.publish(node, "node.json")
        docs_raw = ("TN:\nSF:documentation/scripts/unobserved.mjs\nFNF:0\nFNH:0\n"
                    "BRF:0\nBRH:0\nDA:1,0\nLH:0\nLF:1\nend_of_record\n").encode()
        docs = copy.deepcopy(node)
        docs["profile"] = "node-docs-unit"
        docs["reports"][0].update(path="reports/docs.info", sha256=digest(docs_raw))
        (self.root / "reports/docs.info").write_bytes(docs_raw)
        self.publish(docs, "docs.json")
        # The manifests overlap completely, while their measured SF sets do not.
        self.assertEqual(node["sourceFiles"], docs["sourceFiles"])
        (self.snapshot / "internal").mkdir()
        (self.snapshot / "internal/a.go").write_text("package a\n")
        (self.snapshot / "catalog-ui/src").mkdir()
        (self.snapshot / "catalog-ui/src/a.ts").write_text("export const value = 1;\n")
        go = ("mode: atomic\n" + sonar.MODULE + "internal/a.go:1.1,1.5 1 2\n").encode()
        legacy_js = b"TN:\nSF:src/a.ts\nDA:1,3\nend_of_record\n"
        (self.root / "reports/go.out").write_bytes(go)
        (self.root / "reports/frontend.info").write_bytes(legacy_js)
        environment = {"SONAR_GO_COVERAGE": "reports/go.out", "SONAR_JS_COVERAGE": "reports/frontend.info",
                       "SONAR_TOOLING_REPORTS": "python.json node.json docs.json"}
        source_before = {path.relative_to(self.snapshot).as_posix(): path.read_bytes()
                         for path in self.snapshot.rglob("*") if path.is_file()}
        with mock.patch.object(sonar, "ROOT", self.root), mock.patch.dict(sonar.os.environ, environment, clear=True):
            properties, evidence = sonar.coverage_inputs(self.snapshot, self.artifacts)
        paths = {key: value.split(",") for key, value in (line.split("=", 1) for line in properties.splitlines())}
        self.assertEqual(paths, {
            "sonar.go.coverage.reportPaths": ["/coverage/coverage-go.out"],
            "sonar.javascript.lcov.reportPaths": ["/coverage/coverage-js.info", "/coverage/coverage-tooling-1.info", "/coverage/coverage-tooling-2.info"],
            "sonar.python.coverage.reportPaths": ["/coverage/coverage-tooling-0.xml"],
        })
        self.assertEqual({path.relative_to(self.snapshot).as_posix(): path.read_bytes()
                          for path in self.snapshot.rglob("*") if path.is_file()}, source_before)
        artifact_names = {Path(path).name for values in paths.values() for path in values}
        self.assertEqual({path.name for path in self.artifacts.iterdir()}, artifact_names)
        self.assertTrue(all(not (self.snapshot / name).exists() for name in artifact_names))
        self.assertFalse(list(self.snapshot.rglob("*.xml")))
        self.assertEqual((self.artifacts / "coverage-go.out").read_text(), "mode: atomic\ninternal/a.go:1.1,1.5 1 1\n")
        self.assertEqual((self.artifacts / "coverage-js.info").read_text(), legacy_js.decode().replace("SF:src/", "SF:catalog-ui/src/"))
        self.assertEqual((self.artifacts / "coverage-tooling-1.info").read_text(), NODE_LCOV)
        self.assertEqual((self.artifacts / "coverage-tooling-2.info").read_bytes(), docs_raw)
        self.assertEqual(ET.parse(self.artifacts / "coverage-tooling-0.xml").find(".//class").get("filename"), "test/helper.py")
        self.assertEqual(evidence["go"]["sha256"], digest(go))
        self.assertEqual(evidence["js"]["sha256"], digest(legacy_js))
        self.assertEqual([item["profile"] for item in evidence["tooling"]], ["python-unit", "node-ui-unit", "node-docs-unit"])
        self.assertTrue(all("raw" not in item and "declaredSources" not in item for item in evidence["tooling"]))
        json.dumps(evidence)  # Scanner report metadata stays JSON-serializable.

    def test_scan_preparation_refuses_overlapping_measured_native_and_legacy_sources(self):
        node = self.receipt("javascript")
        self.publish(node, "node.json")
        second = copy.deepcopy(node)
        second["profile"] = "node-docs-unit"
        second["reports"][0]["path"] = "reports/second.info"
        (self.root / "reports/second.info").write_text(NODE_LCOV)
        self.publish(second, "second.json")
        for environment in ({"SONAR_TOOLING_REPORTS": "node.json second.json"},
                            {"SONAR_JS_COVERAGE": "reports/second.info", "SONAR_TOOLING_REPORTS": "node.json"}):
            with self.subTest(environment=environment), mock.patch.object(sonar, "ROOT", self.root), \
                    mock.patch.dict(sonar.os.environ, environment, clear=True), \
                    self.assertRaisesRegex(sonar.Failure, "sources overlap"):
                sonar.coverage_inputs(self.snapshot, self.artifacts)

    def test_scan_preparation_does_not_treat_opaque_function_names_as_source_records(self):
        docs_raw = ("TN:\nSF:documentation/scripts/unobserved.mjs\nFNF:0\nFNH:0\n"
                    "BRF:0\nBRH:0\nDA:1,0\nLH:0\nLF:1\nend_of_record\n").encode()
        frontend = self.snapshot / "catalog-ui/src/a.ts"
        frontend.parent.mkdir()
        frontend.write_bytes(self.files["catalog-ui/scripts/helper.mjs"])
        for first in ("legacy", "native"):
            node = self.receipt("javascript")
            docs = copy.deepcopy(node)
            docs["profile"] = "node-docs-unit"
            docs["reports"][0].update(path="reports/docs.info", sha256=digest(docs_raw))
            (self.root / "reports/docs.info").write_bytes(docs_raw)
            self.publish(docs, "docs.json")
            environment = {"SONAR_TOOLING_REPORTS": "node.json docs.json"}
            if first == "native":
                raw = NODE_LCOV.replace("same", "opaque\u0085SF:documentation/scripts/unobserved.mjs").encode()
                node["reports"][0]["sha256"] = digest(raw)
                (self.root / node["reports"][0]["path"]).write_bytes(raw)
                self.publish(node, "node.json")
                expected = {"coverage-tooling-0.info": raw, "coverage-tooling-1.info": docs_raw}
            else:
                self.publish(node, "node.json")
                raw = NODE_LCOV.replace("catalog-ui/scripts/helper.mjs", "catalog-ui/src/a.ts").replace(
                    "same", "opaque\u0085SF:catalog-ui/scripts/helper.mjs").encode()
                (self.root / "reports/frontend.info").write_bytes(raw)
                environment["SONAR_JS_COVERAGE"] = "reports/frontend.info"
                expected = {"coverage-js.info": raw, "coverage-tooling-0.info": NODE_LCOV.encode(),
                            "coverage-tooling-1.info": docs_raw}
            with self.subTest(first=first), mock.patch.object(sonar, "ROOT", self.root), \
                    mock.patch.dict(sonar.os.environ, environment, clear=True):
                properties, evidence = sonar.coverage_inputs(self.snapshot, self.artifacts)
            self.assertEqual(properties, "sonar.javascript.lcov.reportPaths=" + ",".join(
                "/coverage/" + name for name in expected) + "\n")
            self.assertEqual(len(evidence["tooling"]), 2)
            for name, content in expected.items():
                self.assertEqual((self.artifacts / name).read_bytes(), content)

    def test_scan_preparation_accepts_only_one_python_receipt(self):
        first = self.receipt()
        self.publish(first, "first.json")
        second = copy.deepcopy(first)
        second["reports"][0]["path"] = "reports/second.xml"
        (self.root / "reports/second.xml").write_bytes(PYTHON_XML)
        self.publish(second, "second.json")
        with mock.patch.object(sonar, "ROOT", self.root), \
                mock.patch.dict(sonar.os.environ, {"SONAR_TOOLING_REPORTS": "first.json second.json"}, clear=True), \
                self.assertRaisesRegex(sonar.Failure, "one native Python"):
            sonar.coverage_inputs(self.snapshot, self.artifacts)

    def test_scan_preparation_refuses_artifact_directories_inside_snapshot(self):
        self.publish(self.receipt())
        child = self.snapshot / "artifacts"
        child.mkdir()
        alias = self.root / "snapshot-alias"
        alias.symlink_to(self.snapshot, target_is_directory=True)
        source_before = {path.relative_to(self.snapshot).as_posix(): path.read_bytes()
                         for path in self.snapshot.rglob("*") if path.is_file()}
        for artifacts in (self.snapshot, child, alias):
            with self.subTest(artifacts=artifacts.name), mock.patch.object(sonar, "ROOT", self.root), \
                    mock.patch.dict(sonar.os.environ, {"SONAR_TOOLING_REPORTS": "receipt.json"}, clear=True), \
                    self.assertRaisesRegex(sonar.Failure, "outside the analyzed source snapshot"):
                sonar.coverage_inputs(self.snapshot, artifacts)
            self.assertEqual({path.relative_to(self.snapshot).as_posix(): path.read_bytes()
                              for path in self.snapshot.rglob("*") if path.is_file()}, source_before)


if __name__ == "__main__":
    unittest.main()
