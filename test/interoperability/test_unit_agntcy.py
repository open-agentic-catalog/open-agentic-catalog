"""Directory assertions and supervisor failures without peers, sockets or subprocesses."""

import contextlib
import copy
import io
import json
from pathlib import Path
import signal
import tempfile
import unittest
from unittest.mock import Mock, patch

import agntcy_contract as c
import agntcy_lab as lab
import agntcy_runner as r
from common import Result
from peer_contract import LEGACY
from peer_wire import checker_evidence


def checker(mode):
    """Small captured native diagnostic vocabulary, independent of report validators."""
    if mode == "registry":
        text = ("✗ GET /agents response is not a valid paginated object. Missing 'items' array.\n"
                "✗ POST /search request failed: 400 Bad Request\n"
                "⚠ POST /explore responded with 404 (Not Implemented/Not Found).\n"
                "CONFORMANCE STATUS: FAIL\nFound 2 critical specification errors.")
        return checker_evidence(Result(1, text, ""))
    prefix = ""
    if mode == "publisher":
        prefix = "⚠ No manifest at https://127.0.0.1:8443/.well-known/ard.json (404 Not Found).\n⚠ Reachable only at predecessor.\n"
    text = (prefix + "✓ Manifest validates against ArdManifest.\n✓ All 0 entries validate against ArdEntry.\n"
            "⚠ Found 'collections' at root.\nCONFORMANCE STATUS: PASS\n"
            f"Validated with 0 critical specification errors and {2 if mode == 'publisher' else 1} warnings.")
    return checker_evidence(Result(0, text, ""))


def evidence():
    cid = lab.read_json(lab.ASSETS / "lock.json")["cid"]
    digest = c.document_digest(lab.read_json(lab.ASSETS / "card.json"))
    return {
        "artifact": dict(cid=cid, profile=c.PROFILE, validation="complete", retained=True, native_preserved=True,
                         input_sha256=digest, export_sha256=digest, retained_sha256=digest),
        "discovery": dict(native_record=cid, manifest_entries=0, profile=c.CURRENT, source_type="ard_manifest", state="complete",
                          members=0, observations=0, current_pointer=True, bytes_preserved=True, source_id="source", job_id="job", snapshot_id="snapshot"),
        "search": dict(nested_status=400, scalar_status=503, catalog_status=400, catalog_code="INVALID_ARGUMENT",
                       nested_error=dict(code=3, message="proto: (line 1:11): invalid value for string field query: {", details=[]),
                       scalar_error=dict(code=14, message="natural-language search is unavailable: no OASF extractor is configured on this gateway", details=[])),
        "conformance": dict(modes={mode: checker(mode) for mode in ("manifest", "publisher", "registry")},
                            raw_entry=dict(profile=c.CURRENT, entries=1, valid=0, errors=copy.deepcopy(c.ENTRY_ERRORS), identifier="urn:ai:org.agntcy:cid:" + cid)),
    }


