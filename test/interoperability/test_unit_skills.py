"""Isolated Skill consumer contract tests; no Git, native tools, listeners or containers."""

import copy
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from catalog_control import validate_search_record
from skills_catalog import same_retained
from common import Result
import skills_contract as contract
import skills_git as package
import skills_tools as tools


def representation():
    zipped = package.archive(package.FILES)
    files = {package.NAME + "/" + p: data for p, data in package.FILES.items()}
    result = {"contentDigest": package.digest(zipped), "size": len(zipped), "package": {
        "structureState": "passed", "rootDirectory": package.NAME, "fileCount": len(files),
        "expandedSize": sum(len(b) for b, _ in files.values()), "assessments": {"malware": "not_assessed"},
        "files": [{"path": p, "size": len(b), "digest": package.digest(b), "executable": x} for p, (b, x) in files.items()]}}
    return result, files, zipped


class SkillContracts(unittest.TestCase):
    def test_search_display_is_optional_but_must_match_exact_record(self):
        sections = ("resource", "version", "versionRevision", "listing", "listingRevision", "release", "evidence")
        record = {key: {"id": key} for key in sections}
        record["listingRevision"].update(displayName="name", description="description", tags=["tag"], publisherId="publisher")
        display = {"publisherName": "Publisher", "namespaceName": "Namespace", "license": "Apache-2.0"}
        record["display"] = display
        item = dict(local={key: {"id": key} for key in sections}, displayName="name", description="description", tags=["tag"], publisher="publisher")
        item["local"]["display"] = copy.deepcopy(display)
        validate_search_record(item, record)
        for key in display:
            changed = copy.deepcopy(item)
            changed["local"]["display"][key] = "substituted"
            with self.assertRaises(AssertionError):
                validate_search_record(changed, record)
        changed = copy.deepcopy(item)
        changed["local"]["unexpected"] = {}
        with self.assertRaises(AssertionError):
            validate_search_record(changed, record)
        del item["local"]["display"]
        with self.assertRaises(AssertionError):
            validate_search_record(item, record)
        del record["display"]
        validate_search_record(item, record)

    def test_inventory_requires_every_file_and_original_archive(self):
        rep, files, zipped = representation()
        package.verify_inventory(files, rep, zipped)
        changes = []
        for key, val in (("path", "other"), ("size", 999), ("digest", "sha256:bad"), ("executable", True)):
            altered = copy.deepcopy(rep)
            altered["package"]["files"][0][key] = val
            changes.append(altered)
        for key, val in (("rootDirectory", "other"), ("fileCount", 0), ("expandedSize", 1), ("structureState", "failed")):
            altered = copy.deepcopy(rep)
            altered["package"][key] = val
            changes.append(altered)
        duplicate = copy.deepcopy(rep)
        duplicate["package"]["files"].append(duplicate["package"]["files"][0])
        changes.append(duplicate)
        for altered in changes:
            with self.assertRaises((AssertionError, ValueError)):
                package.verify_inventory(files, altered, zipped)
        for changed in (zipped + b"x", b"different archive"):
            with self.assertRaises(ValueError):
                package.verify_inventory(files, rep, changed)
        for modified in ({}, dict(files, extra=(b"new", False)), dict(files, **{package.NAME + "/SKILL.md": (b"changed", False)})):
            with self.assertRaises(AssertionError):
                package.verify_inventory(modified, rep, zipped)

    def test_git_tree_rejects_links_gitlinks_and_ambiguous_records(self):
        oid = "a" * 40
        self.assertEqual(package.entries(f"100755 blob {oid}\tscripts/sentinel.py\0".encode()), [("scripts/sentinel.py", oid, True)])
        invalid = [b"", b"truncated", f"120000 blob {oid}\tlink\0".encode(), f"160000 commit {oid}\tsubmodule\0".encode(),
                   f"100644 blob {oid}\t../escape\0".encode(), f"100644 blob {oid}\t/absolute\0".encode(),
                   f"100644 blob {oid}\tsame\0".encode()*2, f"100644 blob {oid}\t./name\0".encode(),
                   f"100644 blob {oid}\tbad\\name\0".encode()]
        invalid.append(b"".join(f"100644 blob {oid}\tf{i}\0".encode() for i in range(1025)))
        for raw in invalid:
            with self.subTest(raw=raw[:80]), self.assertRaises(ValueError):
                package.entries(raw)

    def test_materializer_refuses_unmapped_noncommit_and_oversized_before_write(self):
        locator = dict(repository=package.REPOSITORY, commit="a"*40, directory=package.DIRECTORY)
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            for changed in (dict(locator, repository="https://other.example/repo"), dict(locator, commit="main"), dict(locator, directory="../owned-skill")):
                with patch.object(package, "git") as git, self.assertRaises(ValueError):
                    package.materialize(root, changed, root / "out")
                git.assert_not_called()
            with patch.object(package, "git", return_value=b"blob\n"), self.assertRaises(ValueError):
                package.materialize(root, locator, root / "out")
            with patch.object(package, "git", side_effect=[b"commit\n", f"100644 blob {'b'*40}\tSKILL.md\0".encode(), b"16777217\n"]), self.assertRaises(ValueError):
                package.materialize(root, locator, root / "out")
            self.assertFalse((root / "out/SKILL.md").exists())

    def test_native_checks_reject_wrong_exits_missing_fields_and_xml_injection(self):
        path = "/packages/valid/owned-skill"
        contract.check_native(Result(0, "Valid skill: " + path, ""), "validate", path)
        good = dict(name=package.NAME, description=package.DESCRIPTION, license="Apache-2.0", metadata={"author": "owned-fixture"})
        contract.check_native(Result(0, json.dumps(good), ""), "read-properties", path)
        refused_path = "/packages/missing-field/owned-skill"
        refused = "Validation failed for " + refused_path + ":\n  - Missing required field in frontmatter: name\n"
        contract.check_native(Result(1, "", refused), "validate", refused_path, valid=False)
        for result in (Result(0, "", ""), Result(2, "", "tool missing"), Result(1, "unexpected success", "error")):
            with self.assertRaises(AssertionError):
                contract.check_native(result, "validate", refused_path, valid=False)
        for message in ("tool unavailable", refused.replace("name", "description"), refused.replace(refused_path, "/other")):
            with self.assertRaises(AssertionError):
                contract.check_native(Result(1, "", message), "validate", refused_path, valid=False)
        for changed in ({}, dict(good, name="other"), dict(good, description="changed")):
            with self.assertRaises(AssertionError):
                contract.check_native(Result(0, json.dumps(changed), ""), "read-properties", path)
        xml = '<available_skills><skill><name>owned-skill</name><description>Inspect invoices &amp; explain &lt;results&gt; safely.</description><location>' + path + '/SKILL.md</location></skill></available_skills>'
        contract.check_native(Result(0, xml, ""), "to-prompt", path)
        for changed in (xml.replace("owned-skill", "other"), xml.replace("</skill>", "<unexpected/></skill>"), xml.replace("/SKILL.md", "/wrong.md")):
            with self.assertRaises(AssertionError):
                contract.check_native(Result(0, changed, ""), "to-prompt", path)

    def test_incomplete_report_cannot_pass(self):
        good = dict(cleanup=True, source_unchanged=True, isolation=True, direct=dict.fromkeys(contract.DIRECT, True),
                    refusals=dict.fromkeys(contract.REFUSALS, True), governed=True, immutable=True, no_execution=True, retained=True,
                    source="source", tools={"image": "image"}, binaries={"catalogd": "digest"}, selection={"release": "selected"}, native=[])
        for case in sorted(contract.DIRECT | {"governed"}):
            path = "/packages/" + case + "/" + ("wrong-parent" if case == "parent-mismatch" else package.NAME)
            valid = case not in contract.NATIVE_REFUSALS
            good["native"].append(dict(command="validate", path=path, exit=0 if valid else 1,
                                       stdout="Valid skill: " + path if valid else "",
                                       stderr="" if valid else "Validation failed for " + path + ":\n  - " + contract.NATIVE_REFUSALS[case]))
            if case in {"valid", "parent-mismatch", "governed"}:
                props = dict(name=package.NAME, description=package.DESCRIPTION, license="Apache-2.0", metadata={"author": "owned-fixture"})
                good["native"].append(dict(command="read-properties", path=path, exit=0, stdout=json.dumps(props), stderr=""))
                xml = '<available_skills><skill><name>owned-skill</name><description>Inspect invoices &amp; explain &lt;results&gt; safely.</description><location>' + path + '/SKILL.md</location></skill></available_skills>'
                good["native"].append(dict(command="to-prompt", path=path, exit=0, stdout=xml, stderr=""))
        contract.complete(good)
        bad = copy.deepcopy(good)
        next(row for row in bad["native"] if row["exit"] == 1)["stderr"] = "unrelated tool error"
        with self.assertRaises(AssertionError):
            contract.complete(bad)
        for rows in (good["native"][:-1], good["native"] + good["native"][:1], [dict(good["native"][0], exit=99)] + good["native"][1:]):
            bad = dict(good, native=rows)
            with self.assertRaises(AssertionError):
                contract.complete(bad)
        for key in good:
            bad = copy.deepcopy(good)
            del bad[key]
            with self.assertRaises((KeyError, AssertionError)):
                contract.complete(bad)
        for key in ("direct", "refusals"):
            bad = copy.deepcopy(good)
            bad[key].pop(next(iter(bad[key])))
            with self.assertRaises(AssertionError):
                contract.complete(bad)

    def test_later_draft_may_move_candidate_but_not_published_evidence(self):
        before = {key: {"id": key} for key in ("release", "versionRevision", "listingRevision", "evidence", "resource", "version", "listing")}
        before["listing"].update(currentReleaseId="release", candidateRevisionId="old")
        after = copy.deepcopy(before)
        after["listing"]["candidateRevisionId"] = "new"
        same_retained(before, after)
        for key in before:
            changed = copy.deepcopy(after)
            changed[key]["id"] = "wrong"
            with self.assertRaises(AssertionError):
                same_retained(before, changed)

    def test_installed_source_must_match_complete_pin(self):
        pins = {"version": "0.1.0", "files": {"skills-ref/src/skills_ref/cli.py": "one"}}
        tools.inventory_matches({"version": "0.1.0", "source": {"cli.py": "one"}}, pins)
        for source in ({}, {"cli.py": "changed"}, {"cli.py": "one", "extra.py": "two"}):
            with self.assertRaises(ValueError):
                tools.inventory_matches({"version": "0.1.0", "source": source}, pins)

    def test_unrelated_failure_is_not_a_consumer_refusal(self):
        for case in ("missing-commit", "missing-directory", "altered-file"):
            with self.assertRaises(AssertionError):
                contract.check_refusal(case, RuntimeError("git unavailable"), 0, 0)
        error = AssertionError("retained file inventory mismatch")
        contract.check_refusal("altered-file", error, 1, 1)
        with self.assertRaises(AssertionError):
            contract.check_refusal("altered-file", error, 1, 2)

    def test_cleanup_attempts_all_owned_containers(self):
        with patch.object(tools, "docker", side_effect=[RuntimeError("lost"), Result(0, "", ""), Result(0, "", "")]) as docker:
            with self.assertRaises(RuntimeError):
                tools.remove(["first", "second"])
            self.assertEqual(docker.call_args_list[0].args[:3], ("rm", "--force", "second"))
            self.assertEqual(docker.call_args_list[1].args[:3], ("rm", "--force", "first"))


if __name__ == "__main__":
    unittest.main()
