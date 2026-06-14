from __future__ import annotations

import asyncio
import json
from typing import Any

from ..metrics import extract_usage


def _tool_result_text(content: Any) -> str:
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        parts: list[str] = []
        for part in content:
            if isinstance(part, dict) and part.get("text") is not None:
                parts.append(str(part["text"]))
            elif isinstance(part, str):
                parts.append(part)
            else:
                parts.append(
                    json.dumps(part, ensure_ascii=False, separators=(",", ":"))
                )
        return "".join(parts)
    if content is None:
        return ""
    if isinstance(content, dict) and content.get("text") is not None:
        return str(content["text"])
    return json.dumps(content, ensure_ascii=False, separators=(",", ":"))


def _adapt_content(content: Any) -> Any:
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        return [_adapt_content_part(part) for part in content]
    if isinstance(content, dict):
        return [_adapt_content_part(content)]
    return str(content)


def _adapt_content_part(part: Any) -> Any:
    if not isinstance(part, dict):
        return {"type": "text", "text": str(part)}
    part_type = part.get("type")
    if part_type in {"text", "image_url"}:
        return part
    if part_type in {"input_text", "output_text"}:
        return {"type": "text", "text": str(part.get("text", ""))}
    if part_type == "image":
        source = part.get("source")
        if isinstance(source, dict) and source.get("type") == "base64":
            media_type = source.get("media_type") or "image/png"
            data = source.get("data") or ""
            return {
                "type": "image_url",
                "image_url": {"url": f"data:{media_type};base64,{data}"},
            }
    if part_type == "input_image":
        image_url = part.get("image_url") or part.get("file_id") or ""
        return {"type": "image_url", "image_url": {"url": str(image_url)}}
    text = part.get("text")
    if text is not None:
        return {"type": "text", "text": str(text)}
    return {
        "type": "text",
        "text": json.dumps(part, ensure_ascii=False, separators=(",", ":")),
    }


async def _flush_stream_event() -> None:
    # Let the ASGI server flush each SSE event instead of coalescing a burst
    # from one upstream network chunk into a single downstream TCP write.
    await asyncio.sleep(0)


def _openai_choices(data: dict[str, Any]) -> list[dict[str, Any]]:
    choices = data.get("choices")
    if not isinstance(choices, list):
        return []
    return [choice for choice in choices if isinstance(choice, dict)]


def _delta_text(delta: dict[str, Any]) -> str:
    content = delta.get("content", "")
    return _message_text(content)


def _message_text(content: Any) -> str:
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        parts: list[str] = []
        for part in content:
            if isinstance(part, dict) and part.get("text") is not None:
                parts.append(str(part["text"]))
            elif isinstance(part, str):
                parts.append(part)
        return "".join(parts)
    if content is None:
        return ""
    return str(content)


def _stream_usage(buffer: str, chunk: bytes) -> tuple[str, dict[str, Any] | None]:
    buffer += chunk.decode("utf-8", errors="replace")
    usage = None
    while "\n" in buffer:
        line, buffer = buffer.split("\n", 1)
        line = line.strip()
        if not line.startswith("data:"):
            continue
        data = line[5:].strip()
        if not data or data == "[DONE]":
            continue
        chunk_usage = extract_usage(_json_text(data))
        if chunk_usage is not None:
            usage = chunk_usage
    return buffer, usage


def _json_text(content: str) -> Any:
    try:
        return json.loads(content)
    except ValueError:
        return None
