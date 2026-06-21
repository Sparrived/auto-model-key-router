from __future__ import annotations

import json
from typing import Any

from .common import _adapt_content, _tool_result_text


def _adapt_message_payload(payload: dict[str, Any]) -> dict[str, Any]:
    if "messages" in payload:
        return _normalize_chat_compat_parameters(
            _adapt_anthropic_messages_payload(payload)
        )
    if "input" in payload:
        return _normalize_chat_compat_parameters(
            _adapt_responses_input_payload(payload)
        )
    return _normalize_chat_compat_parameters(payload)


def _estimate_anthropic_input_tokens(payload: dict[str, Any]) -> int:
    content = {
        key: value
        for key, value in payload.items()
        if key not in {"model", "stream", "max_tokens", "max_output_tokens"}
    }
    encoded = json.dumps(content, ensure_ascii=False, separators=(",", ":")).encode(
        "utf-8"
    )
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
        adapted["tools"] = [
            _adapt_anthropic_tool(tool) for tool in tools if isinstance(tool, dict)
        ]
    tool_choice = adapted.get("tool_choice")
    if isinstance(tool_choice, dict):
        adapted["tool_choice"] = _adapt_anthropic_tool_choice(tool_choice)
        if "disable_parallel_tool_use" in tool_choice:
            adapted["parallel_tool_calls"] = not bool(
                tool_choice["disable_parallel_tool_use"]
            )
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
        adapted["tools"] = [
            _adapt_anthropic_tool(tool) for tool in tools if isinstance(tool, dict)
        ]
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
                                "id": str(
                                    item.get("call_id") or item.get("id") or "call_amkr"
                                ),
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

    if role == "user" and any(
        isinstance(part, dict) and part.get("type") == "tool_result" for part in content
    ):
        adapted_messages: list[dict[str, Any]] = []
        pending_parts: list[Any] = []
        for part in content:
            if not isinstance(part, dict) or part.get("type") != "tool_result":
                pending_parts.append(part)
                continue
            if pending_parts:
                adapted_messages.append(
                    {"role": "user", "content": _adapt_content(pending_parts)}
                )
                pending_parts = []
            adapted_messages.append(
                {
                    "role": "tool",
                    "tool_call_id": str(part.get("tool_use_id") or ""),
                    "content": _tool_result_text(part.get("content", "")),
                }
            )
        if pending_parts:
            adapted_messages.append(
                {"role": "user", "content": _adapt_content(pending_parts)}
            )
        return adapted_messages

    adapted = dict(message)
    adapted["role"] = role
    adapted["content"] = _adapt_content(content)
    return [adapted]


def _adapt_anthropic_tool(tool: dict[str, Any]) -> dict[str, Any]:
    """将工具定义转换为 OpenAI function calling 格式。

    已经是正确格式的工具直接返回，Anthropic 格式的工具会被转换。
    """
    tool_type = tool.get("type")
    # 已经是 OpenAI function 格式且有 name，直接返回
    if tool_type == "function" and isinstance(tool.get("function"), dict):
        func = tool["function"]
        if func.get("name"):
            return tool
        # function 字典存在但缺少 name，补充 name
        name = str(tool.get("name") or "")
        adapted_func = dict(func)
        adapted_func["name"] = name
        return {"type": "function", "function": adapted_func}
    # 非 function 类型的工具（如 code_interpreter, file_search 等）原样返回
    if tool_type is not None and tool_type != "function":
        return tool
    # Anthropic 格式或顶层有 name 的工具
    function: dict[str, Any] = {"name": str(tool.get("name") or "")}
    if tool.get("description") is not None:
        function["description"] = str(tool["description"])
    parameters = tool.get("input_schema", tool.get("parameters"))
    function["parameters"] = (
        parameters
        if isinstance(parameters, dict)
        else {"type": "object", "properties": {}}
    )
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
