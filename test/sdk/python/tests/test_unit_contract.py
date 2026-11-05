"""Independent complete-contract inventory and honest schema/type boundaries."""
import ast
from copy import deepcopy
import importlib
import json
from pathlib import Path
import re
import unittest

import jsonschema
import yaml

from catalog_sdk.generated import models
from catalog_sdk.generated.models.trust_assessment_request import TrustAssessmentRequest
from catalog_sdk.generated.models.representation_input import RepresentationInput
from catalog_sdk.generated.models.ard_entry import ARDEntry
from catalog_sdk.generated.models.catalog_search_result import CatalogSearchResult
from catalog_sdk.generated.models.catalog_search_response import CatalogSearchResponse
from catalog_sdk.generated.models.audit_details_v1 import AuditDetailsV1
from catalog_sdk.generated.models.create_listing_revision import CreateListingRevision
from catalog_sdk.generated.models.create_listing_revision_extensions import CreateListingRevisionExtensions
from catalog_sdk.generated.models.create_namespace import CreateNamespace

ROOT = Path(__file__).resolve().parents[4]
CONTRACT = yaml.safe_load((ROOT / "api/openapi/control-v1.yaml").read_text())
GENERATED = ROOT / "sdk/python/src/catalog_sdk/generated"


def snake(name):
    return re.sub(r"([a-z0-9])([A-Z])", r"\1_\2", re.sub(r"(.)([A-Z][a-z]+)", r"\1_\2", name)).lower()


