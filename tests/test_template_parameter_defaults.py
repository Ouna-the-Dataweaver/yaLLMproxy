import json

import pytest

from src.core.backend import Backend, ParameterConfig, build_backend_body


@pytest.mark.parametrize("nested", [False, True])
def test_template_parameter_defaults_are_not_recursively_unwrapped(nested):
    low = {"thinking": True, "reasoning_effort": "low"}
    value = {"default": low, "allow_override": True} if nested else low
    backend = Backend(
        name="probe", base_url="http://test.local/v1", api_key="test-key",
        timeout=30, target_model="probe",
        parameters={"chat_template_kwargs": ParameterConfig(default=value)},
    )
    body = build_backend_body({"model": "probe"}, backend, b'{"model":"probe"}')
    assert json.loads(body)["chat_template_kwargs"] == value
