"""Assemble GigaChat tool arguments before restoring required nullable fields."""

from __future__ import annotations

from copy import deepcopy
from typing import Any, Mapping

from .tool_schema import ToolArguments
from .translator import gigachat_chunk_to_openai


class ToolCallStream:
    def __init__(self, arguments: ToolArguments, model: str) -> None:
        self.arguments = arguments
        self.model = model
        self.pending: dict[int, dict[str, Any]] = {}
        self.envelopes: dict[int, dict[str, Any]] = {}

    def _convert(self, data: dict[str, Any]) -> dict[str, Any]:
        converted = gigachat_chunk_to_openai(data, request_model=self.model)
        return self.arguments.client_format(converted, streaming=True)

    def feed(self, data: Mapping[str, Any]) -> list[dict[str, Any]]:
        if not self.arguments.legacy and not any(
            plan.needs_restoration for plan in self.arguments.plans.values()
        ):
            return [self._convert(dict(data))]
        data = deepcopy(dict(data))
        output = []
        choices = data.get("choices") or []
        if not choices:
            return [self._convert(data)]
        for position, choice in enumerate(choices):
            index = choice.get("index", position)
            delta = choice.get("delta") or choice.get("message") or {}
            call = delta.get("function_call")
            if isinstance(call, dict):
                pending = self.pending.setdefault(index, {})
                self.envelopes[index] = {
                    k: v for k, v in data.items() if k not in {"choices", "usage"}
                }
                if call.get("name"):
                    name = str(call["name"])
                    previous = pending.get("name", "")
                    pending["name"] = (
                        name if not previous or name == previous else previous + name
                    )
                if "arguments" in call:
                    value = call["arguments"]
                    if isinstance(value, str) and isinstance(
                        pending.get("arguments", ""), str
                    ):
                        pending["arguments"] = pending.get("arguments", "") + value
                    elif isinstance(value, dict) and isinstance(
                        pending.get("arguments", {}), dict
                    ):
                        pending.setdefault("arguments", {}).update(value)
                    else:
                        pending["arguments"] = value
                delta.pop("function_call", None)
            finish = choice.get("finish_reason")
            if index in self.pending:
                # Text/reasoning may continue streaming while tool JSON waits.
                if delta:
                    text_choice = {
                        "index": index,
                        "delta": delta,
                        "finish_reason": None,
                    }
                    text_data = {**data, "choices": [text_choice]}
                    if finish is not None:
                        text_data.pop("usage", None)
                    output.append(self._convert(text_data))
                if finish is not None:
                    output.append(self._finish(index, finish, data.get("usage")))
            else:
                output.append(self._convert({**data, "choices": [choice]}))
        return output

    def _finish(self, index: int, reason: str, usage: Any = None) -> dict[str, Any]:
        call = self.arguments.restore_call(self.pending.pop(index))
        envelope = self.envelopes.pop(index)
        envelope["choices"] = [
            {"index": index, "delta": {"function_call": call}, "finish_reason": reason}
        ]
        if usage is not None:
            envelope["usage"] = usage
        return self._convert(envelope)

    def finish(self) -> list[dict[str, Any]]:
        return [self._finish(index, "tool_call") for index in list(self.pending)]
