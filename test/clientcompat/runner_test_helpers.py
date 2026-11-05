from contextlib import ExitStack, redirect_stderr, redirect_stdout
import io
import json
from pathlib import Path
import tempfile
from unittest.mock import patch
import runner

"""Launcher safety without downloading tools or starting Catalog processes."""

class RunnerTestsFixture:
    def setUp(self):
        stack = ExitStack()
        self.addCleanup(stack.close)
        self.stdout, self.stderr = io.StringIO(), io.StringIO()
        stack.enter_context(redirect_stdout(self.stdout))
        stack.enter_context(redirect_stderr(self.stderr))
        self.root = Path(stack.enter_context(tempfile.TemporaryDirectory()))
        self.here = self.root / "test/clientcompat"
        stack.enter_context(patch.object(runner, "ROOT", self.root))
        stack.enter_context(patch.object(runner, "HERE", self.here))
        for part in (self.root, self.here / "go", self.here / "tools"):
            part.mkdir(parents=True, exist_ok=True)
            (part / "go.mod").write_text("module fixture\ngo 1.27.0\n")
            (part / "go.sum").write_text("locked\n")
        (self.here / "go/go.mod").write_text(f"module fixture\ngo 1.27.0\nreplace {runner.MODULE} => ../../..\n")
        (self.root / "api/openapi").mkdir(parents=True)
        (self.root / "api/openapi/control-v1.yaml").write_text("contract\n")
        (self.here / "generate.yaml").write_text("config\n")
        (self.here / "go/consumer_test.go").write_text("test\n")
    def stamp(self):
        tools = self.here / ".tools"
        tools.mkdir(exist_ok=True)
        (tools / "go").write_text("go")
        (tools / "oapi-codegen").write_text("generator")
        (tools / "installed.json").write_text(json.dumps({"inputs": runner.inputs(), "go": str(tools / "go"), "binary": runner.digest(tools / "oapi-codegen")}))
    @staticmethod
    def fake_generate(destination, _env):
        destination.parent.mkdir(parents=True, exist_ok=True)
        destination.write_text("package catalogclient\n")
    @staticmethod
    def fake_run(_args, cwd, _env, log=None):
        if cwd.name == "consumer":
            if (cwd / "generated/stale.go").exists():
                raise AssertionError("copied pre-existing generated output")
            if list(cwd.parent.glob("*.go")):
                raise AssertionError("generated Go leaked outside the nested consumer module")
        if log:
            log.write_text("test diagnostics\n")
