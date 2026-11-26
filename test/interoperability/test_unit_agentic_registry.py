"""Offline contract controls using invented IDs and the repository's pinned schemas."""

from http.client import HTTPMessage
import unittest

from jsonschema import ValidationError
import yaml

from agentic_registry_client import allows_discovery_request, live_item
from agentic_registry_native import ORIGIN, SKILL, check_publication, check_registry_item, inventory, matched_result
from agentic_registry_runner import projection_validators
from agentic_registry_tools import ROOT, support_images


class AgenticRegistryContracts(unittest.TestCase):
    def test_live_item_requires_the_peer_attribution_and_preserves_native_field_types(self):
        registry = "https://fixture.example/ard"
        source = {"id": "selected"}
        native = {"identifier": "urn:air:fixture.example:mcp:one", "displayName": "release-index", "description": "Documentation for release-index",
                  "version": "1.0.0", "source": registry, "score": 100, "obs:recommended": True}
        evidence = {"source": registry, "sourceId": "selected", "entry": dict(native)}
        row = {"identifier": native["identifier"], "displayName": "release-index", "kind": "mcp_server", "type": "live",
               "versionPresent": True, "version": "1.0.0", "score": 70, "live": evidence}
        self.assertEqual(live_item(row, native, source),
                         {"identifier": native["identifier"], "version": "1.0.0", "catalogScore": 70,
                          "upstreamScore": 100, "source": registry, "sourceId": "selected"})
        for change in ({"kind": "agent"}, {"versionPresent": False}, {"score": 100},
                       {"live": {**evidence, "source": "https://other.example/ard"}},
                       {"live": {**evidence, "entry": {**native, "obs:recommended": 1}}}):
            with self.subTest(change=change), self.assertRaises(AssertionError):
                live_item({**row, **change}, native, source)

    def test_bridge_allows_only_anonymous_requests_to_the_selected_discovery_routes(self):
        anonymous = HTTPMessage()
        anonymous["Accept"] = "application/json"
        for method, path in (("GET", "/.well-known/ard.json"), ("GET", "/.well-known/ai-catalog.json"),
                             ("GET", "/api/v1/ard/agents?pageSize=1"), ("POST", "/api/v1/ard/search")):
            with self.subTest(method=method, path=path):
                self.assertTrue(allows_discovery_request(method, path, anonymous))
                for name in ("authorization", "COOKIE", "Proxy-Authorization", "x-API-Key"):
                    for value in ("synthetic", ""):
                        with self.subTest(name=name, value=value):
                            headers = HTTPMessage()
                            headers[name] = value
                            self.assertFalse(allows_discovery_request(method, path, headers))
        for method, path in (("GET", "/api/v1/ard/search"), ("POST", "/.well-known/ard.json"),
                             ("GET", "/api/v1/ard/agents?pageSize=2"), ("GET", "/.well-known/ard.json?extra=1")):
            with self.subTest(method=method, path=path):
                self.assertFalse(allows_discovery_request(method, path, anonymous))

    def test_preparation_includes_the_catalog_database_from_the_application_stack(self):
        catalog_db = yaml.safe_load((ROOT / "compose.yaml").read_text())["services"]["postgres"]["image"]
        self.assertIn(catalog_db, support_images())

    def test_skill_evidence_cannot_substitute_another_unknown_format_or_stale_approval(self):
        entry = {"identifier": "urn:air:registry.example.test:skill:example", "type": "application/ai-skill+md",
                 "displayName": SKILL, "version": "1.1.0", "obs:nativeRef": f"author/{SKILL}@1.1.0",
                 "obs:visibility": "public", "obs:lifecycle": "approved", "obs:recommended": True}
        check_publication(entry, SKILL, "1.1.0")
        with self.assertRaisesRegex(AssertionError, "media type"):
            check_publication({**entry, "type": "application/vnd.example.other+json"}, SKILL, "1.1.0")
        projected = {**entry, "obs:approval": "approved"}
        check_registry_item(projected, entry)
        for changed in ({"obs:approval": "pending"}, {"obs:recommended": False}, {"version": "1.0.0"}):
            with self.subTest(changed=changed), self.assertRaises(AssertionError):
                check_registry_item({**projected, **changed}, entry)
        for flag, number in ((True, 1), (False, 0)):
            published = {**entry, "obs:recommended": flag}
            with self.subTest(flag=flag, document="manifest"), self.assertRaisesRegex(AssertionError, "recommended"):
                check_publication({**published, "obs:recommended": number}, SKILL, "1.1.0")
            with self.subTest(flag=flag, document="registry"), self.assertRaisesRegex(AssertionError, "recommended"):
                check_registry_item({**projected, "obs:recommended": number}, published)

    def test_projected_items_may_omit_artifact_but_search_needs_score_and_registry(self):
        validators = projection_validators()
        identity = {"identifier": "urn:air:registry.example.test:mcp:example"}
        validators["list"].validate({"items": [identity]})
        result = {**identity, "score": 0, "source": "https://registry.example.test/ard"}
        validators["search"].validate({"results": [result]})
        for missing in result:
            with self.subTest(missing=missing), self.assertRaises(ValidationError):
                validators["search"].validate({"results": [{k: v for k, v in result.items() if k != missing}]})
        for invalid in ({"score": -1}, {"score": 101}, {"score": True}, {"source": 42}):
            with self.subTest(invalid=invalid), self.assertRaises(ValidationError):
                validators["search"].validate({"results": [{**result, **invalid}]})

    def test_projected_optional_fields_are_validated_when_present(self):
        validators = projection_validators()
        for owner, key in (("search", "results"), ("list", "items")):
            item = {"identifier": "urn:air:test.example:mcp:one", "version": ["1.0.0"]}
            if owner == "search":
                item.update(score=100, source="https://test.example/ard")
            with self.subTest(owner=owner), self.assertRaises(ValidationError):
                validators[owner].validate({key: [item]})

    def test_native_source_is_the_selected_registry_uri(self):
        row = {"version": "1.0.0", "score": 100, "source": ORIGIN + "/api/v1/ard"}
        matched_result(row, "1.0.0")
        for other in ("/api/v1/ard", "https://other.example/api/v1/ard", ORIGIN + "/api/v1/ard/search"):
            with self.subTest(source=other), self.assertRaisesRegex(AssertionError, "Registry URI"):
                matched_result({**row, "source": other}, "1.0.0")

    def test_duplicate_native_identity_cannot_hide_a_second_row_across_pages(self):
        item = {"identifier": "urn:air:test.example:skill:one", "version": "1.0.0"}
        self.assertEqual(inventory([item]), {item["identifier"]: item})
        with self.assertRaisesRegex(AssertionError, "duplicate"):
            inventory([item, {**item, "version": "1.1.0"}])
