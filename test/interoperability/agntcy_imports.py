"""Selected native imports against the pinned, supervisor-owned Directory only."""

import base64
import hashlib
import json
import re
import shutil
import urllib.parse

from agntcy_contract import document_digest
from agntcy_lab import ASSETS, read_json
from agntcy_tools import TOOLS
from peer_contract import A2A, MCP

ROOT = ASSETS.parents[2]
DEPENDENCIES = {
    "github.com/agntcy/dir-importer": "v1.5.5",
    "github.com/agntcy/oasf-sdk/pkg": "v1.3.1-0.20260910081500-f0c6172a010a",
}


def summary(result, *, dry=False, total=1):
    """Exit zero alone is not success: the native CLI prints per-record failures."""
    assert result.code == 0, "native import command failed: " + result.stderr[:2000]
    values = {}
    for label in ("Total records", "Imported", "Skipped", "Failed"):
        matches = re.findall(r"^" + label + r":\s+(\d+)\s*$", result.stdout, re.MULTILINE)
        assert len(matches) == 1, "missing or duplicate import count: " + label
        values[label] = int(matches[0])
    assert values["Total records"] == total, "native import did not process the selected record count"
    assert ("This was a dry run." in result.stdout) is dry, "unexpected native import mode"
    assert sum(values[key] for key in ("Imported", "Skipped", "Failed")) == (0 if dry else total), "inconsistent import counts"
    return values


def original_artifact(record, kind, original):
    """Check embedded input independently of the translator's generated record digest."""
    modules = record["modules"]
    assert len(modules) == 1, "unexpected translated module count"
    module = modules[0]
    data = module["data"]
    if kind == "a2a":
        assert data["card_data"] == original, "translator changed A2A card"
    elif kind == "mcp":
        assert data["mcp_data"] == original, "translator changed MCP input"
    else:
        assert data["skill_file"] == "SKILL.md", "translator changed Skill entrypoint"
    # The original artifact is a second preservation path, not a valid OASF schema guarantee.
    artifact = module["artifact"]
    retained = base64.b64decode(artifact["data"], validate=True)
    assert artifact["size"] == len(retained), "native artifact size differs"
    assert artifact["digest"] == "sha256:" + hashlib.sha256(retained).hexdigest(), "native artifact digest differs"
    if kind == "agent-skill":
        assert retained == original, "Skill artifact bytes differ"
    else:
        assert json.loads(retained) == original, "native artifact differs"
    return dict(module=module["name"], artifact_media_type=artifact["media_type"],
                artifact_sha256=hashlib.sha256(retained).hexdigest(), record_sha256=document_digest(record))


def import_rejection(result, cid_path, *, kind=None):
    """Only the observed schema rejection qualifies; transport/model faults must fail."""
    counts = summary(result)
    assert counts == {"Total records": 1, "Imported": 0, "Skipped": 0, "Failed": 1}, "native admission outcome changed"
    diagnostics = result.stdout + result.stderr
    assert 'code = InvalidArgument desc = record validation failed:' in diagnostics and 'Unknown attribute at "modules[0].artifact"' in diagnostics, "not the selected native schema rejection"
    if kind == "agent-skill":
        for path in ("modules[0].data.source_locator", "modules[0].data.validation"):
            assert 'Required attribute "' + path + '" is missing' in diagnostics, "native Skill schema rejection changed: " + path
    assert not cid_path.exists(), "rejected native import returned a CID"
    return dict(counts=counts, disposition="unsupported_native_schema", exit_code=result.code,
                stdout=result.stdout[:12000], stderr=result.stderr[:4000], returned_cids=[])


def mcp_profiles(document):
    """Resolve local pinned schemas only; IDE settings are not Server Cards."""
    from jsonschema import Draft7Validator, Draft202012Validator
    root = ROOT / "schemas/upstream/mcp"
    card = read_json(root / "mcp-server-card-v1-experimental-526201b/schema.json")
    card["$ref"] = "#/$defs/ServerCard"
    registry = read_json(root / "mcp-registry-server-1.8.1-f52dc85/server.schema.json")
    return {name: sorted([dict(path=list(error.absolute_path), validator=error.validator)
                         for error in validator.iter_errors(document)], key=lambda item: str(item["path"]))
            for name, validator in (("server_card", Draft202012Validator(card)), ("registry", Draft7Validator(registry)))}


def nonempty_eligibility(api, origin):
    """Probe actual collection links without granting them arbitrary network authority."""
    manifest, _ = api.document("peer", "/.well-known/ai-catalog.json")
    assert manifest["entries"] == [], "nonempty peer publication requires renewed transfer qualification"
    expected = {A2A: 4, MCP: 2, "application/agent-skills+md": 0, "application/agent-skills+gzip": 0}
    collections = manifest["collections"]
    assert len(collections) == len(expected), "native collection inventory changed"
    evidence = []
    for collection in collections:
        media = collection["mediaType"]
        assert media in expected, "unexpected or duplicate native collection"
        selected = urllib.parse.urlsplit(collection["url"])
        assert selected.scheme + "://" + selected.netloc == origin and selected.path == "/v1/agents" and not selected.fragment, "collection escaped the owned Directory"
        assert urllib.parse.parse_qs(selected.query) == {"filter": ["type=" + media]}, "collection changed its native filter"
        result, _ = api.document("peer", selected.path + "?" + selected.query)
        count = expected.pop(media)
        assert set(result) == {"results", "nextPageToken", "totalCount"} and result["nextPageToken"] == "", "native collection envelope changed"
        assert type(result["totalCount"]) is int and result["totalCount"] == count and isinstance(result["results"], list) and len(result["results"]) == count, "native collection membership changed"
        evidence.append(dict(media_type=media, native_members=count, ard_list_valid=False, response_sha256=document_digest(result)))
    return dict(disposition="unsupported_nonempty", manifest_entries=0, collections=evidence,
                transfer="unrun", native_change="unrun", withdrawal="unrun", outage_recovery="unrun",
                reason="Publisher has no entries; advertised collections use incompatible List envelopes and raw entries fail ARD conformance.")


