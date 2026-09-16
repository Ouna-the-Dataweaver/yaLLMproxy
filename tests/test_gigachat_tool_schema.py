from __future__ import annotations

from copy import deepcopy

import pytest

from src.core.gigachat.tool_schema import ToolArguments, adapt_schema

NULLABLE_FORMS = [
    {"enum": ["Day", "Year", None]},
    {"type": "string", "enum": ["Day", "Year", None]},
    {"type": ["string", "null"], "enum": ["Day", "Year", None]},
    {"anyOf": [{"type": "string", "enum": ["Day", "Year"]}, {"type": "null"}]},
    {"oneOf": [{"type": "null"}, {"type": "string", "enum": ["Day", "Year"]}]},
    {"type": "string", "enum": ["Day", "Year"], "nullable": True},
]


@pytest.mark.parametrize(
    "values, expected_type",
    [
        ([False, True, None], "boolean"),
        ([0, 1, None], "integer"),
        ([0, 1.5, None], "number"),
    ],
)
def test_nullable_literal_enum_infers_missing_type(values, expected_type):
    plan = adapt_schema(
        {
            "type": "object",
            "properties": {"value": {"enum": values}},
            "required": ["value"],
        }
    )
    assert plan.schema["properties"]["value"] == {
        "type": expected_type,
        "enum": values[:-1],
    }
    assert plan.schema["required"] == []
    assert plan.transform({}) == {"value": None}


@pytest.mark.parametrize("nullable", NULLABLE_FORMS)
@pytest.mark.parametrize("name", ["period", "required", "enum", "type", "anyOf"])
def test_nullable_fields_round_trip(nullable, name):
    original = {
        "type": "object",
        "properties": {name: nullable, "query": {"type": "string"}},
        "required": [name, "query"],
    }
    before = deepcopy(original)
    plan = adapt_schema(original)
    assert original == before
    assert plan.schema["required"] == ["query"]
    assert plan.schema["properties"][name] == {
        "type": "string",
        "enum": ["Day", "Year"],
    }
    assert plan.transform({"query": "test"}) == {"query": "test", name: None}
    assert plan.transform({"query": "test", name: "Day"}) == {
        "query": "test",
        name: "Day",
    }
    assert plan.transform({name: None}, upstream=True) == {}


@pytest.mark.parametrize("value", [False, 0, "", [], {}, None])
def test_present_values_are_never_overwritten(value):
    plan = adapt_schema(
        {
            "type": "object",
            "properties": {"arg": {"type": ["string", "null"]}},
            "required": ["arg"],
        }
    )
    assert plan.transform({"arg": value}) == {"arg": value}


def test_nested_arrays_refs_maps_and_optional_parents():
    schema = {
        "$defs": {
            "Filter": {
                "type": "object",
                "properties": {"term": {"type": ["string", "null"]}},
                "required": ["term"],
            }
        },
        "type": "object",
        "properties": {
            "filters": {"type": "array", "items": {"$ref": "#/$defs/Filter"}},
            "mapping": {
                "type": "object",
                "additionalProperties": {"$ref": "#/$defs/Filter"},
            },
            "parent": {"$ref": "#/$defs/Filter"},
            "optional": {"type": ["string", "null"]},
            "nullable_parent": {
                "anyOf": [{"$ref": "#/$defs/Filter"}, {"type": "null"}]
            },
        },
        "required": ["filters", "nullable_parent"],
    }
    plan = adapt_schema(schema)
    value = {
        "filters": [{}, {"term": "x"}],
        "mapping": {"one": {}},
        "nullable_parent": {},
    }
    restored = plan.transform(value)
    assert restored == {
        "filters": [{"term": None}, {"term": "x"}],
        "mapping": {"one": {"term": None}},
        "nullable_parent": {"term": None},
    }
    assert "parent" not in restored and "optional" not in restored
    assert plan.transform({}) == {"nullable_parent": None}
    assert value["filters"][0] == {}
    assert plan.schema["properties"]["nullable_parent"]["required"] == []


def test_data_keywords_and_null_only_fields():
    example = {"required": ["x"], "properties": {"x": {"enum": [None]}}}
    plan = adapt_schema(
        {
            "type": "object",
            "default": example,
            "examples": [example],
            "properties": {
                "empty": {"type": "null"},
                "enum_null": {"enum": [None]},
                "optional": {"type": "null"},
                "literal_null": {"const": None},
            },
            "required": ["empty", "enum_null", "literal_null"],
        }
    )
    assert plan.schema["default"] == example
    assert plan.schema["examples"] == [example]
    assert plan.schema["properties"] == {}
    assert plan.transform({}) == {
        "empty": None,
        "enum_null": None,
        "literal_null": None,
    }


@pytest.mark.parametrize("keyword", ["anyOf", "oneOf", "allOf"])
def test_ambiguous_nested_nullable_union_fails_without_guessing_branch(keyword):
    nullable_object = {
        "type": "object",
        "properties": {
            "nested": {
                "type": "object",
                "properties": {"value": {"type": ["string", "null"]}},
                "required": ["value"],
            }
        },
    }
    with pytest.raises(ValueError, match="single non-null union variant"):
        adapt_schema({keyword: [nullable_object, {"type": "string"}]})


def test_definitions_and_escaped_reference():
    plan = adapt_schema(
        {
            "definitions": {"a/b~c": {"type": ["integer", "null"]}},
            "type": "object",
            "properties": {
                "value": {
                    "allOf": [{"$ref": "#/definitions/a~1b~0c"}],
                    "description": "value",
                },
            },
            "required": ["value"],
        }
    )
    assert plan.schema["properties"]["value"] == {
        "type": "integer",
        "description": "value",
    }
    assert plan.transform({}) == {"value": None}


@pytest.mark.parametrize(
    "ref", ["#/missing", "https://example.com/schema", "#/$defs/Loop"]
)
def test_invalid_or_recursive_refs_fail_explicitly(ref):
    with pytest.raises(ValueError):
        adapt_schema({"$defs": {"Loop": {"$ref": "#/$defs/Loop"}}, "$ref": ref})


def test_only_originally_required_fields_are_restored_and_history_is_copied():
    schema = {
        "type": "object",
        "properties": {
            "a": {"type": ["integer", "null"]},
            "b": {"type": ["integer", "null"]},
        },
        "required": ["a"],
    }
    payload = {
        "tools": [
            {"type": "function", "function": {"name": "first", "parameters": schema}},
            {
                "type": "function",
                "function": {
                    "name": "second",
                    "parameters": {**schema, "required": ["b"]},
                },
            },
        ],
        "messages": [
            {
                "role": "assistant",
                "tool_calls": [
                    {"function": {"name": "first", "arguments": '{"a":null,"b":0}'}}
                ],
            }
        ],
    }
    before = deepcopy(payload)
    upstream, plans = ToolArguments.prepare(payload)
    assert payload == before
    assert (
        upstream["messages"][0]["tool_calls"][0]["function"]["arguments"] == '{"b": 0}'
    )
    assert plans.restore_call({"name": "first", "arguments": {}})["arguments"] == {
        "a": None
    }
    assert plans.restore_call({"name": "second", "arguments": {}})["arguments"] == {
        "b": None
    }
    assert plans.restore_call({"name": "unknown", "arguments": {}})["arguments"] == {}
    assert (
        plans.restore_call({"name": "first", "arguments": '{"broken"'})["arguments"]
        == '{"broken"'
    )
