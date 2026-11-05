"""Private backup expectations with inert API/SQL ports, without processes or databases."""

import copy
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch

import backup_ratings as ratings
from runner import Failure


def fixture():
    target = {"origin": "local", "resourceId": "resource", "versionRevisionId": "revision",
              "identifier": "urn:air:catalog.example.com:test:agent", "kind": "agent",
              "versionPresent": True, "version": "1.0.0"}
    admission = {"target": target, "releaseId": "release"}
    seed = {"admission": admission, "entryDigest": "sha256:" + "d" * 64, "opinions": {},
            "sharing": {"id": "sharing", "etag": '"rev-1"'}}
    sharing = {"id": "sharing", "tenantId": ratings.TENANT, "host": "127.0.0.1", "audience": "anonymous",
               "enabled": True, "revision": 1, "updatedAt": "2026-11-01T00:30:01Z"}
    opinions, views = {}, {}
    target_row = {"tenant_id": ratings.TENANT, "target_key": "sha256:" + "e" * 64, "target": target,
                  "resource_id": "resource", "version_revision_id": "revision", "source_id": None,
                  "vote_count": 2, "score_sum": 7, "shared_count": 1, "shared_sum": 5}
    tables = {"rating_targets": [target_row], "item_ratings": [], "item_rating_revisions": [],
              "rating_sharing": [{"id": "sharing", "tenant_id": ratings.TENANT, "host": "127.0.0.1",
                                  "audience": "anonymous", "enabled": True, "revision": 1,
                                  "updated_at": "2026-11-01T00:30:01+00:00"}]}
    histories = {"consented": [(4, False, "active"), (5, True, "active")],
                 "private": [(2, False, "active")], "withdrawn": [(1, False, "active"), (0, False, "withdrawn")],
                 "moderated": [(3, True, "active"), (0, False, "removed")]}
    for index, (label, history) in enumerate(histories.items()):
        identifier, revision = "opinion-" + label, len(history)
        score, consent, state = history[-1]
        updated = "2026-11-01T00:30:0" + str(revision - 1)
        opinion = {"id": identifier, "tenantId": ratings.TENANT, "target": target, "releaseId": "release",
                   "entryDigest": seed["entryDigest"], "revision": revision, "score": score, "consent": consent,
                   "state": state, "createdAt": "2026-11-01T00:30:00Z", "updatedAt": updated + "Z"}
        etag = '"rev-' + str(revision) + '"'
        seed["opinions"][label] = {"id": identifier, "revision": revision, "etag": etag}
        opinions[label] = {"record": opinion, "etag": etag}
        views[label] = {"target": target, "count": 2, "average": 3.5, "own": opinion, "changedEntry": False}
        tables["item_ratings"].append({"tenant_id": ratings.TENANT, "id": identifier,
            "principal_key": "sha256:" + str(index) * 64, "submitted_by": "member-" + str(index),
            "target_key": target_row["target_key"], "release_id": "release", "entry_digest": seed["entryDigest"],
            "revision": revision, "score": score, "consent": consent, "state": state,
            "created_at": "2026-11-01T00:30:00+00:00", "updated_at": updated + "+00:00"})
        for number, (old_score, old_consent, old_state) in enumerate(history, 1):
            tables["item_rating_revisions"].append({"tenant_id": ratings.TENANT, "rating_id": identifier,
                "revision": number, "score": old_score, "consent": old_consent, "state": old_state,
                "entry_digest": seed["entryDigest"], "recorded_at": "2026-11-01T00:30:0" + str(number - 1) + "+00:00"})
    return seed, opinions, views, sharing, tables


