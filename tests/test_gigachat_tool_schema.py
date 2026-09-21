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


@pytest.mark.parametrize("schema_format", ["uuid", "uri", "email", "int64"])
def test_unsupported_formats_become_upstream_description_hints(schema_format):
    original = {
        "type": "object",
        "properties": {
            "format": {
                "type": "string",
                "format": schema_format,
                "description": "Identifier.",
            },
        },
        "required": ["format"],
    }
    before = deepcopy(original)
    plan = adapt_schema(original)
    field = plan.schema["properties"]["format"]
    assert field["type"] == "string"
    assert "format" not in field
    assert field["description"] == f"Identifier. Format: {schema_format}."
    assert plan.schema["required"] == ["format"]
    assert plan.transform({"format": "unchanged"}) == {"format": "unchanged"}
    assert adapt_schema(plan.schema).schema == plan.schema
    assert original == before


@pytest.mark.parametrize("schema_format", ["date", "date-time", "time"])
def test_supported_gigachat_formats_are_preserved(schema_format):
    original = {"type": "string", "format": schema_format}
    assert adapt_schema(original).schema == original


@pytest.mark.parametrize("keyword", ["anyOf", "oneOf"])
@pytest.mark.parametrize("required", [False, True])
def test_scalar_nullable_union_preserves_date_types_and_omissions(keyword, required):
    original = {
        "type": "object",
        "properties": {
            "date": {
                keyword: [{"type": "string"}, {"type": "integer"}, {"type": "null"}],
                "default": None,
                "description": "Date or Unix timestamp.",
            },
        },
        "required": ["date"] if required else [],
    }
    before = deepcopy(original)
    plan = adapt_schema(original)
    field = plan.schema["properties"]["date"]
    assert field["type"] == "object"
    assert set(field["properties"]) == {"string", "integer"}
    assert "default" not in field
    assert plan.schema["required"] == []
    assert plan.needs_restoration  # Optional unions also need stream decoding.
    assert plan.transform({}) == ({"date": None} if required else {})
    assert plan.transform({"date": None}, upstream=True) == {}
    for value, branch in [
        ("2026-09-21", "string"),
        ("12345", "string"),
        ("", "string"),
        (0, "integer"),
        (12345, "integer"),
    ]:
        wire = {"date": {branch: value}}
        assert plan.transform({"date": value}, upstream=True) == wire
        assert plan.transform(wire) == {"date": value}
    assert original == before
    # The translator prepares schemas a second time; the wire schema is stable.
    assert adapt_schema(plan.schema).schema == plan.schema


@pytest.mark.parametrize(
    "value",
    [
        {},
        {"string": "x", "integer": 1},
        {"integer": True},
        {"integer": "12"},
        {"unknown": 1},
    ],
)
def test_scalar_union_rejects_ambiguous_or_invalid_wire_values(value):
    plan = adapt_schema(
        {"anyOf": [{"type": "string"}, {"type": "integer"}, {"type": "null"}]}
    )
    with pytest.raises(ValueError, match="scalar union"):
        plan.transform(value)


def test_scalar_union_nested_refs_keep_constraints_and_types():
    schema = {
        "$defs": {"Flag": {"type": "boolean"}},
        "type": "object",
        "properties": {
            "values": {
                "type": "array",
                "items": {
                    "anyOf": [
                        {"$ref": "#/$defs/Flag"},
                        {"type": "integer", "minimum": 0},
                    ]
                },
            },
        },
    }
    plan = adapt_schema(schema)
    assert (
        plan.schema["properties"]["values"]["items"]["properties"]["integer"]["minimum"]
        == 0
    )
    values = {"values": [False, 0, True, 1]}
    wire = {
        "values": [
            {"boolean": False},
            {"integer": 0},
            {"boolean": True},
            {"integer": 1},
        ]
    }
    assert plan.transform(values, upstream=True) == wire
    assert plan.transform(wire) == values