class ContractUnits(unittest.TestCase):
    def test_source_credential_binding_excludes_path_segments(self):
        validator = jsonschema.Draft202012Validator(CONTRACT["components"]["schemas"]["CredentialBindingReference"])
        base = {"url": "https://catalog.example.test"}
        for binding in ("source_primary", "source.backup", "source-2"):
            validator.validate(binding)
            for codec in (models.InspectSource.from_dict, models.InspectSource.model_validate):
                with self.subTest(codec=codec, binding=binding):
                    self.assertEqual(codec(dict(base, credentialBinding=binding)).to_dict()["credentialBinding"], binding)
        for binding in (".", ".."):
            with self.subTest(binding=binding), self.assertRaises(jsonschema.ValidationError):
                validator.validate(binding)
            for codec in (models.InspectSource.from_dict, models.InspectSource.model_validate):
                with self.subTest(codec=codec, binding=binding), self.assertRaises(ValueError):
                    codec(dict(base, credentialBinding=binding))

    def test_full_custom_role_capacity_matches_the_delegable_contract(self):
        cls = models.RoleDefinitionInput
        permissions = [p for p in CONTRACT["components"]["schemas"]["PermissionKey"]["enum"] if p != "access.manage"]
        self.assertEqual(len(permissions), 49)
        value = {"name": "All delegable actions", "permissions": permissions, "reason": "Explicit finite role"}
        self.assertEqual(cls.from_dict(value).to_dict()["permissions"], permissions)
        with self.assertRaises(ValueError):
            cls.from_dict(dict(value, permissions=permissions + ["access.manage"]))

    def test_publication_artifact_ids_are_retained_hex_strings_not_uuids(self):
        identifier = "00000000-0000-7000-8000-000000000001"
        digest = "a" * 64
        value = {"id": identifier, "tenantId": identifier, "host": "catalog.example.com", "audience": "anonymous",
                 "generation": 1, "ardProfile": "ard-profile", "aiCatalogProfile": "ai-profile", "state": "candidate",
                 "ard": {"entries": []}, "aiCatalog": {"specVersion": "1.0", "host": {"displayName": "Catalog", "identifier": "https://catalog.example.com"}, "entries": []},
                 "exclusions": [], "artifactIds": [digest], "etag": '"' + digest + '"', "aiCatalogEtag": '"' + digest + '"',
                 "ardContentDigest": "sha-256=:" + "A" * 43 + "=:", "aiCatalogContentDigest": "sha-256=:" + "A" * 43 + "=:",
                 "createdAt": "2026-10-15T12:00:00Z"}
        for codec in (models.PublicationSnapshot.from_dict, models.PublicationSnapshot.model_validate):
            self.assertEqual(codec(value).to_dict()["artifactIds"], [digest])
            for invalid in (identifier, "a" * 63, "sha256:" + digest):
                with self.subTest(codec=codec, invalid=invalid), self.assertRaises(ValueError):
                    codec(dict(value, artifactIds=[invalid]))

    def test_every_operation_method_path_response_and_media_type(self):
        found = {}
        for path in (GENERATED / "api").glob("*_api.py"):
            source = path.read_text()
            module = importlib.import_module("catalog_sdk.generated.api." + path.stem)
            cls = next(v for k, v in vars(module).items() if k.endswith("Api") and k != "ApiClient")
            tree = ast.parse(source)
            methods = {n.name: n for n in ast.walk(tree) if isinstance(n, (ast.FunctionDef, ast.AsyncFunctionDef))}
            for route, entries in CONTRACT["paths"].items():
                for method, op in entries.items():
                    if not isinstance(op, dict) or "operationId" not in op:
                        continue
                    name = snake(op["operationId"])
                    if name not in methods:
                        continue
                    self.assertTrue(op["operationId"] not in found or found[op["operationId"]] == (method, route))
                    found[op["operationId"]] = (method, route)
                    for suffix in ("", "_with_http_info", "_without_preload_content", "_sync", "_sync_with_http_info", "_sync_without_preload_content"):
                        self.assertTrue(callable(getattr(cls, name + suffix)), name + suffix)
                    serializer = methods["_" + name + "_serialize"]
                    call = next(n for n in ast.walk(serializer) if isinstance(n, ast.Call) and isinstance(n.func, ast.Attribute) and n.func.attr == "param_serialize")
                    arguments = {kw.arg:kw.value for kw in call.keywords}
                    self.assertEqual(ast.literal_eval(arguments["resource_path"]), route)
                    self.assertEqual(ast.literal_eval(arguments["method"]), method.upper())
                    response_types = next(ast.literal_eval(n.value) for n in ast.walk(methods[name]) if isinstance(n, ast.AnnAssign) and isinstance(n.target, ast.Name) and n.target.id == "_response_types_map")
                    self.assertEqual(set(response_types), {str(status) for status in op["responses"]})
                    media_literals = {n.value for n in ast.walk(serializer) if isinstance(n, ast.Constant) and isinstance(n.value, str)}
                    for status, response in op["responses"].items():
                        if "$ref" in response:
                            response = CONTRACT["components"]["responses"][response["$ref"].split("/")[-1]]
                        for media in response.get("content", {}):
                            self.assertIn(media, media_literals, (name, media))
                    request = op.get("requestBody", {})
                    if "$ref" in request:
                        request = CONTRACT["components"]["requestBodies"][request["$ref"].split("/")[-1]]
                    for media in request.get("content", {}):
                        self.assertIn(media, media_literals, (name, media))
        expected = {op["operationId"] for entries in CONTRACT["paths"].values() for op in entries.values() if isinstance(op, dict) and "operationId" in op}
        self.assertEqual(set(found), expected)
        self.assertTrue(found)
        print(f"Qualified {len(found)} unchanged contract operations, sync/async variants and response media")

    def test_named_object_fields_survive_generation(self):
        missing = []
        checked = 0
        for name, schema in CONTRACT["components"]["schemas"].items():
            if not schema.get("properties"):
                continue
            cls = getattr(models, name, None)
            if name in ("TrustAssessmentRequest", "RepresentationInput", "CatalogSearchResult", "AuditDetailsV1"):
                self.assertTrue(issubclass(cls, __import__("pydantic").RootModel), name)
                continue
            self.assertIsNotNone(cls, name)
            fields = getattr(cls, "model_fields", {})
            if "actual_instance" in fields:
                # Alternative-only unions have no flat model-field inventory.
                # The schema-specific controls qualify supported codecs separately.
                continue
            aliases = {field.alias or field_name for field_name, field in fields.items()}
            aliases.discard("additional_properties")
            codec_fields = getattr(cls, "_" + name + "__properties", [])
            self.assertEqual(set(codec_fields), aliases, (name, "codec fields differ from model"))
            checked += 1
            missing += [(name, key) for key in schema["properties"] if key not in aliases]
        self.assertEqual(missing, [])
        self.assertGreater(checked, 100)
        print(f"Qualified fields in {checked} named object models; four conditional schemas use explicit JSON objects")

    def test_conditional_input_against_independent_schema(self):
        validator = jsonschema.Draft202012Validator({"$ref":"#/components/schemas/TrustAssessmentRequest", "components":CONTRACT["components"]})
        base = {"sourceRevision":1,"identifier":"urn:air:example.com:ns:agent", "versionPresent":False, "mediaType":"application/json", "delivery":"data", "exactEntryDigest":"sha256:" + "a" * 64, "reason":"Check", "text":"entry"}
        self.assertEqual(list(validator.iter_errors(base)), [])
        self.assertEqual(TrustAssessmentRequest.model_validate(base).to_dict(), base)
        valid = dict(base, versionPresent=True, version="1.0.0")
        self.assertEqual(list(validator.iter_errors(valid)), [])
        for invalid in (dict(base, observationId="00000000-0000-7000-8000-000000000001"), dict(base, version="1.0.0"), dict(base, versionPresent=True), {k:v for k,v in base.items() if k != "text"}):
            self.assertTrue(list(validator.iter_errors(invalid)))
            # The typed container deliberately preserves input; it is not a schema validator.
            self.assertEqual(TrustAssessmentRequest.model_validate(invalid).to_dict(), invalid)

    def test_search_results_keep_common_fields_and_each_conditional_evidence(self):
        schema = CONTRACT["components"]["schemas"]["CatalogSearchResult"]
        id_validator = jsonschema.Draft202012Validator(schema["properties"]["id"])
        for kind in ("local", "indexed", "live"):
            self.assertEqual(list(id_validator.iter_errors(kind + ":selected")), [])
        for invalid in ("", "foreign:selected", "local:", *("local:before" + space + "after" for space in " \t\n\v\f\r")):
            self.assertTrue(list(id_validator.iter_errors(invalid)))
        validator = jsonschema.Draft202012Validator({"$ref":"#/components/schemas/CatalogSearchResult", "components":CONTRACT["components"]})
        identifier = "urn:air:example.com:ns:agent"
        uid = "00000000-0000-7000-8000-000000000001"
        digest = "sha256:" + "a" * 64
        local = {
            "resource":{"id":uid,"tenantId":uid,"namespaceId":uid,"canonicalIdentifier":identifier,"kind":"agent"},
            "version":{"id":uid,"version":"1.0.0"},
            "versionRevision":{"id":uid,"contentSetDigest":digest,"evidenceSetId":uid},
            "listing":{"id":uid,"namespaceId":uid},
            "listingRevision":{"id":uid,"visibility":"tenant"},
            "release":{"id":uid,"versionRevisionId":uid,"listingRevisionId":uid,"state":"published","activatedAt":"2026-10-11T12:00:00Z"},
            "evidence":{"id":uid,"digest":digest,"state":"complete","trust":{"state":"not_assessed"},"legalRights":{"state":"not_assessed"}},
        }
        # The generator labels this closed composed schema as open. The repair
        # must not change its existing unknown-field behavior.
        closed_local = models.CatalogSearchLocal(**dict(local, undeclared="omitted"))
        self.assertNotIn("undeclared", closed_local.to_dict())
        indexed = {"observationGroupId":"observation-group-v1:" + digest,"normalizedEntryDigest":digest,"sourceIds":[uid],"sourceCount":1,"sourcesTruncated":False,"conflict":False,"sameSourceConflict":False}
        live = {"sourceId":uid,"source":"https://peer.example.com/ard/","entry":{"identifier":identifier,"future":{"large":9007199254740993,"values":[None,False,""]}}}
        for kind, evidence in (("local", local), ("indexed", indexed), ("live", live)):
            value = {"id":kind + ":selected","type":kind,"identifier":identifier,"versionPresent":False,"kind":"agent","displayName":"Selected Agent","tags":["sdk"],"score":91,kind:evidence}
            with self.subTest(kind=kind):
                self.assertEqual(list(validator.iter_errors(value)), [])
                self.assertEqual(CatalogSearchResult.from_dict(value).to_dict(), value)
                page = {"items":[value],"nextCursor":"opaque-next","durableTotal":0 if kind == "live" else 1,"liveCount":1 if kind == "live" else 0,"profile":"core-lexical-v1","generation":digest,"tier":"core","partial":False,"diagnostics":[]}
                self.assertEqual(CatalogSearchResponse.from_dict(page).to_dict(), page)
                invalid = deepcopy(value)
                invalid["type"] = "live" if kind != "live" else "local"
                self.assertTrue(list(validator.iter_errors(invalid)))
                # The explicit object container preserves fields, not schema validity.
                self.assertEqual(CatalogSearchResult.from_dict(invalid).to_dict(), invalid)
                invalid = dict(value, extra="not a declared control field")
                self.assertTrue(list(validator.iter_errors(invalid)))
                invalid = dict(value, **({"live":live} if kind != "live" else {"local":local}))
                self.assertTrue(list(validator.iter_errors(invalid)))
                self.assertEqual(CatalogSearchResult.from_dict(invalid).to_dict(), invalid)
        for scalar in (None, [], "private"):
            with self.subTest(scalar=scalar), self.assertRaises(__import__("pydantic").ValidationError):
                CatalogSearchResult.model_validate(scalar)

    def test_audit_details_keep_each_principal_and_exact_facts(self):
        validator = jsonschema.Draft202012Validator({"$ref":"#/components/schemas/AuditDetailsV1", "components":CONTRACT["components"]})
        uid = "00000000-0000-7000-8000-000000000001"
        facts = {"large":9007199254740993,"values":[None,False,""],"nested":{"future":"retained"}}
        valid = [
            {"principalType":"catalog_membership","actorType":"human","realmId":uid,"authorizationDigest":"sha256:" + "a" * 64,"commandId":uid,"facts":facts},
            {"principalType":"service_executor","facts":facts},
            {"principalType":"database_login","currentUser":"catalog_owner","operatorLabel":"Local SDK qualification","facts":facts},
        ]
        for value in valid:
            with self.subTest(principal=value["principalType"]):
                self.assertEqual(list(validator.iter_errors(value)), [])
                self.assertEqual(AuditDetailsV1.from_dict(value).to_dict(), value)
        invalid = [dict(valid[0], currentUser="owner"), dict(valid[1], actorType="human"), dict(valid[2], commandId=uid), {"principalType":"database_login"}]
        for value in invalid:
            self.assertTrue(list(validator.iter_errors(value)))
            self.assertEqual(AuditDetailsV1.from_dict(value).to_dict(), value)

    def test_open_extensions_survive_direct_nested_and_from_dict_construction(self):
        extensions = {"detail":"SDK extension","large":9007199254740993,"empty":"","null":None,"false":False,"nested":{"values":[None,False,""]}}
        uid = "00000000-0000-7000-8000-000000000001"
        value = {"versionRevisionId":uid,"displayName":"Python Agent","description":"SDK extension retention","publisherId":uid,"visibility":"tenant","extensions":extensions}
        validator = jsonschema.Draft202012Validator({"$ref":"#/components/schemas/CreateListingRevision", "components":CONTRACT["components"]})
        self.assertEqual(list(validator.iter_errors(value)), [])
        for cls, schema in ((CreateListingRevisionExtensions, CONTRACT["components"]["schemas"]["CreateListingRevision"]["properties"]["extensions"]), (models.ListingRevisionExtensions, CONTRACT["components"]["schemas"]["ListingRevision"]["properties"]["extensions"])):
            self.assertIs(schema["additionalProperties"], True)
            self.assertEqual(list(jsonschema.Draft202012Validator({**schema,"components":CONTRACT["components"]}).iter_errors(extensions)), [])
            for model in (cls(**extensions), cls.model_validate(extensions), cls.from_dict(extensions)):
                self.assertEqual(model.to_dict(), extensions)
        for model in (CreateListingRevision(**value), CreateListingRevision.from_dict(value)):
            self.assertEqual(model.to_dict()["extensions"], extensions)
        # The template change is limited to explicitly open objects.
        closed = CreateNamespace(slug="python", displayName="Python", undeclared="omitted")
        self.assertEqual(closed.to_dict(), {"slug":"python","displayName":"Python"})

    def test_open_native_search_and_referral_objects_preserve_constructor_keys(self):
        schemas = CONTRACT["components"]["schemas"]
        extras = {"future":{"large":9007199254740993,"values":[None,False,""]},"empty":"","null":None,"false":False,"additional_properties":{"must":"stay nested"},"model_extra":"wire value"}
        trust = dict(extras, identity="example.com")
        result = dict(extras, identifier="urn:air:example.com:ns:agent", score=91, source="https://peer.example.com/ard/", trustManifest=trust)
        referral = dict(extras, identifier="urn:air:example.com:ns:peer", displayName="Local peer", type="application/ai-registry", url="https://peer.example.com/ard/")
        cases = [
            (models.ARDSearchResult, schemas["ARDSearchResult"], result),
            (models.ARDSearchResultTrustManifest, schemas["ARDSearchResult"]["properties"]["trustManifest"], trust),
            (models.ARDSearchResponseReferralsInner, schemas["ARDSearchResponse"]["properties"]["referrals"]["items"], referral),
        ]
        for cls, schema, value in cases:
            with self.subTest(model=cls.__name__):
                self.assertIs(schema["additionalProperties"], True)
                self.assertEqual(list(jsonschema.Draft202012Validator(schema).iter_errors(value)), [])
                for model in (cls(**value), cls.model_validate(value), cls.from_dict(value)):
                    self.assertEqual(model.to_dict(), value)

    def test_open_metadata_storage_names_are_literal_json_keys(self):
        schema = CONTRACT["components"]["schemas"]["CreateListingRevision"]["properties"]["extensions"]
        validator = jsonschema.Draft202012Validator({**schema, "components":CONTRACT["components"]})
        for extra in (None, False, "", 9007199254740993, [None, False, ""], {"must":"stay nested"}):
            value = {"additional_properties":extra,"model_extra":"literal metadata","normal":None}
            self.assertEqual(list(validator.iter_errors(value)), [])
            for cls in (CreateListingRevisionExtensions, models.ListingRevisionExtensions):
                with self.subTest(model=cls.__name__, extra=extra):
                    for model in (cls(**value), cls.model_validate(value), cls.from_dict(value), cls.from_json(json.dumps(value))):
                        self.assertEqual(model.to_dict(), value)

    def test_ard_entry_preserves_open_keys_and_context_presence(self):
        schema = {"$ref":"#/components/schemas/ARDEntry", "components":CONTRACT["components"]}
        validator = jsonschema.Draft202012Validator(schema)
        base = {"identifier":"urn:air:example.com:ns:agent","displayName":"Agent","type":"application/json","data":{"large":9007199254740993},"future":None,"additional_properties":{"must":"stay nested"},"model_extra":"wire value"}
        for context in ({}, {"@context":None}, {"@context":{"@version":1.1}}):
            value = dict(base, **context)
            self.assertEqual(list(validator.iter_errors(value)), [])
            with self.subTest(context=context):
                for model in (ARDEntry(**value), ARDEntry.model_validate(value), ARDEntry.from_dict(value), ARDEntry.from_json(json.dumps(value))):
                    self.assertEqual(model.to_dict(), value)

    def test_representation_delivery_alternatives_and_archive_constraints(self):
        validator = jsonschema.Draft202012Validator({"$ref":"#/components/schemas/RepresentationInput", "components":CONTRACT["components"]})
        base = {"role":"primary", "mediaType":"application/json"}
        valid = [dict(base, data={"n":9007199254740993,"values":[None,False,""]}),
                 dict(base, content="exact text"), dict(base, url="https://example.com/card.json"),
                 dict(base, mediaType="application/agent-plugins+zip", url="https://example.com/plugin.zip", expectedDigest="sha256:" + "a" * 64),
                 dict(base, mediaType="application/agent-skills+zip", packageData="UEsDBA==", handoff={"git":{"repository":"https://example.com/skill.git", "commit":"a" * 40, "directory":"skill"}})]
        for value in valid:
            with self.subTest(value=value):
                self.assertEqual(list(validator.iter_errors(value)), [])
                self.assertEqual(RepresentationInput.model_validate(value).to_dict(), value)
        invalid = [base, dict(base, data={}, content="two inputs"), dict(base, expectedDigest="sha256:" + "a" * 64, data={}),
                   dict(base, mediaType="application/agent-plugins+zip", data={}),
                   dict(base, mediaType="application/agent-skills+zip", packageData="UEsDBA==")]
        for value in invalid:
            self.assertTrue(list(validator.iter_errors(value)))

    def test_ard_delivery_union_and_context_are_not_full_schema_validation(self):
        validator = jsonschema.Draft202012Validator({"$ref":"#/components/schemas/ARDEntry", "components":CONTRACT["components"]})
        base = {"identifier":"urn:air:example.com:ns:agent", "displayName":"Example", "type":"application/json"}
        for context in ("https://example.com/context.jsonld", {"@version":1.1}, ["https://example.com/context.jsonld", {"x":"https://example.com/x"}]):
            value = dict(base, data={"n":9007199254740993}, **{"@context":context,"extension":False})
            self.assertEqual(list(validator.iter_errors(value)), [])
            self.assertEqual(ARDEntry.from_dict(value).to_dict(), value)
        for value in (base, dict(base, data={}, url="https://example.com/card")):
            self.assertTrue(list(validator.iter_errors(value)))
            # Pydantic cannot enforce the delivery oneOf here. Do not claim otherwise.
            self.assertIsNotNone(ARDEntry.from_dict(value))
