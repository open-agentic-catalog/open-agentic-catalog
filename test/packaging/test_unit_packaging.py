import argparse
import json
import os
from pathlib import Path
import struct
import subprocess
import tempfile
import unittest
from unittest.mock import patch
import charts
import image
from packaging_test_helpers import IDENTIFIER, license_tree, settings


class ImageTests(unittest.TestCase):
    def test_cli_rejects_inputs_before_engine_use(self):
        for env in [{"PACKAGING_PLATFORM": "windows/amd64"}, {"PACKAGING_IMAGE": "--all"}, {"PACKAGING_VERSION": "bad\nversion"}, {"PACKAGING_SOURCE_STATE": "dirty"}]:
            with patch.dict(os.environ, env, clear=True), patch("sys.argv", ["image.py", "inspect"]), patch.object(image, "run") as run:
                with self.assertRaises(SystemExit):
                    image.main()
                run.assert_not_called()
    def test_sbom_failure_never_publishes_partial_output(self):
        with tempfile.TemporaryDirectory() as directory:
            def inspected(options, stage):
                license_tree(stage / "licenses")
                return {"imageId": IDENTIFIER}
            def scanned(command, **kwargs):
                self.assertEqual(command[command.index("--image-src") + 1], "docker")
                self.assertEqual(command[-1], IDENTIFIER)
                self.assertIn("--offline-scan", command)
                Path(command[command.index("--output") + 1]).write_text('{"spdxVersion":"SPDX-2.3","packages":[]}')
                return b""
            with patch.dict(os.environ, {"PACKAGING_OUTPUT": directory}, clear=True), patch("sys.argv", ["image.py", "sbom"]), patch.object(image, "inspect", side_effect=inspected), patch.object(image.trivy, "qualify"), patch.object(image.trivy, "execute", side_effect=scanned):
                with self.assertRaises(SystemExit):
                    image.main()
            self.assertEqual(list(Path(directory).iterdir()), [])
    def test_settings_exactness(self):
        self.assertEqual(image.image_settings(settings(), "linux/amd64", "dev", "unknown"), IDENTIFIER)
        for field, value in [("User", "0"), ("Entrypoint", ["/bin/sh"]), ("Cmd", ["all"]), ("StopSignal", "SIGKILL"), ("ExposedPorts", {})]:
            changed = settings()
            changed["Config"][field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                image.image_settings(changed, "linux/amd64", "dev", "unknown")
        for platform, version, commit in [("linux/arm64", "dev", "unknown"), ("linux/amd64", "wrong", "unknown"), ("linux/amd64", "dev", "wrong")]:
            with self.assertRaises(ValueError):
                image.image_settings(settings(), platform, version, commit)
    def test_source_state_label_must_agree_with_the_supplied_identity(self):
        value = settings()
        value["Config"]["Labels"]["io.open-agentic-catalog.source-state"] = "modified"
        self.assertEqual(image.image_settings(value,"linux/amd64","dev","unknown",source_state="modified"),IDENTIFIER)
        with self.assertRaises(ValueError):
            image.image_settings(value,"linux/amd64","dev","unknown")
        with self.assertRaises(ValueError):
            image.image_settings(value,"linux/amd64","dev","unknown",source_state="dirty")

    def test_failed_copy_always_cleans_owned_container(self):
        options = argparse.Namespace(engine="docker", go="go", image="local", platform="linux/amd64", version="dev", commit="unknown", action="inspect")
        with tempfile.TemporaryDirectory() as directory, patch.object(image, "run") as run:
            run.side_effect = [json.dumps([settings()]).encode(), ("b" * 64).encode(), OSError("copy failed"), b""]
            with self.assertRaises(OSError):
                image.inspect(options, Path(directory))
            self.assertEqual(run.call_args.args[0], ["docker", "container", "rm", "b" * 64])
            self.assertIn("--pull=never", run.call_args_list[1].args[0])
            self.assertEqual(run.call_args_list[1].args[0][-1], IDENTIFIER)
            self.assertFalse(any("build" in call.args[0] or "run" in call.args[0] for call in run.call_args_list))
    def test_license_completeness_and_permissions(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "licenses"
            license_tree(root)
            image.check_licenses(root)
            for mode in (0o700, 0o744):
                root.chmod(mode)
                with self.assertRaisesRegex(ValueError, "license root"):
                    image.check_licenses(root)
            root.chmod(0o755)
            (root / "go/LICENSE").unlink()
            with self.assertRaises(ValueError):
                image.check_licenses(root)
            license_tree(root)
            (root / "link").symlink_to(root / "go/LICENSE")
            with self.assertRaises(ValueError):
                image.check_licenses(root)
            (root / "link").unlink()
            (root / "go").chmod(0o744)
            with self.assertRaises(ValueError):
                image.check_licenses(root)
    def test_each_embedded_notice_is_required(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            license_tree(root)
            image.check_licenses(root)
            notices = [p for p in (root / "samples").rglob("*") if p.is_file()]
            notices.append(root / "upstream/example/LICENSE")
            for notice in notices:
                original = notice.read_bytes()
                for mutation in (None, b"", b" \n"):
                    with self.subTest(notice=notice, mutation=mutation):
                        if mutation is None:
                            notice.unlink()
                        else:
                            notice.write_bytes(mutation)
                        with self.assertRaises((ValueError, OSError)):
                            image.check_licenses(root)
                        notice.write_bytes(original)
            (root / "upstream/example/LICENSE").write_text("changed attribution")
            with self.assertRaisesRegex(ValueError, "notice mismatch"):
                image.check_licenses(root)

    def test_upstream_notice_manifest_rejects_invalid_paths_and_empty_sets(self):
        for entry in ("bad", "a" * 64 + "  ", "a" * 64 + "  ../outside", "a" * 64 + "  /outside", "a" * 64 + "  upstream/../outside", "a" * 64 + "  upstream/schema.json"):
            with self.subTest(entry=entry), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                license_tree(root)
                (root / "upstream/checksums.txt").write_text(entry + "\n")
                with self.assertRaises(ValueError):
                    image.check_licenses(root)

    def test_elf_validation_without_execution(self):
        # Minimal ELF fixture, no compiled or executable fixture is needed.
        raw = bytearray(256)
        raw[:6] = b"\x7fELF\x02\x01"
        struct.pack_into("<HHIQQQIHHHHHH", raw, 16, 2, 62, 1, 0, 64, 120, 0, 64, 56, 1, 64, 1, 0)
        struct.pack_into("<IIQQQQIIQQ", raw, 120, 0, 3, 0, 0, 184, 10, 0, 0, 0, 0)
        raw[184:194] = b"\0.shstrtab"
        metadata = "\tdep\tgolang.org/x/crypto/x509roots/fallback\tv1\n" + "".join("\tbuild\t" + s + "\n" for s in ["CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64", "GOAMD64=v1", "-trimpath=true", "-tags=netgo,osusergo"])
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "binary"
            path.write_bytes(raw)
            with patch.object(image, "run", side_effect=[metadata.encode(), b""]) as run:
                image.executable(path, "linux/amd64", ["go"])
                self.assertEqual(run.call_args_list[0].args[0][:3], ["go", "version", "-m"])
            with self.assertRaises(ValueError):
                image.executable(path, "linux/arm64", ["go"])
            struct.pack_into("<I", raw, 64, 3)
            path.write_bytes(raw)
            with self.assertRaisesRegex(ValueError, "dynamic"):
                image.executable(path, "linux/amd64", ["go"])
            path.write_bytes(b"truncated")
            with self.assertRaises(ValueError):
                image.executable(path, "linux/amd64", ["go"])
    def test_sbom_requires_scanned_dependencies_and_identifies_bundle(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            license_tree(root)
            document = dict(spdxVersion="SPDX-2.3", SPDXID="SPDXRef-DOCUMENT", packages=[dict(name=n) for n in ["usr/local/bin/catalogd", "usr/local/bin/catalogctl", "github.com/open-agentic-catalog/open-agentic-catalog", "github.com/jackc/pgx/v5"]])
            for bad in [{}, dict(spdxVersion="SPDX-2.3", packages=[dict(name="unrelated")])]:
                with self.assertRaises(ValueError):
                    image.supplement_sbom(bad, root, IDENTIFIER, "linux/amd64")
            image.supplement_sbom(document, root, IDENTIFIER, "linux/arm64")
            self.assertIn(IDENTIFIER, document["comment"])
            self.assertIn("linux/arm64", document["comment"])
            self.assertNotIn("documentComment", document)  # SPDX JSON uses comment, not the tag/value field spelling.
            self.assertTrue(document["packages"][-1]["versionInfo"].startswith("manifest-sha256:"))


class ChartTests(unittest.TestCase):
    def test_invalid_inputs_never_call_helm(self):
        with patch.object(charts.subprocess, "run") as run:
            for version in ["../bad", "01.2.3", "1.2", "1.2.3-01", "v1.2.3", "1.2.3+", "1.2.3\n"]:
                with self.assertRaises(ValueError):
                    charts.package("unused", version, "dev", ["helm"])
            with self.assertRaises(ValueError):
                charts.package("unused", "1.2.3", "bad\nvalue", ["helm"])
            run.assert_not_called()
    def test_failed_helm_leaves_no_partial_artifacts(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(charts.subprocess, "run", side_effect=subprocess.TimeoutExpired("helm", 60)):
            with self.assertRaises(subprocess.TimeoutExpired):
                charts.package(directory, "1.2.3", "dev", ["helm"])
            self.assertEqual(list(Path(directory).iterdir()), [])
