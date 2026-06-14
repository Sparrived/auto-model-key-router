from __future__ import annotations

import asyncio
import json
from typing import Any

from .metrics import extract_usage


def _adapt_message_payload(payload: dict[str, Any]) -> dict[str, Any]:
    if "messages" in payload:
        return _normalize_chat_compat_parameters(_adapt_anthropic_messages_payload(payload))
    if "input" in payload:
        return _normalize_chat_compat_parameters(_adapt_responses_input_payload(payload))
    return _normalize_chat_compat_parameters(payload)


def _estimate_anthropic_input_tokens(payload: dict[str, Any]) -> int:
    content = {key: value for key, value in payload.items() if key not in {"model", "stream", "max_tokens", "max_output_tokens"}}
    encoded = json.dumps(content, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    return max(1, (len(encoded) + 3) // 4)


def _adapt_anthropic_messages_payload(payload: dict[str, Any]) -> dict[str, Any]:
    messages = payload.get("messages")
    if not isinstance(messages, list):
        return payload
    adapted = dict(payload)
    adapted_messages: list[dict[str, Any]] = []
    for message in messages:
        if isinstance(message, dict):
            adapted_messages.extend(_adapt_anthropic_message(message))
    system = adapted.pop("system", None)
    if system:
        adapted_messages = [
            {"role": "system", "content": _adapt_content(system)},
            *adapted_messages,
        ]
    adapted["messages"] = adapted_messages
    tools = adapted.get("tools")
    if isinstance(tools, list):
        adapted["tools"] = [_adapt_anthropic_tool(tool) for tool in tools if isinstance(tool, dict)]
    tool_choice = adapted.get("tool_choice")
    if isinstance(tool_choice, dict):
        adapted["tool_choice"] = _adapt_anthropic_tool_choice(tool_choice)
        if "disable_parallel_tool_use" in tool_choice:
            adapted["parallel_tool_calls"] = not bool(tool_choice["disable_parallel_tool_use"])
    return adapted


def _adapt_responses_input_payload(payload: dict[str, Any]) -> dict[str, Any]:
    adapted = dict(payload)
    messages = _responses_input_to_messages(adapted.pop("input"))
    instructions = adapted.pop("instructions", None)
    if instructions:
        messages = [
            {"role": "system", "content": _adapt_content(instructions)},
            *messages,
        ]
    adapted["messages"] = messages
    tools = adapted.get("tools")
    if isinstance(tools, list):
        adapted["tools"] = [_adapt_anthropic_tool(tool) for tool in tools if isinstance(tool, dict)]
    tool_choice = adapted.get("tool_choice")
    if isinstance(tool_choice, dict) and tool_choice.get("type") == "function":
        adapted["tool_choice"] = {
            "type": "function",
            "function": {"name": str(tool_choice.get("name") or "")},
        }
    return adapted


def _normalize_chat_compat_parameters(payload: dict[str, Any]) -> dict[str, Any]:
    adapted = dict(payload)
    if "max_output_tokens" in adapted and "max_tokens" not in adapted:
        adapted["max_tokens"] = adapted.pop("max_output_tokens")
    else:
        adapted.pop("max_output_tokens", None)
    if "stop_sequences" in adapted and "stop" not in adapted:
        adapted["stop"] = adapted.pop("stop_sequences")
    else:
        adapted.pop("stop_sequences", None)
    for key in (
        "anthropic_version",
        "metadata",
        "reasoning",
        "text",
        "truncation",
        "previous_response_id",
        "include",
        "store",
        "prompt_cache_key",
        "safety_identifier",
    ):
        adapted.pop(key, None)
    return adapted


def _responses_input_to_messages(value: Any) -> list[dict[str, Any]]:
    if isinstance(value, str):
        return [{"role": "user", "content": value}]
    if not isinstance(value, list):
        return [{"role": "user", "content": _adapt_content(value)}]
    messages: list[dict[str, Any]] = []
    for item in value:
        if isinstance(item, dict):
            item_type = item.get("type")
            if item_type == "function_call":
                messages.append(
                    {
                        "role": "assistant",
                        "content": None,
                        "tool_calls": [
                            {
                                "id": str(item.get("call_id") or item.get("id") or "call_amkr"),
                                "type": "function",
                                "function": {
                                    "name": str(item.get("name") or ""),
                                    "arguments": item.get("arguments")
                                    if isinstance(item.get("arguments"), str)
                                    else json.dumps(
                                        item.get("arguments") or {},
                                        ensure_ascii=False,
                                        separators=(",", ":"),
                                    ),
                                },
                            }
                        ],
                    }
                )
                continue
            if item_type == "function_call_output":
                messages.append(
                    {
                        "role": "tool",
                        "tool_call_id": str(item.get("call_id") or ""),
                        "content": _tool_result_text(item.get("output", "")),
                    }
                )
                continue
            if item_type == "reasoning":
                continue
            role = str(item.get("role") or "user")
            if role == "developer":
                role = "system"
            content = item.get("content", item.get("text", ""))
            if item.get("type") == "message" or "role" in item or "content" in item:
                messages.append({"role": role, "content": _adapt_content(content)})
            else:
                messages.append({"role": "user", "content": _adapt_content(item)})
        else:
            messages.append({"role": "user", "content": _adapt_content(item)})
    return messages


def _adapt_anthropic_message(message: dict[str, Any]) -> list[dict[str, Any]]:
    role = str(message.get("role") or "user")
    content = message.get("content", "")
    if not isinstance(content, list):
        return [{**message, "role": role, "content": _adapt_content(content)}]

    if role == "assistant":
        text_parts: list[Any] = []
        tool_calls: list[dict[str, Any]] = []
        for index, part in enumerate(content):
            if isinstance(part, dict) and part.get("type") == "tool_use":
                tool_input = part.get("input")
                tool_calls.append(
                    {
                        "id": str(part.get("id") or f"toolu_amkr_{index}"),
                        "type": "function",
                        "function": {
                            "name": str(part.get("name") or ""),
                            "arguments": json.dumps(
                                tool_input if tool_input is not None else {},
                                ensure_ascii=False,
                                separators=(",", ":"),
                            ),
                        },
                    }
                )
            else:
                text_parts.append(part)
        adapted = {key: value for key, value in message.items() if key != "content"}
        adapted["role"] = role
        adapted["content"] = _adapt_content(text_parts) if text_parts else None
        if tool_calls:
            adapted["tool_calls"] = tool_calls
        return [adapted]

    if role == "user" and any(isinstance(part, dict) and part.get("type") == "tool_result" for part in content):
        adapted_messages: list[dict[str, Any]] = []
        pending_parts: list[Any] = []
        for part in content:
            if not isinstance(part, dict) or part.get("type") != "tool_result":
                pending_parts.append(part)
                continue
            if pending_parts:
                adapted_messages.append({"role": "user", "content": _adapt_content(pending_parts)})
                pending_parts = []
            adapted_messages.append(
                {
                    "role": "tool",
                    "tool_call_id": str(part.get("tool_use_id") or ""),
                    "content": _tool_result_text(part.get("content", "")),
                }
            )
        if pending_parts:
            adapted_messages.append({"role": "user", "content": _adapt_content(pending_parts)})
        return adapted_messages

    adapted = dict(message)
    adapted["role"] = role
    adapted["content"] = _adapt_content(content)
    return [adapted]


def _adapt_anthropic_tool(tool: dict[str, Any]) -> dict[str, Any]:
    if tool.get("type") == "function" and isinstance(tool.get("function"), dict):
        return tool
    function: dict[str, Any] = {"name": str(tool.get("name") or "")}
    if tool.get("description") is not None:
        function["description"] = str(tool["description"])
    parameters = tool.get("input_schema", tool.get("parameters"))
    function["parameters"] = parameters if isinstance(parameters, dict) else {"type": "object", "properties": {}}
    return {"type": "function", "function": function}


def _adapt_anthropic_tool_choice(tool_choice: dict[str, Any]) -> Any:
    choice_type = tool_choice.get("type")
    if choice_type == "any":
        return "required"
    if choice_type in {"auto", "none"}:
        return choice_type
    if choice_type == "tool":
        return {
            "type": "function",
            "function": {"name": str(tool_choice.get("name") or "")},
        }
    return tool_choice


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
                parts.append(json.dumps(part, ensure_ascii=False, separators=(",", ":")))
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
                            stored["arguments"] += arguments if isinstance(arguments, str) else json.dumps(arguments, ensure_ascii=False, separators=(",", ":"))
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
    return f"event: {event}\ndata: {json.dumps(payload, ensure_ascii=False, separators=(',', ':'))}\n\n".encode("utf-8")


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
                        events.extend(_anthropic_stream_tool_events(stream_state, tool_call, fallback_index))
            function_call = delta.get("function_call")
            if isinstance(function_call, dict):
                events.extend(_anthropic_stream_tool_events(stream_state, {"index": 0, "function": function_call}, 0))
            if choice.get("finish_reason") is not None:
                stream_state["stop_reason"] = _anthropic_stop_reason(choice.get("finish_reason"))
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
            accumulated["arguments"] += json.dumps(arguments, ensure_ascii=False, separators=(",", ":"))

    if stream_state["active_tool_index"] is None and accumulated["id"] and accumulated["name"]:
        stream_state["active_tool_index"] = tool_index
        events.extend(_start_anthropic_stream_tool(stream_state, tool_index))
    if stream_state["active_tool_index"] == tool_index:
        events.extend(_anthropic_stream_tool_argument_events(accumulated))
    return events


def _start_anthropic_stream_tool(stream_state: dict[str, Any], tool_index: int) -> list[bytes]:
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
    ordered_indexes.extend(index for index in sorted(stream_state["tool_calls"]) if index != active_tool_index)

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
    return f"event: {event}\ndata: {json.dumps(payload, ensure_ascii=False, separators=(',', ':'))}\n\n".encode("utf-8")


async def _flush_stream_event() -> None:
    # Let the ASGI server flush each SSE event instead of coalescing a burst
    # from one upstream network chunk into a single downstream TCP write.
    await asyncio.sleep(0)


def _anthropic_stream_usage(usage: dict[str, Any] | None) -> dict[str, int]:
    return {"output_tokens": int((usage or {}).get("completion_tokens") or (usage or {}).get("output_tokens") or 0)}


def _anthropic_message_response(data: Any, requested_model_id: str) -> dict[str, Any] | None:
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
        "usage": {
            "input_tokens": int(usage.get("prompt_tokens") or usage.get("input_tokens") or 0),
            "output_tokens": int(usage.get("completion_tokens") or usage.get("output_tokens") or 0),
        },
    }


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
            "usage": _responses_usage(data.get("usage") if isinstance(data.get("usage"), dict) else None),
        }
    message = choice.get("message") if isinstance(choice.get("message"), dict) else {}
    return {
        "id": str(data.get("id") or "resp_amkr"),
        "object": "response",
        "status": "completed",
        "model": requested_model_id,
        "output": _responses_message_output_items(message),
        "usage": _responses_usage(data.get("usage") if isinstance(data.get("usage"), dict) else None),
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
        tool_calls = [{"id": "call_amkr_0", "function": function_call}] if isinstance(function_call, dict) else []
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
    output_tokens = int(source.get("completion_tokens") or source.get("output_tokens") or 0)
    return {
        "input_tokens": input_tokens,
        "input_tokens_details": {"cached_tokens": int(source.get("cached_tokens") or 0)},
        "output_tokens": output_tokens,
        "output_tokens_details": {"reasoning_tokens": int(source.get("reasoning_tokens") or 0)},
        "total_tokens": int(source.get("total_tokens") or input_tokens + output_tokens),
    }


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


def _anthropic_tool_use_blocks(message: dict[str, Any]) -> list[dict[str, Any]]:
    tool_calls = message.get("tool_calls")
    if not isinstance(tool_calls, list):
        function_call = message.get("function_call")
        tool_calls = [{"id": "call_amkr_0", "function": function_call}] if isinstance(function_call, dict) else []
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
