"""Pure fixture edits and discovered endpoint admission; no processes or sockets."""
import copy
import json
import re
import unittest

import qualify_catalog
import runtime_clients


class CatalogInputs(unittest.TestCase):
    def test_yaml_only_addition_preserves_original(self):
        raw = qualify_catalog.ROOT.joinpath('samples/catalog.yaml').read_text()
        edited = qualify_catalog.extend_yaml(raw)
        self.assertEqual(edited.count('key: extra-hello-world\n'), 1)
        self.assertEqual(edited.count(':extra-hello-world\n'), 1)
        self.assertIn('name: Additional Hello World\n', edited)
        added = re.search(r'^  - key: extra-hello-world\n.*?(?=^  - key: |^revision:|\Z)', edited, re.M | re.S)
        self.assertIsNotNone(added)
        restored = edited[:added.start()] + edited[added.end():]
        self.assertEqual(restored.replace('    - extra-hello-world\n', '', 1), raw)
        self.assertEqual(edited.splitlines()[-1], raw.splitlines()[-1])
        for bad in ('', raw.replace('key: hello-world\n', 'key: missing\n'), raw + '\n  curated:\n'):
            with self.assertRaises(ValueError):
                qualify_catalog.extend_yaml(bad)

    def test_catalog_handoff_is_bound_to_explicit_peers(self):
        root = qualify_catalog.ROOT / 'samples/cards'
        agent = json.loads((root / 'hello-world.json').read_bytes())
        mcp = json.loads((root / 'everything.json').read_bytes())
        environment = {'DEMO_MCP_PORT': '19444'}
        card, url = runtime_clients.selected_cards(environment, agent, copy.deepcopy(agent), mcp)
        self.assertEqual(card, agent)
        self.assertEqual(url, 'https://127.0.0.1:19444/mcp')
        for field in ('endpoint', 'credentials', 'protocol', 'identity'):
            changed = copy.deepcopy(mcp)
            if field == 'endpoint':
                changed['remotes'][0]['url'] = 'https://mcp.context7.com/mcp'
            elif field == 'credentials':
                changed['remotes'][0]['headers'][0]['name'] = 'Authorization'
            elif field == 'protocol':
                changed['remotes'][0]['supportedProtocolVersions'] = ['future']
            else:
                changed['version'] = 'made-up'
            with self.subTest(field=field), self.assertRaises(ValueError):
                runtime_clients.selected_cards(environment, agent, agent, changed)
        changed = copy.deepcopy(agent)
        changed['supportedInterfaces'][0]['url'] = 'https://external.example/'
        with self.assertRaises(ValueError):
            runtime_clients.selected_cards(environment, agent, changed, mcp)


if __name__ == '__main__':
    unittest.main()
