"""Owned result and native-output assertions need no tool, binary or database."""

import copy
import contextlib
import io
import json
from pathlib import Path
import shutil
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock

import ard_checker as ard
import runner

RUN = 'functional-012345abcdef'


def evidence(run_id=RUN):
    ids = [f'00000000-0000-7000-8000-{n:012d}' for n in range(1, 7)]
    return {'runId': run_id, 'query': copy.deepcopy(ard.QUERY), 'expected': {
        'identifier': ard.identifier(run_id), 'type': ard.TYPE, 'resourceId': ids[0],
        'versionRevisionId': ids[1], 'listingRevisionId': ids[2], 'releaseId': ids[3]},
        'observed': [{'identifier': ard.identifier(run_id), 'type': ard.TYPE}],
        'snapshot': {'id': ids[4], 'etag': '"snapshot"', 'digest': 'sha256:' + 'a' * 64, 'entries': 4,
                     'stable': True, 'originalId': ids[5], 'originalDigest': 'sha256:' + 'b' * 64},
        'native': {mode: {'exit': 0, 'warnings': 0, 'outputDigest': 'sha256:' + 'c' * 64,
                         ('results' if mode == 'registry' else 'entries'): (1 if mode == 'registry' else 4)}
                   for mode in ('manifest', 'publisher', 'registry')}, 'restored': True}


