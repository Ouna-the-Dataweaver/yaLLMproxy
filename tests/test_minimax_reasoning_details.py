"""Tests for MiniMax reasoning_details normalization."""

import json
import sys
from pathlib import Path

# Add src directory to path
sys.path.insert(0, str(Path(__file__).parent.parent / "src"))

from src.parsers.response_pipeline import ParserContext, ResponseParserPipeline


def test_nonstream_reasoning_details_normalized_to_reasoning_content() -> None:
    pipeline = ResponseParserPipeline([], [])
    payload = {
        "id": "resp_1",
        "choices": [
            {
                "index": 0,
                "message": {
                    "role": "assistant",
                    "content": "Visible answer",
                    "reasoning_details": [
                        {"type": "reasoning.text", "text": "Need to inspect the code first."}
                    ],
                },
                "finish_reason": "stop",
            }
        ],
    }

    body = json.dumps(payload).encode("utf-8")
    transformed = pipeline.transform_response_body(
        body,
        "application/json",
        ParserContext(path="/chat/completions", model="MiniMax-M2.7", backend="MiniMax-M2.7", is_stream=False),
    )

    assert transformed is not None
    parsed = json.loads(transformed)
    message = parsed["choices"][0]["message"]
    assert message["reasoning_content"] == "Need to inspect the code first."
    assert message["reasoning_details"][0]["text"] == "Need to inspect the code first."


def test_stream_reasoning_details_normalized_to_reasoning_content() -> None:
    pipeline = ResponseParserPipeline([], ["/chat/completions"])
    parser = pipeline.create_stream_parser(
        ParserContext(path="/chat/completions", model="MiniMax-M2.7", backend="MiniMax-M2.7", is_stream=True)
    )

    assert parser is not None
    chunk = (
        'data: {"id":"resp_1","choices":[{"index":0,"delta":{"role":"assistant","reasoning_details":'
        '[{"type":"reasoning.text","text":"Plan the fix."}]}}]}\n\n'
    ).encode("utf-8")

    output = parser.feed_bytes(chunk)

    assert len(output) == 1
    encoded = output[0].decode("utf-8")
    assert '"reasoning_content": "Plan the fix."' in encoded
    assert '"reasoning_details"' in encoded


def test_stream_reasoning_details_preserves_boundary_spaces() -> None:
    pipeline = ResponseParserPipeline([], ["/chat/completions"])
    parser = pipeline.create_stream_parser(
        ParserContext(path="/chat/completions", model="MiniMax-M2.7", backend="MiniMax-M2.7", is_stream=True)
    )

    assert parser is not None
    first_chunk = (
        'data: {"id":"resp_1","choices":[{"index":0,"delta":{"role":"assistant","reasoning_details":'
        '[{"type":"reasoning.text","text":"The "}]}}]}\n\n'
    ).encode("utf-8")
    second_chunk = (
        'data: {"id":"resp_1","choices":[{"index":0,"delta":{"reasoning_details":'
        '[{"type":"reasoning.text","text":"user asked"}]}}]}\n\n'
    ).encode("utf-8")

    first_output = parser.feed_bytes(first_chunk)
    second_output = parser.feed_bytes(second_chunk)

    assert '"reasoning_content": "The "' in first_output[0].decode("utf-8")
    assert '"reasoning_content": "user asked"' in second_output[0].decode("utf-8")
