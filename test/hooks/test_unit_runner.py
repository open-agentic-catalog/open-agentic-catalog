"""Isolated selection and diagnostic tests; no Git, tools or subprocesses."""

import json
from pathlib import Path
import unittest

import runner


class StagedSelectionTests(unittest.TestCase):
    def test_go_scope_preserves_generated_and_dependency_exclusions(self):
        for path in ("samples/embed.go", "cmd/main.go", "catalog-ui/internal/web/web.go", "test/with spaces.go"):
            self.assertTrue(runner.is_go(path), path)
        for path in ("sdk/go/client.go", "internal/a2apb/a2a.pb.go", "schemas/upstream/example.go",
                     "samples/upstream/example.go", "catalog-ui/node_modules/example.go", "cmd/file.txt"):
            self.assertFalse(runner.is_go(path), path)

    def test_maintained_scopes(self):
        expected = {
            "test/a.py": "ruff", "extensions/vscode/tooling/a.py": "ruff",
            "examples/langgraph/app.py": "ruff", "examples/tutorials/local-oidc/service-read.py": "ruff",
            "test/hooks/pre-commit": "shellcheck", "test/functional/restore.sh": "shellcheck",
            "deployments/container/licenses.sh": "shellcheck",
            "README.md": "markdownlint", "todo/with [brackets]\nand spaces.md": "markdownlint",
            "documentation/src/content/docs/index.mdx": "markdownlint",
            "internal/adapter/testdata/upstream/README.md": "markdownlint",
            "internal/adapter/testdata/upstream/other/README.md": "markdownlint",
            "Containerfile": "hadolint", "test/interoperability/gateway/Containerfile.auth": "hadolint",
            ".github/workflows/check.yml": "actionlint", ".github/workflows/other.yaml": "actionlint",
        }
        for path, tool in expected.items():
            self.assertEqual(runner.lint_kind(path), tool, path)
        for path in ("other.py", "sdk/python/client.py", "arbitrary.sh", "examples/unused/Containerfile",
                     ".github/workflows/nested/check.yml", "schemas/upstream/README.md", "samples/upstream/README.md",
                     "specs/README.md", "output/report.md", "documentation/dist/page.md",
                     "documentation/src/content/docs/generated/page.md", "test/sdk/.tools/a.py",
                     "catalog-ui/node_modules/README.md", "examples/langgraph/.venv/a.py",
                     "test/clientcompat/go/generated/client.py"):
            self.assertIsNone(runner.lint_kind(path), path)

    def test_reviewed_source_markdown_is_not_reformatted(self):
        for path in ("openai/jupyter-notebook/SKILL.md", "huggingface/hf-cli/SKILL.md",
                     "context7/plugin/README.md", "context7/plugin/skills/context7-mcp/SKILL.md"):
            self.assertIsNone(runner.lint_kind("internal/adapter/testdata/upstream/" + path), path)

    def test_reports_only_expose_known_locations(self):
        root = Path("/private-fixture")
        fixtures = {
            "ruff": [{"filename": str(root / "test/a.py"), "code": "F401", "location": {"row": 2},
                      "message": "private source", "fix": {"content": "private source"}}],
            "shellcheck": {"comments": [{"file": "test/a.py", "line": 2, "code": 2086, "message": "private source"}]},
            "hadolint": [{"file": "test/a.py", "line": 2, "code": "DL3006", "message": "private source"}],
            "actionlint": [{"filepath": "test/a.py", "line": 2, "kind": "expression", "snippet": "private source"}],
            "markdownlint": [{"fileName": "stdin", "lineNumber": 2, "ruleNames": ["MD001"], "errorContext": "private source"}],
        }
        for tool, report in fixtures.items():
            locations = runner.lint_locations(tool, json.dumps(report), ["test/a.py"], root,
                                              "test/a.py" if tool == "markdownlint" else None)
            self.assertEqual(len(locations), 1)
            self.assertNotIn("private source", locations[0])
            self.assertEqual(json.loads(locations[0])["file"], "test/a.py")
            self.assertEqual(json.loads(locations[0])["line"], 2)

    def test_invalid_or_unattributed_findings_fail(self):
        for report in ([], {}, [{"file": "/outside/file", "line": 1, "code": "DL3006"}],
                       [{"file": "Containerfile", "line": True, "code": "DL3006"}],
                       [{"file": "Containerfile", "line": 1, "code": "source\ntext"}]):
            with self.assertRaises((ValueError, TypeError)):
                runner.lint_locations("hadolint", json.dumps(report), ["Containerfile"], Path("/fixture"))

    def test_locations_are_bounded_and_escape_filenames(self):
        name = "notes [draft]\n-file.md"
        report = [{"fileName": "stdin", "lineNumber": 1, "ruleNames": ["MD001"],
                   "errorContext": "private source", "errorDetail": "private detail"}] * 12
        locations = runner.lint_locations("markdownlint", json.dumps(report), [name], Path("/fixture"), name)
        self.assertEqual(len(locations), 10)
        for location in locations:
            self.assertNotIn("\n", location)
            self.assertNotIn("private", location)
            self.assertEqual(json.loads(location), {"file": name, "rule": "MD001", "line": 1})


if __name__ == "__main__":
    unittest.main()
