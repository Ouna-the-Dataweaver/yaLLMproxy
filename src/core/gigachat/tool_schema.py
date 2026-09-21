"""Adapt nullable tool fields without changing the caller's argument contract."""

from __future__ import annotations

import json
from collections.abc import Mapping
from copy import deepcopy
from dataclasses import dataclass, field
from typing import Any


@dataclass
class ArgumentSchema:
    schema: dict[str, Any]
    nullable: bool = False
    null_only: bool = False
    properties: dict[str, ArgumentSchema] = field(default_factory=dict)
    required_nulls: set[str] = field(default_factory=set)
    items: ArgumentSchema | None = None
    additional: ArgumentSchema | None = None
    scalar_variants: dict[str, ArgumentSchema] = field(default_factory=dict)

    @property
    def needs_restoration(self) -> bool:
        return (
            bool(self.required_nulls or self.scalar_variants)
            or any(child.needs_restoration for child in self.properties.values())
            or bool(
                (self.items and self.items.needs_restoration)
                or (self.additional and self.additional.needs_restoration)
            )
        )

    def transform(self, value: Any, *, upstream: bool = False) -> Any:
        """Copy arguments, restoring missing nulls or omitting nulls upstream."""
        if self.scalar_variants:
            if value is None and self.nullable:
                return None
            if upstream:
                kind = _scalar_kind(value)
                if kind == "integer" and kind not in self.scalar_variants:
                    kind = "number"
                if kind not in self.scalar_variants:
                    raise ValueError("Invalid GigaChat scalar union argument type")
                return {kind: deepcopy(value)}
            if not isinstance(value, dict) or len(value) != 1:
                raise ValueError(
                    "GigaChat scalar union requires exactly one typed value"
                )
            kind, scalar = next(iter(value.items()))
            actual = _scalar_kind(scalar)
            if kind not in self.scalar_variants or not (
                actual == kind or (kind == "number" and actual == "integer")
            ):
                raise ValueError("Invalid GigaChat scalar union argument type")
            return deepcopy(scalar)
        if isinstance(value, dict):
            result = {}
            for name, child_value in value.items():
                child = self.properties.get(name, self.additional)
                if upstream and child and child.nullable and child_value is None:
                    continue
                result[name] = (
                    child.transform(child_value, upstream=upstream)
                    if child
                    else deepcopy(child_value)
                )
            if not upstream:
                for name in self.required_nulls:
                    result.setdefault(name, None)
            return result
        if isinstance(value, list) and self.items:
            return [self.items.transform(item, upstream=upstream) for item in value]
        return deepcopy(value)

    def arguments(self, value: Any, *, upstream: bool = False) -> Any:
        if isinstance(value, str):
            try:
                parsed = json.loads(value)
            except ValueError:
                return value  # Never manufacture arguments from malformed JSON.
            return json.dumps(
                self.transform(parsed, upstream=upstream), ensure_ascii=False
            )
        return self.transform(value, upstream=upstream)


def _scalar_kind(value: Any) -> str | None:
    # bool is an int subclass, but must never select the integer branch.
    # JSON Schema also considers integral JSON numbers (e.g. 1.0) integers.
    if isinstance(value, float) and value.is_integer():
        return "integer"
    return {str: "string", bool: "boolean", int: "integer", float: "number"}.get(
        type(value)
    )


def _scalar_union(
    node: dict[str, Any], key: str, variants: list[ArgumentSchema], nullable: bool
) -> ArgumentSchema | None:
    """Represent distinct scalar types using GigaChat's supported object schema.

    GigaChat requires a single string-valued `type`; native anyOf and type lists
    fail upstream. A typed envelope preserves scalar types in both directions.
    Object unions and intersections still need branch-aware schema handling.
    """
    if key not in {"anyOf", "oneOf"} or len(variants) < 2:
        return None
    kinds = [variant.schema.get("type") for variant in variants]
    if not all(
        isinstance(kind, str) and kind in {"string", "integer", "number", "boolean"}
        for kind in kinds
    ):
        return None
    if len(set(kinds)) != len(kinds) or (
        key == "oneOf" and {"integer", "number"}.issubset(kinds)
    ):
        raise ValueError("GigaChat scalar union branches must have distinct types")
    # Constraints outside the branches apply to the scalar, not the envelope.
    # Do not silently discard them or move them onto an object.
    if set(node) - {
        key,
        "title",
        "description",
        "default",
        "examples",
        "nullable",
    } or any(
        variant.needs_restoration
        or any(k in variant.schema for k in ("anyOf", "oneOf", "allOf", "not"))
        for variant in variants
    ):
        raise ValueError("Unsupported constraints on GigaChat scalar union")
    properties = {}
    for kind, variant in zip(kinds, variants, strict=True):
        properties[kind] = deepcopy(variant.schema)
        properties[kind].setdefault(
            "description",
            f"The {kind} value. Set only this member when choosing {kind}.",
        )
    description = (
        str(node.get("description") or "")
        + " Provide exactly one member: "
        + ", ".join(kinds)
        + ". Do not set the other members."
    ).strip()
    schema = {
        "type": "object",
        "properties": properties,
        "additionalProperties": False,
        "description": description,
    }
    if "title" in node:
        schema["title"] = node["title"]
    plan = ArgumentSchema(
        schema,
        nullable=nullable,
        scalar_variants=dict(zip(kinds, variants, strict=True)),
    )
    if "default" in node and node["default"] is not None:
        schema["default"] = plan.transform(node["default"], upstream=True)
    if "examples" in node:
        schema["examples"] = [
            plan.transform(value, upstream=True)
            for value in node["examples"]
            if value is not None
        ]
    return plan


