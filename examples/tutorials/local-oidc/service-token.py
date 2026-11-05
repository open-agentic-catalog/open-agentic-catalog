#!/usr/bin/env python3
"""Acquire a local tutorial service token through protected inputs and TLS."""

import argparse
import base64
import datetime
import json
import os
from pathlib import Path
import re
import ssl
import stat
import tempfile
import urllib.error
import urllib.parse
import urllib.request


class NoRedirect(urllib.request.HTTPRedirectHandler):
    """A token request must stay at the exact configured local issuer."""

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def replace_private(path, value):
    parent = path.parent.lstat()
    if not stat.S_ISDIR(parent.st_mode) or parent.st_uid != os.getuid() or stat.S_IMODE(parent.st_mode) != 0o700:
        raise ValueError("Use an owned 0700 output directory")
    descriptor, temporary = tempfile.mkstemp(prefix=".service-", dir=path.parent)
    try:
        with os.fdopen(descriptor, "w", encoding="ascii") as stream:
            stream.write(value)
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def acquire(args):
    if args.issuer != "https://identity.test:8447/realms/catalog":
        raise ValueError("This helper accepts only the tutorial's local issuer")
    selected_secret = Path(args.secret_file).expanduser()
    secret_file = selected_secret.parent.resolve() / selected_secret.name
    metadata = secret_file.lstat()
    parent = secret_file.parent.lstat()
    if not stat.S_ISREG(metadata.st_mode) or metadata.st_uid != os.getuid() or stat.S_IMODE(metadata.st_mode) != 0o600 or metadata.st_size > 4096:
        raise ValueError("Select the owned 0600 service-client secret")
    if not stat.S_ISDIR(parent.st_mode) or parent.st_uid != os.getuid() or stat.S_IMODE(parent.st_mode) != 0o700:
        raise ValueError("Select a secret in an owned 0700 directory")
    secret = secret_file.read_text(encoding="ascii").strip()
    body = urllib.parse.urlencode({"grant_type": "client_credentials", "client_id": args.client_id, "client_secret": secret}).encode("ascii")
    request = urllib.request.Request(args.issuer + "/protocol/openid-connect/token", data=body,
                                     headers={"Content-Type": "application/x-www-form-urlencoded"}, method="POST")
    context = ssl.create_default_context(cafile=args.ca_file)
    # Explicitly ignore ambient HTTP proxies and credentials for this local flow.
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPSHandler(context=context), NoRedirect())
    with opener.open(request, timeout=15) as response:
        raw = response.read((1 << 20) + 1)
    if len(raw) > 1 << 20:
        raise ValueError("The provider response exceeds the tutorial bound")
    token = json.loads(raw)["access_token"]
    if not isinstance(token, str) or len(token) > 32768 or not re.fullmatch(r"[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+", token):
        raise ValueError("The provider did not return a bounded compact JWT")
    encoded = token.split(".")[1]
    claims = json.loads(base64.urlsafe_b64decode(encoded + "=" * (-len(encoded) % 4)))
    audiences = claims["aud"] if isinstance(claims["aud"], list) else [claims["aud"]]
    if claims["iss"] != args.issuer or args.audience not in audiences or not isinstance(claims["sub"], str) or not claims["sub"]:
        raise ValueError("The returned subject, issuer or audience does not match")
    now = datetime.datetime.now(datetime.timezone.utc)
    expires = datetime.datetime.fromtimestamp(claims["exp"], datetime.timezone.utc)
    if expires <= now:
        raise ValueError("The provider returned an expired token")
    selected_output = Path(args.output_stem).expanduser()
    output = selected_output.parent.resolve() / selected_output.name
    if secret_file in [Path(str(output) + suffix) for suffix in (".token", ".headers", ".claims.json")]:
        raise ValueError("Token outputs must not replace the client secret")
    replace_private(Path(str(output) + ".token"), token + "\n")
    replace_private(Path(str(output) + ".headers"), "Authorization: Bearer " + token + "\n")
    replace_private(Path(str(output) + ".claims.json"), json.dumps({
        "subject": claims["sub"], "issuer": claims["iss"], "audience": args.audience,
        "expiresAt": expires.isoformat(), "scope": claims.get("scope", ""),
    }, indent=2) + "\n")
    print("Acquired protected tutorial token; verify its actual identity through /api/v1/me")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--issuer", required=True)
    parser.add_argument("--client-id", required=True)
    parser.add_argument("--audience", required=True)
    parser.add_argument("--secret-file", required=True)
    parser.add_argument("--ca-file", required=True)
    parser.add_argument("--output-stem", required=True)
    args = parser.parse_args()
    try:
        acquire(args)
    except (OSError, ValueError, KeyError, TypeError, urllib.error.URLError):
        raise SystemExit("Service-token acquisition failed; check the local issuer, private secret, client/audience and trust.") from None


if __name__ == "__main__":
    main()
