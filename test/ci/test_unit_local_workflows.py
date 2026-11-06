"""Isolated local workflow policy tests; no Git, act, Docker or subprocesses."""

from datetime import UTC, datetime
import hashlib
import json
import os
from pathlib import Path
import re
import tempfile
import unittest
from unittest.mock import patch
from types import SimpleNamespace

import local_workflows as local


class LocalPolicyTests(unittest.TestCase):
    def setUp(self):
        guard = patch.object(local.subprocess, "run", side_effect=AssertionError("unit attempted a subprocess"))
        guard.start()
        self.addCleanup(guard.stop)

    def test_selections_have_exact_jobs_and_explicit_no_upload_inputs(self):
        expected = {
            "lint": ("quality.yml", "source-local", "lint"),
            "unit": ("quality.yml", "isolated-unit", "unit"),
            "ui": ("quality.yml", "ui", "ui"),
            "docs": ("quality.yml", "documentation", "docs"),
            "charts": ("images.yml", "charts", None),
            "image": ("images.yml", "images", None),
            "security": ("extended.yml", "extended", "source-security"),
            "postgres": ("functional.yml", "qualification", "coverage-postgres-check"),
        }
        self.assertEqual(set(local.SELECTIONS), set(expected))
        for name, (workflow, job, suite) in expected.items():
            selected = local.selection(name)
            self.assertEqual(selected[:2], (workflow, job))
            self.assertEqual(selected[2].get("suite"), suite)
            self.assertEqual(selected[2]["upload_artifacts"], "false")
            self.assertGreater(selected[5], 0)

    def test_image_is_one_native_product_and_never_a_release_candidate(self):
        for product in ("backend", "ui"):
            selected = local.selection("image", product)
            self.assertEqual(selected[3], ["product:" + product, "arch:amd64"])
            self.assertEqual(selected[2]["scan"], "true")
            self.assertEqual(selected[2]["release_candidate"], "false")

    def test_arbitrary_workflows_products_and_flags_are_refused(self):
        for name, product in (("all", "backend"), ("source", "backend"),
                              ("../release.yml", "backend"), ("image", "--push"), ("image", "all")):
            with self.subTest(name=name, product=product), self.assertRaises(ValueError):
                local.selection(name, product)

    def test_environment_excludes_ambient_credentials_and_config(self):
        with patch.dict(os.environ, {"GH_TOKEN": "private", "AWS_SECRET_ACCESS_KEY": "private",
                                    "ACTIONS_RUNTIME_URL": "https://example.invalid", "GOTELEMETRY": "on",
                                    "DOCKER_HOST": "ssh://private", "GIT_DIR": "/private", "MAKEFLAGS": "unsafe"}):
            env = local.environment(Path("/owned"))
        self.assertTrue(all("private" not in value for value in env.values()))
        self.assertEqual(env["GOTELEMETRY"], "off")
        self.assertEqual(env["NPM_CONFIG_AUDIT"], "false")
        self.assertEqual(env["ASTRO_TELEMETRY_DISABLED"], "1")
        self.assertNotIn("GH_TOKEN", env)
        self.assertNotIn("ACTIONS_RUNTIME_URL", env)
        self.assertEqual(env["DOCKER_HOST"], "unix:///var/run/docker.sock")
        for key in ("HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "DOCKER_CONFIG", "TMPDIR"):
            self.assertIn(Path("/owned"), Path(env[key]).parents)

    def test_arguments_keep_paths_literal_and_disable_hidden_inputs_and_services(self):
        root = Path("/owned path/with 'quotes'")
        args = local.arguments("docs", "backend", root, "a" * 40, "123456789012345678")
        for option in ("--env-file", "--secret-file", "--input-file", "--var-file"):
            self.assertEqual(args[args.index(option) + 1], "/dev/null")
        self.assertEqual(args[args.index("--secret") + 1], "GITHUB_TOKEN=")
        self.assertIn("--pull=false", args)
        self.assertIn("--no-cache-server", args)
        self.assertIn("--rm", args)
        self.assertNotIn("--reuse", args)
        self.assertEqual(args[args.index("--artifact-server-path") + 1], "")
        self.assertEqual(args[args.index("--action-cache-path") + 1], str(root / "actions"))
        self.assertEqual(args[args.index("--directory") + 1], str(root / "source"))
        self.assertEqual(args[args.index("--container-daemon-socket") + 1], "-")
        self.assertEqual(args[args.index("--platform") + 1], "ubuntu-24.04=" + local.runner_reference())
        self.assertIn("upload_artifacts=false", args)
        self.assertIn("GOTELEMETRY=off", args)
        self.assertIn("GITHUB_REPOSITORY=open-agentic-catalog/open-agentic-catalog", args)
        self.assertIn("HOME=" + str(root / "tmp/home"), args)
        self.assertIn("DOCKER_CONFIG=" + str(root / "tmp/home/.docker"), args)

    def test_docker_socket_is_explicit_and_not_shared_with_browser_or_doc_jobs(self):
        for name in ("lint", "unit", "docs", "ui", "charts", "security", "postgres", "image"):
            args = local.arguments(name, "backend", Path("/owned"), "b" * 40, "123456789012345678", docker_gid=123)
            socket = args[args.index("--container-daemon-socket") + 1]
            self.assertEqual(socket, "/var/run/docker.sock" if name in {"charts", "postgres", "image"} else "-")
        with self.assertRaisesRegex(ValueError, "host socket group"):
            local.arguments("image", "backend", Path("/owned"), "b" * 40, "123456789012345678")

    def test_age_policy_has_a_passing_control_and_rejects_each_young_input(self):
        now = datetime(2026, 10, 27, tzinfo=UTC)
        local.require_age(now)
        for key in ("publishedAt", "runnerCreatedAt"):
            with patch.dict(local.PIN, {key: "2026-10-26T00:00:00Z"}), self.assertRaisesRegex(ValueError, "14-day"):
                local.require_age(now)

    def test_checksum_mismatch_stops_before_unpack_or_docker(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with (
                patch.object(local, "ROOT", root), patch.object(local, "OWNED", root / ".tools/ci/act"),
                patch.object(local, "require_platform"), patch.object(local, "require_age"),
                patch.object(local.shutil, "disk_usage", return_value=SimpleNamespace(free=16 << 30)),
                patch.object(local.tools, "download", side_effect=lambda _url, destination: destination.write_bytes(b"wrong")),
                patch.object(local.tools, "unpack") as unpack, patch.object(local, "docker") as docker,
            ):
                with self.assertRaisesRegex(ValueError, "checksum mismatch"):
                    local.install()
                unpack.assert_not_called()
                docker.assert_not_called()

    def test_tampered_installation_refuses_before_executable_runs(self):
        with tempfile.TemporaryDirectory() as directory:
            owned = Path(directory)
            (owned / "act").write_bytes(b"modified executable")
            (owned / "installation.json").write_text(json.dumps({"version": local.PIN["version"],
                                                                 "archiveSha256": local.PIN["sha256"],
                                                                 "binarySha256": "00" * 32}))
            with patch.object(local, "OWNED", owned), self.assertRaisesRegex(ValueError, "stale or changed"):
                local.check_act({}, owned)

    def test_installer_records_ready_state_only_after_the_final_probe(self):
        archive = b"verified fixture archive"
        def unpack(_archive, destination):
            (destination / "act").write_bytes(b"fixture act")
            (destination / "LICENSE").write_text("fixture license")

        for fail in (False, True):
            with self.subTest(final_probe_fails=fail), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                owned = root / ".tools/ci/act"
                def docker(arguments, *_args, **_kwargs):
                    if arguments[:2] == ["image", "inspect"]:
                        return SimpleNamespace(stdout=json.dumps([{"Id": "sha256:" + "ab" * 32}]))
                    if "sudo -n true" in " ".join(map(str, arguments)) and fail:
                        raise local.subprocess.CalledProcessError(1, "controlled final probe")
                    return SimpleNamespace(stdout="fixture prerequisite check")
                with (
                    patch.object(local, "ROOT", root), patch.object(local, "OWNED", owned),
                    patch.object(local, "require_platform"), patch.object(local, "require_age"),
                    patch.object(local.os, "getuid", return_value=1001), patch.object(local.os, "getgid", return_value=1001),
                    patch.object(local.shutil, "disk_usage", return_value=SimpleNamespace(free=16 << 30)),
                    patch.dict(local.PIN, {"sha256": hashlib.sha256(archive).hexdigest()}),
                    patch.object(local.tools, "download", side_effect=lambda _url, target: target.write_bytes(archive)),
                    patch.object(local.tools, "unpack", side_effect=unpack),
                    patch.object(local, "check_act"), patch.object(local, "check_runner") as ready_check,
                    patch.object(local, "docker", side_effect=docker),
                ):
                    if fail:
                        with self.assertRaises(local.subprocess.CalledProcessError):
                            local.install()
                    else:
                        local.install()
                    receipt = json.loads((owned / "installation.json").read_text())
                    self.assertEqual("runnerId" in receipt, not fail)
                    self.assertEqual(ready_check.call_count, 0 if fail else 1)

    def test_runner_requires_exact_digest_and_linux_amd64(self):
        valid = {"Id": "sha256:" + "ab" * 32, "Os": "linux", "Architecture": "amd64", "Config": {"User": str(os.getuid()) + ":" + str(os.getgid())}}
        with tempfile.TemporaryDirectory() as temporary:
            owned = Path(temporary)
            receipt = {"baseRunner": local.PIN["runner"], "runnerRecipeSha256": hashlib.sha256(local.RUNNER_RECIPE.read_bytes()).hexdigest(),
                       "runnerUID": os.getuid(), "runnerGID": os.getgid(), "runnerReference": local.runner_reference(), "runnerId": valid["Id"]}
            (owned / "installation.json").write_text(json.dumps(receipt))
            for updates, accepted in (({}, True), ({"Architecture": "arm64"}, False), ({"Config": {"User": "root"}}, False),
                                      ({"Id": "sha256:" + "cd" * 32}, False)):
                result = SimpleNamespace(returncode=0, stdout=json.dumps([{**valid, **updates}]))
                with patch.object(local, "OWNED", owned), patch.object(local, "docker", return_value=result):
                    if accepted:
                        self.assertEqual(local.check_runner({}, Path("/owned")), valid["Id"])
                    else:
                        with self.assertRaisesRegex(ValueError, "identity or platform"):
                            local.check_runner({}, Path("/owned"))

    def test_preexisting_shared_cache_is_preserved(self):
        def docker(args, *_args, **_kwargs):
            if args[:2] == ["volume", "ls"]:
                return SimpleNamespace(stdout="act-toolcache\nother-volume\n")
            self.assertEqual(args[:2], ["ps", "-a"])
            return SimpleNamespace(stdout="")
        with patch.object(local, "docker", side_effect=docker) as calls:
            with local.docker_resources({}, Path("/owned"), "123"):
                pass
            self.assertFalse(any(call.args[0][:2] in (["volume", "create"], ["volume", "rm"]) for call in calls.call_args_list))

    def test_stale_runner_recipe_or_host_identity_refuses_before_docker(self):
        with tempfile.TemporaryDirectory() as temporary:
            owned = Path(temporary)
            (owned / "installation.json").write_text(json.dumps({"baseRunner": local.PIN["runner"]}))
            with patch.object(local, "OWNED", owned), patch.object(local, "docker") as docker:
                with self.assertRaisesRegex(ValueError, "runner setup is stale"):
                    local.check_runner({}, owned)
                docker.assert_not_called()

    def test_new_shared_cache_is_removed_after_body_failure(self):
        created = False
        def docker(args, *_args, **_kwargs):
            nonlocal created
            if args[:2] == ["volume", "create"]:
                created = True
            if args[:2] == ["volume", "ls"]:
                return SimpleNamespace(stdout="act-toolcache\n" if created else "")
            if args[:2] == ["volume", "inspect"]:
                return SimpleNamespace(stdout=json.dumps([{"Labels": {local.LABEL: "123"}}]))
            return SimpleNamespace(stdout="")
        with patch.object(local, "docker", side_effect=docker) as calls:
            with self.assertRaisesRegex(ValueError, "body failed"):
                with local.docker_resources({}, Path("/owned"), "123"):
                    raise ValueError("body failed")
            self.assertEqual(calls.call_args.args[0], ["volume", "rm", "act-toolcache"])

    def test_replaced_shared_cache_is_never_removed(self):
        inspections = 0
        created = False
        def docker(args, *_args, **_kwargs):
            nonlocal inspections, created
            if args[:2] == ["volume", "create"]:
                created = True
            if args[:2] == ["volume", "ls"]:
                return SimpleNamespace(stdout="act-toolcache\n" if created else "")
            if args[:2] == ["volume", "inspect"]:
                inspections += 1
                return SimpleNamespace(stdout=json.dumps([{"Labels": {local.LABEL: "123" if inspections == 1 else "other"}}]))
            return SimpleNamespace(stdout="")
        with patch.object(local, "docker", side_effect=docker) as calls:
            with self.assertRaisesRegex(ValueError, "ownership changed during execution"):
                with local.docker_resources({}, Path("/owned"), "123"):
                    pass
            self.assertFalse(any(call.args[0][:2] == ["volume", "rm"] for call in calls.call_args_list))


class WorkflowSourcePolicyTests(unittest.TestCase):
    def test_reused_workflows_have_separate_concurrency_groups(self):
        groups = {}
        for name in ("quality", "clients", "extended"):
            source = (local.ROOT / ".github/workflows" / (name + ".yml")).read_text()
            group = re.search(r"(?m)^  group: (.+)$", source)[1]
            self.assertTrue(group.startswith(name + "-"))
            # github.workflow names the caller in a reusable workflow. Model
            # one release caller with the same ref and all/default inputs.
            resolved = re.sub(r"\$\{\{[^}]+\}\}", "same", group)
            self.assertNotIn(resolved, groups.values())
            groups[name] = resolved
        self.assertEqual(len(groups), 3)

    def test_pages_deployment_requires_preparation_trusted_branch_and_hosted_admission(self):
        source = (local.ROOT / ".github/workflows/pages.yml").read_text()
        prepare, deploy = source.split("\n  deploy:\n", 1)
        self.assertNotIn("pull_request", source)
        self.assertIn("branches: [main]", source)
        self.assertLess(prepare.index("run: make docs-site-check"), prepare.index("make docs-build"))
        self.assertLess(prepare.index("make docs-build"), prepare.index("make docs-delivery"))
        self.assertIn("!env.ACT && github.ref == 'refs/heads/main'", prepare)
        self.assertIn("needs: prepare", deploy)
        self.assertIn("needs.prepare.outputs.hosted == 'true'", deploy)
        self.assertIn("github.ref == 'refs/heads/main'", deploy)
        self.assertIn("inputs.deploy", deploy)
        self.assertIn("name: github-pages", deploy)
        self.assertNotIn("pages: write", prepare)
        self.assertNotIn("id-token: write", prepare)
        self.assertIn("pages: write", deploy)
        self.assertIn("id-token: write", deploy)
        self.assertIn("path: ${{ env.DOCS_DELIVERY_OUTPUT }}", prepare)

    def test_contribution_completion_runs_after_every_selected_job_without_uploads(self):
        source = (local.ROOT / ".github/workflows/contributions.yml").read_text()
        self.assertNotIn("pull_request_target", source)
        self.assertNotIn("secrets: inherit", source)
        self.assertNotIn("upload-artifact", source)
        self.assertIn("contents: read", source)
        complete = source.split("  complete:\n", 1)[1]
        self.assertIn("name: Contribution checks complete", complete)
        self.assertIn("needs: [selection, baseline, unit, ui, docs, clients]", complete)
        self.assertIn("if: ${{ always() }}", complete)
        self.assertIn("admission.py outcomes", complete)
        for job in ("baseline", "unit"):
            block = re.search(r"(?ms)^  " + job + r":\n(.*?)(?=^  \S|\Z)", source)[1]
            self.assertNotIn("if:", block)
            self.assertIn("uses: ./.github/workflows/quality.yml", block)

    def test_release_delivery_cannot_bypass_qualification_or_rebuild(self):
        source = (local.ROOT / ".github/workflows/release.yml").read_text()
        verification, delivery = source.split("  deliver:\n", 1)
        self.assertIn("needs: [policy, qualify]", delivery)
        self.assertIn("inputs.publish", delivery)
        self.assertIn("environment: release", delivery)
        self.assertIn("needs.policy.result == 'success'", delivery)
        self.assertIn("admission.py release-policy", verification)
        self.assertIn("RELEASE_PUBLISH_ENABLED: ${{ vars.RELEASE_PUBLISH_ENABLED }}", verification)
        self.assertIn("publish: ${{ steps.intent.outputs.publish }}", verification)
        self.assertIn("release_checks: true", verification)
        self.assertNotIn("packages: write", verification)
        self.assertNotIn("contents: write", verification)
        self.assertIn("packages: write", delivery)
        self.assertIn("contents: write", delivery)
        self.assertNotIn("make ", delivery)
        self.assertNotIn("docker build", delivery)
        self.assertIn("qualified-release-${{ needs.policy.outputs.key }}", delivery)
        self.assertIn("release_delivery.py publish", delivery)
        self.assertIn("delivery-result.json", delivery)

    def test_functional_choices_have_exact_prerequisite_profiles(self):
        source = (local.ROOT / ".github/workflows/functional.yml").read_text()
        expected = {
            "coverage-postgres-check": "functional", "test-process-postgres": "functional",
            "test-functional": "functional", "test-ui-compose": "functional-ui", "test-ui-oidc": "functional-ui",
            "test-backup-restore": "functional", "test-federation-peers": "functional-ui", "test-webhook-report": "functional",
        }
        options = re.search(r"(?m)^        options:\n((?:          - .+\n)+)", source)[1]
        self.assertEqual([line.strip().removeprefix("- ") for line in options.splitlines()], list(expected))
        profiles = {}
        for names, profile in re.findall(r"(?m)^            ([^)\n]+)\) profile=([^ ]+) ;;$", source):
            for name in names.split("|"):
                self.assertNotIn(name, profiles)
                profiles[name] = profile
        self.assertEqual(profiles, expected)

    def test_peer_selection_installs_each_ui_and_browser_prerequisite(self):
        source = (local.ROOT / ".github/workflows/functional.yml").read_text()
        expected = {
            "Install locked UI dependencies": "run: make ui-install",
            "Install Chromium system libraries": "run: node node_modules/@playwright/test/cli.js install-deps chromium",
            "Install the locked Chromium browser": "run: make ui-browser-install",
        }
        steps = re.split(r"(?m)^      - ", source)
        for name, command in expected.items():
            with self.subTest(step=name):
                step = next(step for step in steps if step.startswith("name: " + name + "\n"))
                self.assertEqual(re.findall(r"inputs.suite == '([^']+)'", step),
                                 ["test-ui-compose", "test-ui-oidc", "test-federation-peers"])
                self.assertIn(command, step)
                self.assertNotIn("continue-on-error", step)
                if "install-deps" in command:
                    self.assertIn("working-directory: catalog-ui", step)

    def test_go_client_keeps_independent_results_and_explicit_report_paths(self):
        source = (local.ROOT / ".github/workflows/clients.yml").read_text()
        self.assertIn("matrix.language == 'go' && ' and webhook receiver'", source)
        self.assertIn('preflight --profile "$SDK_LANGUAGE"', source)
        self.assertIn('install --profile "$SDK_LANGUAGE"', source)
        steps = re.split(r"(?m)^      - ", source)
        raw_go = next(step for step in steps if "id: raw_go\n" in step)
        self.assertIn("if: ${{ matrix.language == 'go' }}", raw_go)
        self.assertIn("run: make client-go-check\n", raw_go)
        self.assertIn("steps.raw_go.outcome || 'not_run'", source)
        self.assertIn("steps.sdk_checks.outcome || 'not_run'", source)
        upload = next(step for step in steps if step.startswith("name: Retain safe client evidence\n"))
        self.assertIn("inputs.upload_artifacts && !env.ACT", upload)
        paths = re.search(r"(?m)^          path: \|\n((?:            .+\n)+)", upload)[1]
        self.assertEqual([line.strip() for line in paths.splitlines()], [
            "${{ runner.temp }}/ci-client-summary.md",
            "coverage/publication-report-unit.out",
            "output/artifacts/sdk/${{ matrix.language }}/manifest.json",
            "output/sdk/rust-coverage.json",
        ])

    def test_documentation_runs_source_then_site_checks_with_separate_results(self):
        source = (local.ROOT / ".github/workflows/quality.yml").read_text()
        job = re.search(r"(?ms)^  documentation:\n(.*?)(?=^  \S|\Z)", source)[1]
        checks = [step for step in re.split(r"(?m)^      - ", job) if "id: docs_" in step]
        self.assertEqual(len(checks), 2)
        for step, identifier, command in zip(checks, ("docs_source_checks", "docs_site_checks"),
                                             ("make docs-check", "make docs-site-check"), strict=True):
            self.assertIn("id: " + identifier + "\n        run: " + command + "\n", step)
            self.assertNotIn("continue-on-error", step)
            self.assertIn("steps." + identifier + ".outcome || 'not_run'", job)
        self.assertIn("install --profile docs", job)
        self.assertIn('"$DOCS_SOURCE_CHECKS" "$DOCS_SITE_CHECKS"', job)

    def test_local_deadlines_match_the_selected_workflow_job(self):
        for name in local.SELECTIONS:
            workflow, job, _inputs, _matrix, _docker, seconds = local.selection(name)
            source = (local.ROOT / ".github/workflows" / workflow).read_text()
            block = re.search(r"(?ms)^  " + re.escape(job) + r":\n(.*?)(?=^  \S|\Z)", source)[1]
            minutes = int(re.search(r"(?m)^    timeout-minutes: ([0-9]+)$", block)[1])
            with self.subTest(selection=name):
                self.assertEqual(seconds, minutes * 60)

    def test_every_manual_workflow_upload_is_opt_in_and_disabled_under_act(self):
        paths = sorted((local.ROOT / ".github/workflows").glob("*.yml"))
        self.assertEqual({path.name for path in paths}, {"quality.yml", "clients.yml", "images.yml", "functional.yml", "extended.yml", "assurance.yml", "benchmarks.yml", "pages.yml", "contributions.yml", "release.yml"})
        uploads = 0
        for path in paths:
            source = path.read_text()
            if path.name not in {"pages.yml", "contributions.yml"}:
                self.assertRegex(source, r"upload_artifacts:\n[^\n]*\n        type: boolean\n        default: false")
            for step in re.split(r"(?m)^      - ", source):
                if "uses: actions/upload-artifact@" in step:
                    uploads += 1
                    condition = re.search(r"(?m)^        if: (.+)$", step)
                    self.assertIsNotNone(condition, path)
                    expected = "needs.policy.outputs.upload == 'true'" if path.name == "release.yml" else "inputs.upload_artifacts"
                    self.assertIn(expected, condition[1], path)
                    self.assertIn("&& !env.ACT", condition[1], path)
        self.assertEqual(uploads, 18)

    def test_main_builds_and_release_tags_have_separate_read_only_entry_points(self):
        images = (local.ROOT / ".github/workflows/images.yml").read_text()
        self.assertIn("branches: [main]", images)
        self.assertIn("github.event_name == 'push' && fromJSON('[\"amd64\"]')", images)
        self.assertIn("github.event_name == 'push' && github.ref == 'refs/heads/main'", images)
        self.assertNotIn("packages: write", images)
        self.assertNotIn("release_delivery.py publish", images)
        release = (local.ROOT / ".github/workflows/release.yml").read_text()
        self.assertIn("tags: ['v*']", release)
        self.assertIn("needs.policy.outputs.upload == 'true'", release)
        self.assertIn("github.repository == 'open-agentic-catalog/open-agentic-catalog'", release)

    def test_only_internal_tagged_release_handoffs_can_upload_client_packages(self):
        source = (local.ROOT / ".github/workflows/clients.yml").read_text()
        reusable, manual = source.split("  workflow_dispatch:\n", 1)
        self.assertIn("      retain_packages:\n", reusable)
        self.assertRegex(reusable, r"retain_packages:\n[^\n]*\n        type: boolean\n        default: false")
        self.assertNotIn("retain_packages:", manual.split("permissions:\n", 1)[0])
        release = (local.ROOT / ".github/workflows/release.yml").read_text()
        caller = release.split("  clients:\n", 1)[1].split("  security:\n", 1)[0]
        self.assertIn("retain_packages: true", caller)
        self.assertIn("release_checks: true", caller)
        self.assertIn("upload_artifacts: ${{ needs.policy.outputs.upload == 'true' }}", caller)
        steps = re.split(r"(?m)^      - ", source)
        for title in ("Transfer only the tested VSIX and bounded receipt", "Upload the qualified SDK package set", "Stage the exact qualified SDK outputs"):
            step = next(s for s in steps if s.startswith("name: " + title + "\n"))
            condition = re.search(r"(?m)^        if: (.+)$", step)[1]
            self.assertIn("inputs.retain_packages", condition)
            self.assertIn("startsWith(github.ref, 'refs/tags/v')", condition)
            self.assertIn("inputs.upload_artifacts", condition)
        reports = next(s for s in steps if s.startswith("name: Retain safe client evidence\n"))
        self.assertNotIn("inputs.retain_packages", reports)
        self.assertIn("run: make vscode-package", source)

    def test_ordinary_chart_uploads_contain_reports_without_chart_archives(self):
        source = (local.ROOT / ".github/workflows/images.yml").read_text()
        uploads = [s for s in re.split(r"(?m)^      - ", source) if "uses: actions/upload-artifact@" in s]
        self.assertEqual(len(uploads), 2)
        for step in uploads:
            self.assertNotIn("*.tgz", step)
            self.assertNotIn("image.tar", step)
            self.assertIn("/reports/summary.txt", step)
        self.assertIn("run: make helm-package", source)
        release = (local.ROOT / ".github/workflows/release.yml").read_text()
        self.assertIn("output/release/charts/*.tgz", release)

    def test_lint_does_not_select_broad_regression_or_api_launcher_tests(self):
        source = (local.ROOT / ".github/workflows/quality.yml").read_text()
        self.assertIn("          - lint\n", source)
        self.assertIn("inputs.suite == 'source' || inputs.suite == 'lint'", source)
        self.assertIn("id: lint\n        if: inputs.suite == 'lint'\n        run: make check", source)
        for step in ("regression", "api_validation"):
            self.assertIn("id: " + step + "\n        if: inputs.suite != 'lint'", source)
        self.assertIn("(always() && inputs.suite != 'lint') && inputs.upload_artifacts && !env.ACT", source)

    def test_extended_security_keeps_local_scans_and_omits_registry_audit(self):
        source = (local.ROOT / ".github/workflows/extended.yml").read_text()
        self.assertIn("run: make security", source)
        self.assertIn("run: make ui-deps-check", source)
        self.assertNotIn("ui-deps-audit", source)
        self.assertNotIn("ui_audit", source)
        self.assertNotIn("UI_AUDIT", source)

    def test_quality_summaries_copy_only_the_selected_file_under_act(self):
        source = (local.ROOT / ".github/workflows/quality.yml").read_text()
        steps = [step for step in re.split(r"(?m)^      - ", source) if step.startswith("name: Save the local job summary before runner removal")]
        self.assertEqual(len(steps), 4)
        for step, filename, job in zip(steps, ("ci-quality-unit.md", "ci-quality-local.md", "ci-quality-ui.md", "ci-quality-docs.md"),
                                       ("isolated-unit", "source-local", "ui", "documentation"), strict=True):
            self.assertIn("if: ${{ always() && env.ACT }}", step)
            self.assertIn("/quality/" + job, step)
            self.assertIn('cp --no-dereference -- "$RUNNER_TEMP/' + filename + '"', step)
            self.assertNotIn("cp -r", step)


if __name__ == "__main__":
    unittest.main()
