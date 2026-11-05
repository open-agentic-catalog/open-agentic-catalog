#!/usr/bin/env python3
"""Select one prepared tutorial project before invoking Docker Compose."""

import argparse
import ipaddress
import json
import os
from pathlib import Path
import re
import stat


SOURCE = Path(__file__).resolve().parent
INPUTS = {
    "COMPOSE_PROJECT_NAME", "CATALOG_TUTORIAL_DIRECTORY", "CATALOG_TUTORIAL_SOURCE",
    "CATALOG_TUTORIAL_BACKEND_IMAGE", "CATALOG_TUTORIAL_UI_IMAGE",
    "CATALOG_TUTORIAL_FRONTEND_IMAGE", "CATALOG_TUTORIAL_SUBNET",
    "CATALOG_TUTORIAL_FRONTEND_ADDRESS",
}
SELECTORS = ("--project-name", "--env-file", "--file", "--project-directory")


def prepared_inputs(directory):
    """Read only the generated metadata in the selected owned private directory."""
    selected = Path(directory).expanduser()
    directory = selected.parent.resolve() / selected.name
    metadata = directory.lstat()
    if not stat.S_ISDIR(metadata.st_mode) or metadata.st_uid != os.getuid() or stat.S_IMODE(metadata.st_mode) != 0o700:
        raise ValueError("Select an owned 0700 tutorial directory")
    path = directory / "compose.env"
    metadata = path.lstat()
    if not stat.S_ISREG(metadata.st_mode) or metadata.st_uid != os.getuid() or stat.S_IMODE(metadata.st_mode) != 0o600 or metadata.st_size > 65536:
        raise ValueError("Select the generated owned 0600 metadata file")
    values = {}
    for line in path.read_text(encoding="ascii").splitlines():
        key, separator, encoded = line.partition("=")
        if not separator or key not in INPUTS or key in values:
            raise ValueError("The prepared metadata has an unexpected or repeated input")
        value = json.loads(encoded)
        if not isinstance(value, str) or not value or any(character in value for character in "\r\n\0"):
            raise ValueError("The prepared metadata must contain nonempty single-line strings")
        values[key] = value
    if values.keys() != INPUTS or not re.fullmatch(r"catalog-docs-[0-9a-f]{8}", values["COMPOSE_PROJECT_NAME"]):
        raise ValueError("The prepared project selection is missing or invalid")
    if values["CATALOG_TUTORIAL_DIRECTORY"] != str(directory) or values["CATALOG_TUTORIAL_SOURCE"] != str(SOURCE):
        raise ValueError("The prepared inputs belong to another directory or source checkout")
    network = ipaddress.ip_network(values["CATALOG_TUTORIAL_SUBNET"])
    frontend = ipaddress.ip_address(values["CATALOG_TUTORIAL_FRONTEND_ADDRESS"])
    ranges = [ipaddress.ip_network(value) for value in ("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16")]
    if network.version != 4 or not any(network.subnet_of(value) for value in ranges) or frontend not in network or frontend in (network.network_address, network.broadcast_address):
        raise ValueError("The prepared private network selection is invalid")
    return directory, values


def invocation(directory, arguments):
    """Keep selectors fixed, including when Compose accepts flags after commands."""
    for argument in arguments:
        if any(argument == option or argument.startswith(option + "=") for option in SELECTORS) or re.match(r"^-[^-]*[pf]", argument):
            raise ValueError("Project, env-file and Compose-file selectors are fixed by the prepared directory")
    directory, values = prepared_inputs(directory)
    command = ["docker", "compose", "--project-name", values["COMPOSE_PROJECT_NAME"],
               "--env-file", str(directory / "compose.env"), "--file", str(SOURCE / "compose.yaml"), *arguments]
    environment = os.environ.copy()
    environment.update(values)
    return command, environment


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--directory", required=True)
    parser.add_argument("--show-project", action="store_true", help="validate inputs and print only the selected project; do not invoke Docker")
    parser.add_argument("arguments", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    if args.show_project and args.arguments or not args.show_project and not args.arguments:
        parser.error("select either --show-project or a Compose command")
    try:
        command, environment = invocation(args.directory, args.arguments)
        if args.show_project:
            print(environment["COMPOSE_PROJECT_NAME"])
            return
        os.execvpe(command[0], command, environment)
    except (OSError, ValueError, TypeError):
        raise SystemExit("Tutorial Compose selection refused; check the prepared directory, metadata, reserved selectors and Docker prerequisite.") from None


if __name__ == "__main__":
    main()
