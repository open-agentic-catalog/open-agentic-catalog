"""Exact binary build identities and native SPDX associations, without tools."""
import copy
import unittest
from unittest.mock import patch

import image
import trivy


def build_info(name, main_version="(devel)"):
    return {"goBuildInfo": name + ": go1.27.3\n\tmod\texample.test/catalog\t" + main_version + "\t\n"
            "\tdep\texample.test/shared\tv1.2.3\th1:fixture\n"
            "\tdep\texample.test/" + name + "\tv2.0.0\th1:fixture\n"}


def native_fixture(names=("catalogd", "catalogctl"), main_version="(devel)"):
    """Mirror Trivy's observed APPLICATION -> main module -> dependency shape."""
    document = {"spdxVersion": "SPDX-2.3", "packages": [], "relationships": []}
    inspected = {"binaries": {name: build_info(name, main_version) for name in names}}
    for name in names:
        app_id, main_id = "SPDXRef-App-" + name, "SPDXRef-Main-" + name
        document["packages"].append({"SPDXID": app_id, "name": "usr/local/bin/" + name, "primaryPackagePurpose": "APPLICATION"})
        document["relationships"].append({"spdxElementId": app_id, "relationshipType": "CONTAINS", "relatedSpdxElement": main_id})
        for suffix, module, version in [("main", "example.test/catalog", "" if main_version == "(devel)" else main_version),
                                        ("shared", "example.test/shared", "v1.2.3"),
                                        ("own", "example.test/" + name, "v2.0.0"), ("stdlib", "stdlib", "v1.27.3")]:
            package_id = main_id if suffix == "main" else "SPDXRef-" + name + "-" + suffix
            package = {"SPDXID": package_id, "name": module, "sourceInfo": "package found in: usr/local/bin/" + name}
            if version:
                package["versionInfo"] = version
            document["packages"].append(package)
            if suffix != "main":
                document["relationships"].append({"spdxElementId": main_id, "relationshipType": "DEPENDS_ON", "relatedSpdxElement": package_id})
    return document, inspected


