"""Fake process/HTTP tests exercise acquisition, response and cleanup failures."""

import contextlib
import hashlib
import io
import json
from pathlib import Path
import signal
import subprocess
import tempfile
import traceback
import unittest
from unittest.mock import patch
import urllib.error

import evaluation as ev
import runner


class RunnerTests(unittest.TestCase):
    """No test in this class starts Docker, downloads a model or opens a DB."""

    def test_cleanup_on_failure_and_interrupt(self):
        for failure in (RuntimeError("failed"), KeyboardInterrupt("stop")):
            with self.subTest(failure=type(failure).__name__), patch.object(runner.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, "", "")) as run:
                with self.assertRaises(type(failure)):
                    with runner.owned_resources() as names:
                        raise failure
                calls = [call.args[0] for call in run.call_args_list]
                self.assertEqual(calls, [["docker", "rm", "--force", "--volumes", names["tei"]], ["docker", "rm", "--force", "--volumes", names["pg"]], ["docker", "network", "rm", names["network"]]])
                self.assertTrue(names["network"].startswith("catalog-relevance-"))

    def test_cleanup_failure_is_not_success(self):
        with patch.object(runner.subprocess, "run", return_value=subprocess.CompletedProcess([], 1, "", "daemon failed")), self.assertRaisesRegex(RuntimeError, "cleanup failed"):
            with runner.owned_resources():
                pass

    def test_interrupt_during_cleanup_finishes_then_stops(self):
        previous = {sig: signal.getsignal(sig) for sig in (signal.SIGINT, signal.SIGTERM)}
        calls = []
        def cleanup(args, **kwargs):
            calls.append(args)
            self.assertTrue(kwargs["start_new_session"])
            signal.getsignal(signal.SIGTERM)(signal.SIGTERM, None)
            return subprocess.CompletedProcess(args, 0, "", "")
        with patch.object(runner.subprocess, "run", side_effect=cleanup), self.assertRaisesRegex(KeyboardInterrupt, "during cleanup"):
            with runner.owned_resources():
                pass
        self.assertEqual(len(calls), 3)
        self.assertEqual(calls[-1][:3], ["docker", "network", "rm"])
        self.assertEqual({sig: signal.getsignal(sig) for sig in previous}, previous)

    def test_cleanup_error_is_not_hidden_by_deferred_interrupt(self):
        with self.assertRaisesRegex(RuntimeError, "cleanup failure"):
            with runner.finish_cleanup_before_interrupt():
                signal.getsignal(signal.SIGINT)(signal.SIGINT, None)
                raise RuntimeError("cleanup failure")

    def test_missing_resources_are_idempotent(self):
        results = [subprocess.CompletedProcess([], 1, "", "No such container")]*2 + [subprocess.CompletedProcess([], 1, "", "network not found")]
        with patch.object(runner.subprocess, "run", side_effect=results):
            with runner.owned_resources():
                pass

    def test_cleanup_continues_after_process_errors(self):
        """A failed removal must not skip later containers or the owned network."""
        for error in (OSError("docker unavailable"), subprocess.TimeoutExpired("docker", 30)):
            with self.subTest(error=type(error).__name__), patch.object(runner.subprocess, "run", side_effect=error) as run:
                runner.OWNED_ORIGINS.add("http://172.20.0.2:80")
                with self.assertRaisesRegex(RuntimeError, "owned fixture cleanup failed"):
                    with runner.owned_resources() as names:
                        pass
                self.assertEqual(run.call_count, 3)
                self.assertEqual(run.call_args.args[0], ["docker", "network", "rm", names["network"]])
                self.assertTrue(all(call.kwargs["timeout"] == 30 for call in run.call_args_list))
                self.assertFalse(runner.OWNED_ORIGINS)

    def test_install_downloads_verified_bytes_without_overwriting_existing_files(self):
        """Exercise real hashing and exclusive cache creation with no downloads."""
        content = b'{"model":"fixture"}'
        manifest = {"model": "org/model", "revision": "a"*40, "files": {"config.json": hashlib.sha256(content).hexdigest()}, "tei_image": "tei@sha256:fixture", "postgres_image": "pg@sha256:fixture"}
        for failure in (None, "existing", "concurrent", "download"):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as temporary:
                model = Path(temporary).resolve() / "model"
                target = model / "config.json"
                if failure == "existing":
                    model.mkdir()
                    target.write_bytes(b"preserve existing")
                def acquire(args, **kwargs):
                    if args[0] == "curl":
                        self.assertEqual(args[-1], "https://huggingface.co/org/model/resolve/" + "a"*40 + "/config.json")
                        Path(args[args.index("--output") + 1]).write_bytes(content)
                        if failure == "concurrent":
                            target.write_bytes(b"preserve concurrent")
                        elif failure == "download":
                            raise RuntimeError("download failed")
                    return ""
                with patch.object(runner, "command", side_effect=acquire) as command, contextlib.redirect_stdout(io.StringIO()):
                    if failure:
                        expected_error = {"existing": ValueError, "concurrent": FileExistsError, "download": RuntimeError}[failure]
                        with self.assertRaises(expected_error):
                            runner.install(manifest, model)
                    else:
                        runner.install(manifest, model)
                        self.assertEqual(target.read_bytes(), content)
                        self.assertEqual(target.stat().st_mode & 0o777, 0o644)
                        self.assertEqual([call.args[0] for call in command.call_args_list[1:]], [["docker", "pull", manifest["tei_image"]], ["docker", "pull", manifest["postgres_image"]]])
                self.assertEqual(list(model.glob("download-*")), [])
                if failure in ("existing", "concurrent"):
                    self.assertEqual(target.read_bytes(), ("preserve " + failure).encode())
                elif failure == "download":
                    self.assertFalse(target.exists())
                if failure:
                    self.assertFalse(any(call.args[0][0] == "docker" for call in command.call_args_list))

    def test_startup_retries_transient_errors_but_stops_on_exit_or_deadline(self):
        """Mock time and process status so startup failures never add real waits."""
        manifest, _, _ = ev.fixtures()
        info = {"served_model_name": manifest["model"], "model_sha": manifest["revision"], "model_dtype": "float32", "max_input_length": 512, "auto_truncate": False, "model_type": {"embedding": {"pooling": "cls"}}, "version": "1.9.4"}
        self.addCleanup(runner.OWNED_ORIGINS.clear)
        for failure in (None, "exit", "deadline"):
            commands = ["", "", '{"owned":{"IPAddress":"172.20.0.2"}}', "false" if failure == "exit" else "true"]
            if failure == "exit":
                commands.append("model load failed")
            times = [0, 1, 121 if failure == "deadline" else 2]
            with self.subTest(failure=failure), patch.object(runner, "command", side_effect=commands), patch.object(runner, "request", side_effect=[urllib.error.URLError("not ready"), info]) as request, patch.object(runner.time, "monotonic", side_effect=times), patch.object(runner.time, "sleep") as sleep:
                if failure:
                    with self.assertRaisesRegex(RuntimeError, "model exited: model load failed" if failure == "exit" else "model startup timeout") as caught:
                        runner.start_tei(manifest, {"network": "owned", "tei": "owned-tei"})
                    self.assertNotIn("URLError", "".join(traceback.format_exception(caught.exception)))
                    self.assertEqual(request.call_count, 1)
                else:
                    self.assertEqual(runner.start_tei(manifest, {"network": "owned", "tei": "owned-tei"})[1], info)
                    self.assertEqual(request.call_count, 2)
                self.assertEqual(sleep.call_count, 0 if failure == "exit" else 1)

    def test_startup_rejects_non_fixture_addresses_before_http(self):
        manifest, _, _ = ev.fixtures()
        self.addCleanup(runner.OWNED_ORIGINS.clear)
        for address in ("8.8.8.8", "127.0.0.1", "0.0.0.0"):
            with self.subTest(address=address), patch.object(runner, "command", side_effect=["", "", json.dumps({"owned": {"IPAddress": address}})]), patch.object(runner, "request") as request:
                with self.assertRaisesRegex(ValueError, "unexpected fixture network address"):
                    runner.start_tei(manifest, {"network": "owned", "tei": "owned-tei"})
                request.assert_not_called()

    def test_response_checks_and_batch_order(self):
        response = {"model": "model", "usage": {"prompt_tokens": 2}, "data": [{"index": 1, "embedding": [0, 1]}, {"index": 0, "embedding": [1, 0]}]}
        manifest = {"model": "model", "dimensions": 2}
        with patch.object(runner, "request", return_value=response) as request:
            timings = []
            self.assertEqual(runner.embed("http://127.0.0.1:1", manifest, ["a", "b"], timings), [[1, 0], [0, 1]])
            self.assertEqual(request.call_args.args[2]["input"], ["a", "b"])
            self.assertEqual(timings[0]["tokens"], 2)
        for bad in (dict(response, model="other"), dict(response, data=[]), dict(response, data=[response["data"][0]]*2), dict(response, data=[dict(response["data"][0], index=True), response["data"][1]])):
            with patch.object(runner, "request", return_value=bad), self.assertRaises(ValueError):
                runner.embed("http://127.0.0.1:1", manifest, ["a", "b"], [])

    def test_embedding_batches_are_bounded_and_keep_cross_batch_order(self):
        """Provider row order may differ, but output must follow all input batches."""
        texts = [str(i) for i in range(19)]
        def response(origin, path, payload):
            self.assertEqual(path, "/v1/embeddings")
            self.assertEqual(payload["encoding_format"], "float")
            batch = payload["input"]
            return {"model": "fixture", "usage": {"prompt_tokens": len(batch)}, "data": [
                {"index": i, "embedding": [1, 0] if int(text) % 2 == 0 else [0, 1]}
                for i, text in reversed(list(enumerate(batch)))]}
        with patch.object(runner, "request", side_effect=response) as request:
            timings = []
            vectors = runner.embed("owned", {"model": "fixture", "dimensions": 2}, texts, timings)
            self.assertEqual(vectors, [[1, 0] if i % 2 == 0 else [0, 1] for i in range(19)])
            self.assertEqual([call.args[2]["input"] for call in request.call_args_list], [texts[:8], texts[8:16], texts[16:]])
            self.assertEqual([row["inputs"] for row in timings], [8, 8, 3])
            self.assertEqual(sum(row["tokens"] for row in timings), 19)
            request.reset_mock()
            self.assertEqual(runner.embed("owned", {}, [], []), [])
            request.assert_not_called()

    def test_baseline_bridge_receives_only_scoring_inputs(self):
        """Relevance judgments and extra metadata must not influence the scorer."""
        _, docs, queries = ev.fixtures()
        document = dict(docs[0], secret="excluded")
        query = dict(queries[0], rationale="excluded")
        with tempfile.TemporaryDirectory() as temporary, patch.object(runner, "command", side_effect=["", '{"control":{},"public":{}}']) as command:
            directory = Path(temporary).resolve()
            self.assertEqual(runner.baseline([document], [query], directory), {"control": {}, "public": {}})
            self.assertEqual(command.call_args_list[0].args[0], ["go", "build", "-o", str(directory / "baseline"), "./test/search-relevance/baseline"])
            payload = json.loads(command.call_args.kwargs["data"])
            self.assertEqual(set(payload["documents"][0]), {"id", "identifier", "name", "description", "tags", "representativeQueries"})
            self.assertEqual(payload["queries"], [{"id": query["id"], "text": query["text"]}])

    def test_parity_startup_retry_is_bounded_without_running_sql_on_failure(self):
        """A fake unavailable database exercises the retry limit without sleeping."""
        manifest, _, _ = ev.fixtures()
        with patch.object(runner, "command", side_effect=[""] + [RuntimeError("not ready")]*120) as command, patch.object(runner.time, "sleep") as sleep:
            with self.assertRaisesRegex(RuntimeError, "parity database startup timeout"):
                runner.pgvector_parity(manifest, {"pg": "owned"}, [], [], {}, {}, 0.5, {})
            self.assertEqual(command.call_count, 121)
            self.assertEqual(sleep.call_count, 120)
            self.assertTrue(all("pg_isready" in call.args[0] for call in command.call_args_list[1:]))

    def test_network_boundary(self):
        for origin in ("https://example.org", "http://127.0.0.1:1@evil.test", "http://localhost:123"):
            with self.assertRaises(ValueError):
                runner.request(origin, "/info")
        self.assertIsNone(runner.NoRedirect().redirect_request(None, None, 302, "", {}, "http://example.org"))
        class Response(io.BytesIO):
            """Provide the response context manager expected by urllib."""
        opener = unittest.mock.Mock()
        opener.open.return_value = Response(b"x"*(4*1024*1024+1))
        with patch.object(runner.urllib.request, "build_opener", return_value=opener), self.assertRaisesRegex(ValueError, "overlong"):
            runner.request("http://127.0.0.1:1", "/info")

    def test_model_integrity_and_symlinks(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(runner, "MODEL", Path(directory).resolve()/"model"):
            runner.MODEL.mkdir()
            path = runner.MODEL/"config.json"
            path.write_text("{}")
            manifest = {"files": {"config.json": ev.digest(path)}}
            runner.verify_model(manifest)
            extra = runner.MODEL/"unexpected-prompt.json"
            extra.write_text("{}")
            with self.assertRaisesRegex(ValueError, "inventory"):
                runner.verify_model(manifest)
            extra.unlink()
            path.write_text("changed")
            with self.assertRaises(ValueError):
                runner.verify_model(manifest)
            path.unlink()
            path.symlink_to(Path(directory).resolve()/"outside")
            with self.assertRaises(ValueError):
                runner.verify_model(manifest)

    def test_subprocess_errors_and_json_finiteness(self):
        with patch.object(runner.subprocess, "run", return_value=subprocess.CompletedProcess([], 1, "", "error")), self.assertRaises(RuntimeError):
            runner.command(["tool"])
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaises(ValueError):
                runner.save(Path(directory).resolve()/"bad.json", {"value": float("nan")})

    def test_changed_inputs_fail_instead_of_mixing_evidence(self):
        report = {"inputs": {"manifest.json": "expected"}, "scorer_inputs": {"internal/catalog/search_profile.go": "expected"}}
        with patch.object(ev, "digest", return_value="expected"):
            runner.verify_inputs(report)
        with patch.object(ev, "digest", side_effect=["expected", "changed"]), self.assertRaisesRegex(ValueError, "inputs changed"):
            runner.verify_inputs(report)

    def test_failed_evaluation_retains_report(self):
        manifest, docs, queries = ev.fixtures()
        with tempfile.TemporaryDirectory() as directory, patch.object(runner, "OUTPUT", Path(directory).resolve()), patch.object(runner, "verify_model"), patch.object(runner, "command", return_value="revision"), patch.object(runner, "baseline", side_effect=RuntimeError("bridge failed")), contextlib.redirect_stdout(io.StringIO()):
            with self.assertRaisesRegex(RuntimeError, "bridge failed"):
                runner.evaluate(manifest, docs, queries)
            paths = list(Path(directory).resolve().glob("run-*/report.json"))
            self.assertEqual(len(paths), 1)
            self.assertEqual(json.loads(paths[0].read_text())["status"], "failed")

    def test_install_reuses_pins_and_rejects_bad_download(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(runner, "MODEL", Path(directory).resolve()/"model"), contextlib.redirect_stdout(io.StringIO()):
            runner.MODEL.mkdir()
            path = runner.MODEL/"config.json"
            path.write_text("{}")
            manifest = {"files": {"config.json": ev.digest(path)}, "tei_image": "tei", "postgres_image": "pg", "model": "fixed/model", "revision": "fixed"}
            with patch.object(runner, "command", return_value="") as command:
                runner.install(manifest)
                self.assertEqual([c.args[0] for c in command.call_args_list], [["docker", "pull", "tei"], ["docker", "pull", "pg"]])
            path.unlink()
            with patch.object(runner, "command", return_value=""), self.assertRaisesRegex(ValueError, "checksum mismatch"):
                runner.install(manifest)
            self.assertFalse(path.exists())
            self.assertEqual(list(runner.MODEL.iterdir()), [])

    def test_pg_readiness_uses_final_tcp_server_and_checks_parity(self):
        manifest, _, _ = ev.fixtures()
        doc = {"id": "d", "identifier": "urn:air:example.test:demo:d", "name": "Doc", "kind": "agent", "source": "local", "public": True}
        query = {"id": "q", "text": "Doc"}
        vec = [1.0]+[0.0]*383
        extension_version = "0.8.7"
        def fake(args, timeout=120, data=None):
            if "pg_isready" in args or "psql" in args:
                self.assertIn("-h", args)
                self.assertEqual(args[args.index("-h")+1], "127.0.0.1")
            if data and "json_agg(t)" in data:
                self.assertIn("ORDER BY similarity DESC, id", data)
                return "[]" if "source='indexed'" in data else json.dumps([{"id": "d", "kind": "agent", "similarity": 1}])
            if data and "json_object_agg" in data:
                return '{"ids":[],"facets":{}}' if "source='indexed'" in data else '{"ids":["d"],"facets":{"agent":1}}'
            if data and "json_build_object" in data:
                return '{"identical":1,"orthogonal":0}'
            if data and "extversion" in data:
                return extension_version
            if data and "server_version_num" in data:
                return "180003"
            return ""
        with patch.object(runner, "command", side_effect=fake):
            result = runner.pgvector_parity(manifest, {"pg": "owned"}, [doc], [query], {"d": vec}, {"q": vec}, 0.65, {s: {"q": {"d": 1}} for s in ("control", "public")})
        self.assertEqual(result["max_cosine_error"], 0)
        self.assertEqual(result["extra_scope_cases"], 2)
        self.assertEqual(result["extension"], "0.8.7")
        for extension_version in ("0.8.6", "0.8.8"):
            with self.subTest(extension_version=extension_version), patch.object(runner, "command", side_effect=fake), self.assertRaisesRegex(ValueError, "unexpected PostgreSQL/pgvector baseline"):
                runner.pgvector_parity(manifest, {"pg": "owned"}, [doc], [query], {"d": vec}, {"q": vec}, 0.65, {s: {"q": {"d": 1}} for s in ("control", "public")})

    def test_install_rejects_escaping_parent_before_creating_directories(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(runner, "MODEL", Path(directory).resolve()/"model"):
            runner.MODEL.mkdir()
            outside = Path(directory).resolve()/"outside"
            outside.mkdir()
            (runner.MODEL/"link").symlink_to(outside, target_is_directory=True)
            with self.assertRaises(ValueError):
                runner.install({"files": {"link/new/config.json": "a"*64}})
            self.assertFalse((outside/"new").exists())

    def test_pg_rejects_wrong_order_ties_and_same_count_wrong_membership(self):
        manifest, _, _ = ev.fixtures()
        docs = [{"id": i, "kind": "agent", "source": "local", "public": True} for i in ("a", "b")]
        query = {"id": "q", "text": "example"}
        first = [1.0]+[0.0]*383
        second = [0.0, 1.0]+[0.0]*382
        for failure, message in (("order", "ranking differs"), ("tie", "tie ordering"), ("membership", "membership/facets")):
            embeddings = {"a": first, "b": first if failure == "tie" else second}
            rows = [{"id": "a", "kind": "agent", "similarity": 1}, {"id": "b", "kind": "agent", "similarity": 1 if failure == "tie" else 0}]
            if failure != "membership":
                rows.reverse()
            def fake(args, timeout=120, data=None):
                if data and "json_agg(t)" in data:
                    return json.dumps(rows)
                if data and "json_object_agg" in data:
                    return '{"ids":["a","wrong"],"facets":{"agent":2}}'
                return ""
            with self.subTest(failure=failure), patch.object(runner, "command", side_effect=fake), self.assertRaisesRegex(ValueError, message):
                runner.pgvector_parity(manifest, {"pg": "owned"}, docs, [query], embeddings, {"q": first}, 0.65, {s: {"q": {"a": 1, "b": 1}} for s in ("control", "public")})

    def test_startup_batch_permits_and_immutable_metadata(self):
        manifest, _, _ = ev.fixtures()
        info = {"served_model_name": manifest["model"], "model_sha": manifest["revision"], "model_dtype": "float32", "max_input_length": 512, "auto_truncate": False, "model_type": {"embedding": {"pooling": "cls"}}, "version": "1.9.4"}
        with patch.object(runner, "command", side_effect=["", "", '{"owned":{"IPAddress":"172.20.0.2"}}']) as cmd, patch.object(runner, "request", return_value=info):
            origin, result = runner.start_tei(manifest, {"network": "owned", "tei": "owned-tei"})
        self.assertEqual(origin, "http://172.20.0.2:80")
        args = cmd.call_args_list[1].args[0]
        permits = int(args[args.index("--max-concurrent-requests")+1])
        batch = int(args[args.index("--max-client-batch-size")+1])
        self.assertGreaterEqual(permits, 2*batch)
        self.assertEqual(result, info)
        runner.OWNED_ORIGINS.clear()
        with patch.object(runner, "command", side_effect=["", "", '{"owned":{"IPAddress":"172.20.0.2"}}']), patch.object(runner, "request", return_value=dict(info, model_sha="wrong")), self.assertRaisesRegex(ValueError, "configuration mismatch"):
            runner.start_tei(manifest, {"network": "owned", "tei": "owned-tei"})
        runner.OWNED_ORIGINS.clear()


if __name__ == "__main__":
    unittest.main()
