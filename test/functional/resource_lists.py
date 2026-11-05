"""Independent inventory/page oracles; no Catalog code or optional tools."""

from datetime import datetime


# Go's documented Unicode whitespace boundary; Python's default strip also
# removes U+001C-U+001F, which remain literal opaque-identifier characters.
IDENTIFIER_SPACE = "\t\n\v\f\r \x85\xa0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000"


def canonical_filter(value):
    """Normalize an accepted query for comparison, without validating via the API."""
    value = value.strip(IDENTIFIER_SPACE)
    parts = value.split(":", 3)
    if len(parts) == 4 and parts[0].lower() == "urn" and parts[1].lower() == "air":
        # Preserve namespace/name spelling. Go uses simple Unicode lowercase.
        domain = parts[2].removesuffix(".").replace("\u0130", "i").lower()
        return "urn:air:" + domain + ":" + parts[3]
    return value


def check_page(name, page, inventory, *, limit=20, kind="", canonical_identifier="", parent=None, offset=0):
    if name == "listResources":
        identifier = canonical_filter(canonical_identifier)
        expected = sorted((item for item in inventory if (not kind or item["kind"] == kind)
                           and (not canonical_identifier or item["canonicalIdentifier"] == identifier)),
                          key=lambda item: (datetime.fromisoformat(item["updatedAt"]), item["id"]), reverse=True)
    else:
        assert name == "listResourceVersions" and parent
        expected = sorted((item for item in inventory if item["resourceId"] == parent), key=lambda item: item["id"])
    wanted = expected[offset:offset + limit]
    def retained(item):
        return {key: datetime.fromisoformat(value) if key in {"createdAt", "updatedAt"} else value for key, value in item.items()}
    assert [retained(item) for item in page["items"]] == [retained(item) for item in wanted], "incorrect list selection/order/continuation"
    assert len({item["id"] for item in page["items"]}) == len(wanted), "duplicate list identity"
    assert bool(page.get("nextCursor")) == (offset + limit < len(expected)), "incorrect list continuation"


def check_seed(item, seed):
    assert (item["id"], item["kind"], item["canonicalIdentifier"]) == (
        seed["resourceId"], seed["kind"], seed["identifier"]), "seed Resource identity mismatch"
    assert item["tenantId"] == "00000000-0000-7000-8000-000000000001", "seed Resource tenant mismatch"
