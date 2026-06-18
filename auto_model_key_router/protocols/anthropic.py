from __future__ import annotations

import json
from typing import Any

from ..metrics import extract_usage
from .common import _delta_text, _json_text, _message_text, _openai_choices


def _anthropic_stream_events(
    buffer: str,
    chunk: bytes,
    stream_state: dict[str, Any],
) -> tuple[str, list[bytes], dict[str, Any] | None]:
    buffer += chunk.decode("utf-8", errors="replace")
    events: list[bytes] = []
    usage = None
    while "\n" in buffer:
        line, buffer = buffer.split("\n", 1)
        line = line.strip()
        if not line.startswith("data:"):
            continue
        data = line[5:].strip()
        if not data or data == "[DONE]":
            continue
        payload = _json_text(data)
        if not isinstance(payload, dict):
            continue
        chunk_usage = extract_usage(payload)
        if chunk_usage is not None:
            usage = chunk_usage
        for choice in _openai_choices(payload):
            delta = choice.get("delta") if isinstance(choice.get("delta"), dict) else {}
            text = _delta_text(delta)
            if text:
                text_index = stream_state.get("text_index")
                if not isinstance(text_index, int) or stream_state["text_stopped"]:
                    text_index = int(stream_state["next_content_index"])
                    stream_state["next_content_index"] = text_index + 1
                    stream_state["text_index"] = text_index
                    stream_state["text_stopped"] = False
                    events.append(
                        _anthropic_sse(
                            "content_block_start",
                            {
                                "type": "content_block_start",
                                "index": text_index,
                                "content_block": {"type": "text", "text": ""},
                            },
                        )
                    )
                events.append(
                    _anthropic_sse(
                        "content_block_delta",
                        {
                            "type": "content_block_delta",
                            "index": text_index,
                            "delta": {"type": "text_delta", "text": text},
                        },
                    )
                )
            tool_calls = delta.get("tool_calls")
            if isinstance(tool_calls, list):
                for fallback_index, tool_call in enumerate(tool_calls):
                    if isinstance(tool_call, dict):
                        events.extend(
                            _anthropic_stream_tool_events(
                                stream_state, tool_call, fallback_index
                            )
                        )
            function_call = delta.get("function_call")
            if isinstance(function_call, dict):
                events.extend(
                    _anthropic_stream_tool_events(
                        stream_state, {"index": 0, "function": function_call}, 0
                    )
                )
            if choice.get("finish_reason") is not None:
                stream_state["stop_reason"] = _anthropic_stop_reason(
                    choice.get("finish_reason")
                )
    return buffer, events, usage


def _anthropic_stream_tool_events(
    stream_state: dict[str, Any],
    tool_call: dict[str, Any],
    fallback_index: int,
) -> list[bytes]:
    events: list[bytes] = []
    text_index = stream_state.get("text_index")
    if isinstance(text_index, int) and not stream_state["text_stopped"]:
        events.append(
            _anthropic_sse(
                "content_block_stop",
                {"type": "content_block_stop", "index": text_index},
            )
        )
        stream_state["text_stopped"] = True

    try:
        tool_index = int(tool_call.get("index", fallback_index))
    except (TypeError, ValueError):
        tool_index = fallback_index
    accumulated = stream_state["tool_calls"].setdefault(
        tool_index,
        {
            "id": "",
            "name": "",
            "arguments": "",
            "emitted_arguments": 0,
            "content_index": None,
            "started": False,
            "stopped": False,
        },
    )
    if tool_call.get("id") and not accumulated["id"]:
        accumulated["id"] = str(tool_call["id"])
    function = tool_call.get("function")
    if isinstance(function, dict):
        if function.get("name") and not accumulated["name"]:
            accumulated["name"] = str(function["name"])
        arguments = function.get("arguments")
        if isinstance(arguments, str):
            accumulated["arguments"] += arguments
        elif arguments is not None:
            accumulated["arguments"] += json.dumps(
                arguments, ensure_ascii=False, separators=(",", ":")
            )

    if (
        stream_state["active_tool_index"] is None
        and accumulated["id"]
        and accumulated["name"]
    ):
        stream_state["active_tool_index"] = tool_index
        events.extend(_start_anthropic_stream_tool(stream_state, tool_index))
    if stream_state["active_tool_index"] == tool_index:
        events.extend(_anthropic_stream_tool_argument_events(accumulated))
    return events


def _start_anthropic_stream_tool(
    stream_state: dict[str, Any], tool_index: int
) -> list[bytes]:
    tool_call = stream_state["tool_calls"][tool_index]
    if tool_call["started"]:
        return []
    content_index = int(stream_state["next_content_index"])
    stream_state["next_content_index"] = content_index + 1
    tool_call["content_index"] = content_index
    tool_call["started"] = True
    return [
        _anthropic_sse(
            "content_block_start",
            {
                "type": "content_block_start",
                "index": content_index,
                "content_block": {
                    "type": "tool_use",
                    "id": str(tool_call["id"] or f"call_amkr_{tool_index}"),
                    "name": str(tool_call["name"] or ""),
                    "input": {},
                },
            },
        )
    ]


