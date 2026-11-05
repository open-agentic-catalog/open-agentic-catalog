import copy
from pathlib import Path
import tempfile
import unittest
from unittest import mock
import backup_restore as backup
from http_client import ClientFailure
import runner
from backup_restore_test_helpers import chain, evidence, page


class BackupTests(unittest.TestCase):
    def test_restore_archive_preparation_needs_no_peer_catalog_config(self):
        with tempfile.TemporaryDirectory() as directory:
            environment = {"CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY": directory,
                           "CATALOG_FUNCTIONAL_SUBNET": "172.31.1.240/28"}
            with mock.patch("peers.command") as command:
                origin = backup.prepare_plugin_archives(environment, (("restore-plugin", "1.0.0"),), include_mcp=True)
            self.assertEqual(origin, "https://172.31.1.252:8443")
            root = Path(directory)
            self.assertFalse((root / "peer-a.json").exists())
            catalog = runner.read_document(root / "archives/catalog.json")
            self.assertEqual(catalog["profiles"]["peers"], ["restore-plugin"])
            package = root / "archives/restore-plugin"
            self.assertEqual(runner.read_document(package / "plugin.json")["name"], "restore-plugin")
            self.assertEqual(runner.read_document(package / "mcp.json")["mcpServers"],
                             {"tools": {"type": "stdio", "command": "node"}})
            self.assertTrue((package / "skills/review-repository/SKILL.md").is_file())
            self.assertEqual((root / "archives").stat().st_mode & 0o777, 0o700)
            command.assert_called_once()
            arguments = command.call_args.args[0]
            self.assertEqual(arguments[0], str(root / "demo-archives"))
            self.assertIn("--prepare", arguments)
            self.assertEqual(arguments[-2:], ["--archive-origin", origin])

    def test_restore_requires_populated_exact_inputs_and_both_owner_denials(self):
        plugin_reads = [("/api/v1/" + name + "/id", {"id": name, "retained": "document"}, '"rev-1"')
                        for name in ("resources", "resource-versions", "version-revisions")]
        personal = [("/api/v1/personal/items/" + kind, {"id": kind, "type": kind, "label": "Private " + kind,
                     "revision": 1, "createdAt": "2026-10-27T10:00:00Z", "updatedAt": "2026-10-27T10:00:00Z",
                     **({"search": {"text": "order", "origins": ["local"]}} if kind == "search" else {
                         "reference": {"origin": "local", "releaseId": "release", "versionRevisionId": "revision"}})}, '"rev-1"')
                    for kind in ("search", "resource")]
        for index in range(5):
            faults = (None, "value", "etag") + (("cache",) if index >= 3 else ())
            for fault in faults:
                instance = object.__new__(backup.BackupRestore)
                instance.base = "http://inert.invalid"
                instance.plugin_reads, instance.personal_items = plugin_reads, personal
                instance.api, instance.other = mock.Mock(), mock.Mock()
                answers = []
                for number, (_, expected, etag) in enumerate(plugin_reads + personal):
                    current = copy.deepcopy(expected)
                    headers = {"ETag": etag, "Cache-Control": "no-store"}
                    if number == index:
                        if fault == "value":
                            if "search" in current:
                                current["search"]["text"] = "changed"
                            elif "reference" in current:
                                current["reference"]["versionRevisionId"] = "changed"
                            else:
                                current["retained"] = "changed"
                        elif fault == "etag":
                            headers["ETag"] = '"rev-2"'
                        elif fault == "cache":
                            headers["Cache-Control"] = "public"
                    answers.append((current, headers))
                instance.api.request.side_effect = answers
                with self.subTest(index=index, fault=fault), mock.patch.object(backup, "Client") as client:
                    if fault:
                        with self.assertRaises(AssertionError):
                            instance.verify_retained_inputs()
                        self.assertEqual(instance.api.request.call_count, index + 1)
                    else:
                        instance.verify_retained_inputs()
                        client.assert_called_once_with(instance.base, "local-reader")
                        self.assertEqual(instance.other.request.call_args_list,
                                         [mock.call("GET", path, expected=(404,)) for path, _, _ in plugin_reads + personal])
                        self.assertEqual(client.return_value.request.call_args_list,
                                         [mock.call("GET", path, expected=(404,)) for path, _, _ in personal])
        for plugins, items in (([], personal), (plugin_reads, []), (plugin_reads, personal[:1]),
                               (plugin_reads, [personal[0], personal[0]])):
            instance.plugin_reads, instance.personal_items = plugins, items
            instance.api.reset_mock()
            with self.assertRaises(AssertionError):
                instance.verify_retained_inputs()
            instance.api.request.assert_not_called()

    def test_inventory_bounds_precede_hashing(self):
        for names in ("", "invalid-name", "valid\nquoted\"name", "\n".join(["valid"] * 151)):
            instance = object.__new__(backup.BackupRestore)
            instance.sql = mock.Mock(side_effect=[backup.json.dumps(backup.TENANTS), names])
            with self.subTest(names=names), self.assertRaisesRegex(runner.Failure, "table-inventory-limit"):
                instance.inventory()
            self.assertEqual(instance.sql.call_count, 2)
        for count, size in ((10001, 1), (1, backup.MAX_DUMP + 1), (10000, backup.MAX_DUMP), (0, 0)):
            instance.sql = mock.Mock(side_effect=[backup.json.dumps(backup.TENANTS), "resources",
                                                  backup.json.dumps([count, size]), "a" * 64])
            if count > 10000 or size > backup.MAX_DUMP:
                with self.subTest(count=count, size=size), self.assertRaisesRegex(runner.Failure, "fixture-inventory-limit"):
                    instance.inventory()
                self.assertEqual(instance.sql.call_count, 3)
            else:
                self.assertEqual(instance.inventory()["tables"], {"resources": {"rows": count, "digest": "a" * 64}})
            self.assertIn("LIMIT 10001", instance.sql.call_args_list[2].args[0])
        instance.sql = mock.Mock(side_effect=[backup.json.dumps(backup.TENANTS), "resources", "[1,1]", "not-a-digest"])
        with self.assertRaisesRegex(runner.Failure, "invalid-table-digest"):
            instance.inventory()
    def test_dump_size_rejection_precedes_copy(self):
        for size in (0, -1, backup.MAX_DUMP + 1):
            instance = object.__new__(backup.BackupRestore)
            instance.phase = mock.Mock()
            instance.compose = mock.Mock(side_effect=[b"/tmp/catalog-archive.0123456789\n", b"", str(size).encode(), b""])
            with self.subTest(size=size), self.assertRaisesRegex(runner.Failure, "backup-dump-size"):
                instance.dump()
            self.assertFalse(any(call.args[0] in ("cp", "stop") for call in instance.compose.call_args_list))
            self.assertEqual(instance.compose.call_args.args, ("exec", "-T", "postgres", "rm", "-r", "--", "/tmp/catalog-archive.0123456789"))
    def test_existing_destination_is_never_overwritten(self):
        instance = object.__new__(backup.BackupRestore)
        instance.phase = mock.Mock()
        instance.compose = mock.Mock(return_value=b"/tmp/catalog-archive.0123456789\n")
        instance.sql = mock.Mock(return_value="1")
        instance.ctl = mock.Mock()
        with self.assertRaises(AssertionError):
            instance.restore()
        self.assertEqual(instance.compose.call_count, 1)  # Start the owned server only.
        instance.sql.assert_called_once_with("SELECT count(*) FROM pg_database WHERE datname='catalog'", database="postgres")
        instance.ctl.assert_not_called()
    def test_migration_plan_drift_never_reconciles_grants(self):
        instance = object.__new__(backup.BackupRestore)
        instance.phase = mock.Mock()
        instance.compose = mock.Mock(return_value=b"/tmp/catalog-archive.0123456789\n")
        instance.sql = mock.Mock(return_value="0")
        instance.archive = Path("/owned/catalog-backup.dump")
        instance.plan = [{"version": 27, "applied": True}]
        for plan in ([], [{"version": 27, "applied": False}], [{"version": 28, "applied": True}]):
            instance.ctl = mock.Mock(return_value=plan)
            with self.subTest(plan=plan), self.assertRaises(AssertionError):
                instance.restore()
            instance.ctl.assert_called_once_with("migrate", "plan", "--config", "/config/config.json")
    def test_inventory_drift_stops_verification_without_evidence(self):
        for after_scan in (False, True):
            instance = object.__new__(backup.BackupRestore)
            instance.phase = mock.Mock()
            instance.stopped = mock.Mock()
            instance.before_inventory = {"fixture": "original"}
            changed = {"fixture": "changed"}
            instance.inventory = mock.Mock(side_effect=[instance.before_inventory, changed] if after_scan else [changed])
            instance.plan = [{"version": 27}]
            instance.point = "fixture-point"
            instance.manifest_version = 12
            instance.manifests = []
            instance.ctl = mock.Mock()
            instance.receiver = mock.Mock()
            instance.evidence = {}
            with mock.patch.object(backup, "verify_chain", return_value=(2, {"RESTORE_ACTIONABLE_JOB_RETAINED"})) as verify, \
                    self.subTest(after_scan=after_scan), self.assertRaises((runner.Failure, AssertionError)):
                instance.verify()
            self.assertEqual(verify.call_count, 2 if after_scan else 0)
            instance.ctl.assert_not_called()
            instance.receiver.request.assert_not_called()
            self.assertEqual(instance.evidence, {})
    def test_recovered_delivery_waits_for_only_the_retained_event(self):
        instance = object.__new__(backup.BackupRestore)
        instance.endpoint = {"endpoint": {"id": "fixture-endpoint"}}
        instance.pending_event = "retained"
        succeeded = {"eventId": "retained", "state": "succeeded"}
        pages = [[], [{"eventId": "unrelated", "state": "succeeded"}],
                 [{"eventId": "retained", "state": "pending"}], [{"eventId": "retained", "state": "waiting"}], [succeeded]]
        instance.api = mock.Mock()
        instance.api.request.side_effect = [({"items": items}, {}) for items in pages]
        with mock.patch("federation.time.sleep"), mock.patch("federation.time.monotonic", return_value=0):
            self.assertEqual(instance.verified_delivery(instance.pending_event), succeeded)
        self.assertEqual(instance.api.request.call_args_list,
                         [mock.call("GET", "/api/v1/webhooks/fixture-endpoint/deliveries?limit=100")] * len(pages))
    def test_verification_traverses_both_tenants_and_requires_quiet_dependencies(self):
        for changed in (None, "source", "receiver"):
            instance = object.__new__(backup.BackupRestore)
            instance.rating_owner_verified = True
            instance.rating_seed, instance.rating_expectations = {}, {}
            instance.rating_capture_digest, instance.later_rating_state = "digest", {}
            instance.phase = mock.Mock()
            instance.stopped = mock.Mock()
            instance.before_inventory = {"fixture": "retained"}
            instance.inventory = mock.Mock(return_value=instance.before_inventory)
            instance.point = "fixture-point"
            instance.plan = [{"version": 27}]
            instance.manifest_version = 12
            instance.audit_rows = [5, 3]
            instance.manifests = [{"manifest": {"eventsDigest": value}} for value in ("first", "second")]
            instance.fixture_state = {"requests": 1}
            instance.receiver_state = {"accepted": 1}
            instance.receiver = mock.Mock()
            instance.receiver.request.return_value = ({"requests": 2 if changed == "source" else 1}, {})
            instance.stats = mock.Mock(return_value={"accepted": 2 if changed == "receiver" else 1})
            instance.evidence = {}
            def operator(*args):
                if args[:2] == ("restore", "verify"):
                    tenant = args[args.index("--tenant") + 1]
                    if "--cursor" not in args:
                        return page(tenantId=tenant, continuation="cursor-" + tenant, findings=[{
                            "code": "RESTORE_ACTIONABLE_JOB_RETAINED", "class": "operational",
                            "severity": "warning", "domain": "job", "disposition": "Review retained work"}])
                    self.assertEqual(args[args.index("--cursor") + 1], "cursor-" + tenant)
                    return page(tenantId=tenant, traversal="complete", continuation="")
                self.assertEqual(args[:2], ("audit", "compare-restore"))
                index = 0 if "/backup/manifest-0.json" in args else 1
                return {"valid": True, "rowCount": instance.audit_rows[index],
                        "eventsDigest": instance.manifests[index]["manifest"]["eventsDigest"]}
            instance.ctl = mock.Mock(side_effect=operator)
            with self.subTest(changed=changed), mock.patch.object(backup.backup_ratings, "verify_owner", return_value=True):
                if changed:
                    with self.assertRaises(AssertionError):
                        instance.verify()
                    self.assertEqual(instance.evidence, {})
                else:
                    instance.verify()
                    self.assertEqual(instance.evidence, {"tenantCount": 2, "verificationPages": [2, 2],
                                                        "auditRows": [5, 3], "retainedState": True})
            tenant_calls = [call.args[call.args.index("--tenant") + 1] for call in instance.ctl.call_args_list if "--tenant" in call.args]
            self.assertEqual(tenant_calls, [backup.TENANT] * 2 + [backup.OTHER] * 2)
            self.assertEqual(instance.ctl.call_count, 6)
            self.assertEqual(instance.inventory.call_count, 2)
            instance.stopped.assert_called_once_with("api", "worker", "postgres")
    def test_recovered_delivery_fails_on_terminal_or_observation_error(self):
        for error in (None, ClientFailure("private-response"), AssertionError("private-response")):
            instance = object.__new__(backup.BackupRestore)
            instance.endpoint = {"endpoint": {"id": "fixture-endpoint"}}
            instance.pending_event = "retained"
            instance.api = mock.Mock()
            instance.api.request.side_effect = error
            instance.api.request.return_value = ({"items": [{"eventId": "retained", "state": "dead_letter"}]}, {})
            with mock.patch("federation.time.sleep") as sleep, self.subTest(error=type(error)):
                try:
                    instance.verified_delivery(instance.pending_event)
                except (runner.Failure, ClientFailure, AssertionError) as caught:
                    if error:
                        self.assertIs(caught, error)
                    else:
                        self.assertEqual(str(caught), "backup-delivery-dead-letter")
                    location = runner.scenario_failure_location("backup_restore", caught)
                else:
                    self.fail("failed delivery observation passed")
            sleep.assert_not_called()
            instance.api.request.assert_called_once()
            self.assertEqual(location["scenario"], "backup_restore")
            self.assertNotIn("private", backup.json.dumps(location))
    def test_recovered_delivery_timeout_retains_only_safe_location(self):
        instance = object.__new__(backup.BackupRestore)
        instance.endpoint = {"endpoint": {"id": "private-endpoint"}}
        instance.pending_event = "private-event"
        instance.api = mock.Mock()
        instance.api.request.return_value = ({"items": []}, {})
        with mock.patch("federation.time.sleep"), mock.patch("federation.time.monotonic", side_effect=[0, 0, 46, 46]):
            try:
                instance.verified_delivery(instance.pending_event)
            except AssertionError as error:
                location = runner.scenario_failure_location("backup_restore", error)
            else:
                self.fail("undelivered event passed")
        self.assertEqual(set(location), {"scenario", "lines"})
        self.assertEqual(location["scenario"], "backup_restore")
        self.assertTrue(1 <= len(location["lines"]) <= 3)
        self.assertTrue(all(type(line) is int and line > 0 for line in location["lines"]))
        self.assertNotIn("private", backup.json.dumps(location))
        instance.api.request.assert_called_once()
    def test_search_requires_positive_local_and_indexed_results(self):
        results = [{"tier": "core", "partial": False, "items": [{"type": "local"}] * 3},
                   {"tier": "core", "partial": False, "items": [{"type": "indexed"}]}]
        backup.require_search_baseline(results)
        for index, change in ((0, {"items": []}), (0, {"items": [{"type": "indexed"}] * 3}),
                              (1, {"items": []}), (1, {"items": [{"type": "local"}]}),
                              (0, {"partial": True}), (1, {"partial": True}), (1, {"tier": "semantic"})):
            bad = copy.deepcopy(results)
            bad[index].update(change)
            with self.subTest(change=change), self.assertRaisesRegex(runner.Failure, "search-baseline"):
                backup.require_search_baseline(bad)
    def test_all_pages_and_exact_cursor_binding(self):
        warning = {"code": "RESTORE_ACTIONABLE_JOB_RETAINED", "class": "operational",
                   "severity": "warning", "domain": "job", "disposition": "Review retained work"}
        reader = mock.Mock(side_effect=[page(findings=[warning]), page(traversal="complete", continuation="")])
        self.assertEqual(chain(reader), (2, {warning["code"]}))
        self.assertEqual(reader.call_args_list, [mock.call(""), mock.call("next")])
    def test_current_check_set_is_required_on_every_page(self):
        for version in (6, 8):
            for position in (0, 1):
                pages = [page(), page(traversal="complete", continuation="")]
                pages[position]["checkSetVersion"] = version
                reader = mock.Mock(side_effect=pages)
                with self.subTest(version=version, position=position):
                    with self.assertRaisesRegex(runner.Failure, "backup-invalid-verification-page"):
                        chain(reader)
                    self.assertEqual(reader.call_args_list, [mock.call(""), mock.call("next")][:position + 1])
    def test_report_types_and_complete_warning_shape_are_required(self):
        warning = {"code": "RESTORE_ACTIONABLE_JOB_RETAINED", "class": "operational",
                   "severity": "warning", "domain": "job", "disposition": "Review retained work"}
        bad_pages = [page(**{field: value}) for field, value in (
            ("formatVersion", True), ("checkSetVersion", 1.0), ("checkSetVersion", 3), ("checkSetVersion", 4), ("schemaVersion", 27.0),
            ("privilegeManifestVersion", 12.0), ("assuranceBoundary", None), ("assuranceBoundary", ""))]
        bad_pages += [page(traversal="complete", continuation=value) for value in (None, False, 0, [])]
        for field in warning:
            incomplete = dict(warning)
            del incomplete[field]
            bad_pages.append(page(findings=[incomplete]))
        bad_pages += [page(findings=[dict(warning, **change)]) for change in (
            {"severity": "error"}, {"domain": "resource"}, {"disposition": None}, {"recordId": []})]
        for bad in bad_pages:
            with self.subTest(page=bad), self.assertRaises(runner.Failure):
                chain(mock.Mock(side_effect=[page(), bad]))
    def test_invalid_page_at_any_position_never_becomes_success(self):
        bad_pages = [page(integrity="invalid"), page(traversal="blocked"), page(continuation=""),
                     page(continuation="x" * 4097), page(tenantId=backup.OTHER), page(recoveryPoint="other"),
                     page(schemaVersion=26), page(privilegeManifestVersion=11), page(processed=8),
                     page(processed=True), page(decodedBytes=(80 << 20) + 1), page(decodedBytes=-1),
                     page(findings=[{"class": "repairable", "code": "RESTORE_SEARCH_PROJECTION_DRIFT"}]),
                     page(findings=[{"class": "integrity", "code": "RESTORE_ACTIONABLE_JOB_RETAINED"}]),
                     page(findings=[{"class": "operational", "code": "RESTORE_PENDING_IDEMPOTENCY_V2"}]),
                     page(findings=["private"]), page(secret="private"), None]
        for bad in bad_pages:
            for preceding in ([], [page()]):
                with self.subTest(bad=bad, position=len(preceding)), self.assertRaises(runner.Failure):
                    chain(mock.Mock(side_effect=preceding + [bad]))
    def test_missing_terminal_cursor_cycle_and_page_bound(self):
        with self.assertRaisesRegex(runner.Failure, "continuation"):
            chain(mock.Mock(return_value=page()))
        with self.assertRaisesRegex(runner.Failure, "pagination"):
            chain(mock.Mock(return_value=page(traversal="complete", continuation="")))
        with self.assertRaisesRegex(runner.Failure, "pagination"):
            chain(mock.Mock(side_effect=[page(), page(traversal="complete")]))
        with mock.patch.object(backup.restore_pages, "MAX_PAGES", 2), self.assertRaisesRegex(runner.Failure, "page-limit"):
            chain(mock.Mock(side_effect=[page(), page(continuation="last")]))
        with self.assertRaisesRegex(runner.Failure, "child-failed"):
            chain(mock.Mock(side_effect=[page(), runner.Failure("child-failed")]))
    def test_closed_complete_report_required(self):
        original = evidence()
        self.assertTrue(backup.valid_evidence(original))
        for field in original:
            value = copy.deepcopy(original)
            del value[field]
            self.assertFalse(backup.valid_evidence(value), field)
        for field, value in (("tenantCount", 1), ("tenantCount", True), ("dumpBytes", 0),
                             ("dumpBytes", backup.MAX_DUMP + 1), ("dumpDigest", "private"),
                             ("verificationPages", [1, 2]), ("verificationPages", [True, 2]),
                             ("auditRows", [0, 1]), ("postgresVersions", [180006, 180005]),
                             ("sourceStopped", False), ("signedDelivery", 1), ("tenantSettingsRetained", False),
                             ("pluginRetained", False), ("personalItemsRetained", 1), ("archiveStopped", False), ("ratingsRetained", False)):
            self.assertFalse(backup.valid_evidence(dict(original, **{field: value})), field)
        self.assertFalse(backup.valid_evidence(dict(original, logs="private")))
    def test_failed_phase_never_starts_restored_runtime(self):
        for failed in ("baseline", "capture", "dump", "restore", "verify"):
            instance = object.__new__(backup.BackupRestore)
            for name in ("baseline", "capture", "dump", "restore", "verify", "runtime"):
                setattr(instance, name, mock.Mock(side_effect=runner.Failure("fixture-failed") if name == failed else None))
            with self.assertRaisesRegex(runner.Failure, "fixture-failed"):
                instance.run()
            instance.runtime.assert_not_called()
    def test_missing_tenant_stops_before_row_scan(self):
        instance = object.__new__(backup.BackupRestore)
        for tenants in ([backup.TENANT], [backup.OTHER], list(backup.TENANTS) + ["extra"]):
            instance.sql = mock.Mock(return_value=backup.json.dumps(tenants))
            with self.assertRaisesRegex(runner.Failure, "tenant-inventory"):
                instance.inventory()
            self.assertEqual(instance.sql.call_count, 1)
    def test_dump_failure_does_not_copy_or_stop_source(self):
        instance = object.__new__(backup.BackupRestore)
        instance.phase = mock.Mock()
        instance.compose = mock.Mock(side_effect=[b"/tmp/catalog-archive.0123456789\n", runner.Failure("child-failed"), b""])
        with self.assertRaisesRegex(runner.Failure, "child-failed"):
            instance.dump()
        self.assertFalse(any(call.args[0] in ("cp", "stop") for call in instance.compose.call_args_list))
        self.assertEqual(instance.compose.call_args.args, ("exec", "-T", "postgres", "rm", "-r", "--", "/tmp/catalog-archive.0123456789"))
    def test_runtime_requires_one_baseline_per_search_before_replay(self):
        results = [{"items": [{"id": name}]} for name in ("one", "two", "extra")]
        for baseline_count in (1, 2, 3):
            instance = object.__new__(backup.BackupRestore)
            instance.rating_owner_verified = True
            instance.rating_seed, instance.rating_expectations = {}, {}
            instance.rating_capture_digest, instance.later_rating_state = "digest", {}
            instance.phase, instance.stopped, instance.compose = mock.Mock(), mock.Mock(), mock.Mock()
            instance.env, instance.retained, instance.feedback_records, instance.other_resources = {}, [], [], []
            instance.tenant_settings = []
            instance.other_namespace = {"id": "other"}
            instance.other = mock.Mock()
            instance.other.request.return_value = (instance.other_namespace, {})
            instance.base = "http://inert.invalid"
            instance.public = (200, {"ETag": '"' + backup.hashlib.sha256(b"retained").hexdigest() + '"'}, b"retained")
            instance.searches = [{"text": "one"}, {"text": "two"}]
            instance.search_results = results[:baseline_count]
            instance.api = mock.Mock()
            instance.api.request.side_effect = [(None, {})] + [(dict(value, partial=False), {}) for value in results[:2]]
            # Stop the valid control at the next phase without exercising replay,
            # process recovery or real HTTP. Mismatched baselines must stop earlier.
            instance.inventory = mock.Mock(side_effect=runner.Failure("after-search"))
            with self.subTest(baseline_count=baseline_count), mock.patch.object(backup, "records", return_value=[]), \
                    mock.patch.object(backup.audit, "request", return_value=instance.public):
                with self.assertRaisesRegex(runner.Failure if baseline_count == 2 else ValueError,
                                            "after-search" if baseline_count == 2 else "zip"):
                    instance.runtime()
                self.assertEqual(instance.inventory.call_count, int(baseline_count == 2))
                searches = [call for call in instance.api.request.call_args_list if call.args[0] == "POST"]
                self.assertEqual(searches, [mock.call("POST", "/api/v1/catalog/search", query)
                                           for query in instance.searches[:min(baseline_count, 2)]])
    def test_runtime_rejects_changed_bytes_etags_and_read_or_replay_writes(self):
        for fault in (None, "settings-values", "settings-defaults", "settings-etag", "public-bytes", "public-etag-missing", "public-etag-wrong", "read-write",
                      "replay-bytes", "replay-etag-missing", "replay-etag-wrong", "replay-marker", "replay-write", "retained-inputs", "ratings"):
            instance = object.__new__(backup.BackupRestore)
            instance.rating_owner_verified = True
            instance.rating_seed, instance.rating_expectations = {}, {}
            instance.rating_capture_digest, instance.later_rating_state = "digest", {}
            instance.policy = {"owned": True}
            instance.verify_retained_inputs = mock.Mock(side_effect=AssertionError("retained-input-mismatch") if fault == "retained-inputs" else None)
            instance.evidence = {}
            instance.phase, instance.stopped = mock.Mock(), mock.Mock()
            # A successful control reaches worker startup. Failures must stop first.
            instance.compose = mock.Mock(side_effect=[None, runner.Failure("worker-start")])
            instance.env, instance.retained, instance.feedback_records, instance.other_resources = {}, [], [], []
            instance.tenant_settings = []
            instance.other_namespace = {"id": "other"}
            instance.other = mock.Mock()
            instance.other.request.return_value = (instance.other_namespace, {})
            instance.base = "http://inert.invalid"
            instance.public = (200, {}, b"retained")
            public_headers = {"ETag": '"' + backup.hashlib.sha256(b"retained").hexdigest() + '"'}
            if fault == "public-etag-missing":
                public_headers = {}
            elif fault == "public-etag-wrong":
                public_headers["ETag"] = '"wrong"'
            public = (200, public_headers, b"changed" if fault == "public-bytes" else b"retained")
            instance.searches, instance.search_results = [], []
            instance.before_inventory = {"tables": {"audit_events": "captured"}}
            instance.inventory = mock.Mock(side_effect=[
                {"changed": True} if fault == "read-write" else instance.before_inventory,
                {"changed": True} if fault == "replay-write" else instance.before_inventory])
            instance.replay_value = b'{"id":"created","revision":1}\n'
            instance.replay_body, instance.replay_headers = {"slug": "retained"}, {"Idempotency-Key": "retained-key"}
            headers = {"ETag": '"rev-1"', "Idempotency-Replayed": "true"}
            if fault == "replay-etag-missing":
                del headers["ETag"]
            elif fault == "replay-etag-wrong":
                headers["ETag"] = '"rev-2"'
            elif fault == "replay-marker":
                del headers["Idempotency-Replayed"]
            replay = instance.replay_value.rstrip() if fault == "replay-bytes" else instance.replay_value
            instance.api = mock.Mock()
            saved = {"tenantId": backup.TENANT, "revision": 1, "overrides": {"newJobMaxAttempts": 2},
                     "deploymentDefaults": {"newJobMaxAttempts": 7}}
            validator = '"settings-' + 'a' * 64 + '"'
            instance.tenant_settings = [(instance.api, saved, validator)]
            current = dict(saved, overrides={"newJobMaxAttempts": 5}) if fault == "settings-values" else saved
            if fault == "settings-defaults":
                current = dict(saved, deploymentDefaults={"newJobMaxAttempts": 5})
            current_headers = {"ETag": '"wrong"' if fault == "settings-etag" else validator}
            instance.api.request.side_effect = [(None, {}), (current, current_headers), (replay, headers)]
            with self.subTest(fault=fault), mock.patch.object(backup, "records", return_value=[]), \
                    mock.patch.object(backup.audit, "request", return_value=public), \
                    mock.patch.object(backup.authorization_recovery, "verify") as policy_verify, \
                    mock.patch.object(backup.backup_ratings, "verify_api", side_effect=AssertionError("ratings-mismatch") if fault == "ratings" else None):
                if fault is None:
                    with self.assertRaisesRegex(runner.Failure, "worker-start"):
                        instance.runtime()
                    self.assertEqual(instance.inventory.call_count, 2)
                    instance.api.request.assert_called_with("POST", "/api/v1/namespaces", instance.replay_body,
                                                            (201,), instance.replay_headers, decode_body=False)
                    self.assertEqual(instance.compose.call_args.args[-1], "worker")
                    policy_verify.assert_called_once_with(instance.api, instance.policy)
                else:
                    with self.assertRaises(AssertionError):
                        instance.runtime()
                    self.assertEqual(instance.compose.call_count, 1)
                    self.assertEqual(instance.compose.call_args.args[-1], "api")
                    if fault in ("retained-inputs", "ratings"):
                        policy_verify.assert_called_once_with(instance.api, instance.policy)
                        self.assertEqual(instance.evidence, {})
                    else:
                        policy_verify.assert_not_called()
                    if fault.startswith("public-") or fault == "read-write":
                        self.assertEqual(instance.api.request.call_count, 2)

    def test_copied_archive_is_read_with_a_bound_before_source_stop(self):
        instance = object.__new__(backup.BackupRestore)
        instance.phase = mock.Mock()
        instance.compose = mock.Mock(side_effect=[b"/tmp/catalog-archive.0123456789\n", b"", b"4", b"", b""])
        instance.state = Path("/owned")
        contents = mock.mock_open(read_data=b"oversized")
        with mock.patch.object(Path, "chmod"), mock.patch.object(Path, "open", contents), \
                self.assertRaisesRegex(runner.Failure, "copied-dump-size"):
            instance.dump()
        contents().read.assert_called_once_with(backup.MAX_DUMP + 1)
        self.assertFalse(any(call.args[0] == "stop" for call in instance.compose.call_args_list))
    def test_copy_failure_cleans_the_correct_container_without_advancing_recovery(self):
        for operation, service in (("dump", "postgres"), ("restore", "restored")):
            instance = object.__new__(backup.BackupRestore)
            instance.phase, instance.ctl = mock.Mock(), mock.Mock()
            instance.sql = mock.Mock(return_value="0")
            instance.state, instance.archive = Path("/owned"), Path("/owned/catalog-backup.dump")
            calls = []
            def execute(*args, **_kwargs):
                calls.append(args)
                if args[0] == "cp":
                    raise runner.Failure("copy-failed")
                return b"4" if "stat" in args else b"/tmp/catalog-archive.0123456789\n"
            instance.compose = execute
            with self.subTest(operation=operation), self.assertRaisesRegex(runner.Failure, "copy-failed"):
                getattr(instance, operation)()
            self.assertEqual(calls[-1], ("exec", "-T", service, "rm", "-r", "--", "/tmp/catalog-archive.0123456789"))
            self.assertFalse(any(call[0] == "stop" or "pg_restore" in call for call in calls))
            instance.ctl.assert_not_called()
    def test_restore_failure_never_reconciles_or_bootstraps(self):
        instance = object.__new__(backup.BackupRestore)
        instance.phase = mock.Mock()
        instance.sql = mock.Mock(return_value="0")
        instance.archive = Path("/owned/catalog-backup.dump")
        instance.ctl = mock.Mock()
        calls = []
        def execute(*args, **_kwargs):
            calls.append(args)
            if "pg_restore" in args:
                raise runner.Failure("child-failed")
            return b"/tmp/catalog-archive.0123456789\n"
        instance.compose = mock.Mock(side_effect=execute)
        with self.assertRaisesRegex(runner.Failure, "child-failed"):
            instance.restore()
        instance.ctl.assert_not_called()
        restore = next(call for call in calls if "pg_restore" in call)
        self.assertIn("--single-transaction", restore)
        self.assertNotIn("--clean", restore)
        self.assertNotIn("--create", restore)
        self.assertEqual(calls[-1], ("exec", "-T", "restored", "rm", "-r", "--", "/tmp/catalog-archive.0123456789"))
        self.assertEqual(calls[0][:3], ("up", "--no-deps", "--detach"))
    def test_audit_mismatch_cannot_finish_verification(self):
        for comparison in ({"valid": False}, {"valid": True, "rowCount": 1, "eventsDigest": "wrong"}):
            instance = object.__new__(backup.BackupRestore)
            instance.phase = mock.Mock()
            instance.stopped = mock.Mock()
            instance.before_inventory = {"fixture": "digest"}
            instance.inventory = mock.Mock(return_value=instance.before_inventory)
            instance.point = "fixture-point"
            instance.plan = [{"version": 27}]
            instance.manifest_version = 12
            instance.manifests = [{"manifest": {"eventsDigest": "expected"}}]
            instance.audit_rows = [1]
            instance.ctl = mock.Mock(return_value=comparison)
            instance.evidence = {}
            with mock.patch.object(backup, "verify_chain", return_value=(2, {"RESTORE_ACTIONABLE_JOB_RETAINED"})), \
                    self.assertRaisesRegex(runner.Failure, "audit-mismatch"):
                instance.verify()
            self.assertEqual(instance.evidence, {})
    def test_partial_evidence_cleanup_and_failed_commands_fail_supervisor(self):
        report = {"profile": "backup-restore", "images": {"api": "image"}, "servedVersion": {"version": "functional"},
                  "smoke": {"checkCount": 1}, "backupRestore": evidence()}
        steps = list(runner.selected_steps("backup-restore"))
        self.assertTrue(runner.finalize(copy.deepcopy(report), steps, "", True))
        for selected, failure, cleanup in ((steps[:-1], "", True), (steps, "child-failed", True), (steps, "", False)):
            self.assertFalse(runner.finalize(copy.deepcopy(report), selected, failure, cleanup))
        report.pop("backupRestore")
        self.assertFalse(runner.finalize(report, steps, "", True))
