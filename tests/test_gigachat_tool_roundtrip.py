from __future__ import annotations

import asyncio
import json
from copy import deepcopy

import httpx
import pytest

from src.config_store import CONFIG_STORE
from src.core.gigachat.client import GigaChatHTTPClient, UpstreamError
from src.core.gigachat.config import build_gigachat_config
from src.core.gigachat.tool_schema import ToolArguments
from src.core.gigachat.tool_stream import ToolCallStream
from src.core.gigachat.translator import openai_chat_to_gigachat
from src.core.sse import SSEJSONDecoder
from src.core.upstream_transport import register_upstream_transport
from src.testing import ProxyHarness

SCHEMA = {
    "type": "object",
    "properties": {
        "query": {"type": "string"},
        "period": {"type": "string", "enum": ["Day", "Year", None]},
        "limit": {"anyOf": [{"type": "integer"}, {"type": "null"}]},
        "optional": {"type": ["string", "null"]},
    },
    "required": ["query", "period", "limit"],
}
SPEC = {"name": "search", "description": "Search materials", "parameters": SCHEMA}


def request_payload(api: str, stream: bool) -> tuple[str, dict]:
    base = {"model": "giga", "stream": stream}
    if api == "responses":
        return "/v1/responses", {
            **base,
            "input": "Search",
            "store": False,
            "tools": [{"type": "function", **deepcopy(SPEC)}],
            "tool_choice": {"type": "function", "name": "search"},
        }
    if api == "messages":
        return "/v1/messages", {
            **base,
            "messages": [{"role": "user", "content": "Search"}],
            "max_tokens": 100,
            "tools": [
                {
                    "name": "search",
                    "description": "Search materials",
                    "input_schema": deepcopy(SCHEMA),
                }
            ],
            "tool_choice": {"type": "tool", "name": "search"},
        }
    base["messages"] = [{"role": "user", "content": "Search"}]
    if api == "legacy":
        base.update(functions=[deepcopy(SPEC)], function_call={"name": "search"})
    else:
        base.update(
            tools=[{"type": "function", "function": deepcopy(SPEC)}],
            tool_choice={"type": "function", "function": {"name": "search"}},
        )
    return "/v1/chat/completions", base


def response_arguments(api: str, response: httpx.Response, stream: bool) -> dict:
    if not stream:
        body = response.json()
        if api == "responses":
            return json.loads(
                next(
                    item for item in body["output"] if item["type"] == "function_call"
                )["arguments"]
            )
        if api == "messages":
            return next(item for item in body["content"] if item["type"] == "tool_use")[
                "input"
            ]
        message = body["choices"][0]["message"]
        call = (
            message["function_call"]
            if api == "legacy"
            else message["tool_calls"][0]["function"]
        )
        return json.loads(call["arguments"])
    events = SSEJSONDecoder().feed(response.content)
    if api == "responses":
        completed = [
            item["item"]
            for item in events
            if item.get("type") == "response.output_item.done"
            and item.get("item", {}).get("type") == "function_call"
        ]
        assert completed, response.text
        return json.loads(completed[0]["arguments"])
    if api == "messages":
        parts = [
            item["delta"]["partial_json"]
            for item in events
            if item.get("type") == "content_block_delta"
            and item.get("delta", {}).get("type") == "input_json_delta"
        ]
    else:
        parts = []
        for item in events:
            for choice in item.get("choices", []):
                delta = choice.get("delta", {})
                calls = (
                    [delta["function_call"]]
                    if "function_call" in delta
                    else [call["function"] for call in delta.get("tool_calls", [])]
                )
                parts.extend(call.get("arguments", "") for call in calls)
    return json.loads("".join(parts))


@pytest.fixture
def isolated_config(monkeypatch):
    monkeypatch.setattr(
        CONFIG_STORE,
        "get_runtime_config",
        lambda: {
            "app_keys": {"enabled": False},
            "proxy_settings": {"logging": {"enabled": False}},
        },
    )


