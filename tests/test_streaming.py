from __future__ import annotations

import asyncio
from collections.abc import AsyncIterator

import pytest

from auto_model_key_router import streaming


async def delayed_bytes(
    delays_and_chunks: tuple[tuple[float, bytes], ...],
) -> AsyncIterator[bytes]:
    for delay, chunk in delays_and_chunks:
        await asyncio.sleep(delay)
        yield chunk


def test_iter_stream_bytes_yields_timely_chunks() -> None:
    async def consume() -> list[bytes]:
        loop = asyncio.get_running_loop()
        return [
            chunk
            async for chunk in streaming.iter_stream_bytes(
                delayed_bytes(((0, b"first"), (0, b"second"))),
                first_byte_deadline=loop.time() + 0.1,
                idle_timeout=0.1,
            )
        ]

    assert asyncio.run(consume()) == [b"first", b"second"]


@pytest.mark.parametrize(
    ("delays_and_chunks", "first_timeout", "idle_timeout", "stage"),
    [
        (((0.05, b"first"),), 0.01, 0.1, "first stream byte"),
        (((0, b"first"), (0.05, b"second")), 0.1, 0.01, "next stream byte"),
    ],
)
def test_iter_stream_bytes_timeout_identifies_stage(
    delays_and_chunks: tuple[tuple[float, bytes], ...],
    first_timeout: float,
    idle_timeout: float,
    stage: str,
) -> None:
    async def consume() -> None:
        loop = asyncio.get_running_loop()
        async for _ in streaming.iter_stream_bytes(
            delayed_bytes(delays_and_chunks),
            first_byte_deadline=loop.time() + first_timeout,
            idle_timeout=idle_timeout,
        ):
            pass

    with pytest.raises(TimeoutError, match=stage):
        asyncio.run(consume())
