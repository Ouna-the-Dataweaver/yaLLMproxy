"""Testing utilities for in-process proxy simulations."""

from .fake_upstream import FakeUpstream, UpstreamResponse
from .proxy_harness import ProxyHarness
from .template_unparse import (
    TemplateMarkers,
    detect_template_markers,
    normalize_message_for_compare,
    normalize_tool_calls,
    render_unparsed_content,
    unparse_assistant_message,
)

__all__ = [
    "FakeUpstream",
    "UpstreamResponse",
    "ProxyHarness",
    "TemplateMarkers",
    "detect_template_markers",
    "normalize_message_for_compare",
    "normalize_tool_calls",
    "render_unparsed_content",
    "unparse_assistant_message",
]
