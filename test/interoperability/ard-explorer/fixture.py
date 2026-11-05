"""Small synthetic HTTP controls, separate from the real Catalog verdict."""

import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit


def response(path, body, origin):
    """Return deliberately labeled client controls, not a translated Catalog."""
    if path.startswith('/referral-target/'):
        return 404, {'error': {'code': 'NOT_FOUND', 'message': 'The referral is a Search URL, not a registry base.'}}
    if path == '/agents':
        return 200, {'items': []}
    if path == '/explore':
        return 200, {'facets': {}}
    if path != '/search':
        return 404, {'error': {'code': 'NOT_FOUND', 'message': 'Unexpected fixture path.'}}
    text = body.get('query', {}).get('text', '')
    if text.startswith('http-'):
        status = int(text[5:])
        return status, {'error': {'code': {400: 'INVALID_ARGUMENT', 401: 'UNAUTHENTICATED', 503: 'UNAVAILABLE'}[status],
                                  'message': 'Synthetic HTTP control.'}}
    if text == 'requires-context':
        return 400, {'error': {'code': 'INVALID_ARGUMENT', 'message': 'Synthetic control requires queryContext.'}}
    if text == 'malformed-envelope':
        return 200, {'results': {'invalid': 'not an array'}}
    neighbor = {'identifier': 'urn:air:fixture.example:example:neighbor', 'displayName': 'Valid neighbor',
                'type': 'application/mcp-server-card+json', 'url': origin + '/artifact',
                'trustManifest': {'identity': origin + '/trust'}}
    if text == 'sparse-unfamiliar':
        return 200, {'results': [{'identifier': 'urn:air:fixture.example:example:sparse'},
            {'identifier': 'urn:air:fixture.example:example:unfamiliar', 'displayName': 'Unfamiliar media type',
             'type': 'application/vnd.example.agent+json;profile=v2'}, neighbor]}
    if text == 'referral':
        return 200, {'results': [neighbor], 'referrals': [{'identifier': 'urn:air:fixture.example:registry:referral',
            'displayName': 'Owned referral target', 'type': 'application/ai-registry+json',
            'url': origin + '/referral-target/search'}]}
    return 200, {'results': []}


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass

    def do_GET(self):
        self.exchange()

    def do_POST(self):
        self.exchange()

    def exchange(self):
        length = int(self.headers.get('Content-Length', '0'))
        assert 0 <= length <= 1048576
        raw = self.rfile.read(length)
        body = json.loads(raw) if raw else {}
        status, document = response(urlsplit(self.path).path, body, self.server.origin)
        data = json.dumps(document).encode()
        assert len(data) <= 1048576
        self.server.observations.append({'method': self.command, 'path': self.path, 'body': body, 'status': status,
                                         'authorization': self.headers.get('Authorization') is not None})
        self.send_response(status)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(data)))
        self.end_headers()
        self.wfile.write(data)


def server():
    value = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
    value.origin = 'http://127.0.0.1:' + str(value.server_port)
    value.observations = []
    return value
