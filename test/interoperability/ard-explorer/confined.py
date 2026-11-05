"""Run only inside the owned, network-isolated, bounded systemd service."""

import json
import os
from pathlib import Path
import re
import socket
import subprocess
import sys
import threading
import time
import urllib.request
from collections import Counter
from urllib.parse import urlsplit

from fixture import server

STATE = Path('/state')


def fetch(url, token=None):
    client = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    headers = {'Authorization': 'Bearer ' + token} if token else {}
    with client.open(urllib.request.Request(url, headers=headers), timeout=2) as response:
        raw = response.read(1048577)
        assert len(raw) <= 1048576
        return raw, {key.lower(): value for key, value in response.headers.items()}


def request(url):
    return json.loads(fetch(url)[0])


def metrics():
    raw, _ = fetch('http://127.0.0.1:8080/metrics')
    result = {}
    for line in raw.decode().splitlines():
        if line.startswith('open_agentic_catalog_http_responses_total{'):
            labels, count = line.split('} ')
            values = dict(re.findall(r'(method|route|status_class)="([^"]*)"', labels))
            if values['route'] != '/metrics':
                result[(values['method'], values['route'], values['status_class'])] = int(float(count))
    assert result, 'Catalog HTTP counter inventory is missing'
    return Counter(result)


def populate():
    result = subprocess.run(['/state/catalogctl', 'smoke', '--run-id', 'explorer'], capture_output=True, timeout=120, check=False)
    (STATE / 'smoke.log').write_bytes(result.stdout + result.stderr)
    assert result.returncode == 0, 'owned Catalog smoke setup failed'
    smoke = json.loads(result.stdout)
    assert smoke['status'] == 'passed' and len(smoke['resources']) == 3
    raw, headers = fetch('http://127.0.0.1:8080/.well-known/ard.json')
    manifest = json.loads(raw)
    expected = []
    retained = []
    for resource in smoke['resources']:
        data, _ = fetch('http://127.0.0.1:8080/api/v1/catalog/resources/' + resource['releaseId'], 'local-reader')
        value = json.loads(data)
        assert value['resource']['id'] == resource['resourceId']
        assert value['resource']['canonicalIdentifier'] == resource['identifier']
        assert value['release']['id'] == resource['releaseId']
        retained.append(value)
        matches = [entry for entry in manifest['entries'] if entry['identifier'] == resource['identifier']]
        assert len(matches) == 1
        expected.append(matches[0])
    (STATE / 'retained.json').write_text(json.dumps(retained) + '\n')
    return {'catalog': 'http://127.0.0.1:8080', 'resources': smoke['resources'], 'entries': expected,
            'manifest': manifest}, raw, headers['etag']


