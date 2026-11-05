"""Bind the pinned checker to one owned, governed MCP publication."""

import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess
import sys

from core import mutate
from federation import Client
from http_client import ClientFailure
from journeys import header
from runner import Failure, read_document, write_document

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('catalog_ard_checker_wrapper', ROOT / 'test/packaging/ard_conformance.py')
ard_conformance = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ard_conformance)

TYPE = 'application/mcp-server-card+json'
DOMAIN = 'ard-qualification.example.com'
QUERY = {'query': {'text': 'weather forecast tools', 'filter': {'type': [TYPE]}}, 'federation': 'none', 'pageSize': 2}
UUID = re.compile(r'[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}')
DIGEST = re.compile(r'sha256:[0-9a-f]{64}')


def digest(raw):
    return 'sha256:' + hashlib.sha256(raw).hexdigest()


def identifier(run_id):
    assert isinstance(run_id, str) and re.fullmatch(r'functional-[0-9a-f]{12}', run_id), 'unowned ARD run'
    return 'urn:air:' + DOMAIN + ':' + run_id + '-ard:weather-tools'


def check_results(document, expected):
    """Expected identity comes from retained fixture authority, never Search."""
    results = document.get('results')
    assert isinstance(results, list) and len(results) == 1, 'expected one owned MCP Search result'
    assert results[0].get('identifier') == expected and results[0].get('type') == TYPE, 'wrong owned MCP identity/type'
    assert not document.get('pageToken') and not document.get('partial'), 'owned MCP Search incomplete'
    return [{'identifier': results[0]['identifier'], 'type': results[0]['type']}]


def native_evidence(mode, result, entries):
    assert result.returncode == 0, 'native ARD checker failed'
    text = result.stdout.decode('utf-8')
    lines = ard_conformance.ANSI.sub('', text).splitlines()
    warnings = [int(m.group(1)) for line in lines
                if (m := re.fullmatch(r'Validated with 0 critical specification errors and ([0-9]+) warnings\.', line))]
    assert warnings == [0], 'native ARD summary incomplete or warnings changed'
    value = {'exit': 0, 'warnings': warnings[0], 'outputDigest': digest(result.stdout + result.stderr)}
    if mode == 'registry':
        assert ard_conformance.registry_result_evidence(text, 1), 'native ARD result count/routes incomplete'
        value['results'] = 1
    else:
        assert ard_conformance.schema_evidence(mode, text), 'native ARD schema evidence incomplete'
        assert lines.count(f'  ✓ All {entries} entries validate against ArdEntry.') == 1, 'native ARD entry count differs'
        value['entries'] = entries
    return value


def valid_evidence(value, run_id):
    try:
        assert isinstance(value, dict) and set(value) == {'runId', 'expected', 'observed', 'query', 'snapshot', 'native', 'restored'}
        assert value['runId'] == run_id and value['query'] == QUERY and value['restored'] is True
        expected = value['expected']
        assert isinstance(expected, dict) and set(expected) == {'identifier', 'type', 'resourceId', 'versionRevisionId', 'listingRevisionId', 'releaseId'}
        assert expected['identifier'] == identifier(run_id) and expected['type'] == TYPE
        assert all(isinstance(expected[k], str) and UUID.fullmatch(expected[k]) for k in ('resourceId', 'versionRevisionId', 'listingRevisionId', 'releaseId'))
        assert value['observed'] == [{'identifier': expected['identifier'], 'type': TYPE}]
        snapshot = value['snapshot']
        assert isinstance(snapshot, dict) and set(snapshot) == {'id', 'etag', 'digest', 'entries', 'stable', 'originalId', 'originalDigest'}
        assert all(isinstance(snapshot[k], str) and UUID.fullmatch(snapshot[k]) for k in ('id', 'originalId'))
        assert snapshot['id'] != snapshot['originalId'] and snapshot['stable'] is True
        assert isinstance(snapshot['etag'], str) and 1 <= len(snapshot['etag']) <= 300
        assert all(isinstance(snapshot[k], str) and DIGEST.fullmatch(snapshot[k]) for k in ('digest', 'originalDigest'))
        assert type(snapshot['entries']) is int and 1 <= snapshot['entries'] <= 1000
        assert isinstance(value['native'], dict) and set(value['native']) == {'manifest', 'publisher', 'registry'}
        for mode, native in value['native'].items():
            field = 'results' if mode == 'registry' else 'entries'
            assert isinstance(native, dict) and set(native) == {'exit', 'warnings', 'outputDigest', field}
            assert type(native['exit']) is int and native['exit'] == 0
            assert type(native['warnings']) is int and native['warnings'] == 0
            assert type(native[field]) is int and native[field] == (1 if mode == 'registry' else snapshot['entries'])
            assert isinstance(native['outputDigest'], str) and DIGEST.fullmatch(native['outputDigest'])
        return True
    except (AssertionError, KeyError, TypeError, ValueError):
        return False


