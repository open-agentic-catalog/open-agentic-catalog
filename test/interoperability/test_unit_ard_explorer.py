"""Explorer boundary decisions are isolated from browsers and native tools."""

import copy
import importlib.util
import json
import sys
import tempfile
from collections import Counter
from pathlib import Path
import unittest
from unittest import mock
from types import SimpleNamespace

import ard_explorer as explorer


def load_controls():
    spec = importlib.util.spec_from_file_location('explorer_fixture', explorer.INPUTS / 'fixture.py')
    fixture = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(fixture)
    spec = importlib.util.spec_from_file_location('explorer_confined', explorer.INPUTS / 'confined.py')
    confined = importlib.util.module_from_spec(spec)
    with mock.patch.dict(sys.modules, fixture=fixture):
        spec.loader.exec_module(confined)
    return fixture, confined


def browsing_evidence():
    kinds = ('agent', 'mcp_server', 'skill')
    ids = ['urn:air:fixture.example:resource:' + kind for kind in kinds]
    fields = {'manifest-connect': {'requests': 4}, 'registry-connect': {'requests': 3},
              'search-pagination': {'pages': [{'identifier': identifier, 'score': 75, 'token': str(i) if i < 2 else None}
                                             for i, identifier in enumerate(ids)]},
              'changed-filter-token': {'status': 400}, 'filter-empty': {'results': 0}, 'filter-match': {'identifier': ids[1]},
              'explore-counts': {'buckets': [{'value': k, 'count': 1} for k in kinds]},
              'list-first-page': {'identifier': ids[0]}, 'list-continuation': {'receivedToken': True, 'nextControl': False},
              'private-authentication': {'statuses': [401, 401, 401], 'authorization': False},
              'sparse-unfamiliar': {'results': 3}, 'malformed-envelope': {'status': 200, 'rejectedEnvelope': True},
              'http-400': {'outerStatus': 200, 'status': 400}, 'http-401': {'outerStatus': 200, 'status': 401},
              'http-503': {'outerStatus': 200, 'status': 503}, 'query-context': {'status': 400, 'queryContext': False},
              'full-referral-connect': {'statuses': [404, 404, 404], 'searchURL': 'http://127.0.0.1:12345/referral-target/search/search'},
              'target-unavailable': {'outerStatus': 200, 'transportFailed': True}}
    limits = {'list-continuation', 'private-authentication', 'query-context', 'full-referral-connect'}
    return {'outcomes': [{'name': name, 'disposition': 'client-limitation' if name in limits else 'supported',
                          'status': 'pass', 'evidence': evidence} for name, evidence in fields.items()],
            'proxyOperations': 38, 'exchanges': [{} for _ in range(38)], 'snapshotStable': True, 'originalETag': '"fixture"',
            'catalogCounters': [{'count': 1}], 'fixtureObservations': [{'path': '/search'}],
            'resources': [{'kind': kind, 'identifier': identifier} for kind, identifier in zip(kinds, ids, strict=True)]}


