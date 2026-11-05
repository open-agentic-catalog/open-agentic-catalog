"""Private native mutation hook; invoked only in an owned process group."""

import os
from pathlib import Path
import signal
import sys

sys.path.insert(0, str(Path(__file__).resolve().parent))
from mutation import hook, interrupt  # noqa: E402

if __name__ == "__main__":
    for selected_signal in (signal.SIGINT, signal.SIGTERM):
        signal.signal(selected_signal, interrupt)
    raise SystemExit(hook(os.environ["CATALOG_MUTATION_HOOK"]))
