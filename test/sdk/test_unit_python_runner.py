"""Python launcher decisions with mocked tools, no process or network calls."""
import json
import hashlib
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import python_runner as runner
import runner as shared


OPEN_MODELS = ("create_listing_revision_extensions", "listing_revision_extensions",
               "ard_search_result", "ard_search_result_trust_manifest",
               "ard_search_response_referrals_inner", "ard_entry")
MODEL_SOURCE = """if True:
    class Generated:
        additional_properties: Dict[str, Any] = {}
        model_config = ConfigDict(
            validate_by_name=True,
        )
        def to_dict(self):
            # Fields in self.additional_properties go in the top level.
            excluded_fields = set([
                "additional_properties",
            ])
            _dict = self.model_dump(
                exclude_none=True,
            )
            if self.additional_properties is not None:
                for _key, _value in self.additional_properties.items():
                    _dict[_key] = _value
            return _dict
        @classmethod
        def from_dict(cls, obj):
            _obj = cls.model_validate({})
            # store additional fields in additional_properties
            for _key in obj.keys():
                if _key not in cls.__properties:
                    _obj.additional_properties[_key] = obj.get(_key)
            return _obj
"""


def write_generator_fixture(base):
    import zipfile
    jar = base / "lib/python3.12/site-packages/openapi_generator_cli/openapi-generator.jar"
    jar.parent.mkdir(parents=True)
    with zipfile.ZipFile(jar, "w") as archive:
        archive.writestr("python/api.mustache", "@validate_call\n")
        archive.writestr("python/partial_api.mustache", "docstring")
        for name in ("model_oneof", "model_generic"):
            archive.writestr("python/" + name + ".mustache", "from __future__ import annotations\nclass {{classname}}: pass\n")


def mock_generation(destination, changed_source=None):
    def run(args, **_):
        if args[1] == "generate":
            models = destination / "catalog_sdk/generated/models"
            models.mkdir(parents=True)
            for name in OPEN_MODELS:
                source = changed_source if changed_source is not None and name == OPEN_MODELS[-1] else MODEL_SOURCE
                (models / (name + ".py")).write_text(source)
        return "output"
    return run


