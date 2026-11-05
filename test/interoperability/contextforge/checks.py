"""SDK-independent checks shared by the native client and isolated helper tests."""

import base64
import json
import math


def proxy_progress(phase, method, content, status):
    """A fixed signal for interruption tests, without arguments or response data."""
    if phase not in ("proxy-2025-11-25", "proxy-2026-07-28") or method != "POST" or status != 200:
        return None
    try:
        body = json.loads(content)
    except (ValueError, UnicodeError):
        return None
    if isinstance(body, dict) and body.get("method") == "tools/call":
        return {"phase": phase, "method": "tools/call", "httpStatus": status}
    return None


def token_times(token):
    """Read numeric diagnostic dates only. These claims never authorize a request."""
    if not isinstance(token, str) or len(token) > 16384 or len(token.split(".")) != 3:
        return {}
    payload = token.split(".")[1]
    try:
        claims = json.loads(base64.b64decode(payload + "=" * (-len(payload) % 4), altchars=b"-_", validate=True))
    except (ValueError, UnicodeError):
        return {}
    if not isinstance(claims, dict):
        return {}
    result = {}
    for key in ("iat", "exp", "nbf"):
        value = claims.get(key)
        if type(value) is int or type(value) is float and math.isfinite(value):
            result[key] = value
    return result


async def tool_pages(client):
    found, cursors, cursor = [], set(), None
    for _ in range(8):
        page = await client.list_tools(cursor=cursor)
        if len(page.tools) > 2:
            raise AssertionError("configured native tool page size")
        found.extend(page.tools)
        cursor = page.next_cursor
        if not cursor:
            if len(found) != len({tool.name for tool in found}):
                raise AssertionError("unique tools across native pages")
            return found
        if cursor in cursors:
            raise AssertionError("native tool cursor made progress")
        cursors.add(cursor)
    raise AssertionError("native tool page bound")
