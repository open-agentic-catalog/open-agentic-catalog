#!/usr/bin/env python3
"""Read the actual service identity and local Catalog through the Python SDK."""

import argparse
import json
from pathlib import Path

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--origin", required=True)
    parser.add_argument("--token-file", required=True)
    parser.add_argument("--claims-file", required=True)
    parser.add_argument("--ca-file", required=True)
    args = parser.parse_args()
    try:
        claims = json.loads(Path(args.claims_file).read_text(encoding="ascii"))
        token = Path(args.token_file).read_text(encoding="ascii").strip()
        if not isinstance(claims, dict) or not isinstance(claims.get("subject"), str) or not claims["subject"] or not token:
            raise ValueError("missing service inputs")
    except (OSError, ValueError, RecursionError):
        raise SystemExit("Cannot read the service token or expected subject from the input files") from None
    # Reject invalid local inputs before loading the generated SDK models.
    from catalog_sdk import Client, Config, SDKError
    from catalog_sdk.generated.api.catalog_discovery_api import CatalogDiscoveryApi
    from catalog_sdk.generated.api.my_access_api import MyAccessApi
    from catalog_sdk.generated.models.catalog_search import CatalogSearch

    try:
        client = Client(Config(origin=args.origin, token=token, ca_file=args.ca_file))
        access = MyAccessApi(client).get_current_access_sync_with_http_info()
        if access.status_code != 200 or access.data.actor_type != "service" or access.data.subject != claims["subject"]:
            raise SystemExit("The API did not accept the expected service identity")
        if "resource.read" not in access.data.permissions:
            raise SystemExit("The service has no current Reader permission")
        result = CatalogDiscoveryApi(client).search_catalog_sync_with_http_info(catalog_search=CatalogSearch(origins=["local"], limit=10))
        if result.status_code != 200:
            raise SystemExit("The local metadata read failed")
    except SDKError as error:
        raise SystemExit("The Catalog read failed: " + error.code) from None
    print("Python SDK verified the service identity and read local Catalog metadata")


if __name__ == "__main__":
    main()