def verify_browser(evidence, fixture_observations, before, after):
    """Native proxy replies must agree with independent server-side counters."""
    exchanges = evidence['exchanges']
    expected = Counter()
    observed_fixture = []
    for item in exchanges:
        url = urlsplit(item['url'])
        assert not item['authorization']
        if url.port == 8080:
            assert url.hostname in ('127.0.0.1', 'localhost')
            assert item['method'] in ('GET', 'POST')
            assert url.path in ('/version', '/.well-known/ard.json', '/ard/v1/search', '/ard/v1/explore', '/ard/v1/agents')
            assert item['native']['ok'] and item['outerStatus'] == 200
            status = item['native']['status']
            expected[(item['method'], url.path, str(status // 100) + 'xx')] += 1
        elif url.hostname == '127.0.0.1' and item['native'].get('ok'):
            observed_fixture.append({'method': item['method'], 'path': url.path + ('?' + url.query if url.query else ''),
                                     'body': json.loads(item['body']) if item['body'] else {}, 'status': item['native']['status'],
                                     'authorization': False})
    assert all(after[key] >= count for key, count in before.items()), 'Catalog HTTP counters decreased during browsing'
    assert after - before == expected, 'Catalog HTTP operation counters differ from native proxy exchanges'
    assert observed_fixture == fixture_observations, 'synthetic HTTP observations differ from native exchanges'
    assert all(item['path'].split('?')[0] in ('/search', '/explore', '/agents', '/referral-target/search/search',
                   '/referral-target/search/explore', '/referral-target/search/agents') for item in fixture_observations), 'fixture artifact or trust destination was requested'
    return [{'method': key[0], 'route': key[1], 'statusClass': key[2], 'count': value} for key, value in sorted(expected.items())]


def wait_for(action, seconds=30):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        try:
            return action()
        except (OSError, ValueError, AssertionError):
            time.sleep(0.05)
    raise RuntimeError('owned process did not become ready')


def run():
    browsing = len(sys.argv) == 2 and sys.argv[1] == 'browse'
    assert os.environ['HOME'] == '/tmp/home'
    assert not any(k.lower().endswith('proxy') or 'TOKEN' in k or 'KEY' in k for k in os.environ)
    assert not any(Path(p).exists() for p in ('/home', '/root', '/var/run/docker.sock')), 'Explorer has unexpected host authority'
    # The new namespace has only loopback and no default route.
    assert len(Path('/proc/net/route').read_text().splitlines()) == 1
    with socket.socket() as connection:
        connection.settimeout(1)
        try:
            connection.connect(('198.51.100.1', 443))
        except OSError as error:
            assert error.errno == 101, 'external connection did not fail for no route'
        else:
            raise AssertionError('external network is reachable')
    group = Path('/sys/fs/cgroup') / Path('/proc/self/cgroup').read_text().strip().split('::', 1)[1].lstrip('/')
    assert (group / 'memory.max').read_text().strip() == '2147483648'
    quota, period = (group / 'cpu.max').read_text().split()
    assert int(quota) / int(period) == 2
    assert (group / 'pids.max').read_text().strip() == '256'
    assert (group / 'memory.swap.max').read_text().strip() == '0'
    children = []
    fixture_server = None
    fixture_thread = None
    reserved = None
    try:
        with (STATE / 'catalog.log').open('w') as catalog_log, (STATE / 'explorer.log').open('w') as explorer_log:
            children.append(subprocess.Popen(['/state/catalogd', 'all', '--config', '/state/config.json'], stdout=catalog_log, stderr=subprocess.STDOUT))
            wait_for(lambda: request('http://127.0.0.1:8080/readyz'))
            fixture = None
            if browsing:
                fixture, original, etag = populate()
                fixture_server = server()
                fixture_thread = threading.Thread(target=fixture_server.serve_forever)
                fixture_thread.start()
                reserved = socket.socket()
                reserved.bind(('127.0.0.1', 0))
                fixture.update(origin=fixture_server.origin, unavailable='http://127.0.0.1:' + str(reserved.getsockname()[1]))
            children.append(subprocess.Popen(['/node/bin/node', '/explorer/dist/cli.js', '--host', '127.0.0.1', '--port', '0',
                                             '--no-open', '--manifest', 'http://127.0.0.1:8080/.well-known/ard.json'],
                                            stdout=explorer_log, stderr=subprocess.STDOUT))
            def origin():
                match = re.search(r'http://127\.0\.0\.1:[0-9]+/', (STATE / 'explorer.log').read_text())
                assert match
                return match.group(0)
            url = wait_for(origin)
            config = request(url + '__ard/config')
            assert config == {'endpoint': None, 'manifest': 'http://127.0.0.1:8080/.well-known/ard.json'}
            (STATE / 'origin.json').write_text(json.dumps({'origin': url, 'fixture': fixture}))
            before = metrics() if browsing else None
            result = subprocess.run(['/node/bin/node', '/browser/scripts/ard-explorer-startup.mjs'], timeout=150, check=False)
            assert result.returncode == 0, 'native browser qualification failed'
            browser = json.loads((STATE / 'browser.json').read_text())
            assert browser['passed'] is True and browser['proxyOperations'] <= 50
            assert all(child.poll() is None for child in children), 'native process exited early'
            (STATE / 'startup.json').write_text(json.dumps({'passed': True, 'network': 'loopback-only',
                'externalConnectErrno': 101, 'memoryMax': 2147483648, 'cpuCores': 2, 'tasksMax': 256,
                'swapMax': 0, 'browser': browser, 'origin': url}) + '\n')
            if browsing:
                evidence = json.loads((STATE / 'browsing.json').read_text())
                evidence['catalogCounters'] = verify_browser(evidence, fixture_server.observations, before, metrics())
                current, current_headers = fetch('http://127.0.0.1:8080/.well-known/ard.json')
                assert current == original and current_headers['etag'] == etag, 'publication changed during browsing'
                evidence.update(snapshotStable=True, originalETag=etag, resources=fixture['resources'],
                                fixtureObservations=fixture_server.observations)
                (STATE / 'browsing.json').write_text(json.dumps(evidence) + '\n')
    finally:
        if fixture_server:
            fixture_server.shutdown()
            fixture_server.server_close()
            fixture_thread.join(timeout=5)
            assert not fixture_thread.is_alive()
        if reserved:
            reserved.close()
        for child in reversed(children):
            if child.poll() is None:
                child.terminate()
            try:
                child.wait(timeout=5)
            except subprocess.TimeoutExpired:
                child.kill()
                child.wait(timeout=5)


if __name__ == '__main__':
    run()