@pytest.mark.parametrize("api", ["chat", "legacy", "responses", "messages"])
@pytest.mark.parametrize("stream", [False, True])
@pytest.mark.parametrize("supplied", [False, True])
@pytest.mark.parametrize("emulated", [False, True])
async def test_client_routes_restore_original_contract(
    api, stream, supplied, emulated, clear_transport_registry, isolated_config
):
    expected = {
        "query": "hi",
        "period": "Day" if supplied else None,
        "limit": 0 if supplied else None,
    }
    arguments = {"query": "hi", **({"period": "Day", "limit": 0} if supplied else {})}

    async def handler(request):
        if request.url.path == "/oauth":
            return httpx.Response(
                200, json={"access_token": "mock-token", "expires_at": 4102444800000}
            )
        payload = json.loads(request.content)
        if emulated:
            assert "functions" not in payload
            specs = json.loads(
                payload["messages"][0]["content"].split("Доступные инструменты:\n")[1]
            )
            schema = specs[0]["parameters"]
        else:
            schema = payload["functions"][0]["parameters"]
        assert schema["required"] == ["query"]
        assert schema["properties"]["period"]["enum"] == ["Day", "Year"]
        if emulated:
            return httpx.Response(
                200,
                json={
                    "choices": [
                        {
                            "message": {
                                "content": json.dumps(
                                    {
                                        "action": "call_tool",
                                        "tool": "search",
                                        "arguments": arguments,
                                    }
                                )
                            },
                            "finish_reason": "stop",
                        }
                    ],
                    "usage": {
                        "prompt_tokens": 10,
                        "completion_tokens": 5,
                        "total_tokens": 15,
                    },
                },
            )
        if not stream:
            return httpx.Response(
                200,
                json={
                    "choices": [
                        {
                            "message": {
                                "function_call": {
                                    "name": "search",
                                    "arguments": arguments,
                                }
                            },
                            "finish_reason": "tool_call",
                        }
                    ]
                },
            )
        raw = json.dumps(arguments)
        chunks = [
            {
                "choices": [
                    {
                        "index": 0,
                        "delta": {"content": "Searching"},
                        "finish_reason": None,
                    }
                ]
            },
            {
                "choices": [
                    {
                        "index": 0,
                        "delta": {"function_call": {"name": "search"}},
                        "finish_reason": None,
                    }
                ]
            },
            *[
                {
                    "choices": [
                        {
                            "index": 0,
                            "delta": {"function_call": {"arguments": part}},
                            "finish_reason": None,
                        }
                    ]
                }
                for part in (raw[:5], raw[5:])
            ],
            {"choices": [{"index": 0, "delta": {}, "finish_reason": "tool_call"}]},
            {
                "choices": [],
                "usage": {
                    "prompt_tokens": 10,
                    "completion_tokens": 5,
                    "total_tokens": 15,
                },
            },
        ]
        wire = (
            "".join("data: " + json.dumps(chunk) + "\n\n" for chunk in chunks)
            + "data: [DONE]\n\n"
        )
        return httpx.Response(
            200, headers={"content-type": "text/event-stream"}, content=wire
        )

    transport = httpx.MockTransport(handler)
    register_upstream_transport("giga.test", transport)
    config = {
        "model_list": [
            {
                "model_name": "giga",
                "model_params": {
                    "api_type": "gigachat",
                    "model": "GigaChat",
                    "api_key": "mock",
                    "api_base": "https://giga.test/api/v1",
                    "auth_url": "https://giga.test/oauth",
                    "emulate_tool_calls": emulated,
                },
            }
        ]
    }
    path, payload = request_payload(api, stream)
    before = deepcopy(payload)
    with ProxyHarness(
        config, enable_responses_endpoint=True, enable_messages_endpoint=True
    ) as proxy:
        async with proxy.make_async_client() as client:
            response = await client.post(path, json=payload)
    assert response.status_code == 200, response.text
    assert response_arguments(api, response, stream) == expected
    assert payload == before
    if stream and api in {"chat", "legacy"}:
        events = SSEJSONDecoder().feed(response.content)
        assert response.text.count("[DONE]") == 1
        assert events[-1]["usage"]["total_tokens"] == 15
        assert (
            sum(
                choice.get("finish_reason") is not None
                for event in events
                for choice in event.get("choices", [])
            )
            == 1
        )


