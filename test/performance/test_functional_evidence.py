import json
from pathlib import Path
import tempfile
import unittest
import runner
from evidence_test_helpers import profile, report


class WorkloadEvidenceTests(unittest.TestCase):
    def test_real_nonzero_child_preserves_only_typed_aggregate(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            data = report()
            data["verdict"] = "failed"
            data["measured"].update(succeeded=2, failed=1)
            data["measured"]["outcomes"]["status"] = 1
            data["measured"]["httpFailureStatuses"] = {"503": 1}
            child = root / "child"
            child.write_text("#!/usr/bin/env python3\nimport os,sys\nf=os.open(sys.argv[2],os.O_CREAT|os.O_EXCL|os.O_WRONLY,0o600)\nos.write(f," + repr(json.dumps(data).encode()) + ")\nos.close(f)\nprint('private stderr text',file=sys.stderr)\nraise SystemExit(1)\n")
            child.chmod(0o700)
            actual, failed = runner.workload_result(child, root, profile(), runner.local.isolated_environment())
            self.assertTrue(failed)
            self.assertEqual(data, actual)
            self.assertNotIn("private", json.dumps(actual))
            with self.assertRaisesRegex(runner.Failure, "already-exists"):
                runner.workload_result(child, root, profile(), {})