class ArdCheckerTests(unittest.TestCase):
    def test_fixture_identity_leaves_the_archive_publishers_domain_available(self):
        self.assertEqual(ard.identifier(RUN), 'urn:air:ard-qualification.example.com:functional-012345abcdef-ard:weather-tools')

    def test_search_requires_expected_identity_and_complete_single_result(self):
        item = {'identifier': ard.identifier(RUN), 'type': ard.TYPE}
        self.assertEqual(ard.check_results({'results': [item]}, item['identifier']), [item])
        for value in ({}, {'results': []}, {'results': None}, {'results': [item, item]},
                      {'results': [{**item, 'identifier': ard.identifier('functional-111111111111')}]},
                      {'results': [{'identifier': item['identifier']}]}, {'results': [{**item, 'type': 'text/plain'}]},
                      {'results': [item], 'partial': True}, {'results': [item], 'pageToken': 'more'}):
            with self.subTest(value=value), self.assertRaises(AssertionError):
                ard.check_results(value, item['identifier'])

    def test_native_count_cannot_be_replaced_by_successful_preflight(self):
        routes = ''.join(f'  ✓ {r} responded successfully with 200 OK.\n' for r in ('GET /agents', 'POST /search', 'POST /explore'))
        summary = 'Validated with 0 critical specification errors and 0 warnings.\n'
        count = '  ✓ POST /search returned valid results list (1 items).\n'
        good = SimpleNamespace(returncode=0, stdout=(routes + count + summary).encode(), stderr=b'')
        self.assertEqual(ard.native_evidence('registry', good, 4)['results'], 1)
        for result in (SimpleNamespace(**{**vars(good), 'returncode': 1}),
                       SimpleNamespace(**{**vars(good), 'stdout': good.stdout.replace(b'1 items', b'0 items')}),
                       SimpleNamespace(**{**vars(good), 'stdout': good.stdout.replace(b'1 items', b'2 items')}),
                       SimpleNamespace(**{**vars(good), 'stdout': good.stdout.replace(summary.encode(), b'')}),
                       SimpleNamespace(**{**vars(good), 'stdout': good.stdout + summary.encode()}),
                       SimpleNamespace(**{**vars(good), 'stdout': good.stdout.replace(b'0 warnings', b'1 warnings')}),
                       SimpleNamespace(**{**vars(good), 'stdout': good.stdout.replace(routes.splitlines(True)[0].encode(), b'')})):
            with self.assertRaises(AssertionError):
                ard.native_evidence('registry', result, 4)
        manifest = ('  ✓ Manifest validates against ArdManifest.\n  ✓ All 4 entries validate against ArdEntry.\n' + summary).encode()
        for mode in ('manifest', 'publisher'):
            value = SimpleNamespace(returncode=0, stdout=manifest, stderr=b'')
            self.assertEqual(ard.native_evidence(mode, value, 4)['entries'], 4)
            with self.assertRaises(AssertionError):
                ard.native_evidence(mode, value, 3)

    def test_report_binds_run_fixture_snapshot_and_native_results(self):
        good = evidence()
        self.assertTrue(ard.valid_evidence(good, RUN))
        cases = []
        for field, value in (('runId', 'functional-111111111111'), ('observed', []), ('query', {}),
                             ('expected', None), ('native', ['manifest', 'publisher', 'registry']),
                             ('snapshot', None), ('restored', False)):
            cases.append({**copy.deepcopy(good), field: value})
        for section, field, value in (('expected', 'identifier', 'urn:air:other.test:wrong'),
                                      ('expected', 'resourceId', ''), ('expected', 'type', 'text/plain'),
                                      ('snapshot', 'stable', False), ('snapshot', 'originalId', good['snapshot']['id']),
                                      ('snapshot', 'entries', True), ('snapshot', 'digest', 'bad')):
            item = copy.deepcopy(good)
            item[section][field] = value
            cases.append(item)
        for mode in good['native']:
            for field, value in (('exit', 1), ('warnings', True), ('warnings', 1), ('outputDigest', 'bad'),
                                 (('results' if mode == 'registry' else 'entries'), 0)):
                item = copy.deepcopy(good)
                item['native'][mode][field] = value
                cases.append(item)
        item = copy.deepcopy(good)
        item['observed'][0]['identifier'] = 'wrong'
        cases.append(item)
        item = copy.deepcopy(good)
        del item['native']['registry']
        cases.append(item)
        for item in cases:
            with self.subTest(item=item):
                self.assertFalse(ard.valid_evidence(item, RUN))
        base = {'profile': 'default-off', 'runId': RUN, 'images': {'api': 'owned'},
                'servedVersion': {'version': 'functional'}, 'smoke': {'resourceCount': 3}, 'ardConformance': good}
        self.assertTrue(runner.finalize(copy.deepcopy(base), list(runner.STEPS), '', True))
        for value in (None, *cases):
            self.assertFalse(runner.finalize({**copy.deepcopy(base), 'ardConformance': value}, list(runner.STEPS), '', True))

    def test_completed_evidence_survives_later_failure_and_cleanup(self):
        for corrupt in (False, True):
            with self.subTest(corrupt=corrupt), tempfile.TemporaryDirectory() as directory, mock.patch.object(Path, 'cwd', return_value=Path(directory)):
                instance = runner.Runner({})
                instance.state = Path(directory) / 'state'
                instance.state.mkdir()
                value = evidence(instance.report['runId'])
                if corrupt:
                    value['private'] = 'unreviewed-value'
                runner.write_document(instance.state / 'ard-conformance.json', value)
                def cleanup():
                    shutil.rmtree(instance.state)
                    return True
                with mock.patch.object(runner, 'Runner', return_value=instance), \
                        mock.patch.object(instance, 'execute', side_effect=runner.Failure('child-failed')), \
                        mock.patch.object(instance, 'cleanup', side_effect=cleanup), \
                        mock.patch.object(runner, 'progress', return_value=list(runner.STEPS)[:6]), \
                        mock.patch.object(runner.signal, 'signal'), \
                        contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                    self.assertEqual(runner.run(), 1)
                retained = runner.read_document(instance.report_path)
                self.assertEqual(retained['failureCode'], 'child-failed')
                self.assertEqual(retained['cleanup'], 'passed')
                self.assertFalse(instance.state.exists())
                if corrupt:
                    self.assertNotIn('ardConformance', retained)
                    self.assertNotIn('unreviewed-value', json.dumps(retained))
                else:
                    self.assertEqual(retained['ardConformance'], value)


if __name__ == '__main__':
    unittest.main()
