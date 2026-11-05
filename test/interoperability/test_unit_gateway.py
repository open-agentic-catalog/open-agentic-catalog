"""Gateway fixture units use files and injected responses, never an independent runtime."""

import copy
import base64
from contextlib import redirect_stdout
import hashlib
import io
import json
import os
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import Mock, patch

import gateway_tools as tools
from gateway import native


class Acquisition(unittest.TestCase):
    def test_platform_refusal_precedes_docker_and_network(self):
        with (
            patch.object(tools.platform, "system", return_value="Windows"),
            patch.object(tools.shared, "local_daemon") as daemon,
            patch.object(tools, "command") as command,
        ):
            with self.assertRaisesRegex(ValueError, "Linux AMD64"):
                tools.install()
            daemon.assert_not_called()
            command.assert_not_called()

    def test_environment_excludes_cluster_selectors_proxies_and_credentials(self):
        with patch.dict(
            os.environ,
            {
                "PATH": "/tools",
                "HOME": "/owned",
                "AWS_SECRET_ACCESS_KEY": "secret",
                "KUBECONFIG": "/ambient",
                "HTTPS_PROXY": "http://ambient",
            },
            clear=True,
        ):
            self.assertEqual(tools.environment(), {"PATH": "/tools", "HOME": "/owned"})

    def test_exact_tree_includes_paths_bytes_extra_files_and_reviewed_links(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "LICENSE").write_bytes(b"license")
            (root / "docs").mkdir()
            (root / "docs/LICENSE.md").symlink_to("../LICENSE")
            expected = hashlib.sha256(
                b"LICENSE\0"
                + hashlib.sha256(b"license").digest()
                + b"docs/LICENSE.md\0link\0../LICENSE\0"
            ).hexdigest()
            self.assertEqual(tools.tree_digest(root), expected)
            (root / "extra").write_text("unexpected")
            self.assertNotEqual(tools.tree_digest(root), expected)
            (root / "docs/LICENSE.md").unlink()
            (root / "docs/LICENSE.md").symlink_to("/etc/passwd")
            with self.assertRaisesRegex(ValueError, "unreviewed"):
                tools.tree_digest(root)
            with self.assertRaises(ValueError):
                tools.tree_digest(root / "missing")

    def test_archive_refuses_traversal_devices_hardlinks_unreviewed_links_and_multiple_roots(
        self,
    ):
        def archive(entries):
            raw = io.BytesIO()
            with tarfile.open(fileobj=raw, mode="w:gz") as stream:
                for name, kind, target in entries:
                    info = tarfile.TarInfo(name)
                    info.type = kind
                    info.linkname = target
                    stream.addfile(info, io.BytesIO())
            return raw.getvalue()

        cases = [
            [("../escaped", tarfile.REGTYPE, "")],
            [("/escaped", tarfile.REGTYPE, "")],
            [("root/a", tarfile.LNKTYPE, "root/b")],
            [("root/docs/LICENSE.md", tarfile.SYMTYPE, "../../escaped")],
            [("root/a", tarfile.CHRTYPE, "")],
            [("one/a", tarfile.REGTYPE, ""), ("two/b", tarfile.REGTYPE, "")],
            [("root/a", tarfile.REGTYPE, ""), ("root/a", tarfile.REGTYPE, "")],
        ]
        for entries in cases:
            with (
                self.subTest(entries=entries),
                tempfile.TemporaryDirectory() as temporary,
                self.assertRaises(ValueError),
            ):
                tools.extract(archive(entries), Path(temporary), "wrong")
        entries = [
            ("root/LICENSE", tarfile.REGTYPE, ""),
            ("root/docs/LICENSE.md", tarfile.SYMTYPE, "../LICENSE"),
        ]
        expected = hashlib.sha256(
            b"LICENSE\0"
            + hashlib.sha256(b"").digest()
            + b"docs/LICENSE.md\0link\0../LICENSE\0"
        ).hexdigest()
        with tempfile.TemporaryDirectory() as temporary:
            root = tools.extract(archive(entries), Path(temporary), expected)
            self.assertEqual(tools.tree_digest(root), expected)
        with (
            tempfile.TemporaryDirectory() as temporary,
            self.assertRaisesRegex(ValueError, "exact pin"),
        ):
            tools.extract(archive(entries), Path(temporary), "wrong")


