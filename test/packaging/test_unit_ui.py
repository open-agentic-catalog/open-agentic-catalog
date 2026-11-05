"""UI image profiles keep backend checks strict and use image-owned evidence."""
import copy
import hashlib
import json
from pathlib import Path
import tempfile
import unittest

import image
import ui
from packaging_test_helpers import IDENTIFIER, settings, license_tree


def fixture(root):
    """Build readable image content without changing the private outer directory."""
    license_tree(root / "licenses")
    (root / "assets").mkdir()
    assets = []
    for name, value in [("index.html", "index"), ("main.js", "compiled")]:
        (root / "assets" / name).write_text(value)
        assets.append({"path": name, "sha256": hashlib.sha256(value.encode()).hexdigest()})
    frontend = root / "licenses/frontend"
    (frontend / "node_modules/react").mkdir(parents=True)
    (frontend / "node_modules/react/LICENSE").write_text("MIT")
    value = dict(schemaVersion=1, scope="locked-production-dependencies", lockSha256="a" * 64,
        packages=[dict(path="node_modules/react", name="react", version="1.0", license="MIT", notices=["node_modules/react/LICENSE"])], assets=assets)
    (frontend / "inventory.json").write_text(json.dumps(value))
    docs = root / "assets/docs"
    (docs / "generated").mkdir(parents=True)
    notice_dir = root / "licenses/documentation"
    notice_dir.mkdir()
    supplier = dict(schemaVersion=1, scope="reviewed-browser-asset-suppliers", lockSha256="b" * 64,
                    packages=[dict(name="astro", version="1.0", license="MIT", notices=["LICENSE"])])
    contents = {"index.html": "manual", "generated/browser-inventory.json": json.dumps(supplier),
                "generated/dependency-inventory.json": json.dumps(supplier["packages"] + [dict(name="build-only", version="1.0", license=["MIT", "Apache-2.0"])]), "generated/third-party-notices.txt": "Notice: astro@1.0 / LICENSE\nMIT notices"}
    files = []
    for name, text in contents.items():
        (docs / name).write_text(text)
        files.append(dict(path=name, size=len(text.encode()), sha256=hashlib.sha256(text.encode()).hexdigest()))
        if name.startswith("generated/"):
            (notice_dir / Path(name).name).write_text(text)
    manual = dict(schemaVersion=1, base="/docs/", lockSha256="b" * 64, files=files, scriptHashes=[], styleHashes=[])
    for target in (docs, notice_dir):
        (target / "documentation-bundle.json").write_text(json.dumps(manual))
    for path in root.rglob("*"):
        path.chmod(0o755 if path.is_dir() else 0o644)
    return value


