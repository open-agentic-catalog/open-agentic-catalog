"""Actual archive extraction and child-process admission in owned directories."""

import json
from pathlib import Path
import subprocess
import tempfile
import unittest

import release_packages as packages


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
