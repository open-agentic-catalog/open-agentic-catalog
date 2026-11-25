"""Credential claim selection and crash-consistent activation in a private temporary directory."""
import base64
import concurrent.futures
from contextlib import redirect_stderr, redirect_stdout
from datetime import datetime
from functools import partial
import importlib.util
import io
import json
import os
from pathlib import Path
from subprocess import CompletedProcess
import sys
import tempfile
import threading
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("spire_renew", Path(__file__).parent / "spire/renew.py")
renew = importlib.util.module_from_spec(spec)
spec.loader.exec_module(renew)

writer_spec = importlib.util.spec_from_file_location("kubernetes_writer", Path(__file__).parent / "spire/kubernetes_writer.py")
kube_writer = importlib.util.module_from_spec(writer_spec)
with patch.dict(sys.modules, {"renew": renew}):
    writer_spec.loader.exec_module(kube_writer)


def token(exp, **extra):
    claims = {"exp": exp, "iss": renew.ISSUER, "aud": [renew.AUDIENCE], "sub": renew.SUBJECT, **extra}
    payload = base64.urlsafe_b64encode(json.dumps(claims).encode()).decode().rstrip("=")
    return "header." + payload + ".signature"


class KubernetesSecretTest(unittest.TestCase):
    def test_atomic_conditional_rotation_preserves_recent_leaves(self):
        original = token(100)
        first = kube_writer.update({"metadata": {"resourceVersion": "5"}}, original, 10)
        self.assertEqual(first["metadata"], {"resourceVersion": "5"})
        data = first["data"]
        metadata = json.loads(base64.b64decode(data[renew.BINDING]))
        leaf = metadata["secretFile"]
        self.assertEqual(base64.b64decode(data[leaf]).decode(), original)
        self.assertEqual(datetime.fromisoformat(metadata["expiresAt"]).timestamp(), 100)
        old = renew.PREFIX + "20-" + "a" * 32 + ".token"
        stored = {"metadata": {"resourceVersion": "6"}, "data": data | {old: "expired", "unrelated": "keep"}}
        self.assertIsNone(kube_writer.update(stored, original, 40))
        self.assertIsNone(kube_writer.update(stored, token(90), 40))
        for candidate in (token(100, serial=2), token(200)):
            change = kube_writer.update(stored, candidate, 60)
            self.assertEqual(change["metadata"], {"resourceVersion": "6"})
            fields = change["data"]
            new = json.loads(base64.b64decode(fields[renew.BINDING]))
            self.assertNotEqual(new["revision"], metadata["revision"])
            self.assertEqual(base64.b64decode(fields[new["secretFile"]]).decode(), candidate)
            self.assertEqual(set(fields), {renew.BINDING, new["secretFile"], old})
            self.assertIsNone(fields[old])

    def test_malformed_active_binding_refuses_a_new_patch(self):
        for raw in (b"null", b"{}", b'{"secretFile":42}', b'{"secretFile":"unrelated"}'):
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                kube_writer.update({"metadata": {"resourceVersion": "1"}, "data": {
                    renew.BINDING: base64.b64encode(raw).decode()}}, token(200), 50)


