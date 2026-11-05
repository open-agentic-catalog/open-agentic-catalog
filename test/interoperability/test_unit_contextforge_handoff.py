"""Isolated governed handoff checks, without clients, processes or databases."""

import copy
import unittest

from contextforge import handoff_checks as h


def fixture():
    expected = dict(resource="resource", version="version", versionRevision="sealed-version", listing="listing", listingRevision="sealed-listing", release="release",
                    contentSetDigest="sha256:content-set", server="a" * 32, representation="representation", evidence="evidence")
    card = h.document(expected["server"])
    record = {"resource": {"id": expected["resource"], "kind": "mcp_server", "canonicalIdentifier": h.IDENTIFIER},
              "version": {"id": expected["version"], "version": h.VERSION, "resourceId": expected["resource"], "sealedRevisionId": expected["versionRevision"]},
              "versionRevision": {"id": expected["versionRevision"], "resourceVersionId": expected["version"], "contentSetDigest": expected["contentSetDigest"], "evidenceSetId": "evidence",
                                  "representations": [{"id": "representation", "role": "primary", "mediaType": h.CARD, "profileId": h.PROFILE, "validationState": "valid",
                                                       "data": card, "contentDigest": h.digest(card), "size": len(h.canonical(card))}]},
              "listing": {"id": expected["listing"], "currentReleaseId": expected["release"], "candidateRevisionId": "later-draft"},
              "listingRevision": {"id": expected["listingRevision"], "listingId": expected["listing"], "versionRevisionId": expected["versionRevision"]},
              "release": {"id": expected["release"], "state": "published", "listingId": expected["listing"], "versionRevisionId": expected["versionRevision"], "listingRevisionId": expected["listingRevision"]},
              "evidence": {"id": "evidence", "state": "complete", "subjectDigest": expected["contentSetDigest"], "findings": [{"code": "MCP_SERVER_CARD_EXPERIMENTAL"}]}}
    return record, expected


class Mapping(unittest.TestCase):
    def test_digest_oracle_uses_exact_ascii_bytes_in_sorted_key_order(self):
        self.assertEqual(h.canonical({"b": "2", "a": "1"}), b'{"a":"1","b":"2"}')
        self.assertEqual(h.digest({"b": "2", "a": "1"}), "sha256:21f76dfbfe6dfe21f762080ef484112cf2952974cef30741fd1931e1c6d92112")
        with self.assertRaises(AssertionError):
            h.canonical({"description": "outside the ASCII oracle: \u00e9"})

    def test_exact_published_release_maps_only_the_owned_url_and_separate_bearer(self):
        record, expected = fixture()
        config = h.map_client(record, expected, "separate-runtime-token")
        self.assertEqual(config, {"url": "https://contextforge:4444/servers/" + "a" * 32 + "/mcp", "headers": {"Authorization": "Bearer separate-runtime-token"}})
        self.assertNotIn("description", config)
        self.assertNotIn("_meta", config)

    def test_revision_digest_profile_and_endpoint_mutations_are_refused(self):
        record, expected = fixture()
        for section, field, value in (("release", "versionRevisionId", "draft"), ("listingRevision", "id", "draft"), ("version", "sealedRevisionId", "draft"),
                                      ("evidence", "id", "other-evidence"), ("evidence", "subjectDigest", "sha256:changed"), ("release", "state", "withdrawn")):
            broken = copy.deepcopy(record)
            broken[section][field] = value
            with self.subTest(section=section, field=field), self.assertRaises(AssertionError):
                h.map_client(broken, expected, "separate-runtime-token")
        for field, value in (("profileId", "another-profile"), ("contentDigest", "sha256:changed"), ("id", "another-representation"), ("size", 0)):
            broken = copy.deepcopy(record)
            broken["versionRevision"]["representations"][0][field] = value
            with self.subTest(field=field), self.assertRaises(AssertionError):
                h.map_client(broken, expected, "separate-runtime-token")
        for mutate in (lambda r: r.update(url="https://external.invalid/mcp"), lambda r: r.update(url=h.endpoint("a" * 32) + "?token=value"),
                       lambda r: r["headers"][0].update(value="embedded-credential"), lambda r: r["headers"][0].update(name="X-MCP-Test-Key"),
                       lambda r: r.update(supportedProtocolVersions=["2025-11-25"]), lambda r: r.update(variables={"host": {"default": "external.invalid"}})):
            broken = copy.deepcopy(record)
            representation = broken["versionRevision"]["representations"][0]
            mutate(representation["data"]["remotes"][0])
            representation.update(contentDigest=h.digest(representation["data"]), size=len(h.canonical(representation["data"])))
            with self.assertRaises(AssertionError):
                h.map_client(broken, expected, "separate-runtime-token")

    def test_server_and_credential_inputs_remain_literal_and_bounded(self):
        for server in ("", "A" * 32, "a" * 31, "a" * 32 + "/other", "a" * 32 + "?query"):
            with self.subTest(server=server), self.assertRaises(AssertionError):
                h.endpoint(server)
        record, expected = fixture()
        for token in ("", "short", "token with whitespace", "x" * 16385, "non-ascii-credential-\u00e9"):
            with self.subTest(token_length=len(token)), self.assertRaises(AssertionError):
                h.map_client(record, expected, token)

    def test_cli_selection_reads_the_selected_release_before_mapping(self):
        record, expected = fixture()
        page = {"data": {"partial": False, "items": [{"type": "local", "kind": "mcp_server", "versionPresent": True, "version": h.VERSION, "local": {"release": {"id": "release"}}}]}}
        called = []
        def read(release):
            called.append(release)
            return {"data": record}
        self.assertEqual(h.select_record(page, read, expected)[1], record)
        self.assertEqual(called, ["release"])
        for broken in ({"data": {"partial": True, "items": []}}, {"data": {"partial": False, "items": []}}):
            called.clear()
            with self.assertRaises(AssertionError):
                h.select_record(broken, read, expected)
            self.assertEqual(called, [])

    def test_runtime_summary_requires_resource_bytes_protocols_and_classified_prompt(self):
        value = {"echo": True, "resourceSha256": h.RESOURCE_SHA256, "credentialsSeparated": True, "protocol": "2026-07-28", "upstreamProtocols": ["2025-11-25"],
                 "serverInfo": {"name": "Independent native application", "version": "1.1.0"}, "nativeNames": {"echo": "imported-echo", "prompt": "imported-prompt", "resource": "demo://features"},
                 "prompt": {"status": "peer_limited", "cause": "private_ca_rejected"}, "promptFirst": True}
        h.validate_runtime(value)
        h.validate_runtime(dict(value, prompt={"status": "passed"}))
        for key, replacement in (("echo", False), ("resourceSha256", "0" * 64), ("credentialsSeparated", False), ("promptFirst", False), ("upstreamProtocols", ["2026-07-28"]),
                                 ("prompt", {"status": "peer_limited", "cause": "unclassified"}), ("prompt", {"status": "not_run"})):
            with self.subTest(key=key), self.assertRaises(AssertionError):
                h.validate_runtime(dict(value, **{key: replacement}))


if __name__ == "__main__":
    unittest.main()
