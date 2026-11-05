"""Validated inputs for the optional local Compose model, without runtime enablement."""

import importlib.util
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


def inputs(environment, verify=True):
    """Select the one pinned fixture; downloads require embeddings-install."""
    spec = importlib.util.spec_from_file_location("catalog_embedding_fixture", Path(__file__).with_name("runner.py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    name = environment.get("EMBEDDING_PROFILE", "minilm-l6-v1")
    if name not in {"minilm-l6-v1", "bge-small-v1"}:
        raise ValueError("unknown local embedding profile")
    profile = json.loads((module.comparison.PROFILES / (name + ".json")).read_text())
    model = module.comparison.cache(name)
    if verify:
        module.runner.verify_model(profile, model)
    result = dict(environment)
    result.update(CATALOG_EMBEDDING_IMAGE=profile["tei_image"], CATALOG_EMBEDDING_MODEL=profile["model"],
                  CATALOG_EMBEDDING_REVISION=profile["revision"], CATALOG_EMBEDDING_POOLING=profile["pooling"],
                  CATALOG_EMBEDDING_MODEL_DIRECTORY=str(model))
    return result


if __name__ == "__main__":
    print(json.dumps(inputs({"EMBEDDING_PROFILE": "minilm-l6-v1"})))