def adapt_schema(schema: Mapping[str, Any]) -> ArgumentSchema:
    """Resolve references, adapt nullability, and encode scalar union choices.

    Only schema-bearing keywords are traversed: defaults, examples, and object
    enum values are data, and property names may themselves be schema keywords.
    """
    root = deepcopy(dict(schema))

    def compile_node(
        node: dict[str, Any], refs: frozenset[str] = frozenset()
    ) -> ArgumentSchema:
        node = deepcopy(node)
        ref = node.pop("$ref", None)
        if ref is not None:
            if not isinstance(ref, str) or not ref.startswith("#/") or ref in refs:
                raise ValueError(
                    "GigaChat tool schemas require non-recursive local references"
                )
            target: Any = root
            try:
                for part in ref[2:].split("/"):
                    target = target[part.replace("~1", "/").replace("~0", "~")]
            except (KeyError, TypeError) as exc:
                raise ValueError(
                    f"Unresolved GigaChat tool schema reference: {ref}"
                ) from exc
            if not isinstance(target, dict):
                raise ValueError(
                    f"GigaChat tool schema reference is not an object: {ref}"
                )
            return compile_node({**target, **node}, refs | {ref})

        node.pop("$defs", None)
        node.pop("definitions", None)
        # GigaChat only accepts temporal formats. Keep other format annotations
        # as model guidance; the caller still validates against its own schema.
        schema_format = node.get("format")
        if isinstance(schema_format, str) and schema_format not in {
            "date",
            "date-time",
            "time",
        }:
            node.pop("format")
            node["description"] = (
                str(node.get("description") or "") + f" Format: {schema_format}."
            ).strip()
        const_null = "const" in node and node["const"] is None
        nullable = node.get("nullable") is True or const_null
        schema_type = node.get("type")
        if schema_type == "null":
            nullable = True
        if isinstance(schema_type, list) and schema_type:
            nullable = nullable or "null" in schema_type
            types = [item for item in schema_type if item != "null"]
            node["type"] = types[0] if len(types) == 1 else types or "null"
        enum = node.get("enum")
        if isinstance(enum, list) and None in enum:
            nullable = True
            node["enum"] = [item for item in enum if item is not None]
            # Pydantic Literal['a', 'b', None] has enum but no type. Once
            # null is removed, GigaChat needs the remaining value type.
            if "type" not in node and node["enum"]:
                json_types = {
                    str: "string",
                    bool: "boolean",
                    int: "integer",
                    float: "number",
                    list: "array",
                    dict: "object",
                }
                types = {json_types.get(type(item)) for item in node["enum"]}
                if types <= {"integer", "number"}:
                    node["type"] = "number" if "number" in types else "integer"
                elif len(types) == 1 and None not in types:
                    node["type"] = types.pop()

        # Unwrap simple nullable unions, including referenced null branches.
        for key in ("anyOf", "oneOf", "allOf"):
            variants = node.get(key)
            if not isinstance(variants, list) or not all(
                isinstance(item, dict) for item in variants
            ):
                continue
            compiled = [compile_node(item, refs) for item in variants]
            non_null = [item for item in compiled if not item.null_only]
            if key != "allOf" and any(item.nullable for item in compiled):
                nullable = True
            if len(non_null) == 1 and (key != "allOf" or len(compiled) == 1):
                # Keep the selected branch's restoration metadata, not just its
                # normalized schema (which has already lost its required nulls).
                branch = next(
                    item
                    for item, plan in zip(variants, compiled, strict=True)
                    if not plan.null_only
                )
                combined = {**branch, **{k: v for k, v in node.items() if k != key}}
                plan = compile_node(combined, refs)
                plan.nullable = plan.nullable or nullable
                if plan.nullable:
                    plan.schema.pop("nullable", None)
                    if plan.schema.get("default") is None:
                        plan.schema.pop("default", None)
                return plan
            if not non_null and key != "allOf":
                return ArgumentSchema({"type": "null"}, nullable=True, null_only=True)
            scalar_plan = _scalar_union(node, key, non_null, nullable)
            if scalar_plan is not None:
                return scalar_plan
            # Ambiguous unions cannot safely choose where missing fields belong.
            if any(item.needs_restoration or item.nullable for item in compiled):
                raise ValueError(
                    "GigaChat nullable tool schemas require a single non-null union variant"
                )
            node[key] = [item.schema for item in compiled]

        null_only = (
            const_null
            or node.get("type") == "null"
            or (isinstance(enum, list) and bool(enum) and not node["enum"])
        )
        plan = ArgumentSchema(node, nullable=nullable, null_only=null_only)
        if nullable:
            node.pop("nullable", None)
            if node.get("default") is None:
                node.pop("default", None)
        properties = node.get("properties")
        if isinstance(properties, dict):
            required = node.get("required", [])
            for name, child in list(properties.items()):
                if not isinstance(child, dict):
                    continue
                adapted = compile_node(child, refs)
                plan.properties[name] = adapted
                if adapted.nullable and name in required:
                    plan.required_nulls.add(name)
                if adapted.null_only:
                    del properties[name]
                else:
                    properties[name] = adapted.schema
            if isinstance(required, list) and "required" in node:
                node["required"] = [
                    name for name in required if name not in plan.required_nulls
                ]
        if isinstance(node.get("items"), dict):
            plan.items = compile_node(node["items"], refs)
            node["items"] = plan.items.schema
        if isinstance(node.get("additionalProperties"), dict):
            plan.additional = compile_node(node["additionalProperties"], refs)
            node["additionalProperties"] = plan.additional.schema
        return plan

    return compile_node(root)


