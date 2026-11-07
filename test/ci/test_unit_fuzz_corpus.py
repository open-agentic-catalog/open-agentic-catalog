"""Isolated file admission and Make inventory checks for fuzz failure evidence."""

from pathlib import Path
import os
import re
import tempfile
import unittest
from unittest.mock import patch

import fuzz_corpus as f

HEADER = b"go test fuzz v1\n"
CORPUS = HEADER + b'string("synthetic counterexample")\n'


class FuzzCorpusTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="catalog-fuzz-evidence-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.output = self.root / "output/report/fuzz-corpus"
        guard = patch.object(f.subprocess, "run", side_effect=AssertionError("unit started a process"))
        guard.start()
        self.addCleanup(guard.stop)

    def fixture(self, name="internal/artwork/testdata/fuzz/FuzzSynthetic/0123456789abcdef", data=CORPUS):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data)
        return name

    def test_every_maintained_fuzz_package_is_collected(self):
        makefile = (f.ROOT / "Makefile").read_text()
        recipe = makefile.split("\ntest-fuzz:", 1)[1].split("\n\n", 1)[0]
        commands = [line for line in recipe.splitlines() if line.startswith("\t")]
        packages = set()
        for command in commands:
            match = re.fullmatch(r"\t\$\(GO\) test \./(\S+) -run '\^\$\$' -fuzz \w+ -fuzztime=\$\(FUZZ_TIME\)", command)
            self.assertIsNotNone(match, "Update corpus inventory qualification for a changed fuzz command")
            packages.add(match[1])
        self.assertTrue(packages, "No maintained fuzz packages discovered")
        self.assertEqual(set(f.PACKAGES), packages)
        names = [self.fixture(package + "/testdata/fuzz/FuzzSynthetic/0123456789abcdef") for package in sorted(packages)]
        self.assertEqual(f.collect(self.root, names, self.output), names)
        for name in names:
            self.assertEqual((self.output / name).read_bytes(), CORPUS)
            self.assertEqual((self.root / name).read_bytes(), CORPUS)

    def test_missing_package_regressions_and_existing_positive_control(self):
        names = [self.fixture(package + "/testdata/fuzz/FuzzSynthetic/0123456789abcdef")
                 for package in ("internal/draftfile", "internal/platform/authn", "internal/artwork")]
        self.assertEqual(f.collect(self.root, names, self.output), names)
        self.assertEqual({path.relative_to(self.output).as_posix() for path in self.output.rglob("*") if path.is_file()}, set(names))

    def test_unrelated_files_are_ignored(self):
        names = [self.fixture(name) for name in (
            "private/credentials", "other/testdata/fuzz/FuzzSynthetic/0123456789abcdef",
            "internal/artwork/testdata/other/0123456789abcdef",
            "internal/artwork-other/testdata/fuzz/FuzzSynthetic/0123456789abcdef",
        )]
        self.assertEqual(f.collect(self.root, names, self.output), [])
        self.assertFalse(self.output.exists())

    def test_malformed_corpus_paths_and_headers_are_refused(self):
        prefix = "internal/artwork/testdata/fuzz/"
        for suffix, data in (("FuzzSynthetic/not-a-hash", CORPUS),
                             ("FuzzSynthetic/" + "a" * 15, CORPUS),
                             ("FuzzSynthetic/" + "a" * 65, CORPUS),
                             ("FuzzSynthetic/" + "A" * 16, CORPUS),
                             ("FuzzSynthetic/nested/0123456789abcdef", CORPUS),
                             ("0123456789abcdef", CORPUS),
                             ("FuzzSynthetic/0123456789abcdef", b"private data\n")):
            with self.subTest(suffix=suffix, data=data):
                name = self.fixture(prefix + suffix, data)
                with self.assertRaises(ValueError):
                    f.collect(self.root, [name], self.output)
                self.assertFalse(self.output.exists())
        for name in ("/internal/artwork/testdata/fuzz/FuzzSynthetic/0123456789abcdef",
                     "internal/artwork/testdata/fuzz/FuzzSynthetic/../0123456789abcdef"):
            with self.subTest(name=name), self.assertRaises(ValueError):
                f.collect(self.root, [name], self.output)

    def test_source_symlinks_and_special_files_are_refused(self):
        name = self.fixture()
        source = self.root / name
        source.unlink()
        source.symlink_to(self.root / self.fixture("private/fixture"))
        with self.assertRaises(ValueError):
            f.collect(self.root, [name], self.output)
        source.unlink()
        source.mkdir()
        with self.assertRaises(ValueError):
            f.collect(self.root, [name], self.output)
        source.rmdir()
        os.mkfifo(source)
        with self.assertRaises(ValueError):
            f.collect(self.root, [name], self.output)
        self.assertFalse(self.output.exists())

    def test_redirected_source_ancestors_are_refused(self):
        name = "internal/artwork/testdata/fuzz/FuzzSynthetic/0123456789abcdef"
        actual = self.root / "elsewhere"
        actual.mkdir()
        (actual / "0123456789abcdef").write_bytes(CORPUS)
        parent = self.root / name
        parent.parent.parent.mkdir(parents=True)
        parent.parent.symlink_to(actual, target_is_directory=True)
        with self.assertRaises(ValueError):
            f.collect(self.root, [name], self.output)
        self.assertFalse(self.output.exists())

    def test_existing_or_redirected_destinations_are_preserved(self):
        name = self.fixture()
        f.collect(self.root, [name], self.output)
        retained = self.output / name
        retained.write_bytes(b"existing report")
        with self.assertRaises(FileExistsError):
            f.collect(self.root, [name], self.output)
        self.assertEqual(retained.read_bytes(), b"existing report")
        for output in (self.root / "outside", self.root / "output", self.root / "output/../outside"):
            with self.subTest(output=output), self.assertRaises(ValueError):
                f.collect(self.root, [name], output)
        redirected = self.root / "output/redirected"
        redirected.symlink_to(self.output, target_is_directory=True)
        with self.assertRaises(ValueError):
            f.collect(self.root, [name], redirected / "corpus")
        self.assertEqual(retained.read_bytes(), b"existing report")

    def test_redirected_destination_ancestor_cannot_write_outside_reports(self):
        name = self.fixture()
        outside = self.root / "private"
        outside.mkdir()
        redirected = self.output / "internal"
        redirected.parent.mkdir(parents=True)
        redirected.symlink_to(outside, target_is_directory=True)
        with self.assertRaises(ValueError):
            f.collect(self.root, [name], self.output)
        self.assertEqual(list(outside.iterdir()), [])

    def test_file_and_total_byte_limits(self):
        # Small injected limits exercise the exact inclusive boundaries without large files.
        length = len(CORPUS)
        with patch.object(f, "MAX_FILE_BYTES", length), patch.object(f, "MAX_TOTAL_BYTES", 2 * length):
            first = self.fixture()
            second = self.fixture("internal/artwork/testdata/fuzz/FuzzSynthetic/" + "b" * 64)
            third = self.fixture("internal/artwork/testdata/fuzz/FuzzSynthetic/" + "c" * 16)
            self.assertEqual(f.collect(self.root, [first, second], self.output), [first, second])
            with self.assertRaisesRegex(ValueError, "excessive"):
                f.collect(self.root, [first, second, third], self.root / "output/excessive")
            self.assertFalse((self.root / "output/excessive" / third).exists())
            (self.root / first).write_bytes(CORPUS + b"x")
            with self.assertRaises(ValueError):
                f.collect(self.root, [first], self.root / "output/oversized")
            self.assertFalse((self.root / "output/oversized").exists())
