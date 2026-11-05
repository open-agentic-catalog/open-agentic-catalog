import os
from pathlib import Path
import subprocess
import tempfile
import unittest

from postgres_archive import dump_archive, temporary_archive


class ArchiveTests(unittest.TestCase):
    def test_private_files_unique_paths_and_cleanup_with_real_shell_commands(self):
        # Exercise real mktemp, shell umask and removal without Docker. The
        # packaged backup/webhook gates separately qualify pg_dump/pg_restore.
        # A writable /tmp models the container namespace; host TMPDIR may contain
        # dots or spaces which the container-path guard deliberately rejects.
        with tempfile.TemporaryDirectory(prefix="archive-regression-", dir="/tmp") as root:
            parent = Path(root)
            self.assertEqual(parent.stat().st_mode & 0o777, 0o700)
            binary = parent / "bin"
            binary.mkdir()
            stub = binary / "pg_dump"
            stub.write_text('#!/bin/sh\nset -eu\n'
                'test "$#" -eq 7\n'
                'test "$1:$2:$3:$4:$5:$6" = "-U:catalog:-d:catalog:-Fc:--file"\n'
                'printf "fixture dump" > "$7"\n'
                'test "$ARCHIVE_TEST_FAIL" = 0\n')
            stub.chmod(0o700)
            sentinel = parent / "sentinel"
            sentinel.write_bytes(b"unrelated retained data")
            for name in ("catalog-backup.dump", "webhook-key.dump"):
                (parent / name).symlink_to(sentinel)
            for failure in ("none", "dump", "operation"):
                environment = dict(os.environ, TMPDIR=root, PATH=str(binary) + os.pathsep + os.environ["PATH"],
                                   ARCHIVE_TEST_FAIL=str(int(failure == "dump")))
                calls = []

                def compose(*args, capture=False, limit=1024):
                    self.assertEqual(args[:3], ("exec", "-T", "postgres"))
                    calls.append(args)
                    result = subprocess.run(args[3:], env=environment, check=True, capture_output=True, timeout=5, umask=0)
                    self.assertLessEqual(len(result.stdout) + len(result.stderr), limit)
                    return result.stdout if capture else b""

                with self.subTest(failure=failure):
                    archive = None
                    try:
                        with temporary_archive(compose, "postgres") as archive:
                            path = Path(archive)
                            self.assertEqual(path.parent.parent, parent)
                            self.assertEqual(path.parent.stat().st_mode & 0o777, 0o700)
                            with temporary_archive(compose, "postgres") as other:
                                self.assertNotEqual(archive, other)
                            self.assertFalse(Path(other).parent.exists())
                            try:
                                dump_archive(compose, "postgres", archive)
                            finally:
                                self.assertEqual(path.stat().st_mode & 0o777, 0o600)
                                self.assertEqual(path.read_bytes(), b"fixture dump")
                            if failure == "operation":
                                raise RuntimeError("later operation failed")
                    except subprocess.CalledProcessError:
                        self.assertEqual(failure, "dump")
                    except RuntimeError:
                        self.assertEqual(failure, "operation")
                    else:
                        self.assertEqual(failure, "none")
                    self.assertIsNotNone(archive)
                    self.assertFalse(Path(archive).parent.exists())
                    self.assertEqual(calls[-1][3:6], ("rm", "-r", "--"))
                    self.assertEqual(list(parent.glob("catalog-archive.*")), [])
                    self.assertEqual(sentinel.read_bytes(), b"unrelated retained data")
                    self.assertTrue(all((parent / name).is_symlink() for name in ("catalog-backup.dump", "webhook-key.dump")))
        self.assertFalse(parent.exists())
