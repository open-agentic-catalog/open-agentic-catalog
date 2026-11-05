"""Fixed SDK diagnostics. Private data is available only through explicit results."""


class SDKError(Exception):
    """A fixed failure classification, with safe HTTP correlation when available."""

    def __init__(self, code: str, *, status: int | None = None, request_id: str | None = None):
        self.code = code
        self.status = status
        # Untrusted response headers must not appear in default diagnostics either.
        self.request_id = request_id
        super().__init__("Catalog SDK: " + code)
