#!/usr/bin/env python3
"""Explicitly convert one current private CLI login to curl/native inputs."""

import argparse
import datetime
import json
import os
from pathlib import Path
import re
import stat
import tempfile


def private_directory(path):
    metadata = path.lstat()
    if not stat.S_ISDIR(metadata.st_mode) or metadata.st_uid != os.getuid() or stat.S_IMODE(metadata.st_mode) != 0o700:
        raise ValueError("The selected output directory must be owned by this user with mode 0700")


def replace_private(path, value):
    """Replace only the selected derived file, without following a final symlink."""
    private_directory(path.parent)
    descriptor, temporary = tempfile.mkstemp(prefix=".derived-", dir=path.parent)
    try:
        with os.fdopen(descriptor, "w", encoding="ascii") as stream:
            stream.write(value)
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def convert(args):
    selected = Path(args.login_file).expanduser()
    source = selected.parent.resolve() / selected.name
    metadata = source.lstat()
    if not stat.S_ISREG(metadata.st_mode) or metadata.st_uid != os.getuid() or stat.S_IMODE(metadata.st_mode) != 0o600 or metadata.st_size > 65536:
        raise ValueError("Select the owned 0600 CLI credential file")
    private_directory(source.parent)
    value = json.loads(source.read_text(encoding="utf-8"))
    if value["apiOrigin"] != args.origin:
        raise ValueError("The CLI login belongs to another API origin")
    expires = datetime.datetime.fromisoformat(value["expiresAt"].replace("Z", "+00:00"))
    if expires.tzinfo is None or expires <= datetime.datetime.now(datetime.timezone.utc):
        raise ValueError("Refresh through a supported remote read or sign in again first")
    token = value["accessToken"]
    if not isinstance(token, str) or not re.fullmatch(r"[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+", token) or len(token) > 32768:
        raise ValueError("The selected access token is not a bounded compact JWT")
    selected_output = Path(args.output_stem).expanduser()
    output = selected_output.parent.resolve() / selected_output.name
    headers = Path(str(output) + ".headers")
    bare_token = Path(str(output) + ".token")
    if headers == source or bare_token == source:
        raise ValueError("Derived files must not replace the CLI login file")
    replace_private(headers, "Authorization: Bearer " + token + "\n")
    if args.token:
        replace_private(bare_token, token + "\n")
    print("Wrote protected derived credentials; the access token was not printed")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("login_file")
    parser.add_argument("origin")
    parser.add_argument("output_stem")
    parser.add_argument("--token", action="store_true", help="also write the bare access-token file")
    args = parser.parse_args()
    try:
        convert(args)
    except (OSError, ValueError, KeyError, TypeError):
        raise SystemExit("Credential conversion refused; check the private file, directory, API origin and expiry.") from None


if __name__ == "__main__":
    main()
