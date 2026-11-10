"""Isolated build metadata and configuration policy tests; no subprocesses."""

import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import versioning as v


class VersionTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="catalog-version-unit-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        (self.root / "VERSION").write_text("0.1.0\n")
        self.sha = "a" * 40

    def test_full_release_base_validation(self):
        for value in ("0.1.0", "10.20.30", "1.2.3-rc.1", "0.0.0-rc.99"):
            (self.root / "VERSION").write_text(value + "\n")
            self.assertEqual(v.base_version(self.root), value)
        for value in ("1.2", "01.2.3", "1.2.3-rc.0", "1.2.3-rc.01", "1.2.3\n\n", "1.2.3", " 1.2.3\n", "1.2.3+x\n", "1.2.3-rc.1evil\n"):
            (self.root / "VERSION").write_text(value)
            with self.subTest(value=value), self.assertRaises(ValueError):
                v.base_version(self.root)

    def test_tagged_untagged_modified_and_unknown_identity(self):
        for state, tags, expected in (("clean", ("v0.1.0",), "0.1.0"),
                                      ("clean", ("v0.2.0",), "0.1.0-dev+g" + self.sha[:12]),
                                      ("modified", ("v0.1.0",), "0.1.0-dev+g" + self.sha[:12]),
                                      ("unknown", (), "0.1.0-dev")):
            commit = "unknown" if state == "unknown" else self.sha
            with self.subTest(state=state, tags=tags), patch.object(v, "source", return_value=(commit, state, tags)):
                self.assertEqual(v.metadata(self.root), {"version": expected, "commit": commit, "sourceState": state})

    def test_prerelease_dev_is_valid_and_overrides_do_not_manufacture_clean_source(self):
        (self.root / "VERSION").write_text("0.1.0-rc.2\n")
        with patch.object(v, "source", return_value=(self.sha, "clean", ("v0.1.0-rc.2",))):
            self.assertEqual(v.metadata(self.root)["version"], "0.1.0-rc.2")
            result = v.metadata(self.root, commit="fixture")
            self.assertEqual(result, {"version": "0.1.0-rc.2.dev+g" + self.sha[:12], "commit": "fixture", "sourceState": "unknown"})
            self.assertEqual(v.metadata(self.root, "functional", "fixture"), {"version": "functional", "commit": "fixture", "sourceState": "unknown"})

    def test_injection_and_oversized_overrides_are_rejected_before_git(self):
        with patch.object(v, "source") as source:
            for value in ("", "a" * 129, "$(touch marker)", "x'; touch marker; '", "x\ny", "--x", "has space", "x`id`", "x/../../secret"):
                for keyword in ("version", "commit"):
                    with self.subTest(value=value, field=keyword), self.assertRaises(ValueError):
                        v.metadata(self.root, **{keyword: value})
            source.assert_not_called()

    def test_release_requires_exact_clean_tag_and_no_overrides(self):
        for state, tags in (("modified", ("v0.1.0",)), ("clean", ()), ("unknown", ("v0.1.0",)), ("clean", ("v0.2.0",))):
            with patch.object(v, "source", return_value=(self.sha, state, tags)), self.assertRaises(ValueError):
                v.release(self.root)
        with patch.object(v, "source", return_value=(self.sha, "clean", ("v0.1.0",))):
            self.assertEqual(v.release(self.root)["version"], "0.1.0")
        with self.assertRaises(ValueError):
            v.release(self.root, "0.1.0")

    def test_gitless_archive_does_not_query_a_parent_repo(self):
        with patch.object(v, "run") as run:
            self.assertEqual(v.source(self.root), ("unknown", "unknown", ()))
            run.assert_not_called()

    def test_git_failures_are_not_reported_as_clean_or_gitless(self):
        (self.root / ".git").mkdir()
        for results in ((str(self.root.parent),), (str(self.root), "bad")):
            with patch.object(v, "run", side_effect=results), self.assertRaises(ValueError):
                v.source(self.root)
        with patch.object(v, "run", side_effect=ValueError("failed Git")), self.assertRaisesRegex(ValueError, "failed Git"):
            v.source(self.root)

    def test_environment_removes_inherited_redirectors(self):
        with patch.dict(v.os.environ, {"GIT_DIR": "/elsewhere", "UV_PROJECT_ENVIRONMENT": "/personal", "BUMPVERSION_COMMIT": "true", "VIRTUAL_ENV": "/personal", "PYTHONPATH": "/private", "PATH": "/usr/bin"}, clear=True):
            self.assertEqual(v.environment(), {"PATH": "/usr/bin"})

    def test_installer_rejects_redirected_or_incomplete_environment(self):
        (self.root / ".tools").symlink_to(self.root.parent, target_is_directory=True)
        with self.assertRaises(ValueError):
            v.private_environment_path(self.root)
        (self.root / ".tools").unlink()
        (self.root / ".tools/dev").mkdir(parents=True)
        with self.assertRaisesRegex(ValueError, "incomplete"):
            v.private_environment_path(self.root)

    def test_bump_failure_checks_and_forced_mutation_controls(self):
        with patch.object(v, "check"), patch.object(v, "source", return_value=(self.sha, "clean", ())), patch.object(v, "run", side_effect=["bump-my-version, version 1.6.7", "preview"]) as run:
            v.bump(self.root, "/tool", "rc", True)
            args = run.call_args.args[0]
            self.assertTrue({"--no-commit", "--no-tag", "--no-push-tag", "--no-allow-dirty", "--dry-run"}.issubset(args))
            self.assertEqual(args[args.index("--new-version") + 1], "0.1.0-rc.1")
        with patch.object(v, "check"), patch.object(v, "source", return_value=(self.sha, "modified", ())):
            with self.assertRaisesRegex(ValueError, "clean"):
                v.bump(self.root, "/tool", "patch", False)

    def test_tool_versions_are_checked_with_the_selected_interpreter(self):
        with patch.object(v, "run", return_value=json.dumps(v.TOOL_PINS)):
            v.verify_tools("/selected/python", self.root)
        with patch.object(v, "run", return_value=json.dumps({"ruff": "wrong"})), self.assertRaises(ValueError):
            v.verify_tools("/selected/python", self.root)

    def test_manifest_pin_parity(self):
        v.check(v.ROOT)

    def test_sdk_metadata_parity_rejects_each_stale_derived_copy(self):
        import shutil
        for rule in v.bump_files()[1:]:
            target = self.root / rule["filename"]
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(v.ROOT / rule["filename"], target)
        v.sdk_version_check(self.root, "0.1.0")
        for name in ("sdk/rust/Cargo.toml", "sdk/rust/Cargo.lock", "test/sdk/rust/consumer/Cargo.toml", "test/sdk/rust/consumer/Cargo.lock", "examples/rust-catalog-report/Cargo.toml", "examples/rust-catalog-report/Cargo.lock", "sdk/python/pyproject.toml", "test/sdk/python/uv.lock", "examples/langgraph/uv.lock", "sdk/typescript/package.json", "sdk/typescript/package-lock.json", "extensions/vscode/package.json", "extensions/vscode/package-lock.json"):
            path = self.root / name
            original = path.read_text()
            path.write_text(original.replace('"0.1.0"', '"0.2.0"'))
            with self.subTest(name=name), self.assertRaisesRegex(ValueError, "version"):
                v.sdk_version_check(self.root, "0.1.0")
            path.write_text(original)

    def test_openapi_metadata_matches_the_release_base_without_processes(self):
        path = self.root / "api/openapi/control-v1.yaml"
        path.parent.mkdir(parents=True)
        for base in ("0.1.0", "0.2.0-rc.2", "1.0.0"):
            with self.subTest(base=base):
                path.write_text("openapi: 3.1.0\ninfo:\n  title: Catalog\n  version: " + base
                                + "\npaths:\n  /example:\n    version: 9.8.7\n")
                with patch.object(v, "run") as run:
                    v.openapi_version_check(self.root, base)
                run.assert_not_called()

    def test_openapi_metadata_rejects_stale_missing_or_ambiguous_versions(self):
        path = self.root / "api/openapi/control-v1.yaml"
        path.parent.mkdir(parents=True)
        for document in (
            "openapi: 3.1.0\ninfo:\n  version: 1.0.0\n",
            "openapi: 3.1.0\ninfo:\n  title: Catalog\n",
            "openapi: 3.1.0\npaths:\n  version: 0.1.0\n",
            "info:\n  version: 0.1.0\n  version: 0.1.0\n",
            "info:\n  version: 0.1.0\ninfo:\n  version: 0.1.0\n",
            'info:\n  version: "0.1.0"\n',
            "info:\n    version: 0.1.0\n",
            "info:\n  version: 0.1.0\npaths:\n  version: 0.1.0\n",
        ):
            with self.subTest(document=document):
                path.write_text(document)
                with self.assertRaisesRegex(ValueError, "OpenAPI info.version must match VERSION"):
                    v.openapi_version_check(self.root, "0.1.0")

    def test_install_rejects_wrong_uv_and_missing_or_unsupported_python(self):
        with patch.object(v, "check"), patch.object(v, "run", return_value="uv 0.9.18"), self.assertRaisesRegex(ValueError, "uv 0.13.6"):
            v.install(self.root, "/uv", "python3")
        with patch.object(v, "check"), patch.object(v, "run", return_value="uv 0.13.6"), patch.object(v.shutil, "which", return_value=None), self.assertRaisesRegex(ValueError, "installed Python"):
            v.install(self.root, "/uv", "missing")
        for version in ([3, 11], [3, 15], [4, 0]):
            with self.subTest(version=version), patch.object(v, "check"), patch.object(v, "run", side_effect=["uv 0.13.6", json.dumps(version)]) as run, patch.object(v.shutil, "which", return_value="/python"), self.assertRaisesRegex(ValueError, "installed Python"):
                v.install(self.root, "/uv", "python3")
            self.assertEqual(run.call_count, 2, "unsupported interpreter must not invoke sync")

    def test_installer_preserves_the_configured_cutoff_with_no_config(self):
        cutoff = "2026-09-15T09:10:11Z"
        (self.root / "pyproject.toml").write_text('[tool.uv]\nexclude-newer = "' + cutoff + '"\n')
        with patch.object(v, "check"), patch.object(v, "run", side_effect=["uv 0.13.6", "[3, 13]", ""]) as run, \
             patch.object(v.shutil, "which", return_value="/selected/python"), patch.object(v, "environment", return_value={}), \
             patch.object(v, "verify_tools") as verify:
            v.install(self.root, "/pinned/uv", "python3")
        command = run.call_args.args[0]
        self.assertEqual(command[:2], ["/pinned/uv", "sync"])
        self.assertTrue({"--no-config", "--locked", "--no-python-downloads", "--no-build"}.issubset(command))
        self.assertEqual(command[command.index("--exclude-newer") + 1], cutoff)
        self.assertEqual(command[command.index("--only-group") + 1], "dev")
        self.assertEqual(command[command.index("--python") + 1], "/selected/python")
        self.assertEqual(run.call_args.kwargs["env"], {"UV_PROJECT_ENVIRONMENT": str(self.root / ".tools/dev")})
        verify.assert_called_once_with(self.root / ".tools/dev/bin/python", self.root)
