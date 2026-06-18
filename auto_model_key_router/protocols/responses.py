from __future__ import annotations

import json
from typing import Any

from ..metrics import extract_usage
from .common import _delta_text, _json_text, _message_text, _openai_choices


def _responses_stream_events(
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
                if not stream_state["text_started"]:
                    events.append(
                        _responses_sse(
                            "response.output_item.added",
                            {
                                "type": "response.output_item.added",
                                "output_index": 0,
                                "item": {
                                    "type": "message",
                                    "id": "msg_amkr",
                                    "role": "assistant",
                                    "status": "in_progress",
                                    "content": [],
                                },
                            },
                        )
                    )
                    events.append(
                        _responses_sse(
                            "response.content_part.added",
                            {
                                "type": "response.content_part.added",
                                "item_id": "msg_amkr",
                                "output_index": 0,
                                "content_index": 0,
                                "part": {
                                    "type": "output_text",
                                    "text": "",
                                    "annotations": [],
                                },
                            },
                        )
                    )
                    stream_state["text_started"] = True
                stream_state["text"].append(text)
                events.append(
                    _responses_sse(
                        "response.output_text.delta",
                        {
                            "type": "response.output_text.delta",
                            "item_id": "msg_amkr",
                            "output_index": 0,
                            "content_index": 0,
                            "delta": text,
                        },
                    )
                )
            tool_calls = delta.get("tool_calls")
            if isinstance(tool_calls, list):
                for fallback_index, tool_call in enumerate(tool_calls):
                    if not isinstance(tool_call, dict):
                        continue
                    index = int(tool_call.get("index", fallback_index))
                    stored = stream_state["tool_calls"].setdefault(
                        index,
                        {"id": "", "name": "", "arguments": ""},
                    )
                    if tool_call.get("id"):
                        stored["id"] = str(tool_call["id"])
                    function = tool_call.get("function")
                    if isinstance(function, dict):
                        if function.get("name"):
                            stored["name"] += str(function["name"])
                        arguments = function.get("arguments")
                        if arguments is not None:
                            stored["arguments"] += (
                                arguments
                                if isinstance(arguments, str)
                                else json.dumps(
                                    arguments, ensure_ascii=False, separators=(",", ":")
                                )
                            )
    return buffer, events, usage


def _responses_stream_output_items(
    stream_state: dict[str, Any],
) -> list[dict[str, Any]]:
    items: list[dict[str, Any]] = []
    text = "".join(stream_state["text"])
    if text or not stream_state["tool_calls"]:
        items.append(
            {
                "type": "message",
                "id": "msg_amkr",
                "role": "assistant",
                "content": [{"type": "output_text", "text": text}],
            }
        )
    for index in sorted(stream_state["tool_calls"]):
        tool_call = stream_state["tool_calls"][index]
        call_id = str(tool_call.get("id") or f"call_amkr_{index}")
        items.append(
            {
                "type": "function_call",
                "id": f"fc_amkr_{index}",
                "call_id": call_id,
                "name": str(tool_call.get("name") or ""),
                "arguments": str(tool_call.get("arguments") or "{}"),
            }
        )
    return items


def _responses_sse(event: str, payload: dict[str, Any]) -> bytes:
    return f"event: {event}\ndata: {json.dumps(payload, ensure_ascii=False, separators=(',', ':'))}\n\n".encode(
        "utf-8"
    )


def _responses_response(data: Any, requested_model_id: str) -> dict[str, Any] | None:
    if not isinstance(data, dict):
        return None
    if data.get("object") == "response" and isinstance(data.get("output"), list):
        return data
    choice = next(iter(_openai_choices(data)), None)
    if choice is None:
        return {
            "id": str(data.get("id") or "resp_amkr"),
            "object": "response",
            "status": "completed",
            "model": requested_model_id,
            "output": [],
            "usage": _responses_usage(
                data.get("usage") if isinstance(data.get("usage"), dict) else None
            ),
        }
    message = choice.get("message") if isinstance(choice.get("message"), dict) else {}
    return {
        "id": str(data.get("id") or "resp_amkr"),
        "object": "response",
        "status": "completed",
        "model": requested_model_id,
        "output": _responses_message_output_items(message),
        "usage": _responses_usage(
            data.get("usage") if isinstance(data.get("usage"), dict) else None
        ),
    }


def _responses_message_output_items(message: dict[str, Any]) -> list[dict[str, Any]]:
    items: list[dict[str, Any]] = []
    text = _message_text(message.get("content", ""))
    if text:
        items.append(
            {
                "type": "message",
                "id": "msg_amkr",
                "role": "assistant",
                "content": [{"type": "output_text", "text": text}],
            }
        )
    tool_calls = message.get("tool_calls")
    if not isinstance(tool_calls, list):
        function_call = message.get("function_call")
        tool_calls = (
            [{"id": "call_amkr_0", "function": function_call}]
            if isinstance(function_call, dict)
            else []
        )
    for index, tool_call in enumerate(tool_calls):
        if not isinstance(tool_call, dict):
            continue
        function = tool_call.get("function")
        if not isinstance(function, dict):
            continue
        items.append(
            {
                "type": "function_call",
                "id": f"fc_amkr_{index}",
                "call_id": str(tool_call.get("id") or f"call_amkr_{index}"),
                "name": str(function.get("name") or ""),
                "arguments": function.get("arguments")
                if isinstance(function.get("arguments"), str)
                else json.dumps(
                    function.get("arguments") or {},
                    ensure_ascii=False,
                    separators=(",", ":"),
                ),
            }
        )
    if not items:
        items.append(
            {
                "type": "message",
                "id": "msg_amkr",
                "role": "assistant",
                "content": [{"type": "output_text", "text": ""}],
            }
        )
    return items


def _responses_usage(usage: dict[str, Any] | None) -> dict[str, Any]:
    source = usage or {}
    input_tokens = int(source.get("prompt_tokens") or source.get("input_tokens") or 0)
    output_tokens = int(
        source.get("completion_tokens") or source.get("output_tokens") or 0
    )
    input_details = source.get("input_tokens_details")
    if not isinstance(input_details, dict):
        input_details = {}
    cached_tokens = int(
        source.get("cached_tokens")
        or source.get("cache_read_input_tokens")
        or input_details.get("cached_tokens")
        or input_details.get("cache_read_input_tokens")
        or 0
    )
    return {
        "input_tokens": input_tokens,
        "input_tokens_details": {"cached_tokens": cached_tokens},
        "output_tokens": output_tokens,
        "output_tokens_details": {
            "reasoning_tokens": int(source.get("reasoning_tokens") or 0)
        },
        "total_tokens": int(source.get("total_tokens") or input_tokens + output_tokens),
    }
