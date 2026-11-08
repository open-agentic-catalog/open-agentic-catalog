"""Rust launcher policy decisions without Cargo, processes, sockets or downloads."""

from datetime import datetime, timedelta, timezone
import io
import json
import os
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

import rust_runner as rust
from rust import generation


class RustRunnerUnits(unittest.TestCase):
    def test_native_llvm_coverage_requires_unique_selected_rows_and_valid_counts(self):
        sources = [Path("/fixture/lib.rs"), Path("/fixture/transport.rs")]
        rows = [{"filename": str(sources[0]), "summary": {"lines": {"count": 0, "covered": 0}}},
                {"filename": str(sources[1]), "summary": {"lines": {"count": 20, "covered": 15}}}]
        self.assertEqual(rust.coverage_totals(rows, sources), {"count":20, "covered":15, "percent":75.0})
        with self.assertRaisesRegex(RuntimeError, "duplicate"):
            rust.coverage_totals(rows + [rows[1]], sources)
        with self.assertRaisesRegex(RuntimeError, "unselected"):
            rust.coverage_totals(rows, sources[:1])
        with self.assertRaisesRegex(RuntimeError, "empty"):
            rust.coverage_totals(rows[:1], sources)
        for count, covered in ((1, 2), (-1, 0), (2, -1), (True, 0), (2, 1.5)):
            with self.assertRaisesRegex(RuntimeError, "invalid"):
                rust.coverage_totals([{"filename":str(sources[1]), "summary":{"lines":{"count":count,"covered":covered}}}], sources)

    def test_core_install_never_acquires_or_executes_the_analyzer(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            tools, project, source = root / "tools", root / "project", root / "generator"
            project.mkdir()
            source.mkdir()
            (project / "generator.lock").write_text("owned generator lock")
            built = tools / "build-tools/release/openapi-to-rust"
            built.parent.mkdir(parents=True)
            built.write_text("owned generator binary")
            go = root / "go/bin/go"
            go.parent.mkdir(parents=True)
            go.write_text("owned Go compiler")
            with (
                patch.object(rust, "ROOT", root),
                patch.object(rust, "TOOLS", tools),
                patch.object(rust, "PROJECT", project),
                patch.object(rust, "compiler_installed", return_value=True),
                patch.object(rust, "acquire_source", return_value=source) as acquire,
                patch.object(rust, "cargo") as cargo,
                patch.object(rust, "dependency_inventory", return_value=[]),
                patch.object(rust, "binary_paths", return_value=[tools / "bin/openapi-to-rust"]),
                patch.object(rust, "inputs", return_value={"core": "bound"}),
                patch.object(rust, "run", return_value=str(go.parent.parent)) as run,
                patch.object(rust, "policy", side_effect=AssertionError("analyzer invoked")),
                patch.object(rust, "tool_advisories", side_effect=AssertionError("analyzer invoked")),
                patch.object(rust, "verify_freshness", side_effect=AssertionError("audit required")),
            ):
                rust.install()
                acquire.assert_called_once_with("generator", rust.pins()["generator"])
                self.assertEqual(sum("fetch" in c.args for c in cargo.call_args_list), 4)
                self.assertNotIn("cargo-deny", str(cargo.call_args_list) + str(run.call_args_list))
                self.assertEqual(rust.installed()["dependencyAudit"], "not_run")
                self.assertFalse((tools / "bin/cargo-deny").exists())
                self.assertFalse((tools / "audit-installed.json").exists())

    def test_core_stamp_requires_real_identities_but_no_audit_state(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary, go = root / "openapi-to-rust", root / "go"
            binary.write_text("generator")
            go.write_text("compiler")
            stamp = {"schema": "rust-sdk-tools/v2", "inputs": {"core": "input"},
                     "binaries": {binary.name: rust.digest(binary)}, "go": str(go),
                     "goSha256": rust.digest(go), "dependencyAudit": "not_run"}
            with (patch.object(rust, "TOOLS", root),
                  patch.object(rust, "inputs", return_value=stamp["inputs"]),
                  patch.object(rust, "binary_paths", return_value=[binary]),
                  patch.object(rust, "verify_freshness", side_effect=AssertionError("audit required"))):
                target = root / "installed.json"
                target.write_text(json.dumps(stamp))
                self.assertEqual(rust.installed(), stamp)
                for altered in (dict(stamp, schema="old"), dict(stamp, dependencyAudit="passed"),
                                dict(stamp, inputs={}), dict(stamp, goSha256="changed"),
                                dict(stamp, binaries={})):
                    target.write_text(json.dumps(altered))
                    with self.assertRaisesRegex(RuntimeError, "sdk-tools-install"):
                        rust.installed()
                target.write_text(json.dumps(stamp))
                binary.write_text("changed executable")
                with self.assertRaisesRegex(RuntimeError, "sdk-tools-install"):
                    rust.installed()

    def test_audit_requires_its_own_matching_stamp_and_fresh_data(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "bin").mkdir()
            binary = root / "bin/cargo-deny"
            binary.write_text("scanner")
            policy = root / "generator-deny.toml"
            policy.write_text(rust.tool_policy_text())
            stamp = {"schema": "rust-sdk-audit/v1", "inputs": {"audit": "bound"},
                     "binarySha256": rust.digest(binary), "generatorPolicySha256": rust.digest(policy)}
            with (patch.object(rust, "TOOLS", root), patch.object(rust, "installed"),
                  patch.object(rust, "audit_inputs", return_value=stamp["inputs"]),
                  patch.object(rust, "verify_freshness") as freshness):
                target = root / "audit-installed.json"
                with self.assertRaisesRegex(RuntimeError, "sdk-rust-audit-tools-install"):
                    rust.audit_installed()
                target.write_text(json.dumps(stamp))
                self.assertEqual(rust.audit_installed(), stamp)
                freshness.assert_called_once_with(stamp)
                freshness.side_effect = RuntimeError("stale advisory snapshot")
                with self.assertRaisesRegex(RuntimeError, "sdk-rust-audit-tools-install"):
                    rust.audit_installed()
                freshness.side_effect = None
                binary.write_text("different scanner")
                with self.assertRaisesRegex(RuntimeError, "sdk-rust-audit-tools-install"):
                    rust.audit_installed()

    def test_audit_failure_cannot_be_reported_as_core_or_audit_success(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(rust, "ROOT", Path(directory)):
            def policy(manifest):
                if manifest == rust.SDK / "Cargo.toml":
                    raise RuntimeError("SDK finding")

            with (patch.object(rust, "audit_installed", return_value={"audit": "identity"}),
                  patch.object(rust, "policy", side_effect=policy) as scans,
                  patch.object(rust, "tool_advisories", side_effect=RuntimeError("tool finding")),
                  patch.object(rust, "qualify_refusals") as controls):
                with self.assertRaisesRegex(RuntimeError, "sdk, tools"):
                    rust.audit()
                report = json.loads((Path(directory) / "output/sdk/rust-audit.json").read_text())
                self.assertEqual(report["status"], "failed")
                self.assertEqual([x["scope"] for x in report["checks"] if x["status"] == "failed"], ["sdk", "tools"])
                self.assertEqual(scans.call_count, 3)
                controls.assert_called_once()

    def test_tool_audit_keeps_full_licenses_separate_from_executable_advisories(self):
        with (patch.object(rust, "policy") as policy,
              patch.object(rust, "run") as run,
              patch.object(rust, "environment", return_value={})):
            rust.tool_advisories()
            self.assertEqual(policy.call_count, 2)
            for name, license_call, advisory_call in zip(("generator", "deny-source"), policy.call_args_list, run.call_args_list, strict=True):
                self.assertEqual(license_call.args, (rust.TOOLS / name / "Cargo.toml",))
                self.assertEqual(license_call.kwargs, {"advisories": False})
                command = advisory_call.args[0]
                self.assertIn("--exclude-dev", command)
                self.assertEqual(command[-2:], ["check", "advisories"])
                self.assertIn(rust.TOOLS / name / "Cargo.toml", command)

    def test_audit_missing_tools_removes_previous_success_before_refusing(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(rust, "ROOT", Path(directory)):
            target = Path(directory) / "output/sdk/rust-audit.json"
            target.parent.mkdir(parents=True)
            target.write_text('{"status":"passed"}')
            with (patch.object(rust, "audit_installed", side_effect=RuntimeError("missing audit tools")),
                  patch.object(rust, "policy") as policy):
                with self.assertRaisesRegex(RuntimeError, "missing audit tools"):
                    rust.audit()
                self.assertFalse(target.exists())
                policy.assert_not_called()

    def test_generator_notice_binds_both_version_and_immutable_source(self):
        generator = {
            "version": "0.22.0",
            "repository": "gpu-cli/openapi-to-rust",
            "revision": "a9138670383497794eeabab40f73cb2b2f37b9c3",
        }
        notice = (
            "Generated bindings use openapi-to-rust 0.22.0 under the MIT License.\n"
            "Source: https://github.com/gpu-cli/openapi-to-rust/tree/a9138670383497794eeabab40f73cb2b2f37b9c3\n"
            "MIT License\n"
        )
        rust.verify_generator_notice(notice, generator)
        for invalid in (
            notice.replace("a9138670383497794eeabab40f73cb2b2f37b9c3", "e5fc983c02dae5a09bcb08ebdf5669f6ae1f624f"),
            notice.replace("0.22.0", "0.19.0"),
            notice.replace("Source:", "Unbound source:"),
            notice + "Source: https://github.com/gpu-cli/openapi-to-rust/tree/e5fc983c02dae5a09bcb08ebdf5669f6ae1f624f\n",
            notice + notice,
        ):
            with self.subTest(notice=invalid), self.assertRaisesRegex(RuntimeError, "notice version/source drift"):
                rust.verify_generator_notice(invalid, generator)

    def test_shared_union_repair_removes_only_an_empty_projection(self):
        spec = {"components": {"schemas": {}}}
        empty = "let mut variant_input = value;\n if let Some(object) = variant_input.as_object_mut() {}\n decode(variant_input)"
        self.assertEqual(generation.types_source(empty, spec), "let variant_input = value;\n decode(variant_input)")
        nonempty = 'let mut variant_input = value;\n if let Some(object) = variant_input.as_object_mut() { object.remove("field"); }'
        self.assertEqual(generation.types_source(nonempty, spec), nonempty)

    def test_rustdoc_formats_types_and_urls_without_changing_code(self):
        source = (
            '/// Codec for Option<Vec<u8>> and Vec<u8>.\n'
            '/// See http://purl.org/dc/terms/subject.\n'
            '/// Keep `Vec<u8>` and <https://example.com/reference>.\n'
            '/// Keep `https://example.com/code` as code.\n'
            'fn decode(value: Option<Vec<u8>>) {}\n'
            'const URL: &str = "https://example.com/data";\n'
        )
        expected = (
            '/// Codec for `Option<Vec<u8>>` and `Vec<u8>`.\n'
            '/// See <http://purl.org/dc/terms/subject>.\n'
            '/// Keep `Vec<u8>` and <https://example.com/reference>.\n'
            '/// Keep `https://example.com/code` as code.\n'
            'fn decode(value: Option<Vec<u8>>) {}\n'
            'const URL: &str = "https://example.com/data";\n'
        )
        spec = {"components": {"schemas": {}}}
        rendered = generation.types_source(source, spec)
        self.assertEqual(rendered, expected)
        self.assertEqual(generation.types_source(rendered, spec), expected)

    def test_package_rejects_a_stale_source_notice_before_cargo(self):
        notice = (
            "Generated bindings use openapi-to-rust 0.22.0 under the MIT License.\n"
            "Source: https://github.com/gpu-cli/openapi-to-rust/tree/e5fc983c02dae5a09bcb08ebdf5669f6ae1f624f\n"
        )
        with tempfile.TemporaryDirectory() as directory, patch.object(rust, "SDK", Path(directory)), patch.object(rust, "cargo") as cargo:
            (Path(directory) / "NOTICE").write_text(notice)
            with self.assertRaisesRegex(RuntimeError, "notice version/source drift"):
                rust.build_package(Path(directory))
            cargo.assert_not_called()

    def test_exact_complete_installed_compiler_needs_no_download(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(rust, "TOOLS", Path(directory)):
            version = rust.pins()["rust"]
            binary = rust.toolchain(version) / "rustc"
            binary.parent.mkdir(parents=True)
            binary.write_text("inert compiler fixture")
            component = binary.with_name("rustfmt")
            with patch.object(rust, "binary_paths", return_value=[binary, component]), patch.object(rust, "run") as run:
                self.assertFalse(rust.compiler_installed(version))
                run.assert_not_called()
                component.write_text("inert component fixture")
                run.return_value = "rustc " + version + " (owned fixture)\n"
                self.assertTrue(rust.compiler_installed(version))
                self.assertEqual(run.call_args.args[0], [binary, "--version"])
                run.return_value = "rustc 1.88.0 (wrong selection)\n"
                self.assertFalse(rust.compiler_installed(version))

    def test_security_patch_admission_is_exact_and_keeps_other_checks(self):
        config = rust.pins()
        self.assertEqual(
            {(fix["name"], fix["version"]) for fix in config["securityFixes"]},
            {("rustls", "0.23.46"), ("rustls-webpki", "0.103.16"), ("webpki-root-certs", "1.0.10"), ("webpki-roots", "1.0.10"), ("tokio", "1.53.3"),
             ("zstd", "0.13.4"), ("zstd-safe", "7.4.0"), ("zstd-sys", "2.1.2+zstd.1.5.7")},
        )
        for fix in config["securityFixes"]:
            valid = {"pubtime": fix["released"], "cksum": fix["checksum"], "yanked": False}
            with self.subTest(accepted=(fix["name"], fix["version"])):
                self.assertTrue(rust.release_allowed(fix["name"], fix["version"], valid))
            for name, version, entry in (
                ("other", fix["version"], valid),
                (fix["name"], "999.0.0", valid),
                (fix["name"], fix["version"], dict(valid, cksum="changed")),
                (fix["name"], fix["version"], dict(valid, pubtime="2026-10-23T00:00:00Z")),
                (fix["name"], fix["version"], dict(valid, yanked=True)),
                (fix["name"], fix["version"], dict(valid, pubtime=None)),
            ):
                with self.subTest(name=name, version=version, entry=entry):
                    self.assertFalse(rust.release_allowed(name, version, entry))
        older = {"pubtime": config["dependencyCutoff"], "cksum": "unrelated", "yanked": False}
        self.assertTrue(rust.release_allowed("other", "1.0.0", older))
        self.assertFalse(rust.release_allowed("other", "1.0.0", dict(older, yanked=True)))

    def test_environment_rejects_ambient_tool_and_registry_redirectors(self):
        poison = {
            "CARGO_HOME": "/personal",
            "CARGO_REGISTRIES_CRATES_IO_INDEX": "elsewhere",
            "RUSTC_WRAPPER": "remote",
            "RUSTFLAGS": "secret",
            "RUSTUP_TOOLCHAIN": "nightly",
            "LLVM_PROFILE_FILE": "/unowned",
            "LD_PRELOAD": "bad",
            "GIT_DIR": "/other",
            "PATH": "/usr/bin",
        }
        with patch.dict(os.environ, poison, clear=True):
            env = rust.environment()
        self.assertNotIn("RUSTC_WRAPPER", env)
        self.assertNotIn("RUSTFLAGS", env)
        self.assertNotIn("CARGO_REGISTRIES_CRATES_IO_INDEX", env)
        self.assertNotIn("LLVM_PROFILE_FILE", env)
        self.assertNotIn("LD_PRELOAD", env)
        self.assertNotIn("GIT_DIR", env)
        self.assertEqual(env["CARGO_NET_OFFLINE"], "true")
        self.assertEqual(env["CARGO_LLVM_COV_SETUP"], "no")
        self.assertTrue(env["RUSTC"].startswith(str(rust.TOOLS)))

    def test_manifest_precedes_forwarded_clippy_arguments(self):
        with patch.object(rust, "run", return_value="ok") as run:
            rust.cargo(
                rust.pins()["rust"], "clippy", "--frozen", "--", "-D", "warnings"
            )
        args = run.call_args.args[0]
        self.assertLess(args.index("--manifest-path"), args.index("--"))
        self.assertIn("--frozen", args)

    def test_owned_outputs_accept_temporary_roots_and_reject_symlinks_and_mounts(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(rust, "ROOT", Path(directory)):
            root = Path(directory)
            rust.owned_directory(root / "archive")
            self.assertTrue((root / "archive").is_dir())
            (root / "redirect").symlink_to(root / "archive", target_is_directory=True)
            with self.assertRaisesRegex(RuntimeError, "Redirected"):
                rust.owned_directory(root / "redirect/child")
            self.assertFalse((root / "archive/child").exists())
            with patch.object(Path, "is_mount", autospec=True,
                              side_effect=lambda path: path in (root, root / "mounted")):
                rust.owned_directory(root / "accepted")
                self.assertTrue((root / "accepted").is_dir())
                with self.assertRaisesRegex(RuntimeError, "Redirected"):
                    rust.owned_directory(root / "mounted/child")
            self.assertFalse((root / "mounted").exists())

    def test_archive_refuses_links_traversal_and_nonfiles(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(rust, "ROOT", Path(directory)):
            root = Path(directory)
            for name, kind in (
                ("../outside", tarfile.REGTYPE),
                ("/absolute", tarfile.REGTYPE),
                ("link", tarfile.SYMTYPE),
                ("hardlink", tarfile.LNKTYPE),
                ("device", tarfile.CHRTYPE),
            ):
                archive = root / "input.tar"
                with tarfile.open(archive, "w") as output:
                    member = tarfile.TarInfo(name)
                    member.type = kind
                    output.addfile(member, io.BytesIO())
                with (
                    self.subTest(name=name),
                    self.assertRaisesRegex(RuntimeError, "Unsafe"),
                ):
                    rust.extract(archive, root / "out")
            with tarfile.open(root / "input.tar", "w") as output:
                member = tarfile.TarInfo("package/src/lib.rs")
                member.size = 2
                output.addfile(member, io.BytesIO(b"ok"))
            rust.extract(root / "input.tar", root / "valid", strip_root=True)
            self.assertEqual((root / "valid/src/lib.rs").read_bytes(), b"ok")

    def test_advisory_freshness_missing_changed_and_dirty_controls(self):
        now = datetime(2026, 10, 13, tzinfo=timezone.utc)
        with (
            tempfile.TemporaryDirectory() as directory,
            patch.object(rust, "ADVISORIES", Path(directory)),
        ):
            repo = Path(directory) / rust.pins()["advisoryDirectory"]
            (repo / ".git").mkdir(parents=True)
            (repo / ".git/HEAD").write_text("owned")
            stamp = {"advisory": {"revision": "a" * 40, "fetchedAt": now.isoformat()}}
            with patch.object(rust, "run", side_effect=["a" * 40, " "]):
                rust.verify_freshness(stamp, now)
            for delta in (timedelta(days=-8), timedelta(minutes=6)):
                stamp["advisory"]["fetchedAt"] = (now + delta).isoformat()
                with (
                    patch.object(rust, "run") as run,
                    self.assertRaisesRegex(RuntimeError, "stale|future"),
                ):
                    rust.verify_freshness(stamp, now)
                run.assert_not_called()
            stamp["advisory"]["fetchedAt"] = now.isoformat()
            for replies, message in (
                (["b" * 40], "revision"),
                (["a" * 40, " M crates/example.md"], "local changes"),
            ):
                with (
                    patch.object(rust, "run", side_effect=replies),
                    self.assertRaisesRegex(RuntimeError, message),
                ):
                    rust.verify_freshness(stamp, now)
            (repo / ".git/HEAD").unlink()
            with self.assertRaisesRegex(RuntimeError, "missing"):
                rust.verify_freshness(stamp, now)

    def test_current_contract_operation_methods_paths_and_managed_calls(self):
        expected = generation.contract_operations(
            (rust.ROOT / "api/openapi/control-v1.yaml").read_text()
        )
        operations = json.loads(
            (rust.SDK / "src/generated/operations.json").read_text()
        )
        self.assertEqual(set(expected), {o["operation_id"] for o in operations})
        for operation in operations:
            self.assertEqual(
                expected[operation["operation_id"]],
                (operation["source_method"], operation["source_path"]),
            )
        source = (rust.SDK / "src/generated/client.rs").read_text()
        self.assertEqual(source.count("self.send(req).await?"), len(operations))
        for forbidden in (
            "req.send()",
            "reqwest::Client::new",
            "reqwest_middleware",
            "reqwest_retry",
            "bytes_stream()",
            "serde_json::from_str",
        ):
            self.assertNotIn(forbidden, source)
        with self.assertRaises(RuntimeError):
            generation.contract_operations("paths: {}")
        with self.assertRaises(RuntimeError):
            generation.client_source("changed upstream template", {"symbols": []})

    @staticmethod
    def raw_client(types=("Owned",)):
        operations, symbols = [], []
        for index, result_type in enumerate(types):
            name = "owned_" + str(index)
            symbols.append({"name": name, "operation": {
                "consumption": "buffered", "response_statuses": ["200"],
                "response_excluded_statuses": [],
            }})
            operations.append(
                "    pub async fn " + name + "(&self) -> Result<" + result_type + ", ApiOpError<OwnedError>> {\n"
                '        let mut req = self.http.get("owned");\n'
                "        if let Some(api_key) = &self.api_key {\n"
                "            req = req.bearer_auth(api_key);\n"
                "        }\n"
                "        for (name, value) in &self.custom_headers {\n"
                "            if name.is_empty() {\n"
                "                continue;\n"
                "            }\n"
                "            req = req.header(name, value);\n"
                "        }\n"
                "        let response = req.send().await?;\n"
                "        let body = __read_bounded_response_body(response, self.max_response_body_bytes,).await?;\n"
                "        serde_json::from_str(&body_text)\n"
                "        Ok(())\n"
                "    }\n"
            )
        return (
            "fn __pct_encode_path_segment(s: &str) -> String { s.into() }\n"
            "impl HttpClient {\n" + "".join(operations) + "}\n",
            {"symbols": symbols},
        )

    def test_client_repair_preserves_exact_valid_output(self):
        source, bindings = self.raw_client()
        expected = '''//! Generated operations from the complete Control OpenAPI.
//! Regenerate with make sdk-generate SDK_LANGUAGE=rust; do not edit.
#![allow(clippy::format_in_format_args, clippy::let_unit_value, clippy::too_many_arguments)]
use super::types::*;
use crate::{Client, Error as HttpError, ApiError, ApiOpError, Response};
fn __pct_encode_path_segment(s: &str) -> String { s.into() }
impl Client {
    pub async fn owned_0(&self) -> Result<Response<Owned>, ApiOpError<OwnedError>> {
        let req = self.http.get("owned");
        let response = self.send(req).await?;
        let body = response.body.clone();
        serde_json::from_slice(&raw_body)
        Ok(Response::new((), status_code, headers, raw_body))
    }
}
'''
        self.assertEqual(generation.client_source(source, bindings), expected)
        # The pinned template permits horizontal padding and a trailing comma.
        padded = source.replace(
            "Result<Owned, ApiOpError<OwnedError>>",
            "Result< \tOwned, \tApiOpError<OwnedError> \t, \t>",
        )
        self.assertEqual(generation.client_source(padded, bindings), expected)
        multiline = source.replace(
            "Result<Owned, ApiOpError<OwnedError>>",
            "Result<\n            Owned,\n            ApiOpError<OwnedError>,\n        >",
        )
        self.assertEqual(generation.client_source(multiline, bindings), expected)
        header = "        for (name, value) in &self.custom_headers {\n"
        nested = source.replace(header, header + "    " + header)
        self.assertEqual(generation.client_source(nested, bindings), expected)

    def test_client_repair_keeps_every_operation_and_managed_transport(self):
        types = ("Owned", "Vec<Owned>", "Option<Owned>", "()", "bytes::Bytes")
        source, bindings = self.raw_client(types)
        rendered = generation.client_source(source, bindings)
        self.assertEqual(rendered.count("pub async fn "), len(types))
        self.assertEqual(rendered.count("self.send(req).await?"), len(types))
        for index, result_type in enumerate(types):
            self.assertIn(
                "pub async fn owned_" + str(index) + "(&self) -> Result<Response<" + result_type + ">, ApiOpError<OwnedError>>",
                rendered,
            )
        for forbidden in ("self.api_key", "self.custom_headers", "req.send()", "__read_bounded", "serde_json::from_str"):
            self.assertNotIn(forbidden, rendered)

    def test_client_repair_refuses_changed_header_and_signature_layouts(self):
        source, bindings = self.raw_client()
        header = "        for (name, value) in &self.custom_headers {\n"
        for name, changed, rejection in (
            ("missing header end", source.replace("        }\n        let response", "                x\n" * 12 + "        let response"), "identity boilerplate"),
            ("wrong body indent", source.replace("            if name", "          if name"), "identity boilerplate"),
            ("extra header", source.replace("        let response", header + "        }\n        let response"), "identity boilerplate"),
            ("nested unterminated headers", source.replace(header, header + ("    " + header) * 12).replace("        }\n        let response", "        let response"), "identity boilerplate"),
            ("missing comma", source.replace("Owned, ApiOpError", " " * 4096 + "Owned ApiOpError"), "result signature"),
            ("repeated result opener", source.replace("Result<Owned, ApiOpError<OwnedError>> {", "Result<" + ") -> Result<" * 12 + "Owned ApiOpError<OwnedError>> {"), "result signature"),
            ("missing closing type", source.replace("OwnedError>> {", "OwnedError>" + " " * 4096 + " {"), "result signature"),
            ("type crosses body brace", source.replace("Result<Owned", "Result<Owned {\n"), "result signature"),
        ):
            with self.subTest(layout=name), self.assertRaisesRegex(RuntimeError, rejection):
                generation.client_source(changed, bindings)

    def test_client_repair_retains_identity_count_inventory_and_survivor_guards(self):
        source, bindings = self.raw_client()
        for name, changed, rejection in (
            ("missing auth", source.replace("&self.api_key", "&different_key"), "identity boilerplate"),
            ("missing send", source.replace("req.send()", "other.send()"), "send boundary"),
            ("wrong operation", source.replace("fn owned_0", "fn unbound"), "operation inventory"),
            ("unmanaged survivor", source + "// reqwest_retry\n", "Unmanaged Rust transport"),
        ):
            with self.subTest(layout=name), self.assertRaisesRegex(RuntimeError, rejection):
                generation.client_source(changed, bindings)

    def test_client_repair_keeps_request_mutability_with_its_own_method(self):
        source, bindings = self.raw_client(("Owned", "Owned"))
        first, second = source.split("    pub async fn owned_1", 1)
        blank_lines = "        \n" * 64
        first = first.replace("        let response", blank_lines + "        let response")
        second = second.replace(
            "        let response",
            blank_lines + "        req = req.query(&query);\n        let response",
        )
        rendered = generation.client_source(first + "    pub async fn owned_1" + second, bindings)
        immutable, mutable = rendered.split("    pub async fn owned_1", 1)
        self.assertIn('let req = self.http.get("owned");', immutable)
        self.assertNotIn("let mut req =", immutable)
        self.assertIn('let mut req = self.http.get("owned");', mutable)
        self.assertIn("req = req.query(&query);", mutable)
        self.assertEqual(rendered.count(blank_lines), 2)

    def test_client_repair_refuses_changed_ends_without_borrowing_another_method(self):
        source, bindings = self.raw_client(("Owned", "Owned"))
        end = "\n    }\n"
        for name, changed in (
            ("first missing", source.replace(end, "\n    // missing end\n", 1)),
            ("last missing", "\n    // missing end\n".join(source.rsplit(end, 1))),
            ("wrong indentation", source.replace(end, "\n        }\n", 1)),
            ("CRLF closing", source.replace(end, "\n    }\r\n", 1)),
            ("trailing closing comment", source.replace(end, "\n    } // changed\n", 1)),
            ("extra closing", source.replace(end, end + "    }\n", 1)),
        ):
            with self.subTest(layout=name), self.assertRaisesRegex(RuntimeError, "method boundary"):
                generation.client_source(changed, bindings)
        # Inventory refusal precedes scanning even a malformed first method.
        extra = source.replace(end, "\n    // missing end\n", 1)
        extra += "    pub async fn unbound(&self) -> Result<Owned, ApiOpError<OwnedError>> {\n"
        with self.assertRaisesRegex(RuntimeError, "operation inventory"):
            generation.client_source(extra, bindings)

    def test_streaming_removal_uses_only_its_exact_validated_method(self):
        source, bindings = self.raw_client(("Owned", "Owned"))
        bindings["symbols"][0]["operation"]["consumption"] = "streaming"
        rendered = generation.client_source(source, bindings)
        self.assertNotIn("pub async fn owned_0", rendered)
        self.assertIn("pub async fn owned_1", rendered)
        self.assertEqual(rendered.count("self.send(req).await?"), 1)
        self.assertNotIn("self.api_key", rendered)
        self.assertNotIn("self.custom_headers", rendered)
        for parameters in (
            "<__Param0: AsRef<str>>",
            "<\n        __Param0: AsRef<str>,\n        __Param1: AsRef<str>,\n    >",
        ):
            generic = source.replace("fn owned_0(", "fn owned_0" + parameters + "(")
            with self.subTest(generic_parameters=parameters):
                self.assertEqual(generation.client_source(generic, bindings), rendered)
        for streaming_index in (0, 1):
            source, bindings = self.raw_client(("Owned", "Owned"))
            bindings["symbols"][streaming_index]["operation"]["consumption"] = "streaming"
            source = source.replace("\n    }\n", "\n    // missing end\n", 1)
            with self.subTest(streaming_index=streaming_index), self.assertRaisesRegex(RuntimeError, "method boundary"):
                generation.client_source(source, bindings)

    def test_license_policy_includes_tests_builds_and_unpublished_crates(self):
        import tomllib

        policy = tomllib.loads((rust.PROJECT / "deny.toml").read_text())
        self.assertTrue(policy["licenses"]["include-dev"])
        self.assertTrue(policy["licenses"]["include-build"])
        self.assertFalse(policy["licenses"]["private"]["ignore"])
        self.assertNotIn("MPL-2.0", policy["licenses"]["allow"])
        self.assertEqual(policy["advisories"]["maximum-db-staleness"], "P7D")

    def test_missing_compiler_or_llvm_component_requires_explicit_installation(self):
        with (
            tempfile.TemporaryDirectory() as directory,
            patch.object(rust, "TOOLS", Path(directory)),
        ):
            (Path(directory) / "installed.json").write_text("{}")
            for name in ("rustc", "llvm-cov"):
                with (
                    self.subTest(name=name),
                    patch.object(
                        rust, "binary_paths", return_value=[Path(directory) / name]
                    ),
                    patch.object(rust, "run") as run,
                ):
                    with self.assertRaisesRegex(RuntimeError, "sdk-tools-install"):
                        rust.installed()
                    run.assert_not_called()

    def test_package_input_inventory_excludes_target_cache_and_binds_qualification(
        self,
    ):
        with (
            tempfile.TemporaryDirectory() as directory,
            patch.object(rust, "SDK", Path(directory)),
        ):
            root = Path(directory)
            for name in (
                "src/lib.rs",
                "examples/discover.rs",
                "target/unowned/generated.rs",
            ):
                path = root / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("// fixture")
            selected = rust.source_files()
            self.assertIn(root / "src/lib.rs", selected)
            self.assertIn(root / "examples/discover.rs", selected)
            self.assertNotIn(root / "target/unowned/generated.rs", selected)
            self.assertIn(rust.PROJECT / "consumer/src/main.rs", selected)
            self.assertIn(rust.HERE / "rust_consumer_functional_test.go.txt", selected)
            self.assertIn(rust.ROOT / "api/examples/ard-manifest.json", selected)


if __name__ == "__main__":
    unittest.main()
