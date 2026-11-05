"""Client provenance and stale-install admission without external tools."""

import copy
from datetime import datetime, timedelta, timezone
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from common import Result
import mcp_catalog_tools as tools


class Provenance(unittest.TestCase):
    def test_exact_wheel_and_fourteen_day_boundary(self):
        expected = tools.provenance()["mcp"]
        actual = {"info": {"version": expected["version"]}, "urls": copy.deepcopy(expected["wheels"])}
        published = datetime.fromisoformat(expected["wheels"][0]["upload_time_iso_8601"].replace("Z", "+00:00"))
        tools.validate_metadata(expected, actual, published + timedelta(days=14))
        with self.assertRaisesRegex(AssertionError, "too recent"):
            tools.validate_metadata(expected, actual, published + timedelta(days=14, microseconds=-1))
        for field, value in (("url", "https://example.invalid/changed.whl"),
                             ("digests", {"sha256": "0" * 64}),
                             ("upload_time_iso_8601", "2020-01-01T00:00:00Z")):
            changed = copy.deepcopy(actual)
            changed["urls"][0][field] = value
            with self.subTest(field=field), self.assertRaises(AssertionError):
                tools.validate_metadata(expected, changed, datetime.now(timezone.utc))
        changed = copy.deepcopy(actual)
        changed["info"]["version"] = "unreviewed"
        with self.assertRaisesRegex(AssertionError, "version drift"):
            tools.validate_metadata(expected, changed, datetime.now(timezone.utc))
        with tempfile.TemporaryDirectory() as name:
            directory = Path(name)
            (directory / "requirements.txt").write_text("unrelated lock\n")
            with patch.object(tools, "INPUTS", directory), self.assertRaisesRegex(AssertionError, "absent from lock"):
                tools.validate_metadata(expected, actual, datetime.now(timezone.utc))

    def test_installed_rejects_stale_inputs_inventory_provenance_and_image(self):
        with tempfile.TemporaryDirectory() as name:
            directory = Path(name)
            inventory = b'{"packages":[],"sdk_source_sha256":"recorded"}\n'
            (directory / "dependencies.json").write_bytes(inventory)
            stamp = dict(identity="current", image="sha256:owned", packages={"mcp": "pinned"},
                         inventory_sha256=hashlib.sha256(inventory).hexdigest())
            file = directory / "installation.json"
            with patch.object(tools, "OUTPUT", directory), patch.object(tools, "STAMP", file), \
                 patch.object(tools, "identity", return_value="current"), \
                 patch.object(tools, "provenance", return_value={"mcp": "pinned"}), \
                 patch.object(tools, "docker", return_value=Result(0, "sha256:owned\n", "")) as docker:
                with self.assertRaisesRegex(RuntimeError, "mcp-catalog-tools-install"):
                    tools.installed()
                file.write_text(json.dumps(stamp))
                self.assertEqual(tools.installed(), stamp)
                for field, value in (("identity", "old"), ("image", "tag-only"),
                                     ("packages", {"mcp": "unreviewed"}), ("inventory_sha256", "bad")):
                    file.write_text(json.dumps(dict(stamp, **{field: value})))
                    with self.subTest(field=field), self.assertRaises(RuntimeError):
                        tools.installed()
                file.write_text(json.dumps(stamp))
                docker.return_value = Result(0, "sha256:another\n", "")
                with self.assertRaises(RuntimeError):
                    tools.installed()
                file.write_text("null")
                with self.assertRaises(RuntimeError):
                    tools.installed()