def test_scalar_oneof_overlapping_numeric_branches_still_rejected():
    with pytest.raises(ValueError):
        adapt_schema(
            {"oneOf": [{"type": "integer"}, {"type": "number"}, {"type": "null"}]}
        )


def test_scalar_union_numeric_defaults_examples_and_map_values():
    plan = adapt_schema(
        {
            "type": "object",
            "additionalProperties": {
                "anyOf": [{"type": "number"}, {"type": "string"}, {"type": "null"}],
                "default": 0,
                "examples": [None, 1.5, "1.5"],
            },
        }
    )
    field = plan.schema["additionalProperties"]
    assert field["default"] == {"number": 0}
    assert field["examples"] == [{"number": 1.5}, {"string": "1.5"}]
    original = {"zero": 0, "decimal": 1.5, "integer_number": 1.0, "text": "1.5"}
    wire = {
        "zero": {"number": 0},
        "decimal": {"number": 1.5},
        "integer_number": {"number": 1.0},
        "text": {"string": "1.5"},
    }
    assert plan.transform(original, upstream=True) == wire
    assert plan.transform(wire) == original


def test_scalar_union_integral_json_numbers_preserve_value_without_coercion():
    plan = adapt_schema({"anyOf": [{"type": "string"}, {"type": "integer"}]})
    assert plan.transform(1.0, upstream=True) == {"integer": 1.0}
    assert plan.transform({"integer": 1.0}) == 1.0
    assert type(plan.transform({"integer": 1.0})) is float
    with pytest.raises(ValueError, match="scalar union"):
        plan.transform(1.5, upstream=True)


def test_scalar_union_does_not_discard_outer_constraints():
    with pytest.raises(ValueError, match="Unsupported constraints"):
        adapt_schema(
            {
                "anyOf": [{"type": "string"}, {"type": "integer"}, {"type": "null"}],
                "enum": ["a", 1, None],
            }
        )


@pytest.mark.parametrize(
    "kind", ["string", "integer", "number", "boolean", "array", "object"]
)
def test_single_type_lists_preserve_required_non_nullable_properties(kind):
    original = {
        "type": ["object"],
        "properties": {"categories": {"type": [kind]}},
        "required": ["categories"],
    }
    before = deepcopy(original)
    plan = adapt_schema(original)
    assert plan.schema == {
        "type": "object",
        "properties": {"categories": {"type": kind}},
        "required": ["categories"],
    }
    assert plan.transform({}) == {}  # A missing non-nullable field is not repaired.
    assert not plan.nullable and not plan.properties["categories"].nullable
    assert original == before


def test_single_type_lists_in_refs_items_maps_and_nullable_unions():
    original = {
        "$defs": {"Category": {"type": ["string"]}},
        "type": ["object"],
        "properties": {
            "categories": {"type": ["array"], "items": {"$ref": "#/$defs/Category"}},
            "mapping": {
                "type": ["object"],
                "additionalProperties": {"type": ["integer"]},
            },
            "period": {"anyOf": [{"type": ["string"]}, {"type": ["null"]}]},
        },
        "required": ["categories", "period"],
    }
    plan = adapt_schema(original)
    assert plan.schema["properties"] == {
        "categories": {"type": "array", "items": {"type": "string"}},
        "mapping": {"type": "object", "additionalProperties": {"type": "integer"}},
        "period": {"type": "string"},
    }
    assert plan.schema["required"] == ["categories"]
    assert plan.transform({"categories": []}) == {"categories": [], "period": None}


@pytest.mark.parametrize("nullable", [False, True])
def test_multiple_non_null_types_are_not_arbitrarily_narrowed(nullable):
    types = ["string", "array"] + (["null"] if nullable else [])
    plan = adapt_schema({"type": types})
    assert plan.schema["type"] == ["string", "array"]
    assert plan.nullable is nullable


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
