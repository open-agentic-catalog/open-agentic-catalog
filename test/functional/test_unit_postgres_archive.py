import unittest
from unittest import mock

from postgres_archive import dump_archive, temporary_archive
from runner import Failure


class ArchiveTests(unittest.TestCase):
    def test_invalid_allocation_never_exposes_or_removes_an_unowned_path(self):
        for raw in (b"", b"/\n", b"relative/catalog-archive.0123456789\n",
                    b"/tmp/../catalog-archive.0123456789\n", b"/tmp/catalog-archive.short\n",
                    b"/tmp/catalog-archive.0123456789\nextra\n", b"/tmp/catalog-archive.0123456789\x00\n"):
            compose = mock.Mock(return_value=raw)
            with self.subTest(raw=raw), self.assertRaisesRegex(Failure, "invalid-container-archive-directory"):
                with temporary_archive(compose, "postgres"):
                    self.fail("invalid allocation reached the operation")
            self.assertEqual(compose.call_count, 1)

    def test_cleanup_runs_after_success_or_failure_and_cannot_silently_pass(self):
        for operation_failed in (False, True):
            for cleanup_failed in (False, True):
                failure = Failure("operation-failed")
                compose = mock.Mock(side_effect=[b"/tmp/catalog-archive.0123456789\n",
                    Failure("cleanup-failed") if cleanup_failed else b""])
                with self.subTest(operation_failed=operation_failed, cleanup_failed=cleanup_failed):
                    try:
                        with temporary_archive(compose, "restored") as archive:
                            self.assertEqual(archive, "/tmp/catalog-archive.0123456789/archive.dump")
                            if operation_failed:
                                raise failure
                    except Failure as error:
                        self.assertTrue(operation_failed or cleanup_failed)
                        self.assertEqual(str(error), "cleanup-failed" if cleanup_failed else "operation-failed")
                        if operation_failed and cleanup_failed:
                            self.assertIs(error.__context__, failure)
                    else:
                        self.assertFalse(operation_failed or cleanup_failed)
                    compose.assert_called_with("exec", "-T", "restored", "rm", "-r", "--", "/tmp/catalog-archive.0123456789")

    def test_allocation_and_dump_failures_propagate_without_extra_work(self):
        compose = mock.Mock(side_effect=Failure("child-failed"))
        with self.assertRaisesRegex(Failure, "child-failed"):
            with temporary_archive(compose, "postgres"):
                self.fail("allocation failure reached the operation")
        self.assertEqual(compose.call_count, 1)
        compose.reset_mock()
        with self.assertRaisesRegex(Failure, "child-failed"):
            dump_archive(compose, "postgres", "/owned/archive.dump")
        self.assertEqual(compose.call_count, 1)