class Pagination(unittest.TestCase):
    def setUp(self):
        self.client = object.__new__(native.Native)
        self.client.origin = native.ORIGIN
        self.client.request = Mock()
        self.first = {
            "identifier": next(iter(native.expected("alpha"))),
            "type": native.MCP,
            "url": native.ORIGIN + "/api/public/servers/alpha/server.json",
        }
        self.second = {**self.first, "identifier": next(iter(native.expected("beta")))}

    def test_walk_requires_complete_unique_membership_and_native_page_size(self):
        self.client.request.side_effect = [
            {"items": [self.first], "total": 2, "pageToken": "next"},
            {"items": [self.second], "total": 2, "pageToken": None},
        ]
        with patch.object(
            native,
            "validate",
            return_value=[{"path": "/pageToken", "validator": "type"}],
        ):
            result = self.client.walk("list", native.expected("alpha", "beta"))
        self.assertEqual(
            result["identifiers"], sorted(native.expected("alpha", "beta"))
        )
        self.assertIn("pageSize=1", self.client.request.call_args_list[0].args[0])
        self.assertIn("pageToken=next", self.client.request.call_args_list[1].args[0])
        self.assertEqual(
            len(result["schema_errors"]), 2
        )  # Preserve native schema failures instead of repairing payloads.

    def test_incomplete_duplicate_wrong_origin_type_total_and_cursor_cycle_fail(self):
        valid = {"items": [self.second], "total": 2, "pageToken": None}
        changes = [
            lambda p: p.update(items=[]),
            lambda p: p.update(pageToken="next"),
            lambda p: p.update(total=3),
            lambda p: p.update(total=True),
            lambda p: p["items"][0].update(identifier=self.first["identifier"]),
            lambda p: p["items"][0].update(type="other"),
            lambda p: p["items"][0].update(url="https://external.invalid/descriptor"),
        ]
        for change in changes:
            page = copy.deepcopy(valid)
            change(page)
            self.client.request.side_effect = [
                {"items": [self.first], "total": 2, "pageToken": "next"},
                page,
            ]
            with (
                self.subTest(change=change),
                patch.object(native, "validate", return_value=[]),
                self.assertRaises(AssertionError),
            ):
                self.client.walk("list", native.expected("alpha", "beta"))
        with self.assertRaises(ValueError):
            native.identities([self.first, self.first])

    def test_search_omits_absent_cursor_and_sends_exact_continuation(self):
        self.client.request.side_effect = [
            {
                "results": [
                    {
                        **self.first,
                        "source": native.ORIGIN + "/api/ard/search",
                        "score": 1,
                    }
                ],
                "referrals": [],
                "pageToken": "next",
            },
            {
                "results": [
                    {
                        **self.second,
                        "source": native.ORIGIN + "/api/ard/search",
                        "score": 2,
                    }
                ],
                "referrals": [],
                "pageToken": None,
            },
        ]
        with patch.object(native, "validate", return_value=[]):
            self.client.walk("search", native.expected("alpha", "beta"))
        first, second = self.client.request.call_args_list
        self.assertNotIn("pageToken", first.kwargs["body"])
        self.assertEqual(second.kwargs["body"]["pageToken"], "next")

    def test_schema_validation_is_local_and_rejects_optional_nulls(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            schema_dir = root / "spec/schemas"
            schema_dir.mkdir(parents=True)
            schema = {
                "$schema": "https://json-schema.org/draft/2020-12/schema",
                "$defs": {
                    "ArdEntry": {
                        "type": "object",
                        "properties": {"url": {"type": "string"}},
                    },
                    "ArdManifest": {"type": "object"},
                },
            }
            (schema_dir / "ard-entry.schema.json").write_text(json.dumps(schema))
            api = {
                "components": {
                    "schemas": {
                        "ListResponse": {
                            "type": "object",
                            "properties": {
                                "pageToken": {"type": "string"},
                                "items": {
                                    "type": "array",
                                    "items": {
                                        "$ref": "./ard-entry.schema.json#/$defs/ArdEntry"
                                    },
                                },
                            },
                        }
                    }
                }
            }
            (schema_dir / "ard.openapi.json").write_text(json.dumps(api))
            with patch.object(native, "DIRECTORY", root):
                self.assertEqual(
                    native.validate(
                        {
                            "items": [{"url": "https://local.invalid"}],
                            "pageToken": "next",
                        },
                        "ListResponse",
                    ),
                    [],
                )
                errors = native.validate(
                    {"items": [{"url": None}], "pageToken": None}, "ListResponse"
                )
                self.assertEqual(
                    [e["path"] for e in errors], ["/items/0/url", "/pageToken"]
                )


class Transport(unittest.TestCase):
    def test_optimized_python_cannot_report_a_passing_qualification(self):
        namespace = {"__name__": "optimized_gateway", "__file__": native.__file__}
        exec(
            compile(
                Path(native.__file__).read_text(), native.__file__, "exec", optimize=1
            ),
            namespace,
        )
        namespace["Native"] = Mock(
            return_value=Mock(captures=[], evidence={}, secrets={})
        )
        namespace["qualify"] = Mock()
        output = io.StringIO()
        with (
            patch.object(Path, "read_text", return_value="{}"),
            redirect_stdout(output),
        ):
            self.assertEqual(namespace["main"](), 1)
        self.assertEqual(json.loads(output.getvalue())["status"], "fail")
        self.assertIn("requires assertions", json.loads(output.getvalue())["error"])
        namespace["qualify"].assert_not_called()

    def test_mcp_card_checks_the_named_definition_instead_of_empty_schema_root(self):
        directory = (
            tools.shared.ROOT
            / "schemas/upstream/mcp/mcp-server-card-v1-experimental-526201b"
        )
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "spec/schemas").mkdir(parents=True)
            (root / "spec/schemas/mcp-card.schema.json").write_bytes(
                (directory / "schema.json").read_bytes()
            )
            with patch.object(native, "DIRECTORY", root):
                self.assertTrue(native.descriptor_errors({}, "mcp-card"))
                good = {
                    "$schema": "https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json",
                    "description": "Fixture",
                    "name": "local/fixture",
                    "version": "1.0.0",
                }
                self.assertEqual(native.descriptor_errors(good, "mcp-card"), [])
                good["$schema"] = (
                    "https://raw.githubusercontent.com/modelcontextprotocol/registry/main/docs/reference/server-json/draft/server.schema.json"
                )
                self.assertEqual(
                    [e["path"] for e in native.descriptor_errors(good, "mcp-card")],
                    ["/$schema"],
                )

    def test_refuses_redirect_before_follow_and_never_records_secret_or_oversized_bytes(
        self,
    ):
        with self.assertRaisesRegex(ValueError, "redirect"):
            native.NoRedirect().redirect_request(
                None, None, 302, "redirect", {}, "https://external.invalid"
            )
        client = object.__new__(native.Native)
        client.origin = native.ORIGIN
        client.secrets, client.captures, client.http = (
            {"admin": "synthetic-secret"},
            [],
            Mock(),
        )
        for raw in (b'{"leaked":"synthetic-secret"}', b"x" * (native.LIMIT + 1)):
            response = Mock()
            response.__enter__ = Mock(return_value=response)
            response.__exit__ = Mock(return_value=False)
            response.read.return_value = raw
            response.geturl.return_value = native.ORIGIN + "/control"
            client.http.open.return_value = response
            with self.assertRaises(ValueError):
                client.request("/control")
            self.assertEqual(client.captures, [])
            response.read.assert_called_once_with(native.LIMIT + 1)
        client.http.reset_mock()
        for path in ("https://external.invalid", "//external.invalid"):
            with self.assertRaises(ValueError):
                client.request(path)
        client.http.open.assert_not_called()

    def test_characterization_refuses_new_failures_instead_of_accepting_any_error(self):
        def error(path):
            return {"path": path, "validator": "type", "null": True}

        evidence = {
            "publisher": {"schema_errors": []},
            "descriptors": {
                "fixture": {
                    "mcp_registry_errors": [],
                    "mcp_card_errors": [{"path": "/$schema", "validator": "pattern"}],
                }
            },
            "no_match": {"schema_errors": [error("/pageToken")]},
            "list_no_match": {"schema_errors": [error("/pageToken")]},
            "official": {
                "manifest": {
                    "exit": 0,
                    "output": "CONFORMANCE STATUS: PASS\nValidated with 0 critical specification errors and 1 warnings.",
                },
                "publisher": {
                    "exit": 0,
                    "output": "CONFORMANCE STATUS: PASS\npredecessor path\nValidated with 0 critical specification errors and 2 warnings.",
                },
                "registry": {
                    "exit": 1,
                    "output": "CONFORMANCE STATUS: FAIL\nPOST /explore returned server error: 405\nFound 1 critical specification errors.",
                },
            },
        }
        for name, paths in (
            ("list", ["/items/0/capabilities"]),
            (
                "search",
                [
                    "/results/0/capabilities",
                    "/results/0/updatedAt",
                    "/results/0/version",
                ],
            ),
        ):
            for suffix in (
                "",
                "_reader",
                "_private",
                "_types",
                "_media",
                "_tag",
                "_after_denial",
            ):
                evidence[name + suffix] = {
                    "identifiers": ["fixture"],
                    "schema_errors": [error(path) for path in [*paths, "/pageToken"]],
                }
        captures = [{"status": 401, "schema_errors": []} for _ in range(7)]
        native.check_characterization(evidence, captures)
        changes = [
            lambda e: e["publisher"]["schema_errors"].append(error("/unknown")),
            lambda e: e["list"]["schema_errors"].append(error("/extra")),
            lambda e: e["search"]["schema_errors"][0].update(null=False),
            lambda e: e["list_reader"]["schema_errors"].clear(),
            lambda e: e["list_media"]["schema_errors"].append(error("/extra")),
            lambda e: e["search_after_denial"]["schema_errors"].clear(),
            lambda e: e["list_no_match"]["schema_errors"].clear(),
            lambda e: e["no_match"]["schema_errors"].append(error("/extra")),
            lambda e: e["descriptors"]["fixture"]["mcp_registry_errors"].append(
                error("/name")
            ),
            lambda e: e["official"]["registry"].update(output="unrelated HTTP 500"),
            lambda e: e["official"]["publisher"].update(
                output="404; Validated with 2 critical specification errors"
            ),
        ]
        for change in changes:
            changed = copy.deepcopy(evidence)
            change(changed)
            with self.subTest(change=change), self.assertRaises(AssertionError):
                native.check_characterization(changed, captures)
        captures[0]["schema_errors"] = [error("/")]
        with self.assertRaises(AssertionError):
            native.check_characterization(evidence, captures)

    def test_error_envelopes_are_validated_without_repair_and_original_bytes_survive(
        self,
    ):
        client = object.__new__(native.Native)
        client.origin = native.ORIGIN
        client.secrets, client.captures, client.http = {"admin": "secret"}, [], Mock()
        for content_type, raw, document in (
            (
                "application/json",
                b'{"errorCode":"UNAUTHENTICATED","message":"Authentication required."}',
                {"errorCode": "UNAUTHENTICATED", "message": "Authentication required."},
            ),
            (
                "text/html",
                b"<html>native refusal</html>",
                "<html>native refusal</html>",
            ),
        ):
            response = Mock()
            response.__enter__ = Mock(return_value=response)
            response.__exit__ = Mock(return_value=False)
            response.read.return_value = raw
            response.geturl.return_value = native.ORIGIN + "/api/ard/agents"
            response.status, response.headers = 401, {"Content-Type": content_type}
            client.http.open.return_value = response
            errors = (
                [{"path": "/", "validator": "type"}]
                if content_type == "text/html"
                else []
            )
            with patch.object(native, "validate", return_value=errors) as validate:
                self.assertEqual(
                    client.request("/api/ard/agents", status=401), document
                )
            validate.assert_called_once_with(document, "Error")
            self.assertEqual(client.captures[-1]["schema_errors"], errors)
            self.assertEqual(
                client.captures[-1]["sha256"], hashlib.sha256(raw).hexdigest()
            )


