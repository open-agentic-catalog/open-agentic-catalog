"""Owned YAML -> PostgreSQL -> browser -> selected native clients qualification."""
import hashlib
import json
from pathlib import Path
import re
import shutil
import signal
import socket
import sys
import tempfile
import urllib.request
import uuid

import qualify_runtimes
import runtime_clients

launcher = qualify_runtimes.launcher
ROOT = launcher.ROOT


def extend_yaml(raw):
    """Add one sample by editing YAML only; preserve every prior definition."""
    match = re.search(r'^  - key: hello-world\n.*?(?=^  - key: |\Z)', raw, re.M | re.S)
    if match is None or raw.count('  curated:\n') != 1:
        raise ValueError('missing selected sample definition')
    extra = match.group().replace('key: hello-world\n', 'key: extra-hello-world\n', 1)
    extra = extra.replace(':hello-world\n', ':extra-hello-world\n', 1)
    extra = extra.replace('name: A2A Hello World\n', 'name: Additional Hello World\n', 1)
    edited = raw[:match.end()] + extra + raw[match.end():]
    return edited.replace('  curated:\n', '  curated:\n    - extra-hello-world\n', 1)


class API:
    def __init__(self, port):
        self.base = 'http://127.0.0.1:' + port
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), launcher.runtimes.NoRedirect())

    def exchange(self, method, path, body=None, actor='local-reader', etag=None):
        if not path.startswith(('/api/v1/', '/.well-known/')) or '://' in path:
            raise ValueError('invalid owned API path')
        headers = {'Authorization': 'Bearer ' + actor}
        if body is not None:
            headers.update({'Content-Type': 'application/json', 'Idempotency-Key': 'demo-test-' + uuid.uuid4().hex})
        if etag is not None:
            headers['If-Match'] = etag
        request = urllib.request.Request(self.base + path, data=None if body is None else json.dumps(body).encode(),
                                         headers=headers, method=method)
        with self.opener.open(request, timeout=20) as response:
            raw = response.read(2*1024*1024+1)
            if len(raw) > 2*1024*1024:
                raise ValueError('API test response bound')
            return json.loads(raw), response.headers.get('ETag')

    def get(self, path, actor='local-reader'):
        return self.exchange('GET', path, actor=actor)[0]


def records(api, receipt):
    return {item['key']: api.get('/api/v1/catalog/resources/' + item['releaseId'])
            for item in receipt['items'] if item['state'] == 'published'}


def retained_proof(api, receipt):
    proof = {}
    for item in receipt['items']:
        revision = api.get('/api/v1/version-revisions/' + item['versionRevisionId'], 'local-contributor')['revision']
        proof[item['key']] = {'item': item, 'content': revision['contentSetDigest'],
                              'representations': revision['representations']}
        if item.get('submissionId'):
            proof[item['key']]['evidence'] = api.get('/api/v1/review-submissions/' + item['submissionId'] + '/evidence', 'local-reviewer')
    return proof


def search(api):
    found, cursor, cursors = [], '', set()
    for _ in range(16):
        page, _ = api.exchange('POST', '/api/v1/catalog/search', {'limit': 2, **({'cursor': cursor} if cursor else {})})
        assert not page['partial'] and 0 < len(page['items']) <= 2
        found.extend(item['identifier'] for item in page['items'])
        cursor = page.get('nextCursor', '')
        if not cursor:
            assert len(found) == len(set(found))
            return set(found)
        assert cursor not in cursors
        cursors.add(cursor)
    raise ValueError('discovery pagination bound exceeded')