class GoInventoryTests(unittest.TestCase):
    def test_each_binary_requires_exact_dependency_and_standard_library_versions(self):
        for names in (("catalogd", "catalogctl"), ("catalog-ui",)):
            for name in names:
                for suffix in ("own", "stdlib"):
                    for change in ("missing", "wrong-version", "missing-version"):
                        with self.subTest(binary=name, package=suffix, change=change):
                            document, inspected = native_fixture(names)
                            trivy.spdx_go_inventory(document, inspected)
                            identifier = "SPDXRef-" + name + "-" + suffix
                            package = next(p for p in document["packages"] if p["SPDXID"] == identifier)
                            if change == "missing":
                                document["packages"].remove(package)
                                document["relationships"] = [r for r in document["relationships"]
                                                             if r["relatedSpdxElement"] != identifier]
                            elif change == "wrong-version":
                                package["versionInfo"] = "v0.0.1"
                            else:
                                del package["versionInfo"]
                            with self.assertRaisesRegex(ValueError, "inventory mismatch: " + name):
                                trivy.spdx_go_inventory(document, inspected)

    def test_valid_duplicate_nodes_extra_content_and_package_urls_are_unchanged(self):
        for names in (("catalogd", "catalogctl"), ("catalog-ui",)):
            document, inspected = native_fixture(names)
            for name in names:
                original = next(p for p in document["packages"] if p["SPDXID"] == "SPDXRef-" + name + "-own")
                original["externalRefs"] = [{"referenceCategory": "PACKAGE-MANAGER", "referenceType": "purl",
                                             "referenceLocator": "pkg:golang/" + original["name"] + "@v2.0.0"}]
                duplicate = copy.deepcopy(original)
                duplicate["SPDXID"] += "-duplicate"
                document["packages"].append(duplicate)
                document["relationships"].append({"spdxElementId": "SPDXRef-Main-" + name,
                                                  "relationshipType": "DEPENDS_ON", "relatedSpdxElement": duplicate["SPDXID"]})
                main = next(p for p in document["packages"] if p["SPDXID"] == "SPDXRef-Main-" + name)
                main["versionInfo"] = ""  # Both omitted and explicitly empty development versions are valid.
            document["packages"].append({"SPDXID": "SPDXRef-Extra", "name": "extra-described-content", "versionInfo": "1"})
            before = copy.deepcopy(document)
            trivy.spdx_go_inventory(document, inspected)
            self.assertEqual(document, before)

    def test_malformed_or_ambiguous_application_graphs_fail(self):
        for change, expected in (("application", "application"), ("main-version", "main module"),
                                 ("duplicate-id", "ambiguous"), ("unresolved", "unresolved"),
                                 ("source", "another application"), ("wrong-type", "package identity")):
            with self.subTest(change=change):
                document, inspected = native_fixture()
                trivy.spdx_go_inventory(document, inspected)
                if change == "application":
                    document["packages"][0]["primaryPackagePurpose"] = "LIBRARY"
                elif change == "main-version":
                    document["packages"][1]["versionInfo"] = "v9.9.9"
                elif change == "duplicate-id":
                    document["packages"].append(copy.deepcopy(document["packages"][1]))
                elif change == "unresolved":
                    document["relationships"][1]["relatedSpdxElement"] = "SPDXRef-Missing"
                elif change == "source":
                    document["packages"][1].pop("sourceInfo")
                else:
                    document["packages"][1]["versionInfo"] = None
                with self.assertRaisesRegex(ValueError, expected):
                    trivy.spdx_go_inventory(document, inspected)

    def test_invalid_document_shapes_fail_with_a_validation_error(self):
        _, inspected = native_fixture()
        for document in (None, [], "invalid", {}, {"spdxVersion": "SPDX-2.3", "packages": None, "relationships": []}):
            with self.subTest(document=document), self.assertRaisesRegex(ValueError, "SPDX Go inventory"):
                trivy.spdx_go_inventory(document, inspected)

    def test_linked_inventory_keeps_main_version_and_compiler_stdlib(self):
        for version in ("(devel)", "", "v0.1.0"):
            with self.subTest(version=version):
                main, packages = trivy.go_inventory(build_info("catalogd", version))
                self.assertEqual(main, ("example.test/catalog", "" if version == "(devel)" else version))
                self.assertEqual(packages, {main, ("stdlib", "v1.27.3"),
                                           ("example.test/shared", "v1.2.3"), ("example.test/catalogd", "v2.0.0")})

    def test_incomplete_build_info_is_not_an_inventory(self):
        original = build_info("catalogd")["goBuildInfo"]
        for raw in ("", original.replace("go1.27.3", "unknown"),
                    original.replace("\tmod\t", "\tpath\t"),
                    original.replace("\tdep\texample.test/shared\tv1.2.3", "\tdep\texample.test/shared\t")):
            with self.subTest(raw=raw), self.assertRaisesRegex(ValueError, "Go build identity"):
                trivy.go_inventory({"goBuildInfo": raw})

    def test_native_per_application_nodes_and_empty_main_versions(self):
        for names in (("catalogd", "catalogctl"), ("catalog-ui",)):
            for version in ("(devel)", "", "v0.1.0"):
                document, inspected = native_fixture(names, version)
                before = copy.deepcopy(document)
                trivy.spdx_go_inventory(document, inspected)
                self.assertEqual(document, before, "validation must preserve native nodes and relationships")

    def test_a_global_union_cannot_replace_one_applications_dependencies(self):
        document, inspected = native_fixture()
        # The shared module still exists for both binaries, but catalogd's edge
        # is absent. Looking only at names/versions across the image would pass.
        document["relationships"] = [r for r in document["relationships"]
                                     if r["relatedSpdxElement"] != "SPDXRef-catalogd-shared"]
        with self.assertRaisesRegex(ValueError, "inventory mismatch: catalogd"):
            trivy.spdx_go_inventory(document, inspected)
        # Borrowing another binary's main-module graph is not an association.
        document, inspected = native_fixture()
        edge = next(r for r in document["relationships"] if r["spdxElementId"] == "SPDXRef-App-catalogd")
        edge["relatedSpdxElement"] = "SPDXRef-Main-catalogctl"
        with self.assertRaisesRegex(ValueError, "another application"):
            trivy.spdx_go_inventory(document, inspected)

    def test_standalone_generation_checks_go_before_adding_supplements(self):
        from argparse import Namespace
        import json
        from pathlib import Path
        import tempfile
        options = Namespace(engine="docker", trivy="selected-trivy", platform="linux/amd64", profile="backend")
        document, inspected = native_fixture()
        document["name"] = inspected["imageId"] = "sha256:" + "a" * 64
        def convert(command, **kwargs):
            Path(command[command.index("--output") + 1]).write_text(json.dumps(document))
        with tempfile.TemporaryDirectory() as directory, patch.object(trivy, "qualify"), patch.object(trivy, "execute", side_effect=convert), patch.object(image, "supplement_sbom") as supplement:
            image.make_sbom(options, Path(directory), inspected)
            supplement.assert_called_once()
            document["relationships"] = []
            supplement.reset_mock()
            with self.assertRaisesRegex(ValueError, "main module"):
                image.make_sbom(options, Path(directory), inspected)
            supplement.assert_not_called()
