# Generated synchronous bridge: no background threads or persistent event loops.
import asyncio
from typing import Any, Coroutine, TypeVar
from catalog_sdk.errors import SDKError

T = TypeVar("T")


def run_sync(coro: Coroutine[Any, Any, T]) -> T:
    try:
        asyncio.get_running_loop()
    except RuntimeError:
        return asyncio.run(coro)
    coro.close()
    raise SDKError("use_async_method")
