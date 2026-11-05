"""Reusable draft interchange within the existing two-Catalog fixture."""

import json
import time

from runner import Failure, command, read_document, write_document


KINDS = ["agent", "mcp_server", "skill", "plugin"]
STAGES = {"prepare", "transfer", "startup", "destination", "source-download", "cli-import", "retained-readback",
          "cli-export", "destination-import", "publication", "publication-readback"}


def valid_failure(value):
    """Allow only a fixed transfer stage, without private child diagnostics."""
    return (isinstance(value, dict) and {"stage"} <= set(value) <= {"stage", "line"}
            and isinstance(value["stage"], str) and value["stage"] in STAGES
            and ("line" not in value or type(value["line"]) is int and 0 < value["line"] < 10000))


def failed_stage(raw, checkpoint=None):
    """Project only a closed stage from a failed child, never its payload."""
    try:
        value = json.loads(raw)
    except (ValueError, UnicodeError):
        return
    if valid_failure(value):
        if checkpoint is not None:
            write_document(checkpoint, value)
        raise Failure("peer-draft-" + value["stage"])


def valid_evidence(value):
    """Require both clients, all kinds, governed publication and owned cleanup."""
    return (isinstance(value, dict)
            and set(value) == {"kinds", "directions", "newResources", "newVersions", "contentOnly",
                               "independentlyPublished", "resourceInvocations", "cleanup"}
            and value["kinds"] == KINDS
            and value["directions"] == ["ui-to-cli", "cli-to-ui"]
            and all(type(value[field]) is int and value[field] == count for field, count in (
                ("newResources", 4), ("newVersions", 4), ("contentOnly", 2),
                ("independentlyPublished", 1), ("resourceInvocations", 0)))
            and value["cleanup"] == "passed")


def qualify(scenario):
    """Reuse the peer browser build and the separate PostgreSQL-backed APIs."""
    checkpoint = scenario.state / "peer-draft-failure.json"
    write_document(checkpoint, {"stage": "prepare"})
    directory = scenario.fixture / "peer-ui"
    previous_manifest = scenario.public.request("GET", "/.well-known/ard.json")[0]
    command(["go", "build", "-mod=readonly", "-o", str(directory / "catalogctl"), "./cmd/catalogctl"],
            scenario.env, min(120, scenario.end - time.monotonic()))
    write_document(directory / "draft-transfer.json", {
        "a": scenario.a.base_url, "b": scenario.b.base_url,
        "publisherA": scenario.publisher, "publisherB": scenario.b_owner.publisher,
        "items": [{"kind": item["resource"]["kind"],
                   "identifier": item["resource"]["canonicalIdentifier"],
                   "namespaceId": item["resource"]["namespaceId"],
                   "resourceId": item["resource"]["id"],
                   "listingId": item["listing"]["id"],
                   "releaseId": item["release"]["id"]} for item in scenario.b_items],
    })
    write_document(checkpoint, {"stage": "transfer"})
    result = json.loads(command(["node", "catalog-ui/scripts/draft-transfer-journeys.mjs", str(directory)],
                                scenario.env, min(180, scenario.end - time.monotonic()), capture=True,
                                failed_stdout=lambda raw: failed_stage(raw, checkpoint)))
    assert valid_evidence(result), "draft transfer qualification incomplete"
    write_document(checkpoint, {"stage": "publication-readback"})
    assert scenario.public.request("GET", "/.well-known/ard.json")[0] == previous_manifest, "draft transfer changed an active immutable snapshot"
    scenario.snapshot(scenario.publisher_client, "peer-a.test")
    manifest = scenario.public.request("GET", "/.well-known/ard.json")[0]
    identifier = scenario.b_items[0]["resource"]["canonicalIdentifier"]
    entries = [entry for entry in manifest["entries"] if entry["identifier"] == identifier]
    assert len(entries) == 1 and entries[0]["type"] == "application/a2a-agent-card+json", "imported release missing from ARD publication"
    assert entries[0]["version"] == "1.0.0" and entries[0]["metadata"]["license"] == "Apache-2.0", "ARD publication changed imported declarations"
    write_document(scenario.state / "peer-drafts.json", result)
    assert valid_evidence(read_document(scenario.state / "peer-drafts.json")), "draft transfer checkpoint invalid"
    checkpoint.unlink()