class SourceRenewalTest(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name)
        self.binding = self.root / renew.BINDING

    def refresh(self, value, now=10):
        return renew.refresh(self.root, lambda: value, lambda: now)

    def active(self):
        metadata = json.loads(self.binding.read_bytes())
        leaf = self.root / metadata["secretFile"]
        self.assertEqual(leaf.name, metadata["revision"] + ".token")
        self.assertEqual(leaf.stat().st_mode & 0o777, 0o440)
        self.assertEqual(self.binding.stat().st_mode & 0o777, 0o440)
        return metadata["revision"], leaf.read_text()

    def test_identity_audience_and_expiry_precede_activation(self):
        first = token(100)
        revision = self.refresh(first)
        for change in ({"exp": 13}, {"exp": True}, {"exp": 2**64}, {"aud": ["another", renew.AUDIENCE]},
                       {"iss": "other"}, {"sub": "other"}):
            with self.subTest(change=change), self.assertRaises(ValueError):
                data = {"exp": 100, **change}
                self.refresh(token(**data))
        self.assertIsNone(self.refresh(first))
        self.assertIsNone(self.refresh(token(99)))
        self.assertEqual(self.active(), (revision, first))

    def test_agent_refusals_preserve_active_metadata_and_token(self):
        old, new = token(100), token(200, aud=renew.AUDIENCE)
        first = self.refresh(old)
        metadata = self.binding.read_bytes()
        selected = {"spiffe_id": renew.SUBJECT, "svid": new}
        other = {"spiffe_id": "spiffe://workloads.test/other", "svid": new}
        for name, status, reply, reason in (
            ("agent unavailable", 1, [], "Workload API unavailable"),
            ("no identity", 0, [{"svids": []}], "Unexpected workload identity"),
            ("another identity", 0, [{"svids": [other]}], "Unexpected workload identity"),
            ("multiple identities", 0, [{"svids": [selected, other]}], "Unexpected workload identity"),
            ("multiple SVID replies", 0, [{"svids": [selected]}, {"svids": [other]}], "Unexpected workload identity"),
            ("only a bundle", 0, [{"bundles": {}}], "Unexpected workload identity"),
            ("incomplete reply", 0, [{"svids": None}], "Unexpected workload identity"),
        ):
            with self.subTest(name=name), patch.object(renew.subprocess, "run", return_value=CompletedProcess(
                    [], status, stdout=json.dumps(reply).encode())):
                with self.assertRaisesRegex(ValueError, reason):
                    renew.refresh(self.root, renew.acquire, lambda: 10)
                self.assertEqual(self.binding.read_bytes(), metadata)
                self.assertEqual(self.active(), (first, old))

    def test_writer_retries_acquisition_and_publishes_the_matching_svid(self):
        new = token(200, aud=renew.AUDIENCE)
        first = self.refresh(token(100))
        reply = json.dumps([{"svids": [{"spiffe_id": renew.SUBJECT, "svid": new}]}, {"bundles": {}}]).encode()
        refresh = partial(renew.refresh, self.root, renew.acquire, lambda: 10)
        output, errors = io.StringIO(), io.StringIO()
        with (patch.object(renew, "refresh", refresh), patch.object(renew.os, "umask"),
              patch.object(renew.subprocess, "run", side_effect=[
                  CompletedProcess([], 1, stderr=b"private Agent diagnostic"), CompletedProcess([], 0, stdout=reply),
              ]) as run, patch.object(renew.time, "sleep", side_effect=[None, KeyboardInterrupt]),
              redirect_stdout(output), redirect_stderr(errors), self.assertRaises(KeyboardInterrupt)):
            renew.main()
        self.assertEqual(run.call_count, 2)
        args = run.call_args.args[0]
        self.assertEqual(args[args.index("-spiffeID") + 1], renew.SUBJECT)
        self.assertEqual(args[args.index("-audience") + 1], renew.AUDIENCE)
        second, stored = self.active()
        self.assertNotEqual(second, first)
        self.assertEqual(stored, new)
        self.assertEqual(datetime.fromisoformat(json.loads(self.binding.read_bytes())["expiresAt"]).timestamp(), 200)
        self.assertIn(second, output.getvalue())
        self.assertIn("Credential refresh failed", errors.getvalue())
        self.assertNotIn("private Agent diagnostic", errors.getvalue())
        self.assertNotIn(new, output.getvalue() + errors.getvalue())

    def test_repair_and_reissue_without_extending_lifetime(self):
        value = token(100)
        revision = self.refresh(value)
        (self.root / (revision + ".token")).unlink()
        repaired = self.refresh(value)
        self.assertNotEqual(revision, repaired)
        self.assertEqual(self.active(), (repaired, value))
        self.assertIsNone(self.refresh(value))
        # An issuer may replace the signing material without extending the expiry.
        replacement = token(100, serial=2)
        reissued = self.refresh(replacement)
        self.assertNotEqual(reissued, repaired)
        self.assertEqual(self.active(), (reissued, replacement))
        self.assertEqual((self.root / (repaired + ".token")).read_text(), value)
        self.assertIsNone(self.refresh(replacement))

    def test_binding_change_with_cached_token_gets_a_new_revision(self):
        value = token(100)
        previous = self.refresh(value)
        with patch.object(renew, "ORIGIN", "https://replacement.example"):
            new = self.refresh(value)
            self.assertNotEqual(previous, new)
            self.assertEqual(json.loads(self.binding.read_bytes())["origin"], renew.ORIGIN)
            self.assertEqual(self.active(), (new, value))
            self.assertIsNone(self.refresh(value))

    def test_damaged_binding_preserves_files_until_its_pointer_is_restored(self):
        retired = self.refresh(token(60))
        current = self.refresh(token(150))
        original = self.binding.read_bytes()
        retired_leaf = self.root / (retired + ".token")
        current_leaf = self.root / (current + ".token")
        unrelated = self.root / "another-writer.token"
        unrelated.write_text("keep")
        for damaged in (None, {}, {"secretFile": 1}, {"secretFile": unrelated.name}):
            with self.subTest(damaged=damaged):
                self.binding.unlink()
                contents = json.dumps(damaged).encode()
                self.binding.write_bytes(contents)
                with self.assertRaises(ValueError):
                    self.refresh(token(220), now=120)
                self.assertEqual(self.binding.read_bytes(), contents)
                self.assertEqual(retired_leaf.read_text(), token(60))
                self.assertEqual(current_leaf.read_text(), token(150))
                self.assertEqual(unrelated.read_text(), "keep")

        self.binding.unlink()
        self.binding.write_bytes(original)
        renewed = self.refresh(token(220), now=120)
        self.assertNotEqual(renewed, current)
        self.assertEqual(self.active(), (renewed, token(220)))
        self.assertFalse(retired_leaf.exists())
        self.assertEqual(current_leaf.read_text(), token(150))
        self.assertEqual(unrelated.read_text(), "keep")

    def test_interruption_before_rename_and_bounded_retirement(self):
        old, new = token(80), token(160)
        first = self.refresh(old)
        unrelated = self.root / ("other-reader-80-" + "a" * 32 + ".token")
        unrelated.write_text("keep")
        with patch.object(renew.os, "replace", side_effect=OSError("interrupted")), self.assertRaises(OSError):
            self.refresh(new)
        self.assertEqual(self.active(), (first, old))
        self.assertTrue((self.root / ".catalog-a-reader.next").is_file())
        second = self.refresh(new)
        self.assertNotEqual(first, second)
        self.assertEqual(self.active(), (second, new))
        old_leaf = self.root / (first + ".token")
        self.assertIsNone(self.refresh(new, now=109))
        self.assertEqual(old_leaf.read_text(), old)
        self.assertIsNone(self.refresh(new, now=111))
        self.assertFalse(old_leaf.exists())
        self.assertEqual(self.active(), (second, new))
        self.refresh(token(300), now=200)
        self.assertEqual(len(list(self.root.glob(renew.PREFIX + "*.token"))), 2)  # previous active and current
        self.refresh(token(300), now=201)
        self.assertEqual(len(list(self.root.glob(renew.PREFIX + "*.token"))), 1)
        self.assertEqual(unrelated.read_text(), "keep")

    def test_readers_observe_whole_generations_during_activation(self):
        old, new = token(80), token(160)
        first = self.refresh(old)
        staged, resume = threading.Event(), threading.Event()
        replace = os.replace

        def pause(source, destination):
            staged.set()
            if not resume.wait(5):
                raise TimeoutError("reader failed to inspect the staged state")
            replace(source, destination)

        with concurrent.futures.ThreadPoolExecutor() as pool, patch.object(renew.os, "replace", side_effect=pause):
            task = pool.submit(self.refresh, new)
            try:
                self.assertTrue(staged.wait(5))
                self.assertEqual(self.active(), (first, old))
                # Keep an old descriptor across activation, like a consumer between its two metadata reads.
                with self.binding.open() as reader:
                    resume.set()
                    second = task.result(5)
                    stored = json.load(reader)
                    self.assertEqual((self.root / stored["secretFile"]).read_text(), old)
            finally:
                resume.set()
        self.assertEqual(self.active(), (second, new))