class Reports(unittest.TestCase):
    def test_search_status_codes_do_not_hide_different_failure_causes(self):
        expected = evidence()["search"]
        nested, scalar = expected["nested_error"], expected["scalar_error"]
        c.check_search_errors(nested, scalar)
        for key, change in (("nested", {"code": 14}), ("nested", {"message": "invalid field pageSize"}),
                            ("nested", {"details": ["unexpected"]}), ("scalar", {"code": True}),
                            ("scalar", {"message": "upstream connection refused"}),
                            ("scalar", {"message": "x" * 1025})):
            with self.subTest(key=key, change=change), self.assertRaises(ValueError):
                c.check_search_errors({**nested, **change} if key == "nested" else nested,
                                      {**scalar, **change} if key == "scalar" else scalar)
        for invalid in (None, {}, True):
            with self.subTest(invalid=invalid), self.assertRaises(ValueError):
                c.check_search_errors(invalid, scalar)

    def test_search_checks_native_causes_before_catalog_inspection(self):
        expected = evidence()["search"]
        api = Mock()
        api.document.side_effect = [(expected["nested_error"], {}), (expected["scalar_error"], {}), ({"code": "INVALID_ARGUMENT"}, {})]
        result = r.search(api, Mock(lock={"tls_origin": "https://owned.test"}))
        self.assertEqual(result, expected)
        self.assertEqual(api.document.call_count, 3)
        api.document.reset_mock()
        api.document.side_effect = [(dict(code=3, message="invalid credentials", details=[]), {}), (expected["scalar_error"], {})]
        with self.assertRaisesRegex(ValueError, "query object"):
            r.search(api, Mock(lock={"tls_origin": "https://owned.test"}))
        self.assertEqual(api.document.call_count, 2)

    def test_selected_release_expectations_do_not_replace_retained_outcomes(self):
        selected = lab.read_json(lab.ASSETS / "deployment.json")["expected"]
        values = evidence()
        rows = [dict(name=name, disposition=selected["dispositions"][name], status="pass", evidence=value)
                for name, value in values.items()]
        self.assertEqual(c.check_cases(rows, values["artifact"]["cid"], selected), 4)
        changed = copy.deepcopy(selected)
        changed["scalar_status"] = 502
        with self.assertRaises(ValueError):
            c.check_cases(rows, values["artifact"]["cid"], changed)
        self.assertEqual(c.check_cases(rows, values["artifact"]["cid"]), 4)

    def test_generic_predecessor_alias_requires_current_ard_inspection(self):
        cid = lab.read_json(lab.ASSETS / "lock.json")["cid"]
        origin = "https://owned.test"
        api = Mock()
        api.request.return_value = (b'{"specVersion":"1.0","entries":[]}', {})
        inspection = dict(id="inspection", sourceType="ard_manifest", profileId=c.CURRENT, entryCount=0,
                          resolvedUrl=origin + "/.well-known/ai-catalog.json")
        api.document.side_effect = [
            ({"identifier": "urn:ai:org.agntcy:cid:" + cid}, {}), (inspection, {}),
            ({"id": "source"}, {"etag": "version-one"}), ({"job": {"id": "job"}}, {}),
            ({"state": "succeeded", "result": {"snapshotId": "snapshot"}}, {}),
            ({"id": "snapshot", "sourceId": "source", "state": "complete", "memberCount": 0}, {}),
            ({"lastCompleteSnapshotId": "snapshot"}, {}), ({"items": []}, {}),
        ]
        result = r.discovery(api, Mock(lock={"cid": cid, "tls_origin": origin}))
        self.assertEqual(result["profile"], c.CURRENT)
        self.assertEqual(result["source_type"], "ard_manifest")
        rows = [dict(name=name, disposition=c.DISPOSITIONS[name], status="pass", evidence=value) for name, value in evidence().items()]
        rows[1]["evidence"] = result
        c.check_cases(rows, cid)
        rows[1]["evidence"] = {**result, "profile": LEGACY, "source_type": "ai_catalog"}
        with self.assertRaises(ValueError):
            c.check_cases(rows, cid)

    def test_conformance_inputs_bind_shared_dev_manifest_and_lock(self):
        self.assertIn(r.shared.ROOT / "pyproject.toml", r.INPUTS)
        self.assertIn(r.shared.ROOT / "uv.lock", r.INPUTS)
        self.assertNotIn(r.CHECKER.with_name("requirements.txt"), r.INPUTS)

    def test_four_obligations_require_actual_typed_results(self):
        values = evidence()
        rows = [dict(name=name, disposition=c.DISPOSITIONS[name], status="pass", evidence=value) for name, value in values.items()]
        cid = values["artifact"]["cid"]
        self.assertEqual(c.check_cases(rows, cid), 4)
        changes = [lambda v: v.pop(), lambda v: v.append(v[0]), lambda v: v.__setitem__(0, None),
                   lambda v: v[0].update(name="discovery"), lambda v: v[0].update(status="unrun"),
                   lambda v: v[0]["evidence"].update(retained_sha256="a" * 64),
                   lambda v: v[1]["evidence"].update(members=False), lambda v: v[1]["evidence"].pop("job_id"),
                   lambda v: v[1]["evidence"].update(current_pointer=False),
                   lambda v: v[2]["evidence"].update(catalog_status=201),
                   lambda v: v[3]["evidence"]["modes"].pop("registry"),
                   lambda v: v[3]["evidence"]["raw_entry"].update(errors=[{}] * 3)]
        for change in changes:
            altered = copy.deepcopy(rows)
            change(altered)
            with self.subTest(change=change), self.assertRaises(ValueError):
                c.check_cases(altered, cid)

    def test_native_status_schema_and_warning_counts_cannot_be_fabricated(self):
        for mode in ("manifest", "publisher", "registry"):
            valid = checker(mode)
            c.check_checker(mode, valid)
            changes = [{"exit_code": True}, {"exit_code": 124}, {"stderr": "traceback"}, {"output": ""},
                       {"warnings": []}, {"output": valid["output"] + "\nskipping schema"}]
            if mode == "registry":
                changes.extend([{"output": valid["output"].replace("400 Bad Request", "503 Service Unavailable")},
                                {"output": valid["output"] + "\n✗ Other failure"}])
            else:
                changes.extend([{"summary_warnings": True}, {"output": valid["output"].replace("All 0 entries", "All 1 entries")},
                                {"output": valid["output"].replace("✓ Manifest validates against ArdManifest.", "")},
                                {"output": valid["output"].replace("CONFORMANCE STATUS: PASS", "CONFORMANCE STATUS: FAIL")}])
            for change in changes:
                with self.subTest(mode=mode, change=change), self.assertRaises(ValueError):
                    c.check_checker(mode, {**valid, **change})

    def test_semantic_digest_preserves_types_and_ignores_member_order(self):
        self.assertEqual(c.document_digest({"a": 1, "b": 2}), c.document_digest({"b": 2, "a": 1}))
        self.assertNotEqual(c.document_digest({"a": 1}), c.document_digest({"a": True}))


