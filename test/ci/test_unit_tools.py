"""Isolated boundaries for CI acquisition policy; these tests acquire no tools."""

import hashlib
import json
from pathlib import Path
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import tools


class ChecksumPolicyTests(unittest.TestCase):
    def test_archive_pin_binds_name_version_and_exact_filename(self):
        checksum = "ab" * 32
        with patch.dict(tools.PINS, {"archives": {"tool": {"version": "1.2.3", "filename": "tool-linux.tar.gz", "sha256": checksum}}}):
            self.assertEqual(tools.archive_checksum("tool", "1.2.3", "tool-linux.tar.gz"), checksum)
            for selection in (("other", "1.2.3", "tool-linux.tar.gz"), ("tool", "1.2.4", "tool-linux.tar.gz"), ("tool", "1.2.3", "tool-arm64.tar.gz")):
                with self.subTest(selection=selection), self.assertRaisesRegex(ValueError, "no reviewed archive"):
                    tools.archive_checksum(*selection)

    def test_download_uses_only_retained_digest_and_rejects_substituted_bytes(self):
        expected = hashlib.sha256(b"reviewed").hexdigest()
        for payload, accepted in ((b"reviewed", True), (b"replacement with matching remote manifest", False)):
            with tempfile.TemporaryDirectory() as directory, self.subTest(accepted=accepted):
                target = Path(directory) / "tool.tar.gz"
                with patch.object(tools, "download", side_effect=lambda _url, path: path.write_bytes(payload)) as download:
                    if accepted:
                        tools.checked_asset("https://publisher.invalid/tool", expected, target)
                    else:
                        with self.assertRaisesRegex(ValueError, "checksum mismatch"):
                            tools.checked_asset("https://publisher.invalid/tool", expected, target)
                    download.assert_called_once_with("https://publisher.invalid/tool", target)

    def test_missing_or_invalid_digest_refuses_before_network(self):
        for value in ("", "not-a-hash", "ab" * 31, None):
            with self.subTest(value=value), patch.object(tools, "download") as acquire:
                with self.assertRaisesRegex(ValueError, "missing reviewed"):
                    tools.checked_asset("https://publisher.invalid/tool", value, Path("unused"))
                acquire.assert_not_called()

    def test_go_version_drift_and_bad_bytes_never_unpack_or_execute(self):
        for reviewed in (False, True):
            with tempfile.TemporaryDirectory() as directory, self.subTest(reviewed=reviewed):
                root = Path(directory)
                (root / "go.mod").write_text("module example.invalid\ngo 1.2.3\n")
                pin = {"version": "1.2.3" if reviewed else "1.2.2", "filename": "go1.2.3.linux-amd64.tar.gz", "sha256": "00" * 32}
                with patch.object(tools, "ROOT", root), patch.object(tools, "OWNED", root), patch.dict(tools.PINS, {"archives": {"go": pin}}), patch.object(tools, "download", side_effect=lambda _url, path: path.write_bytes(b"untrusted")) as acquire, patch.object(tools, "unpack") as unpack, patch.object(tools, "run") as execute:
                    with self.assertRaisesRegex(ValueError, "checksum mismatch" if reviewed else "no reviewed archive"):
                        tools.go_install()
                    self.assertEqual(acquire.call_count, int(reviewed))
                    unpack.assert_not_called()
                    execute.assert_not_called()
                    self.assertFalse((root / "go-1.2.3").exists())


