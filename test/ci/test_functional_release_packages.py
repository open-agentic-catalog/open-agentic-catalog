"""Actual archive extraction and child-process admission in owned directories."""

import json
import os
import platform
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import admission
import candidate
import release_packages as packages
import release_standalone as standalone


class NativeArchiveConsumerTests(unittest.TestCase):
    def test_new_archive_consumer_binds_payload_build_identity_and_exit(self):
        build = {"version": "0.1.0-rc.1", "commit": "a" * 40, "sourceState": "modified"}
        for fault in (None, "identity", "version-exit", "help-exit"):
            with self.subTest(fault=fault), tempfile.TemporaryDirectory(prefix="catalog-cli-consumer-") as temporary:
                root = Path(temporary)
                inspected = root / "inspected"
                (inspected / "licenses/project").mkdir(parents=True)
                notices = b"Exact archive consumer notice\n"
                (inspected / "licenses/project/LICENSE").write_bytes(notices)
                reported = {**build, **({"commit": "b" * 40} if fault == "identity" else {})}
                # A local executable fixture exercises the actual process
                # boundary; packaged image qualification uses real catalogctl.
                program = "#!/bin/sh\ncase \"$1\" in\nversion) "
                program += "exit 8" if fault == "version-exit" else "printf '%s\\n' '" + json.dumps(reported) + "'"
                program += ";;\n--help) " + ("exit 9" if fault == "help-exit" else "printf '%s\\n' 'Catalog command help'") + ";;\n*) exit 10;;\nesac\n"
                (inspected / "catalogctl").write_text(program)
                output = root / "archive"
                output.mkdir()
                archive = packages.archive_cli(inspected, output, build["version"], "linux/amd64")
                consumer = root / "consumer"
                executable = packages.unpack_cli(archive, inspected, consumer)
                self.assertEqual(executable.read_bytes(), program.encode())
                self.assertEqual((consumer / "licenses/project/LICENSE").read_bytes(), notices)
                if fault == "identity":
                    with self.assertRaisesRegex(ValueError, "Unpacked CLI version"):
                        packages.smoke_cli(executable, build)
                elif fault:
                    with self.assertRaises(subprocess.CalledProcessError):
                        packages.smoke_cli(executable, build)
                else:
                    packages.smoke_cli(executable, build)

    def test_native_check_refuses_wrong_discovery_entry_without_a_receipt(self):
        arch = {"x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64"}.get(platform.machine())
        target = f"{platform.system().lower()}/{arch}"
        if target not in standalone.PLATFORMS:
            self.skipTest("native release packages require a supported target")
        with tempfile.TemporaryDirectory(prefix="catalog-native-discovery-") as temporary:
            root = Path(temporary)
            (root / "licenses/project").mkdir(parents=True)
            (root / "licenses/project/LICENSE").write_text("Fixture notice")
            executable = root / "catalogctl"
            subprocess.run(["go", "build", "-o", executable, "./test/ci/testdata/discovery-cli"],
                           cwd=admission.ROOT, env={**os.environ, "GOOS": target.split("/")[0], "GOARCH": arch, "CGO_ENABLED": "0"},
                           check=True, timeout=90)
            source = admission.source_identity(admission.ROOT)
            build = {"version": "0.1.0", "commit": source["commit"], "sourceState": "modified"}
            archive = packages.archive_cli(root, root, build["version"], target)
            common = {"source": source, "build": build, "imageId": "sha256:" + "a" * 64}
            (root / "cli.json").write_text(json.dumps({**common, "artifact": archive.name, "sha256": candidate.digest(archive)}))
            (root / "standalone.json").write_text(json.dumps({**common, "files": {}}))
            receipt = root / "native-result.json"
            with patch.dict(os.environ, FIXTURE_CLI_BUILD=json.dumps(build), FIXTURE_DISCOVERY_IDENTIFIER=""):
                standalone.smoke_discovery(executable)
                with patch.dict(os.environ, FIXTURE_DISCOVERY_IDENTIFIER="urn:air:agent:fixture:other"):
                    result = subprocess.run([sys.executable, "-B", str(admission.ROOT / "test/ci/release_standalone.py"),
                                             "--directory", root, "--platform", target, "--output", receipt],
                                            capture_output=True, text=True, timeout=30)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("Packaged CLI discovery entry differs from the selected peer", result.stderr)
            self.assertFalse(receipt.exists())
