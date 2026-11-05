"""Pure policy decisions for the test-only Rapid license exception."""

import json
import unittest

import go_licenses as licenses


class LicensePolicyTests(unittest.TestCase):
    def rapid(self, **changes):
        module = {"Path": "pgregory.net/rapid", "Version": "v1.3.0", **changes}
        return [{"ImportPath": "pgregory.net/rapid", "Module": module}]

    def test_go_stream_preserves_all_objects_and_rejects_incomplete_discovery(self):
        values = [{"ImportPath": "fmt"}, *self.rapid()]
        self.assertEqual(licenses.packages("\n".join(map(json.dumps, values))), values)
        for raw in ("", "null", "{}", "[]", '{"ImportPath":"fmt"}broken'):
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                licenses.packages(raw)

    def test_rapid_exception_requires_exact_unreplaced_test_module(self):
        self.assertTrue(licenses.rapid_dependency(self.rapid(), tests=True))
        self.assertFalse(licenses.rapid_dependency([{"ImportPath": "fmt"}], tests=False))
        for changes in ({"Version": "v1.3.1"}, {"Version": ""}, {"Path": "example.com/other"},
                        {"Replace": {"Path": "../rapid"}}):
            with self.subTest(changes=changes), self.assertRaisesRegex(ValueError, "reviewed"):
                licenses.rapid_dependency(self.rapid(**changes), tests=True)
        with self.assertRaisesRegex(ValueError, "non-test"):
            licenses.rapid_dependency(self.rapid(), tests=False)
        # Module identity also catches an import under a different package path.
        discovered = self.rapid()
        discovered[0]["ImportPath"] = "somewhere.invalid/alias"
        with self.assertRaisesRegex(ValueError, "non-test"):
            licenses.rapid_dependency(discovered, tests=False)

    def test_full_report_keeps_transitive_and_unknown_license_rejection(self):
        allowed = {"MIT", "Apache-2.0", "BSD-2-Clause", "BSD-3-Clause"}
        ordinary = "example.com/transitive,https://example.com/LICENSE,MIT\n"
        rapid = "pgregory.net/rapid,https://example.com/LICENSE,MPL-2.0\n"
        licenses.check_report(ordinary, allowed, False)
        licenses.check_report(ordinary + rapid, allowed, True)
        for license_name in ("Unknown", "MPL-2.0", "GPL-3.0", "AGPL-3.0", "LGPL-3.0"):
            with self.subTest(license=license_name), self.assertRaisesRegex(ValueError, "unapproved"):
                licenses.check_report(ordinary.replace(",MIT", "," + license_name) + rapid, allowed, True)
        for report, present in ((rapid, False), (ordinary, True), (rapid.replace("MPL-2.0", "Unknown"), True),
                                (rapid.replace("pgregory.net/rapid,", "pgregory.net/rapid-extra,"), True),
                                ("", False), (ordinary + ordinary, False), ("broken\n", False)):
            with self.subTest(report=report, present=present), self.assertRaises(ValueError):
                licenses.check_report(report, allowed, present)


if __name__ == "__main__":
    unittest.main()
