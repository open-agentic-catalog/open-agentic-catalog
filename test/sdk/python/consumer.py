"""Executed with -I from a clean environment containing the built wheel only."""
from importlib.metadata import distributions
from importlib.resources import files
import json
from pathlib import Path
import sys
from catalog_sdk import Client, Config
from catalog_sdk.generated.api.service_api import ServiceApi

assert ServiceApi(Client(Config(origin="https://catalog.example.com")))
installed = {d.metadata["Name"].lower().replace("_", "-"): d.version for d in distributions()}
assert installed == json.loads(Path(sys.argv[1]).read_text()), installed
assert "open-agentic-catalog" in installed
assert files("catalog_sdk").joinpath("py.typed").is_file()
metadata = next(d for d in distributions() if d.metadata["Name"].lower() == "open-agentic-catalog")
assert Client(Config(origin="https://catalog.example.com")).user_agent == "open-agentic-catalog-python/" + metadata.version
names = {str(path) for path in metadata.files}
assert any(name.endswith("/licenses/LICENSE") for name in names)
assert any(name.endswith("/licenses/NOTICE") for name in names)
assert not any(name.endswith((".log", ".pem", ".key", ".pyc", ".pyo")) or "__pycache__/" in name or "tests/" in name or "docs/" in name for name in names)
assert not set(installed).intersection({"openapi-generator-cli", "mypy", "coverage", "ruff", "hatchling", "build", "certifi", "httpx"})
print("Clean Python wheel consumer imported with runtime dependencies only")
