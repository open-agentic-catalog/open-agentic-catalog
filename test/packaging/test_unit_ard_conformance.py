"""Schema evidence is distinct from an upstream PASS or protocol-only mode."""

import sys
import tomllib
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

import ard_conformance


class SchemaEvidenceTests(unittest.TestCase):
    def test_installation_requirement_matches_enforced_version(self):
        config = tomllib.loads((ard_conformance.ROOT / 'pyproject.toml').read_text())
        self.assertIn('jsonschema==' + ard_conformance.JSONSCHEMA_VERSION,
                      config['dependency-groups']['dev'])

    def test_manifest_and_publisher_require_both_schema_results(self):
        manifest = '  \x1b[32m✓\x1b[0m Manifest validates against ArdManifest.\n'
        entries = '  \x1b[32m✓\x1b[0m All 0 entries validate against ArdEntry.\n'
        for mode in ('manifest', 'publisher'):
            self.assertTrue(ard_conformance.schema_evidence(mode, manifest + entries + "  ✓ [Skipping optional work] Valid URN.\n"))
            for output in ('CONFORMANCE STATUS: PASS\n', manifest, entries,
                           manifest + entries + manifest,
                           manifest + entries + "  ⚠ Failed to run JSON Schema validator: unavailable",
                           manifest + entries.replace('0 entries', 'invalid entries')):
                with self.subTest(mode=mode, output=output):
                    self.assertFalse(ard_conformance.schema_evidence(mode, output))

    def test_prerequisite_validates_version_schema_dialect_and_definitions(self):
        validator = Mock()
        validator_for = Mock(return_value=validator)
        schema = '{"$defs":{"ArdManifest":{},"ArdEntry":{}}}'
        modules = {
            'jsonschema': SimpleNamespace(Draft202012Validator=validator, SchemaError=RuntimeError),
            'jsonschema.validators': SimpleNamespace(validator_for=validator_for),
        }
        with patch.dict(sys.modules, modules), \
             patch.object(ard_conformance.importlib.metadata, 'version', return_value='4.26.0') as version, \
             patch.object(ard_conformance, 'SCHEMA') as source:
            source.read_text.return_value = schema
            ard_conformance.prerequisite()
            validator.check_schema.assert_called_once()
            version.return_value = 'unsupported'
            with self.assertRaisesRegex(ValueError, 'Install'):
                ard_conformance.prerequisite()
            version.return_value = '4.26.0'
            validator_for.return_value = object()
            with self.assertRaisesRegex(ValueError, 'Draft 2020-12'):
                ard_conformance.prerequisite()
            validator_for.return_value = validator
            source.read_text.return_value = '{"$defs":{}}'
            with self.assertRaisesRegex(ValueError, 'missing ArdManifest'):
                ard_conformance.prerequisite()
            source.read_text.return_value = schema
            validator.check_schema.side_effect = RuntimeError('bad schema')
            with self.assertRaisesRegex(ValueError, 'schema is invalid'):
                ard_conformance.prerequisite()

    def test_registry_does_not_claim_manifest_schema_validation(self):
        self.assertTrue(ard_conformance.schema_evidence('registry', 'protocol results'))

    def test_owned_registry_requires_all_routes_and_exact_positive_native_count(self):
        routes = [f"  ✓ {route} responded successfully with 200 OK.\n"
                  for route in ('GET /agents', 'POST /search', 'POST /explore')]
        count = '  ✓ POST /search returned valid results list (1 items).\n'
        good = ''.join(routes) + count
        self.assertTrue(ard_conformance.registry_result_evidence(good, 1))
        self.assertTrue(ard_conformance.registry_result_evidence(good.replace('1 items', '2 items'), 2))
        self.assertTrue(ard_conformance.registry_result_evidence(good.replace('✓', '\x1b[32m✓\x1b[0m'), 1))
        for output in ('CONFORMANCE STATUS: PASS\n', good.replace(count, ''),
                       good.replace('1 items', '0 items'), good.replace('1 items', '2 items'),
                       good + count, good + count.replace('1 items', '0 items'),
                       good.replace('1 items', 'invalid items')):
            with self.subTest(output=output):
                self.assertFalse(ard_conformance.registry_result_evidence(output, 1))
        for route in routes:
            for output in (good.replace(route, ''), good + route, good.replace(route, route.replace('200', '201'))):
                with self.subTest(route=route, output=output):
                    self.assertFalse(ard_conformance.registry_result_evidence(output, 1))
        for expected in (0, -1, True, '1', None):
            self.assertFalse(ard_conformance.registry_result_evidence(good, expected))
        empty = good.replace('1 items', '0 items')
        self.assertTrue(ard_conformance.schema_evidence('registry', empty))
        self.assertFalse(ard_conformance.registry_result_evidence(empty, 1))


if __name__ == '__main__':
    unittest.main()
