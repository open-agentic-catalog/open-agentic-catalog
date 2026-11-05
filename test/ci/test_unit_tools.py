"""Isolated boundaries for CI acquisition policy; these tests acquire no tools."""

import hashlib
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import tools


class ChecksumPolicyTests(unittest.TestCase):
    def test_exact_asset_ignores_other_platforms(self):
        checksum = "ab" * 32
        manifest = "cd" * 32 + "  tool-arm64.tar.gz\n" + checksum + "  tool-amd64.tar.gz\n"
        self.assertEqual(tools.manifest_checksum(manifest, "tool-amd64.tar.gz"), checksum)

    def test_single_digest_and_binary_manifest_forms(self):
        checksum = "ab" * 32
        for value in (checksum, checksum + " *./tool-amd64.tar.gz"):
            with self.subTest(value=value):
                self.assertEqual(tools.manifest_checksum(value, "tool-amd64.tar.gz"), checksum)

    def test_missing_ambiguous_and_invalid_digests_fail_closed(self):
        checksum = "ab" * 32
        for value in (checksum + "  other.tar.gz", "not-a-digest  tool.tar.gz",
                      (checksum + "  tool.tar.gz\n") * 2, ""):
            with self.subTest(value=value), self.assertRaisesRegex(ValueError, "unique exact asset"):
                tools.manifest_checksum(value, "tool.tar.gz")


class WorkflowAdmissionTests(unittest.TestCase):
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

    def test_markdown_profiles_preserve_pins_cutoff_and_tool_scope(self):
        for profile, actual_version in (("quality", "9.9.9"), ("docs", "9.9.9"), ("docs", "9.9.8")):
            with self.subTest(profile=profile, actual_version=actual_version):
                def command(arguments, **_kwargs):
                    return actual_version if arguments == ["markdownlint", "--version"] else ""

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
                    patch.dict(tools.PINS, {"markdownlint-cli": "9.9.9", "dependencyCutoff": "2020-01-01T00:00:00Z"}),
                    patch.dict(tools.os.environ, {"GITHUB_ACTIONS": "true"}),
                    patch.object(tools, "run", side_effect=command) as execute,
                ):
                    if actual_version == "9.9.9":
                        tools.install(profile)
                    else:
                        with self.assertRaisesRegex(ValueError, "Markdown CLI version mismatch"):
                            tools.install(profile)
                    installs = [call.args[0] for call in execute.call_args_list if call.args[0][:2] == ["npm", "install"]]
                    self.assertEqual(len(installs), 1)
                    self.assertIn("--before=2020-01-01T00:00:00Z", installs[0])
                    self.assertIn("markdownlint-cli@9.9.9", installs[0])
                    self.assertIn("--ignore-scripts", installs[0])
                    self.assertIn("--no-audit", installs[0])
                    go.assert_called_once_with()
                    python.assert_called_once_with(profile)
                    node.assert_called_once_with("documentation")
                    if profile == "docs":
                        binaries.assert_not_called()
                        actionlint.assert_not_called()
                        self.assertNotIn(profile, tools.SOURCE_TOOLS)
                        self.assertNotIn(profile, tools.DOCKER)

    def test_python_install_uses_pinned_yaml_dependencies(self):
        def command(arguments, **_kwargs):
            if str(arguments[0]).endswith("/uv"):
                return "uv 0.12.21"
            if str(arguments[0]).endswith("/yamllint"):
                return "yamllint 1.2.3"
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
                    patch.dict(tools.PINS, {"yamllint": "1.2.3", "pyyaml": "6.0.99", "pathspec": "1.0.99"}),
                    patch.object(tools, "run", side_effect=command) as execute,
                ):
                    tools.python_install(profile)
                    installs = [call.args[0] for call in execute.call_args_list if "--only-binary=:all:" in call.args[0]]
                    self.assertEqual(len(installs), 1)
                    expected = ["uv==0.12.21"]
                    if profile == "quality":
                        expected += ["yamllint==1.2.3", "PyYAML==6.0.99", "pathspec==1.0.99"]
                    self.assertEqual(installs[0][installs[0].index("--only-binary=:all:") + 1:], expected)


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
                patch.dict(tools.PINS, {"compose": "5.6.0"}),
                patch.object(tools, "checked_asset", side_effect=lambda _url, _manifest, path: path.write_bytes(b"compose fixture")) as acquire,
                patch.object(tools, "run", return_value="5.6.0\n") as execute,
            ):
                tools.binary_install("compose")
                destination = root / "home/.docker/cli-plugins/docker-compose"
                self.assertEqual(destination.read_bytes(), b"compose fixture")
                self.assertEqual(destination.stat().st_mode & 0o777, 0o755)
                self.assertEqual(acquire.call_args.args[:2], (
                    "https://github.com/docker/compose/releases/download/v5.6.0/docker-compose-linux-x86_64",
                    "https://github.com/docker/compose/releases/download/v5.6.0/checksums.txt",
                ))
                execute.assert_called_once_with(["docker", "compose", "version", "--short"], capture=True)

    def test_shellcheck_checksum_mismatch_prevents_extraction(self):
        with (
            tempfile.TemporaryDirectory() as directory,
            patch.object(tools, "OWNED", Path(directory)),
            patch.dict(tools.PINS, {"shellcheckSha256": "00" * 32}),
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
                patch.dict(tools.PINS, {"shellcheck": "0.11.0", "shellcheckSha256": hashlib.sha256(b"archive").hexdigest()}),
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
