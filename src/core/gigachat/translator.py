from __future__ import annotations

import json
import re
import time
from ast import literal_eval
from copy import deepcopy
from typing import Any, Mapping, Sequence
from uuid import uuid4

TOOL_EMULATION_SCHEMA = {
    "action": "call_tool | final_answer",
    "tool": "имя инструмента, если action равен call_tool",
    "arguments": "объект с аргументами инструмента, если action равен call_tool",
    "content": "текст ответа пользователю, если action равен final_answer",
}

TERMINAL_TOOL_NAMES = {"extract_assignments", "analyse_reports"}
TERMINAL_TOOL_RESULT_INLINE_LIMIT = 2_000


def openai_chat_to_gigachat(payload: Mapping[str, Any], *, default_model: str) -> dict[str, Any]:
    model = str(payload.get("model") or default_model)
    converted: dict[str, Any] = {
        "model": model,
        "messages": openai_messages_to_gigachat(payload.get("messages") or []),
        "profanity_check": False,
    }

    for key in ("temperature", "top_p", "max_tokens", "presence_penalty", "frequency_penalty", "stop"):
        value = payload.get(key)
        if value is not None:
            converted[key] = value

    if payload.get("stream") is True:
        converted["stream"] = True

    max_completion_tokens = payload.get("max_completion_tokens")
    if "max_tokens" not in converted and max_completion_tokens is not None:
        converted["max_tokens"] = max_completion_tokens

    tools = payload.get("tools")
    if isinstance(tools, Sequence) and not isinstance(tools, (str, bytes)):
        functions = tools_to_gigachat_functions(tools)
        if functions:
            converted["functions"] = functions
            tool_choice = payload.get("tool_choice")
            if isinstance(tool_choice, Mapping):
                function = tool_choice.get("function")
                if isinstance(function, Mapping) and isinstance(function.get("name"), str):
                    converted["function_call"] = {"name": function["name"]}
                else:
                    converted["function_call"] = "auto"
            elif tool_choice == "none":
                converted["function_call"] = "none"
            else:
                converted["function_call"] = "auto"

    return converted


def openai_chat_to_gigachat_tool_emulation(
    payload: Mapping[str, Any],
    *,
    default_model: str,
    retry_error: str | None = None,
) -> dict[str, Any]:
    model = str(payload.get("model") or default_model)
    converted: dict[str, Any] = {
        "model": model,
        "messages": _openai_messages_to_tool_emulation_gigachat(payload.get("messages") or [], payload.get("tools") or [], retry_error=retry_error),
        "profanity_check": False,
    }

    for key in ("temperature", "top_p", "max_tokens", "presence_penalty", "frequency_penalty", "stop"):
        value = payload.get(key)
        if value is not None:
            converted[key] = value

    max_completion_tokens = payload.get("max_completion_tokens")
    if "max_tokens" not in converted and max_completion_tokens is not None:
        converted["max_tokens"] = max_completion_tokens

    return converted


def parse_tool_emulation_response(content: str, tools: Any) -> dict[str, Any]:
    try:
        data = _loads_model_json(content)
    except (json.JSONDecodeError, ValueError):
        plain_text = content.strip()
        if plain_text:
            return {"action": "final_answer", "content": plain_text, "fallback": "plain_text"}
        raise
    if not isinstance(data, Mapping):
        raise ValueError("Tool emulation response must be a JSON object.")

    action = data.get("action")
    known_tools = _tool_names(tools)
    if isinstance(action, str) and action in known_tools:
        action = "call_tool"
        data = {**data, "tool": data.get("tool") or data["action"]}
    elif action is None and isinstance(data.get("tool"), str) and data["tool"] in known_tools:
        action = "call_tool"

    if action == "final_answer":
        final_content = data.get("content")
        if not isinstance(final_content, str):
            raise ValueError("Tool emulation final_answer requires string field 'content'.")
        return {"action": "final_answer", "content": final_content}

    if action == "call_tool":
        tool_name = data.get("tool") or data.get("tool_name")
        if not isinstance(tool_name, str) or not tool_name:
            raise ValueError("Tool emulation call_tool requires string field 'tool'.")
        if known_tools and tool_name not in known_tools:
            raise ValueError(f"Unknown tool {tool_name!r}. Available tools: {', '.join(sorted(known_tools))}.")
        arguments = data.get("arguments", data.get("args"))
        if arguments is None:
            arguments = {}
        if isinstance(arguments, str):
            arguments = _loads_model_json(arguments)
        if not isinstance(arguments, Mapping):
            raise ValueError("Tool emulation call_tool requires object field 'arguments'.")
        return {"action": "call_tool", "tool": tool_name, "arguments": dict(arguments)}

    raise ValueError("Tool emulation response field 'action' must be either 'call_tool' or 'final_answer'.")