@dataclass
class ToolArguments:
    """Per-request plans, keyed by function name; never shared across requests."""

    plans: dict[str, ArgumentSchema]
    legacy: bool = False

    @classmethod
    def prepare(
        cls, payload: Mapping[str, Any]
    ) -> tuple[dict[str, Any], ToolArguments]:
        prepared = deepcopy(dict(payload))
        legacy = "tools" not in prepared and isinstance(prepared.get("functions"), list)
        if legacy:
            prepared["tools"] = [
                {"type": "function", "function": spec}
                for spec in prepared.pop("functions")
            ]
            choice = prepared.pop("function_call", "auto")
            prepared["tool_choice"] = (
                {"type": "function", "function": choice}
                if isinstance(choice, dict)
                else choice
            )
        plans = {}
        for tool in prepared.get("tools") or []:
            if not isinstance(tool, dict) or tool.get("type", "function") != "function":
                continue
            spec = tool.get("function")
            if not isinstance(spec, dict) or not isinstance(spec.get("name"), str):
                continue
            schema = spec.get("parameters")
            if not isinstance(schema, dict):
                continue
            plan = adapt_schema(schema)
            plans[spec["name"]] = plan
            spec["parameters"] = plan.schema
        adapter = cls(plans, legacy)
        # Tool arguments echoed in conversation history use the upstream form.
        for message in prepared.get("messages") or []:
            if not isinstance(message, dict):
                continue
            calls = [
                call.get("function")
                for call in message.get("tool_calls") or []
                if isinstance(call, dict)
            ]
            if isinstance(message.get("function_call"), dict):
                calls.append(message["function_call"])
            for call in calls:
                if (
                    isinstance(call, dict)
                    and call.get("name") in plans
                    and "arguments" in call
                ):
                    call["arguments"] = plans[call["name"]].arguments(
                        call["arguments"], upstream=True
                    )
            if (
                isinstance(message.get("function_call"), dict)
                and "tool_calls" not in message
            ):
                message["tool_calls"] = [
                    {"type": "function", "function": message.pop("function_call")}
                ]
        return prepared, adapter

    def restore_call(self, call: Mapping[str, Any]) -> dict[str, Any]:
        result = deepcopy(dict(call))
        plan = self.plans.get(result.get("name"))
        if plan:
            # A completed argument-less call represents an empty object.
            result["arguments"] = plan.arguments(result.get("arguments", {}))
        return result

    def restore_response(
        self, response: Mapping[str, Any], *, convert_legacy: bool = True
    ) -> dict[str, Any]:
        result = deepcopy(dict(response))
        for choice in result.get("choices") or []:
            message = choice.get("message") or {}
            for call in message.get("tool_calls") or []:
                if isinstance(call.get("function"), dict):
                    call["function"] = self.restore_call(call["function"])
        return self.client_format(result) if convert_legacy else result

    def client_format(
        self, response: Mapping[str, Any], *, streaming: bool = False
    ) -> dict[str, Any]:
        result = deepcopy(dict(response))
        if self.legacy:
            for choice in result.get("choices") or []:
                message = choice.get("delta" if streaming else "message") or {}
                if message.get("tool_calls"):
                    message["function_call"] = message.pop("tool_calls")[0]["function"]
                if choice.get("finish_reason") == "tool_calls":
                    choice["finish_reason"] = "function_call"
        return result
