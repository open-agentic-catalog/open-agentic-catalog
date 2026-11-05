"""Credential-bound, request-scoped HTTPX2 transport with a whole-call deadline."""
from __future__ import annotations

import asyncio
from contextlib import contextmanager
from contextvars import ContextVar
from dataclasses import dataclass, field
import ipaddress
import json
import math
import ssl
from typing import Any, Callable, Iterator
from urllib.parse import urlsplit

import httpx2 as httpx

from .errors import SDKError

_LIMIT: ContextVar[int | None] = ContextVar("catalog_response_limit", default=None)


def _budget(value: int) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or not 0 < value <= 1024**3:
        raise SDKError("invalid_byte_budget")
    return value


@contextmanager
def response_limit(value: int) -> Iterator[None]:
    """Override the response budget for this context, including generated calls."""
    token = _LIMIT.set(_budget(value))
    try:
        yield
    finally:
        _LIMIT.reset(token)


@dataclass(frozen=True, repr=False)
class Config:
    origin: str
    token: str | None = field(default=None, repr=False)
    token_provider: Callable[[], str | None] | None = field(default=None, repr=False)
    ca_file: str | None = None
    timeout: float = 30.0
    phase_timeout: float = 10.0
    request_bytes: int = 16 * 1024**2
    response_bytes: int = 8 * 1024**2
    header_bytes: int = 32 * 1024
    allow_loopback_http: bool = False

    def __post_init__(self) -> None:
        try:
            url = urlsplit(self.origin)
            port = url.port
            valid = url.scheme in ("https", "http") and url.hostname and url.username is None and url.password is None
            # Even empty delimiters alter the generated client's appended API path.
            valid = valid and url.path in ("", "/") and "?" not in self.origin and "#" not in self.origin
            valid = valid and not any(c.isspace() or ord(c) < 32 for c in self.origin)
            valid = valid and "\\" not in self.origin and (port is None or 0 < port < 65536)
            if url.scheme == "http":
                valid = valid and self.allow_loopback_http is True and ipaddress.ip_address(url.hostname or "").is_loopback
            if not valid or self.token is not None and self.token_provider is not None:
                raise ValueError()
            for value in (self.timeout, self.phase_timeout):
                if isinstance(value, bool) or not math.isfinite(value) or value <= 0:
                    raise ValueError()
        except (ValueError, TypeError, AttributeError):
            raise SDKError("invalid_configuration") from None
        for value in (self.request_bytes, self.response_bytes, self.header_bytes):
            _budget(value)

    def __repr__(self) -> str:
        return "Config(<private>)"


@dataclass(repr=False)
class Response:
    status: int
    headers: httpx.Headers
    data: bytes

    @property
    def response(self) -> Response:
        return self

    async def read(self) -> bytes:
        return self.data

    def __repr__(self) -> str:
        return f"Response(status={self.status}, <private>)"


class HTTPBoundary:
    def __init__(self, config: Config):
        if not isinstance(config, Config):
            raise SDKError("managed_client_required")
        self.config = config
        try:
            self.ssl_context = ssl.create_default_context(cafile=config.ca_file)
            self.ssl_context.minimum_version = ssl.TLSVersion.TLSv1_2
        except (OSError, ValueError, ssl.SSLError):
            raise SDKError("invalid_tls_configuration") from None
        self.origin = httpx.URL(config.origin)

    async def close(self) -> None:
        # Each request owns and closes its client, stream and event-loop resources.
        pass

    async def request(self, method: str, url: str, headers: dict[str, Any] | None = None,
                      body: Any = None, post_params: Any = None,
                      _request_timeout: Any = None) -> Response:
        try:
            target = httpx.URL(url)
            if target.scheme != self.origin.scheme or target.host != self.origin.host or target.port != self.origin.port or target.userinfo or target.fragment:
                raise SDKError("origin_mismatch")
            if post_params:
                raise SDKError("unsupported_form_body")
            data = body if isinstance(body, bytes) else body.encode("utf-8") if isinstance(body, str) else b"" if body is None else json.dumps(body, ensure_ascii=False, allow_nan=False, separators=(",", ":")).encode("utf-8")
            if len(data) > self.config.request_bytes:
                raise SDKError("request_too_large")
            selected = {k: v for k, v in (headers or {}).items() if k.lower() not in ("authorization", "cookie", "proxy-authorization", "host", "accept-encoding", "content-length", "transfer-encoding")}
            selected["Accept-Encoding"] = "identity"
            if _request_timeout is None:
                timeout = self.config.timeout
            elif isinstance(_request_timeout, (float, int)) and not isinstance(_request_timeout, bool) and math.isfinite(_request_timeout) and _request_timeout > 0:
                timeout = min(self.config.timeout, _request_timeout)
            else:
                raise SDKError("invalid_timeout")
            started = asyncio.get_running_loop().time()
            # Synchronous providers must be bounded by the caller; no helper thread.
            token = self.config.token_provider() if self.config.token_provider else self.config.token
            if token is not None:
                if not isinstance(token, str) or not token or any(ord(c) < 33 or ord(c) > 126 for c in token):
                    raise SDKError("invalid_token")
                selected["Authorization"] = "Bearer " + token
            remaining = timeout - (asyncio.get_running_loop().time() - started)
            if remaining <= 0:
                raise SDKError("deadline_exceeded")
            return await self._exchange(method, target, selected, data, remaining)
        except SDKError:
            raise
        except TimeoutError:
            raise SDKError("deadline_exceeded") from None
        except Exception:
            raise SDKError("request_failed") from None

    def _client(self) -> httpx.AsyncClient:
        transport = httpx.AsyncHTTPTransport(verify=self.ssl_context, trust_env=False, retries=0,
                                            http2=False)
        return httpx.AsyncClient(transport=transport, trust_env=False, follow_redirects=False,
                                 timeout=self.config.phase_timeout)

    async def _exchange(self, method: str, target: httpx.URL, headers: dict[str, Any], data: bytes, timeout: float) -> Response:
        status = None
        request_id = None
        try:
            async with asyncio.timeout(timeout), self._client() as client:
                async with client.stream(method, target, headers=headers, content=data) as response:
                    status = response.status_code
                    request_id = response.headers.get("X-Request-ID")
                    if sum(len(k) + len(v) + 4 for k, v in response.headers.raw) > self.config.header_bytes:
                        raise SDKError("headers_too_large", status=status, request_id=request_id)
                    if response.headers.get("Content-Encoding", "identity").lower() != "identity":
                        raise SDKError("encoded_response", status=status, request_id=request_id)
                    limit = _LIMIT.get() or self.config.response_bytes
                    chunks = bytearray()
                    async for chunk in response.aiter_raw():
                        if len(chunks) + len(chunk) > limit:
                            raise SDKError("response_too_large", status=status, request_id=request_id)
                        chunks.extend(chunk)
                    return Response(status, response.headers, bytes(chunks))
        except SDKError:
            raise
        except TimeoutError:
            raise SDKError("deadline_exceeded", status=status, request_id=request_id) from None
        except Exception:
            raise SDKError("transport_failed", status=status, request_id=request_id) from None
