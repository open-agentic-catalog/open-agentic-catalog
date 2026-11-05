"""Finite MCP refusal evidence. Never retain tool text, arguments or credentials."""

import json

CODES = frozenset(('internal_error', 'busy', 'cancelled', 'invalid_arguments', 'api_denied',
                   'api_not_found', 'api_failure', 'invalid_response', 'output_limit'))
TRANSPORTS = ('stdio', 'http')
PROTOCOLS = ('2025-11-25', '2026-07-28')
CASES = frozenset(('inventory', 'local-pages', 'filters', 'indexed', 'exact-release', 'explore-access',
                   'empty-sparse', 'closed-arguments', 'live', 'partial', 'replica-continuation',
                   'restart-continuation', 'stale-cursor', 'live-kind-agent', 'live-kind-mcp_server',
                   'live-kind-skill', 'live-kind-plugin'))


class ToolRefusal(AssertionError):
    def __init__(self, content):
        code = 'unclassified'
        if isinstance(content, list) and len(content) == 1 and isinstance(content[0], dict):
            text = content[0].get('text')
            if content[0].get('type') == 'text' and isinstance(text, str) and len(text) <= 1024:
                prefix, separator, _ = text.partition(': ')
                if separator and prefix in CODES:
                    code = prefix
        self.code = code
        super().__init__('unexpected tool refusal: ' + code)


def failure_evidence(error, phase):
    """Keep only a reviewed phase and the refusal class from a nested SDK error."""
    parts = phase.split('/')
    if len(parts) != 3 or parts[0] not in TRANSPORTS or parts[1] not in PROTOCOLS or parts[2] not in CASES:
        return None
    def codes(value):
        if isinstance(value, BaseExceptionGroup):
            return [code for child in value.exceptions for code in codes(child)]
        return [value.code] if isinstance(value, ToolRefusal) else []
    found = codes(error)
    code = found[0] if len(set(found)) == 1 else 'unclassified'
    return dict(status='failed', transport=parts[0], protocol=parts[1], case=parts[2], toolRefusal=code)


def read_failure(stdout):
    if not isinstance(stdout, str) or len(stdout) > 1024:
        return None
    try:
        data = json.loads(stdout)
    except ValueError:
        return None
    if not isinstance(data, dict) or set(data) != {'status', 'transport', 'protocol', 'case', 'toolRefusal'}:
        return None
    if not all(isinstance(value, str) for value in data.values()):
        return None
    if (data['status'] != 'failed' or data['transport'] not in TRANSPORTS or data['protocol'] not in PROTOCOLS
            or data['case'] not in CASES or data['toolRefusal'] not in (*CODES, 'unclassified')):
        return None
    return data
