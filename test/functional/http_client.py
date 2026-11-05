"""Bounded, redirect-refusing HTTP for invocation-owned test listeners."""

import json
from email.utils import parsedate_to_datetime
import ssl
import time
import urllib.error
import urllib.request


MAX_RESPONSE = 8 << 20
MAX_HEADERS = 64 << 10
REQUEST_SECONDS = 10


class ClientFailure(Exception):
    """Fixed safe diagnostics only; never include bodies, URLs or credentials."""


def response_date(headers):
    """Read one bounded UTC response Date for transient diagnostic comparison."""
    dates = [value for name, value in headers.items() if name.lower() == "date"]
    if len(dates) != 1 or not isinstance(dates[0], str) or len(dates[0]) > 64:
        return None
    try:
        value = parsedate_to_datetime(dates[0])
        if value.utcoffset() is not None and value.utcoffset().total_seconds() == 0:
            return int(value.timestamp())
    except (OverflowError, TypeError, ValueError):
        pass
    return None


def fixture_tls_context(cafile):
    """Verify fixture identities against the selected private CA with TLS 1.2+."""
    context = ssl.create_default_context(cafile=cafile)
    context.minimum_version = ssl.TLSVersion.TLSv1_2
    return context


class NoRedirect(urllib.request.HTTPRedirectHandler):
    """Keep fixture credentials on the explicitly selected test listener."""

    def redirect_request(self, _request, _file, _code, _message, _headers, _url):
        """Refuse every redirect rather than following even another local origin."""
        raise ClientFailure("functional HTTP redirect refused")


def request(value, ssl_context=None):
    """Return status, headers and bounded bytes, including HTTP error responses.

    Environment proxies and redirects are disabled. Framing, header, body and
    elapsed-time checks fail with fixed diagnostics that exclude response content.
    The caller supplies an invocation-owned destination and any fixture CA trust.
    """
    started = time.monotonic()
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect(),
                                        urllib.request.HTTPSHandler(context=ssl_context))
    try:
        try:
            response = opener.open(value, timeout=REQUEST_SECONDS)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            if sum(len(key) + len(item) + 4 for key, item in response.headers.items()) > MAX_HEADERS:
                raise ClientFailure("functional HTTP header limit")
            lengths = response.headers.get_all("Content-Length", [])
            transfer = response.headers.get("Transfer-Encoding")
            if len(lengths) > 1 or (lengths and transfer) or (transfer and transfer.lower() != "chunked"):
                raise ClientFailure("functional HTTP invalid framing")
            expected = None
            if lengths:
                value_length = lengths[0].strip()
                if not value_length.isascii() or not value_length.isdecimal() or len(value_length) > 20:
                    raise ClientFailure("functional HTTP invalid framing")
                expected = int(value_length)
            # HEAD and 304 may describe the selected representation's length
            # without transferring that representation.
            if value.get_method() == "HEAD" or response.code in (204, 304):
                expected = 0
            if expected is not None and expected > MAX_RESPONSE:
                raise ClientFailure("functional HTTP response limit")
            blocks = []
            size = 0
            while True:
                if time.monotonic() - started >= REQUEST_SECONDS:
                    raise ClientFailure("functional HTTP deadline")
                block = response.read1(min(65536, MAX_RESPONSE + 1 - size))
                size += len(block)
                if size > MAX_RESPONSE:
                    raise ClientFailure("functional HTTP response limit")
                if not block:
                    if expected is not None and size != expected:
                        raise ClientFailure("functional HTTP incomplete response")
                    return response.code, response.headers, b"".join(blocks)
                blocks.append(block)
    except ClientFailure:
        raise
    except Exception:
        raise ClientFailure("functional HTTP transport failed") from None


def decode(raw):
    """Decode bounded JSON without duplicate keys or non-finite numeric constants."""
    def pairs(values):
        """Reject repeated object members before a dictionary can hide them."""
        result = {}
        for key, value in values:
            if key in result:
                raise ValueError()
            result[key] = value
        return result
    def constant(_value):
        """Reject Python's otherwise accepted NaN and Infinity JSON extensions."""
        raise ValueError()
    try:
        if len(raw) > MAX_RESPONSE:
            raise ValueError()
        return json.loads(raw, object_pairs_hook=pairs, parse_constant=constant)
    except (ValueError, UnicodeError):
        raise ClientFailure("functional JSON response invalid") from None
