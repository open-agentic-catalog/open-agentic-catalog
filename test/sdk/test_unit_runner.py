from contextlib import redirect_stdout
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import runner


class RunnerUnits(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        for name, value in {
            "ROOT": self.root, "HERE": self.root / "test/sdk",
            "SDK": self.root / "sdk/go", "COMPAT": self.root / "test/clientcompat",
            "TOOLS": self.root / "test/sdk/.tools",
        }.items():
            replacement = patch.object(runner, name, value)
            replacement.start()
            self.addCleanup(replacement.stop)
            value.mkdir(parents=True, exist_ok=True)
        for directory in (runner.ROOT, runner.SDK, runner.COMPAT / "tools", runner.COMPAT / "go"):
            directory.mkdir(parents=True, exist_ok=True)
            (directory / "go.mod").write_text("module fixture\n\ngo 1.27.0\n")
            (directory / "go.sum").write_text("locks\n")
        (runner.ROOT / "api/openapi").mkdir(parents=True)
        (runner.ROOT / "api/openapi/control-v1.yaml").write_text("contract\n")
        (runner.COMPAT / "generate.yaml").write_text("config\n")
        (runner.SDK / "client.gen.go").write_text("generated\n")
        (runner.SDK / "README.md").write_text("# Go SDK\n")
        (runner.ROOT / "Makefile").write_text("\n".join(pin + " := v1.0.0" for pin, _ in runner.ANALYZERS.values()))
        self.go = self.root / "native/bin/go"
        self.go.parent.mkdir(parents=True)
        self.go.write_text("pinned Go")

    def stamp(self):
        for name in ("oapi-codegen", *runner.ANALYZERS):
            (runner.TOOLS / name).write_text("pinned " + name)
        (runner.TOOLS / "installed.json").write_text(json.dumps({
            "inputs": runner.inputs(), "go": str(self.go),
            "binaries": runner.tool_identities()}))

    def test_missing_malformed_stale_installation(self):
        for value in (None, "[]", "null", "bad JSON", '{"inputs":null}'):
            with self.subTest(value=value):
                if value is not None:
                    (runner.TOOLS / "installed.json").write_text(value)
                with self.assertRaisesRegex(RuntimeError, "sdk-tools-install"):
                    runner.installed()
        self.stamp()
        self.assertEqual(runner.installed(), self.go)
        (runner.SDK / "go.sum").write_text("changed locks")
        with self.assertRaisesRegex(RuntimeError, "stale"):
            runner.installed()
        self.stamp()
        (runner.TOOLS / "oapi-codegen").write_text("changed binary")
        with self.assertRaisesRegex(RuntimeError, "stale"):
            runner.installed()

    def test_native_offline_environment(self):
        poisoned = {"GOFLAGS": "-mod=mod", "GOWORK": "foreign", "GOOS": "other", "GOARCH": "arm64",
                    "CGO_ENABLED": "0", "GONOPROXY": "*", "GOPRIVATE": "*", "GOENV": "foreign"}
        with patch.dict(os.environ, poisoned):
            env = runner.environment(True)
        for key, value in {"GOFLAGS": "-mod=readonly", "GOWORK": "off", "GOENV": "off", "GOTOOLCHAIN": "local",
                           "GOPROXY": "off", "GONOPROXY": "none", "GOPRIVATE": "", "GOSUMDB": "off"}.items():
            self.assertEqual(env[key], value)
        for key in ("GOOS", "GOARCH", "CGO_ENABLED", "GOROOT"):
            self.assertNotIn(key, env)

    def test_install_uses_existing_generator_locks(self):
        calls = []
        def execute(args, cwd, env, **kwargs):
            calls.append((args, cwd))
            if args[1:3] == ["env", "GOROOT"]:
                return str(self.go.parents[1]) + "\n"
            if "build" in args:
                (runner.TOOLS / "oapi-codegen").write_text("built pinned generator")
            if "install" in args:
                (runner.TOOLS / args[2].split("@", 1)[0].rsplit("/", 1)[-1]).write_text("built analyzer")
        with patch.object(runner, "run", side_effect=execute), patch.object(runner.shutil, "which", return_value="go"):
            runner.install()
        self.assertEqual(runner.installed(), self.go)
        self.assertEqual([cwd for args, cwd in calls if "download" in args], [runner.COMPAT / "tools", runner.SDK, runner.COMPAT / "go"])

    def test_failed_install_removes_stamp(self):
        self.stamp()
        with patch.object(runner.shutil, "which", return_value=None), self.assertRaisesRegex(RuntimeError, "Go is required"):
            runner.install()
        self.assertFalse((runner.TOOLS / "installed.json").exists())

    def test_lint_and_license_checks_use_native_tools_and_reject_bad_reports(self):
        with (runner.ROOT / "Makefile").open("a") as source:
            source.write("\nGO_LICENSE_ALLOWLIST := Apache-2.0,MIT,BSD-2-Clause,BSD-3-Clause\n")
        reports = ("", "dependency,url,MPL-2.0\n", "dependency,,MIT\n", "dep,url,MIT\ndep,url,MIT\n")
        for report in reports:
            with patch.object(runner, "run", return_value=report), self.assertRaisesRegex(RuntimeError, "license report"):
                runner.lint_and_licenses(self.go, {"PATH": "/host/bin"})
        calls = []
        def execute(args, cwd, env, **kwargs):
            self.assertEqual(cwd, runner.SDK)
            self.assertEqual(env["PATH"].split(os.pathsep)[0], str(self.go.parent))
            calls.append(args)
            return "dependency,url,Apache-2.0\n"
        with patch.object(runner, "run", side_effect=execute):
            runner.lint_and_licenses(self.go, {"PATH": "/host/bin"})
        self.assertEqual(len(calls), 4)
        (runner.ROOT / "Makefile").write_text("missing policy\n")
        with self.assertRaisesRegex(RuntimeError, "Missing shared tool policy"):
            runner.inputs()

    def test_template_override_is_narrow_and_covers_emitted_parser_errors(self):
        source = 'return nil, err\nreturn err\nfunc Parse{{genResponseTypeName $opid | ucFirst}}(rsp *http.Response) (*{{genResponseTypeName $opid}}, error) {\n{{genResponseUnmarshal .}}'
        altered = runner.diagnostic_template(source)
        self.assertIn("return nil, safeError(err)", altered)
        self.assertIn("safeResponseError(err, rsp)", altered)
        self.assertIn("{{genResponseUnmarshal .}}", altered)
        with self.assertRaisesRegex(RuntimeError, "template changed"):
            runner.diagnostic_template("incompatible template")

    def test_coverage_excludes_generated_code_and_counts_weighted_blocks(self):
        profile = self.root / "local.out"
        blocks = "fixture/sdk.go:1.1,2.1 2 1\nfixture/sdk.go:3.1,4.1 3 0\n"
        profile.write_text("mode: atomic\nfixture/client.gen.go:1.1,2.1 1000 1\n" + blocks)
        output = io.StringIO()
        with patch.object(runner, "run") as execute, redirect_stdout(output):
            runner.report_coverage(self.go, profile, "Unit plus local HTTP/TLS functional", {})
        self.assertIn("Unit plus local HTTP/TLS functional handwritten SDK coverage: 2/5 statements (40.0%)", output.getvalue())
        filtered = self.root / "local-handwritten.out"
        self.assertEqual(filtered.read_text(), "mode: atomic\n" + blocks)
        execute.assert_called_once_with([self.go, "tool", "cover", "-func=" + str(filtered)], runner.SDK, {})
        profile.write_text("mode: atomic\nfixture/client.gen.go:1.1,2.1 1000 1\n")
        with patch.object(runner, "run") as execute, self.assertRaisesRegex(RuntimeError, "Missing handwritten"):
            runner.report_coverage(self.go, profile, "Isolated unit", {})
        execute.assert_not_called()

    def test_generation_checks_drift_without_rewriting_source(self):
        self.stamp()
        def generate(go, path, work, env):
            self.assertEqual(env["GOPROXY"], "off")
            path.write_text("different\n")
        with patch.object(runner, "generate", side_effect=generate), self.assertRaisesRegex(RuntimeError, "generation drift"):
            runner.check("check")
        self.assertEqual((runner.SDK / "client.gen.go").read_text(), "generated\n")

    def test_generate_changes_only_selected_output_and_cleans(self):
        self.stamp()
        def generate(go, path, work, env):
            path.write_text("new generated\n")
        with patch.object(runner, "generate", side_effect=generate):
            runner.check("generate")
        self.assertEqual((runner.SDK / "client.gen.go").read_text(), "new generated\n")
        self.assertEqual(list((self.root / "output/sdk").iterdir()), [])

    def test_nondeterministic_generation_rejected(self):
        self.stamp()
        def generate(go, path, work, env):
            path.write_text(path.name)
        with patch.object(runner, "generate", side_effect=generate), self.assertRaisesRegex(RuntimeError, "not deterministic"):
            runner.check("check")

    def test_source_changes_fail_generation(self):
        self.stamp()
        def generate(go, path, work, env):
            path.write_text("generated\n")
            (runner.ROOT / "api/openapi/control-v1.yaml").write_text("unexpected rewrite")
        with patch.object(runner, "generate", side_effect=generate), self.assertRaisesRegex(RuntimeError, "changed its inputs"):
            runner.check("generate")


if __name__ == "__main__":
    unittest.main()