@pytest.mark.parametrize("ending", ["finish", "done", "eof"])
@pytest.mark.parametrize("argument_form", ["dict", "fragments", "missing"])
def test_stream_flushes_once_with_correct_nulls(ending, argument_form):
    _, args = ToolArguments.prepare({"tools": [{"function": SPEC}]})
    stream = ToolCallStream(args, "giga")
    call = {"name": "search"}
    if argument_form == "dict":
        call["arguments"] = {"query": "hi"}
    output = stream.feed(
        {
            "choices": [
                {"index": 0, "delta": {"function_call": call}, "finish_reason": None}
            ]
        }
    )
    assert not output
    if argument_form == "fragments":
        for part in ['{"query":', '"hi"}']:
            assert not stream.feed(
                {
                    "choices": [
                        {
                            "index": 0,
                            "delta": {"function_call": {"arguments": part}},
                            "finish_reason": None,
                        }
                    ]
                }
            )
    if ending == "finish":
        output += stream.feed(
            {"choices": [{"index": 0, "delta": {}, "finish_reason": "tool_call"}]}
        )
    output += stream.finish()
    assert len(output) == 1
    call = output[0]["choices"][0]["delta"]["tool_calls"][0]
    expected = {"period": None, "limit": None}
    if argument_form != "missing":
        expected["query"] = "hi"
    assert json.loads(call["function"]["arguments"]) == expected
    assert stream.finish() == []


async def test_parallel_calls_on_same_client_do_not_share_schema_state():
    async def handler(request):
        await asyncio.sleep(0)
        return httpx.Response(
            200,
            json={
                "choices": [
                    {
                        "message": {"function_call": {"name": "same", "arguments": {}}},
                        "finish_reason": "tool_call",
                    }
                ]
            },
        )

    cfg = build_gigachat_config(
        {"mode": "local", "client_cert": "mock", "client_key": "mock"}
    )
    async with httpx.AsyncClient(
        base_url="https://giga.test", transport=httpx.MockTransport(handler)
    ) as http:
        client = GigaChatHTTPClient(cfg, http_client=http)

        async def run(name):
            payload = {
                "messages": [{"role": "user", "content": "test"}],
                "tools": [
                    {
                        "function": {
                            "name": "same",
                            "parameters": {
                                "type": "object",
                                "properties": {name: {"type": ["string", "null"]}},
                                "required": [name],
                            },
                        }
                    }
                ],
            }
            result = await client.chat_completions(payload)
            return json.loads(
                result["choices"][0]["message"]["tool_calls"][0]["function"][
                    "arguments"
                ]
            )

        results = await asyncio.gather(run("first"), run("second"))
    assert results == [{"first": None}, {"second": None}]


@pytest.mark.parametrize("api", ["chat", "legacy"])
def test_echoed_tool_history_uses_upstream_optional_form(api):
    _, payload = request_payload(api, False)
    function = {"name": "search", "arguments": '{"query":"hi","period":null,"limit":0}'}
    if api == "legacy":
        assistant = {"role": "assistant", "function_call": function}
        result = {"role": "function", "name": "search", "content": "[]"}
    else:
        assistant = {
            "role": "assistant",
            "tool_calls": [{"id": "call_1", "type": "function", "function": function}],
        }
        result = {"role": "tool", "tool_call_id": "call_1", "content": "[]"}
    payload["messages"] += [assistant, result, {"role": "user", "content": "Continue"}]
    before = deepcopy(payload)
    prepared, _ = ToolArguments.prepare(payload)
    upstream = openai_chat_to_gigachat(prepared, default_model="giga")
    assert upstream["messages"][1]["function_call"] == {
        "name": "search",
        "arguments": {"query": "hi", "limit": 0},
    }
    assert upstream["messages"][2]["name"] == "search"
    assert payload == before


