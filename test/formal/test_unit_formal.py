"""Isolated source-binding and proof-evidence checks; never starts the verifier."""

import hashlib
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
        with tempfile.TemporaryDirectory() as directory, patch.object(formal, "host"):
            root = Path(directory).resolve()
            area = root / formal.AREA
            area.mkdir(parents=True)
            archive = root / "fixture.zip"
            files = {"server/server.jar": b"jar", "z3/bin/z3": b"executable", "z3/LICENSE.txt": b"license"}
            with ZipFile(archive, "w") as bundle:
                for name, raw in files.items():
                    bundle.writestr(name, raw)
            pin = dict(self.pin, archiveSHA256=hashlib.sha256(archive.read_bytes()).hexdigest(),
                       files={name: hashlib.sha256(raw).hexdigest() for name, raw in files.items()})
            config = area / "tool.json"
            def acquire(argv, **_kwargs):
                self.assertEqual(argv[:3], ["gh", "release", "download"])
                (Path(argv[-1]) / "GobraToolsLinux.zip").write_bytes(archive.read_bytes())
            with patch.object(formal, "run", acquire), patch.object(formal, "verify_tools") as verify:
                bad_hash = "0" * 64
                cases = [("formal archive checksum mismatch", dict(pin, archiveSHA256=bad_hash))]
                cases.extend((f"formal member checksum mismatch: {name}", dict(pin, files={**pin["files"], name: bad_hash}))
                             for name in files)
                for expected, bad in cases:
                    with self.subTest(expected=expected):
                        config.write_text(json.dumps(bad))
                        with self.assertRaises(ValueError) as failure:
                            formal.install(root)
                        self.assertEqual(str(failure.exception), expected)
                        self.assertFalse((root / formal.TOOLS).exists())
                        self.assertFalse(list(area.glob("install-*")))
                        verify.assert_not_called()
                config.write_text(json.dumps(pin))
                formal.install(root)
                for name, raw in files.items():
                    self.assertEqual((root / formal.TOOLS / name).read_bytes(), raw)
                verify.assert_called_once()
            with patch.object(formal, "run", side_effect=AssertionError("unexpected download or tool execution")):
                with patch.object(formal, "verify_tools") as verify_cached:
                    formal.install(root)
                    verify_cached.assert_called_once()
                solver = root / formal.TOOLS / "z3/bin/z3"
                solver.write_bytes(b"changed")
                with self.assertRaisesRegex(ValueError, "formal tool checksum mismatch: z3/bin/z3"):
                    formal.install(root)
                self.assertEqual(solver.read_bytes(), b"changed")

    def test_tool_inventory_cannot_omit_checks_or_extract_other_paths(self):
        files = self.pin["files"]
        self.assertEqual(formal.tool_files(self.pin), files)
        wrong_inventory = (None, {}, list(files), {k: v for k, v in files.items() if k != "server/server.jar"},
                           {**files, "../unexpected": "0" * 64})
        wrong_checksums = ({**files, "server/server.jar": ""}, {**files, "server/server.jar": 42})
        with patch.object(formal, "host"), patch.object(formal, "run", side_effect=AssertionError("must not execute")):
            for expected, cases in (("formal tool inventory", wrong_inventory), ("invalid formal tool checksum", wrong_checksums)):
                for invalid in cases:
                    with self.subTest(invalid=invalid), self.assertRaisesRegex(ValueError, expected):
                        formal.verify_tools(formal.ROOT, dict(self.pin, files=invalid), {})

    def test_host_admission(self):
        with patch.object(formal.sys, "platform", "linux"), patch.object(formal.platform, "machine", return_value="x86_64"):
            formal.host()
        for system, machine in (("linux", "aarch64"), ("darwin", "x86_64"), ("darwin", "arm64")):
            with (patch.object(formal.sys, "platform", system), patch.object(formal.platform, "machine", return_value=machine),
                  patch.object(formal, "read", side_effect=AssertionError("must refuse before reading tool inputs"))):
                for action in (formal.install, formal.execute):
                    with self.subTest(system=system, machine=machine, action=action.__name__), \
                            self.assertRaisesRegex(ValueError, "formal tools require Linux amd64"):
                        action()

    def test_environment_and_bounded_inputs(self):
        with patch.dict(formal.os.environ, JAVA_TOOL_OPTIONS="-bad", JDK_JAVA_OPTIONS="-bad", _JAVA_OPTIONS="-bad", Z3_EXE="wrong"):
            self.assertFalse(set(formal.environment()) & {"JAVA_TOOL_OPTIONS", "JDK_JAVA_OPTIONS", "_JAVA_OPTIONS", "Z3_EXE"})
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
