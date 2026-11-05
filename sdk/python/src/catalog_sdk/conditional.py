"""Opaque object fallback for the generator's unsupported JSON Schema conditionals."""
from typing import Any, Self
from pydantic import ConfigDict, RootModel


class ConditionalObject(RootModel[dict[str, Any]]):
    """Preserve all keys. The API, not this container, validates conditional rules."""
    model_config = ConfigDict(hide_input_in_errors=True)

    def to_dict(self) -> dict[str, Any]:
        return self.root

    @classmethod
    def from_dict(cls, value: dict[str, Any]) -> Self:
        return cls.model_validate(value)
