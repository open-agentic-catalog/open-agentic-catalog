"""Pinned Directory input checks without tool execution or sockets."""

import hashlib
import io
import json
import os
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

import agntcy_tools as tools
import agntcy_fresh
import agntcy_lab
from common import Result
import runner


class Inputs(unittest.TestCase):
    def test_fresh_inputs_do_not_change_retained_lab_admission(self):
        retained = agntcy_lab.read_json(tools.ASSETS / "lock.json")
        selected = agntcy_lab.read_json(tools.ASSETS / "deployment.json")
        self.assertEqual(retained["release"], "v1.7.1")
        self.assertEqual(retained["revision"], "7bf5795ce7f6b2c3069ba2da1a32768701412334")
        with tempfile.TemporaryDirectory() as directory:
            private = Path(directory)
            with patch.object(agntcy_lab, "selected_lab", return_value=(private, {})):
                lab = agntcy_lab.Lab("explicit-retained-lab", private)
            with patch.object(agntcy_fresh, "read_json", return_value={**selected, "revision": "fresh-only-change"}):
                fresh = agntcy_fresh.FreshLab(dict(certificate=str(private / "ca"), private=str(private), grpc="127.0.0.1:1234"))
            self.assertEqual(lab.lock, retained)
            self.assertEqual(fresh.lock["revision"], "fresh-only-change")
            self.assertNotEqual(lab.cli, fresh.cli)
            self.assertEqual(fresh.lock["cid"], retained["cid"])

    def test_inspect_uses_selected_release_chart_and_cli_version(self):
        lock = agntcy_lab.read_json(tools.ASSETS / "deployment.json")
        def command(*args, **_kwargs):
            return Result(0, lock["cli_version"] if args[-1] == "version" else "embedded dependencies", "")
        with patch.object(tools, "require_platform"), patch.object(tools.shared, "local_daemon"), \
                patch.object(tools, "source_identity", return_value="license"), patch.object(tools, "image_identity", return_value={}), \
                patch.object(tools, "file_digest", side_effect=[lock["cli_sha256"], lock["chart_sha256"]]) as digests, \
                patch.object(tools, "command", side_effect=command):
            actual = tools.inspect()
            self.assertEqual(actual["release"], lock["release"])
            self.assertEqual(digests.call_args_list[1].args[0].name, "dir-" + lock["release"] + ".tgz")

    def test_acquisition_environment_excludes_ambient_context_and_credentials(self):
        with patch.dict(os.environ, {"PATH": "/tools", "HOME": "/owned", "GIT_DIR": "/other", "KUBECONFIG": "/other", "DOCKER_HOST": "unix:///owned.sock", "AWS_SECRET_ACCESS_KEY": "private"}, clear=True):
            self.assertEqual(tools.environment(), {"PATH": "/tools", "HOME": "/owned", "DOCKER_HOST": "unix:///owned.sock"})

    def test_wrong_platform_refused_before_acquisition(self):
        with patch.object(tools.platform, "system", return_value="Windows"), patch.object(tools, "command") as command, patch.object(tools.shared, "local_daemon") as daemon:
            with self.assertRaisesRegex(ValueError, "Linux AMD64"):
                tools.install()
            command.assert_not_called()
            daemon.assert_not_called()

    def test_source_is_complete_path_sorted_and_tamper_evident(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory)
            (source / "a").mkdir()
            files = {"a.txt": b"before directory", "a/b": b"nested", "LICENSE.md": b"license"}
            digest = hashlib.sha256()
            for name, content in sorted(files.items()):
                (source / name).write_bytes(content)
                digest.update(name.encode() + b"\0" + hashlib.sha256(content).digest())
            with patch.object(tools, "read_json", return_value={"source_sha256": digest.hexdigest()}):
                self.assertEqual(tools.source_identity(source, "revision"), hashlib.sha256(b"license").hexdigest())
                (source / "unexpected").write_text("unreviewed")
                with self.assertRaisesRegex(ValueError, "differs"):
                    tools.source_identity(source, "revision")
                (source / "unexpected").unlink()
                (source / "link").symlink_to(source / "LICENSE.md")
                with self.assertRaisesRegex(ValueError, "symlink"):
                    tools.source_identity(source, "revision")

    def test_exact_image_platform_repository_and_digest(self):
        reference = "docker.io/bitnami/postgresql@sha256:exact"
        valid = {"Id": "sha256:config", "Os": "linux", "Architecture": "amd64", "RepoDigests": ["bitnami/postgresql@sha256:exact"]}
        with patch.object(tools, "command", return_value=Result(0, json.dumps([valid]), "")):
            self.assertEqual(tools.image_identity(reference)["id"], "sha256:config")
        for change in ({"Architecture": "arm64"}, {"Os": "windows"}, {"RepoDigests": ["unrelated@sha256:exact"]}, {"RepoDigests": ["bitnami/postgresql@sha256:wrong"]}):
            with self.subTest(change=change), patch.object(tools, "command", return_value=Result(0, json.dumps([{**valid, **change}]), "")), self.assertRaises(ValueError):
                tools.image_identity(reference)

    def test_inspection_rejects_changed_installation_without_acquisition(self):
        with patch.object(tools, "inspect", return_value={"revision": "current"}), patch.object(tools, "read_json", return_value={"revision": "old"}), patch.object(tools, "command") as command:
            with self.assertRaisesRegex(ValueError, "installation changed"):
                tools.check()
            command.assert_not_called()

    def test_shared_archive_extractor_accepts_only_explicit_expected_tree(self):
        content = b"fixed upstream bytes"
        data = io.BytesIO()
        with tarfile.open(fileobj=data, mode="w:gz") as archive:
            member = tarfile.TarInfo("upstream/file")
            member.size = len(content)
            archive.addfile(member, io.BytesIO(content))
        expected = hashlib.sha256(b"file\0" + hashlib.sha256(content).digest()).hexdigest()
        with tempfile.TemporaryDirectory() as directory:
            destination = Path(directory) / "source"
            runner.extract_source(data.getvalue(), destination, expected)
            self.assertEqual((destination / "file").read_bytes(), content)
            with self.assertRaisesRegex(ValueError, "digest"):
                runner.extract_source(data.getvalue(), Path(directory) / "wrong", "0" * 64)


if __name__ == "__main__":
    unittest.main()