class Prerequisites(unittest.TestCase):
    def test_kubectl_cache_and_tool_temp_files_stay_inside_owned_private_directory(self):
        private = Path("/owned/private")
        client = lab.Lab.__new__(lab.Lab)
        client.cache = private / "kube-cache"
        client.env = lab.environment(private)
        client.state = {"kubeconfig": "/selected/private/kubeconfig", "profile": "selected", "namespace": "agntcy-research"}
        with patch.object(lab, "run") as command:
            client.kubectl("get", "pods", "-o", "json")
        argv = command.call_args.args[0]
        self.assertEqual(argv[argv.index("--cache-dir") + 1], str(private / "kube-cache"))
        self.assertEqual(argv[argv.index("--kubeconfig") + 1], client.state["kubeconfig"])
        self.assertEqual(argv[argv.index("--context") + 1], "selected")
        for key in ("XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "TMPDIR"):
            self.assertTrue(Path(client.env[key]).is_relative_to(private))
        self.assertNotIn("HOME", client.env)

    def test_explicit_owned_kubernetes_context_and_no_external_auth(self):
        valid = {"contexts": [{"name": "lab", "context": {"cluster": "lab", "user": "lab"}}],
                 "clusters": [{"name": "lab", "cluster": {"server": "https://127.0.0.1:1234"}}],
                 "users": [{"name": "lab", "user": {"client-certificate": "/owned/cert"}}]}
        lab.validate_kubeconfig(valid, {"profile": "lab"})
        for changes in ({"server": "https://public.example:443"}, {"server": "http://127.0.0.1:1234"},
                        {"server": "https://user@127.0.0.1:1234"}, {"insecure-skip-tls-verify": True}, {"proxy-url": "https://proxy"}):
            altered = copy.deepcopy(valid)
            altered["clusters"][0]["cluster"].update(changes)
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                lab.validate_kubeconfig(altered, {"profile": "lab"})
        valid["users"][0]["user"]["exec"] = {"command": "external-auth"}
        with self.assertRaises(ValueError):
            lab.validate_kubeconfig(valid, {"profile": "lab"})

    def test_explicit_lab_selection_and_foreign_private_paths(self):
        with self.assertRaisesRegex(ValueError, "AGNTCY_LAB_DIR"):
            lab.selected_lab("")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "private").mkdir()
            (root / "private/state.json").write_text(json.dumps({"kubeconfig": "/not-owned", "minikubeHome": "/not-owned"}))
            with self.assertRaisesRegex(ValueError, "own private"):
                lab.selected_lab(directory)

    def test_wrong_missing_duplicate_or_unready_images_are_rejected(self):
        lock = lab.read_json(lab.ASSETS / "lock.json")
        pods = []
        images = {**lock["images"], "router": "agntcy-ard-schema-fixture:local"}
        for name, image in images.items():
            pods.append(dict(metadata={"uid": name}, spec={"containers": [dict(name=name, image=image)]},
                             status={"containerStatuses": [dict(name=name, ready=True, containerID=name,
                                 imageID=lock["router_image_id"] if name == "router" else image.split("@")[1])]}))
        valid = {"items": pods}
        self.assertEqual(len(lab.pod_identities(valid, lock)), 6)
        for change in (lambda v: v["items"].pop(), lambda v: v["items"].append(v["items"][0]),
                       lambda v: v["items"][0]["status"]["containerStatuses"][0].update(ready=False),
                       lambda v: v["items"][0]["status"]["containerStatuses"][0].update(imageID="sha256:wrong"),
                       lambda v: v["items"][0]["spec"]["containers"][0].update(image="unexpected:latest")):
            altered = copy.deepcopy(valid)
            change(altered)
            with self.subTest(change=change), self.assertRaises(ValueError):
                lab.pod_identities(altered, lock)

    def test_http_origin_and_path_rejection_precedes_io(self):
        for origin in ("https://example.com:443", "http://172.25.0.1:80", "https://0.0.0.0:443",
                       "https://169.254.169.254:443", "https://user@127.0.0.1:443", "https://127.0.0.1:443/path"):
            with self.subTest(origin=origin), self.assertRaises(ValueError):
                lab.LocalHTTP({"peer": origin}, "/never-read")
        with patch.object(lab.ssl, "create_default_context"), patch.object(lab.urllib.request, "build_opener") as opener:
            client = lab.LocalHTTP({"peer": "https://127.0.0.1:443"}, "/owned/ca")
            for peer, path in (("other", "/"), ("peer", "//example.com"), ("peer", "/\\example.com")):
                with self.assertRaises(ValueError):
                    client.request(peer, path)
            opener.return_value.open.assert_not_called()