def test_interleaved_choices_buffer_separately_and_preserve_usage():
    _, arguments = ToolArguments.prepare({"tools": [{"function": SPEC}]})
    stream = ToolCallStream(arguments, "giga")
    assert (
        stream.feed(
            {
                "choices": [
                    {
                        "index": index,
                        "delta": {
                            "function_call": {
                                "name": "search",
                                "arguments": '{"query":',
                            }
                        },
                    }
                    for index in (0, 1)
                ]
            }
        )
        == []
    )
    output = stream.feed(
        {
            "choices": [
                {
                    "index": 1,
                    "delta": {
                        "content": "Ready",
                        "function_call": {"arguments": '"second"}'},
                    },
                    "finish_reason": "tool_call",
                }
            ],
            "usage": {"total_tokens": 10},
        }
    )
    assert output[0]["choices"][0]["delta"] == {"content": "Ready"}
    assert sum("usage" in event for event in output) == 1
    output += stream.feed(
        {
            "choices": [
                {
                    "index": 0,
                    "delta": {"function_call": {"arguments": '"first"}'}},
                    "finish_reason": "tool_call",
                }
            ]
        }
    )
    calls = [
        choice
        for event in output
        for choice in event["choices"]
        if "tool_calls" in choice["delta"]
    ]
    assert [choice["index"] for choice in calls] == [1, 0]
    assert [
        json.loads(choice["delta"]["tool_calls"][0]["function"]["arguments"])
        for choice in calls
    ] == [
        {"query": "second", "period": None, "limit": None},
        {"query": "first", "period": None, "limit": None},
    ]
    assert stream.finish() == []


@pytest.mark.parametrize(
    "tools",
    [
        [],
        [
            {
                "function": {
                    "name": "plain",
                    "parameters": {
                        "type": "object",
                        "properties": {"query": {"type": "string"}},
                    },
                }
            }
        ],
    ],
)
def test_unaffected_streams_are_not_buffered(tools):
    _, arguments = ToolArguments.prepare({"tools": tools})
    stream = ToolCallStream(arguments, "giga")
    data = {
        "id": "stable",
        "choices": [{"index": 0, "delta": {"content": "Hello"}, "finish_reason": None}],
    }
    result = stream.feed(data)
    assert result[0]["choices"] == data["choices"]
    assert stream.finish() == []


@pytest.mark.parametrize("streaming", [False, True])
async def test_unsafe_schema_returns_explicit_client_error_without_upstream_request(
    streaming,
):
    cfg = build_gigachat_config(
        {"mode": "local", "client_cert": "mock", "client_key": "mock"}
    )

    async def handler(request):
        pytest.fail("Invalid schema must fail before sending any upstream request")

    async with httpx.AsyncClient(
        base_url="https://giga.test", transport=httpx.MockTransport(handler)
    ) as http:
        client = GigaChatHTTPClient(cfg, http_client=http)
        payload = {
            "tools": [
                {"function": {"name": "bad", "parameters": {"$ref": "#/missing"}}}
            ]
        }
        with pytest.raises(UpstreamError) as error:
            if streaming:
                _ = [chunk async for chunk in client.stream_chat_completions(payload)]
            else:
                await client.chat_completions(payload)
        assert error.value.status_code == 400


@pytest.mark.parametrize("failure", [httpx.ReadError, asyncio.CancelledError])
async def test_stream_failure_does_not_emit_synthetic_completed_tool_call(failure):
    class FailingBody(httpx.AsyncByteStream):
        async def __aiter__(self):
            yield b'data: {"choices":[{"index":0,"delta":{"content":"Starting"}}]}\n\n'
            yield b'data: {"choices":[{"index":0,"delta":{"function_call":{"name":"search"}}}]}\n\n'
            raise failure("interrupted")

    async def handler(request):
        return httpx.Response(200, stream=FailingBody())

    cfg = build_gigachat_config(
        {"mode": "local", "client_cert": "mock", "client_key": "mock"}
    )
    async with httpx.AsyncClient(
        base_url="https://giga.test", transport=httpx.MockTransport(handler)
    ) as http:
        client = GigaChatHTTPClient(cfg, http_client=http)
        _, payload = request_payload("chat", True)
        emitted = []
        with pytest.raises(failure):
            async for chunk in client.stream_chat_completions(payload):
                emitted.append(chunk)
        assert len(emitted) == 1
        assert "Starting" in emitted[0]
        assert "tool_calls" not in emitted[0]