def qualify(api, lab, private, output, evidence, save):
    """Extend, never replace, the six-record/four-group controls with real file imports."""
    evidence.update(status="running", cases=[dict(kind=kind, status="unrun") for kind in ("a2a", "mcp", "agent-skill")])
    save()
    installed = read_json(TOOLS / "installation.json")
    dependencies = (TOOLS / "cli-dependencies.txt").read_text()
    for name, version in DEPENDENCIES.items():
        assert "\tdep\t" + name + "\t" + version + "\t" in dependencies, "embedded native producer changed"
    evidence["producer"] = dict(revision=installed["revision"], cli_sha256=installed["cli_sha256"],
                                dependencies=DEPENDENCIES, schema_version="1.0.0")
    working = private / "native-imports"
    working.mkdir(mode=0o700)
    card = read_json(ASSETS / "card.json")
    card["name"] = "https://example.test/agntcy-import/a2a"
    mcp = read_json(ROOT / "samples/cards/everything.registry.json")
    profiles = mcp_profiles(mcp)
    assert profiles["registry"] == [] and profiles["server_card"], "MCP input profile changed"
    evidence["mcp_input_profiles"] = profiles
    skill = ROOT / "samples/packages/repository-essentials/skills/review-repository/SKILL.md"
    for selected, (kind, original) in zip(evidence["cases"], (("a2a", card), ("mcp", mcp), ("agent-skill", skill.read_bytes())), strict=True):
        print("AGNTCY native import: " + kind, flush=True)
        selected["status"] = "running"
        save()
        stage = working / kind
        stage.mkdir(mode=0o700)
        source_dir = stage / "input"
        source_dir.mkdir(mode=0o700)
        source = source_dir / ("SKILL.md" if kind == "agent-skill" else "input.json")
        source.write_bytes(original if kind == "agent-skill" else json.dumps(original).encode())
        source.chmod(0o600)
        input_digest = hashlib.sha256(source.read_bytes()).hexdigest()
        config = dict(type=kind, file_path=str(source_dir if kind == "agent-skill" else source),
                      schema_version="1.0.0", limit=1, enricher={"static": {"skills": [{
                          "name": "natural_language_processing/natural_language_understanding/contextual_comprehension", "id": 10101}]}})
        path, cid_path, records = stage / "config.json", stage / "cids.txt", stage / "records"
        # JSON is a YAML subset accepted by the native --config parser.
        path.write_text(json.dumps(config))
        path.chmod(0o600)
        args = ("import", "--config", str(path), "--output-cids", str(cid_path))
        dry = lab.native(*args, "--force", "--dry-run", "--output-dir", str(records))
        selected["translation_counts"] = summary(dry, dry=True)
        files = list(records.glob("*.record.json"))
        assert len(files) == 1, "native translation did not write one exact record"
        translated = read_json(files[0])
        selected.update(original_artifact(translated, kind, original), input_sha256=input_digest)
        # Retain exact generated bytes, without rewriting the peer's record.
        shutil.copyfile(files[0], output / ("import-" + kind + ".record.json"))
        # The pinned producer emits module.artifact, rejected by the pinned OASF schema.
        # Both first and repeat admission must fail for that cause, with no CID.
        selected["attempts"] = []
        for _ in range(2):
            selected["attempts"].append(import_rejection(lab.native(*args, check=False), cid_path, kind=kind))
            save()
        assert hashlib.sha256(source.read_bytes()).hexdigest() == input_digest, "native importer modified its input"
        selected["status"] = "pass"
        save()
    malformed = working / "malformed.json"
    malformed.write_text("{")
    config["type"], config["file_path"] = "a2a", str(malformed)
    path.write_text(json.dumps(config))
    rejected = lab.native("import", "--config", str(path), "--force", check=False)
    counts = summary(rejected, total=0)
    assert "fetch error: decode JSON object: unexpected end of JSON input" in rejected.stdout, "malformed-input control failed for another cause"
    evidence["malformed"] = dict(counts=counts, stdout=rejected.stdout[:12000], stderr=rejected.stderr[:4000])
    # No import may silently pollute storage or the baseline projection.
    cids = [row["cid"] for row in read_json(ASSETS / "corpus.json")]
    native = json.loads(lab.native("search", "--limit", "10", "--output", "json").stdout)
    assert sorted(native) == sorted(cids), "native import changed the six-record inventory"
    federation = nonempty_eligibility(api, lab.lock["tls_origin"])
    corpus = {row["name"]: row for row in read_json(ASSETS / "corpus.json")}
    exported = json.loads(lab.native("export", corpus["mcp"]["cid"], "--format", "mcp-ghcopilot").stdout)
    assert isinstance(exported.get("servers"), dict) and exported["servers"], "MCP IDE export changed"
    profiles = mcp_profiles(exported)
    assert all(profiles.values()), "native MCP export gained a supported profile; qualify it explicitly"
    evidence["mcp_export_profiles"] = profiles
    (output / "native-mcp-ide-export.json").write_text(json.dumps(exported, indent=2) + "\n")
    evidence.update(status="pass", stored_after=6, plugin="no_native_mapping", mcp_export="ide_connection_settings",
                    federation=federation)
    save()
