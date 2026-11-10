"""Isolated Plugin consumer assertions; no SDK, processes, sockets or databases."""

import copy
import io
import json
from pathlib import Path
import stat
import tarfile
import tempfile
import unittest
from unittest.mock import patch
import zipfile

from common import Result
import plugins_archive as archive
import plugins_contract as contract
import plugins_data as data
import plugins_tools as tools


def zipped(entries=None):
    stream = io.BytesIO()
    with zipfile.ZipFile(stream, "w", zipfile.ZIP_DEFLATED) as z:
        for name, body, mode in entries or [
            (p, b, stat.S_IFREG | 0o444) for p, b in data.files().items()
        ]:
            info = zipfile.ZipInfo(name)
            info.external_attr = mode << 16
            z.writestr(info, body)
    return stream.getvalue()


def representation():
    raw = zipped()
    files, dirs = archive.decode(raw)
    rep = dict(
        size=len(raw),
        contentDigest=archive.digest(raw),
        plugin=dict(
            rootDirectory="",
            directories=sorted(dirs),
            expandedSize=sum(len(b) for b, _ in files.values()),
            files=[
                dict(path=p, size=len(b), digest=archive.digest(b), executable=x)
                for p, (b, x) in files.items()
            ],
            documents=[
                dict(path=p, content=b.decode())
                for p, (b, _) in files.items()
                if p in ("plugin.json", "mcp.json", "skills/owned-skill/SKILL.md")
            ],
            components=[
                dict(
                    kind="skill", name="owned-skill", path="skills/owned-skill/SKILL.md"
                ),
                dict(kind="mcp_server", name="owned", path="mcp.json"),
            ],
            assessments=dict.fromkeys(
                ("malware", "secrets", "license", "sbom", "provenance"), "not_assessed"
            ),
        ),
    )
    return raw, rep


def outcome(case):
    path = "/packages/" + case
    v = dict(seconds=1, observer_control=True, requests=[], executed=False)
    if case in contract.FATAL:
        v.update(
            error_type="ValueError",
            error=contract.FATAL[case] + path + "/plugin.json: rejected",
        )
        return Result(1, json.dumps(v), "")
    root = path + "/skills/" + data.SKILL
    v.update(
        name=data.NAME,
        version="1.0.0",
        path=path,
        manifest=dict(data.MANIFEST, author=None, entry_command=None),
        skills=[
            dict(
                name=data.SKILL,
                description=data.DESCRIPTION,
                content=data.BODY,
                source=root + "/SKILL.md",
                resources=dict(
                    skill_root=root,
                    scripts=["sentinel.py"],
                    references=["guide.txt"],
                    assets=[],
                ),
            )
        ],
        mcp={"owned": dict(url=data.URL, transport="streamable-http")},
        hooks=False,
        agents=0,
        commands=0,
        entry=None,
    )
    if case == "missing-schema":
        v.update(name="legacy-decoy", version="9.9.9", mcp={},
                 manifest=dict(name="legacy-decoy", version="9.9.9", description="", author=None, entry_command=None))
    return Result(
        0,
        json.dumps(v),
        "Skipping MCP server agent_plugins_mcp.py:137\n 'invalid' in /fixture/mcp.json",
    )


def report():
    r = {
        k: True
        for k in (
            "cleanup",
            "source_unchanged",
            "isolation",
            "governed",
            "immutable",
            "retained",
            "no_execution",
            "source",
            "tools",
            "binaries",
            "selection",
            "network_denial",
            "later_archive_distinct",
        )
    }
    r["refusals"] = dict.fromkeys(data.REFUSALS, True)
    r["native"] = [
        dict(
            case=c,
            exit=outcome(c).code,
            stdout=outcome(c).stdout,
            stderr=outcome(c).stderr,
        )
        for c in (*data.DIRECT, "governed")
    ]
    r["archive_reads"] = [
        dict(
            method="GET", path="/original-plugin.zip", authorization=False, cookie=False
        )
    ]
    return r