class BackupRatingUnits(unittest.TestCase):
    def test_prepare_uses_real_commands_with_distinct_owners_and_original_validators(self):
        for changed in (None, "bytes", "etag"):
            seed, _, _, _, _ = fixture()
            journey = Mock(base="http://inert.invalid", retained=[{
                "resource": {"id": "resource", "kind": "agent", "canonicalIdentifier": seed["admission"]["target"]["identifier"]},
                "version": {"version": "1.0.0"}, "versionRevision": {"id": "revision", "contentSetDigest": seed["entryDigest"]},
                "release": {"id": "release"}}])
            journey.api.request.return_value = ({"revision": 0, "enabled": False}, {})
            clients = {token: Mock(name=label) for label, token in ratings.OWNERS.items()}
            counts = {}
            def mutate(actor, _method, path, _body, _status=200, **_kwargs):
                if path == "/api/v1/ratings/sharing":
                    return {"id": "sharing", "revision": 1}, {"ETag": '"rev-1"'}
                if path == "/api/v1/ratings":
                    label = next(label for label, token in ratings.OWNERS.items() if clients[token] is actor)
                else:
                    label = "withdrawn" if path.endswith(":withdraw") else "moderated"
                counts[label] = counts.get(label, 0) + 1
                return {"id": "opinion-" + label, "revision": counts[label]}, {"ETag": '"rev-' + str(counts[label]) + '"'}
            before = (200, {"ETag": '"publication"'}, b"immutable publication")
            after = (200, {"ETag": '"changed"' if changed == "etag" else '"publication"'},
                     b"changed publication" if changed == "bytes" else before[2])
            with (self.subTest(changed=changed), patch.object(ratings, "Client", side_effect=lambda _base, token: clients[token]),
                  patch.object(ratings, "mutate", side_effect=mutate) as commands,
                  patch.object(ratings.audit, "request", side_effect=[before, after])):
                if changed:
                    with self.assertRaisesRegex(Failure, "backup-rating-baseline-mismatch"):
                        ratings.prepare(journey)
                else:
                    self.assertEqual(ratings.prepare(journey), seed)
                self.assertEqual(commands.call_count, 8)
                edit = commands.call_args_list[1]
                self.assertIs(edit.args[0], clients["local-reader"])
                self.assertEqual(edit.args[3], {**seed["admission"], "score": 5, "consent": True})
                self.assertEqual(edit.kwargs, {"etag": '"rev-1"'})
                removed = next(call for call in commands.call_args_list if call.args[2].endswith(":remove"))
                self.assertIs(removed.args[0], journey.api)
                self.assertEqual(removed.args[3], {"reason": "Owned backup moderation"})
                self.assertEqual(removed.kwargs, {"etag": '"rev-1"'})
                self.assertEqual(commands.call_args_list[-1].args[3]["enabled"], True)
                journey.sql.assert_not_called()

    def test_capture_private_exact_readbacks_and_all_four_bounded_tables(self):
        seed, opinions, views, sharing, tables = fixture()
        with tempfile.TemporaryDirectory() as directory:
            journey = Mock(base="http://inert.invalid", state=Path(directory), evidence={})
            def sql(statement):
                self.assertTrue(statement.startswith("SELECT "))
                self.assertIn("LIMIT 11", statement)
                table = next(name for name in tables if "FROM " + name + " WHERE" in statement)
                return json.dumps(tables[table])
            journey.sql.side_effect = sql
            journey.api.request.return_value = (sharing, {"ETag": '"rev-1"'})
            clients = {}
            for label, token in ratings.OWNERS.items():
                client = Mock()
                client.request.side_effect = [(opinions[label]["record"], {"ETag": opinions[label]["etag"], "Cache-Control": "no-store"}),
                                              (views[label], {})]
                clients[token] = client
            with patch.object(ratings, "Client", side_effect=lambda _base, token: clients[token]):
                captured = ratings.capture(journey, seed)
            path = Path(directory) / "rating-expectations.json"
            self.assertEqual(json.loads(path.read_text()), captured)
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            self.assertEqual(captured["tables"], tables)
            self.assertEqual(captured["opinions"], opinions)
            self.assertEqual(captured["expectedLocal"], {"count": 2, "sum": 7, "average": 3.5})
            self.assertEqual(captured["expectedShared"], {"count": 1, "sum": 5, "summaryEligible": False})
            self.assertEqual(journey.sql.call_count, 4)
            self.assertEqual(journey.evidence, {})
            self.assertTrue(all(token not in path.read_text() for token in ratings.OWNERS.values()))

    def test_retained_rows_require_real_history_consent_identity_and_rollups(self):
        seed, opinions, _, sharing, original = fixture()
        ratings.validate_retained(seed, opinions, sharing, original)
        mutations = [("rating_targets", 0, field, value) for field, value in (
            ("vote_count", 0), ("score_sum", 8), ("shared_count", 2), ("shared_sum", 7),
            ("resource_id", "wrong"), ("version_revision_id", "wrong"))]
        mutations += [("item_ratings", 0, field, value) for field, value in (
            ("consent", False), ("score", 4), ("revision", 1), ("target_key", "wrong"),
            ("release_id", "wrong"), ("entry_digest", "wrong"), ("updated_at", "2026-11-01T00:30:02+00:00"))]
        mutations += [("item_rating_revisions", 0, field, value) for field, value in (
            ("score", 3), ("consent", True), ("state", "withdrawn"), ("revision", 2), ("entry_digest", "wrong"))]
        mutations += [("rating_sharing", 0, field, value) for field, value in (
            ("enabled", False), ("host", "wrong"), ("audience", "authenticated"), ("revision", 2))]
        for table, index, field, value in mutations:
            rows = copy.deepcopy(original)
            rows[table][index][field] = value
            with self.subTest(table=table, field=field), self.assertRaisesRegex(Failure, "^backup-rating-baseline-mismatch$"):
                ratings.validate_retained(seed, opinions, sharing, rows)
        for table in ("item_ratings", "item_rating_revisions"):
            rows = copy.deepcopy(original)
            rows[table].pop()
            with self.subTest(missing=table), self.assertRaisesRegex(Failure, "backup-rating-baseline-mismatch"):
                ratings.validate_retained(seed, opinions, sharing, rows)

    def test_bad_api_readback_stops_before_sql_or_private_capture(self):
        seed, opinions, _, _, _ = fixture()
        for field, value in (("score", 4), ("consent", False), ("revision", 1), ("releaseId", "wrong"), ("entryDigest", "wrong")):
            opinion = {**opinions["consented"]["record"], field: value}
            with tempfile.TemporaryDirectory() as directory:
                journey = Mock(base="http://inert.invalid", state=Path(directory))
                client = Mock()
                client.request.return_value = (opinion, {"ETag": '"rev-2"', "Cache-Control": "no-store"})
                with (self.subTest(field=field), patch.object(ratings, "Client", return_value=client),
                      self.assertRaisesRegex(Failure, "^backup-rating-baseline-mismatch$")):
                    ratings.capture(journey, seed)
                journey.sql.assert_not_called()
                self.assertEqual(list(Path(directory).iterdir()), [])

    def test_empty_or_overfull_sql_tables_never_create_private_expectations(self):
        seed, opinions, views, sharing, tables = fixture()
        for selected in tables:
            for bad_rows in ([], [tables[selected][0]] * 9):
                with tempfile.TemporaryDirectory() as directory:
                    journey = Mock(base="http://inert.invalid", state=Path(directory))
                    journey.api.request.return_value = (sharing, {"ETag": '"rev-1"'})
                    def sql(statement):
                        table = next(name for name in tables if "FROM " + name + " WHERE" in statement)
                        return json.dumps(bad_rows if table == selected else tables[table])
                    journey.sql.side_effect = sql
                    clients = {}
                    for label, token in ratings.OWNERS.items():
                        client = Mock()
                        client.request.side_effect = [(opinions[label]["record"], {
                            "ETag": opinions[label]["etag"], "Cache-Control": "no-store"}), (views[label], {})]
                        clients[token] = client
                    with (self.subTest(table=selected, rows=len(bad_rows)),
                          patch.object(ratings, "Client", side_effect=lambda _base, token: clients[token]),
                          self.assertRaisesRegex(Failure, "^backup-rating-baseline-mismatch$")):
                        ratings.capture(journey, seed)
                    self.assertEqual(list(Path(directory).iterdir()), [])


