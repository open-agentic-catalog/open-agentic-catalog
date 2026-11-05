"""Fail closed on unreviewed example dependency licenses; no imports of runtime code."""

from importlib.metadata import distributions
import json
from pathlib import Path
import tomllib

# These packages are used only by the optional developer example. No framework
# dependency enters the Catalog images, shipped SDK wheel or Go license gate.
ALLOWED = {
    "MIT",
    "BSD-2-Clause",
    "BSD-3-Clause",
    "Modified BSD License",
    "Apache-2.0",
    "Apache 2.0",
    "Apache License, Version 2.0",
    "PSF-2.0",
    "Apache-2.0 OR BSD-2-Clause",
    "Apache-2.0 OR MIT",
    "MIT OR Apache-2.0",
}
MPL = {
    "certifi": ("2026.10.19", "MPL-2.0"),
    "orjson": ("3.13.0", "MPL-2.0 AND (Apache-2.0 OR MIT)"),
}


def inventory():
    result = {}
    lock = tomllib.loads(
        (Path(__file__).resolve().parents[2] / "examples/langgraph/uv.lock").read_text()
    )
    locked = {p["name"]: p["version"] for p in lock["package"]}
    for package in distributions():
        name = package.metadata["Name"].lower().replace("_", "-")
        license = package.metadata.get("License-Expression") or package.metadata.get(
            "License"
        )
        if (
            name == "python-dateutil"
            and package.version == "2.9.0.post0"
            and license == "Dual License"
        ):
            license = "Apache-2.0 OR BSD-3-Clause"
        if locked.get(name) != package.version:
            raise RuntimeError("Example environment differs from its lock: " + name)
        if license not in ALLOWED | {"Apache-2.0 OR BSD-3-Clause"} and MPL.get(
            name
        ) != (package.version, license):
            raise RuntimeError("Unreviewed example dependency license: " + name)
        result[name] = {"version": package.version, "license": license}
    if (
        not {"langgraph", "langchain-ollama", "open-agentic-catalog", "coverage"}
        <= result.keys()
    ):
        raise RuntimeError("Incomplete example environment; reinstall the locked tools")
    return result


if __name__ == "__main__":
    print(json.dumps(inventory(), sort_keys=True, indent=2))
