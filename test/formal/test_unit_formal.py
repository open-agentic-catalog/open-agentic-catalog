"""Isolated source-binding and proof-evidence checks; never starts the verifier."""

import json
from zipfile import ZipFile
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import run as formal
from common import Result


class FormalUnitTests(unittest.TestCase):
    def setUp(self):
        self.pin = json.loads((formal.ROOT / formal.AREA / "tool.json").read_text())
        self.source = (formal.ROOT / formal.SOURCE).read_bytes()
        self.contract = (formal.ROOT / formal.AREA / "contract.txt").read_bytes()

    def test_projection_preserves_body_and_binds_caller(self):
        proof = formal.projection(self.source, self.contract, self.pin)
        projected = formal.function(proof, "backoffCeiling")
        restored = "\n".join(line for line in projected.splitlines() if not line.strip().startswith("//@"))
        self.assertEqual(restored.replace(formal.NAMED, formal.SIGNATURE),
                         formal.function(self.source.decode(), "backoffCeiling"))
        self.assertEqual(set(formal.controls(proof)), {"valid", "false-specification", "broken-arithmetic", "nondecreasing-measure", "calibration-missing-contract", "calibration-vacuous", "calibration-unsupported", "calibration-overflow"})
        changed = self.source.replace(b"delay *= 3", b"delay *= 4")
        self.assertIn("delay *= 4", formal.projection(changed, self.contract, self.pin))
        for source in (self.source.replace(b"base := min(backoffCeiling(attempt), ceiling, 24*time.Hour-hint)", b"base := time.Hour"),
                       self.source.replace(b"func backoffCeiling", b"func other"),
                       self.source + b"\n" + formal.function(self.source.decode(), "backoffCeiling").encode(),
                       self.source.replace(b"for attempt > 1 {", b"for attempt > 2 {"),
                       self.source.replace(b"delay *= 3", b"/*@ assume false @*/ delay *= 3")):
            with self.subTest(source=source[-50:]), self.assertRaises(ValueError):
                formal.projection(source, self.contract, self.pin)

    def test_missing_changed_and_vacuous_contracts_are_rejected(self):
        for contract in (b"", self.contract.replace(b"ensures", b"requires", 1),
                         b"//@ requires attempt > 0 && attempt < 0\n" + self.contract,
                         self.contract.replace(b"time.Hour-delay", b"0")):
            with self.subTest(contract=contract), self.assertRaisesRegex(ValueError, "contract drift"):
                formal.projection(self.source, contract, self.pin)
        # Updating the checksum cannot silently add preconditions or assumptions.
        contract = self.contract.replace(b"//@ ensures", b"//@ requires", 1)
        with self.assertRaises(ValueError):
            formal.projection(self.source, contract, dict(self.pin, contractSHA256=formal.digest(contract)))

    def test_complete_success_and_specific_failure_evidence(self):
        header = self.pin["gobraCommit"] + "@HEAD\nVerifying package .. - proof\n"
        def result(code, message):
            return Result(code, header + message, "")
        good = result(0, "Gobra found 0 errors.\n")
        formal.validate(good, None, self.pin["gobraCommit"])
        bad = result(1, "Error at: x Postcondition might not hold\nGobra found 1 error.\n")
        formal.validate(bad, "Postcondition might not hold", self.pin["gobraCommit"])
        for evidence in (result(0, ""), result(0, "Gobra found 0 errors.\n" * 2),
                         result(0, "The verification of package .. - jobs timed out after 1 millisecond.\n"),
                         result(1, "Gobra found 0 errors."), result(-15, "Gobra found 0 errors."), bad):
            with self.assertRaises(ValueError):
                formal.validate(evidence, None, self.pin["gobraCommit"])
        for evidence in (good, result(1, "Error at: x parse error\nGobra found 1 error."),
                         result(1, "Postcondition might not hold"),
                         result(-15, "Error at: x Postcondition might not hold\nGobra found 1 error.")):
            with self.assertRaises(ValueError):
                formal.validate(evidence, "Postcondition might not hold", self.pin["gobraCommit"])

    def test_installer_checks_archive_and_member_hashes_before_publication(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            area = root / formal.AREA
            area.mkdir(parents=True)
            archive = root / "fixture.zip"
            files = {"server/server.jar": b"jar", "z3/bin/z3": b"executable", "z3/LICENSE.txt": b"license"}
            with ZipFile(archive, "w") as bundle:
                for name, raw in files.items():
                    bundle.writestr(name, raw)
            pin = dict(self.pin, archiveSHA256=formal.digest(archive.read_bytes()),
                       files={name: formal.digest(raw) for name, raw in files.items()})
            config = area / "tool.json"
            def acquire(argv, **_kwargs):
                self.assertEqual(argv[:3], ["gh", "release", "download"])
                (Path(argv[-1]) / "GobraToolsLinux.zip").write_bytes(archive.read_bytes())
            with patch.object(formal, "run", acquire), patch.object(formal, "verify_tools") as verify:
                for bad in (dict(pin, archiveSHA256="0" * 64), dict(pin, files=dict(pin["files"], **{"server/server.jar": "0" * 64}))):
                    config.write_text(json.dumps(bad))
                    with self.assertRaisesRegex(ValueError, "checksum"):
                        formal.install(root)
                    self.assertFalse((root / formal.TOOLS).exists())
                    self.assertFalse(list(area.glob("install-*")))
                config.write_text(json.dumps(pin))
                formal.install(root)
                self.assertEqual((root / formal.TOOLS / "server/server.jar").read_bytes(), b"jar")
                verify.assert_called_once()
                with patch.object(formal, "run", side_effect=AssertionError("must not download")):
                    formal.install(root)

    def test_tool_inventory_cannot_omit_checks_or_extract_other_paths(self):
        files = self.pin["files"]
        self.assertEqual(formal.tool_files(self.pin), files)
        for invalid in (None, {}, list(files), {k: v for k, v in files.items() if k != "server/server.jar"},
                        dict(files, **{"../unexpected": "0" * 64}),
                        dict(files, **{"server/server.jar": ""}),
                        dict(files, **{"server/server.jar": 42})):
            with self.subTest(invalid=invalid), patch.object(formal, "run", side_effect=AssertionError("must not execute")), \
                    self.assertRaisesRegex(ValueError, "formal tool"):
                formal.verify_tools(formal.ROOT, dict(self.pin, files=invalid), {})

    def test_environment_host_and_bounded_inputs(self):
        with patch.dict(formal.os.environ, JAVA_TOOL_OPTIONS="-bad", JDK_JAVA_OPTIONS="-bad", _JAVA_OPTIONS="-bad", Z3_EXE="wrong"):
            self.assertFalse(set(formal.environment()) & {"JAVA_TOOL_OPTIONS", "JDK_JAVA_OPTIONS", "_JAVA_OPTIONS", "Z3_EXE"})
        with patch.object(formal.platform, "machine", return_value="aarch64"), self.assertRaises(ValueError):
            formal.host()
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory).resolve() / "input"
            with self.assertRaises(ValueError):
                formal.read(path)
            path.write_bytes(b"123")
            self.assertEqual(formal.read(path, 3), b"123")
            with self.assertRaises(ValueError):
                formal.read(path, 2)
            link = Path(directory).resolve() / "link"
            link.symlink_to(path)
            with self.assertRaises(ValueError):
                formal.read(link)


if __name__ == "__main__":
    unittest.main()
