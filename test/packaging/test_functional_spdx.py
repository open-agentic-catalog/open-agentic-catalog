"""Exercise standalone CLI publication with a controlled converter process."""
import json
import os
from pathlib import Path
import shlex
import subprocess
import sys
import tempfile
import unittest

from packaging_test_helpers import IDENTIFIER
from test_unit_spdx import native_fixture

ROOT = Path(__file__).resolve().parents[2]

# Only extraction and the final engine lookup are doubled. The real CLI,
# converter subprocess, qualification, supplements and exclusive write run.
DRIVER = '''
import json, os
from pathlib import Path
from unittest.mock import patch
import image
from packaging_test_helpers import license_tree, IDENTIFIER
from test_unit_ui import fixture
root = Path(os.environ["OWNED_CASE"])
def inspect(options, directory):
    if options.profile == "ui":
        fixture(directory)
    else:
        license_tree(directory / "licenses")
    return json.loads((root / "inspection.json").read_text())
def identity(options):
    (root / "identity-checked").touch()
    return "sha256:" + "b" * 64 if os.environ["OWNED_MODE"] == "drift" else IDENTIFIER
with patch.object(image, "inspect", side_effect=inspect), patch.object(image, "local_identity", side_effect=identity):
    image.main()
'''

CONVERTER = '''
import json, os, sys
from pathlib import Path
root = Path(os.environ["OWNED_CASE"])
args = sys.argv[1:]
with (root / "calls.jsonl").open("a") as log:
    log.write(json.dumps({"args":args,"cwd":os.getcwd()}) + "\\n")
if args == ["--version", "--format", "json"]:
    print(json.dumps({"Version":os.environ["OWNED_TRIVY_VERSION"]}))
elif args == ["image", "--help"]:
    print("--image-src --platform --offline-scan --skip-db-update --list-all-pkgs --image-config-scanners")
else:
    destination = Path(args[args.index("--output") + 1])
    mode = os.environ["OWNED_MODE"]
    if mode == "converter-failure":
        destination.write_text("partial")
        sys.exit(7)
    if mode == "symlink":
        destination.symlink_to(root / "retained.txt")
    elif mode == "empty":
        destination.touch()
    else:
        destination.write_bytes((root / "native.json").read_bytes())
    if mode == "output-race":
        Path(os.environ["OWNED_DESTINATION"]).write_text("caller-owned output")
'''


def inputs(profile):
    names = ("catalog-ui",) if profile == "ui" else ("catalogd", "catalogctl")
    document, inspected = native_fixture(names)
    project = "github.com/open-agentic-catalog/open-agentic-catalog"
    for binary in inspected["binaries"].values():
        binary["goBuildInfo"] = binary["goBuildInfo"].replace("\tmod\texample.test/catalog\t", "\tmod\t" + project + "\t").replace("example.test/shared", "github.com/jackc/pgx/v5")
    for package in document["packages"]:
        if package["name"] == "example.test/catalog":
            package["name"] = project
        elif package["name"] == "example.test/shared":
            package["name"] = "github.com/jackc/pgx/v5"
        if package["SPDXID"].endswith("-own"):
            package["externalRefs"] = [{"referenceCategory": "PACKAGE-MANAGER", "referenceType": "purl",
                                        "referenceLocator": "pkg:golang/" + package["name"] + "@v2.0.0"}]
    document.update(name=IDENTIFIER, SPDXID="SPDXRef-DOCUMENT")
    inspected["imageId"] = IDENTIFIER
    return document, inspected, names[0]