def _anthropic_stream_tool_argument_events(tool_call: dict[str, Any]) -> list[bytes]:
    arguments = str(tool_call["arguments"])
    emitted_arguments = int(tool_call["emitted_arguments"])
    if not tool_call["started"] or emitted_arguments >= len(arguments):
        return []
    tool_call["emitted_arguments"] = len(arguments)
    return [
        _anthropic_sse(
            "content_block_delta",
            {
                "type": "content_block_delta",
                "index": int(tool_call["content_index"]),
                "delta": {
                    "type": "input_json_delta",
                    "partial_json": arguments[emitted_arguments:],
                },
            },
        )
    ]


def _finish_anthropic_stream_tools(stream_state: dict[str, Any]) -> list[bytes]:
    events: list[bytes] = []
    active_tool_index = stream_state.get("active_tool_index")
    ordered_indexes = []
    if isinstance(active_tool_index, int):
        ordered_indexes.append(active_tool_index)
    ordered_indexes.extend(
        index
        for index in sorted(stream_state["tool_calls"])
        if index != active_tool_index
    )

    for tool_index in ordered_indexes:
        tool_call = stream_state["tool_calls"][tool_index]
        if tool_call["stopped"]:
            continue
        if not tool_call["started"]:
            if not tool_call["id"]:
                tool_call["id"] = f"call_amkr_{tool_index}"
            events.extend(_start_anthropic_stream_tool(stream_state, tool_index))
        events.extend(_anthropic_stream_tool_argument_events(tool_call))
        events.append(
            _anthropic_sse(
                "content_block_stop",
                {
                    "type": "content_block_stop",
                    "index": int(tool_call["content_index"]),
                },
            )
        )
        tool_call["stopped"] = True
    stream_state["active_tool_index"] = None
    return events


def _anthropic_sse(event: str, payload: dict[str, Any]) -> bytes:
    return f"event: {event}\ndata: {json.dumps(payload, ensure_ascii=False, separators=(',', ':'))}\n\n".encode(
        "utf-8"
    )


def _anthropic_stream_usage(usage: dict[str, Any] | None) -> dict[str, int]:
    return _anthropic_usage(usage, include_input=True)


def _anthropic_usage(
    usage: dict[str, Any] | None, *, include_input: bool = True
) -> dict[str, int]:
    source = usage or {}
    result: dict[str, int] = {}
    if include_input:
        result["input_tokens"] = _usage_int(
            source.get("prompt_tokens") or source.get("input_tokens")
        )
    result["output_tokens"] = _usage_int(
        source.get("completion_tokens") or source.get("output_tokens")
    )
    for key in ("cache_creation_input_tokens", "cache_read_input_tokens"):
        value = _usage_int(source.get(key))
        if value:
            result[key] = value
    cached_tokens = _usage_int(source.get("cached_tokens"))
    if cached_tokens and "cache_read_input_tokens" not in result:
        result["cache_read_input_tokens"] = cached_tokens
    return result


def _usage_int(value: Any) -> int:
    if isinstance(value, int):
        return value
    if isinstance(value, float):
        return int(value)
    if isinstance(value, str) and value.isdigit():
        return int(value)
    return 0


def _anthropic_message_response(
    data: Any, requested_model_id: str
) -> dict[str, Any] | None:
    if not isinstance(data, dict):
        return None
    if data.get("type") == "message" and isinstance(data.get("content"), list):
        return data
    choice = next(iter(_openai_choices(data)), None)
    if choice is None:
        return None
    message = choice.get("message") if isinstance(choice.get("message"), dict) else {}
    text = _message_text(message.get("content", ""))
    content: list[dict[str, Any]] = []
    if text:
        content.append({"type": "text", "text": text})
    content.extend(_anthropic_tool_use_blocks(message))
    if not content:
        content.append({"type": "text", "text": ""})
    usage = data.get("usage") if isinstance(data.get("usage"), dict) else {}
    return {
        "id": str(data.get("id") or "msg_amkr"),
        "type": "message",
        "role": "assistant",
        "model": requested_model_id,
        "content": content,
        "stop_reason": _anthropic_stop_reason(
            choice.get("finish_reason"),
            any(block.get("type") == "tool_use" for block in content),
        ),
        "stop_sequence": None,
        "usage": _anthropic_usage(usage),
    }


def _anthropic_tool_use_blocks(message: dict[str, Any]) -> list[dict[str, Any]]:
    tool_calls = message.get("tool_calls")
    if not isinstance(tool_calls, list):
        function_call = message.get("function_call")
        tool_calls = (
            [{"id": "call_amkr_0", "function": function_call}]
            if isinstance(function_call, dict)
            else []
        )
    blocks: list[dict[str, Any]] = []
    for index, tool_call in enumerate(tool_calls):
        if not isinstance(tool_call, dict):
            continue
        function = tool_call.get("function")
        if not isinstance(function, dict):
            continue
        arguments = function.get("arguments", {})
        if isinstance(arguments, str):
            parsed_arguments = _json_text(arguments)
            arguments = parsed_arguments if parsed_arguments is not None else {}
        blocks.append(
            {
                "type": "tool_use",
                "id": str(tool_call.get("id") or f"call_amkr_{index}"),
                "name": str(function.get("name") or ""),
                "input": arguments if arguments is not None else {},
            }
        )
    return blocks


def _anthropic_stop_reason(reason: Any, has_tool_use: bool = False) -> str:
    if reason == "length":
        return "max_tokens"
    if has_tool_use or reason in {"tool_calls", "function_call"}:
        return "tool_use"
    return "end_turn"
