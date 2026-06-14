from __future__ import annotations

import anyio

from auto_model_key_router.runtime import RuntimeManager, RuntimeResources


class ClosingClient:
    def __init__(self) -> None:
        self.closed = 0

    async def aclose(self) -> None:
        self.closed += 1


class ClosingMetrics:
    def __init__(self) -> None:
        self.closed = 0

    async def close(self) -> None:
        self.closed += 1


def resources(
    config: object, client: ClosingClient, metrics: ClosingMetrics
) -> RuntimeResources:
    return RuntimeResources(config, object(), metrics, client)  # type: ignore[arg-type]


def test_retired_resources_close_after_last_lease() -> None:
    async def run() -> tuple[int, int]:
        first_client = ClosingClient()
        first_metrics = ClosingMetrics()
        manager = RuntimeManager(resources(object(), first_client, first_metrics))
        lease = await manager.acquire()

        await manager.replace(resources(object(), ClosingClient(), ClosingMetrics()))
        assert first_client.closed == 0
        assert first_metrics.closed == 0

        await lease.release()
        await manager.close()
        return first_client.closed, first_metrics.closed

    assert anyio.run(run) == (1, 1)


def test_shared_resources_close_only_after_final_generation() -> None:
    async def run() -> tuple[int, int]:
        client = ClosingClient()
        metrics = ClosingMetrics()
        manager = RuntimeManager(resources(object(), client, metrics))

        await manager.replace(resources(object(), client, metrics))
        assert client.closed == 0
        assert metrics.closed == 0

        await manager.close()
        return client.closed, metrics.closed

    assert anyio.run(run) == (1, 1)
