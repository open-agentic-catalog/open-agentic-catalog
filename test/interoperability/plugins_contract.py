"""Independent native-result and complete-scenario assertions; no SDK imports."""

import json

from plugins_data import BODY, DESCRIPTION, DIRECT, MANIFEST, NAME, REFUSALS, SKILL, URL

FATAL = {
    "malformed": "Invalid JSON in ",
    "unsupported-schema": "Unsupported or missing $schema in ",
    "invalid-name": "Invalid Agent Plugins manifest ",
}


def native(result, case, path):
    value = json.loads(result.stdout)
    assert (
        value["observer_control"] and not value["requests"] and not value["executed"]
    ), "unexpected component execution"
    assert 0 <= value["seconds"] <= 90
    if case in FATAL:
        assert result.code == 1 and value["error_type"] == "ValueError"
        assert value["error"].startswith(FATAL[case] + path + "/plugin.json:"), (
            "wrong native refusal"
        )
        assert "name" not in value and "skills" not in value
    else:
        assert (
            case in (*DIRECT, "governed") and result.code == 0 and "error" not in value
        )
        legacy = case == "missing-schema"
        assert (
            value["name"] == ("legacy-decoy" if legacy else NAME)
            and value["version"] == ("9.9.9" if legacy else "1.0.0")
            and value["path"] == path
        )
        manifest = (dict(name="legacy-decoy", version="9.9.9", description="", author=None, entry_command=None)
                    if legacy else dict(MANIFEST, author=None, entry_command=None))
        assert value["manifest"] == manifest
        root = path + "/skills/" + SKILL
        assert value["skills"] == [
            dict(
                name=SKILL,
                description=DESCRIPTION,
                content=BODY,
                source=root + "/SKILL.md",
                resources=dict(
                    skill_root=root,
                    scripts=["sentinel.py"],
                    references=["guide.txt"],
                    assets=[],
                ),
            )
        ]
        assert value["mcp"] == ({} if legacy else {"owned": {"url": URL, "transport": "streamable-http"}})
        assert (
            not value["hooks"]
            and value["agents"] == value["commands"] == 0
            and value["entry"] is None
        )
        if case == "component-recovery":
            # Rich wraps output; require the upstream entry-specific warning, not arbitrary stderr.
            warning = " ".join(result.stderr.split())
            assert "Skipping MCP server" in warning and "'invalid' in" in warning
    return value


def complete(report):
    from common import Result

    assert all(
        report.get(k)
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
        )
    )
    assert set(report["refusals"]) == set(REFUSALS) and all(report["refusals"].values())
    assert len(report["native"]) == len(DIRECT) + 1
    expected = set(DIRECT) | {"governed"}
    for row in report["native"]:
        assert row["case"] in expected
        expected.remove(row["case"])
        native(
            Result(row["exit"], row["stdout"], row["stderr"]),
            row["case"],
            "/packages/" + row["case"],
        )
    assert not expected
    assert report["archive_reads"] and all(
        r["method"] == "GET"
        and not r["authorization"]
        and not r["cookie"]
        and r["path"] in ("/original-plugin.zip", "/later-plugin.zip")
        for r in report["archive_reads"]
    )
    assert report["network_denial"] and report["later_archive_distinct"]
