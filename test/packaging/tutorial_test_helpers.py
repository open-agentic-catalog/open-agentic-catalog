"""Inert source loading and synthetic inputs for the local tutorial tests."""

import importlib.util
import json
from pathlib import Path


SOURCE = Path(__file__).resolve().parents[2] / "examples/tutorials/local-oidc"
PROJECT = "catalog-docs-0123abcd"


def load_example(name):
    specification = importlib.util.spec_from_file_location("tutorial_" + name, SOURCE / (name + ".py"))
    module = importlib.util.module_from_spec(specification)
    specification.loader.exec_module(module)
    return module


def inputs(directory):
    """Supply a valid generated selection without credentials or running services."""
    return {
        "COMPOSE_PROJECT_NAME": PROJECT,
        "CATALOG_TUTORIAL_DIRECTORY": str(directory),
        "CATALOG_TUTORIAL_SOURCE": str(SOURCE),
        "CATALOG_TUTORIAL_BACKEND_IMAGE": "example-backend:unrun",
        "CATALOG_TUTORIAL_UI_IMAGE": "example-ui:unrun",
        "CATALOG_TUTORIAL_FRONTEND_IMAGE": "example-frontend:unrun",
        "CATALOG_TUTORIAL_SUBNET": "172.29.245.0/24",
        "CATALOG_TUTORIAL_FRONTEND_ADDRESS": "172.29.245.10",
    }


def write_inputs(directory, values=None):
    values = inputs(directory) if values is None else values
    path = directory / "compose.env"
    path.write_text("".join(key + "=" + json.dumps(value) + "\n" for key, value in values.items()), encoding="ascii")
    path.chmod(0o600)
    return path