class PythonRunnerUnits(unittest.TestCase):
    def test_package_manifest_binds_runtime_generated_code_typing_and_notices(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            sdk = root / "sdk/python"
            sources = {"VERSION": "0.1.0\n", "api/openapi/control-v1.yaml": "openapi: 3.1.0\n",
                       "sdk/python/pyproject.toml": "[project]\nname = 'fixture'\n", "sdk/python/README.md": "SDK guide\n",
                       "sdk/python/LICENSE": "Apache License 2.0\n", "sdk/python/NOTICE": "Template attribution\n",
                       "sdk/python/src/catalog_sdk/client.py": "value = 'original'\n", "sdk/python/src/catalog_sdk/py.typed": "",
                       "sdk/python/src/catalog_sdk/generated/model.py": "value = 'generated'\n"}
            for name, content in sources.items():
                path = root / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(content)
            archive = root / "fixture.whl"
            archive.write_bytes(b"retained artifact")
            with patch.object(runner, "ROOT", root), patch.object(runner, "SDK", sdk), patch.object(shared, "ROOT", root), \
                 patch.object(runner, "installed", return_value={"inputs": "reviewed"}), patch.object(runner, "identity", return_value={}), \
                 patch.object(runner, "build_packages", return_value=[archive]):
                original = runner.package()
                self.assertEqual(original["sources"], {name: hashlib.sha256(content.encode()).hexdigest() for name, content in sources.items()})
                changed = sdk / "src/catalog_sdk/client.py"
                changed.write_text("value = 'changed'\n")
                updated = runner.package()
            key = "sdk/python/src/catalog_sdk/client.py"
            self.assertNotEqual(original["sources"][key], updated["sources"][key])
            self.assertEqual(updated["sources"][key], hashlib.sha256(changed.read_bytes()).hexdigest())
            self.assertEqual(original["artifacts"], updated["artifacts"])

    def test_environment_ignores_interpreter_tool_and_java_redirects(self):
        with patch.dict(os.environ, {"UV_PROJECT_ENVIRONMENT":"/outside", "UV_INDEX_URL":"secret", "PYTHONPATH":"/outside", "PYTHONHOME":"/outside", "VIRTUAL_ENV":"/outside", "JAVA_TOOL_OPTIONS":"secret"}):
            env = runner.environment()
        self.assertNotIn("PYTHONHOME", env)
        self.assertNotIn("VIRTUAL_ENV", env)
        self.assertNotIn("JAVA_TOOL_OPTIONS", env)
        self.assertNotIn("UV_INDEX_URL", env)
        self.assertEqual(env["UV_OFFLINE"], "1")
        self.assertEqual(env["PYTHONPATH"], str(runner.SDK / "src"))

    def test_install_rejects_unpinned_uv_before_mutation(self):
        with patch.object(runner, "run", return_value="uv 0.9.18"), patch.object(runner, "identity") as identity:
            with self.assertRaisesRegex(RuntimeError, "0.13.4"):
                runner.install()
            identity.assert_not_called()

    def test_missing_malformed_stale_installations_fail_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            tools = Path(directory)
            with patch.object(runner, "TOOLS", tools), patch.object(runner, "identity", return_value={"lock":"current"}):
                for data in (None, "not json", "{}", json.dumps({"inputs":{"lock":"old"}})):
                    if data is not None:
                        (tools / "installed.json").write_text(data)
                    with self.assertRaisesRegex(RuntimeError, "sdk-tools-install"):
                        runner.installed()

    def test_generated_tree_tracks_only_python_sources(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "nested").mkdir()
            (root / "nested/a.py").write_text("pass\n")
            (root / "generator.log").write_text("timestamp")
            original = runner.tree(root)
            self.assertEqual(set(original), {"nested/a.py"})
            (root / "nested/a.py").write_text("raise ValueError()\n")
            self.assertNotEqual(original, runner.tree(root))

    def test_installed_generator_and_runtime_wheel_drift_fail_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            tools = Path(directory)
            uv, jar, python, go = (tools / name for name in ("uv", "generator.jar", "python", "go"))
            for path in (uv, jar, python, go):
                path.write_bytes(b"qualified")
            stamp = {"inputs":{}, "uv":str(uv), "uv_hash":hashlib.sha256(uv.read_bytes()).hexdigest(),
                     "go":str(go), "go_hash":hashlib.sha256(go.read_bytes()).hexdigest(),
                     "generator_hash":hashlib.sha256(jar.read_bytes()).hexdigest(), "inventory":{"installed":{}}}
            (tools / "installed.json").write_text(json.dumps(stamp))
            with patch.object(runner, "TOOLS", tools), patch.object(runner, "PYTHON", python), patch.object(runner, "identity", return_value={}), patch.object(runner, "generator_jar", return_value=jar), patch.object(runner, "run", return_value=json.dumps(stamp["inventory"])) as run:
                go.write_bytes(b"changed")
                with self.assertRaisesRegex(RuntimeError, "sdk-tools-install"):
                    runner.installed()
                run.assert_not_called()
                go.write_bytes(b"qualified")
                jar.write_bytes(b"changed")
                with self.assertRaisesRegex(RuntimeError, "sdk-tools-install"):
                    runner.installed()
                run.assert_not_called()
                jar.write_bytes(b"qualified")
                run.return_value = '{}'
                with self.assertRaisesRegex(RuntimeError, "installation drift"):
                    runner.installed()
                run.side_effect = [json.dumps(stamp["inventory"]), '[{"url":"https://example.com/runtime.whl","hash":"sha256:absent"}]']
                with self.assertRaisesRegex(RuntimeError, "locked runtime wheel"):
                    runner.installed()

    def test_catalog_qualification_uses_installed_go_offline(self):
        import runner as go_runner
        with tempfile.TemporaryDirectory() as directory:
            work = Path(directory)
            compat = work / "compat"
            (compat / "go").mkdir(parents=True)
            (compat / "go/go.mod").write_text("replace " + go_runner.MODULE + " => ../../..\n")
            native_go = work / "qualified-go"
            with patch.object(go_runner, "COMPAT", compat), patch.object(runner, "run") as run:
                runner.qualify_catalog(work, native_go)
            self.assertEqual(len(run.call_args_list), 2)
            for call in run.call_args_list:
                self.assertEqual(call.args[0][0], native_go)
                env = call.kwargs["env"]
                self.assertEqual(env["GOTOOLCHAIN"], "local")
                self.assertEqual(env["GOPROXY"], "off")
                self.assertNotIn("PYTHONPATH", env)
                self.assertEqual(env["CATALOG_SDK_PYTHON"], str(work / "consumer/bin/python"))
                self.assertEqual(env["CATALOG_SDK_SERVICE_READ"], str(runner.SERVICE_READ))
            command = run.call_args_list[-1].args[0]
            self.assertIn("-run=^(TestPythonSDKCatalogWorkflow|TestPythonSDKServiceRead)$", command)
            self.assertTrue((work / "catalog/python_service_read_functional_test.go").is_file())

    def test_generate_uses_supported_templates_and_unchanged_contract(self):
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            write_generator_fixture(base)
            destination = base / "generated"
            with patch.object(runner, "TOOLS", base), patch.object(runner, "run", side_effect=mock_generation(destination)) as run:
                runner.generate(destination, base / "work")
            args = run.call_args_list[0].args[0]
            self.assertIn(runner.ROOT / "api/openapi/control-v1.yaml", args)
            self.assertNotIn("--skip-validate-spec", args)
            self.assertEqual(run.call_args_list[1].args[0][1], "format")
            source = (base / "work/templates/api.mustache").read_text()
            self.assertIn("hide_input_in_errors", source)
            self.assertEqual((base / "work/templates/model_oneof.mustache").read_text().count("from __future__ import annotations"), 1)
            for name in OPEN_MODELS:
                source = (destination / "catalog_sdk/generated/models" / (name + ".py")).read_text()
                self.assertEqual(source.count('extra="allow"'), 1)
                self.assertNotIn("additional_properties: Dict", source)
                self.assertNotIn(".additional_properties", source)
                self.assertEqual(source.count("self.model_extra"), 3)
                self.assertEqual(source.count("_obj.model_extra[_key]"), 1)
                self.assertEqual(source.count("_obj.model_fields_set.intersection_update("), 1)

    def test_generation_rejects_missing_or_duplicate_open_object_markers(self):
        for marker in ("        model_config = ConfigDict(\n", "                exclude_none=True,\n            )",
                       "        additional_properties: Dict[str, Any] = {}\n", '                "additional_properties",\n',
                       "            # store additional fields in additional_properties\n",
                       "self.additional_properties", "_obj.additional_properties"):
            for replacement in ("", marker * 2):
                with self.subTest(marker=marker, replacement=replacement), tempfile.TemporaryDirectory() as directory:
                    base = Path(directory)
                    write_generator_fixture(base)
                    destination = base / "generated"
                    changed = MODEL_SOURCE.replace(marker, replacement)
                    with patch.object(runner, "TOOLS", base), patch.object(runner, "run", side_effect=mock_generation(destination, changed)) as run:
                        with self.assertRaisesRegex(RuntimeError, "Unsupported Python open-object template: " + OPEN_MODELS[-1]):
                            runner.generate(destination, base / "work")
                    self.assertEqual(run.call_count, 1, "Template drift must stop before formatter or qualification")


if __name__ == "__main__":
    unittest.main()
