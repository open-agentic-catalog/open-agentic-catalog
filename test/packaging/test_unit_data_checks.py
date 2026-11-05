"""Data syntax and inventory policy operate on values, without external tools."""

import unittest

import data_checks


class DataSyntaxTests(unittest.TestCase):
    def test_json_valid_values_and_exact_number_tokens(self):
        for raw in (b'null', b'[]', b'1e999999', b'1.2345678901234567890',
                    b'9' * 5000, '{"é": [true, false, -1, {"a": 1}], "b": {"a": 2}}'.encode()):
            with self.subTest(raw=raw[:40]):
                data_checks.validate_json(raw)

    def test_json_rejects_malformed_ambiguous_and_non_utf8_input(self):
        for raw in (b'', b'{} {}', b'{"a":1,"a":2}', b'{"a":1,"\\u0061":2}',
                    b'[{"nested": {"a":1,"a":2}}]', b'NaN', b'Infinity', b'-Infinity',
                    b'{"x":NaN}', b'01', b'1.', b'1e', b'{"a":}', b'{/*comment*/}',
                    b'[1,]', b'"\xff"', '{}'.encode('utf-16'), b'\xef\xbb\xbf{}'):
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                data_checks.validate_json(raw)

    def test_yaml_owners_are_narrow(self):
        for name in ('deployments/helm/open-agentic-catalog/templates/service.yaml',
                     'deployments/helm/open-agentic-catalog-monitoring/templates/configmap.yaml',
                     'api/openapi/control-v1.yaml'):
            self.assertIsNone(data_checks.yaml_scope(name))
        self.assertEqual(data_checks.yaml_scope('schemas/upstream/profile/spec.yaml'), 'upstream')
        for name in ('.yamllint.yml', 'test/clientcompat/generate.yaml',
                     'deployments/helm/open-agentic-catalog/ci/semantic-values.yaml',
                     'test/templates/real.yaml'):
            self.assertEqual(data_checks.yaml_scope(name), 'maintained')


if __name__ == '__main__':
    unittest.main()