class RestoredRatingUnits(unittest.TestCase):
    def snapshot(self):
        seed, opinions, views, sharing, tables = fixture()
        return seed, {"admission": seed["admission"], "opinions": opinions, "views": views,
                      "sharing": {"record": sharing, "etag": '"rev-1"'}, "tables": tables,
                      "expectedLocal": {"count": 2, "sum": 7, "average": 3.5},
                      "expectedShared": {"count": 1, "sum": 5, "summaryEligible": False}}

    def test_owner_rejects_missing_changed_or_insecure_capture_and_changed_restored_rows(self):
        for fault in (None, "missing", "permissions", "target", "revision", "consent", "aggregate", "history", "disk", "seed", "restored"):
            seed, expected = self.snapshot()
            bound = ratings.digest(expected)
            with tempfile.TemporaryDirectory() as directory, self.subTest(fault=fault):
                journey = Mock(state=Path(directory))
                path = journey.state / "rating-expectations.json"
                ratings.write_document(path, expected)
                if fault == "missing":
                    path.unlink()
                elif fault == "permissions":
                    path.chmod(0o644)
                elif fault == "target":
                    expected["admission"]["target"]["resourceId"] = "other"
                elif fault == "revision":
                    expected["opinions"]["private"]["record"]["revision"] = 9
                elif fault == "consent":
                    expected["opinions"]["private"]["record"]["consent"] = True
                elif fault == "aggregate":
                    expected["expectedLocal"]["sum"] = 8
                elif fault == "history":
                    expected["tables"]["item_rating_revisions"].pop()
                elif fault == "disk":
                    ratings.write_document(path, {})
                if fault == "seed":
                    seed = {}
                restored = copy.deepcopy(expected["tables"])
                if fault == "restored":
                    restored["item_ratings"][0]["consent"] = False
                with patch.object(ratings, "read_tables", return_value=restored):
                    if fault:
                        with self.assertRaises(Failure):
                            ratings.verify_owner(journey, seed, expected, bound)
                    else:
                        self.assertIs(ratings.verify_owner(journey, seed, expected, bound), True)
                journey.compose.assert_not_called()
                journey.api.request.assert_not_called()

    def test_restored_api_checks_moderator_and_both_privacy_boundaries_without_writes(self):
        seed, expected = self.snapshot()
        later = {"opinions": {}, "sharing": {"id": "sharing", "revision": 2, "enabled": False}}
        for label in ("consented", "private", "withdrawn"):
            old = expected["opinions"][label]["record"]
            later["opinions"][label] = dict(old, revision=old["revision"] + 1)
        for fault in (None, "snapshot", "moderator", "etag", "later", "sharing"):
            journey = Mock(base="http://inert.invalid")
            current = copy.deepcopy(expected)
            proof = copy.deepcopy(later)
            if fault == "snapshot":
                current["views"]["private"]["count"] = 1
            if fault == "later":
                proof["opinions"].pop("private")
            if fault == "sharing":
                proof["sharing"]["enabled"] = True
            answers = []
            for value in expected["opinions"].values():
                record = dict(value["record"], consent=False) if fault == "moderator" else value["record"]
                answers.append((record, {"ETag": '"wrong"' if fault == "etag" else value["etag"]}))
            journey.api.request.side_effect = answers
            stranger = Mock()
            with (self.subTest(fault=fault), patch.object(ratings, "verify_owner", return_value=True),
                  patch.object(ratings, "read_snapshot", return_value=current),
                  patch.object(ratings, "Client", return_value=stranger), patch.object(ratings, "mutate") as writes):
                if fault:
                    with self.assertRaises(Failure):
                        ratings.verify_api(journey, seed, expected, ratings.digest(expected), proof)
                else:
                    ratings.verify_api(journey, seed, expected, ratings.digest(expected), proof)
                    self.assertEqual(journey.other.request.call_count, 4)
                    self.assertEqual(stranger.request.call_count, 4)
                    for call in stranger.request.call_args_list + journey.other.request.call_args_list:
                        self.assertEqual(call.args[0], "GET")
                        self.assertEqual(call.kwargs, {"expected": (404,)})
                writes.assert_not_called()
                journey.compose.assert_not_called()

    def test_runtime_cannot_start_without_owner_verification(self):
        import backup_restore
        for verified in (None, False, 1):
            journey = object.__new__(backup_restore.BackupRestore)
            journey.rating_owner_verified = verified
            journey.phase, journey.compose = Mock(), Mock()
            with self.subTest(verified=verified), self.assertRaisesRegex(Failure, "backup-ratings-not-verified"):
                journey.runtime()
            journey.compose.assert_not_called()

    def test_post_backup_changes_use_original_etags_and_leave_worker_stopped(self):
        seed, expected = self.snapshot()
        for changed_public in (False, True):
            journey = Mock(base="http://inert.invalid", public=(200, {"ETag": '"snapshot"'}, b"sealed"))
            clients, updated = {}, {}
            states = {"consented": (0, False, "withdrawn"), "private": (2, True, "active"),
                      "withdrawn": (0, False, "removed")}
            for label, (score, consent, state) in states.items():
                current = dict(expected["opinions"][label]["record"], score=score, consent=consent, state=state,
                               revision=seed["opinions"][label]["revision"] + 1)
                updated[label] = current
                client = Mock()
                client.request.return_value = (current, {"ETag": '"rev-' + str(current["revision"]) + '"'})
                clients[ratings.OWNERS[label]] = client
            def mutate(actor, method, path, body, **kwargs):
                self.assertEqual(method, "POST")
                if path == "/api/v1/ratings/sharing":
                    self.assertFalse(body["enabled"])
                    self.assertEqual(kwargs["etag"], '"rev-1"')
                    return {"id": "sharing", "revision": 2, "enabled": False}, {"ETag": '"rev-2"'}
                label = "private" if path == "/api/v1/ratings" else next(label for label in states if seed["opinions"][label]["id"] in path)
                self.assertEqual(kwargs["etag"], seed["opinions"][label]["etag"])
                self.assertIs(actor, journey.api if label == "withdrawn" else clients[ratings.OWNERS[label]])
                return {"id": updated[label]["id"], "revision": updated[label]["revision"]}, {
                    "ETag": '"rev-' + str(updated[label]["revision"]) + '"'}
            tables = {"rating_targets": [{"vote_count": 1, "score_sum": 2, "shared_count": 1, "shared_sum": 2}]}
            with (self.subTest(changed_public=changed_public), patch.object(ratings, "Client", side_effect=lambda _base, token: clients[token]),
                  patch.object(ratings, "mutate", side_effect=mutate) as commands,
                  patch.object(ratings, "read_tables", return_value=tables) as reads,
                  patch.object(ratings.audit, "request", return_value=(200, {"ETag": '"snapshot"'}, b"changed" if changed_public else b"sealed"))):
                if changed_public:
                    with self.assertRaises(Failure):
                        ratings.later_changes(journey, seed)
                else:
                    proof = ratings.later_changes(journey, seed)
                    self.assertEqual(proof["opinions"], updated)
                    self.assertEqual(journey.compose.call_args.args, ("stop", "--timeout", "20", "api"))
                    journey.stopped.assert_called_with("api", "worker", "peer-archives")
                self.assertEqual(commands.call_count, 4)
                reads.assert_called_once_with(journey, history_count=10)
                self.assertTrue(all(call.args[-1] == "api" for call in journey.compose.call_args_list))

    def test_rating_integrity_finding_rejects_exit_zero_report(self):
        import backup_restore_test_helpers as helpers
        warning = {"code": "RESTORE_RATING_INVALID", "class": "integrity", "severity": "error",
                   "domain": "rating", "recordId": "vote:retained", "disposition": "Keep traffic disabled"}
        with self.assertRaisesRegex(Failure, "backup-unexpected-finding"):
            helpers.chain(lambda _cursor: helpers.page(findings=[warning]))
