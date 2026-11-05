"""Verify the two installed coverage reporters against their isolated Go lock."""
import json
import os
from pathlib import Path
import shlex
import shutil
import subprocess

ROOT = Path(__file__).resolve().parents[2]
MODULE = ROOT / "test/packaging/coverage-tools"
TOOLS = {"GOCOV": "github.com/axw/gocov/gocov", "GOCOV_HTML": "github.com/matm/gocov-html/cmd/gocov-html"}


def validate(metadata, package, modules, toolchain):
    """Reject another command, an old compiler, replacements or dependency drift."""
    lines = metadata.splitlines()
    if not lines or not lines[0].split() or lines[0].split()[-1] != toolchain or "\tpath\t" + package not in lines:
        raise ValueError("coverage tool command/toolchain mismatch")
    found = {}
    for line in lines[1:]:
        fields = line.split("\t")
        if fields[0] == "=>" or (len(fields) > 1 and fields[1] == "=>"):
            raise ValueError("coverage tool replacements are not allowed")
        if len(fields) > 1 and fields[1] in ("mod", "dep"):
            if len(fields) != 5:
                raise ValueError("coverage tool module metadata is incomplete")
            name, version, checksum = fields[2:5]
            if name in found or modules.get(name) != (version, checksum):
                raise ValueError("coverage tool dependency mismatch")
            found[name] = version
    owner = package.rsplit("/", 1)[0].removesuffix("/cmd")
    if owner not in found:
        raise ValueError("coverage tool module is missing")


def check():
    go = shlex.split(os.environ.get("PACKAGING_GO", "go"))
    toolchain = "go" + next(line.split()[1] for line in (ROOT / "go.mod").read_text().splitlines() if line.startswith("go "))
    requirements = json.loads(subprocess.check_output(go + ["mod", "edit", "-json"], cwd=MODULE, timeout=30))
    sums = {tuple(line.split()[:2]): line.split()[2] for line in (MODULE / "go.sum").read_text().splitlines()}
    modules = {item["Path"]: (item["Version"], sums[(item["Path"], item["Version"])]) for item in requirements["Require"]}
    for variable, package in TOOLS.items():
        command = os.environ.get(variable, variable.lower().replace("_", "-"))
        path = shutil.which(command)
        if not path:
            raise ValueError("coverage reporter missing; run coverage-tools-install and select its installed binaries")
        metadata = subprocess.check_output(go + ["version", "-m", path], text=True, timeout=30)
        validate(metadata, package, modules, toolchain)


if __name__ == "__main__":
    try:
        check()
    except (ValueError, KeyError, OSError, subprocess.SubprocessError):
        raise SystemExit("Coverage tool identity check failed; run coverage-tools-install with the repository Go toolchain and select those binaries.") from None
    print("Coverage reporter identities match the pinned tool module.")