class CatalogLifecycle(unittest.TestCase):
    def test_native_forward_is_loopback_only_and_retains_canonical_host(self):
        for origin in (
            "https://external.test:443",
            "http://127.0.0.1:1234",
            "https://127.0.0.1:1234/path",
            "https://user@127.0.0.1:1234",
            "https://127.0.0.2:1234",
            "https://127.0.0.1:1234?x",
        ):
            with self.assertRaisesRegex(ValueError, "owned loopback"):
                native.Native({}, origin=origin)
        with (
            patch.object(native.ssl, "create_default_context"),
            patch.object(native.urllib.request, "build_opener") as opener,
        ):
            client = native.Native(
                {}, origin="https://127.0.0.1:1234", certificate="/owned/ca.pem"
            )
            response = Mock()
            response.__enter__ = Mock(return_value=response)
            response.__exit__ = Mock(return_value=False)
            response.read.return_value = b"{}"
            response.geturl.return_value = "https://127.0.0.1:1234/control"
            response.status = 200
            response.headers = {"Content-Type": "application/json"}
            opener.return_value.open.return_value = response
            client.request("/control", credential="")
            request = opener.return_value.open.call_args.args[0]
            self.assertEqual(
                request.get_header("Host"),
                "gateway.gateway-qualification.svc.cluster.local",
            )

    def test_reverse_requires_healthy_catalog_no_fetch_and_unchanged_inventory(self):
        from gateway import lifecycle

        entries = [
            {"identifier": item}
            for item in sorted(
                native.expected(
                    "qualified-alpha",
                    "qualified-beta",
                    "qualified-gamma",
                    "qualified-private",
                )
            )
        ]
        manifest = b'{"entries":[{"identifier":"owned-resource"}]}'
        target = {"uri": lifecycle.REVERSE}
        outcome = dict(
            success=True, servers_synced=0, agents_synced=0, new_generation=1
        )
        status = {
            "sources": [
                dict(
                    source_id="owned-catalog",
                    generation=1,
                    consecutive_failures=0,
                    last_error=None,
                    servers_synced=0,
                    agents_synced=0,
                    skills_synced=0,
                    rejected=0,
                )
            ]
        }

        def attempt(change=""):
            api, peer = Mock(), Mock()
            api.request.side_effect = [
                (manifest, {}),
                (manifest if change != "body" else b"{}", {}),
                (b'{"requests":1}', {}),
                (b'{"requests":2}' if change == "fetch" else b'{"requests":1}', {}),
                (manifest, {}),
                (b'{"requests":2}', {}),
            ]
            result = copy.deepcopy(outcome)
            if change == "import":
                result["servers_synced"] = 1
            final = entries[:-1] if change == "inventory" else entries
            peer.request.side_effect = [
                {"items": entries},
                {"config": {"ai_catalog": {"sources": [target]}}},
                {"results": [result]},
                status,
                {"items": final},
            ]
            return lifecycle.reverse(api, peer)

        result = attempt()
        self.assertTrue(result["native_inventory_unchanged"])
        self.assertEqual(
            result["disposition"],
            "private-address-refusal-reported-as-successful-empty",
        )
        for change in ("body", "fetch", "import", "inventory"):
            with self.assertRaises(AssertionError, msg=change):
                attempt(change)

    def test_retention_checks_exact_bytes_digest_and_occurrence_binding(self):
        from gateway import lifecycle

        identifier = next(iter(native.expected("qualified-alpha")))
        entry = {"identifier": identifier, "type": native.MCP}
        raw_entry = json.dumps(entry).encode()
        raw_manifest = b'{"entries":[' + raw_entry + b"]}"
        snapshot = dict(
            id="snapshot",
            sourceId="source",
            memberCount=1,
            sourceRevision=1,
            bodyDigest=lifecycle.digest(raw_manifest),
            withdrawalAuthority=True,
        )
        row = dict(
            id="observation",
            identifier=identifier,
            sourceId="source",
            tenantId=lifecycle.TENANT,
            kind="mcp_server",
            rawEntryAvailable=True,
            exactEntryDigest=lifecycle.digest(raw_entry),
            identityDigest="identity",
            normalizedEntryDigest="normalized",
            freshness="current",
        )
        occurrence = dict(
            snapshotId="snapshot",
            observationId="observation",
            sourceRevision=1,
            evidenceComplete=True,
            normalizedEntryDigest="normalized",
            exactEntryDigest=lifecycle.digest(raw_entry),
            retrievalDigest=lifecycle.digest(raw_manifest),
            sourceId="source",
        )

        def attempt(change=""):
            api, peer = Mock(), Mock()
            api.document.return_value = ({"lastCompleteSnapshotId": "snapshot"}, {})
            modified = copy.deepcopy(row)
            relation = copy.deepcopy(occurrence)
            if change == "digest":
                modified["exactEntryDigest"] = "wrong"
            if change == "occurrence":
                relation["retrievalDigest"] = "wrong"
            api.pages.side_effect = [
                [modified],
                [relation],
                [] if change != "resource" else [{"id": "adopted"}],
            ]
            api.request.return_value = (raw_entry if change != "bytes" else b"{}", {})
            peer.request.return_value = {"entries": [entry]}
            peer.captures = [{"raw_base64": base64.b64encode(raw_manifest).decode()}]
            return lifecycle.retained(
                api,
                peer,
                {
                    "source_id": "source",
                    "resources": [],
                },
                ["alpha"],
                snapshot,
            )

        self.assertTrue(attempt()["resources_unchanged"])
        for change in ("digest", "occurrence", "resource", "bytes"):
            with self.assertRaises(AssertionError, msg=change):
                attempt(change)


