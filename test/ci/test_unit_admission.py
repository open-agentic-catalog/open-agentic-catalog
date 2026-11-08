"""Isolated selection, package and candidate refusal tests. No processes or network."""

import copy
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import admission as a
import candidate as c
import release_delivery as r

SOURCE = {"commit": "a" * 40, "sourceSha256": "b" * 64}


class AdmissionTests(unittest.TestCase):
    def setUp(self):
        guard = patch.object(a.subprocess, "check_output", side_effect=AssertionError("unit started a process"))
        guard.start()
        self.addCleanup(guard.stop)

    def test_backend_contract_and_shared_tools_select_every_client(self):
        for name in ("internal/catalog/service.go", "api/openapi/control-v1.yaml", "VERSION", "Makefile", "test/sdk/runner.py", ".github/workflows/clients.yml", "sdk/shared-input"):
            with self.subTest(name=name):
                self.assertEqual(a.select([name])["languages"], list(a.LANGUAGES))

    def test_shared_python_inputs_select_all_consumers(self):
        expected = {"ui": True, "docs": True, "languages": ["go", "python", "typescript", "rust"]}
        for name in ("pyproject.toml", "uv.lock", "ruff.toml", "test/ci/pins.json",
                     "test/ci/tools.py", "test/packaging/versioning.py"):
            with self.subTest(name=name):
                self.assertEqual(a.select([name]), expected)
        results = dict.fromkeys(("baseline", "unit", "ui", "docs", "clients"), "success")
        plan = a.select(["uv.lock"])
        self.assertEqual(set(a.outcomes(plan, results).values()), {"passed"})
        for job in ("ui", "docs", "clients"):
            for outcome in ("failure", "cancelled", "skipped"):
                with self.subTest(job=job, outcome=outcome), self.assertRaises(ValueError):
                    a.outcomes(plan, {**results, job: outcome})

    def test_nested_manifests_keep_their_own_selection(self):
        for name in ("misc/pyproject.toml", "misc/uv.lock", "uv.lock.notes", "pyproject.toml.example"):
            with self.subTest(name=name):
                self.assertEqual(a.select([name]), {"ui": False, "docs": False, "languages": []})
        self.assertEqual(a.select(["documentation/uv.lock"]), {"ui": False, "docs": True, "languages": []})
        self.assertEqual(a.select(["sdk/python/pyproject.toml"]), {"ui": False, "docs": True, "languages": ["python"]})

    def test_independent_language_and_projected_document_inputs(self):
        for language in a.LANGUAGES:
            self.assertEqual(a.select([f"sdk/{language}/client.code"])["languages"], [language])
        self.assertTrue(a.select(["some/published-guide.md"], ["some/published-guide.md"])["docs"])
        self.assertEqual(a.select(["examples/rust-catalog-report/main.rs"])["languages"], ["rust"])
        self.assertEqual(a.select(["extensions/vscode/src/main.ts"])["languages"], ["typescript"])
        self.assertEqual(a.select(["documentation/assets/favicon.svg"])["languages"], [])
        self.assertTrue(a.select(["catalog-ui/src/view.tsx"])["ui"])
        self.assertFalse(a.select([])["docs"])
        for invalid in ("../private", "/private"):
            with self.assertRaises(ValueError):
                a.select([invalid])

    def test_invalid_language_never_silently_returns_an_empty_matrix(self):
        self.assertEqual(a.languages("all"), list(a.LANGUAGES))
        for value in ("", "unknown", "--publish"):
            with self.assertRaises(ValueError):
                a.languages(value)

    def test_selected_failure_skip_and_cancellation_cannot_complete(self):
        plan = {"ui": True, "docs": False, "languages": ["rust"]}
        result = {"baseline": "success", "unit": "success", "ui": "success", "docs": "skipped", "clients": "success"}
        self.assertEqual(a.outcomes(plan, result)["docs"], "unselected")
        for name in ("baseline", "unit", "ui", "clients"):
            for failure in ("failure", "cancelled", "skipped", ""):
                with self.subTest(name=name, failure=failure), self.assertRaises(ValueError):
                    a.outcomes(plan, {**result, name: failure})

    def sdk(self, root):
        directory = root / "output/artifacts/sdk/typescript"
        directory.mkdir(parents=True)
        (root / "api/openapi").mkdir(parents=True)
        (root / "api/openapi/control-v1.yaml").write_text("api")
        (root / "VERSION").write_text("0.1.0\n")
        (root / "client.ts").write_text("source")
        (directory / "client.tgz").write_text("qualified package")
        value = {"language": "typescript", "releaseBase": "0.1.0", "openapiSha256": a.digest(root / "api/openapi/control-v1.yaml"),
                 "sources": {"client.ts": a.digest(root / "client.ts")}, "tools": {"node": "pinned"},
                 "artifacts": {"client.tgz": a.digest(directory / "client.tgz")}}
        (directory / "manifest.json").write_text(json.dumps(value))
        return directory

    def test_only_fresh_successful_sdk_packages_are_staged(self):
        with tempfile.TemporaryDirectory() as temporary, patch.object(a, "source_identity", return_value=SOURCE):
            root = Path(temporary)
            directory = self.sdk(root)
            output = root / "output/artifacts/qualified-sdk/run-1"
            with self.assertRaises(ValueError):
                a.stage_sdk(root, "typescript", "run-1", output)
            a.qualify_sdk(root, "typescript", "run-1", SOURCE)
            a.stage_sdk(root, "typescript", "run-1", output)
            self.assertEqual({p.name for p in output.iterdir()}, {"manifest.json", "qualification.json", "client.tgz"})
            for change in ("run", "package", "source"):
                with self.subTest(change=change), self.assertRaises(ValueError):
                    if change == "run":
                        a.stage_sdk(root, "typescript", "another-run", root / "output/artifacts/qualified-sdk/run-2")
                    elif change == "package":
                        (directory / "client.tgz").write_text("changed")
                        a.stage_sdk(root, "typescript", "run-1", root / "output/artifacts/qualified-sdk/run-2")
                    else:
                        (root / "client.ts").write_text("changed")
                        a.qualify_sdk(root, "typescript", "run-1", SOURCE)

    def test_same_source_and_all_results_are_required_for_release(self):
        results = dict.fromkeys(("source", "security", "clients", "charts", "candidates", "runtime"), "success")
        self.assertEqual(c.release_results(SOURCE, results)["source"], SOURCE)
        for key in results:
            for bad in (None, "failure", "cancelled", "skipped"):
                changed = {**results, key: bad}
                with self.assertRaises(ValueError):
                    c.release_results(SOURCE, changed)

    def test_image_platform_and_build_substitution_are_refused(self):
        value = {"product": "backend", "platform": "linux/amd64", "imageId": "sha256:" + "c" * 64,
                 "build": {"version": "0.1.0", "commit": SOURCE["commit"], "sourceState": "clean"}}
        raw = {"Id": value["imageId"], "Os": "linux", "Architecture": "amd64", "Config": {"Labels": {
            "org.opencontainers.image.version": "0.1.0", "org.opencontainers.image.revision": SOURCE["commit"],
            "io.open-agentic-catalog.source-state": "clean", "org.opencontainers.image.title": "Open Agentic Catalog"}}}
        c.image_identity(raw, value)
        for key, bad in (("Id", "sha256:" + "d" * 64), ("Architecture", "arm64")):
            with self.assertRaises(ValueError):
                c.image_identity({**raw, key: bad}, value)
        changed = copy.deepcopy(raw)
        changed["Config"]["Labels"]["org.opencontainers.image.version"] = "different"
        with self.assertRaises(ValueError):
            c.image_identity(changed, value)

    def test_runtime_candidate_admission_binds_native_host_without_enabling_emulation(self):
        for machine, platform in (("x86_64", "linux/amd64"), ("aarch64", "linux/arm64"), ("arm64", "linux/arm64")):
            with self.subTest(machine=machine), patch.object(c.host_platform, "system", return_value="Linux"), patch.object(c.host_platform, "machine", return_value=machine):
                self.assertEqual(c.native_platform(), platform)
                value = {"imageId": "sha256:" + "c" * 64, "platform": platform}
                with patch.object(a, "source_identity", return_value=SOURCE), patch.object(c, "read_candidate", return_value=value) as read, \
                        patch.object(c, "run", return_value='[{}]') as run, patch.object(c, "image_identity") as inspect:
                    self.assertEqual(c.admit(Path("/owned/candidate.json"), "backend", load=True, platform=platform), value)
                    read.assert_called_once_with(Path("/owned/candidate.json"), identity=SOURCE, product="backend", platform=platform)
                    self.assertEqual(run.call_args_list[0].args[0], ["docker", "image", "load", "--input", "/owned/image.tar"])
                    self.assertEqual(run.call_args_list[1].args[0], ["docker", "image", "inspect", value["imageId"]])
                    inspect.assert_called_once_with({}, value)
                foreign = "linux/arm64" if platform == "linux/amd64" else "linux/amd64"
                with patch.object(c, "read_candidate", side_effect=AssertionError("read before native refusal")), patch.object(c, "run", side_effect=AssertionError("process before native refusal")):
                    with self.assertRaisesRegex(ValueError, "not native"):
                        c.admit(Path("/owned/candidate.json"), "backend", platform=foreign)
        for system, machine in (("Darwin", "arm64"), ("Windows", "x86_64"), ("Linux", "ppc64le")):
            with self.subTest(system=system, machine=machine), patch.object(c.host_platform, "system", return_value=system), patch.object(c.host_platform, "machine", return_value=machine):
                with self.assertRaisesRegex(ValueError, "native Linux"):
                    c.native_platform()

    def test_external_delivery_is_refused_locally_and_under_act(self):
        for environment in ({}, {"ACT": "true", "GITHUB_ACTIONS": "true", "RELEASE_PUBLISH": "true"}, {"GITHUB_ACTIONS": "true"}):
            with self.assertRaises(ValueError):
                r.delivery_plan(Path("/nonexistent"), environment)

    def test_destination_reuse_requires_the_same_qualified_objects(self):
        image_id = "sha256:" + "c" * 64
        r.existing_member(None, image_id)
        r.existing_member({"config": {"digest": image_id}}, image_id)
        with self.assertRaises(ValueError):
            r.existing_member({"config": {"digest": "sha256:" + "d" * 64}}, image_id)
        members = {"amd64": "sha256:" + "1" * 64, "arm64": "sha256:" + "2" * 64}
        index = {"manifests": [{"platform": {"os": "linux", "architecture": arch}, "digest": digest} for arch, digest in members.items()]}
        r.existing_index(None, members)
        r.existing_index(index, members)
        changed = copy.deepcopy(index)
        changed["manifests"][1]["digest"] = "sha256:" + "3" * 64
        with self.assertRaises(ValueError):
            r.existing_index(changed, members)
        with self.assertRaises(ValueError):
            r.existing_index({"manifests": index["manifests"][:1]}, members)

    def release_fixture(self, directory):
        images = {}
        base = (r.ROOT / "VERSION").read_text().strip()
        build = {"version": base, "commit": SOURCE["commit"], "sourceState": "clean"}
        for product in c.PRODUCTS:
            for arch in ("amd64", "arm64"):
                name = product + "-" + arch
                target = directory / name
                target.mkdir()
                image_id = "sha256:" + str(len(images) + 1) * 64
                images[name] = image_id
                (target / "image.tar").write_bytes(b"archive")
                (target / "inventory.spdx.json").write_text("{}")
                (target / "scan-summary.json").write_text(json.dumps({"outcome": "passed", "imageId": image_id, "platform": "linux/" + arch}))
                value = {"schemaVersion": 1, "product": product, "platform": "linux/" + arch, "imageId": image_id,
                         "build": build, "source": SOURCE, "checks": dict.fromkeys(("inspection", "sbom", "scan"), "success"),
                         "files": c.inventory(target)}
                (target / "candidate.json").write_text(json.dumps(value))
        for language in a.LANGUAGES:
            target = directory / language
            target.mkdir()
            names = {"go": ["client-go.tar.gz"], "python": ["client.whl", "client.tar.gz"], "typescript": ["client.tgz"], "rust": ["client.crate"]}[language]
            artifacts = {}
            for name in names:
                (target / name).write_bytes(language.encode())
                artifacts[name] = c.digest(target / name)
            manifest = {"language": language, "releaseBase": (r.ROOT / "VERSION").read_text().strip(),
                        "openapiSha256": a.digest(r.ROOT / "api/openapi/control-v1.yaml"), "tools": {"compiler": "pinned"},
                        "sources": {"VERSION": a.digest(r.ROOT / "VERSION")}, "artifacts": artifacts}
            (target / "manifest.json").write_text(json.dumps(manifest))
            receipt = {"schemaVersion": 1, "language": language, "run": "fixture-run", **SOURCE,
                       "manifestSha256": c.digest(target / "manifest.json"), "result": "success"}
            (target / "qualification.json").write_text(json.dumps(receipt))
        target = directory / "charts"
        target.mkdir()
        charts = {}
        for name in ("open-agentic-catalog-0.1.0.tgz", "open-agentic-catalog-monitoring-0.1.0.tgz"):
            (target / name).write_bytes(b"chart")
            charts[name] = c.digest(target / name)
        (target / "qualification.json").write_text(json.dumps({"source": SOURCE, "result": "success", "files": charts}))
        for suite in r.SUITES:
            selected = {"backend": images["backend-amd64"]}
            if suite != "api":
                selected["ui"] = images["ui-amd64"]
            (directory / ("runtime-" + suite + ".json")).write_text(json.dumps({"suite": suite, "source": SOURCE, "result": "success", "images": selected}))
        for arch in ("amd64", "arm64"):
            target = directory / ("cli-" + arch)
            target.mkdir()
            name = "catalogctl-" + base + "-linux-" + arch + ".tar.gz"
            (target / name).write_bytes(b"cli")
            (target / "cli.json").write_text(json.dumps({"kind": "cli", "source": SOURCE, "build": build,
                "platform": "linux/" + arch, "imageId": images["backend-" + arch], "artifact": name,
                "sha256": c.digest(target / name), "releaseCandidate": True, "result": "success"}))
        target = directory / "editor"
        target.mkdir()
        name = "open-agentic-catalog-" + base + ".vsix"
        (target / name).write_bytes(b"vsix")
        (target / "editor.json").write_text(json.dumps({"kind": "editor", "source": SOURCE, "releaseBase": base,
            "artifact": name, "sha256": c.digest(target / name), "sdkSha256": c.digest(directory / "typescript/client.tgz"),
            "result": "success", "cleanup": "passed"}))
        return images

    def test_release_assembly_requires_complete_same_candidate_evidence(self):
        with tempfile.TemporaryDirectory() as temporary, patch.object(a, "source_identity", return_value=SOURCE):
            directory = Path(temporary) / "inputs"
            directory.mkdir()
            images = self.release_fixture(directory)
            results = dict.fromkeys(("source", "security", "clients", "charts", "candidates", "runtime"), "success")
            output = Path(temporary) / "complete"
            result = r.assemble(directory, results, output)
            self.assertEqual(result["images"], images)
            self.assertEqual(set(result["runtime"]), set(r.SUITES))
            self.assertTrue((output / "release-evidence.tar.gz").is_file())
            self.assertIn("charts/open-agentic-catalog-0.1.0.tgz", result["files"])
            self.assertIn("sdk/rust/client.crate", result["files"])
            (directory / "runtime-ui-oidc.json").unlink()
            with self.assertRaisesRegex(ValueError, "Missing runtime"):
                r.assemble(directory, results, Path(temporary) / "missing")
            self.assertFalse((Path(temporary) / "missing").exists())

    def test_failed_delivery_preserves_its_completed_subset_without_rebuilding(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            (directory / "release.json").write_text("{}")
            (directory / "release-evidence.tar.gz").write_bytes(b"evidence")
            value = {"source": SOURCE, "build": {"version": "0.1.0"}, "files": {},
                     "images": {product + "-" + arch: "sha256:" + "c" * 64 for product in c.PRODUCTS for arch in ("amd64", "arm64")}}
            calls = []

            def execute(args, **_kwargs):
                calls.append(args)
                if args[:3] == ["docker", "image", "push"]:
                    raise OSError("fixture push failure")

            with patch.object(r, "delivery_plan", return_value=value), patch.object(r, "existing_assets", return_value={"draft": True, "missing": []}), patch.object(r, "destination_commit", return_value=SOURCE["commit"]), patch.object(r, "registry_manifest", return_value=None), patch.object(r.subprocess, "run", side_effect=execute):
                with self.assertRaises(OSError):
                    r.publish(directory)
            record = json.loads((directory / "delivery-result.json").read_text())
            self.assertEqual(record["result"], "incomplete")
            self.assertEqual(record["completed"], ["load-backend-amd64", "tag-backend-amd64"])
            self.assertFalse(any("build" in args or "make" in args for args in calls))

    def test_sdk_staging_rejects_a_copy_that_changes_qualified_bytes(self):
        with tempfile.TemporaryDirectory() as temporary, patch.object(a, "source_identity", return_value=SOURCE):
            root = Path(temporary)
            self.sdk(root)
            a.qualify_sdk(root, "typescript", "run-1", SOURCE)
            copyfile = a.shutil.copyfile

            def changed_copy(source, destination):
                copyfile(source, destination)
                if destination.name == "client.tgz":
                    destination.write_bytes(b"changed during copy")

            output = root / "output/artifacts/qualified-sdk/run-1"
            with patch.object(a.shutil, "copyfile", side_effect=changed_copy), self.assertRaisesRegex(ValueError, "during staging"):
                a.stage_sdk(root, "typescript", "run-1", output)
            self.assertFalse(output.exists())

    def test_delivery_admission_accepts_an_exact_edition_and_refuses_tag_or_checksum_drift(self):
        with tempfile.TemporaryDirectory() as temporary, patch.object(a, "source_identity", return_value=SOURCE):
            directory = Path(temporary)
            (directory / "asset.tgz").write_bytes(b"qualified")
            value = {"result": "success", "source": SOURCE, "build": {"version": "0.1.0", "sourceState": "clean"},
                     "files": {"asset.tgz": c.digest(directory / "asset.tgz")}}
            (directory / "release.json").write_text(json.dumps(value))
            (directory / "release-evidence.tar.gz").write_bytes(b"evidence")
            checksums = r.public_checksums(directory, value)
            (directory / "SHA256SUMS").write_text(checksums)
            environment = {"GITHUB_EVENT_NAME": "push", "GITHUB_REPOSITORY": a.REPOSITORY, "RELEASE_PUBLISH_ENABLED": "true", "GITHUB_ACTIONS": "true", "RELEASE_PUBLISH": "true", "GITHUB_REF": "refs/tags/v0.1.0", "GITHUB_SHA": SOURCE["commit"]}
            self.assertEqual(r.delivery_plan(directory, environment), value)
            for key, wrong in (("GITHUB_REF", "refs/heads/main"), ("GITHUB_SHA", "d" * 40), ("ACT", "true")):
                with self.assertRaises(ValueError):
                    r.delivery_plan(directory, {**environment, key: wrong})
            (directory / "SHA256SUMS").write_text("substituted checksums")
            with self.assertRaisesRegex(ValueError, "checksums changed"):
                r.delivery_plan(directory, environment)
