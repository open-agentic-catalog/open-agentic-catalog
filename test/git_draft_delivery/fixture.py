"""Disposable Git source and installed CLI controls shared by the example tests."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
EXAMPLE = ROOT / "examples/git-draft-delivery"
SAMPLES = ROOT / "examples/native-import"
SOURCE = "https://git.example.com/team/resources.git"
NAMESPACE = "00000000-0000-7000-8000-000000000001"


class Fixture:
    def __init__(self, directory, cli):
        self.root = directory
        self.cli = cli
        self.repo = directory / "source"
        self.repo.mkdir()
        self.state = directory / "private"
        self.state.mkdir(mode=0o700)
        self.token = directory / "token"
        self.token.write_text("local-template-service-client")
        self.token.chmod(0o600)
        self.git("init", "--template=", "-b", "main", "-q")
        self.git("remote", "add", "origin", SOURCE)
        self.draft = self.repo / "resources/agent.json"
        self.draft.parent.mkdir()
        shutil.copyfile(SAMPLES / "drafts/agent.json", self.draft)
        self.commit = self.save("First resource")

    def git(self, *args):
        env = {"PATH": os.defpath, "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull,
               "GIT_AUTHOR_NAME": "Fixture", "GIT_AUTHOR_EMAIL": "fixture@example.invalid",
               "GIT_COMMITTER_NAME": "Fixture", "GIT_COMMITTER_EMAIL": "fixture@example.invalid"}
        return subprocess.check_output(["git", *args], cwd=self.repo, env=env, text=True).strip()

    def save(self, message):
        self.git("add", ".")
        self.git("commit", "-qm", message)
        return self.git("rev-parse", "HEAD")

    def run(self, action, *flags, commit=None, repo=None, success=True, file="resources/agent.json"):
        command = [sys.executable, "-I", EXAMPLE / "deliver.py", action, "--cli", self.cli,
                   "--repo", repo or self.repo, "--commit", commit or self.commit, "--file", file]
        if action != "preview":
            command.extend(["--source", SOURCE, "--ref", "refs/heads/main", "--state-dir", self.state,
                            "--base-url", getattr(self, "origin", "http://127.0.0.1:1"), "--token-file", self.token])
        env = {"PATH": os.defpath}
        if os.environ.get("GOCOVERDIR"):
            env["GOCOVERDIR"] = os.environ["GOCOVERDIR"]
        result = subprocess.run([*command, *flags], env=env, capture_output=True, text=True, check=False)
        if (result.returncode == 0) != success:
            diagnostic = self.state / (commit or self.commit) / "diagnostic.txt"
            raise AssertionError((action, result.returncode, result.stderr,
                                  diagnostic.read_text() if diagnostic.exists() else ""))
        return result

    def add_skill(self):
        folder = self.draft.parent
        shutil.copytree(SAMPLES / "skills/review-notes", folder / "review-notes")
        script = folder / "review-notes/scripts/example.sh"
        script.parent.mkdir()
        script.write_text("#!/bin/sh\nexit 1\n")
        script.chmod(0o755)
        self.draft.write_text((SAMPLES / "drafts/skill-package.json").read_text())
        artifact = self.save("Skill artifact")
        draft = json.loads(self.draft.read_text())
        handoff = {"git": {"repository": SOURCE, "commit": artifact, "directory": "resources/review-notes"}}
        draft["representations"][0]["input"]["handoff"] = json.dumps(handoff)
        (self.repo / ".gitattributes").write_text("*.md text eol=crlf\n")
        self.draft.write_text(json.dumps(draft))
        self.commit = self.save("Deliver the earlier Skill artifact")
        skill = folder / "review-notes/SKILL.md"
        skill.unlink()
        self.git("checkout", "--", "resources/review-notes/SKILL.md")
        assert b"\r\n" in skill.read_bytes()
        (skill.parent / "untracked").mkdir()
        return artifact
