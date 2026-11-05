"""ANS helper units inspect inert files and injected transports, never the peer runtime."""

import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
import urllib.request
from unittest.mock import patch

import ans_tools as tools
from ans import lifecycle

spec = importlib.util.spec_from_file_location("ans_native", Path(__file__).parent / "ans/native.py")
native = importlib.util.module_from_spec(spec)
spec.loader.exec_module(native)


class ANSUnits(unittest.TestCase):
    def test_original_search_outcome_and_pinned_lowercase_alias_checks_are_independent(self):
        validate = lifecycle.native.validate
        original_errors = []

        def selected(instance, definition):
            # Runtime supplies the exact OpenAPI converted by the Go harness.
            # Inject only its outcome here; alias checks use the retained schema.
            return original_errors if definition == "SearchResponse" else validate(instance, definition)

        with patch.object(lifecycle.native, "validate", side_effect=selected):
            row = {"identifier": "urn:air:alpha.ans.test:agents:alpha", "score": 90, "source": "https://finder.test",
                   "trustManifest": {"identity": "https://alpha.ans.test", "future": {"sequence": 9007199254740993}}}
            result = lifecycle.ard_contracts({"results": [row]})
            self.assertEqual(result, {"original_search_errors": [], "lowercase_alias_errors": [], "trust_manifests_checked": 1})
            self.assertEqual(row["trustManifest"]["future"]["sequence"], 9007199254740993)
            self.assertNotIn("TrustManifest", row)
            self.assertEqual(lifecycle.ard_contracts({"results": []})["trust_manifests_checked"], 0)
            for manifest in (None, {}, {"identity": 17}, {"identity": "https://alpha.ans.test", "attestations": [{"type": "ANS-Registration", "uri": 17}]}):
                document = {"results": [{**row, "trustManifest": manifest}]}
                with self.subTest(manifest=manifest), self.assertRaisesRegex(AssertionError, "lowercase trust"):
                    lifecycle.ard_contracts(document)
            original_errors.append({"rule": "type", "path": "/results/0/score"})
            with self.assertRaisesRegex(AssertionError, "original pinned"):
                lifecycle.ard_contracts({"results": [{**row, "score": "invalid"}]})
            original_errors.clear()
            with self.assertRaisesRegex(AssertionError, "conflicting"):
                lifecycle.ard_contracts({"results": [{**row, "TrustManifest": row["trustManifest"]}]})

    def test_ard_checks_exact_inventory_and_preserves_admitted_native_trust(self):
        state = {"locals": ["urn:air:local.test:agents:local"], "source_id": "private-source-id"}
        local = {"identifier": state["locals"][0]}
        entry = {"identifier": "urn:air:alpha.ans.test:agents:alpha", "version": "1.0.0", "type": native.MCP,
                 "url": "https://alpha.ans.test/card", "score": 90, "source": "https://finder.test",
                 "metadata": {"logId": "original"}, "trustManifest": {"identity": "https://alpha.ans.test", "identityType": "https",
                 "attestations": [{"type": "ANS-Registration", "uri": "https://tl.test/v1/agents/alpha/receipt", "mediaType": "application/scitt-receipt+cose"}]}}
        projected = dict(entry)
        lifecycle.assert_ard([local, entry], state, [entry])
        lifecycle.assert_ard([local], state, [])
        for rows in ([projected], [local], [local, projected, projected], [local, projected, {"identifier": "unexpected"}]):
            with self.subTest(rows=rows), self.assertRaisesRegex(AssertionError, "membership"):
                lifecycle.assert_ard(rows, state, [entry])
        for changes in ({"version": "2.0.0"}, {"source": "https://wrong.test"}, {"url": "https://wrong.test"},
                        {"metadata": {"logId": "changed"}}, {"trustManifest": {"identity": "https://wrong.test"}},
                        {"trustManifest": None}, {"trustManifest": {"identity": "https://alpha.ans.test"}}):
            with self.subTest(changes=changes), self.assertRaisesRegex(AssertionError, "ARD changed"):
                lifecycle.assert_ard([local, {**projected, **changes}], state, [entry])
        for name, rows in (
            ("omitted trust", [local, {key: value for key, value in entry.items() if key != "trustManifest"}]),
            ("uppercase output", [local, {**entry, "TrustManifest": entry["trustManifest"]}]),
            ("local inheritance", [{**local, "trustManifest": entry["trustManifest"]}, entry]),
            ("local uppercase claim", [{**local, "TrustManifest": entry["trustManifest"]}, entry]),
            ("changed receipt", [local, {**entry, "trustManifest": {**entry["trustManifest"], "attestations": [{"type": "ANS-Registration", "uri": "https://wrong.test/receipt"}]}}]),
        ):
            with self.subTest(name=name), self.assertRaises(AssertionError):
                lifecycle.assert_ard(rows, state, [entry])
        with self.assertRaisesRegex(AssertionError, "private source"):
            lifecycle.assert_ard([{**local, "private": state["source_id"]}, projected], state, [entry])

    def test_authenticated_host_overrides_urllib_default_without_new_destination(self):
        for path, wanted in (("/ard/v1/search", "localhost"), ("/api/v1/catalog/search", "127.0.0.1:1234")):
            request = urllib.request.Request("http://127.0.0.1:1234" + path)
            request.add_unredirected_header("Host", "127.0.0.1:1234")
            result = lifecycle.DiscoveryHost().http_request(request)
            self.assertEqual(result.get_header("Host"), wanted)
            self.assertEqual(result.full_url, "http://127.0.0.1:1234" + path)

    def test_live_membership_preserves_identity_provenance_and_native_extensions(self):
        identifier = "urn:air:alpha.ans.test:agents:alpha"
        entry = {"identifier": identifier, "type": native.MCP, "url": "https://alpha.ans.test/mcp-card", "trustManifest": {"identity": "https://alpha.ans.test"}, "metadata": {"logId": "native"}}
        state = {"source_id": "source", "origin": "https://finder.test"}
        row = {"identifier": identifier, "sourceId": "source", "source": state["origin"], "versionPresent": True, "version": "1.0.0", "entry": entry}
        lifecycle.assert_live([row], state, [entry])
        lifecycle.assert_live([{**row, "kind": "mcp_server"}], state, [entry])
        alternate = {**entry, "type": native.A2A, "url": "https://alpha.ans.test/agent-card"}
        variant = {**row, "entry": alternate}
        lifecycle.assert_live([row, variant], state, [entry, alternate], variants=True)
        for rows in ([row], [row, row], [row, variant, variant]):
            with self.subTest(variants=rows), self.assertRaises(AssertionError):
                lifecycle.assert_live(rows, state, [entry, alternate], variants=True)
        for changes in ({"sourceId": "other"}, {"source": "https://other.test"}, {"versionPresent": False}, {"version": "2.0.0"}, {"entry": {**entry, "metadata": {}}}, {"kind": "agent"}):
            with self.subTest(changes=changes), self.assertRaises(AssertionError):
                lifecycle.assert_live([{**row, **changes}], state, [entry])
        for rows in ([], [row, row], [{**row, "identifier": "wrong"}]):
            with self.assertRaisesRegex(AssertionError, "membership"):
                lifecycle.assert_live(rows, state, [entry])

    def test_exact_source_inventory_refuses_links_and_detects_extra_bytes(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            (directory / "source").write_text("pinned")
            before = tools.digest_tree(directory)
            (directory / "extra").write_text("changed")
            self.assertNotEqual(before, tools.digest_tree(directory))
            (directory / "link").symlink_to(directory / "source")
            with self.assertRaisesRegex(ValueError, "link"):
                tools.digest_tree(directory)

    def test_entry_schema_rejects_malformed_identifiers(self):
        self.assertTrue(native.validate({"identifier": "bad"}, "ArdEntry"))
        self.assertEqual(native.validate({"identifier": "urn:air:alpha.ans.test:agents:alpha", "displayName": "alpha", "type": native.MCP, "url": "https://alpha.ans.test/card.json"}, "ArdEntry"), [])

    def test_native_transport_rejects_nonlocal_origins_before_io(self):
        for origin in ("https://public.example:8443", "http://127.0.0.1:8080", "https://127.0.0.1:8443/path", "https://token@127.0.0.1:8443", "https://127.0.0.1:8443?secret=yes"):
            settings = {"ra": "http://127.0.0.1:18080", "tl": "http://127.0.0.1:18081", "finder": "http://127.0.0.1:18082", "finder-edge": origin}
            with self.subTest(origin=origin), patch.object(native.ssl, "create_default_context") as tls:
                with self.assertRaises(ValueError):
                    native.Native(settings)
                tls.assert_not_called()

    def test_traversal_rejects_repeated_tokens_and_invalid_envelopes(self):
        class Fake:
            def __init__(self, document):
                self.document = document
            def request(self, *args):
                return json.dumps(self.document).encode()
        with patch.object(native, "validate", return_value=[]), self.assertRaisesRegex(AssertionError, "repeated"):
            native.traverse(Fake({"results": [], "pageToken": "repeated"}), {"text": "weather"})
        with patch.object(native, "validate", return_value=[{"rule": "type"}]), self.assertRaisesRegex(AssertionError, "schema"):
            native.traverse(Fake({"results": "not-an-array"}), {"text": "weather"})

    def test_exact_membership_rejects_empty_duplicate_and_wrong_identity(self):
        definition = native.seed_definition("alpha", "MCP")
        row = {"host": definition["agentHost"], "definition": definition}
        for entries in ([], [{"identifier": "wrong"}], [{"identifier": "duplicate"}] * 2):
            with self.assertRaisesRegex(AssertionError, "membership"):
                native.assert_entries(entries, [row], {})
        shared = native.seed_definition("shared", "MCP", shared=True)
        self.assertEqual([e["protocol"] for e in shared["endpoints"]], ["MCP", "A2A"])
        self.assertEqual(shared["endpoints"][0]["agentUrl"], shared["endpoints"][1]["agentUrl"])

    def test_installation_stamp_cannot_hide_source_or_image_changes(self):
        actual = {"source": "changed"}
        with tempfile.TemporaryDirectory() as temporary, patch.object(tools, "TOOLS", Path(temporary)), patch.object(tools, "inspect", return_value=actual):
            (Path(temporary) / "installation.json").write_text(json.dumps({"source": "original"}))
            with self.assertRaisesRegex(ValueError, "changed"):
                tools.check()

    def test_identity_csr_uses_ans_uri_and_server_csr_uses_dns_name(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            def emitted(command, **kwargs):
                Path(command[command.index("-keyout") + 1]).write_text("private key")
                Path(command[command.index("-out") + 1]).write_text("public CSR")
            with patch.object(native, "run", side_effect=emitted) as command:
                self.assertEqual(native.csr("alpha.ans.test", directory, "identity"), "public CSR")
                args = command.call_args.args[0]
                self.assertIn("CN=ans://v1.0.0.alpha.ans.test", Path(args[args.index("-config") + 1]).read_text())
                self.assertIn("subjectAltName=URI:ans://v1.0.0.alpha.ans.test", Path(args[args.index("-config") + 1]).read_text())
                self.assertEqual(native.csr("alpha.ans.test", directory, "server"), "public CSR")
                args = command.call_args.args[0]
                self.assertIn("CN=alpha.ans.test", Path(args[args.index("-config") + 1]).read_text())
                self.assertIn("subjectAltName=DNS:alpha.ans.test", Path(args[args.index("-config") + 1]).read_text())