class Artifacts(unittest.TestCase):
    def test_push_pull_export_uses_pinned_bytes_and_no_import_or_signing(self):
        seed, card = lab.read_json(lab.ASSETS / "record.json"), lab.read_json(lab.ASSETS / "card.json")
        client = Mock(lock=lab.read_json(lab.ASSETS / "lock.json"))
        client.native.side_effect = [Result(0, json.dumps(seed), ""), Result(0, client.lock["cid"], ""),
                                     Result(0, json.dumps(seed), ""), Result(0, json.dumps(card), "")]
        self.assertEqual(r.native_artifact(client), (card, True))
        self.assertEqual([call.args[0] for call in client.native.call_args_list], ["pull", "push", "pull", "export"])
        self.assertNotIn("--sign", str(client.native.call_args_list))

    def test_changed_existing_record_or_unavailable_server_never_triggers_push(self):
        for result in (Result(0, "{}", ""), Result(1, "", "Unavailable")):
            client = Mock(lock=lab.read_json(lab.ASSETS / "lock.json"))
            client.native.return_value = result
            with self.subTest(result=result), self.assertRaises((AssertionError, RuntimeError)):
                r.native_artifact(client)
            self.assertEqual(client.native.call_count, 1)

    def test_existing_record_and_export_are_both_compared(self):
        seed = lab.read_json(lab.ASSETS / "record.json")
        for position in (2, 3):
            client = Mock(lock=lab.read_json(lab.ASSETS / "lock.json"))
            results = [Result(0, json.dumps(seed), ""), Result(0, client.lock["cid"], ""),
                       Result(0, json.dumps(seed), ""), Result(0, json.dumps(lab.read_json(lab.ASSETS / "card.json")), "")]
            results[position] = Result(0, "{}", "")
            client.native.side_effect = results
            with self.subTest(position=position), self.assertRaises(AssertionError):
                r.native_artifact(client)


