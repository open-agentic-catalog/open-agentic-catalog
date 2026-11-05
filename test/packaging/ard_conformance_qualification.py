"""Explicit actual-tool qualification; requires the pinned Python jsonschema package."""

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import threading
import unittest

import ard_conformance


class ActualConformanceTests(unittest.TestCase):
    def setUp(self):
        self.workspace = tempfile.TemporaryDirectory()
        self.addCleanup(self.workspace.cleanup)
        self.root = Path(self.workspace.name)
        self.wrapper = self.root / 'test/packaging/ard_conformance.py'
        self.schema = self.root / ard_conformance.SCHEMA.relative_to(ard_conformance.ROOT)
        for source, target in (
            (Path(ard_conformance.__file__), self.wrapper),
            (ard_conformance.CLI, self.root / ard_conformance.CLI.relative_to(ard_conformance.ROOT)),
            (ard_conformance.SCHEMA, self.schema),
        ):
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(source, target)
        self.document = json.loads((ard_conformance.ROOT / 'api/examples/ard-manifest.json').read_text())
        self.manifest = self.root / 'manifest.json'
        self.manifest.write_text(json.dumps(self.document))

    def run_cli(self, mode, target, env=None):
        result = subprocess.run([sys.executable, str(self.wrapper), mode, str(target)],
                                capture_output=True, text=True, timeout=65, check=False, env=env)
        self.record_result(mode, result)
        return result

    def record_result(self, label, result):
        print(f'\nOwned {label} control: wrapper exit={result.returncode}')
        sys.stdout.write(result.stdout)
        sys.stderr.write(result.stderr)

    def require_registry_result(self, result):
        self.assertEqual(result.returncode, 0, 'checker wrapper did not succeed:\n' + result.stdout + result.stderr)
        self.assertTrue(ard_conformance.registry_result_evidence(result.stdout, 1),
                        'expected exactly one nonempty Registry result')

    def test_valid_and_schema_invalid_documents(self):
        result = self.run_cli('manifest', self.manifest)
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertIn('All 1 entries validate against ArdEntry.', result.stdout)
        # The semantic-only CLI does not reject a numeric version. Schema
        # validation must catch this even when the other entry fields are valid.
        self.document['entries'][0]['version'] = 42
        self.manifest.write_text(json.dumps(self.document))
        result = self.run_cli('manifest', self.manifest)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('ArdManifest validation failed', result.stdout)

    def test_missing_malformed_and_invalid_schemas_fail(self):
        for content in (None, '{', '{"$schema":"https://json-schema.org/draft/2020-12/schema","type":42}'):
            with self.subTest(content=content):
                if content is None:
                    self.schema.unlink()
                else:
                    self.schema.write_text(content)
                result = self.run_cli('manifest', self.manifest)
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn('Validating Manifest', result.stdout)

    def test_validator_execution_failure_cannot_pass(self):
        (self.root / 'sitecustomize.py').write_text(
            'import jsonschema\ndef fail(*args, **kwargs):\n    raise RuntimeError("qualification failure")\n'
            'jsonschema.validate = fail\n'
        )
        result = self.run_cli('manifest', self.manifest, env=dict(os.environ, PYTHONPATH=str(self.root)))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Failed to run JSON Schema validator', result.stdout)
        self.assertIn('did not confirm complete', result.stderr)

    def test_live_modes_against_owned_loopback_fixture(self):
        from jsonschema import Draft202012Validator
        entry = {
            'identifier': 'urn:air:catalog.example.com:qualification:weather-tools',
            'displayName': 'Weather tools',
            'type': 'application/mcp-server-card+json',
            'representativeQueries': ['weather forecast tools', 'Find weather forecast information'],
            'data': {
                '$schema': 'https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json',
                'name': 'com.example/weather-tools', 'description': 'Provides weather forecast tools.',
                'version': '1.0.0',
                'remotes': [{'type': 'streamable-http', 'url': 'https://weather.example.invalid/mcp',
                             'supportedProtocolVersions': ['2025-11-25']}],
            },
        }
        card_schema = json.loads((ard_conformance.ROOT / 'schemas/upstream/mcp/mcp-server-card-v1-experimental-526201b/schema.json').read_text())
        Draft202012Validator({**card_schema, '$ref': '#/$defs/ServerCard'}).validate(entry['data'])
        document = {'entries': [entry]}
        query = {'query': {'text': 'weather forecast tools', 'filter': {'type': ['application/mcp-server-card+json']}},
                 'federation': 'none', 'pageSize': 2}
        explore = {'query': {'text': 'weather'}, 'resultType': {'facets': [{'field': 'type'}]}}
        seen, served = [], []
        state = {'omit': None, 'empty': False}

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_args):
                pass

            def reply(self, value, body=None):
                status = 200 if value is not None else 404
                raw = json.dumps(value).encode()
                seen.append((self.command, self.path, body, status))
                served.append((self.path, json.loads(raw)))
                self.send_response(status)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Content-Length', str(len(raw)))
                self.end_headers()
                self.wfile.write(raw)

            def do_GET(self):
                self.reply({'/.well-known/ard.json': document, '/ard/v1/agents': {'items': [entry]}}.get(self.path))

            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
                item = {**entry, 'score': 100, 'source': base + '/.well-known/ard.json'}
                if state['omit'] is not None:
                    item.pop(state['omit'])
                results = [] if state['empty'] else [item]
                self.reply({'/ard/v1/search': {'results': results},
                            '/ard/v1/explore': {'resultType': 'facets', 'facets': {}}}.get(self.path), body)

        server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            base = f'http://127.0.0.1:{server.server_port}'
            live_env = {**os.environ, **dict.fromkeys(('http_proxy', 'https_proxy', 'all_proxy', 'HTTP_PROXY', 'HTTPS_PROXY', 'ALL_PROXY'), ''),
                        'NO_PROXY': '127.0.0.1', 'no_proxy': '127.0.0.1'}
            registry_requests = [('GET', '/ard/v1/agents', None, 200),
                                 ('POST', '/ard/v1/search', query, 200), ('POST', '/ard/v1/explore', explore, 200)]
            result = subprocess.run(
                ['make', '--no-print-directory', 'conformance-live', 'CATALOG_ENV_FILE=/dev/null',
                 'PYTHON=' + sys.executable, 'ARD_MANIFEST_URL=' + base + '/.well-known/ard.json',
                 'ARD_PUBLISHER_URL=' + base, 'ARD_REGISTRY_URL=' + base + '/ard/v1'],
                cwd=ard_conformance.ROOT, capture_output=True, text=True, timeout=65, check=False, env=live_env,
            )
            self.record_result('nonempty live modes', result)
            self.require_registry_result(result)
            self.assertEqual(result.stdout.count('Manifest validates against ArdManifest.'), 2)
            self.assertEqual(result.stdout.count('All 1 entries validate against ArdEntry.'), 2)
            self.assertEqual(result.stdout.count('Validated with 0 critical specification errors and 0 warnings.'), 3)
            self.assertEqual(seen, [('GET', '/.well-known/ard.json', None, 200)] * 2 + registry_requests)
            search = dict(served)['/ard/v1/search']['results']
            self.assertEqual(len(search), 1)
            self.assertEqual((search[0]['identifier'], search[0]['type']), (entry['identifier'], entry['type']))
            self.assertEqual(search[0]['score'], 100)
            self.assertEqual(search[0]['source'], base + '/.well-known/ard.json')
            print('Owned Registry result: ' + json.dumps({'identifier': entry['identifier'], 'type': entry['type'],
                                                        'count': len(search), 'query': query}, sort_keys=True))

            for field, message in (('score', "Missing required SearchResultItem field 'score'."),
                                   ('source', "Missing required SearchResultItem field 'source'."),
                                   ('identifier', "Result is missing required 'identifier'.")):
                with self.subTest(omitted=field):
                    seen.clear()
                    served.clear()
                    state['omit'] = field
                    result = self.run_cli('registry', base + '/ard/v1', env=live_env)
                    self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
                    self.assertIn('[Result #0] ' + message, result.stdout)
                    self.assertIn('Found 1 critical specification errors.', result.stdout)
                    self.assertTrue(ard_conformance.registry_result_evidence(result.stdout, 1))
                    with self.assertRaisesRegex(AssertionError, 'checker wrapper did not succeed'):
                        self.require_registry_result(result)
                    self.assertEqual(seen, registry_requests)
                    self.assertNotIn(field, dict(served)['/ard/v1/search']['results'][0])

            seen.clear()
            state.update(omit=None, empty=True)
            result = self.run_cli('registry', base + '/ard/v1', env=live_env)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertIn('POST /search returned valid results list (0 items).', result.stdout)
            self.assertIn('Validated with 0 critical specification errors and 0 warnings.', result.stdout)
            self.assertEqual(seen, registry_requests)
            with self.assertRaisesRegex(AssertionError, 'expected exactly one nonempty Registry result'):
                self.require_registry_result(result)
            self.assertTrue(ard_conformance.schema_evidence('registry', result.stdout))
            print('Owned empty control: native pass; expected nonempty result assertion refused.')
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=5)
            self.assertFalse(thread.is_alive(), 'owned Registry listener did not stop')


if __name__ == '__main__':
    unittest.main()