def tool_emulation_result_to_openai_response(
    result: Mapping[str, Any],
    *,
    request_model: str,
    usage: Mapping[str, Any] | None = None,
) -> dict[str, Any]:
    if result.get("action") == "call_tool":
        message = {
            "role": "assistant",
            "content": None,
            "tool_calls": [
                {
                    "id": f"call_{uuid4().hex}",
                    "type": "function",
                    "function": {
                        "name": str(result["tool"]),
                        "arguments": _arguments_to_json(result.get("arguments")),
                    },
                }
            ],
        }
        finish_reason = "tool_calls"
    else:
        message = {"role": "assistant", "content": str(result.get("content") or "")}
        finish_reason = "stop"

    return {
        "id": f"chatcmpl-{uuid4().hex}",
        "object": "chat.completion",
        "created": int(time.time()),
        "model": request_model,
        "choices": [{"index": 0, "message": message, "finish_reason": finish_reason}],
        "usage": usage or {"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
    }


def openai_response_to_sse(response: Mapping[str, Any]) -> list[str]:
    choice = response["choices"][0]
    message = choice["message"]
    delta: dict[str, Any] = {}
    if message.get("tool_calls"):
        delta["tool_calls"] = [
            {
                "index": 0,
                "id": tool_call["id"],
                "type": tool_call["type"],
                "function": tool_call["function"],
            }
            for tool_call in message["tool_calls"]
        ]
    elif message.get("content"):
        delta["content"] = message["content"]

    chunk = {
        "id": response["id"],
        "object": "chat.completion.chunk",
        "created": response["created"],
        "model": response["model"],
        "choices": [{"index": choice["index"], "delta": delta, "finish_reason": choice["finish_reason"]}],
        "usage": response.get("usage", {"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0}),
    }
    return [f"data: {json.dumps(chunk, ensure_ascii=False)}\n\n", "data: [DONE]\n\n"]


def openai_messages_to_gigachat(messages: Any) -> list[dict[str, Any]]:
    if not isinstance(messages, Sequence) or isinstance(messages, (str, bytes)):
        raise ValueError("messages must be a list")

    tool_call_names: dict[str, str] = {}
    converted: list[dict[str, Any]] = []
    system_contents: list[str] = []
    for raw_message in messages:
        if not isinstance(raw_message, Mapping):
            continue

        role = str(raw_message.get("role") or "")
        content = _content_to_text(raw_message.get("content"))

        if role == "system":
            if content:
                system_contents.append(content)
            continue

        if role == "user":
            if content:
                converted.append({"role": role, "content": content})
            continue

        if role == "assistant":
            assistant_message: dict[str, Any] = {"role": "assistant", "content": content}
            tool_calls = raw_message.get("tool_calls")
            if isinstance(tool_calls, Sequence) and not isinstance(tool_calls, (str, bytes)) and tool_calls:
                first_call = tool_calls[0]
                function_call = _openai_tool_call_to_gigachat_function_call(first_call)
                if function_call is not None:
                    assistant_message["function_call"] = function_call
                    if isinstance(first_call, Mapping):
                        call_id = first_call.get("id")
                        if isinstance(call_id, str):
                            tool_call_names[call_id] = function_call["name"]
            converted.append(assistant_message)
            continue

        if role == "tool":
            tool_call_id = raw_message.get("tool_call_id")
            name = raw_message.get("name")
            if not isinstance(name, str) or not name:
                name = tool_call_names.get(str(tool_call_id), "tool_result")
            converted.append({"role": "function", "name": name, "content": _tool_return_content_to_json(raw_message.get("content"))})
            continue

        if role == "function":
            name = raw_message.get("name")
            converted.append({"role": "function", "name": str(name or "tool_result"), "content": _tool_return_content_to_json(raw_message.get("content"))})

    if system_contents:
        converted.insert(0, {"role": "system", "content": "\n\n".join(system_contents)})

    if not converted:
        raise ValueError("GigaChat request must contain at least one message")
    return converted


def _openai_messages_to_tool_emulation_gigachat(messages: Any, tools: Any, *, retry_error: str | None) -> list[dict[str, Any]]:
    if not isinstance(messages, Sequence) or isinstance(messages, (str, bytes)):
        raise ValueError("messages must be a list")

    converted: list[dict[str, Any]] = [{"role": "system", "content": _tool_emulation_instruction(tools)}]
    tool_call_names: dict[str, str] = {}
    last_message_kind: str | None = None
    last_tool_result_name: str | None = None

    for raw_message in messages:
        if not isinstance(raw_message, Mapping):
            continue

        role = str(raw_message.get("role") or "")
        content = _content_to_text(raw_message.get("content"))

        if role == "system":
            if content:
                converted[0]["content"] = f"{converted[0]['content']}\n\n{content}"
            continue

        if role == "user":
            if content:
                converted.append({"role": role, "content": content})
                last_message_kind = "user"
                last_tool_result_name = None
            continue

        if role == "assistant":
            parts = [content] if content else []
            tool_calls = raw_message.get("tool_calls")
            if isinstance(tool_calls, Sequence) and not isinstance(tool_calls, (str, bytes)):
                for tool_call in tool_calls:
                    function_call = _openai_tool_call_to_gigachat_function_call(tool_call)
                    if function_call is None:
                        continue
                    call_id = tool_call.get("id") if isinstance(tool_call, Mapping) else None
                    if isinstance(call_id, str):
                        tool_call_names[call_id] = function_call["name"]
                    if function_call["name"] not in TERMINAL_TOOL_NAMES:
                        parts.append(
                            "Предыдущий вызов инструмента: "
                            f"{json.dumps({'tool': function_call['name'], 'arguments': function_call['arguments']}, ensure_ascii=False)}"
                        )
            if parts:
                converted.append({"role": "assistant", "content": "\n".join(parts)})
                last_message_kind = "assistant"
                last_tool_result_name = None
            continue

        if role == "tool":
            tool_call_id = raw_message.get("tool_call_id")
            name = raw_message.get("name")
            if not isinstance(name, str) or not name:
                name = tool_call_names.get(str(tool_call_id), "tool_result")
            converted.append({"role": "user", "content": f"Результат инструмента {name}:\n{_tool_return_content_to_emulation_json(name, raw_message.get('content'))}"})
            last_message_kind = "tool_result"
            last_tool_result_name = name
            continue

        if role == "function":
            name = raw_message.get("name")
            tool_name = str(name or "tool_result")
            converted.append({"role": "user", "content": f"Результат инструмента {tool_name}:\n{_tool_return_content_to_emulation_json(tool_name, raw_message.get('content'))}"})
            last_message_kind = "tool_result"
            last_tool_result_name = tool_name

    if last_message_kind == "tool_result":
        _append_system_instruction(converted, _after_tool_result_instruction(last_tool_result_name))
    if retry_error:
        _append_system_instruction(converted, _retry_instruction(retry_error))

    return converted


def _tool_emulation_instruction(tools: Any) -> str:
    return (
        "Ты работаешь в режиме эмуляции вызова инструментов. Нативные function calling/tools недоступны.\n"
        "Верни только валидный JSON-объект без markdown-разметки и без поясняющего текста.\n\n"
        "Если нужно вызвать инструмент, верни:\n"
        '{"action":"call_tool","tool":"имя_инструмента","arguments":{...}}\n\n'
        "Если нужно ответить пользователю без вызова инструмента, верни:\n"
        '{"action":"final_answer","content":"текст ответа"}\n\n'
        "Схема ответа:\n"
        f"{json.dumps(TOOL_EMULATION_SCHEMA, ensure_ascii=False, indent=2)}\n\n"
        "Доступные инструменты:\n"
        f"{json.dumps(_tool_specs_for_prompt(tools), ensure_ascii=False, indent=2)}"
    )


def _append_system_instruction(messages: list[dict[str, Any]], instruction: str) -> None:
    messages[0]["content"] = f"{messages[0]['content']}\n\n{instruction}"


def _after_tool_result_instruction(tool_name: str | None) -> str:
    if tool_name in TERMINAL_TOOL_NAMES:
        return (
            "# Правило после результата инструмента\n"
            f"Последний результат получен от терминального инструмента `{tool_name}`. Этот инструмент уже выполнил основную задачу пользователя.\n"
            "Теперь верни только JSON с action=\"final_answer\" и кратким сообщением пользователю по результату инструмента.\n"
            "Не вызывай инструменты повторно, если результат инструмента явно не просит выполнить еще один инструмент.\n"
            "Не пересказывай полный отчет, если инструмент сообщил, что результат сохранен в файл.\n"
            "Не упоминай JSON, схему, внутреннюю валидацию, retry, function calling или технические ошибки."
        )
    return (
        "# Правило после результата инструмента\n"
        "Последнее сообщение содержит результат инструмента. Используй этот результат для следующего шага.\n"
        "Если данных достаточно для ответа пользователю, верни JSON с action=\"final_answer\".\n"
        "Вызывай новый инструмент только если без него невозможно выполнить просьбу пользователя.\n"
        "Не упоминай JSON, схему, внутреннюю валидацию, retry, function calling или технические ошибки."
    )


def _retry_instruction(retry_error: str) -> str:
    return (
        "# Внутреннее исправление формата\n"
        "Предыдущий ответ не прошел внутреннюю проверку формата. Это служебная информация только для тебя.\n"
        "Нельзя сообщать пользователю, что ответ не удалось разобрать, что JSON невалиден, что есть ошибка схемы или что выполняется retry.\n"
        f"Техническая причина: {retry_error}.\n"
        "Исправь только формат следующего ответа: верни один валидный JSON-объект по описанной схеме, без markdown и без пояснений."
    )


def _tool_return_content_to_emulation_json(tool_name: str, content: Any) -> str:
    raw_json = _tool_return_content_to_json(content)
    if tool_name not in TERMINAL_TOOL_NAMES or len(raw_json) <= TERMINAL_TOOL_RESULT_INLINE_LIMIT:
        return raw_json

    text = _tool_result_content_to_text(content)
    result_file = _extract_saved_result_path(text)
    compact_result: dict[str, Any] = {
        "result": _terminal_tool_result_summary(text),
        "full_result_omitted_from_tool_routing_context": True,
        "full_result_chars": len(text),
        "note": (
            "Полный текст результата не передан в служебный контекст выбора инструмента, чтобы не перегружать модель. "
            "Если пользователю нужно обсудить содержание результата, вызови read для result_file."
        ),
    }
    if result_file:
        compact_result["result_file"] = result_file
    return json.dumps(compact_result, ensure_ascii=False)


def _tool_result_content_to_text(content: Any) -> str:
    if isinstance(content, str):
        return content
    if isinstance(content, Mapping):
        result = content.get("result")
        if isinstance(result, str):
            return result
        return json.dumps(content, ensure_ascii=False, default=str)
    return str(content)


def _extract_saved_result_path(text: str) -> str | None:
    marker = "Результат сохранен в файл "
    start = text.find(marker)
    if start == -1:
        return None
    start += len(marker)
    end = text.find("\n", start)
    if end == -1:
        end = len(text)
    path = text[start:end].strip()
    return path or None


def _terminal_tool_result_summary(text: str) -> str:
    marker = "Содержание файла:"
    marker_index = text.find(marker)
    summary = text[:marker_index].strip() if marker_index != -1 else text.strip()
    if not summary:
        summary = "Результат терминального инструмента сформирован, полный текст опущен из служебного контекста выбора инструмента."
    if len(summary) > 1_000:
        summary = f"{summary[:1_000].rstrip()}..."
    return summary


def _tool_specs_for_prompt(tools: Any) -> list[dict[str, Any]]:
    if not isinstance(tools, Sequence) or isinstance(tools, (str, bytes)):
        return []
    specs: list[dict[str, Any]] = []
    for tool in tools:
        if not isinstance(tool, Mapping) or tool.get("type", "function") != "function":
            continue
        function_spec = tool.get("function")
        if not isinstance(function_spec, Mapping):
            continue
        name = function_spec.get("name")
        if not isinstance(name, str) or not name:
            continue
        parameters = function_spec.get("parameters")
        if not isinstance(parameters, Mapping):
            parameters = {"type": "object", "properties": {}}
        specs.append(
            {
                "name": name,
                "description": function_spec.get("description") or "",
                "parameters": prepare_gigachat_json_schema(dict(parameters)),
            }
        )
    return specs


def _tool_names(tools: Any) -> set[str]:
    return {spec["name"] for spec in _tool_specs_for_prompt(tools)}


def tools_to_gigachat_functions(tools: Sequence[Any]) -> list[dict[str, Any]]:
    functions: list[dict[str, Any]] = []
    for tool in tools:
        if not isinstance(tool, Mapping):
            continue
        if tool.get("type", "function") != "function":
            continue
        function_spec = tool.get("function")
        if not isinstance(function_spec, Mapping):
            continue
        name = function_spec.get("name")
        if not isinstance(name, str) or not name:
            continue
        parameters = function_spec.get("parameters")
        if not isinstance(parameters, Mapping):
            parameters = {"type": "object", "properties": {}}
        function: dict[str, Any] = {
            "name": name,
            "parameters": prepare_gigachat_json_schema(dict(parameters)),
        }
        description = function_spec.get("description")
        if isinstance(description, str) and description:
            function["description"] = description
        functions.append(function)
    return functions


def prepare_gigachat_json_schema(schema: dict[str, Any]) -> dict[str, Any]:
    return _normalize_gigachat_json_schema(_inline_json_schema_refs(schema))


def gigachat_response_to_openai(data: Mapping[str, Any], *, request_model: str) -> dict[str, Any]:
    choices = data.get("choices") if isinstance(data.get("choices"), list) else []
    return {
        "id": data.get("id") or f"chatcmpl-{uuid4().hex}",
        "object": "chat.completion",
        "created": int(time.time()),
        "model": data.get("model") or request_model,
        "choices": [_gigachat_choice_to_openai(choice, index) for index, choice in enumerate(choices)],
        "usage": data.get("usage") or {"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
    }


def gigachat_chunk_to_openai(data: Mapping[str, Any], *, request_model: str) -> dict[str, Any]:
    choices = data.get("choices") if isinstance(data.get("choices"), list) else []
    return {
        "id": data.get("id") or f"chatcmpl-{uuid4().hex}",
        "object": "chat.completion.chunk",
        "created": int(time.time()),
        "model": data.get("model") or request_model,
        "choices": [_gigachat_stream_choice_to_openai(choice, index) for index, choice in enumerate(choices)],
        **({"usage": data["usage"]} if "usage" in data else {}),
    }


def _gigachat_choice_to_openai(choice: Any, index: int) -> dict[str, Any]:
    if not isinstance(choice, Mapping):
        choice = {}
    message = choice.get("message") if isinstance(choice.get("message"), Mapping) else {}
    content = message.get("content")
    function_call = message.get("function_call")
    openai_message: dict[str, Any] = {"role": "assistant", "content": content or ""}
    finish_reason = _finish_reason(choice.get("finish_reason"))
    if isinstance(function_call, Mapping):
        openai_message["content"] = content or None
        openai_message["tool_calls"] = [_gigachat_function_call_to_openai_tool_call(function_call)]
        finish_reason = "tool_calls"
    return {
        "index": index,
        "message": openai_message,
        "finish_reason": finish_reason,
    }


def _gigachat_stream_choice_to_openai(choice: Any, index: int) -> dict[str, Any]:
    if not isinstance(choice, Mapping):
        choice = {}
    delta = choice.get("delta") or choice.get("message") or {}
    if not isinstance(delta, Mapping):
        delta = {}
    openai_delta: dict[str, Any] = {}
    if content := delta.get("content"):
        openai_delta["content"] = content
    function_call = delta.get("function_call")
    finish_reason = _finish_reason(choice.get("finish_reason"))
    if isinstance(function_call, Mapping):
        openai_delta["tool_calls"] = [_gigachat_function_delta_to_openai_tool_call_delta(function_call)]
        finish_reason = "tool_calls" if finish_reason in {None, "stop"} else finish_reason
    return {
        "index": choice.get("index", index),
        "delta": openai_delta,
        "finish_reason": finish_reason,
    }


def _openai_tool_call_to_gigachat_function_call(tool_call: Any) -> dict[str, Any] | None:
    if not isinstance(tool_call, Mapping):
        return None
    function = tool_call.get("function")
    if not isinstance(function, Mapping):
        return None
    name = function.get("name")
    if not isinstance(name, str) or not name:
        return None
    arguments = function.get("arguments") or {}
    if isinstance(arguments, str):
        try:
            arguments = json.loads(arguments)
        except json.JSONDecodeError:
            arguments = {"arguments": arguments}
    return {"name": name, "arguments": arguments}


def _gigachat_function_call_to_openai_tool_call(function_call: Mapping[str, Any]) -> dict[str, Any]:
    name = str(function_call.get("name") or "")
    return {
        "id": f"call_{uuid4().hex}",
        "type": "function",
        "function": {
            "name": name,
            "arguments": _arguments_to_json(function_call.get("arguments")),
        },
    }


def _gigachat_function_delta_to_openai_tool_call_delta(function_call: Mapping[str, Any]) -> dict[str, Any]:
    delta: dict[str, Any] = {
        "index": 0,
        "type": "function",
        "function": {},
    }
    if name := function_call.get("name"):
        delta["id"] = f"call_{uuid4().hex}"
        delta["function"]["name"] = str(name)
    if "arguments" in function_call:
        delta["function"]["arguments"] = _arguments_to_json(function_call.get("arguments"))
    return delta


def _inline_json_schema_refs(schema: dict[str, Any]) -> dict[str, Any]:
    schema = deepcopy(schema)
    defs = schema.get("$defs") or schema.get("definitions") or {}

    def resolve(node: Any) -> Any:
        if isinstance(node, list):
            return [resolve(item) for item in node]
        if not isinstance(node, dict):
            return node

        ref = node.get("$ref")
        if isinstance(ref, str) and ref.startswith("#/$defs/"):
            ref_name = ref.removeprefix("#/$defs/")
            target = deepcopy(defs.get(ref_name, {}))
            target.update({key: value for key, value in node.items() if key != "$ref"})
            return resolve(target)
        if isinstance(ref, str) and ref.startswith("#/definitions/"):
            ref_name = ref.removeprefix("#/definitions/")
            target = deepcopy(defs.get(ref_name, {}))
            target.update({key: value for key, value in node.items() if key != "$ref"})
            return resolve(target)

        return {key: resolve(value) for key, value in node.items() if key not in {"$defs", "definitions"}}

    resolved = resolve(schema)
    return resolved if isinstance(resolved, dict) else schema


def _normalize_gigachat_json_schema(schema: dict[str, Any]) -> dict[str, Any]:
    def normalize(node: Any) -> Any:
        if isinstance(node, list):
            return [normalize(item) for item in node]
        if not isinstance(node, dict):
            return node

        normalized = {key: normalize(value) for key, value in node.items()}
        for union_key in ("anyOf", "oneOf"):
            variants = normalized.get(union_key)
            if not isinstance(variants, list):
                continue
            non_null_variants = [variant for variant in variants if not (isinstance(variant, dict) and variant.get("type") == "null")]
            if len(non_null_variants) == 1 and len(non_null_variants) != len(variants):
                merged = dict(non_null_variants[0])
                for key, value in normalized.items():
                    if key == union_key or (key == "default" and value is None):
                        continue
                    merged[key] = value
                normalized = merged
                break

        schema_type = normalized.get("type")
        if isinstance(schema_type, list):
            non_null_types = [item for item in schema_type if item != "null"]
            if len(non_null_types) == 1:
                normalized["type"] = non_null_types[0]
                if normalized.get("default") is None:
                    normalized.pop("default", None)
        return normalized

    normalized_schema = normalize(schema)
    return normalized_schema if isinstance(normalized_schema, dict) else schema


def _content_to_text(content: Any) -> str:
    if content is None:
        return ""
    if isinstance(content, str):
        return content
    if isinstance(content, Sequence) and not isinstance(content, (str, bytes)):
        parts: list[str] = []
        for item in content:
            if isinstance(item, Mapping):
                if item.get("type") == "text":
                    text = item.get("text")
                    if isinstance(text, str):
                        parts.append(text)
                elif isinstance(item.get("text"), str):
                    parts.append(str(item["text"]))
            elif isinstance(item, str):
                parts.append(item)
        return "\n".join(parts)
    return str(content)


def _tool_return_content_to_json(content: Any) -> str:
    if isinstance(content, str):
        try:
            parsed = json.loads(content)
        except json.JSONDecodeError:
            parsed = content
        content = parsed if isinstance(parsed, Mapping) else {"result": parsed}
    elif not isinstance(content, Mapping):
        content = {"result": content}

    try:
        return json.dumps(content, ensure_ascii=False)
    except TypeError:
        return json.dumps({"result": str(content)}, ensure_ascii=False)


def _arguments_to_json(arguments: Any) -> str:
    if arguments is None:
        return "{}"
    if isinstance(arguments, str):
        stripped = arguments.strip()
        if not stripped:
            return "{}"
        try:
            parsed = _loads_model_json(stripped)
        except (json.JSONDecodeError, SyntaxError, ValueError):
            return json.dumps({"arguments": stripped}, ensure_ascii=False)
        if isinstance(parsed, Mapping):
            return json.dumps(parsed, ensure_ascii=False)
        return json.dumps({"arguments": parsed}, ensure_ascii=False)
    try:
        return json.dumps(arguments, ensure_ascii=False)
    except TypeError:
        return json.dumps(str(arguments), ensure_ascii=False)


def _loads_model_json(text: str) -> Any:
    text = text.strip()
    if text.startswith("```"):
        text = text.removeprefix("```json").removeprefix("```").strip()
        if text.endswith("```"):
            text = text[:-3].strip()

    start = text.find("{")
    end = text.rfind("}")
    if start != -1 and end != -1 and end > start:
        text = text[start : end + 1]
    return _loads_lenient_json(text)


def _loads_lenient_json(text: str) -> Any:
    last_error: Exception | None = None
    candidates = [text, text.replace("\\'", "'")]
    for candidate in candidates:
        repaired = re.sub(r",\s*([}\]])", r"\1", candidate)
        for variant in (candidate, repaired):
            try:
                return json.loads(variant)
            except json.JSONDecodeError as exc:
                last_error = exc
            try:
                return literal_eval(variant)
            except (SyntaxError, ValueError) as exc:
                last_error = exc

    if isinstance(last_error, json.JSONDecodeError):
        raise last_error
    raise ValueError(str(last_error) if last_error is not None else "Invalid JSON")


def _finish_reason(reason: Any) -> str | None:
    if reason in {None, ""}:
        return None
    if reason in {"tool_call", "function_call", "tool_calls"}:
        return "tool_calls"
    return str(reason)