def unrelated(api, receipt, card):
    """Prepare one independent release through ordinary governance, without a snapshot."""
    def post(path, actor, body, etag=None):
        return api.exchange('POST', '/api/v1/' + path, body, actor, etag)
    resource, _ = post('resources', 'local-contributor', {'namespaceId': receipt['namespaceId'], 'kind': 'agent',
        'canonicalIdentifier': 'urn:air:demo.catalog.example.com:demo:unrelated-local-agent'})
    version, _ = post('resources/' + resource['id'] + '/versions', 'local-contributor', {'version': '1.0.0',
        'representations': [{'role': 'primary', 'mediaType': 'application/a2a-agent-card+json', 'data': card}]})
    listing, etag = post('catalog-listings', 'local-contributor', {'namespaceId': receipt['namespaceId'], 'resourceId': resource['id']})
    _, etag = post('catalog-listings/' + listing['id'] + '/revisions', 'local-contributor', {
        'versionRevisionId': version['revision']['id'], 'displayName': 'Independent local Agent',
        'description': 'Unrelated governed release; the demo loader must preserve it without adopting it.',
        'publisherId': receipt['publisherId'], 'visibility': 'public', 'license': 'Apache-2.0',
        'tags': [], 'representativeQueries': ['Find the independent local Agent', 'Find unrelated governed metadata']}, etag)
    submission, submitted_etag = post('catalog-listings/' + listing['id'] + '/submissions', 'local-contributor', {})
    post('review-submissions/' + submission['id'] + ':approve', 'local-reviewer', {'reason': 'Independent local fixture review'}, submitted_etag)
    result, _ = post('catalog-listings/' + listing['id'] + ':publish', 'local-publisher', {'submissionId': submission['id']}, etag)
    return api.get('/api/v1/catalog/resources/' + result['release']['id'])