class PluginConsumerContracts(unittest.TestCase):
    def test_exact_materialization_preserves_files_and_detects_changes(self):
        raw, rep = representation()
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "package"
            files, dirs = archive.materialize(raw, rep, root)
            self.assertEqual({p: b for p, (b, _) in files.items()}, data.files())
            archive.unchanged(root, files, dirs)
            (root / "extra").write_text("unexpected")
            with self.assertRaises(AssertionError):
                archive.unchanged(root, files, dirs)

    def test_integrity_and_inventory_refuse_before_writes(self):
        raw, rep = representation()
        changes = []
        for field, value in (("size", 0), ("contentDigest", "sha256:wrong")):
            altered = copy.deepcopy(rep)
            altered[field] = value
            changes.append(altered)
        for field, value in (
            ("rootDirectory", "other"),
            ("directories", []),
            ("expandedSize", 0),
            ("components", []),
            ("documents", []),
            ("files", []),
            ("assessments", {"malware": "passed"}),
        ):
            altered = copy.deepcopy(rep)
            altered["plugin"][field] = value
            changes.append(altered)
        for field, value in (
            ("path", "other"),
            ("size", 0),
            ("digest", "bad"),
            ("executable", True),
        ):
            altered = copy.deepcopy(rep)
            altered["plugin"]["files"][0][field] = value
            changes.append(altered)
        altered = copy.deepcopy(rep)
        altered["plugin"]["files"].append(altered["plugin"]["files"][0])
        changes.append(altered)
        with tempfile.TemporaryDirectory() as tmp:
            for altered in changes:
                root = Path(tmp) / "no-write"
                with self.assertRaises((AssertionError, ValueError)):
                    archive.materialize(raw, altered, root)
                self.assertFalse(root.exists())
            for changed, reason in (
                (raw[:-1], "size"),
                (bytes([raw[0] ^ 1]) + raw[1:], "digest"),
            ):
                with self.assertRaisesRegex(
                    archive.IntegrityError, "archive " + reason + " mismatch"
                ):
                    archive.materialize(changed, rep, Path(tmp) / "no-write")

    def test_zip_paths_types_duplicates_and_bounds(self):
        base = [("plugin.json", b"{}", stat.S_IFREG | 0o444)]
        for name in (
            "../escape",
            "/absolute",
            "./relative",
            "a//b",
            "a\\b",
            "a:b",
            "a\x01b",
            "x" * 1025,
            "/".join(["a"] * 18),
            "dir//",
        ):
            with self.subTest(name=name), self.assertRaises(ValueError):
                archive.decode(zipped(base + [(name, b"x", stat.S_IFREG | 0o444)]))
        for mode in (stat.S_IFLNK, stat.S_IFIFO, stat.S_IFSOCK, stat.S_IFCHR):
            with self.assertRaises(ValueError):
                archive.decode(zipped(base + [("special", b"other", mode | 0o777)]))
        for extra in (
            [("PLUGIN.JSON", b"{}", stat.S_IFREG | 0o444)],
            [("plugin.json/child", b"{}", stat.S_IFREG | 0o444)],
            [
                ("DIR/file", b"", stat.S_IFREG | 0o444),
                ("dir/", b"", stat.S_IFDIR | 0o555),
            ],
        ):
            with self.assertRaises(ValueError):
                archive.decode(zipped(base + extra))
        with self.assertRaises(ValueError):
            archive.decode(zipped([("absent", b"{}", stat.S_IFREG | 0o444)]))
        for key, value in (
            ("MAX_ARCHIVE", 1),
            ("MAX_FILE", 1),
            ("MAX_FILES", 0),
            ("MAX_EXPANDED", 1),
        ):
            with patch.object(archive, key, value), self.assertRaises(ValueError):
                archive.decode(zipped())
        raw = zipped(base + [("exec", b"print(1)", stat.S_IFREG | 0o555)])
        self.assertEqual(archive.decode(raw)[0]["exec"], (b"print(1)", True))

    def test_native_contract_rejects_wrong_identity_refusal_and_side_effects(self):
        for case in (*data.DIRECT, "governed"):
            result = outcome(case)
            contract.native(result, case, "/packages/" + case)
            original = json.loads(result.stdout)
            changes = [
                dict(original, requests=["/mcp"]),
                dict(original, executed=True),
                dict(original, observer_control=False),
            ]
            changes += (
                [dict(original, error="unrelated")]
                if case in contract.FATAL
                else [
                    dict(original, name="wrong-identity"),
                    dict(original, mcp={"unexpected": {}} if case == "missing-schema" else {}),
                    dict(original, entry="run"),
                    dict(original, skills=[]),
                ]
            )
            for value in changes:
                with self.assertRaises(AssertionError):
                    contract.native(
                        Result(result.code, json.dumps(value), result.stderr),
                        case,
                        "/packages/" + case,
                    )
            with self.assertRaises(AssertionError):
                contract.native(
                    Result(17, result.stdout, result.stderr), case, "/packages/" + case
                )

    def test_report_rejects_incomplete_duplicate_stale_and_dirty_outcomes(self):
        good = report()
        contract.complete(good)
        changes = []
        for key in (
            "cleanup",
            "source_unchanged",
            "governed",
            "retained",
            "network_denial",
            "isolation",
        ):
            changed = copy.deepcopy(good)
            changed[key] = False
            changes.append(changed)
        changed = copy.deepcopy(good)
        changed["native"].pop()
        changes.append(changed)
        changed = copy.deepcopy(good)
        changed["native"][-1] = changed["native"][0]
        changes.append(changed)
        changed = copy.deepcopy(good)
        changed["refusals"]["digest"] = False
        changes.append(changed)
        changed = copy.deepcopy(good)
        changed["archive_reads"][0]["authorization"] = True
        changes.append(changed)
        for changed in changes:
            with self.assertRaises(AssertionError):
                contract.complete(changed)

    def test_source_archive_requires_exact_regular_files(self):
        def tar(mode="file", body=b"code"):
            stream = io.BytesIO()
            with tarfile.open(fileobj=stream, mode="w:gz") as t:
                info = tarfile.TarInfo("root/module.py")
                info.size = len(body)
                if mode == "link":
                    info.type = tarfile.SYMTYPE
                    info.linkname = "/other"
                    info.size = 0
                t.addfile(info, io.BytesIO(body))
            return stream.getvalue()

        pins = {"files": {"module.py": archive.digest(b"code").removeprefix("sha256:")}}
        with tempfile.TemporaryDirectory() as tmp:
            tools.unpack_source(tar(), pins, Path(tmp) / "valid")
            for raw in (tar("link"), tar(body=b"changed")):
                with self.assertRaises(ValueError):
                    tools.unpack_source(raw, pins, Path(tmp) / "bad")
            with self.assertRaises(ValueError):
                tools.unpack_source(
                    tar(), {"files": {"missing": "digest"}}, Path(tmp) / "bad"
                )

    def test_nul_zip_name_and_incomplete_installed_source_are_rejected(self):
        raw = zipped(
            [
                ("plugin.json", b"{}", stat.S_IFREG | 0o444),
                ("badXname", b"x", stat.S_IFREG | 0o444),
            ]
        )
        # ZIP's Python reader truncates filename at NUL; compare the original spelling too.
        raw = raw.replace(b"badXname", b"bad\x00name")
        with self.assertRaisesRegex(ValueError, "noncanonical"):
            archive.decode(raw)
        pins = {
            "version": "1.0",
            "files": {"openhands-sdk/openhands/sdk/module.py": "hash"},
        }
        good = {"version": "1.0", "source": {"module.py": "hash"}}
        tools.inventory_matches(good, pins)
        for bad in (
            {"version": "other", "source": good["source"]},
            {"version": "1.0", "source": {}},
            {"version": "1.0", "source": {"module.py": "changed"}},
        ):
            with self.assertRaises(ValueError):
                tools.inventory_matches(bad, pins)

    def test_missing_assessment_cannot_pass_inventory_verification(self):
        raw, rep = representation()
        files, dirs = archive.decode(raw)
        archive.verify_inventory(files, dirs, rep)
        for field in rep["plugin"]["assessments"]:
            altered = copy.deepcopy(rep)
            del altered["plugin"]["assessments"][field]
            with self.subTest(field=field), self.assertRaises(AssertionError):
                archive.verify_inventory(files, dirs, altered)

    def test_effective_container_resource_limits_are_verified(self):
        info = {
            "HostConfig": {
                "NetworkMode": "none",
                "ReadonlyRootfs": True,
                "Privileged": False,
                "CapDrop": ["ALL"],
                "SecurityOpt": ["no-new-privileges"],
                "PortBindings": {},
                "Memory": 2 * 1024**3,
                "NanoCpus": 2 * 10**9,
                "PidsLimit": 256,
                "Tmpfs": {"/tmp": "rw,noexec,nosuid,size=128m"},
            },
            "Config": {"User": "1000:1000"},
            "Mounts": [],
        }

        def check(value):
            with (
                patch.object(tools.os, "getuid", return_value=1000),
                patch.object(tools.os, "getgid", return_value=1000),
                patch.object(tools.signal, "pthread_sigmask"),
                patch.object(
                    tools, "docker", return_value=Result(0, json.dumps([value]), "")
                ),
            ):
                tools.create("catalog-plugins-unit", "sha256:fixture")

        check(info)
        for key, value in (
            ("Memory", 0),
            ("NanoCpus", 0),
            ("PidsLimit", -1),
            ("Tmpfs", {}),
            ("Tmpfs", {"/tmp": "rw,size=128m"}),
            ("Tmpfs", {"/tmp": "rw,noexec,nosuid,size=1g"}),
        ):
            changed = copy.deepcopy(info)
            changed["HostConfig"][key] = value
            with self.subTest(key=key, value=value), self.assertRaises(AssertionError):
                check(changed)

    def test_cleanup_attempts_every_owned_name_even_when_one_fails(self):
        calls = []

        def docker(*args, **_kwargs):
            calls.append(args)
            return Result(
                1 if args[:3] == ("rm", "--force", "second") else 0, "", "failure"
            )

        with (
            patch.object(tools, "docker", side_effect=docker),
            self.assertRaises(RuntimeError),
        ):
            tools.remove(["first", "second"])
        self.assertIn(("rm", "--force", "first"), calls)


if __name__ == "__main__":
    unittest.main()
