"""Pure governed A2A evidence/negative-result checks; no SDK, processes or sockets."""

import copy
import json
import unittest
from unittest.mock import Mock, patch

from a2a_catalog import Catalog
import a2a_contract as control
import a2a_handoff as handoff
from common import Result
from test_unit_a2a import valid_result, observations


def evidence():
    card = valid_result()["card"]
    card["version"] = "1.0.0"
    expected = {name: name + "-id" for name in ("resource", "version", "versionRevision", "listing", "listingRevision", "release", "snapshot")}
    expected["contentSetDigest"] = "sha256:" + "c" * 64
    record = {
        "resource": {"id": expected["resource"], "canonicalIdentifier": handoff.IDENTIFIER, "kind": "agent"},
        "version": {"id": expected["version"], "resourceId": expected["resource"], "version": "1.0.0", "sealedRevisionId": expected["versionRevision"]},
        "versionRevision": {"id": expected["versionRevision"], "resourceVersionId": expected["version"], "contentSetDigest": expected["contentSetDigest"],
                            "evidenceSetId": "evidence-id", "representations": [{"role": "primary", "mediaType": handoff.CARD, "profileId": handoff.PROFILE,
                            "validationState": "valid", "data": card, "contentDigest": handoff.digest(card), "size": len(handoff.canonical(card))}]},
        "listing": {"id": expected["listing"], "currentReleaseId": expected["release"]},
        "listingRevision": {"id": expected["listingRevision"], "listingId": expected["listing"], "versionRevisionId": expected["versionRevision"],
                            "displayName": "Hello World", "description": "Governed local fixture", "tags": [handoff.TAG], "publisherId": "publisher"},
        "release": {"id": expected["release"], "listingRevisionId": expected["listingRevision"], "listingId": expected["listing"], "versionRevisionId": expected["versionRevision"], "state": "published"},
        "evidence": {"id": "evidence-id", "subjectDigest": expected["contentSetDigest"], "state": "complete"},
    }
    return card, expected, record


def failure(case):
    cause = ({"type": "SSLCertVerificationError", "message": "CERTIFICATE_VERIFY_FAILED"} if case == "untrusted-ca"
             else {"type": "ConnectionRefusedError", "message": "[Errno 111] Connect call failed ('127.0.0.1', 9443)"})
    return {"error": "A2AClientError", "causes": [{"type": "ConnectError", "message": "All connection attempts failed"}, cause]}


def public_document(card, record):
    entry = dict(identifier=handoff.IDENTIFIER, metadata={"catalogReleaseId": record["release"]["id"]}, type=handoff.CARD,
                 version="1.0.0", data=card, **{key: record["listingRevision"][key] for key in ("displayName", "description", "tags")})
    return {"entries": [entry, {"identifier": "self", "type": "application/ai-registry+json", "metadata": {"catalogReleaseId": "registry:self"}}]}


def report():
    direct = valid_result()
    governed = copy.deepcopy(direct)
    message, message_id, context_id = control.interaction(True)
    task = governed["send"]
    task.update(id="governed-task", contextId=context_id)
    task["artifacts"][0]["parts"][0]["text"] = "Hello, World! I have received your request (" + message + ")"
    task["history"][0].update(messageId=message_id, contextId=context_id)
    task["history"][0]["parts"][0]["text"] = message
    governed["readback"] = copy.deepcopy(dict(task, history=task["history"][-1:]))
    traffic = observations()[1:]
    traffic[0]["params"]["message"] = copy.deepcopy(task["history"][0])
    traffic[1]["params"]["id"] = "governed-task"
    denied = {}
    for case, (_, status, code) in handoff.CATALOG_DENIALS.items():
        error = ("catalogctl: remote: INTERNAL: remote: supply a nonempty bearer token of at most 16 KiB through CATALOG_REMOTE_TOKEN or a token file" if case == "missing"
                 else json.dumps({"status": status, "code": code, "requestId": "request"}))
        denied[case] = dict(api_status=status, api_code=code, cli_exit=1, cli_error=error, outcome="denied", handoff_created=False, observations=observations())
    return dict(scope="a2a-governed-handoff", cleanup=True, cases=dict.fromkeys(control.CASES, True), native_exit=0,
                result=direct, direct_observations=observations(), governed_cases=dict.fromkeys(handoff.GOVERNED_CASES, True),
                selection=dict(versionRevision="published", draftRevision="draft", listingRevision="published-listing", draftListingRevision="draft-listing",
                               cardDigest=handoff.digest(direct["card"]), profiles={handoff.CARD: handoff.PROFILE}),
                metadata_observations=observations(), handoff_exit=0, handoff_result=governed, handoff_observations=observations() + traffic,
                catalog_denials=denied, failures={case: handoff.failure_result(case, Result(3, json.dumps(failure(case)), "")) for case in ("untrusted-ca", "unavailable")},
                observations=observations() + traffic, server_stopped=True, post_failure_integrity=True, binaries={name: "1" * 64 for name in ("catalogd", "catalogctl")})


