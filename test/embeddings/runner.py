"""Qualify the Go client against one owned local model, not a hosted provider.

Reuse the relevance fixture's pinned acquisition, offline launcher and cleanup.
This protocol test does not change judgments or claim runtime search integration.
"""

import argparse
import ipaddress
import json
from pathlib import Path
import signal
import sys
import tempfile
from urllib.parse import urlsplit

REPO = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(REPO / "test/search-relevance"))
import comparison  # noqa: E402 -- Reuse the maintained profile/cache definitions.
import runner  # noqa: E402 -- One owned model launcher and cleanup implementation.


def settings(profile, origin):
    """Build a fixed private-endpoint configuration without credential authority."""
    url = urlsplit(origin)
    address = ipaddress.ip_address(url.hostname)
    if url.scheme != "http" or url.port != 80 or not address.is_private or address.is_loopback or address.is_link_local or address.is_unspecified:
        raise ValueError("expected an owned private model address")
    if url.path or url.query or url.fragment or url.username or url.password:
        raise ValueError("expected an origin without path or credentials")
    return {"enabled": True, "endpoint": origin + "/v1/embeddings", "model": profile["model"],
            "profileRevision": profile["revision"], "dimensions": profile["dimensions"],
            "queryPrefix": profile["query_prefix"], "allowHttp": True,
            "allowedAddressPrefixes": [str(ipaddress.ip_network(f"{address}/{address.max_prefixlen}"))],
            "batchSize": 8, "maxInputBytes": 8192, "queryTimeout": "10s", "batchTimeout": "20s"}


def qualify(profile, model):
    """Compile first, then execute the actual Go adapter against verified TEI."""
    runner.verify_model(profile, model)
    with tempfile.TemporaryDirectory(prefix="catalog-embedding-client-") as temporary:
        binary = str(Path(temporary) / "qualify")
        runner.command(["go", "build", "-o", binary, "./test/embeddings/qualify"], timeout=180)
        with runner.owned_resources() as names:
            origin, _ = runner.start_tei(profile, names, model)
            result = json.loads(runner.command([binary], data=json.dumps(settings(profile, origin)), timeout=90))
            if (not isinstance(result, dict)
                    or any(result.get(flag) is not True for flag in ("batchOrdering", "repeatStable", "overlengthRejected"))
                    or type(result.get("dimensions")) is not int or result["dimensions"] != profile["dimensions"]
                    or type(result.get("elapsedMillis")) is not int or result["elapsedMillis"] < 0):
                raise ValueError("invalid embedding qualification report")
            runner.verify_model(profile, model)
        result["cleanup"] = "complete"
        result["model"] = profile["model"]
        result["revision"] = profile["revision"]
        result["teiImage"] = profile["tei_image"]
        print(json.dumps(result, indent=2), flush=True)


def main():
    """Separate explicit acquisition from offline owned inference execution."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("install", "test"))
    parser.add_argument("--profile", choices=("minilm-l6-v1", "bge-small-v1"), default="minilm-l6-v1")
    args = parser.parse_args()
    profile = json.loads((comparison.PROFILES / (args.profile + ".json")).read_text())
    model = comparison.cache(args.profile)
    if args.action == "install":
        runner.install(profile, model, postgres=False)
    else:
        def interrupted(signum, frame):
            raise KeyboardInterrupt(f"signal {signum}")
        previous = signal.signal(signal.SIGTERM, interrupted)
        try:
            qualify(profile, model)
        finally:
            signal.signal(signal.SIGTERM, previous)


if __name__ == "__main__":
    main()
