from pathlib import Path
import tempfile
from unittest.mock import patch
import runner

class SupervisorRecoveryFixture:
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name).resolve()
        root = patch.object(runner, "ROOT", self.root)
        root.start()
        self.addCleanup(root.stop)
        self.run_id = "a" * 24
        self.state = {"version": 2, "runId": self.run_id, "project": "open-agentic-catalog-perf-" + self.run_id, "image": "open-agentic-catalog:perf-" + self.run_id, "nonce": "b" * 24, "engine": "engine", "monitoring": True}
    def record(self):
        directory = runner.local.private_directory(self.root / "coverage/performance" / self.run_id)
        (directory / "owner.lock").touch(mode=0o600)
        runner.local.safety.write_document(directory / "ownership.json", self.state)
        runner.local.safety.write_document(directory / "owned-compose.json", runner.state_model(self.state))
        runner.local.safety.write_document(directory / "report.json", {"status": "incomplete", "cleanup": "failed"})
        return directory