@pytest.mark.parametrize("streaming", [False, True])
async def test_native_arguments_preserve_false_zero_and_empty_values(streaming):
    expected = {"flag": False, "count": 0, "note": "", "values": [], "settings": {}}
    schema = {
        "type": "object",
        "properties": {
            name: {"type": [kind, "null"]}
            for name, kind in zip(
                expected, ["boolean", "integer", "string", "array", "object"]
            )
        },
        "required": list(expected),
    }

    async def handler(request):
        assert (
            json.loads(request.content)["functions"][0]["parameters"]["required"] == []
        )
        call = {"name": "inspect", "arguments": expected}
        body = {
            "choices": [
                {
                    "delta" if streaming else "message": {"function_call": call},
                    "finish_reason": "tool_call",
                }
            ]
        }
        if streaming:
            return httpx.Response(
                200, content="data: " + json.dumps(body) + "\n\ndata: [DONE]\n\n"
            )
        return httpx.Response(200, json=body)

    cfg = build_gigachat_config(
        {"mode": "local", "client_cert": "mock", "client_key": "mock"}
    )
    async with httpx.AsyncClient(
        base_url="https://giga.test", transport=httpx.MockTransport(handler)
    ) as http:
        client = GigaChatHTTPClient(cfg, http_client=http)
        payload = {
            "messages": [{"role": "user", "content": "Inspect"}],
            "tools": [{"function": {"name": "inspect", "parameters": schema}}],
        }
        if streaming:
            chunks = [chunk async for chunk in client.stream_chat_completions(payload)]
            response = httpx.Response(200, content="".join(chunks))
        else:
            response = httpx.Response(200, json=await client.chat_completions(payload))
        assert response_arguments("chat", response, streaming) == expected


@pytest.mark.parametrize("streaming", [False, True])
@pytest.mark.parametrize("nullable", [False, True])
async def test_categories_type_list_roundtrip_includes_history(streaming, nullable):
    expected = {"categories": None if nullable else ["news"]}
    schema = {
        "type": ["object"],
        "properties": {
            "categories": {
                "type": ["array", "null"] if nullable else ["array"],
                "items": {"type": ["string"]},
            },
        },
        "required": ["categories"],
    }
    upstream_args = {} if nullable else expected

    async def handler(request):
        payload = json.loads(request.content)
        assert payload["functions"][0]["parameters"] == {
            "type": "object",
            "properties": {
                "categories": {"type": "array", "items": {"type": "string"}}
            },
            "required": [] if nullable else ["categories"],
        }
        assert payload["messages"][1]["function_call"]["arguments"] == upstream_args
        call = {"name": "categorize", "arguments": upstream_args}
        body = {
            "choices": [
                {
                    "delta" if streaming else "message": {"function_call": call},
                    "finish_reason": "tool_call",
                }
            ]
        }
        if streaming:
            return httpx.Response(
                200, content="data: " + json.dumps(body) + "\n\ndata: [DONE]\n\n"
            )
        return httpx.Response(200, json=body)

    cfg = build_gigachat_config(
        {"mode": "local", "client_cert": "mock", "client_key": "mock"}
    )
    async with httpx.AsyncClient(
        base_url="https://giga.test", transport=httpx.MockTransport(handler)
    ) as http:
        client = GigaChatHTTPClient(cfg, http_client=http)
        payload = {
            "tools": [
                {
                    "type": "function",
                    "function": {"name": "categorize", "parameters": schema},
                }
            ],
            "messages": [
                {"role": "user", "content": "Categorize"},
                {
                    "role": "assistant",
                    "tool_calls": [
                        {
                            "id": "call_1",
                            "type": "function",
                            "function": {
                                "name": "categorize",
                                "arguments": json.dumps(expected),
                            },
                        }
                    ],
                },
                {"role": "tool", "tool_call_id": "call_1", "content": "[]"},
                {"role": "user", "content": "Repeat"},
            ],
        }
        before = deepcopy(payload)
        if streaming:
            chunks = [chunk async for chunk in client.stream_chat_completions(payload)]
            response = httpx.Response(200, content="".join(chunks))
        else:
            response = httpx.Response(200, json=await client.chat_completions(payload))
        assert response_arguments("chat", response, streaming) == expected
        assert payload == before