class WorkflowAdmissionTests(unittest.TestCase):
    def test_ci_npm_aliases_preserve_node_distribution_and_refuse_existing_commands(self):
        for existing in (False, True):
            with tempfile.TemporaryDirectory() as directory, self.subTest(existing=existing):
                root = Path(directory).resolve()
                project = root / "documentation"
                project.mkdir()
                (project / ".nvmrc").write_text("22.23.4")
                (project / "package.json").write_text(json.dumps({"packageManager": "npm@12.2.2"}))
                (root / "test/sdk").mkdir(parents=True)
                (root / "test/sdk/node-runtimes.json").write_text(json.dumps({"22.23.4": {"node-v22.23.4-linux-x64.tar.gz": "ab" * 32}}))
                owned = root / "owned"
                (owned / "bin").mkdir(parents=True)
                node_root = owned / "node-v22.23.4-linux-x64"
                bundled = node_root / "lib/node_modules/npm/bin/npm-cli.js"
                cli = owned / "npm-12.2.2/node_modules/npm/bin/npm-cli.js"

                def unpack(_archive, stage):
                    unpacked = stage / node_root.name
                    (unpacked / "bin").mkdir(parents=True)
                    (unpacked / "bin/node").write_text("reviewed Node")
                    source = unpacked / "lib/node_modules/npm/bin/npm-cli.js"
                    source.parent.mkdir(parents=True)
                    source.write_text("bundled npm remains unchanged")

                def install_npm(node, version, destination, *, env):
                    self.assertEqual((node, version, destination), (node_root / "bin/node", "12.2.2", owned))
                    cli.parent.mkdir(parents=True)
                    cli.write_text("selected npm")
                    cli.with_name("npx-cli.js").write_text("selected npx")
                    return cli

                if existing:
                    (owned / "bin/npm").write_text("retained command")
                helper = SimpleNamespace(install_npm=unittest.mock.Mock(side_effect=install_npm))
                with patch.object(tools, "ROOT", root), patch.object(tools, "OWNED", owned), patch.object(tools, "unpack", side_effect=unpack), patch.object(tools, "checked_asset") as acquire, patch.object(tools, "publish_path"), patch.object(tools, "run", side_effect=("v22.23.4", "12.2.2")) as execute, patch.object(sys, "path", list(sys.path)), patch.dict(sys.modules, {"node_runtime": helper}):
                    if existing:
                        with self.assertRaisesRegex(ValueError, "owned npm command"):
                            tools.node_install("documentation")
                        acquire.assert_not_called()
                        helper.install_npm.assert_not_called()
                        execute.assert_not_called()
                        self.assertEqual((owned / "bin/npm").read_text(), "retained command")
                    else:
                        tools.node_install("documentation")
                        self.assertEqual((owned / "bin/npm").resolve(), cli)
                        self.assertEqual((owned / "bin/npx").resolve(), cli.with_name("npx-cli.js"))
                        self.assertEqual(bundled.read_text(), "bundled npm remains unchanged")
                        self.assertEqual((node_root / "bin/node").read_text(), "reviewed Node")
                        self.assertEqual(execute.call_count, 2)

    def test_actionlint_install_disables_ambient_telemetry(self):
        with (
            tempfile.TemporaryDirectory() as directory,
            patch.object(tools, "OWNED", Path(directory)),
            patch.dict(tools.os.environ, {"GOTELEMETRY": "on"}),
            patch.object(tools, "run") as execute,
            patch.object(tools, "actionlint_check"),
        ):
            tools.actionlint_install()
            self.assertEqual(execute.call_args.kwargs["env"]["GOTELEMETRY"], "off")
            self.assertEqual(execute.call_args.kwargs["env"]["GOBIN"], str(Path(directory) / "bin"))

    def test_missing_shellcheck_refuses_incomplete_validation_before_execution(self):
        with (
            patch("tools.Path.is_file", return_value=True),
            patch("tools.shutil.which", return_value=None),
            patch("tools.run") as command,
        ):
            with self.assertRaisesRegex(ValueError, "ShellCheck is missing"):
                tools.actionlint_check()
            command.assert_not_called()

    def test_markdown_profiles_preserve_tool_scope(self):
        for profile in ("quality", "docs", "unit"):
            with self.subTest(profile=profile):
                with (
                    tempfile.TemporaryDirectory() as directory,
                    patch.object(tools, "OWNED", Path(directory)),
                    patch.object(tools, "preflight"),
                    patch.object(tools, "go_install") as go,
                    patch.object(tools, "python_install") as python,
                    patch.object(tools, "node_install") as node,
                    patch.object(tools, "publish_path"),
                    patch.object(tools, "binary_install") as binaries,
                    patch.object(tools, "actionlint_install") as actionlint,
                    patch.object(tools, "markdown_install") as markdown,
                    patch.dict(tools.os.environ, {"GITHUB_ACTIONS": "true"}),
                ):
                    tools.install(profile)
                    self.assertEqual(markdown.call_count, int(profile != "unit"))
                    go.assert_called_once_with()
                    python.assert_called_once_with(profile)
                    if profile != "unit":
                        node.assert_called_once_with("documentation")
                    else:
                        node.assert_not_called()
                    if profile == "docs":
                        binaries.assert_not_called()
                        actionlint.assert_not_called()
                        self.assertNotIn(profile, tools.SOURCE_TOOLS)
                        self.assertNotIn(profile, tools.DOCKER)

    def test_python_bootstrap_uses_hashes_and_uv_only_for_every_profile(self):
        def command(arguments, **_kwargs):
            if str(arguments[0]).endswith("/uv"):
                return "uv 0.12.21"
            if "-c" in arguments:
                return "0.12.21"
            return ""

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "pyproject.toml").write_text('[tool.uv]\nrequired-version = "==0.12.21"\n')
            for profile in ("quality", "docs"):
                with (
                    self.subTest(profile=profile),
                    patch.object(tools, "ROOT", root),
                    patch.object(tools, "OWNED", root / ".tools"),
                    patch.object(tools, "publish_path"),
                    patch.dict(tools.os.environ, {"PIP_EXTRA_INDEX_URL": "https://untrusted.invalid/", "PYTHONPATH": "/untrusted"}),
                    patch.object(tools, "run", side_effect=command) as execute,
                ):
                    tools.python_install(profile)
                    installs = [call.args[0] for call in execute.call_args_list if "--only-binary=:all:" in call.args[0]]
                    self.assertEqual(len(installs), 1)
                    self.assertIn("--require-hashes", installs[0])
                    self.assertEqual(installs[0][-2:], ["-r", root / "test/ci/uv/requirements.txt"])
                    self.assertFalse(any("yaml" in str(value).lower() for value in installs[0]))
                    environment = execute.call_args_list[1].kwargs["env"]
                    self.assertEqual(environment["PIP_CONFIG_FILE"], tools.os.devnull)
                    self.assertNotIn("PIP_EXTRA_INDEX_URL", environment)
                    self.assertNotIn("PYTHONPATH", environment)

    def test_python_bootstrap_refuses_existing_environment_without_execution(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "python").mkdir()
            sentinel = root / "python/retained"
            sentinel.write_text("keep")
            with patch.object(tools, "OWNED", root), patch.object(tools, "run") as execute:
                with self.assertRaisesRegex(ValueError, "fresh environment"):
                    tools.python_install("quality")
                execute.assert_not_called()
                self.assertEqual(sentinel.read_text(), "keep")

    def test_failed_python_bootstrap_removes_only_new_environment(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "pyproject.toml").write_text('[tool.uv]\nrequired-version = "==0.13.6"\n')
            retained = root / "retained"
            retained.write_text("keep")

            def command(arguments, **_kwargs):
                if "venv" in arguments:
                    (root / "python").mkdir()
                else:
                    raise ValueError("hash refusal")

            with patch.object(tools, "ROOT", root), patch.object(tools, "OWNED", root), patch.object(tools, "run", side_effect=command), patch.object(tools, "publish_path") as publish:
                with self.assertRaisesRegex(ValueError, "hash refusal"):
                    tools.python_install("quality")
                self.assertFalse((root / "python").exists())
                self.assertEqual(retained.read_text(), "keep")
                publish.assert_not_called()

    def test_python_metadata_drift_refuses_before_executing_uv(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "pyproject.toml").write_text('[tool.uv]\nrequired-version = "==0.13.6"\n')

            def command(arguments, **_kwargs):
                if "venv" in arguments:
                    (root / "python").mkdir()
                return "0.13.5" if "-c" in arguments else ""

            with patch.object(tools, "ROOT", root), patch.object(tools, "OWNED", root), patch.object(tools, "run", side_effect=command) as execute, patch.object(tools, "publish_path") as publish:
                with self.assertRaisesRegex(ValueError, "metadata differs"):
                    tools.python_install("quality")
                self.assertFalse(any(str(call.args[0][0]).endswith("/uv") for call in execute.call_args_list))
                self.assertFalse((root / "python").exists())
                publish.assert_not_called()


