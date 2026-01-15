"""Response module pipeline (alias for legacy parser pipeline)."""

from __future__ import annotations

from ..parsers.response_pipeline import (
    ModuleContext,
    ParserContext,
    ResponseModule,
    ResponseModulePipeline,
    ResponseStreamModule,
    ResponseParser,
    ResponseParserPipeline,
    ResponseStreamParser,
    ParseTagsParser,
    ReasoningSwapParser,
    SSEDecoder,
    SSEEvent,
    build_response_module_pipeline,
    build_response_module_overrides,
    build_response_parser_pipeline,
    build_response_parser_overrides,
)

# Legacy alias - TemplateParseParser is now unified into ParseTagsParser
TemplateParseParser = ParseTagsParser

__all__ = [
    "ModuleContext",
    "ParserContext",
    "ResponseModule",
    "ResponseModulePipeline",
    "ResponseStreamModule",
    "ResponseParser",
    "ResponseParserPipeline",
    "ResponseStreamParser",
    "ParseTagsParser",
    "TemplateParseParser",  # Legacy alias
    "ReasoningSwapParser",
    "SSEDecoder",
    "SSEEvent",
    "build_response_module_pipeline",
    "build_response_module_overrides",
    "build_response_parser_pipeline",
    "build_response_parser_overrides",
]