class GovernedEvidence(unittest.TestCase):
    def test_canonical_card_digest_and_reviewed_scope(self):
        self.assertEqual(handoff.canonical({"z": {}, "a": ["hello"]}), b'{"a":["hello"],"z":{}}')
        self.assertEqual(handoff.digest({}), "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a")
        for text in ("café", "<&>", 1, 1.2):
            with self.assertRaises(AssertionError):
                handoff.canonical({"description": text})

    def test_exact_record_rejects_identity_profile_and_evidence_corruptions(self):
        card, expected, record = evidence()
        self.assertEqual(handoff.validate_record(record, expected, card), card)
        cases = [(key, "id", "other") for key in ("resource", "version", "versionRevision", "listingRevision", "release")]
        cases += [("resource", "kind", "mcp_server"), ("version", "sealedRevisionId", "draft"), ("version", "version", "1.0.1"),
                  ("versionRevision", "resourceVersionId", "other"), ("versionRevision", "contentSetDigest", "changed"),
                  ("versionRevision", "evidenceSetId", "other"), ("listingRevision", "versionRevisionId", "draft"),
                  ("release", "listingRevisionId", "draft-listing"), ("release", "state", "withdrawn"),
                  ("listing", "currentReleaseId", "other"), ("evidence", "state", "blocked"), ("evidence", "subjectDigest", "changed")]
        for section, field, value in cases:
            with self.subTest(section=section, field=field):
                changed = copy.deepcopy(record)
                changed[section][field] = value
                with self.assertRaises(AssertionError):
                    handoff.validate_record(changed, expected, card)
        for key, value in (("role", "distribution"), ("mediaType", "application/agent-card+json"), ("profileId", "old"), ("validationState", "invalid"),
                           ("data", dict(card, description="changed")), ("contentDigest", "bad"), ("size", 0), ("url", "https://external.invalid/")):
            with self.subTest(representation=key):
                changed = copy.deepcopy(record)
                changed["versionRevision"]["representations"][0][key] = value
                with self.assertRaises(AssertionError):
                    handoff.validate_record(changed, expected, card)

    def test_cli_search_drives_exact_release_read_not_resource_id(self):
        card, expected, record = evidence()
        item = dict(type="local", kind="agent", versionPresent=True, version="1.0.0", id="search-item", local={section: {"id": record[section]["id"]} for section in
                         ("resource", "version", "versionRevision", "listing", "listingRevision", "release", "evidence")},
                    **{key: record["listingRevision"][key] for key in ("displayName", "description", "tags")}, publisher="publisher")
        page = {"data": {"partial": False, "items": [item]}}
        item["local"]["versionRevision"].update(contentSetDigest=expected["contentSetDigest"], evidenceSetId=record["evidence"]["id"])
        read = Mock(return_value={"data": record})
        self.assertEqual(handoff.select_record(page, read, expected, card), record)
        read.assert_called_once_with(expected["release"])
        for section in item["local"]:
            changed = copy.deepcopy(item)
            changed["local"][section]["id"] = "wrong-summary"
            with self.subTest(summary=section), self.assertRaises(AssertionError):
                handoff.select_record({"data": {"partial": False, "items": [changed]}}, read, expected, card)
        changed = copy.deepcopy(item)
        changed["description"] = "different display"
        with self.assertRaises(AssertionError):
            handoff.select_record({"data": {"partial": False, "items": [changed]}}, read, expected, card)
        for field, value in (("partial", True), ("nextCursor", "more"), ("items", []), ("items", [item, item])):
            with self.subTest(field=field, value=value):
                read.reset_mock()
                with self.assertRaises(AssertionError):
                    handoff.select_record({"data": dict(page["data"], **{field: value})}, read, expected, card)
                read.assert_not_called()
        changed = copy.deepcopy(record)
        changed["versionRevision"]["representations"][0]["data"]["version"] = "1.0.1"
        with self.assertRaises(AssertionError):
            handoff.select_record(page, lambda _id: {"data": changed}, expected, card)

    def test_public_projection_remains_bound_to_selected_release(self):
        card, _, record = evidence()
        manifest = public_document(card, record)
        handoff.projection(manifest, record, card)
        for field, value in (("metadata", {"catalogReleaseId": "draft"}), ("data", dict(card, version="1.0.1")),
                             ("type", "application/agent-card+json"), ("description", "draft"), ("url", "https://external.invalid/")):
            with self.subTest(field=field):
                changed = copy.deepcopy(manifest)
                changed["entries"][0][field] = value
                with self.assertRaises(AssertionError):
                    handoff.projection(changed, record, card)

    def test_draft_cannot_change_retained_validation_evidence(self):
        card, expected, record = evidence()
        record["evidence"]["findings"] = []
        manifest = public_document(card, record)
        for changed in (False, True):
            with self.subTest(changed=changed):
                selected = copy.deepcopy(record)
                selected["listing"]["candidateRevisionId"] = "draft-listing"
                if changed:
                    # Keep every reference and the card unchanged: only the
                    # retained validation evidence is altered by the later draft.
                    selected["evidence"]["findings"] = [{"code": "UNEXPECTED", "severity": "warning"}]
                handoff.validate_record(selected, expected, card)
                api = Catalog(None, "inert-catalog", card)
                with patch.object(api, "publish", return_value=(expected, {})), \
                     patch.object(api, "cli", return_value={"data": {"resourceKinds": ["agent"], "operations": ["catalog.search"]}}), \
                     patch.object(api, "discover", side_effect=[record, selected]), \
                     patch.object(api, "draft", return_value=("draft-version", "draft-listing")), \
                     patch.object(api, "request", side_effect=[(manifest, '"snapshot"'), (manifest, '"snapshot"'), ({"state": "active"}, "")]):
                    if changed:
                        with self.assertRaisesRegex(AssertionError, "draft changed immutable release evidence"):
                            api.qualify()
                    else:
                        self.assertEqual(api.qualify()[0], selected)

    def test_failures_require_native_transport_cause_and_owned_endpoint(self):
        for case in ("untrusted-ca", "unavailable"):
            native = failure(case)
            handoff.failure_result(case, Result(3, json.dumps(native), ""))
            for result in (Result(0, json.dumps(native), ""), Result(2, "", "timeout"), Result(3, json.dumps(native), "warning"),
                           Result(3, json.dumps(dict(native, error="ValueError")), ""), Result(3, json.dumps(dict(native, causes=[])), ""),
                           Result(3, json.dumps(failure("unavailable" if case == "untrusted-ca" else "untrusted-ca")), "")):
                with self.subTest(case=case, result=result), self.assertRaises(AssertionError):
                    handoff.failure_result(case, result)
        changed = failure("unavailable")
        changed["causes"][-1]["message"] = "Connect call failed ('external.invalid', 443)"
        with self.assertRaises(AssertionError):
            handoff.failure_result("unavailable", Result(3, json.dumps(changed), ""))

    def test_partial_or_inconsistent_handoff_reports_cannot_pass(self):
        handoff.complete(report())
        corruptions = [
            lambda r: r.update(cleanup=False), lambda r: r.update(handoff_exit=3), lambda r: r.update(governed_cases={}),
            lambda r: r["governed_cases"].update(governance="yes"), lambda r: r["selection"].update(cardDigest="wrong"),
            lambda r: r["selection"].update(draftRevision="published"), lambda r: r["selection"].update(profiles={}),
            lambda r: r["metadata_observations"].append(copy.deepcopy(r["direct_observations"][0])),
            lambda r: r["handoff_observations"].append(copy.deepcopy(r["direct_observations"][0])),
            lambda r: r["handoff_result"]["card"].update(description="substituted"),
            lambda r: r["catalog_denials"].pop("wrong-tenant"), lambda r: r["catalog_denials"]["missing"].update(handoff_created=True),
            lambda r: r["catalog_denials"]["invalid"].update(cli_error="unrelated error"),
            lambda r: r["failures"].pop("unavailable"), lambda r: r.update(server_stopped=False),
            lambda r: r.update(post_failure_integrity=False), lambda r: r.update(binaries={}),
        ]
        for index, corrupt in enumerate(corruptions):
            with self.subTest(index=index):
                changed = report()
                corrupt(changed)
                with self.assertRaises((AssertionError, ValueError)):
                    handoff.complete(changed)

    def test_prior_direct_result_cannot_satisfy_governed_exchange(self):
        with self.assertRaises(AssertionError):
            control.check_result(valid_result(), governed=True)
        changed = report()
        control.check_result(changed["handoff_result"], governed=True)
        for section in ("send", "readback"):
            changed["handoff_result"][section]["artifacts"][0]["parts"][0]["text"] = "Hello, World!"
        with self.assertRaises(AssertionError):
            control.check_result(changed["handoff_result"], governed=True)