class MarkdownAcquisitionTests(unittest.TestCase):
    def test_locked_install_checks_version_and_cleans_failed_staging(self):
        for actual in ("0.50.0", "0.49.1"):
            with tempfile.TemporaryDirectory() as directory, self.subTest(actual=actual):
                root = Path(directory)
                project = root / "test/ci/markdown"
                project.mkdir(parents=True)
                package = {"private": True, "dependencies": {"markdownlint-cli": "0.50.0"}}
                lock = {"packages": {"": package, "node_modules/tool": {"resolved": "https://registry.npmjs.org/tool/-/tool.tgz", "integrity": "sha512-reviewed"}}}
                for name, value in (("package.json", package), ("package-lock.json", lock)):
                    (project / name).write_text(json.dumps(value))
                owned = root / "owned"
                owned.mkdir()
                sentinel = owned / "retained"
                sentinel.write_text("keep")

                def command(arguments, **options):
                    if arguments[:2] == ["npm", "ci"]:
                        self.assertEqual(json.loads((options["cwd"] / "package-lock.json").read_text()), lock)
                        return ""
                    return actual

                with patch.object(tools, "ROOT", root), patch.object(tools, "OWNED", owned), patch.object(tools, "run", side_effect=command) as execute, patch.object(tools, "publish_path") as publish:
                    if actual == "0.50.0":
                        tools.markdown_install()
                        self.assertEqual(json.loads((owned / "markdown/package-lock.json").read_text()), lock)
                        publish.assert_called_once_with(owned / "markdown/node_modules/.bin")
                    else:
                        with self.assertRaisesRegex(ValueError, "version mismatch"):
                            tools.markdown_install()
                        self.assertFalse((owned / "markdown").exists())
                        publish.assert_not_called()
                    self.assertEqual(execute.call_args_list[0].args[0], ["npm", "ci", "--ignore-scripts", "--no-audit", "--no-fund"])
                    self.assertEqual(sentinel.read_text(), "keep")
                    self.assertFalse(list(owned.glob("markdown-acquire-*")))

    def test_unexpected_lock_source_refuses_before_install(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            project = root / "test/ci/markdown"
            project.mkdir(parents=True)
            (project / "package.json").write_text(json.dumps({"private": True, "dependencies": {"markdownlint-cli": "0.50.0"}}))
            for bundled in (False, True):
                (project / "package-lock.json").write_text(json.dumps({"packages": {"node_modules/tool": {"resolved": "https://untrusted.invalid/tool.tgz", "integrity": "sha512-hash", "inBundle": bundled}}}))
                with self.subTest(bundled=bundled), patch.object(tools, "ROOT", root), patch.object(tools, "run") as execute:
                    with self.assertRaisesRegex(ValueError, "unexpected source"):
                        tools.markdown_install()
                    execute.assert_not_called()


class BinaryAcquisitionTests(unittest.TestCase):
    def test_local_compose_install_refuses_before_home_path_or_download(self):
        with (
            patch.dict(tools.os.environ, {"GITHUB_ACTIONS": ""}),
            patch.object(tools.Path, "home") as home,
            patch.object(tools, "checked_asset") as acquire,
            patch.object(tools.tempfile, "TemporaryDirectory") as temporary,
            patch.object(tools, "run") as execute,
        ):
            with self.assertRaisesRegex(ValueError, "explicitly selected hosted job"):
                tools.binary_install("compose")
            home.assert_not_called()
            acquire.assert_not_called()
            temporary.assert_not_called()
            execute.assert_not_called()

    def test_hosted_compose_install_preserves_plugin_path_and_version_check(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            owned = root / "owned"
            owned.mkdir()
            with (
                patch.object(tools, "OWNED", owned),
                patch.object(tools.Path, "home", return_value=root / "home"),
                patch.dict(tools.os.environ, {"GITHUB_ACTIONS": "true"}),
                patch.dict(tools.PINS, {"compose": "5.6.1"}),
                patch.object(tools, "checked_asset", side_effect=lambda _url, _checksum, path: path.write_bytes(b"compose fixture")) as acquire,
                patch.object(tools, "run", return_value="5.6.1\n") as execute,
            ):
                tools.binary_install("compose")
                destination = root / "home/.docker/cli-plugins/docker-compose"
                self.assertEqual(destination.read_bytes(), b"compose fixture")
                self.assertEqual(destination.stat().st_mode & 0o777, 0o755)
                self.assertEqual(acquire.call_args.args[:2], (
                    "https://github.com/docker/compose/releases/download/v5.6.1/docker-compose-linux-x86_64",
                    tools.PINS["archives"]["compose"]["sha256"],
                ))
                execute.assert_called_once_with(["docker", "compose", "version", "--short"], capture=True)

    def test_shellcheck_checksum_mismatch_prevents_extraction(self):
        with (
            tempfile.TemporaryDirectory() as directory,
            patch.object(tools, "OWNED", Path(directory)),
            patch.dict(tools.PINS, {"archives": {"shellcheck": {"version": "0.11.0", "filename": "shellcheck-v0.11.0.linux.x86_64.tar.xz", "sha256": "00" * 32}}}),
            patch.object(tools, "download", side_effect=lambda _url, path: path.write_bytes(b"wrong archive")),
            patch.object(tools, "unpack") as unpack,
            patch.object(tools, "run") as execute,
        ):
            with self.assertRaisesRegex(ValueError, "checksum mismatch"):
                tools.binary_install("shellcheck")
            unpack.assert_not_called()
            execute.assert_not_called()
            self.assertFalse((Path(directory) / "bin/shellcheck").exists())

    def test_shellcheck_installs_in_owned_directory_and_checks_exact_version(self):
        def unpack(_archive, destination):
            member = destination / "shellcheck-v0.11.0/shellcheck"
            member.parent.mkdir()
            member.write_bytes(b"shellcheck fixture")

        for actual, accepted in [("version: 0.11.0\n", True), ("version: 0.11.0.1\n", False)]:
            with (
                self.subTest(actual=actual),
                tempfile.TemporaryDirectory() as directory,
                patch.object(tools, "OWNED", Path(directory)),
                patch.dict(tools.PINS, {"shellcheck": "0.11.0", "archives": {"shellcheck": {"version": "0.11.0", "filename": "shellcheck-v0.11.0.linux.x86_64.tar.xz", "sha256": hashlib.sha256(b"archive").hexdigest()}}}),
                patch.object(tools, "download", side_effect=lambda _url, path: path.write_bytes(b"archive")) as download,
                patch.object(tools, "unpack", side_effect=unpack),
                patch.object(tools, "run", return_value=actual) as execute,
            ):
                if accepted:
                    tools.binary_install("shellcheck")
                else:
                    with self.assertRaisesRegex(ValueError, "version mismatch"):
                        tools.binary_install("shellcheck")
                destination = Path(directory) / "bin/shellcheck"
                self.assertEqual(destination.read_bytes(), b"shellcheck fixture")
                self.assertEqual(destination.stat().st_mode & 0o777, 0o755)
                self.assertEqual(download.call_args.args[0], "https://github.com/koalaman/shellcheck/releases/download/v0.11.0/shellcheck-v0.11.0.linux.x86_64.tar.xz")
                execute.assert_called_once_with([destination, "--version"], capture=True)


class CapacityPolicyTests(unittest.TestCase):
    def test_rust_rejects_standard_runner_storage(self):
        with self.assertRaisesRegex(ValueError, "runner with sufficient capacity"):
            tools.resource_budget("rust", 14 << 30, 16 << 30)

    def test_other_jobs_reject_low_memory(self):
        with self.assertRaisesRegex(ValueError, "insufficient"):
            tools.resource_budget("quality", 30 << 30, 1 << 30)

    def test_budget_accepts_adequate_capacity(self):
        tools.resource_budget("unit", 4 << 30, 2 << 30)


if __name__ == "__main__":
    unittest.main()
