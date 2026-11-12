"""Pinned Node report admission and publication; no process or network."""
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
from test_unit_runner import core


class CoverageUnits(unittest.TestCase):
    def fixture(self, root):
        ext = root / "extension"
        for directory in (ext / "src", ext / "dist/test", ext / "test"):
            directory.mkdir(parents=True)
        module = ext / "dist/test/core.js"
        module.write_text("export const one = 1;\n")
        (ext / "src/core.ts").write_text("export const one = 1;\n")
        tests = [ext / "test/a.unit.test.mjs", ext / "test/b.unit.test.mjs"]
        for test in tests:
            test.write_text("// independent selected input\n")
        xml = '<testsuites>' + ''.join(f'<testcase name="case {i}" file="{test}" />' for i, test in enumerate(tests)) + '</testsuites>'
        lcov = f"TN:\nSF:{module}\nFN:1,one\nFNDA:1,one\nFNF:1\nFNH:1\nBRDA:1,0,0,1\nBRF:1\nBRH:1\nDA:1,1\nLF:1\nLH:1\nend_of_record\n"
        return ext, module, tests, xml, lcov

    def test_report_admission_requires_each_selection_and_consistent_unique_coverage(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            _, module, tests, xml, lcov = self.fixture(root)
            invalid = [
                ("", lcov), ("<broken", lcov), (xml.replace('testsuites', 'unknown'), lcov),
                (xml.replace(' />', '><skipped type="todo" /></testcase>', 1), lcov),
                (xml.replace(' />', '><failure /></testcase>', 1), lcov),
                (xml.replace(str(tests[1]), str(tests[0])), lcov),
                (xml.replace(f'<testcase name="case 1" file="{tests[1]}" />', ''), lcov),
                (xml.replace(str(tests[1]), str(root / 'unselected.mjs')), lcov),
                (xml.replace('case 1', 'case 0').replace(str(tests[1]), str(tests[0])), lcov),
                (xml.replace('case 0',str(tests[0])), lcov),
                (xml, ''), (xml, lcov + lcov), (xml, lcov.replace('end_of_record\n', '')),
                (xml, lcov.replace(str(module), str(root / 'unexpected.js'))),
                (xml, lcov.replace('LF:1', 'LF:2')), (xml, lcov.replace('DA:1,1\n', 'DA:1,1\nDA:1,1\n')),
                (xml, lcov.replace('FNDA:1', 'FNDA:-1')), (xml, lcov.replace('BRH:1', 'BRH:0')),
                (xml, lcov.replace('FN:1,one', 'FN:not-a-function')),
                (xml, lcov.replace('FNDA:1,one', 'FNDA:1')),
                (xml, lcov.replace('FN:1,one', 'FN:1,one\nFN:1,one')),
                (xml, lcov.replace('FNDA:1,one', 'FNDA:1,one\nFNDA:1,one').replace('FNF:1','FNF:2').replace('FNH:1','FNH:2')),
                (xml, lcov.replace('BRDA:1,0,0,1','BRDA:1,0,0,1\nBRDA:1,0,0,1').replace('BRF:1','BRF:2').replace('BRH:1','BRH:2')),
                (xml, lcov + 'unrecognized\n'),
            ]
            with patch.object(core, "ROOT", root):
                for junit, profile in invalid:
                    with self.subTest(junit=junit[:25], profile=profile[-35:]):
                        (root / 'tests.xml').write_text(junit)
                        (root / 'lcov.info').write_text(profile)
                        with self.assertRaisesRegex(RuntimeError, 'Invalid or incomplete'):
                            core.validate_coverage_reports(root, tests, [module])
                (root / 'tests.xml').write_text(xml)
                (root / 'lcov.info').write_text(lcov)
                actual = core.validate_coverage_reports(root, tests, [module])
                self.assertEqual(actual['tests'], 2)
                self.assertEqual(actual['counts'], {'LF':1, 'LH':1, 'FNF':1, 'FNH':1, 'BRF':1, 'BRH':1})
                leaf = f'<testcase name="same child" file="{tests[0]}"/>'
                nested = '<testsuites><testsuite name="first.outer"><testsuite name="middle">' + leaf + '</testsuite></testsuite><testsuite name="first"><testsuite name="outer.middle">' + leaf + '</testsuite></testsuite></testsuites>'
                (root / 'tests.xml').write_text(nested)
                self.assertEqual(core.validate_coverage_reports(root,[tests[0]],[module])['tests'],2)
                (root / 'tests.xml').write_text(nested.replace(leaf,leaf+leaf,1))
                with self.assertRaisesRegex(RuntimeError, 'Invalid or incomplete'):
                    core.validate_coverage_reports(root,[tests[0]],[module])
                (root / 'tests.xml').write_text(xml)
                repeated = lcov.replace('FN:1,one', 'FN:1,one\nFN:2,one').replace('FNDA:1,one', 'FNDA:1,one\nFNDA:0,one').replace('FNF:1','FNF:2')
                (root / 'lcov.info').write_text(repeated)
                self.assertEqual(core.validate_coverage_reports(root,tests,[module])['counts']['FNF'],2)
                with self.assertRaisesRegex(RuntimeError, 'Invalid or incomplete'):
                    core.validate_coverage_reports(root, tests, [module, root / 'unloaded.js'])
                with self.assertRaisesRegex(RuntimeError, 'Invalid or incomplete'):
                    core.validate_coverage_reports(root, [], [module])

    def test_publication_invalidates_previous_success_and_publishes_only_valid_bound_reports(self):
        for failure in (None, 'snapshot', 'inputs', 'process', 'skipped', 'missing', 'source'):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as directory:
                root = Path(directory).resolve()
                ext, _, _, xml, lcov = self.fixture(root)
                destination = root / 'output/vscode/unit-coverage'
                destination.mkdir(parents=True)
                (destination / 'tests.xml').write_text('old tests')
                (destination / 'lcov.info').write_text('old coverage')
                (destination / 'manifest.json').write_text('{"status":"passed"}')
                def run(args, **_):
                    current = json.loads((destination / 'manifest.json').read_text())
                    self.assertEqual(current['status'], 'running')
                    junit = next(Path(str(args[i+1]).split('=',1)[1]) for i, arg in enumerate(args) if arg == '--test-reporter=junit')
                    coverage = next(Path(str(args[i+1]).split('=',1)[1]) for i, arg in enumerate(args) if arg == '--test-reporter=lcov')
                    self.assertNotEqual(junit.parent, destination)
                    self.assertEqual(junit.parent, coverage.parent)
                    if failure == 'process':
                        junit.write_text('partial')
                        raise RuntimeError('process refused')
                    if failure != 'missing':
                        junit.write_text(xml if failure != 'skipped' else xml.replace(' />', '><skipped /></testcase>', 1))
                        coverage.write_text(lcov)
                with patch.object(core, 'ROOT', root), patch.object(core, 'EXTENSION', ext), patch.object(core, 'source_hashes', return_value={}, side_effect=RuntimeError('snapshot refused') if failure=='snapshot' else None), patch.object(core, 'inputs', return_value={}, side_effect=RuntimeError('input refused') if failure=='inputs' else None), patch.object(core, 'run', side_effect=run), patch.object(core, 'assert_sources', side_effect=RuntimeError('source changed') if failure == 'source' else None):
                    if failure:
                        with self.assertRaises(RuntimeError):
                            core.coverage({'node':'inert-node'}, 'unit')
                    else:
                        core.coverage({'node':'inert-node'}, 'unit')
                manifest = json.loads((destination / 'manifest.json').read_text())
                self.assertEqual(manifest['status'], 'failed' if failure else 'passed')
                self.assertEqual(list(destination.parent.glob('.unit-coverage-*')), [])
                if failure:
                    self.assertEqual((destination / 'tests.xml').read_text(), 'old tests')
                    self.assertEqual((destination / 'lcov.info').read_text(), 'old coverage')
                else:
                    self.assertEqual(manifest['results']['tests'], 2)
                    self.assertEqual(manifest['reports'], {name:core.digest(destination/name) for name in ('tests.xml','lcov.info')})


if __name__ == '__main__':
    unittest.main()
