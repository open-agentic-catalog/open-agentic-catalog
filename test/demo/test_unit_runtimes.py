"""Isolated admission/configuration units; no Docker, servers, SDK or subprocess."""
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import agent_runtime
import runtimes


class RuntimeInputs(unittest.TestCase):
    def test_ports_and_development_refusal_before_tools(self):
        for env in ({'DEMO_A2A_PORT': '443'}, {'DEMO_A2A_PORT': '01943'}, {'DEMO_MCP_PORT': 'https://example.com'},
                    {'DEMO_A2A_PORT': '19444'}, {'CATALOG_ENVIRONMENT': 'production'}, {'CATALOG_AUTH_MODE': 'oidc'}):
            with self.subTest(env=env), patch.object(runtimes, 'tools') as tools:
                with self.assertRaises(ValueError):
                    runtimes.inputs(env, True, None)
                tools.assert_not_called()

    def test_missing_stale_tools_before_credentials(self):
        with tempfile.TemporaryDirectory() as temporary, patch.object(runtimes, 'tools', side_effect=RuntimeError('stale')):
            path = Path(temporary) / 'inputs'
            with self.assertRaises(RuntimeError):
                runtimes.inputs({'DEMO_RUNTIME_DIR': str(path)}, True, None)
            self.assertFalse(path.exists())

    def test_selection_and_stop_without_tools_or_rotation(self):
        with tempfile.TemporaryDirectory() as temporary, patch.object(runtimes, 'tools') as tools:
            root = Path(temporary)
            state = {'a2aImage': 'sha256:' + 'a'*64, 'mcpImage': 'sha256:' + 'b'*64, 'a2aPort': '21443', 'mcpPort': '21444'}
            (root / 'inputs.json').write_text(json.dumps(state))
            result = runtimes.inputs({'DEMO_RUNTIME_DIR': str(root)}, False, None)
            self.assertEqual(result['DEMO_A2A_PORT'], '21443')
            self.assertEqual(result['DEMO_MCP_IMAGE'], state['mcpImage'])
            tools.assert_not_called()
            self.assertEqual(state, json.loads((root / 'inputs.json').read_text()))
            for field, value in (('a2aImage', 'arbitrary/image:latest'), ('mcpPort', '12')):
                changed = dict(state, **{field: value})
                (root / 'inputs.json').write_text(json.dumps(changed))
                with self.assertRaises(ValueError):
                    runtimes.inputs({'DEMO_RUNTIME_DIR': str(root)}, False, None)

    def test_private_directory_and_input_link_rejection(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            target = base / 'target'
            link = base / 'link'
            link.symlink_to(target)
            with self.assertRaises(ValueError):
                runtimes.directory(link / 'nested', True)
            self.assertFalse(target.exists())
            base.chmod(0o755)
            with self.assertRaises(ValueError):
                runtimes.directory(base, True)
            base.chmod(0o700)
            outside = base / 'outside'
            outside.write_bytes(b'original')
            link.unlink()
            link.symlink_to(outside)
            with self.assertRaises(ValueError):
                runtimes.write(link, b'changed', 0o600)
            self.assertEqual(b'original', outside.read_bytes())
            runtimes.write(base / 'input', b'one', 0o600)
            runtimes.write(base / 'input', b'two', 0o600)
            self.assertEqual(0o600, (base / 'input').stat().st_mode & 0o777)


    def test_linked_state_and_lock_fail_without_write_or_build(self):
        with tempfile.TemporaryDirectory() as temporary, patch.object(runtimes, 'tools', return_value=({}, {}, 'amd64')):
            root = Path(temporary)
            state = root / 'inputs.json'
            state.symlink_to(root / 'missing')
            with self.assertRaises(ValueError):
                runtimes.inputs({'DEMO_RUNTIME_DIR': str(root)}, False, None)
            state.unlink()
            outside = root / 'outside'
            outside.write_bytes(b'unchanged')
            (root / '.prepare.lock').symlink_to(outside)
            with patch.object(runtimes, 'write') as write:
                # No launcher command should run before unsafe lock refusal.
                with self.assertRaises(OSError):
                    runtimes.inputs({'DEMO_RUNTIME_DIR': str(root)}, True, None)
                write.assert_not_called()
            self.assertEqual(b'unchanged', outside.read_bytes())


class AgentAdmission(unittest.IsolatedAsyncioTestCase):
    async def exchange(self, app, path='/', method='POST', headers=None, raw=b'{"method":"SendMessage"}', query=b''):
        sent, chunks = [], [{'type': 'http.request', 'body': raw, 'more_body': False}]
        async def receive():
            return chunks.pop(0) if chunks else {'type': 'http.disconnect'}
        async def send(event):
            sent.append(event)
        await app({'type': 'http', 'path': path, 'method': method, 'headers': headers or [], 'query_string': query}, receive, send)
        return sent

    async def test_fixed_admission_and_bounded_lifetime(self):
        calls = []
        async def native(scope, receive, send):
            calls.append(await receive())
            await agent_runtime.reply(send, 200)
        app = agent_runtime.guard(native)
        for kwargs, status in [({'headers': [(b'authorization', b'private')]}, 401),
                                ({'headers': [(b'cookie', b'private')]}, 401),
                                ({'path': '/observations'}, 404), ({'query': b'private'}, 404),
                                ({'method': 'GET'}, 405), ({'raw': b'['}, 400),
                                ({'raw': b'x'*65537}, 413), ({'raw': b'[]'}, 403),
                                ({'raw': b'{"method":"CancelTask"}'}, 403)]:
            rows = await self.exchange(app, **kwargs)
            self.assertEqual(status, rows[0]['status'])
        self.assertEqual([], calls)
        for _ in range(256):
            self.assertEqual(200, (await self.exchange(app))[0]['status'])
        self.assertEqual(429, (await self.exchange(app))[0]['status'])
        self.assertEqual(200, (await self.exchange(app, raw=b'{"method":"GetTask"}'))[0]['status'])
        self.assertEqual(257, len(calls))
        self.assertEqual(b'{"method":"SendMessage"}', calls[0]['body'])

    async def test_native_fault_does_not_disclose_dependency_text(self):
        async def native(_scope, _receive, _send):
            raise ValueError('secret dependency payload')
        rows = await self.exchange(agent_runtime.guard(native))
        self.assertEqual(500, rows[0]['status'])
        self.assertNotIn('secret', str(rows))


if __name__ == '__main__':
    unittest.main()
