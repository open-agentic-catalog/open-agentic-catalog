import contextlib
import copy
import io
import json
import os
from pathlib import Path
import signal
import stat
import tempfile
import unittest
from unittest import mock
import runner
from publication_report_test_helpers import evidence as report_evidence


class RunnerTests(unittest.TestCase):
    def test_candidate_build_identity_and_cleanup_preserve_borrowed_images(self):
        commit = "a" * 40
        value = {"service": "open-agentic-catalog", "version": "0.1.0", "commit": commit, "sourceState": "clean",
                 "ardProfile": "ard-v1", "aiCatalogProfile": "aicatalog-v1"}
        self.assertEqual(runner.version_identity(value, commit, "0.1.0", "clean")["version"], "0.1.0")
        for version, state in (("different", "clean"), ("0.1.0", "unknown")):
            with self.assertRaises(runner.Failure):
                runner.version_identity(value, commit, version, state)
        environment = {"IMAGE": "sha256:" + "c" * 64, "CATALOG_FUNCTIONAL_FIXTURE_IMAGE": "fixture-owned"}
        self.assertEqual(set(runner.cleanup_images(environment, "default-off", "owned")), {"fixture-owned", environment["IMAGE"]})
        environment["CATALOG_FUNCTIONAL_CANDIDATE_IMAGE"] = environment["IMAGE"]
        for profile in ("default-off", "security-active"):
            self.assertNotIn(environment["IMAGE"], runner.cleanup_images(environment, profile, "owned"))

    def test_search_page_diagnostic_is_closed_and_does_not_replace_the_original_failure(self):
        positive = {"surface": "control", "page": 2, "completedPages": 1, "continuation": True,
                    "httpFailure": {"status": 400, "category": "CATALOG_CURSOR_STALE"}}
        self.assertEqual(runner.validate_search_page_failure(positive), positive)
        clock = {"responseDeltaSeconds": -2, "cursorExpiryMinusResponseDateSeconds": 902}
        self.assertEqual(runner.validate_search_page_failure({**positive, "clock": clock})["clock"], clock)
        for change in ({"surface": "private"}, {"page": True}, {"page": 0}, {"page": 65}, {"completedPages": True},
                       {"completedPages": 0}, {"continuation": 1}, {"continuation": False},
                       {"httpFailure": {"status": 400, "category": "private"}},
                       {"httpFailure": {"status": 400, "category": "unclassified", "body": "private"}}, {"query": "private"},
                       {"clock": {**clock, "token": "private"}}, {"clock": {**clock, "responseDeltaSeconds": True}},
                       {"clock": {**clock, "responseDeltaSeconds": -86401}}, {"clock": {}}):
            with self.subTest(change=change), self.assertRaises(runner.Failure):
                runner.validate_search_page_failure({**positive, **change})
        with tempfile.TemporaryDirectory() as directory, mock.patch.dict(os.environ, {"CATALOG_FUNCTIONAL_STATE_DIRECTORY": directory}):
            error = ValueError("private-original")
            error.search_page_failure = positive
            with mock.patch.object(runner, "write_document", side_effect=OSError("private-write")):
                runner.record_scenario_failure("peers", error)
            self.assertFalse((Path(directory) / "search-page-failure.json").exists())

    def test_failed_peer_report_retains_page_evidence_and_cleanup(self):
        for corrupt in (False, True):
            with self.subTest(corrupt=corrupt), tempfile.TemporaryDirectory() as directory, mock.patch.object(Path, "cwd", return_value=Path(directory)):
                instance = runner.Runner({}, "peers")
                instance.state = Path(directory)
                value = {"surface": "public", "page": 1, "completedPages": 0, "continuation": False,
                         "httpFailure": {"status": 400, "category": "PUBLIC_CURSOR_INVALID"}}
                runner.write_document(instance.state / "search-page-failure.json", {**value, **({"secret": "private-secret"} if corrupt else {})})
                with mock.patch.object(runner, "Runner", return_value=instance), \
                        mock.patch.object(instance, "execute", side_effect=runner.Failure("child-failed")), \
                        mock.patch.object(instance, "cleanup", return_value=True) as cleanup, \
                        mock.patch.object(runner, "progress", return_value=[]), mock.patch.object(signal, "signal"), \
                        contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                    self.assertEqual(runner.run("peers"), 1)
                    cleanup.assert_called_once()
                report = runner.read_document(instance.report_path)
                self.assertEqual(report["failureCode"], "child-failed")
                self.assertEqual(report["cleanup"], "passed")
                if corrupt:
                    self.assertNotIn("searchPageFailure", report)
                else:
                    self.assertEqual(report["searchPageFailure"], value)
                self.assertNotIn("private-secret", json.dumps(report))

    def test_completed_peer_identity_binds_both_runtimes_archives_and_source(self):
        instance = object.__new__(runner.Runner)
        digest = "sha256:" + "a" * 64
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            instance.state = root / "state"
            instance.state.mkdir()
            runner.write_document(instance.state / "version.json", {"version": "owned"})
            instance.fixture = root / "fixture"
            (instance.fixture / "archives").mkdir(parents=True)
            (instance.fixture / "demo-archives").write_bytes(b"owned tool")
            (instance.fixture / "archives/allowlist.json").write_bytes(b"owned allowlist")
            instance.environment = {"IMAGE": "owned-image"}
            instance.base = {}
            instance.compose, instance.engine = ["compose"], ["docker"]
            instance.report = {"profile": "peers", "source": {"worktreeDigest": "source-bound"}}
            def command(args, *_args, **_kwargs):
                if args[0] == "git":
                    return b"tracked-file\0"
                if args[0] == "compose":
                    return ("b" * 64).encode()
                return digest.encode()
            with mock.patch.object(runner, "command", side_effect=command), mock.patch.object(runner, "digest_files", return_value="source-bound"):
                instance.capture_identity(runner.time.monotonic() + 60)
                self.assertIs(instance.report["peerIdentityVerified"], True)
                self.assertEqual(set(instance.report["images"]), {"api", "worker", "postgres", "federation-fixture", "api-a", "worker-a", "postgres-a", "peer-archives"})
                self.assertEqual(instance.report["servedVersion"], {"version": "owned"})
                self.assertEqual(instance.report["peerArchiveInputs"]["demo-archives"], runner.hashlib.sha256(b"owned tool").hexdigest())
                self.assertEqual(instance.report["peerArchiveInputs"]["archives/allowlist.json"], runner.hashlib.sha256(b"owned allowlist").hexdigest())
            with mock.patch.object(runner, "command", side_effect=command), mock.patch.object(runner, "digest_files", return_value="changed"):
                with self.assertRaisesRegex(runner.Failure, "source-changed-during-run"):
                    instance.capture_identity(runner.time.monotonic() + 60)
                self.assertIs(instance.report["peerIdentityVerified"], False)

    def test_restore_identity_requires_matching_archive_and_database_images(self):
        digest = "sha256:" + "a" * 64
        for mismatch in (None, "peer-archives", "restored"):
            with self.subTest(mismatch=mismatch), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                instance = object.__new__(runner.Runner)
                instance.state = root / "state"
                instance.state.mkdir()
                runner.write_document(instance.state / "version.json", {"version": "owned"})
                runner.write_document(instance.state / "backup-restore.json", {"retainedState": True})
                instance.fixture = root / "fixture"
                (instance.fixture / "archives").mkdir(parents=True)
                (instance.fixture / "demo-archives").write_bytes(b"owned tool")
                (instance.fixture / "archives/allowlist.json").write_bytes(b"owned allowlist")
                instance.environment, instance.base = {"IMAGE": "owned-image"}, {}
                instance.compose, instance.engine = ["compose"], ["docker"]
                instance.report = {"profile": "backup-restore", "source": {"worktreeDigest": "source-bound"}}
                identifiers = {name: str(i) * 64 for i, name in enumerate(
                    ("api", "worker", "postgres", "federation-fixture", "restored", "peer-archives"), 1)}
                def command(args, *_args, **_kwargs):
                    if args[0] == "git":
                        return b"tracked-file\0"
                    if args[0] == "compose":
                        return identifiers[args[-1]].encode()
                    if args[1:3] == ["container", "inspect"] and mismatch and args[-1] == identifiers[mismatch]:
                        return ("sha256:" + "f" * 64).encode()
                    return digest.encode()
                with mock.patch.object(runner, "command", side_effect=command), mock.patch.object(runner, "digest_files", return_value="source-bound"):
                    if mismatch:
                        with self.assertRaisesRegex(runner.Failure, "restore-image-mismatch"):
                            instance.capture_identity(runner.time.monotonic() + 60)
                        self.assertNotIn("backupArchiveInputs", instance.report)
                    else:
                        instance.capture_identity(runner.time.monotonic() + 60)
                        self.assertEqual(set(instance.report["images"]), set(identifiers))
                        self.assertEqual(instance.report["backupArchiveInputs"], {
                            "demo-archives": runner.hashlib.sha256(b"owned tool").hexdigest(),
                            "archives/allowlist.json": runner.hashlib.sha256(b"owned allowlist").hexdigest()})

    def test_job_wait_evidence_is_closed_bounded_and_best_effort(self):
        job = {"id": "private-id", "safeError": "private-text", "result": {"private": "result"},
               "state": "waiting", "failureClass": "transient_upstream", "attempt": 2,
               "attemptLimit": 7, "cancellationRequested": False,
               "updatedAt": "2026-09-26T12:00:00Z", "notBefore": "2026-09-26T12:00:02Z"}
        expected = {"state": "waiting", "failureClass": "transient_upstream", "attempt": 2,
                    "attemptLimit": 7, "cancellationRequested": False, "elapsedMillis": 45000,
                    "scheduledDelayMillis": 2000}
        with tempfile.TemporaryDirectory() as directory, mock.patch.dict(os.environ, {"CATALOG_FUNCTIONAL_STATE_DIRECTORY": directory}):
            self.assertIsNone(runner.read_job_wait_failure(Path(directory)))
            runner.record_job_wait_failure(job, 45)
            self.assertEqual(runner.read_job_wait_failure(Path(directory)), expected)
            for change in ({"state": "private"}, {"failureClass": "private"}, {"attempt": True},
                           {"attempt": -1}, {"attemptLimit": 1001}, {"elapsedMillis": 3600001},
                           {"cancellationRequested": 1}, {"scheduledDelayMillis": True},
                           {"scheduledDelayMillis": 604800001}, {"id": "private"}, {"safeError": "private"}):
                with self.subTest(change=change):
                    runner.write_document(Path(directory) / "job-wait-failure.json", dict(expected, **change))
                    with self.assertRaisesRegex(runner.Failure, "invalid-job-wait-evidence"):
                        runner.read_job_wait_failure(Path(directory))
            for invalid in (None, [], {}, {"state": "running"}):
                with self.assertRaises(runner.Failure):
                    runner.validate_job_wait_failure(invalid)
            for error in (OSError("private"), runner.Failure("write-failed")):
                with mock.patch.object(runner, "write_document", side_effect=error):
                    runner.record_job_wait_failure(job, 45)  # Must not replace the scenario error.
            for malformed in (dict(job, state="private"), dict(job, notBefore="private"), None):
                with mock.patch.object(runner, "write_document") as write:
                    runner.record_job_wait_failure(malformed, 45)
                    write.assert_not_called()
            with mock.patch.object(runner, "write_document") as write:
                runner.record_job_wait_failure(job, float("inf"))
                write.assert_not_called()
        with mock.patch.dict(os.environ, {}, clear=True), mock.patch.object(runner, "write_document") as write:
            runner.record_job_wait_failure(job, 45)
            write.assert_not_called()

    def test_core_failure_location_is_bounded_and_profile_specific(self):
        for scenario in ("core", "credentials", "operators", "operations"):
            filename = str(Path(runner.__file__).with_name(scenario + ".py").resolve())
            with self.subTest(scenario=scenario), tempfile.TemporaryDirectory() as directory, mock.patch.dict(os.environ, {"CATALOG_FUNCTIONAL_STATE_DIRECTORY": directory}):
                try:
                    exec(compile("raise AssertionError('private owned body')", filename, "exec"), {})
                except AssertionError as error:
                    runner.record_scenario_failure(scenario, error)
                location = runner.read_scenario_failure(Path(directory), "default-off")
                self.assertEqual(location, {"scenario": scenario, "lines": [1]})
                self.assertNotIn("private owned body", json.dumps(location))
                for profile in ("oidc", "webhooks", "backup-restore", "peers"):
                    with self.assertRaises(runner.Failure):
                        runner.read_scenario_failure(Path(directory), profile)

    def test_worker_status_evidence_is_closed_and_cannot_hide_a_failure(self):
        evidence = {"lastStatus": {"status": "not_ready", "ready": False, "degraded": True,
                    "components": dict.fromkeys(("job_worker", "outbox_processor", "source_maintainer", "housekeeper", "operations_server"), "running")},
                    "samples": 2, "elapsedMillis": 45000, "observedDegraded": ["job_worker"]}
        self.assertEqual(runner.validate_worker_status_failure(evidence), evidence)
        self.assertEqual(runner.validate_worker_status_failure({**evidence, "lastStatus": None, "samples": 0, "observedDegraded": []})["samples"], 0)
        invalid = [evidence | {"private": "secret"}, evidence | {"elapsedMillis": -1},
                   evidence | {"elapsedMillis": True}, evidence | {"samples": 1001},
                   evidence | {"observedDegraded": ["private-url"]}, evidence | {"observedDegraded": ["job_worker", "job_worker"]},
                   evidence | {"lastStatus": None}, evidence | {"samples": 0},
                   evidence | {"lastStatus": evidence["lastStatus"] | {"instanceId": "private-id"}},
                   evidence | {"lastStatus": evidence["lastStatus"] | {"status": "private-response"}},
                   evidence | {"lastStatus": evidence["lastStatus"] | {"ready": 0}},
                   evidence | {"lastStatus": evidence["lastStatus"] | {"components": {"private": "private"}}}]
        for value in invalid:
            with self.subTest(value=value), self.assertRaises(runner.Failure):
                runner.validate_worker_status_failure(value)
        filename = str(Path(runner.__file__).with_name("operations.py").resolve())
        with tempfile.TemporaryDirectory() as directory, mock.patch.dict(os.environ, {"CATALOG_FUNCTIONAL_STATE_DIRECTORY": directory}):
            for index, diagnostic in enumerate([evidence, *invalid]):
                try:
                    exec(compile("raise AssertionError('private-error')", filename, "exec"), {})
                except AssertionError as error:
                    error.worker_status_failure = diagnostic
                    runner.record_scenario_failure("operations", error, phase="degraded")
                result = runner.read_scenario_failure(Path(directory), "default-off")
                self.assertEqual(result.get("workerStatus"), evidence if index == 0 else None)
                self.assertNotIn("private", json.dumps(result))
            for scenario, phase in (("operators", "degraded"), ("operations", None)):
                runner.write_document(Path(directory) / "scenario-failure.json", {"scenario": scenario, "lines": [1], "workerStatus": evidence, **({"phase": phase} if phase else {})})
                with self.assertRaises(runner.Failure):
                    runner.read_scenario_failure(Path(directory), "default-off")

    def test_worker_failure_phase_is_closed_and_profile_specific(self):
        filename = str(Path(runner.__file__).with_name("operations.py").resolve())
        with tempfile.TemporaryDirectory() as directory, mock.patch.dict(os.environ, {"CATALOG_FUNCTIONAL_STATE_DIRECTORY": directory}):
            for phase in ("degraded", "recovered", None, "private-secret"):
                with self.subTest(phase=phase):
                    try:
                        exec(compile("raise AssertionError('private-secret')", filename, "exec"), {})
                    except AssertionError as error:
                        runner.record_scenario_failure("operations", error, phase=phase)
                    expected = {"scenario": "operations", "lines": [1]}
                    if phase in ("degraded", "recovered"):
                        expected["phase"] = phase
                    self.assertEqual(runner.read_scenario_failure(Path(directory), "default-off"), expected)
                    self.assertNotIn("private-secret", (Path(directory) / "scenario-failure.json").read_text())
            for invalid in (
                {"scenario": "operations", "lines": [1], "phase": "private-secret"},
                {"scenario": "operations", "lines": [1], "phase": None},
                {"scenario": "operations", "lines": [1], "phase": ["recovered"]},
                {"scenario": "operations", "lines": [1], "phase": "recovered", "error": "private-secret"},
                {"scenario": "operators", "lines": [1], "phase": "recovered"},
            ):
                with self.subTest(invalid=invalid):
                    runner.write_document(Path(directory) / "scenario-failure.json", invalid)
                    with self.assertRaisesRegex(runner.Failure, "invalid-scenario-location"):
                        runner.read_scenario_failure(Path(directory), "default-off")

    def test_scenario_diagnostics_are_optional_and_write_failure_is_safe(self):
        with tempfile.TemporaryDirectory() as directory:
            self.assertIsNone(runner.read_scenario_failure(Path(directory), "webhooks"))
        with mock.patch.dict(os.environ, {}, clear=True), mock.patch.object(runner, "write_document") as write:
            runner.record_scenario_failure("webhooks", ValueError("private-secret"))
            write.assert_not_called()
        with mock.patch.dict(os.environ, {"CATALOG_FUNCTIONAL_STATE_DIRECTORY": "/unused"}), \
                mock.patch.object(runner, "scenario_failure_location", return_value={"scenario": "webhooks", "lines": [1]}):
            for error in (OSError("private-secret"), runner.Failure("report-limit")):
                with mock.patch.object(runner, "write_document", side_effect=error) as write:
                    runner.record_scenario_failure("webhooks", ValueError("original-error"))
                    write.assert_called_once()
    def test_scenario_locations_exclude_exception_text_and_private_frames(self):
        filename = str(Path(runner.__file__).with_name("webhooks.py").resolve())
        # Static test code with the owned source filename, not an external script.
        source = "def recurse(n):\n if n: recurse(n-1)\n else: raise ValueError('private-secret')\nrecurse(6)\n"
        with tempfile.TemporaryDirectory() as directory, mock.patch.dict(os.environ, {"CATALOG_FUNCTIONAL_STATE_DIRECTORY": directory}):
            try:
                exec(compile(source, filename, "exec"), {})
            except ValueError as error:
                self.assertIsNone(runner.scenario_failure_location("private-secret", error))
                self.assertIsNone(runner.scenario_failure_location("federation", error))
                runner.record_scenario_failure("webhooks", error)
            location = runner.read_scenario_failure(Path(directory), "webhooks")
            self.assertEqual(location, {"scenario": "webhooks", "lines": [2, 2, 3]})
            self.assertNotIn("private-secret", json.dumps(location))
            self.assertNotIn(directory, json.dumps(location))
            with self.assertRaisesRegex(runner.Failure, "invalid-scenario-location"):
                runner.read_scenario_failure(Path(directory), "default-off")
            for invalid in ({"scenario": "webhooks", "lines": [True]},
                            {}, [], None,
                            {"scenario": "webhooks", "lines": None},
                            {"scenario": "webhooks", "lines": [0]},
                            {"scenario": "webhooks", "lines": [-1]},
                            {"scenario": "webhooks", "lines": ["1"]},
                            {"scenario": "webhooks", "lines": [10001]},
                            {"scenario": "webhooks", "lines": []},
                            {"scenario": "webhooks", "lines": [1, 2, 3, 4]},
                            {"scenario": "webhooks", "lines": [1], "message": "private-secret"},
                            {"scenario": "private-secret", "lines": [1]}):
                runner.write_document(Path(directory) / "scenario-failure.json", invalid)
                with self.assertRaisesRegex(runner.Failure, "invalid-scenario-location"):
                    runner.read_scenario_failure(Path(directory), "webhooks")
    def test_backup_policy_helper_location_is_closed_and_private(self):
        helper = str(Path(runner.__file__).with_name("authorization_recovery.py").resolve())
        caller = str(Path(runner.__file__).with_name("backup_restore.py").resolve())
        scope = {}
        exec(compile("def fail():\n raise ValueError('private-secret')\n", helper, "exec"), scope)
        with tempfile.TemporaryDirectory() as directory, mock.patch.dict(os.environ, {"CATALOG_FUNCTIONAL_STATE_DIRECTORY": directory}):
            try:
                exec(compile("fail()", caller, "exec"), scope)
            except ValueError as error:
                runner.record_scenario_failure("backup_restore", error)
            location = runner.read_scenario_failure(Path(directory), "backup-restore")
            self.assertEqual(location, {"scenario": "authorization_recovery", "lines": [2]})
            self.assertNotIn("private-secret", json.dumps(location))
            self.assertNotIn(directory, json.dumps(location))
            with self.assertRaisesRegex(runner.Failure, "invalid-scenario-location"):
                runner.read_scenario_failure(Path(directory), "webhooks")

    def test_peer_pagination_helper_location_stays_private_and_peer_only(self):
        helper = str(Path(runner.__file__).with_name("peer_pagination.py").resolve())
        caller = str(Path(runner.__file__).with_name("peers.py").resolve())
        scope = {}
        exec(compile("def fail():\n raise ValueError('private-secret')\n", helper, "exec"), scope)
        with tempfile.TemporaryDirectory() as directory, mock.patch.dict(os.environ, {"CATALOG_FUNCTIONAL_STATE_DIRECTORY": directory}):
            try:
                exec(compile("fail()", caller, "exec"), scope)
            except ValueError as error:
                runner.record_scenario_failure("peers", error)
            location = runner.read_scenario_failure(Path(directory), "peers")
            self.assertEqual(location, {"scenario": "peer_pagination", "lines": [2]})
            self.assertNotIn("private-secret", json.dumps(location))
            self.assertNotIn(directory, json.dumps(location))
            with self.assertRaisesRegex(runner.Failure, "invalid-scenario-location"):
                runner.read_scenario_failure(Path(directory), "webhooks")

    def test_failed_run_retains_only_validated_scenario_location(self):
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(Path, "cwd", return_value=Path(directory)):
            instance = runner.Runner({})
            instance.state = Path(directory)
            location = {"scenario": "federation", "lines": [141]}
            runner.write_document(instance.state / "scenario-failure.json", location)
            job_wait = {"state": "failed", "failureClass": "internal", "attempt": 1,
                        "attemptLimit": 7, "cancellationRequested": False, "elapsedMillis": 100}
            runner.write_document(instance.state / "job-wait-failure.json", job_wait)
            with mock.patch.object(runner, "Runner", return_value=instance), \
                    mock.patch.object(instance, "execute", side_effect=runner.Failure("child-failed")), \
                    mock.patch.object(instance, "cleanup", return_value=True), \
                    mock.patch.object(runner, "progress", return_value=[]), \
                    mock.patch.object(signal, "signal"), \
                    contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(runner.run(), 1)
            report = runner.read_document(instance.report_path)
            self.assertEqual(report["failureLocation"], location)
            self.assertEqual(report["jobWaitFailure"], job_wait)
            self.assertEqual(report["status"], "failed")
    def test_invalid_failure_location_cannot_skip_cleanup_or_leak_into_report(self):
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(Path, "cwd", return_value=Path(directory)):
            instance = runner.Runner({})
            instance.state = Path(directory)
            runner.write_document(instance.state / "scenario-failure.json", {"scenario": "federation", "lines": [1], "secret": "private-secret"})
            with mock.patch.object(runner, "Runner", return_value=instance), \
                    mock.patch.object(instance, "execute", side_effect=runner.Failure("child-failed")), \
                    mock.patch.object(instance, "cleanup", return_value=True) as cleanup, \
                    mock.patch.object(runner, "progress", return_value=[]), \
                    mock.patch.object(signal, "signal"), \
                    contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(runner.run(), 1)
                cleanup.assert_called_once()
            report = runner.read_document(instance.report_path)
            self.assertEqual(report["failureCode"], "child-failed")
            self.assertEqual(report["cleanup"], "passed")
            self.assertNotIn("failureLocation", report)
            self.assertNotIn("private-secret", json.dumps(report))
    def test_invalid_job_wait_evidence_preserves_original_failure_and_cleanup(self):
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(Path, "cwd", return_value=Path(directory)):
            instance = runner.Runner({})
            instance.state = Path(directory)
            runner.write_document(instance.state / "job-wait-failure.json", {"result": "private-secret"})
            with mock.patch.object(runner, "Runner", return_value=instance), \
                    mock.patch.object(instance, "execute", side_effect=runner.Failure("child-failed")), \
                    mock.patch.object(instance, "cleanup", return_value=True) as cleanup, \
                    mock.patch.object(runner, "progress", return_value=[]), mock.patch.object(signal, "signal"), \
                    contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(runner.run(), 1)
                cleanup.assert_called_once()
            report = runner.read_document(instance.report_path)
            self.assertEqual(report["failureCode"], "child-failed")
            self.assertNotIn("jobWaitFailure", report)
            self.assertNotIn("private-secret", json.dumps(report))

    def test_work_directory_is_allocated_beside_the_run_report_not_in_system_temporary(self):
        # A VM-based engine shares the checkout under the home directory, not the host TMPDIR.
        with tempfile.TemporaryDirectory() as directory, tempfile.TemporaryDirectory() as system_temporary, \
                mock.patch.object(Path, "cwd", return_value=Path(directory)), \
                mock.patch.dict(os.environ, {"TMPDIR": system_temporary}), \
                mock.patch.object(tempfile, "tempdir", None):
            instance = runner.Runner({})
            with mock.patch.object(runner, "command", side_effect=runner.Failure("child-failed")), \
                    self.assertRaises(runner.Failure):
                instance.execute()
            self.assertEqual(instance.directory.parent, instance.report_path.parent)
            self.assertTrue(instance.directory.name.startswith("work-"))
            self.assertTrue(instance.directory.is_relative_to(Path(directory)))
            self.assertFalse(instance.directory.is_relative_to(Path(system_temporary)))
            self.assertEqual(oct(instance.directory.stat().st_mode & 0o777), "0o700")

    def test_readiness_diagnostic_is_fixed_and_survives_every_pipe_split(self):
        for component in ("API", "worker"):
            for phase in ("readiness", "read-only readiness", "dependency readiness"):
                status = 200 if phase == "readiness" else 503
                suffix = f"{status}; last status was 000." if component == "API" else f"{status}."
                worker = "worker " if component == "worker" else ""
                line = f"Functional {component} {phase} timed out waiting for {worker}HTTP {suffix}\n".encode()
                expected = component.lower() + "-" + phase.replace(" ", "-") + "-timeout"
                for split in range(len(line) + 1):
                    first = runner.readiness_failure_chunk(b"\n", line[:split])
                    tail, _ = runner.http_failure_chunk(b"\n", line[:split])
                    second = runner.readiness_failure_chunk(tail, line[split:])
                    self.assertEqual(first or second, expected)
                for invalid in (b"private-prefix " + line, line.rstrip(b"\n"),
                                line.replace(b".\n", b". private-suffix\n"),
                                line.replace(b"Functional", b"private-secret")):
                    self.assertIsNone(runner.readiness_failure_chunk(b"\n", invalid))

    def test_preflight_diagnostics_are_fixed_and_survive_every_pipe_split(self):
        for message, expected in runner.PREFLIGHT_FAILURES.items():
            line = message + b"\n"
            for split in range(len(line) + 1):
                first = runner.readiness_failure_chunk(b"\n", line[:split])
                tail, _ = runner.http_failure_chunk(b"\n", line[:split])
                second = runner.readiness_failure_chunk(tail, line[split:])
                self.assertEqual(first or second, expected)
            for invalid in (b"private-prefix " + line, message, line.replace(b".\n", b". private-suffix\n")):
                self.assertIsNone(runner.readiness_failure_chunk(b"\n", invalid))

    def test_fixture_mount_probe_requires_the_marker_and_removes_it(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = Path(directory)
            seen = []
            def visible(arguments, environment, seconds, **options):
                seen.append((arguments, (fixture / ".mount-probe").exists()))
                return b".mount-probe\ntrust\n"
            with mock.patch.object(runner, "command", side_effect=visible):
                self.assertTrue(runner.verify_fixture_mount(["docker"], "image", fixture, {}, 5))
            self.assertTrue(seen[0][1], "marker must exist while the probe runs")
            self.assertIn(str(fixture) + ":/probe:ro", seen[0][0])
            self.assertIn("none", seen[0][0])
            self.assertFalse((fixture / ".mount-probe").exists())
            with mock.patch.object(runner, "command", return_value=b"trust\n"), self.assertRaises(runner.Failure) as unshared:
                runner.verify_fixture_mount(["docker"], "image", fixture, {}, 5)
            self.assertEqual(str(unshared.exception), "fixture-directory-not-shared")
            self.assertFalse((fixture / ".mount-probe").exists())
            with mock.patch.object(runner, "command", side_effect=runner.Failure("child-failed")):
                self.assertFalse(runner.verify_fixture_mount(["docker"], "image", fixture, {}, 5))
            with mock.patch.object(runner, "command", side_effect=runner.Failure("deadline")), self.assertRaises(runner.Failure) as late:
                runner.verify_fixture_mount(["docker"], "image", fixture, {}, 5)
            self.assertEqual(str(late.exception), "deadline")

    def test_http_diagnostic_is_closed_and_survives_every_pipe_split(self):
        line = b"AssertionError: functional federation request returned HTTP 503 (REQUEST_DEADLINE_EXCEEDED)\n"
        expected = {"status": 503, "category": "REQUEST_DEADLINE_EXCEEDED"}
        for split in range(len(line) + 1):
            tail, first = runner.http_failure_chunk(b"\n", line[:split])
            tail, second = runner.http_failure_chunk(tail, line[split:])
            self.assertEqual(second or first, expected)
            self.assertLessEqual(len(tail), 160)
        for bad in (b"private-secret " + line, line.replace(b")\n", b") private-secret\n"),
                    line.replace(b"REQUEST_DEADLINE_EXCEEDED", b"private-secret"),
                    line.replace(b"503", b"999"), line.rstrip(b"\n")):
            _, diagnostic = runner.http_failure_chunk(b"\n", bad)
            self.assertIsNone(diagnostic)
        tail, diagnostic = runner.http_failure_chunk(b"\n", b"private-secret" * 1000 + line)
        self.assertIsNone(diagnostic)
        self.assertLessEqual(len(tail), 160)
        for invalid in ({"status": True, "category": "INTERNAL"},
                        {"status": 503, "category": "private-secret"},
                        dict(expected, body="private-secret")):
            self.assertIsNone(runner.Failure("child-failed", invalid).http_failure)

    def test_public_cursor_diagnostics_require_exact_route_status_code_and_text(self):
        cases = {
            "ARD hybrid pageToken is stale; restart the search": "PUBLIC_CURSOR_STALE",
            "ARD pageToken is stale": "PUBLIC_CURSOR_STALE",
            "page token is stale": "PUBLIC_CURSOR_STALE",
            "page token is invalid": "PUBLIC_CURSOR_INVALID",
            "page token is invalid for this request": "PUBLIC_CURSOR_INVALID",
            "page token has expired": "PUBLIC_CURSOR_INVALID",
            "live search cursor is invalid or expired; restart the search": "PUBLIC_CURSOR_INVALID",
            "live search window changed; restart the search": "LIVE_WINDOW_CHANGED",
        }
        for message, category in cases.items():
            problem = {"errorCode": "INVALID_ARGUMENT", "message": message, "private": "private-secret"}
            with self.subTest(message=message):
                self.assertEqual(runner.http_failure_category(problem, 400, "/ard/v1/search"), category)
                self.assertEqual(runner.http_failure_category(problem, 503, "/ard/v1/search"), "unclassified")
                self.assertEqual(runner.http_failure_category(problem, 400, "/api/v1/catalog/search"), "unclassified")
                self.assertEqual(runner.http_failure_category({**problem, "errorCode": "private-secret"}, 400, "/ard/v1/search"), "unclassified")
                self.assertEqual(runner.http_failure_category({**problem, "message": message + " private-secret"}, 400, "/ard/v1/search"), "unclassified")
            line = f"AssertionError: functional federation request returned HTTP 400 ({category})\n".encode()
            for split in range(len(line) + 1):
                tail, first = runner.http_failure_chunk(b"\n", line[:split])
                _, second = runner.http_failure_chunk(tail, line[split:])
                self.assertEqual(first or second, {"status": 400, "category": category})
            self.assertEqual(runner.Failure("child-failed", {"status": 400, "category": category}).http_failure, {"status": 400, "category": category})
        for problem in (None, [], {"message": "private-secret"}, {"errorCode": "INVALID_ARGUMENT", "message": ["private-secret"]}):
            self.assertEqual(runner.http_failure_category(problem, 400, "/ard/v1/search"), "unclassified")

    def test_catalog_cursor_diagnostics_are_closed(self):
        for detail, category in (("catalog generation changed; restart the search", "CATALOG_CURSOR_STALE"),
                                 ("catalog cursor is stale", "CATALOG_CURSOR_STALE"),
                                 ("hybrid search generation changed; restart the search", "CATALOG_CURSOR_STALE"),
                                 ("catalog cursor is invalid for this request", "CATALOG_CURSOR_BINDING_INVALID"),
                                 ("catalog cursor has expired", "CATALOG_CURSOR_EXPIRED"),
                                 ("live search cursor is invalid or expired; restart the search", "LIVE_CURSOR_INVALID"),
                                 ("hybrid cursor is invalid or expired; restart the search", "HYBRID_CURSOR_INVALID"),
                                 ("live search window changed; restart the search", "LIVE_WINDOW_CHANGED")):
            problem = {"code": "INVALID_ARGUMENT", "detail": detail}
            self.assertEqual(runner.http_failure_category(problem, 400, "/api/v1/catalog/search"), category)
            for invalid in ({**problem, "code": "private-secret"}, {**problem, "detail": detail + " private-secret"}):
                self.assertEqual(runner.http_failure_category(invalid, 400, "/api/v1/catalog/search"), "unclassified")
            self.assertEqual(runner.http_failure_category(problem, 400, "/ard/v1/search"), "unclassified")
            line = f"AssertionError: functional federation request returned HTTP 400 ({category})\n".encode()
            for split in range(len(line) + 1):
                tail, first = runner.http_failure_chunk(b"\n", line[:split])
                _, second = runner.http_failure_chunk(tail, line[split:])
                self.assertEqual(first or second, {"status": 400, "category": category})
    def test_enabled_profile_requires_every_named_case(self):
        report = {"profile": "webhooks", "images": {"api": "bound"},
                  "servedVersion": {"service": "bound"}, "smoke": {"checkCount": 1}}
        self.assertFalse(runner.finalize(report, list(runner.WEBHOOK_STEPS), "", True))
        report["webhookCases"] = list(runner.WEBHOOK_CASES)
        self.assertFalse(runner.finalize(report, list(runner.WEBHOOK_STEPS), "", True))
        from test_unit_webhook_crash import evidence
        report["webhookCrash"] = evidence()
        self.assertFalse(runner.finalize(report, list(runner.WEBHOOK_STEPS), "", True))
        report["webhookPublicationReport"] = report_evidence()
        self.assertTrue(runner.finalize(report, list(runner.WEBHOOK_STEPS), "", True))
        self.assertEqual(report["qualification"], "enabled-webhook-profile")
        self.assertFalse(runner.finalize(report, list(runner.STEPS), "", True))
        with self.assertRaisesRegex(runner.Failure, "invalid-profile"):
            runner.selected_steps("unknown")
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)
            self.assertEqual(runner.webhook_evidence(path), {"completed": []})
            for invalid in (["unknown"], runner.WEBHOOK_CASES[1:], "secret", None):
                runner.write_document(path / "webhooks.json", {"completed": invalid})
                with self.assertRaisesRegex(runner.Failure, "invalid-webhook-progress"):
                    runner.webhook_evidence(path)
            runner.write_document(path / "webhooks.json", {"completed": runner.WEBHOOK_CASES, "crash": evidence(), "publicationReport": report_evidence()})
            self.assertEqual(runner.webhook_evidence(path), {"completed": runner.WEBHOOK_CASES, "crash": evidence(), "publicationReport": report_evidence()})
    def test_strict_bounded_json(self):
        for value, code in [(b'{"x":1,"x":2}', "duplicate-json-key"),
                            (b'{}{}', "invalid-json"),
                            (b'NaN', "invalid-json"),
                            (b'\xff', "invalid-json"),
                            (b' ' * (runner.MAX_DOCUMENT + 1), "document-limit")]:
            with self.subTest(code=code), self.assertRaisesRegex(runner.Failure, code):
                runner.decode(value)
    def test_atomic_private_report_and_limits(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "report.json"
            runner.write_document(path, {"status": "incomplete"})
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
            runner.write_document(path, {"status": "failed"})
            self.assertEqual(runner.read_document(path), {"status": "failed"})
            with self.assertRaisesRegex(runner.Failure, "report-limit"):
                runner.write_document(path, {"bad": "x" * runner.MAX_DOCUMENT})
            self.assertEqual(runner.read_document(path), {"status": "failed"})
            self.assertEqual(list(Path(directory).iterdir()), [path])
    def test_frozen_order_rejects_empty_unknown_repeated_or_skipped_units(self):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory)
            self.assertEqual(runner.progress(state), [])
            for bad in ("", "unknown", list(runner.STEPS)[1]):
                with self.assertRaisesRegex(runner.Failure, "unexpected-step"):
                    runner.mark(state, bad)
            for step in runner.STEPS:
                runner.mark(state, step)
                with self.assertRaisesRegex(runner.Failure, "unexpected-step"):
                    runner.mark(state, step)
            self.assertEqual(runner.progress(state), list(runner.STEPS))
            runner.write_document(state / "steps.json", ["not-a-step"])
            with self.assertRaisesRegex(runner.Failure, "invalid-step-order"):
                runner.progress(state)
    def test_only_complete_clean_selected_baseline_can_pass(self):
        from test_unit_ard_checker import RUN, evidence
        for completed, failure, clean, identity, expected in [
                ([], "", True, True, False),
                (list(runner.STEPS)[:-1], "", True, True, False),
                (list(runner.STEPS), "child-failed", True, True, False),
                (list(runner.STEPS), "deadline", True, True, False),
                (list(runner.STEPS), "interrupted", True, True, False),
                (list(runner.STEPS), "", False, True, False),
                (list(runner.STEPS), "", True, False, False),
                (list(runner.STEPS), "", True, True, True)]:
            with self.subTest(completed=len(completed), failure=failure, clean=clean, identity=identity):
                report = {"images": {"api": "bound"}, "servedVersion": {"service": "bound"}, "smoke": {"checkCount": 1}} if identity else {}
                report.update(runId=RUN, ardConformance=evidence())
                self.assertEqual(runner.finalize(report, completed, failure, clean), expected)
                self.assertEqual(report["status"], "passed" if expected else "failed")
                self.assertEqual(report["qualification"], "partial-default-off-baseline")
    def test_version_identity_is_whitelisted_not_diagnostic_capture(self):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory)
            document = {"service": "open-agentic-catalog", "version": "functional", "commit": "a" * 40, "sourceState": "unknown",
                        "ardProfile": "ard-0.91", "aiCatalogProfile": "ai-catalog", "secret": "do-not-retain"}
            runner.retain_version(state, json.dumps(document).encode(), "a" * 40)
            self.assertNotIn("do-not-retain", (state / "version.json").read_text())
            with self.assertRaisesRegex(runner.Failure, "build-identity-mismatch"):
                runner.retain_version(state, json.dumps(document).encode(), "b" * 40)
            document["ardProfile"] = "private://do-not-retain"
            with self.assertRaisesRegex(runner.Failure, "profile-identity-mismatch"):
                runner.retain_version(state, json.dumps(document).encode(), "a" * 40)
    def test_smoke_retains_counts_and_digest_not_private_values(self):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory)
            document = {"status": "passed", "baseUrl": "http://127.0.0.1:8080", "runId": "owned",
                        "checks": ["private-secret"], "resources": [{"kind": kind, "resourceId": "private-secret"}
                                                                      for kind in ("agent", "mcp_server", "skill")],
                        "ardEntries": 3, "aiCatalogEntries": 3}
            runner.retain_smoke(state, json.dumps(document).encode(), "owned")
            raw = (state / "smoke.json").read_text()
            self.assertNotIn("private-secret", raw)
            self.assertNotIn("127.0.0.1", raw)
            self.assertEqual(json.loads(raw)["resourceCount"], 3)
            for field, value in (("status", "failed"), ("runId", "wrong"), ("checks", []),
                                 ("resources", []), ("resources", [None, {}, {}]),
                                 ("resources", [{"kind": []}, {}, {}]),
                                 ("ardEntries", True), ("aiCatalogEntries", 0)):
                with self.subTest(field=field), self.assertRaisesRegex(runner.Failure, "smoke-result-mismatch"):
                    runner.retain_smoke(state, json.dumps(dict(document, **{field: value})).encode(), "owned")
    def test_inventory_shapes_fail_with_safe_codes(self):
        source = Path(__file__).parent / "coverage.json"
        good = json.loads(source.read_bytes())
        malformed = [[], {"formatVersion": True, "scenarios": good["scenarios"]},
                     dict(good, extra="private-secret"), dict(good, scenarios=[None]),
                     dict(good, scenarios=[{"id": []}]), dict(good, scenarios=[])]
        for field, value in (("status", None), ("surfaces", "private-secret"), ("gaps", None)):
            document = copy.deepcopy(good)
            document["scenarios"][0][field] = value
            malformed.append(document)
        for document in malformed:
            with mock.patch.object(runner, "read_document", return_value=document):
                with self.assertRaisesRegex(runner.Failure, "inventory-mismatch") as error:
                    runner.inventory()
                self.assertNotIn("private-secret", str(error.exception))
        with mock.patch.object(runner, "read_document", return_value=good):
            families = runner.inventory()
            self.assertEqual(len(families), 51)
            for identifier, count in (("publisher-domain-verification", 5), ("catalog-content-overview", 14), ("service-activity", 1)):
                family = next(item for item in families if item["id"] == identifier)
                self.assertEqual(family["surfaceCount"], count)
                self.assertEqual(family["inventoryStatus"], "covered")
                self.assertEqual(family["disposition"], "lower-level-only")
                self.assertEqual(family["steps"], [])
            mcp = next(family for family in families if family["id"] == "catalog-control-mcp")
            self.assertEqual(mcp["surfaceCount"], 1)
            self.assertEqual(mcp["disposition"], "lower-level-only")
            self.assertEqual(mcp["steps"], [])
            mcp_http = next(family for family in families if family["id"] == "catalog-control-mcp-http")
            self.assertEqual(mcp_http["surfaceCount"], 7)
            self.assertEqual(mcp_http["disposition"], "lower-level-only")
            self.assertEqual(mcp_http["steps"], [])
            export = next(family for family in families if family["id"] == "native-release-export")
            self.assertEqual(export["surfaceCount"], 4)
            self.assertEqual(export["disposition"], "lower-level-only")
            self.assertEqual(export["steps"], [])
            preview = next(family for family in families if family["id"] == "native-draft-preview")
            self.assertEqual(preview["surfaceCount"], 3)
            self.assertEqual(preview["disposition"], "lower-level-only")
            self.assertEqual(preview["steps"], [])
            trust = next(family for family in families if family["id"] == "private-source-trust-assessments")
            self.assertEqual(trust["surfaceCount"], 4)
            self.assertEqual(trust["disposition"], "lower-level-only")
            self.assertEqual(trust["steps"], [])
            ratings = next(family for family in families if family["id"] == "human-resource-ratings")
            self.assertEqual(ratings["surfaceCount"], 10)
            self.assertEqual(ratings["disposition"], "lower-level-only")
            self.assertEqual(ratings["steps"], [])
            artwork = next(family for family in families if family["id"] == "listing-artwork")
            self.assertEqual(artwork["surfaceCount"], 4)
            self.assertEqual(artwork["disposition"], "lower-level-only")
            self.assertEqual(artwork["steps"], [])
            authorization = next(family for family in families if family["id"] == "authorization-administration")
            self.assertEqual(authorization["disposition"], "lower-level-only")
            remote = next(family for family in families if family["id"] == "remote-cli")
            self.assertEqual(remote["surfaceCount"], 4)
            self.assertEqual(remote["steps"], ["operator-workflows"])
            feedback = next(family for family in families if family["id"] == "private-feedback")
            self.assertEqual(feedback["steps"], ["extended-core"])
            demo = next(family for family in families if family["id"] == "seed-demo-cli")
            self.assertEqual(demo["surfaceCount"], 1)
            self.assertEqual(demo["steps"], [])
    def test_source_fingerprint_binds_bytes_paths_and_deletions(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "source"
            # Atomic JSON writes provide small representative source bytes.
            runner.write_document(path, {"version": 1})
            first = runner.digest_files([str(path)])
            self.assertEqual(first, runner.digest_files([str(path)]))
            runner.write_document(path, {"version": 2})
            self.assertNotEqual(first, runner.digest_files([str(path)]))
            renamed = Path(directory) / "renamed"
            path.rename(renamed)
            self.assertNotEqual(first, runner.digest_files([str(renamed)]))
            self.assertNotEqual(first, runner.digest_files([str(path)]))
            path.symlink_to(renamed)
            with self.assertRaisesRegex(runner.Failure, "source-not-regular"):
                runner.digest_files([str(path)])
    def test_failed_report_write_cannot_return_success(self):
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(Path, "cwd", return_value=Path(directory)):
            instance = runner.Runner({})
            with mock.patch.object(runner, "Runner", return_value=instance), \
                    mock.patch.object(instance, "execute", side_effect=runner.Failure("child-failed")), \
                    mock.patch.object(instance, "cleanup", return_value=True), \
                    mock.patch.object(runner, "write_document", side_effect=OSError("secret-file-name")), \
                    mock.patch.object(signal, "signal"), \
                    contextlib.redirect_stderr(io.StringIO()) as terminal:
                self.assertEqual(runner.run(), 1)
            self.assertNotIn("secret-file-name", terminal.getvalue())
            self.assertEqual(runner.read_document(instance.report_path)["status"], "incomplete")
    def test_interruption_still_cleans_and_records_failure(self):
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(Path, "cwd", return_value=Path(directory)):
            instance = runner.Runner({})
            with mock.patch.object(runner, "Runner", return_value=instance), \
                    mock.patch.object(instance, "execute", side_effect=KeyboardInterrupt), \
                    mock.patch.object(instance, "cleanup", return_value=True) as cleanup, \
                    mock.patch.object(signal, "signal"), \
                    contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(runner.run(), 1)
            cleanup.assert_called_once()
            document = runner.read_document(instance.report_path)
            self.assertEqual((document["status"], document["failureCode"], document["cleanup"]),
                             ("failed", "interrupted", "passed"))
