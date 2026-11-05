"""Pure checks for exact ANS fixture extraction, without peers or processes."""
import importlib.util
from pathlib import Path
import sys
import unittest

ANS = Path(__file__).parent / "ans"
sys.path.insert(0, str(ANS))
spec = importlib.util.spec_from_file_location("ans_capture", ANS / "capture.py")
capture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(capture)


class CaptureTests(unittest.TestCase):
    def test_exact_bytes_and_unicode(self):
        entry = '{ "identifier" : "urn:air:example.test:agents:é", "number": 1.00 }'.encode()
        response = b'{"results": [ \n' + entry + b' ], "other": true}'
        self.assertEqual(capture.entry_bytes(response), entry)

    def test_single_entry_required(self):
        for response in (b'{"results": []}', b'{"results": [{},{}]}', b'{"results": [1]}', b'not json'):
            with self.subTest(response=response), self.assertRaises((ValueError, TypeError)):
                capture.entry_bytes(response)
