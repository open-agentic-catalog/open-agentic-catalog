"""Isolated protected-file controls for the documentation's credential example."""

import argparse
import datetime
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


def load(name, filename):
    specification = importlib.util.spec_from_file_location(name, Path(__file__).resolve().parents[2] / "examples/tutorials/local-oidc" / filename)
    module = importlib.util.module_from_spec(specification)
    specification.loader.exec_module(module)
    return module


credentials = load("tutorial_credentials", "credential-files.py")
service = load("tutorial_service_token", "service-token.py")
preparer = load("tutorial_preparer", "prepare.py")


class CredentialFilesTests(unittest.TestCase):
    def test_personal_write_is_explicitly_available_only_to_human_clients(self):
        human = preparer.client("catalog-ui-a", "open-agentic-catalog", redirect="https://catalog-a.test/auth/callback")
        automation = preparer.client("catalog-automation-a", "open-agentic-catalog", service=True)
        self.assertIn("personal.write", human["optionalClientScopes"])
        self.assertNotIn("personal.write", human["defaultClientScopes"])
        self.assertNotIn("personal.write", automation["defaultClientScopes"])
        self.assertEqual(automation["optionalClientScopes"], [])

    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.directory = Path(self.temporary.name)
        self.source = self.directory / "login.json"
        self.value = {
            "apiOrigin": "https://catalog-a.test:8443",
            "expiresAt": (datetime.datetime.now(datetime.timezone.utc) + datetime.timedelta(minutes=5)).isoformat(),
            "accessToken": "header.access.signature",
            "idToken": "header.identity.signature", "refreshToken": "never-export-this",
        }
        self.write_source()

    def write_source(self):
        self.source.write_text(json.dumps(self.value), encoding="ascii")
        self.source.chmod(0o600)

    def arguments(self, stem="derived", token=True):
        return argparse.Namespace(login_file=str(self.source), origin=self.value["apiOrigin"],
                                  output_stem=str(self.directory / stem), token=token)

    def test_current_access_token_has_exact_distinct_formats(self):
        with patch("builtins.print"):
            credentials.convert(self.arguments())
        self.assertEqual((self.directory / "derived.headers").read_text(), "Authorization: Bearer header.access.signature\n")
        self.assertEqual((self.directory / "derived.token").read_text(), "header.access.signature\n")
        for name in ("derived.headers", "derived.token"):
            self.assertEqual((self.directory / name).stat().st_mode & 0o777, 0o600)
        self.assertEqual(json.loads(self.source.read_text()), self.value)

    def test_utf8_login_metadata_preserves_ascii_credentials(self):
        self.value["subject"] = "reader-\u00e9"
        self.source.write_text(json.dumps(self.value, ensure_ascii=False), encoding="utf-8")
        original = self.source.read_bytes()
        self.assertIn(b"\xc3\xa9", original)
        with patch("builtins.print"):
            credentials.convert(self.arguments())
        self.assertEqual((self.directory / "derived.headers").read_bytes(), b"Authorization: Bearer header.access.signature\n")
        self.assertEqual((self.directory / "derived.token").read_bytes(), b"header.access.signature\n")
        for name in ("derived.headers", "derived.token"):
            self.assertEqual((self.directory / name).stat().st_mode & 0o777, 0o600)
        self.assertEqual(self.source.read_bytes(), original)

    def test_dotted_stem_cannot_replace_the_selected_login(self):
        for suffix in ("headers", "token"):
            self.source = self.directory / ("selected.login." + suffix)
            self.write_source()
            original = self.source.read_bytes()
            with self.assertRaises(ValueError):
                credentials.convert(self.arguments("selected.login"))
            self.assertEqual(self.source.read_bytes(), original)

    def test_wrong_origin_and_expired_login_create_no_outputs(self):
        args = self.arguments()
        args.origin = "https://catalog-b.test:8445"
        with self.assertRaises(ValueError):
            credentials.convert(args)
        self.value["expiresAt"] = "2000-01-01T00:00:00Z"
        self.write_source()
        with self.assertRaises(ValueError):
            credentials.convert(self.arguments())
        self.assertFalse((self.directory / "derived.headers").exists())

    def test_an_ancestor_alias_cannot_hide_source_collision(self):
        real = self.directory / "real"
        real.mkdir(mode=0o700)
        self.source = real / "selected.login.headers"
        self.write_source()
        alias = self.directory / "alias"
        alias.symlink_to(self.directory, target_is_directory=True)
        args = self.arguments()
        args.output_stem = str(alias / "real" / "selected.login")
        original = self.source.read_bytes()
        with self.assertRaises(ValueError):
            credentials.convert(args)
        self.assertEqual(self.source.read_bytes(), original)

    def test_unprotected_and_symlink_sources_are_refused(self):
        self.source.chmod(0o644)
        with self.assertRaises(ValueError):
            credentials.convert(self.arguments())
        self.source.chmod(0o600)
        alias = self.directory / "alias.json"
        alias.symlink_to(self.source)
        args = self.arguments()
        args.login_file = str(alias)
        with self.assertRaises(ValueError):
            credentials.convert(args)

    def test_nonprivate_output_directory_is_refused(self):
        output = self.directory / "shared"
        output.mkdir(mode=0o755)
        args = self.arguments()
        args.output_stem = str(output / "derived")
        with self.assertRaises(ValueError):
            credentials.convert(args)
        self.assertEqual(list(output.iterdir()), [])

    def test_service_secret_is_validated_before_any_provider_request(self):
        secret = self.directory / "client-secret"
        secret.write_text("synthetic-example-secret", encoding="ascii")
        secret.chmod(0o644)
        args = argparse.Namespace(issuer="https://identity.test:8447/realms/catalog", client_id="catalog-automation-a",
                                  audience="open-agentic-catalog", secret_file=str(secret), ca_file="not-read.pem",
                                  output_stem=str(self.directory / "service"))
        with patch.object(service.urllib.request, "build_opener") as provider:
            with self.assertRaises(ValueError):
                service.acquire(args)
            secret.chmod(0o600)
            alias = self.directory / "secret-alias"
            alias.symlink_to(secret)
            args.secret_file = str(alias)
            with self.assertRaises(ValueError):
                service.acquire(args)
            provider.assert_not_called()

    def test_preparation_refuses_checkout_and_symlink_redirect_before_generation(self):
        checkout = self.directory / "checkout"
        checkout.mkdir()
        redirect = self.directory / "redirect"
        redirect.symlink_to(checkout, target_is_directory=True)
        for destination in (checkout / "private", redirect / "private"):
            args = argparse.Namespace(directory=str(destination), backend_image="fixture:backend", ui_image="fixture:ui",
                                      subnet="172.29.245.0/24", frontend_address="172.29.245.10")
            with patch.object(preparer, "ROOT", checkout), patch.object(preparer, "openssl") as generate:
                with self.assertRaises(ValueError):
                    preparer.prepare(args)
                generate.assert_not_called()
            self.assertFalse((checkout / "private").exists())

    def test_preparation_preserves_an_existing_destination(self):
        marker = self.directory / "retained"
        marker.write_text("owned retained input", encoding="ascii")
        args = argparse.Namespace(directory=str(self.directory), backend_image="fixture:backend", ui_image="fixture:ui",
                                  subnet="172.29.245.0/24", frontend_address="172.29.245.10")
        with patch.object(preparer, "openssl") as generate:
            with self.assertRaises(FileExistsError):
                preparer.prepare(args)
            generate.assert_not_called()
        self.assertEqual(marker.read_text(), "owned retained input")


if __name__ == "__main__":
    unittest.main()
