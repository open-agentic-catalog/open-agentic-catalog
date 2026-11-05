"""Maintained entry point for complete generated operations and exact raw bytes."""
from __future__ import annotations

from typing import Any

from . import generated
from .errors import SDKError
from .transport import Config, HTTPBoundary, Response
from .generated.api_client import ApiClient
from .generated.api_response import ApiResponse
from .generated.configuration import Configuration


class Client(ApiClient):
    """Pass this client to a generated API class. Prefer with_http_info results."""

    def __init__(self, config: Config):
        # Do not construct the generator's unmanaged transport or global defaults.
        self.configuration = Configuration(host=config.origin.rstrip("/"), debug=False)
        self.rest_client = HTTPBoundary(config)
        self.default_headers = {"User-Agent": "open-agentic-catalog-python/" + generated.__version__}
        self.cookie = None
        self.client_side_validation = True

    def select_header_content_type(self, content_types: list[str]) -> str | None:
        # Prefer the implemented JSON alternative over merge-patch advertisements.
        if "application/json" in content_types:
            return "application/json"
        return super().select_header_content_type(content_types)

    def param_serialize(self, *args: Any, **kwargs: Any) -> Any:
        try:
            return super().param_serialize(*args, **kwargs)
        except Exception:
            raise SDKError("invalid_request") from None

    async def raw(self, method: str, path: str, *, body: bytes | None = None,
                  headers: dict[str, str] | None = None, timeout: float | None = None) -> Response:
        """Send one exact request to the configured origin; never follow a response."""
        if body is not None and not isinstance(body, bytes):
            raise SDKError("invalid_raw_body")
        if not path.startswith("/") or path.startswith("//") or "\\" in path:
            raise SDKError("invalid_path")
        return await self.rest_client.request(method, str(self.rest_client.origin).rstrip("/") + path,
                                              headers=headers, body=body, _request_timeout=timeout)

    def raw_sync(self, method: str, path: str, *, body: bytes | None = None,
                 headers: dict[str, str] | None = None, timeout: float | None = None) -> Response:
        from .generated.sync_helper import run_sync
        return run_sync(self.raw(method, path, body=body, headers=headers, timeout=timeout))

    def response_deserialize(self, response_data: Response,
                             response_types_map: dict[str, Any] | None = None) -> ApiResponse[Any]:
        """Decode bounded results without exceptions that contain response bodies."""
        mapping = response_types_map or {}
        kind = mapping.get(str(response_data.status), mapping.get("default"))
        try:
            data = None
            if kind in ("file", "bytes", "bytearray"):
                data = response_data.data
            elif kind is not None:
                if not response_data.data:
                    raise SDKError("invalid_response")
                data = self.deserialize(response_data.data.decode("utf-8"), kind,
                                        response_data.headers.get("Content-Type"))
                # Current body-bearing response schemas exclude top-level null.
                # The generated decoder returns None before validating a model.
                if data is None:
                    raise SDKError("invalid_response")
            result = ApiResponse(status_code=response_data.status, headers=None,
                                 raw_data=response_data.data, data=data)
            result.headers = response_data.headers
            return result
        except Exception:
            raise SDKError("invalid_response", status=response_data.status,
                           request_id=response_data.headers.get("X-Request-ID")) from None