class StandaloneSPDXProcessTests(unittest.TestCase):
    def test_cli_rejects_incomplete_native_graphs_before_publication(self):
        modes = {"valid": None, "missing": "inventory mismatch", "wrong-version": "inventory mismatch",
                 "wrong-stdlib": "inventory mismatch", "shared-edge": "inventory mismatch",
                 "wrong-image": "image identity mismatch", "array": "image identity mismatch",
                 "null": "image identity mismatch", "truncated": "Local image operation failed",
                 "converter-failure": "scanner command failed", "empty": "missing or oversized",
                 "symlink": "missing or oversized", "drift": "selected image changed",
                 "output-race": "File exists"}
        for profile in ("backend", "ui"):
            for mode, diagnostic in modes.items():
                with self.subTest(profile=profile, mode=mode), tempfile.TemporaryDirectory() as directory:
                    root = Path(directory)
                    output, scratch = root / "output", root / "scratch"
                    output.mkdir()
                    scratch.mkdir()
                    (root / "retained.txt").write_text("retained caller input")
                    document, inspected, name = inputs(profile)
                    if mode == "missing":
                        identifier = "SPDXRef-" + name + "-own"
                        document["packages"] = [p for p in document["packages"] if p["SPDXID"] != identifier]
                        document["relationships"] = [r for r in document["relationships"] if r["relatedSpdxElement"] != identifier]
                    elif mode in ("wrong-version", "wrong-stdlib"):
                        identifier = "SPDXRef-" + name + ("-stdlib" if mode == "wrong-stdlib" else "-own")
                        next(p for p in document["packages"] if p["SPDXID"] == identifier)["versionInfo"] = "v0.0.1"
                    elif mode == "shared-edge":
                        document["relationships"] = [r for r in document["relationships"] if r["relatedSpdxElement"] != "SPDXRef-" + name + "-shared"]
                    elif mode == "wrong-image":
                        document["name"] = "sha256:" + "b" * 64
                    elif mode in ("array", "null"):
                        document = [] if mode == "array" else None
                    (root / "native.json").write_text("{" if mode == "truncated" else json.dumps(document))
                    (root / "inspection.json").write_text(json.dumps(inspected))
                    (root / "driver.py").write_text(DRIVER)
                    (root / "converter.py").write_text(CONVERTER)
                    destination = output / ("sbom-linux-amd64-" + IDENTIFIER[7:19] + ".spdx.json")
                    env = {k: v for k, v in os.environ.items() if not k.startswith(("PACKAGING_", "TRIVY_", "PYTHON", "CATALOG_"))}
                    env.update(OWNED_CASE=str(root), OWNED_MODE=mode, OWNED_DESTINATION=str(destination),
                               OWNED_TRIVY_VERSION=json.loads((ROOT / "test/packaging/trivy.json").read_text())["version"],
                               PACKAGING_TRIVY=shlex.join([sys.executable, str(root / "converter.py")]),
                               PACKAGING_OUTPUT=str(output), PACKAGING_IMAGE=IDENTIFIER, TMPDIR=str(scratch),
                               PYTHONPATH=str(ROOT / "test/packaging"), PYTHONDONTWRITEBYTECODE="1")
                    command = [sys.executable, "-B", str(root / "driver.py"), "sbom", "--profile", profile]
                    result = subprocess.run(command, cwd=ROOT, env=env, text=True, capture_output=True, timeout=15, check=False)
                    self.assertEqual(result.returncode, 0 if diagnostic is None else 1, result.stdout + result.stderr)
                    self.assertNotIn("Traceback", result.stderr)
                    calls = [json.loads(line) for line in (root / "calls.jsonl").read_text().splitlines()]
                    converted = calls[-1]["args"]
                    self.assertEqual(converted[-1], IDENTIFIER)
                    self.assertEqual(converted[converted.index("--image-src") + 1], "docker")
                    self.assertIn("--offline-scan", converted)
                    for flag in ("--skip-db-update", "--skip-java-db-update", "--skip-check-update", "--skip-version-check"):
                        self.assertIn(flag, converted)
                    self.assertEqual(list(scratch.iterdir()), [], "all inspection/converter scratch must be removed")
                    self.assertTrue(all(not Path(call["cwd"]).exists() for call in calls))
                    self.assertEqual((root / "retained.txt").read_text(), "retained caller input")
                    self.assertEqual((root / "identity-checked").exists(), mode in ("valid", "drift", "output-race"))
                    if diagnostic is not None:
                        self.assertIn(diagnostic, result.stderr)
                        self.assertEqual(list(output.iterdir()), [destination] if mode == "output-race" else [])
                        if mode == "output-race":
                            self.assertEqual(destination.read_text(), "caller-owned output")
                        continue
                    self.assertEqual(list(output.iterdir()), [destination])
                    self.assertEqual(destination.stat().st_mode & 0o777, 0o600)
                    published = json.loads(destination.read_text())
                    self.assertEqual(published["packages"][:len(document["packages"])], document["packages"])
                    self.assertEqual(published["relationships"][:len(document["relationships"])], document["relationships"])
                    names = {p["name"] for p in published["packages"]}
                    self.assertTrue({"react", "astro"}.issubset(names) if profile == "ui" else "catalog-embedded-standards" in names)
                    self.assertNotIn("build-only", names)
                    before = destination.read_bytes()
                    repeat = subprocess.run(command, cwd=ROOT, env=env, text=True, capture_output=True, timeout=15, check=False)
                    self.assertEqual(repeat.returncode, 1, repeat.stdout + repeat.stderr)
                    self.assertIn("output already exists", repeat.stderr)
                    self.assertEqual(destination.read_bytes(), before)
                    self.assertEqual(len((root / "calls.jsonl").read_text().splitlines()), len(calls), "refuse existing output before converter use")
                    self.assertEqual(list(scratch.iterdir()), [])