class Supervisor(unittest.TestCase):
    def test_interrupt_during_spawn_is_deferred_until_ownership_is_registered(self):
        previous = signal.getsignal(signal.SIGTERM)
        registered = []
        with self.assertRaisesRegex(KeyboardInterrupt, "registering"):
            with r.register_process():
                signal.getsignal(signal.SIGTERM)(signal.SIGTERM, None)
                registered.append("child reference and PID retained")
        self.assertEqual(registered, ["child reference and PID retained"])
        self.assertIs(signal.getsignal(signal.SIGTERM), previous)

    def test_shared_binary_build_stays_offline_without_creating_an_image(self):
        with patch.object(r.shared, "run") as command, patch.object(r.shared, "docker") as docker:
            r.shared.build_binaries(Path("/owned/build"), "/owned/go", "amd64")
        self.assertEqual(command.call_count, 2)
        self.assertEqual([call.args[0][-1] for call in command.call_args_list], ["./cmd/catalogd", "./cmd/catalogctl"])
        for call in command.call_args_list:
            self.assertEqual(call.args[0][0], "/owned/go")
            self.assertEqual(call.kwargs["env"]["GOPROXY"], "off")
            self.assertEqual(call.kwargs["env"]["GOTOOLCHAIN"], "local")
            self.assertEqual(call.kwargs["env"]["GOFLAGS"], "-mod=readonly")
            self.assertEqual(call.kwargs["timeout"], 300)
        docker.assert_not_called()

    def exercise(self, *, malformed=False, setup_error=None, cleanup_error=None, interruption=False):
        values = evidence()
        version = next(line.split()[1] for line in (r.shared.ROOT / "go.mod").read_text().splitlines() if line.startswith("go "))
        client = Mock(lock=lab.read_json(lab.ASSETS / "lock.json"))
        client.env = {}
        client.certificate = Path("/owned/certificate")
        client.preflight.return_value = {"workloads": {"owned": "unchanged"}}
        client.preflight.side_effect = setup_error
        client.workloads.return_value = {"owned": "unchanged"}
        with tempfile.TemporaryDirectory() as directory, contextlib.ExitStack() as stack:
            stack.enter_context(patch.object(r.shared, "OUTPUT", Path(directory)))
            stack.enter_context(patch.object(r, "Lab", return_value=client))
            stack.enter_context(patch.object(r, "run", return_value=Result(0, "go version go" + version + " linux/amd64", "")))
            stack.enter_context(patch.object(r.shutil, "which", return_value="/owned/go"))
            stack.enter_context(patch.object(r.shared, "build_binaries"))
            stack.enter_context(patch.object(r.shared, "fingerprint", return_value="source"))
            stack.enter_context(patch.object(r, "file_digest", return_value="binary"))
            stack.enter_context(patch.object(r, "native_artifact", return_value=({}, True)))
            socket = stack.enter_context(patch.object(r.socket, "socket"))
            socket.return_value.__enter__.return_value.getsockname.return_value = ("127.0.0.1", 1234)
            process = Mock(pid=123, returncode=0)
            process.poll.return_value = None
            stack.enter_context(patch.object(r.subprocess, "Popen", return_value=process))
            stack.enter_context(patch.object(r, "LocalHTTP"))
            stack.enter_context(patch.object(r.signal, "setitimer"))
            stop = stack.enter_context(patch.object(r, "stop_catalog", side_effect=cleanup_error))
            stack.enter_context(contextlib.redirect_stdout(io.StringIO()))
            for name, value in values.items():
                stack.enter_context(patch.object(r, name, return_value=None if malformed and name == "artifact" else value,
                                                side_effect=KeyboardInterrupt("interrupted") if interruption and name == "artifact" else None))
            result = r.check("explicit-lab")
            report = json.loads(next(Path(directory).glob("*/result.json")).read_text())
            self.assertFalse(Path(report["owned"]["private_directory"]).exists())
            stop.assert_called_once_with(None if setup_error else process)
            return result, report

    def test_success_validates_four_groups_and_cleans_owned_state(self):
        code, report = self.exercise()
        self.assertEqual((code, report["status"], report["passed"], report["cleanup"]["status"]), (0, "pass", 4, "pass"))

    def test_malformed_report_setup_interruption_and_cleanup_fail_closed(self):
        for options in ({"malformed": True}, {"setup_error": ValueError("wrong pin")},
                        {"cleanup_error": RuntimeError("cleanup failed")}, {"interruption": True}):
            with self.subTest(options=options):
                code, report = self.exercise(**options)
                self.assertEqual((code, report["status"]), (1, "fail"))
                self.assertEqual(report["cleanup"]["status"], "fail" if "cleanup_error" in options else "pass")
