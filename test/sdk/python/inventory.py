"""Installed runtime closure and reviewed license classifications, never imports SDK."""
from importlib.metadata import distribution, distributions
import json
from pathlib import Path
import sys
import tomllib
from packaging.tags import sys_tags
from packaging.utils import parse_wheel_filename

from packaging.requirements import Requirement
from packaging.utils import canonicalize_name


def inventory():
    return {canonicalize_name(d.metadata["Name"]): d.version for d in distributions()}


def runtime_closure():
    pending = ["open-agentic-catalog"]
    result = {}
    while pending:
        name = canonicalize_name(pending.pop())
        if name in result:
            continue
        package = distribution(name)
        result[name] = package.version
        for raw in package.requires or []:
            requirement = Requirement(raw)
            if requirement.marker is None or requirement.marker.evaluate({"extra":""}):
                pending.append(requirement.name)
    return result


def licenses():
    result = {}
    for name, version in runtime_closure().items():
        package = distribution(name)
        declared = package.metadata.get("License-Expression") or package.metadata.get("License")
        if name == "python-dateutil" and version == "2.9.0.post0" and declared == "Dual License":
            declared = "Apache-2.0 OR BSD-3-Clause"
        if name == "typing-extensions" and declared is None:
            # Metadata older than PEP 639; upstream wheel's license is PSF-2.0.
            if not any("Python Software Foundation" in str(c) for c in package.metadata.get_all("Classifier", [])):
                raise RuntimeError("Unreviewed typing-extensions license")
            declared = "PSF-2.0"
        if declared not in {"MIT", "BSD-2-Clause", "BSD-3-Clause", "Apache-2.0", "PSF-2.0", "Apache-2.0 OR BSD-3-Clause"}:
            raise RuntimeError("Unreviewed Python SDK runtime license: " + name + " " + str(declared))
        result[name] = {"version":version, "license":declared}
    return result


def wheels(lock_path):
    tags = set(sys_tags())
    closure = runtime_closure()
    result = []
    for package in tomllib.loads(Path(lock_path).read_text())["package"]:
        name = canonicalize_name(package["name"])
        if name not in closure or name == "open-agentic-catalog":
            continue
        if package["version"] != closure[name]:
            raise RuntimeError("Installed runtime differs from lock")
        candidates = [w for w in package["wheels"] if parse_wheel_filename(w["url"].rsplit("/", 1)[-1])[3] & tags]
        if not candidates:
            raise RuntimeError("No locked wheel for this Python/platform: " + name)
        result.append(candidates[0])
    if len(result) != len(closure) - 1:
        raise RuntimeError("Incomplete runtime wheel inventory")
    return result


if __name__ == "__main__":
    if len(sys.argv) == 2:
        print(json.dumps(wheels(sys.argv[1]), sort_keys=True))
        sys.exit(0)
    print(json.dumps({"installed":inventory(), "runtime":runtime_closure(), "licenses":licenses()}, sort_keys=True))