class UITests(unittest.TestCase):
    def test_profile_keeps_backend_requirements(self):
        value = settings()
        value["Config"].update(WorkingDir="/", Entrypoint=["/usr/local/bin/catalog-ui"], Cmd=["--config", "/config/config.json"], ExposedPorts={"8080/tcp": {}})
        value["Config"]["Labels"].update({"org.opencontainers.image.title": "Open Agentic Catalog UI", "org.opencontainers.image.licenses": "Apache-2.0"})
        self.assertEqual(image.image_settings(value, "linux/amd64", "dev", "unknown", "ui"), IDENTIFIER)
        value["Config"]["WorkingDir"] = ""
        self.assertEqual(image.image_settings(value, "linux/amd64", "dev", "unknown", "ui"), IDENTIFIER)
        for profile in ["backend", "anything"]:
            with self.assertRaises(ValueError):
                image.image_settings(value, "linux/amd64", "dev", "unknown", profile)
        with self.assertRaises(ValueError):
            image.image_settings(settings(), "linux/amd64", "dev", "unknown", "ui")

    def test_inventory_and_sbom_use_extracted_assets(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            expected = fixture(root)
            image.check_licenses(root / "licenses", "ui")
            self.assertEqual(ui.inventory(root), expected)
            doc = dict(spdxVersion="SPDX-2.3", SPDXID="SPDXRef-DOCUMENT", packages=[dict(name=name) for name in ["usr/local/bin/catalog-ui", "github.com/open-agentic-catalog/open-agentic-catalog"]])
            ui.supplement(doc, root, IDENTIFIER, "linux/amd64")
            self.assertEqual(doc["packages"][-2]["name"], "react")
            self.assertEqual(doc["packages"][-1]["name"], "astro")
            self.assertEqual(doc["packages"][-1]["licenseDeclared"], "MIT")
            self.assertEqual(doc["packages"][-2]["externalRefs"][0]["referenceLocator"], "pkg:npm/react@1.0")
            self.assertIn(IDENTIFIER, doc["comment"])
            self.assertNotIn("catalog-embedded-standards", str(doc))
            (root / "assets/main.js").write_text("changed")
            with self.assertRaisesRegex(ValueError, "mismatch"):
                ui.inventory(root)

    def test_manual_is_required_exact_and_partitioned(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            fixture(root)
            self.assertEqual(ui.documentation_inventory(root)["packages"][0]["name"], "astro")
            self.assertEqual({p["name"] for p in ui.scan_inventory(root)["packages"]}, {"react", "astro"})
            manual = root / "assets/docs"
            original = (manual / "index.html").read_bytes()
            (manual / "index.html").chmod(0o600)
            with self.assertRaisesRegex(ValueError, "not runtime-readable"):
                ui.documentation_inventory(root)
            (manual / "index.html").chmod(0o644)
            for altered in (b"changed", b"tamper"):
                with self.subTest(altered=altered):
                    (manual / "index.html").write_bytes(altered)
                    with self.assertRaisesRegex(ValueError, "asset mismatch"):
                        ui.documentation_inventory(root)

            (manual / "index.html").write_bytes(original)
            (manual / "extra.js").write_text("not inventoried")
            # Reach inventory completeness independently of the inherited umask.
            (manual / "extra.js").chmod(0o644)
            with self.assertRaisesRegex(ValueError, "incomplete"):
                ui.documentation_inventory(root)
            (manual / "extra.js").unlink()
            (root / "assets/extra.js").write_text("not frontend inventoried")
            (root / "assets/extra.js").chmod(0o644)
            with self.assertRaisesRegex(ValueError, "incomplete"):
                ui.inventory(root)
            (manual / "documentation-bundle.json").unlink()
            with self.assertRaises(OSError):
                ui.documentation_inventory(root)

    def test_manual_suppliers_require_their_own_notice_sections(self):
        for notices in (None, [], ["missing-LICENSE"], ["../LICENSE"], ["LICENSE", "LICENSE"]):
            with self.subTest(notices=notices), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                fixture(root)
                supplier = json.loads((root / "licenses/documentation/browser-inventory.json").read_bytes())
                supplier["packages"][0]["notices"] = notices
                data = json.dumps(supplier).encode()
                (root / "licenses/documentation/browser-inventory.json").write_bytes(data)
                (root / "assets/docs/generated/browser-inventory.json").write_bytes(data)
                manifest = json.loads((root / "assets/docs/documentation-bundle.json").read_bytes())
                entry = next(e for e in manifest["files"] if e["path"] == "generated/browser-inventory.json")
                entry.update(size=len(data), sha256=hashlib.sha256(data).hexdigest())
                for target in (root / "assets/docs", root / "licenses/documentation"):
                    (target / "documentation-bundle.json").write_text(json.dumps(manifest))
                with self.assertRaisesRegex(ValueError, "notice|path"):
                    ui.documentation_inventory(root)

    def test_manual_metadata_rejections(self):
        for change in (lambda v: v.update(base="/other/"), lambda v: v.update(scriptHashes=["sha256-invalid"]),
                       lambda v: v["files"][0].update(path="../secret"), lambda v: v["files"].append(v["files"][0]),
                       lambda v: v["files"][0].update(size=99)):
            with self.subTest(change=change), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                fixture(root)
                target = root / "assets/docs/documentation-bundle.json"
                value = json.loads(target.read_bytes())
                change(value)
                target.write_text(json.dumps(value))
                (root / "licenses/documentation/documentation-bundle.json").write_bytes(target.read_bytes())
                with self.assertRaises((ValueError, OSError)):
                    ui.documentation_inventory(root)

    def test_rejects_incomplete_unsafe_and_inconsistent_inventory(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            original = fixture(root)
            for change in [lambda v: v.update(schemaVersion=2), lambda v: v.update(packages=[]),
                           lambda v: v["assets"].pop(), lambda v: v["assets"].append(v["assets"][0]),
                           lambda v: v["assets"][0].update(path="../outside"),
                           lambda v: v["packages"][0].update(notices=["../project/LICENSE"]),
                           lambda v: v["packages"][0].update(name="wrong"),
                           lambda v: v["packages"][0].update(license="")]:
                value = copy.deepcopy(original)
                change(value)
                (root / "licenses/frontend/inventory.json").write_text(json.dumps(value))
                with self.assertRaises((ValueError, OSError)):
                    ui.inventory(root)
            (root / "licenses/frontend/inventory.json").write_text(json.dumps(original))
            (root / "assets/link").symlink_to(root / "assets/main.js")
            with self.assertRaisesRegex(ValueError, "file type"):
                ui.inventory(root)
            with self.assertRaises(ValueError):
                ui.supplement({}, root, IDENTIFIER, "linux/amd64")


if __name__ == "__main__":
    unittest.main()