def qualify():
    # Validate tools before allocating a project, private keys or containers.
    launcher.runtimes.tools()
    root = ROOT / 'output/demo/qualification'
    root.mkdir(parents=True, exist_ok=True)
    project = 'oac-demo-catalog-' + uuid.uuid4().hex[:12]
    report_file = root / (project + '.json')
    report = {'scope': 'owned-yaml-postgres-browser-native-handoff', 'project': project, 'success': False, 'cleanup': False}
    environment = launcher.isolated_environment()
    phase = 'prepare'
    with tempfile.TemporaryDirectory(prefix=project + '-', dir=root) as temporary:
        private = Path(temporary)
        shutil.copytree(ROOT / 'samples', private / 'samples')
        ports = set()
        while len(ports) < 6:
            ports.add(qualify_runtimes.unused_port())
        api_port, ui_port, db_port, archive_port, agent_port, mcp_port = sorted(ports)
        subnet = '172.29.' + str(16 + uuid.uuid4().int % 220) + '.0/24'
        ip = subnet.split('/')[0][:-1] + '41'
        file, state = private / 'samples/catalog.yaml', private / 'receipt.json'
        for name, field, port, suffix in [('hello-world', 'supportedInterfaces', agent_port, '/'), ('everything', 'remotes', mcp_port, '/mcp')]:
            path = private / 'samples/cards' / (name + '.json')
            card = json.loads(path.read_bytes())
            card[field][0]['url'] = 'https://127.0.0.1:' + port + suffix
            path.write_text(json.dumps(card) + '\n')
        selections = {'COMPOSE_PROJECT': project, 'IMAGE': 'open-agentic-catalog:' + project, 'UI_IMAGE': 'open-agentic-catalog-ui:' + project,
            'CATALOG_COMPOSE_SUBNET': subnet, 'CATALOG_COMPOSE_HTTP_PORT': api_port, 'CATALOG_COMPOSE_POSTGRES_PORT': db_port,
            'CATALOG_UI_PORT': ui_port, 'LOCAL_UI': 'on', 'LOCAL_DEMO_ARCHIVES': 'on', 'LOCAL_DEMO_RUNTIMES': 'on',
            'DEMO_RUNTIME_DIR': str(private / 'runtimes'), 'DEMO_A2A_PORT': agent_port, 'DEMO_MCP_PORT': mcp_port,
            'DEMO_ARCHIVE_DIR': str(private / 'archives'), 'DEMO_ARCHIVE_IP': ip, 'DEMO_ARCHIVE_PORT': archive_port,
            'DEMO_DATA_FILE': str(file), 'DEMO_STATE_FILE': str(state), 'DEMO_SAMPLE_SET': 'curated', 'DEMO_EXTEND': 'false'}
        environment.update(selections)
        compose = launcher.compose_arguments(project, False, ui=True, demo=True, peers=True)
        def make(target, *extra):
            launcher.command(['make', target, 'CATALOG_ENV_FILE=/dev/null', *extra], environment, seconds=900)
        def sql(query):
            return launcher.command(['docker', 'exec', project + '-postgres-1', 'psql', '-U', 'catalog', '-d', 'catalog', '-Atc', query], environment, capture=True).decode().strip()
        def authoring():
            return sql("select action,count(*) from audit_events where action not in ('evidence.version_revision.read','identity.membership_provisioned') group by action order by action")
        try:
            make('demo-check')
            phase = 'demo-up'
            make('demo-up')
            environment = launcher.runtimes.inputs(environment, verify=False, command=None)
            environment = launcher.demo_inputs(environment, verify=False)
            environment = launcher.ui_inputs(project, environment, create=False)
            api = API(api_port)
            first = json.loads(state.read_bytes())
            assert len(first['items']) == 14 and sum(i['state'] == 'published' for i in first['items']) == 12
            original = retained_proof(api, first)
            selected = records(api, first)
            assert all(len(value['listingRevision']['categories']) == 2 for value in selected.values())
            categories, _ = api.exchange('POST', '/api/v1/catalog/explore', {
                'query': {}, 'facets': [{'field': 'category', 'limit': 24, 'minCount': 1}]})
            assert categories['matched'] == 12
            assert {bucket['value']: bucket['count'] for bucket in categories['facets']['category']['buckets']} == {
                'development': 10, 'examples': 3, 'testing': 3, 'data': 2, 'productivity': 1,
                'design': 3, 'documentation': 1, 'discovery': 1}
            report['categoryCounts'] = categories['facets']['category']['buckets']
            assert search(api) == {i['identifier'] for i in first['items'] if i['state'] == 'published'}
            card = selected['hello-world']['versionRevision']['representations'][0]['data']
            mcp_card = selected['everything']['versionRevision']['representations'][0]['data']
            for key in ('frontend-design', 'brand-guidelines', 'webapp-testing'):
                representation = selected[key]['versionRevision']['representations'][0]
                assert representation['package'] and representation['handoff']['git']['commit'] == '8a1541c4a3ffa5a20a5a91de0dcf3f0bab1d1ef4'
            plugin = selected['frontend-toolkit']['versionRevision']['representations'][0]['plugin']
            assert 'https://mcp.context7.com/mcp' in json.dumps(plugin)
            assert 'aws-mcp.us-east-1.api.aws/mcp' not in json.dumps(mcp_card)
            report['images'] = {kind: launcher.command(['docker', 'image', 'inspect', selections[key], '--format', '{{.Id}}'],
                environment, capture=True).decode().strip() for kind, key in (('backend', 'IMAGE'), ('ui', 'UI_IMAGE'))}
            phase = 'browser'
            launcher.command(['node', 'catalog-ui/scripts/demo-journeys.mjs', 'http://127.0.0.1:' + ui_port,
                str(state), str(ROOT / 'output/playwright' / project)], environment, seconds=240)
            phase = 'catalog-selected-clients'
            report['clients'] = runtime_clients.qualify(environment, card=card, mcp_card=mcp_card)
            phase = 'unrelated-release'
            independent = unrelated(api, first, card)
            phase = 'yaml-only-extension'
            file.write_text(extend_yaml(file.read_text()))
            make('demo-check')
            phase = 'extend-demo-up'
            make('demo-up', 'DEMO_EXTEND=true')
            phase = 'extension-readback'
            extended = json.loads(state.read_bytes())
            assert len(extended['items']) == 15 and all(item in extended['items'] for item in first['items'])
            phase = 'immutable-evidence'
            assert retained_proof(api, first) == original
            extended_records = records(api, first)
            for key, value in selected.items():
                assert value['release'] == extended_records[key]['release']
                assert value['listingRevision'] == extended_records[key]['listingRevision']
            phase = 'unrelated-preservation'
            assert independent == api.get('/api/v1/catalog/resources/' + independent['release']['id'])
            assert independent['resource']['id'] not in {item['resourceId'] for item in extended['items']}
            expected = {i['identifier'] for i in extended['items'] if i['state'] == 'published'} | {independent['resource']['canonicalIdentifier']}
            assert search(api) == expected
            ard = api.get('/.well-known/ard.json')
            ai = api.get('/.well-known/ai-catalog.json')
            assert len(ard['entries']) == 12 and len(ai['entries']) == 11
            phase = 'public-exclusions'
            snapshot = api.get('/api/v1/publication/snapshots/' + extended['snapshotId'] + '?host=127.0.0.1', 'local-publisher')
            assert len([e for e in snapshot['exclusions'] if 'PUBLIC_DELIVERY_FORM_REQUIRED' in e['reasons']]) == 3
            before = authoring()
            make('demo-up')
            assert authoring() == before and json.loads(state.read_bytes()) == extended
            phase = 'archive-off-rerun'
            launcher.command(compose + ['stop', 'demo-archives'], environment)
            make('demo-seed', 'DEMO_ARCHIVE_TRUST_FILE=/unavailable/ca.pem')
            assert authoring() == before and json.loads(state.read_bytes()) == extended
            phase = 'peer-outage'
            launcher.command(compose + ['stop', *launcher.runtimes.SERVICES], environment)
            for port in (agent_port, mcp_port):
                with socket.socket() as connection:
                    connection.settimeout(2)
                    assert connection.connect_ex(('127.0.0.1', int(port))) != 0
            # Extension refreshes current Plugin validation; compare the complete
            # post-extension projection across outage, plus prior bound evidence above.
            assert records(api, first) == extended_records and search(api) == expected
            identities = {name: hashlib.sha256((private / 'runtimes' / name).read_bytes()).hexdigest() for name in ('cert.pem', 'key.pem', 'mcp-key')}
            phase = 'peer-restart'
            launcher.command(compose + ['up', '--no-deps', '-d', *launcher.runtimes.SERVICES], environment)
            launcher.runtimes.ready(environment, compose, launcher.command)
            assert identities == {name: hashlib.sha256((private / 'runtimes' / name).read_bytes()).hexdigest() for name in identities}
            report['restartedClients'] = runtime_clients.qualify(environment, card=card, mcp_card=mcp_card)
            report.update(resources=15, published=13, unrelatedPreserved=True, pagination=True, evidencePreserved=True,
                yamlOnlyExtension=True, readOnlyRerun=True, archiveOff=True, peerOutageMetadata=True,
                peerIdentityStable=True, ardEntries=12, aiCatalogEntries=11,
                manifestSHA256=hashlib.sha256(file.read_bytes()).hexdigest(), success=True)
        except (Exception, KeyboardInterrupt) as error:
            report['failureType'] = type(error).__name__
            trace = error.__traceback__
            while trace is not None:
                if trace.tb_frame.f_code.co_filename == __file__:
                    report['failureLine'] = trace.tb_lineno
                trace = trace.tb_next
            raise
        finally:
            report['phase'] = phase
            try:
                make('compose-reset')
                for kind in ('ps', 'network', 'volume'):
                    command = ['docker', 'ps', '-a'] if kind == 'ps' else ['docker', kind, 'ls']
                    raw = launcher.command([*command, '--filter', 'label=com.docker.compose.project=' + project, '--format', '{{.Names}}' if kind == 'ps' else '{{.Name}}'], environment, capture=True)
                    assert not raw.strip()
                report['cleanup'] = True
            finally:
                shutil.rmtree(ROOT / 'output/ui-compose' / project, ignore_errors=True)
                report_file.write_text(json.dumps(report, indent=2) + '\n')
                print('Catalog demo workflow report: ' + str(report_file))
    assert report['success'] and report['cleanup']


if __name__ == '__main__':
    def interrupted(_signum, _frame):
        raise KeyboardInterrupt
    signal.signal(signal.SIGTERM, interrupted)
    try:
        qualify()
    except (Exception, KeyboardInterrupt):
        sys.exit('Owned Catalog demo qualification failed; inspect its phase/outcome/cleanup report.')
