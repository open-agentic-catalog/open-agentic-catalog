import fcntl
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import yaml

from fixture import EXAMPLE, Fixture, NAMESPACE, ROOT, SOURCE

spec = importlib.util.spec_from_file_location("delivery", EXAMPLE / "deliver.py")
delivery = importlib.util.module_from_spec(spec)
spec.loader.exec_module(delivery)


class CommittedDraftTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.fixture = Fixture(Path(directory.name), ROOT / "bin/catalogctl")
        self.args = ["--namespace-id", NAMESPACE]

    def test_commit_only_skill_preview(self):
        f = self.fixture
        artifact = f.add_skill()
        self.assertNotEqual(f.commit, artifact)
        args = [*self.args, "--package-directory", "1=review-notes"]
        first = json.loads(f.run("preview", *args).stdout)
        self.assertTrue(first["locallyValid"])
        script = f.draft.parent / "review-notes/scripts/example.sh"
        script.write_text("uncommitted")
        script.chmod(0o600)
        self.assertEqual(json.loads(f.run("preview", *args).stdout), first)
        f.git("restore", script.relative_to(f.repo))
        script.chmod(0o644)
        commit = f.save("Remove executable mode")
        changed = json.loads(f.run("preview", *args, commit=commit).stdout)
        self.assertNotEqual(first["resources"][0]["commands"], changed["resources"][0]["commands"])

    def test_invalid_package_inputs(self):
        f = self.fixture
        f.add_skill()
        for path in ("../outside", "missing", "/absolute"):
            with self.subTest(path=path):
                f.run("preview", *self.args, "--package-directory", "1="+path, success=False)
        sidecar = f.draft.parent / "review-notes/sidecar"
        sidecar.symlink_to("SKILL.md")
        for name in ("link", "lfs"):
            if name == "lfs":
                sidecar.unlink()
                sidecar.write_text("version https://git-lfs.github.com/spec/v1\noid sha256:" + "1"*64)
            f.commit = f.save(name)
            f.run("preview", *self.args, "--package-directory", "1=review-notes", success=False)

    def test_source_and_push_admission(self):
        f = self.fixture
        f.git("switch", "-qc", "pr")
        (f.repo / "extra").write_text("first")
        intermediate = f.save("Superseded PR")
        f.run("preview", *self.args, commit=intermediate)
        f.git("switch", "-q", "main")
        self.assertIn("exact approved branch tip", f.run("apply", *self.args, commit=intermediate, success=False).stderr)
        f.git("merge", "--no-ff", "-qm", "Reviewed merge", "pr")
        self.assertIn("exact approved branch tip", f.run("apply", *self.args, commit=intermediate, success=False).stderr)
        event = f.root / "push.json"
        approved = {"repository": {"html_url": SOURCE.removesuffix(".git")}, "after": f.commit,
                    "ref": "refs/heads/main", "deleted": False}
        for override in ({"deleted": True}, {"ref": "refs/heads/pr"}, {"after": intermediate},
                         {"repository": {"html_url": "https://git.example.com/other"}}):
            event.write_text(json.dumps(approved | override))
            self.assertIn("push did not admit", f.run("apply", *self.args, "--push-event", event, success=False).stderr)
        self.assertEqual(list(f.state.iterdir()), [])
        event.write_text(json.dumps(approved))
        # The unreachable loopback API separates a valid older push from source refusal.
        self.assertIn("import incomplete", f.run("apply", *self.args, "--push-event", event, success=False).stderr)
        self.assertEqual((f.state / "pending").read_text().strip(), f.commit)
        self.assertIn("missing original", f.run("resume", *self.args, success=False).stderr)
        self.assertIn("already attempted", f.run("apply", *self.args, success=False).stderr)

    def test_deleted_file_leaves_delivery_unstarted(self):
        f = self.fixture
        f.draft.unlink()
        f.commit = f.save("Delete selected draft")
        result = f.run("apply", *self.args, success=False)
        self.assertIn("absent from selected commit", result.stderr)
        self.assertEqual(list(f.state.iterdir()), [])

    def test_operator_selects_destination_even_if_file_is_a_native_manifest(self):
        f = self.fixture
        manifest = yaml.safe_load((ROOT / "examples/native-import/import.yaml").read_text())
        agent = manifest["resources"][0]
        rep, = agent["representations"]
        del rep["dataFile"]
        rep["url"] = "https://example.org/agent-card.json"
        f.draft.write_text(json.dumps({"format": 1, "resources": [agent]}))
        f.commit = f.save("A native manifest carries its own destination")
        for action in ("preview", "apply"):
            with self.subTest(action=action):
                result = f.run(action, success=False)
                self.assertIn("select a destination Namespace or exact Resource", result.stderr)
        self.assertEqual(list(f.state.iterdir()), [])
        # With explicit destination flags, catalogctl's reusable-file contract owns validation.
        f.run("preview", *self.args, success=False)

    def test_path_is_relative_to_the_git_root(self):
        f = self.fixture
        f.run("preview", *self.args)
        for action in ("preview", "apply"):
            result = f.run(action, *self.args, repo=f.draft.parent, file="agent.json", success=False)
            self.assertIn("select the root of the Git repository", result.stderr)
        self.assertEqual(list(f.state.iterdir()), [])

    def test_tamper_and_concurrent_writer(self):
        f = self.fixture
        lock = os.open(f.state, os.O_DIRECTORY)
        try:
            fcntl.flock(lock, fcntl.LOCK_EX)
            self.assertIn("holds this destination lock", f.run("apply", *self.args, success=False).stderr)
        finally:
            os.close(lock)
        self.assertEqual(list(f.state.iterdir()), [])
        args = type("Inputs", (), {"action": "preview", "file": "resources/agent.json", "repo": f.repo, "commit": f.commit,
                                  "package_directory": None})()
        staged = f.root / "staged"
        staged.mkdir()
        expected = delivery.stage(args, staged)
        self.assertEqual(delivery.input_digest(staged, args.file), expected)
        (staged / args.file).write_text("changed")
        self.assertNotEqual(delivery.input_digest(staged, args.file), expected)
        (staged / "extra").mkdir()
        with self.assertRaises(delivery.Refusal):
            delivery.input_digest(staged, args.file)

    def test_altered_staging_refused_before_cli_or_receipt(self):
        f = self.fixture
        real_stage = delivery.stage

        def alter_staging(args, directory):
            digest = real_stage(args, directory)
            (directory / args.file).write_text("changed after extraction")
            return digest

        command = ["deliver.py", "apply", "--repo", str(f.repo), "--commit", f.commit, "--file",
                   "resources/agent.json", "--cli", str(f.cli), "--source", SOURCE, "--ref", "refs/heads/main",
                   "--state-dir", str(f.state), "--base-url", "http://127.0.0.1:1", "--token-file", str(f.token), *self.args]
        with patch.object(delivery, "stage", alter_staging), patch.object(delivery.sys, "argv", command):
            with self.assertRaisesRegex(delivery.Refusal, "staged inputs differ"):
                delivery.main()
        self.assertEqual(list(f.state.iterdir()), [])

    def test_workflow_trust_and_recovery_selection(self):
        preview, online = (yaml.load((EXAMPLE / name).read_text(), Loader=yaml.BaseLoader)
                           for name in ("preview.yml", "deliver.yml"))
        self.assertEqual(list(preview["on"]), ["pull_request"])
        self.assertEqual(preview["permissions"], {"contents": "read"})
        job = preview["jobs"]["preview"]
        self.assertEqual(job["runs-on"], "ubuntu-24.04")
        checkout, install, check = job["steps"]
        self.assertRegex(checkout["uses"], r"^actions/checkout@[a-f0-9]{40}$")
        self.assertEqual(checkout["with"]["ref"], "${{ github.event.pull_request.base.sha }}")
        self.assertEqual(checkout["with"]["persist-credentials"], "false")
        self.assertIn('sha256sum -c -', install["run"])
        self.assertEqual(check["env"]["HEAD_SHA"], "${{ github.event.pull_request.head.sha }}")
        self.assertEqual(online["on"], {"push": {"branches": ["main"], "paths": preview["on"]["pull_request"]["paths"]}})
        self.assertEqual(online["concurrency"]["cancel-in-progress"], "false")
        trusted = online["jobs"]["deliver"]
        self.assertEqual(trusted["runs-on"], {"group": "catalog-draft-delivery"})
        self.assertIn("!github.event.deleted", trusted["if"])
        self.assertEqual(online["permissions"], {"contents": "read"})
        self.assertNotIn("CATALOG_NAMESPACE_ID", json.dumps(trusted["steps"][0]["env"]))
        self.assertEqual(trusted["steps"][0]["env"]["COMMIT"], "${{ github.event.after }}")
        self.assertIn('action=resume', trusted["steps"][0]["run"])

    def test_workflow_commands_fetch_the_selected_commit_and_recover_without_git(self):
        f = self.fixture
        runner = f.root / "runner"
        runner.mkdir()
        automation = f.root / "automation/catalog"
        automation.mkdir(parents=True)
        (automation / "deliver.py").symlink_to(EXAMPLE / "deliver.py")
        tools = runner / "catalog-preview-cli"
        tools.mkdir()
        (tools / "catalogctl").symlink_to(f.cli)
        (tools / "deliver.py").symlink_to(EXAMPLE / "deliver.py")
        f.git("update-ref", "refs/pull/12/head", f.commit)
        event = runner / "event.json"
        event.write_text(json.dumps({"deleted": False, "after": f.commit, "ref": "refs/heads/main",
                                     "repository": {"html_url": SOURCE.removesuffix(".git")}}))
        # The scripts fetch real Git objects. Only the fixture's file transport is available.
        env = {"PATH": os.defpath, "HOME": str(runner), "RUNNER_TEMP": str(runner),
               "GIT_CONFIG_NOSYSTEM": "1", "GIT_ALLOW_PROTOCOL": "file", "GIT_CONFIG_COUNT": "1",
               "GIT_CONFIG_KEY_0": f"url.{f.repo.as_uri()}.insteadOf", "GIT_CONFIG_VALUE_0": SOURCE,
               "GITHUB_SERVER_URL": "https://git.example.com", "GITHUB_REPOSITORY": "team/resources",
               "GH_TOKEN": "synthetic-read-only-fixture", "PR_NUMBER": "12", "HEAD_SHA": f.commit}

        def run_step(workflow, **overrides):
            document = yaml.load((EXAMPLE / (workflow + ".yml")).read_text(), Loader=yaml.BaseLoader)
            script = document["jobs"][workflow]["steps"][-1]["run"]
            return subprocess.run(["bash", "-c", script], cwd=f.root, env=env | overrides,
                                  text=True, capture_output=True, timeout=20, check=False)

        preview = run_step("preview")
        self.assertEqual(preview.returncode, 0, preview.stderr)
        self.assertTrue(json.loads(preview.stdout)["locallyValid"])
        # An updated PR ref cannot substitute for the head recorded on an older event.
        self.assertNotEqual(run_step("preview", HEAD_SHA="1"*40).returncode, 0)
        env.update(APPROVED_REPOSITORY="team/resources", COMMIT=f.commit, GITHUB_EVENT_PATH=str(event),
                   CATALOG_URL="http://127.0.0.1:1", CATALOG_NAMESPACE_ID=NAMESPACE,
                   TOOL_DIR=str(tools), TOKEN_FILE=str(f.token), STATE_DIR=str(f.state))
        # The unavailable fixture destination fails after admission. Private state proves
        # which commit and operator inputs the actual delivery script supplied.
        first = run_step("deliver", GITHUB_RUN_ATTEMPT="1")
        self.assertNotEqual(first.returncode, 0)
        self.assertIn("import incomplete", first.stderr)
        metadata = json.loads((f.state / f.commit / "delivery.json").read_text())
        self.assertEqual((metadata["commit"], metadata["arguments"]), (f.commit, self.args))
        f.repo.rename(f.root / "offline-repository")
        retry = run_step("deliver", GITHUB_RUN_ATTEMPT="2")
        self.assertNotEqual(retry.returncode, 0)
        self.assertIn("missing original delivery or receipt", retry.stderr)
        self.assertNotIn(NAMESPACE, first.stdout + first.stderr + retry.stdout + retry.stderr)
        self.assertEqual(list(runner.glob("catalog-objects.*")), [])
        self.assertEqual(list(runner.glob("catalog-preview.*")), [])
