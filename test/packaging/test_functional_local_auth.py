"""Actual private-input preparation, Compose rendering and TLS renewal."""

import argparse
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
from unittest import TestCase, mock, skipUnless

from tutorial_test_helpers import load_example

with mock.patch.dict(sys.modules, {"compose": load_example("compose"), "prepare": load_example("prepare")}):
    local_auth = load_example("local_auth")


@skipUnless(shutil.which("openssl") and shutil.which("docker"), "OpenSSL and Docker Compose CLI are required")
class LocalAuthenticationInputsTests(TestCase):
    def test_preparation_reuse_rejection_and_certificate_renewal(self):
        with tempfile.TemporaryDirectory(prefix="catalog-local-auth-") as temporary:
            directory = Path(temporary) / "private"
            args = argparse.Namespace(directory=str(directory), backend_image="test-backend:unrun", ui_image="test-ui:unrun",
                                      subnet="172.29.245.0/24", frontend_address="172.29.245.10")
            root, command, environment = local_auth.initialize(args)
            self.assertEqual(root, directory)
            linked = Path(temporary) / "linked"
            linked.symlink_to(directory, target_is_directory=True)
            with self.assertRaisesRegex(ValueError, "symbolic link"):
                local_auth.initialize(argparse.Namespace(**{**vars(args), "directory": str(linked)}))
            realm = json.loads((directory / "catalog-realm.json").read_text())
            self.assertEqual({item["clientId"] for item in realm["clients"]}, {"catalog-ui", "catalog-cli"})
            self.assertEqual(realm["attributes"], {"CreateDefaultClientScopes": "true"})
            self.assertNotIn("basic", {scope["name"] for scope in realm["clientScopes"]})
            for client in realm["clients"]:
                self.assertFalse(client["directAccessGrantsEnabled"])
                self.assertEqual(client["defaultClientScopes"], ["basic"])
                self.assertNotIn("roles", client["optionalClientScopes"])
            accounts = json.loads((directory / "accounts.json").read_text())
            self.assertEqual(set(accounts), {"catalog-admin", "catalog-reader"})
            passwords = []
            for user in realm["users"]:
                self.assertEqual(user["id"], accounts[user["username"]])
                self.assertTrue(user["emailVerified"])
                self.assertEqual(user["realmRoles"], ["default-roles-catalog"])
                credential = user["credentials"][0]
                self.assertFalse(credential["temporary"])
                passwords.append(credential["value"])
                path = directory / (user["username"] + "-initial-password")
                self.assertEqual(path.read_text(), credential["value"])
                self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            self.assertNotEqual(*passwords)
            self.assertFalse((directory / "b").exists())
            self.assertFalse((directory / "trust/browse-b.pem").exists())
            topology = json.loads(local_auth.run(command + ["config", "--format", "json"], environment))
            self.assertEqual(set(topology["services"]), local_auth.SERVICES)
            self.assertTrue(all(port["host_ip"] == "127.0.0.1" for service in topology["services"].values() for port in service.get("ports", [])))
            for name in ("api-a", "worker-a", "ui-a"):
                self.assertFalse(any("owner" in volume["source"] for volume in topology["services"][name]["volumes"]))
            key = directory / "a/ui-keys.json"
            original_key = key.read_bytes()
            key.unlink()
            with mock.patch.object(local_auth, "run") as execute:
                with self.assertRaisesRegex(ValueError, "bind input"):
                    local_auth.preflight(directory, command, environment)
                execute.assert_not_called()
            local_auth.prepare.write(key, original_key.decode(), 0o444)
            before = {str(path.relative_to(directory)): hashlib.sha256(path.read_bytes()).hexdigest()
                      for path in directory.rglob("*") if path.is_file()}
            self.assertEqual(local_auth.initialize(args), (root, command, environment))
            self.assertEqual(before, {str(path.relative_to(directory)): hashlib.sha256(path.read_bytes()).hexdigest()
                                     for path in directory.rglob("*") if path.is_file()})
            calls = []
            with mock.patch.object(local_auth, "run", side_effect=lambda *args: calls.append(args[0])):
                local_auth.renew(directory, command, environment)
            self.assertEqual(calls, [command + ["stop"], command + ["rm", "-f"]])
            self.assertNotEqual(hashlib.sha256((directory / "trust/ca.pem").read_bytes()).hexdigest(), before["trust/ca.pem"])
            for name, digest in before.items():
                if not name.startswith("trust/"):
                    self.assertEqual(hashlib.sha256((directory / name).read_bytes()).hexdigest(), digest)
            local_auth.certificates_current(directory)
            # An expired certificate must stop admission before Docker startup.
            expired = directory / "trust/expired.pem"
            subprocess.run(["openssl", "x509", "-in", str(directory / "trust/ca.pem"),
                            "-signkey", str(directory / "trust/ca-key.pem"), "-days", "-1", "-out", str(expired)],
                           capture_output=True, check=True, timeout=15)
            # An unused backup must not block startup or certificate renewal.
            local_auth.certificates_current(directory)
            active = directory / "trust/identity.pem"
            original_certificate = active.read_text()
            active.unlink()
            local_auth.prepare.write(active, expired.read_text(), 0o444)
            with self.assertRaisesRegex(ValueError, "expired or invalid"):
                local_auth.certificates_current(directory)
            active.unlink()
            local_auth.prepare.write(active, original_certificate, 0o444)
            local_auth.certificates_current(directory)
            expired.unlink()
            configuration = directory / "compose.single.json"
            raw = configuration.read_bytes()
            configuration.write_bytes(raw + b" ")
            with self.assertRaisesRegex(ValueError, "changed"):
                local_auth.selection(directory)
            configuration.write_bytes(raw)
            state = directory / "single.json"
            state.write_text("[]")
            with self.assertRaisesRegex(ValueError, "JSON object"):
                local_auth.selection(directory)
            state.write_text('{"format":1,"ready":false}')
            with self.assertRaisesRegex(ValueError, "incomplete"):
                local_auth.initialize(args)


class LocalAuthenticationPortTests(TestCase):
    def test_active_listener_is_refused_and_recently_closed_listener_is_available(self):
        with local_auth.socket.socket() as listener:
            listener.setsockopt(local_auth.socket.SOL_SOCKET, local_auth.socket.SO_REUSEADDR, 1)
            listener.settimeout(5)
            listener.bind(("127.0.0.1", 0))
            listener.listen(1)
            port = listener.getsockname()[1]
            with self.assertRaisesRegex(ValueError, "Local port " + str(port) + " is unavailable"):
                local_auth.available_port(port)
            with local_auth.socket.create_connection(("127.0.0.1", port), timeout=5) as client:
                accepted, _ = listener.accept()
                with accepted:
                    accepted.shutdown(local_auth.socket.SHUT_WR)
                    self.assertEqual(client.recv(1), b"")
        local_auth.available_port(port)