def run():
    env = dict(os.environ)
    suffix = env['CATALOG_FUNCTIONAL_SUFFIX']
    run_id = 'functional-' + suffix
    expected_id = identifier(run_id)
    assert env['CATALOG_FUNCTIONAL_PROFILE'] == 'default-off', 'ARD qualification needs owned default-off profile'
    base = env['CATALOG_FUNCTIONAL_BASE_URL']
    assert re.fullmatch(r'http://127\.0\.0\.1:[0-9]{1,5}', base), 'ARD qualification needs owned loopback origin'
    directory = Path(env['CATALOG_FUNCTIONAL_STATE_DIRECTORY'])
    output = ROOT / 'output/functional' / ('run-' + suffix)
    api, reviewer, public = Client(base, 'local-admin'), Client(base, 'local-reviewer'), Client(base)
    original, _ = public.request('GET', '/.well-known/ard.json', decode_body=False)
    snapshots, _ = api.request('GET', '/api/v1/publication/snapshots?host=127.0.0.1&limit=10')
    assert not snapshots.get('nextCursor'), 'original publication exceeds owned bound'
    active = [s for s in snapshots['items'] if s['state'] == 'active']
    assert len(active) == 1, 'original active publication is ambiguous'
    original_id = active[0]['id']
    namespace, _ = mutate(api, 'POST', '/api/v1/namespaces', {'slug': run_id + '-ard', 'displayName': 'Owned ARD checker'}, 201)
    publisher, ph = mutate(api, 'POST', '/api/v1/publishers', {'displayName': 'Owned ARD checker', 'domains': [DOMAIN]}, 201)
    mutate(api, 'POST', '/api/v1/publishers/' + publisher['id'] + '/publication-domains/' + DOMAIN + ':approve',
           {'reason': 'Owned ARD fixture domain'}, etag=header(ph, 'ETag'))
    resource, _ = mutate(api, 'POST', '/api/v1/resources', {'namespaceId': namespace['id'], 'kind': 'mcp_server', 'canonicalIdentifier': expected_id}, 201)
    card = read_document(ROOT / 'samples/cards/order-mcp.json')
    card.update(name='com.example/weather-tools', title='Weather forecast tools', description='Weather forecast tools for discovery qualification.')
    card['remotes'][0]['url'] = 'https://weather.example.invalid/mcp'
    version, _ = mutate(api, 'POST', '/api/v1/resources/' + resource['id'] + '/versions', {'version': '1.0.0',
        'representations': [{'role': 'primary', 'mediaType': TYPE, 'data': card}]}, 201)
    listing, lh = mutate(api, 'POST', '/api/v1/catalog-listings', {'namespaceId': namespace['id'], 'resourceId': resource['id']}, 201)
    revision, lh = mutate(api, 'POST', '/api/v1/catalog-listings/' + listing['id'] + '/revisions', {
        'versionRevisionId': version['revision']['id'], 'publisherId': publisher['id'], 'visibility': 'public',
        'displayName': 'Weather forecast tools', 'description': 'Weather forecast tools for owned ARD qualification.',
        'representativeQueries': ['weather forecast tools', 'Find weather forecast information'], 'license': 'Apache-2.0'}, 201, header(lh, 'ETag'))
    submission, sh = mutate(api, 'POST', '/api/v1/catalog-listings/' + listing['id'] + '/submissions', {}, 201)
    mutate(reviewer, 'POST', '/api/v1/review-submissions/' + submission['id'] + ':approve', {'reason': 'Independent ARD fixture review'}, etag=header(sh, 'ETag'))
    published, _ = mutate(api, 'POST', '/api/v1/catalog-listings/' + listing['id'] + ':publish', {'submissionId': submission['id']}, 201, header(lh, 'ETag'))
    release = published['release']
    activated = False
    try:
        # Read retained authority before any public Search result.
        retained, _ = api.request('GET', '/api/v1/catalog/resources/' + release['id'])
        assert retained['resource']['id'] == resource['id'] and retained['release']['id'] == release['id']
        assert retained['resource']['canonicalIdentifier'] == expected_id and retained['resource']['kind'] == 'mcp_server'
        assert retained['versionRevision']['id'] == version['revision']['id'] and retained['listingRevision']['id'] == revision['revision']['id']
        candidate, _ = mutate(api, 'POST', '/api/v1/publication/snapshots', {'host': '127.0.0.1'}, 201)
        current, _ = mutate(api, 'POST', '/api/v1/publication/snapshots/' + candidate['id'] + ':activate',
                            {'host': '127.0.0.1', 'reason': 'Owned ARD checker publication'}, etag=candidate['etag'])
        activated = True
        raw, headers = public.request('GET', '/.well-known/ard.json', decode_body=False)
        assert header(headers, 'ETag') == current['etag'], 'ARD publication validator differs'
        entries = json.loads(raw)['entries']
        assert len(entries) == len(json.loads(original)['entries']) + 1 and len(entries) <= 1000
        assert len([e for e in entries if e.get('identifier') == expected_id and e.get('type') == TYPE]) == 1
        listed, _ = public.request('GET', '/ard/v1/agents')
        assert any(e.get('identifier') == expected_id for e in listed['items']), 'ARD List omitted owned MCP'
        search, _ = public.request('POST', '/ard/v1/search', QUERY)
        observed = check_results(search, expected_id)
        public.request('POST', '/ard/v1/explore', {'query': {'text': 'weather'}, 'resultType': {'facets': [{'field': 'type'}]}})
        native = {}
        child_env = {**env, **dict.fromkeys(('http_proxy', 'https_proxy', 'all_proxy', 'HTTP_PROXY', 'HTTPS_PROXY', 'ALL_PROXY'), ''), 'NO_PROXY': '127.0.0.1'}
        for mode, target in (('manifest', base + '/.well-known/ard.json'), ('publisher', base), ('registry', base + '/ard/v1')):
            result = subprocess.run([sys.executable, '-B', str(ROOT / 'test/packaging/ard_conformance.py'), mode, target],
                                    capture_output=True, timeout=65, check=False, env=child_env)
            log = output / ('ard-' + mode + '.log')
            log.write_bytes(result.stdout + result.stderr)
            log.chmod(0o600)
            native[mode] = native_evidence(mode, result, len(entries))
        after, ah = public.request('GET', '/.well-known/ard.json', decode_body=False)
        assert after == raw and header(ah, 'ETag') == current['etag'], 'publication changed during native checks'
        stable, _ = api.request('GET', '/api/v1/publication/snapshots/' + current['id'] + '?host=127.0.0.1')
        assert stable['state'] == 'active' and stable['etag'] == current['etag'], 'checker snapshot is no longer active'
        evidence = {'runId': run_id, 'query': QUERY, 'expected': {'identifier': expected_id, 'type': TYPE,
            'resourceId': resource['id'], 'versionRevisionId': version['revision']['id'], 'listingRevisionId': revision['revision']['id'], 'releaseId': release['id']},
            'observed': observed, 'snapshot': {'id': current['id'], 'etag': current['etag'], 'digest': digest(raw), 'entries': len(entries),
            'stable': True, 'originalId': original_id, 'originalDigest': digest(original)}, 'native': native, 'restored': False}
    finally:
        _, rh = api.request('GET', '/api/v1/listing-releases/' + release['id'])
        mutate(api, 'POST', '/api/v1/listing-releases/' + release['id'] + ':retire', {'reason': 'Finish owned ARD checker'}, etag=header(rh, 'ETag'))
        if activated:
            prior, _ = api.request('GET', '/api/v1/publication/snapshots/' + original_id + '?host=127.0.0.1')
            restored, _ = mutate(api, 'POST', '/api/v1/publication/snapshots/' + original_id + ':rollback',
                                {'host': '127.0.0.1', 'reason': 'Restore original smoke publication'}, etag=prior['etag'])
            assert restored['id'] == original_id and restored['state'] == 'active'
        restored_raw, _ = public.request('GET', '/.well-known/ard.json', decode_body=False)
        assert restored_raw == original, 'original smoke publication was not restored'
    evidence['restored'] = True
    assert valid_evidence(evidence, run_id), 'incomplete ARD qualification evidence'
    write_document(directory / 'ard-conformance.json', evidence)
    print('Owned ARD checker passed: exact MCP, native count, stable publication and original snapshot restored.')


if __name__ == '__main__':
    try:
        run()
    except (AssertionError, Failure, ClientFailure, OSError, KeyError, ValueError, subprocess.SubprocessError):
        print('Owned ARD qualification failed; inspect private native logs.', file=sys.stderr)
        raise SystemExit(1) from None
