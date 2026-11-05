"""Private dump files inside supervisor-owned PostgreSQL fixture containers."""

from contextlib import contextmanager
import re


@contextmanager
def temporary_archive(compose, service, *, error_type=None):
    """Allocate atomically in the container, never in the host's shared /tmp."""
    if error_type is None:
        from runner import Failure  # Import only for the original functional supervisor.
        error_type = Failure
    raw = compose("exec", "-T", service, "mktemp", "-d", "-t", "catalog-archive.XXXXXXXXXX",
                  capture=True, limit=1024)
    # Accept only a bounded absolute mktemp result, not diagnostics, parent
    # traversal or a caller-selected path that could redirect recursive cleanup.
    if not re.fullmatch(rb"/(?:[A-Za-z0-9_-]+/)*catalog-archive\.[A-Za-z0-9]{10}\n", raw):
        raise error_type("invalid-container-archive-directory")
    directory = raw[:-1].decode("ascii")
    try:
        yield directory + "/archive.dump"
    finally:
        # Failure here must fail the scenario. If the container is unavailable
        # or its deadline expired, the supervisor still removes the owned stack.
        compose("exec", "-T", service, "rm", "-r", "--", directory)


def dump_archive(compose, service, archive):
    """Create an owner-only dump beneath an allocated private directory."""
    compose("exec", "-T", service, "sh", "-ec",
            'umask 077; exec pg_dump -U catalog -d catalog -Fc --file "$1"', "catalog-dump", archive)