class ExplorerTests(unittest.TestCase):
    def test_cleanup_failure_cannot_leave_a_passing_report(self):
        for failure in ('scope', 'files', None):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as temporary:
                directory = Path(temporary)
                (directory / 'tmp').mkdir()
                for name in ('catalogd', 'catalogctl', 'config.json'):
                    (directory / name).write_text('synthetic')
                report = {'status': 'passed', 'cleanup': 'pending'}
                with mock.patch.object(explorer, 'stop_scope', side_effect=RuntimeError('scope remains') if failure == 'scope' else None), \
                        mock.patch.object(explorer.shutil, 'rmtree', wraps=explorer.shutil.rmtree,
                                          side_effect=OSError('filesystem cleanup failed') if failure == 'files' else None):
                    if failure:
                        with self.assertRaises((RuntimeError, OSError)):
                            explorer.cleanup_run(directory, 'owned.service', report)
                    else:
                        explorer.cleanup_run(directory, 'owned.service', report)
                retained = json.loads((directory / 'result.json').read_text())
                self.assertEqual(retained, {'status': 'failed', 'cleanup': 'failed'} if failure else {'status': 'passed', 'cleanup': 'passed'})
                if failure is None:
                    self.assertFalse((directory / 'tmp').exists())
                    self.assertFalse((directory / 'config.json').exists())

    def test_scanner_must_cover_every_locked_build_package(self):
        lock = {'packages': {'': {}, 'node_modules/esbuild': {'version': '0.24.2'}, 'node_modules/react': {'version': '18.3.1'}}}
        report = {'SchemaVersion': 2, 'Results': [{'Type': 'npm', 'Target': 'package-lock.json', 'Packages': [
            {'Name': 'esbuild', 'Version': '0.24.2'}, {'Name': 'react', 'Version': '18.3.1'}]}]}
        self.assertEqual(explorer.validate_scan(report, lock), [])
        for index in range(2):
            bad = copy.deepcopy(report)
            del bad['Results'][0]['Packages'][index]
            with self.assertRaises(ValueError):
                explorer.validate_scan(bad, lock)
        known = {'VulnerabilityID': 'GHSA-67mh-4wv8-2f99', 'PkgName': 'esbuild', 'InstalledVersion': '0.24.2'}
        report['Results'][0]['Vulnerabilities'] = [known]
        self.assertEqual(explorer.validate_scan(report, lock), [known])
        for field, value in [('VulnerabilityID', 'another-advisory'), ('PkgName', 'react'), ('InstalledVersion', '0.24.1')]:
            bad = copy.deepcopy(report)
            bad['Results'][0]['Vulnerabilities'][0][field] = value
            with self.assertRaises(ValueError):
                explorer.validate_scan(bad, lock)
        for value in ({}, {'SchemaVersion': 2, 'Results': []}):
            with self.assertRaises(ValueError):
                explorer.validate_scan(value, lock)

    def test_environment_never_inherits_credentials_or_npm_configuration(self):
        value = explorer.environment(Path('/owned/home'))
        self.assertEqual(set(value), {'PATH', 'HOME', 'LANG', 'npm_config_userconfig', 'npm_config_globalconfig',
                                    'npm_config_audit', 'npm_config_fund', 'npm_config_ignore_scripts'})
        self.assertEqual(value['npm_config_userconfig'], '/dev/null')
        self.assertEqual(value['npm_config_ignore_scripts'], 'true')

    def test_sandbox_has_no_host_network_home_or_credential_mount(self):
        args = explorer.sandbox(Path('/owned/state'), {'executable': '/cache/chromium-1248/chrome-linux64/chrome'})
        self.assertIn('--unshare-net', args)
        self.assertIn('--unshare-pid', args)
        self.assertIn('--clearenv', args)
        self.assertIn('--die-with-parent', args)
        self.assertNotIn('--share-net', args)
        self.assertNotIn('/', [args[i + 1] for i, arg in enumerate(args) if arg in ('--bind', '--ro-bind')])
        binds = {args[i + 2]: (arg, args[i + 1]) for i, arg in enumerate(args) if arg in ('--bind', '--ro-bind')}
        self.assertEqual({key for key, value in binds.items() if value[0] == '--bind'}, {'/state', '/tmp'})
        self.assertEqual(binds['/explorer'][0], '--ro-bind')
        self.assertEqual(binds['/browser/node_modules'][0], '--ro-bind')
        self.assertTrue({'/home', '/root', '/var/run/docker.sock'}.isdisjoint(binds))
        self.assertFalse(any('/.ssh' in arg or '/.npmrc' in arg or 'docker.sock' in arg for arg in args))

    def test_host_home_or_docker_visibility_refuses_before_network_or_process_work(self):
        _, confined = load_controls()

        class RouteCheckReached(Exception):
            pass

        for exposed in (None, '/home', '/root', '/var/run/docker.sock'):
            with self.subTest(exposed=exposed), mock.patch.dict(confined.os.environ, HOME='/tmp/home', clear=True), \
                    mock.patch.object(confined.Path, 'exists', autospec=True, side_effect=lambda p: str(p) == exposed), \
                    mock.patch.object(confined.Path, 'read_text', side_effect=RouteCheckReached) as read, \
                    mock.patch.object(confined.socket, 'socket') as connect, \
                    mock.patch.object(confined.subprocess, 'Popen') as start:
                if exposed is None:
                    # The valid control passes the path guard and stops at the
                    # next boundary, without reading routes or opening a socket.
                    with self.assertRaises(RouteCheckReached):
                        confined.run()
                    read.assert_called_once()
                else:
                    with self.assertRaisesRegex(AssertionError, 'unexpected host authority'):
                        confined.run()
                    read.assert_not_called()
                connect.assert_not_called()
                start.assert_not_called()

    def test_service_bounds_and_cleanup_refuse_a_live_scope(self):
        command = explorer.scope_command('owned.service', ['/owned/command'])
        for value in ('RuntimeMaxSec=300', 'MemoryMax=2147483648', 'MemorySwapMax=0', 'CPUQuota=200%',
                      'TasksMax=256', 'KillMode=control-group'):
            self.assertIn(value, command)
        self.assertEqual(command[-1], '/owned/command')
        for state in ('active', 'activating'):
            with self.subTest(state=state), mock.patch.object(explorer, 'run', side_effect=[
                    SimpleNamespace(code=0, stdout='258'), SimpleNamespace(code=0, stdout=''),
                    SimpleNamespace(code=0, stdout=state), SimpleNamespace(code=0, stdout='LoadState=loaded\nControlGroup=')]):
                with self.assertRaisesRegex(RuntimeError, 'remains active'):
                    explorer.stop_scope('owned.service')
        for state, load_state in (('inactive', 'loaded'), ('unknown', 'not-found')):
            with mock.patch.object(explorer, 'run', side_effect=[SimpleNamespace(code=0, stdout='258'),
                    SimpleNamespace(code=0, stdout=''), SimpleNamespace(code=3, stdout=state),
                    SimpleNamespace(code=0, stdout='LoadState=' + load_state + '\nControlGroup=')]):
                explorer.stop_scope('owned.service')

    def test_user_manager_failure_precedes_private_staging(self):
        for result in (SimpleNamespace(code=1, stdout=''), SimpleNamespace(code=0, stdout='')):
            with mock.patch.object(explorer, 'installed'), mock.patch.object(explorer, 'run', return_value=result), \
                 mock.patch.object(explorer.tempfile, 'mkdtemp') as stage, mock.patch.object(explorer, 'build_binaries') as build:
                with self.assertRaisesRegex(RuntimeError, 'available systemd user manager'):
                    explorer.check(browsing=True)
                stage.assert_not_called()
                build.assert_not_called()

    def test_failed_cleanup_query_is_not_an_active_or_clean_scope(self):
        with mock.patch.object(explorer, 'run', return_value=SimpleNamespace(code=1, stdout='')):
            with self.assertRaisesRegex(RuntimeError, 'cleanup could not be verified: user manager unavailable'):
                explorer.stop_scope('owned.service')
        for state, properties in ((SimpleNamespace(code=1, stdout=''), SimpleNamespace(code=1, stdout='')),
                                  (SimpleNamespace(code=3, stdout='inactive'), SimpleNamespace(code=0, stdout=''))):
            with mock.patch.object(explorer, 'run', side_effect=[SimpleNamespace(code=0, stdout='258'),
                    SimpleNamespace(code=0, stdout=''), state, properties]):
                with self.assertRaisesRegex(RuntimeError, 'could not be verified'):
                    explorer.stop_scope('owned.service')

    def test_supervisor_requires_complete_native_confinement_evidence(self):
        browser = {'version': '156.0.8078.4'}
        value = {'passed': True, 'network': 'loopback-only', 'externalConnectErrno': 101, 'memoryMax': 2147483648,
                 'cpuCores': 2, 'tasksMax': 256, 'swapMax': 0, 'origin': 'http://127.0.0.1:12345/',
                 'browser': {'passed': True, 'proxyOperations': 2, 'externalProxyRefused': True,
                             'externalBrowserRefused': True, 'browserVersion': browser['version'], 'errors': [], 'refused': []}}
        explorer.validate_startup(value, browser)
        cases = [{}, {'passed': True}, {**value, 'cpuCores': 3}, {**value, 'swapMax': False},
                 {**value, 'origin': 'http://example.com/'}, {**value, 'origin': 'http://127.0.0.1:12345/?external=yes'}]
        for field, changed in [('passed', False), ('proxyOperations', 0), ('externalProxyRefused', False),
                               ('externalBrowserRefused', False), ('browserVersion', 'other'), ('errors', ['error']), ('refused', ['external'])]:
            cases.append({**value, 'browser': {**value['browser'], field: changed}})
        for case in cases:
            with self.subTest(case=case), self.assertRaises(ValueError):
                explorer.validate_startup(case, browser)

    def test_browsing_report_rejects_incomplete_or_relabelled_evidence(self):
        good = browsing_evidence()
        explorer.validate_browsing(good)
        self.assertEqual(len(good['outcomes']), 18)
        cases = [{}, {**good, 'snapshotStable': False}, {**good, 'catalogCounters': []},
                 {**good, 'fixtureObservations': []}, {**good, 'proxyOperations': 51}, {**good, 'exchanges': []},
                 {**good, 'resources': []}, {**good, 'outcomes': good['outcomes'][:-1]}]
        for case in good['outcomes']:
            for field, changed in [('status', 'fail'), ('disposition', 'invented')]:
                value = copy.deepcopy(good)
                next(c for c in value['outcomes'] if c['name'] == case['name'])[field] = changed
                cases.append(value)
        for name in ('private-authentication', 'query-context', 'full-referral-connect', 'list-continuation'):
            value = copy.deepcopy(good)
            next(c for c in value['outcomes'] if c['name'] == name)['disposition'] = 'supported'
            cases.append(value)
        for field, changed in [('identifier', 'wrong'), ('score', 101), ('token', None)]:
            value = copy.deepcopy(good)
            next(c for c in value['outcomes'] if c['name'] == 'search-pagination')['evidence']['pages'][0][field] = changed
            cases.append(value)
        for case in cases:
            with self.subTest(case=case), self.assertRaises(ValueError):
                explorer.validate_browsing(case)

    def test_independent_counters_reject_hidden_requests_or_credentials(self):
        _, confined = load_controls()
        item = {'url': 'http://127.0.0.1:8080/ard/v1/search', 'method': 'POST', 'body': '{}',
                'authorization': False, 'outerStatus': 200, 'native': {'ok': True, 'status': 200}}
        before = Counter({('GET', '/readyz', '2xx'): 1})
        after = before + Counter({('POST', '/ard/v1/search', '2xx'): 1})
        self.assertEqual(confined.verify_browser({'exchanges': [item]}, [], before, after),
                         [{'method': 'POST', 'route': '/ard/v1/search', 'statusClass': '2xx', 'count': 1}])
        with self.assertRaisesRegex(AssertionError, 'differ'):
            confined.verify_browser({'exchanges': [item]}, [], before, after + Counter({('GET', '/public/v1/artifacts/{artifactId}', '2xx'): 1}))
        with self.assertRaisesRegex(AssertionError, 'decreased'):
            confined.verify_browser({'exchanges': [item]}, [], before, after - before)
        for changed in ({'authorization': True}, {'url': 'http://127.0.0.1:8080/api/v1/resources'}, {'method': 'DELETE'}):
            with self.subTest(changed=changed), self.assertRaises(AssertionError):
                confined.verify_browser({'exchanges': [{**item, **changed}]}, [], before, after)

    def test_fixture_preserves_sparse_unfamiliar_and_valid_neighbors(self):
        fixture, _ = load_controls()
        status, payload = fixture.response('/search', {'query': {'text': 'sparse-unfamiliar'}}, 'http://127.0.0.1:12345')
        self.assertEqual(status, 200)
        self.assertEqual(set(payload['results'][0]), {'identifier'})
        self.assertEqual(payload['results'][1]['type'], 'application/vnd.example.agent+json;profile=v2')
        self.assertEqual(payload['results'][2]['displayName'], 'Valid neighbor')
        for status in (400, 401, 503):
            actual, _ = fixture.response('/search', {'query': {'text': 'http-' + str(status)}}, 'http://127.0.0.1:12345')
            self.assertEqual(actual, status)


if __name__ == '__main__':
    unittest.main()