class LifecycleBoundaries(unittest.TestCase):
    def test_advertised_replacement_uses_reviewed_removal_and_preserves_history(self):
        from gateway import lifecycle

        endpoint = native.ORIGIN + "/.well-known/ai-catalog.json"
        inspection = dict(
            id="inspection",
            sourceType="hybrid",
            profileId=lifecycle.CURRENT,
            registrySearchUrl=native.ORIGIN + "/api/ard/search",
        )
        snapshot = dict(id="snapshot", state="complete", memberCount=3)
        source = dict(
            id="replacement",
            endpoint=endpoint,
            type="hybrid",
            registrySearchUrl=inspection["registrySearchUrl"],
            retrievalMode="indexed",
        )
        for change in ("", "identity", "history"):
            api, peer = Mock(), Mock()
            modified = dict(source)
            if change == "identity":
                modified["id"] = "source"
            history = dict(snapshot)
            if change == "history":
                history["memberCount"] = 0
            api.inspect.side_effect = [
                inspection,
                *[
                    dict(
                        code="INVALID_ARGUMENT",
                        detail="Registry Search probe returned HTTP 401",
                    )
                    for _ in range(2)
                ],
                dict(
                    code="INVALID_ARGUMENT",
                    detail="Registry Search probe returned incompatible JSON",
                ),
            ]
            api.document.side_effect = [
                (dict(id="source", endpoint=endpoint), {"etag": '"1"'}),
                (snapshot, {}),
                (dict(digest="reviewed-removal"), {}),
                (dict(code="NOT_FOUND"), {}),
                (modified, {}),
                (history, {}),
            ]
            api.enqueue.return_value = {"id": "job"}
            api.complete.return_value = ({}, dict(memberCount=4, state="complete"))
            with (
                patch.object(lifecycle, "publisher"),
                patch.object(native, "check_registry_nulls"),
                patch.object(lifecycle, "retained", return_value={}),
            ):
                if change:
                    with self.assertRaises(AssertionError):
                        lifecycle.advertised(
                            api,
                            peer,
                            dict(
                                source_id="source", snapshot_id="snapshot", resources=[]
                            ),
                        )
                else:
                    result = lifecycle.advertised(
                        api,
                        peer,
                        dict(source_id="source", snapshot_id="snapshot", resources=[]),
                    )
                    self.assertTrue(result["previous_snapshot_unchanged"])
                    self.assertEqual(result["replaced_source_id"], "source")
                    self.assertEqual(result["previous_snapshot"], snapshot)
            api.request.assert_called_once_with(
                "catalog",
                lifecycle.PREFIX + "sources/source:remove",
                dict(
                    changePreviewDigest="reviewed-removal",
                    reason="Replace the source after a reviewed native interface change",
                ),
                expected=204,
                etag='"1"',
            )

    def test_advertised_probe_requires_complete_native_search_readiness(self):
        from gateway import lifecycle

        wanted = native.expected(
            "qualified-alpha", "qualified-beta", "qualified-gamma", "qualified-private"
        )
        result = dict(
            identifiers=sorted(wanted),
            schema_errors=[
                dict(path="/results/0/" + field, validator="type", null=True)
                for _ in wanted
                for field in ("capabilities", "updatedAt", "version")
            ]
            + [dict(path="/pageToken", validator="type", null=True)],
        )
        api, peer = Mock(), Mock()
        api.inspect.side_effect = RuntimeError("qualified Search reached Catalog")
        for change in ("", "schema", "startup"):
            peer.walk.reset_mock()
            peer.walk.side_effect = None
            peer.walk.return_value = copy.deepcopy(result)
            api.inspect.reset_mock()
            if change == "schema":
                peer.walk.return_value["schema_errors"][0]["null"] = False
            if change == "startup":
                peer.walk.side_effect = TimeoutError("native cold startup")
            with patch.object(lifecycle, "publisher"):
                if not change:
                    with self.assertRaisesRegex(RuntimeError, "qualified Search"):
                        lifecycle.advertised(api, peer, {})
                    api.inspect.assert_called_once_with()
                else:
                    with self.assertRaises((AssertionError, TimeoutError)):
                        lifecycle.advertised(api, peer, {})
                    api.inspect.assert_not_called()
            peer.walk.assert_called_once_with("search", wanted)

    def test_occurrence_pagination_uses_the_contract_composite_key(self):
        from gateway import lifecycle

        first = dict(
            snapshotId="snapshot-one",
            retrievalDigest="digest",
            entryLocation="/entries/0",
        )
        second = dict(
            snapshotId="snapshot-two",
            retrievalDigest="digest",
            entryLocation="/entries/0",
        )
        client = object.__new__(lifecycle.Catalog)
        for last, passes in ((second, True), (first, False)):
            client.document = Mock(
                side_effect=[
                    ({"items": [first], "nextCursor": "next"}, {}),
                    ({"items": [last]}, {}),
                ]
            )

            def identity(item):
                return (
                    item["snapshotId"],
                    item["retrievalDigest"],
                    item["entryLocation"],
                )

            if passes:
                self.assertEqual(
                    client.pages("/occurrences", identity=identity), [first, second]
                )
            else:
                with self.assertRaisesRegex(
                    AssertionError, "duplicate paginated items"
                ):
                    client.pages("/occurrences", identity=identity)
            self.assertEqual(
                client.document.call_args.args[0], "/occurrences?cursor=next&limit=3"
            )

    def test_publisher_requires_registered_names_and_separate_registry_self_entry(self):
        from gateway import lifecycle

        entry = {
            "identifier": "urn:air:gateway.gateway-qualification.svc.cluster.local:server:qualified-alpha",
            "type": native.MCP,
        }
        registry = {
            "identifier": "owned-registry",
            "type": "application/ai-registry+json",
        }
        peer = Mock()
        for entries, advertised in (([entry], False), ([entry, registry], True)):
            document = {"entries": entries}
            raw = json.dumps(document).encode()
            peer.request.return_value = document
            peer.captures = [{"raw_base64": base64.b64encode(raw).decode()}]
            self.assertEqual(
                lifecycle.publisher(peer, ["alpha"], advertised), (document, raw)
            )
            with self.assertRaises(AssertionError):
                lifecycle.publisher(peer, ["beta"], advertised)
        with self.assertRaises(AssertionError):
            lifecycle.publisher(peer, ["alpha"], False)

    def test_unknown_registry_entry_retains_raw_evidence_without_normalization(self):
        from gateway import lifecycle

        entry = {"identifier": "owned-registry", "type": "application/ai-registry+json"}
        raw_entry = json.dumps(entry).encode()
        raw = b'{"entries":[' + raw_entry + b"]}"
        snapshot = dict(
            id="snapshot",
            sourceId="source",
            memberCount=1,
            sourceRevision=1,
            bodyDigest=lifecycle.digest(raw),
            withdrawalAuthority=True,
        )
        row = dict(
            id="observation",
            identifier="owned-registry",
            sourceId="source",
            tenantId=lifecycle.TENANT,
            kind="",
            rawEntryAvailable=True,
            compatibility={"support": "unknown"},
            exactEntryDigest=lifecycle.digest(raw_entry),
            freshness="current",
        )
        occurrence = dict(
            snapshotId="snapshot",
            observationId="observation",
            sourceRevision=1,
            evidenceComplete=True,
            sourceId="source",
            exactEntryDigest=lifecycle.digest(raw_entry),
            retrievalDigest=lifecycle.digest(raw),
        )
        api, peer = Mock(), Mock()
        api.document.return_value = ({"lastCompleteSnapshotId": "snapshot"}, {})
        api.pages.side_effect = [[row], [occurrence], []]
        api.request.return_value = (raw_entry, {})
        peer.request.return_value = {"entries": [entry]}
        peer.captures = [{"raw_base64": base64.b64encode(raw).decode()}]
        result = lifecycle.retained(
            api,
            peer,
            {"source_id": "source", "resources": []},
            [],
            snapshot,
            registry=True,
        )
        self.assertIsNone(result["observations"][0]["normalizedEntryDigest"])
        self.assertTrue(result["resources_unchanged"])

    def test_terminal_job_failure_captures_diagnostics_before_cleanup(self):
        from gateway import lifecycle

        api = object.__new__(lifecycle.Catalog)
        api.document = Mock(return_value=({"state": "failed"}, {}))
        api.pages = Mock(return_value=[])
        with patch.object(
            lifecycle, "wait_for", side_effect=lambda _, check, **__: check()
        ):
            with self.assertRaisesRegex(AssertionError, "sync terminated"):
                api.complete("owned-job")
        self.assertEqual(api.document.call_args.args[0], "/api/v1/jobs/owned-job")
        self.assertEqual(api.pages.call_args.args[0], "/api/v1/audit-events")
        self.assertEqual(api.pages.call_args.kwargs, {"targetId": "owned-job"})

    def test_unexpected_response_retains_bounded_bytes_without_secrets(self):
        from agntcy_lab import LocalHTTP, UnexpectedStatus
        from gateway.lifecycle import Catalog

        settings = dict(
            token="admin-secret",
            reader="reader-secret",
            other="other-secret",
            secrets={},
        )
        for raw in (b'{"code":"INVALID_ARGUMENT"}', b"admin-secret"):
            client = object.__new__(Catalog)
            client.settings, client.captures = settings, []
            failure = UnexpectedStatus("catalog", "/inspect", 201, 400, raw)
            with patch.object(LocalHTTP, "request", side_effect=failure):
                if raw == b"admin-secret":
                    with self.assertRaisesRegex(AssertionError, "credential"):
                        client.request("catalog", "/inspect")
                    self.assertEqual(client.captures, [])
                else:
                    with self.assertRaises(UnexpectedStatus):
                        client.request("catalog", "/inspect")
                    self.assertEqual(client.captures[0]["status"], 400)
                    self.assertEqual(
                        base64.b64decode(client.captures[0]["raw_base64"]), raw
                    )

    def test_owned_origin_uses_fixed_upstream_closes_and_bounds_response(self):
        from gateway import catalog_origin

        for size in (catalog_origin.LIMIT, catalog_origin.LIMIT + 1):
            with patch.object(
                catalog_origin.http.client, "HTTPConnection"
            ) as connection:
                instance = connection.return_value
                response = instance.getresponse.return_value
                response.read.return_value = b"x" * size
                response.status = 200
                response.getheader.return_value = "application/json"
                if size > catalog_origin.LIMIT:
                    with self.assertRaisesRegex(ValueError, "bound"):
                        catalog_origin.manifest()
                else:
                    self.assertEqual(
                        catalog_origin.manifest(),
                        (200, "application/json", b"x" * size),
                    )
                instance.request.assert_called_once_with(
                    "GET",
                    catalog_origin.PATH,
                    headers={"Host": "127.0.0.1", "Accept": "application/json"},
                )
                response.read.assert_called_once_with(catalog_origin.LIMIT + 1)
                instance.close.assert_called_once()


if __name__ == "__main__":
    unittest.main()
