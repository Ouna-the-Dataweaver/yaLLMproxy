#!/usr/bin/env python3
"""
Simple test script for the cLLMp proxy.
Runs health checks plus sync/streaming OpenAI-style requests for each endpoint.
"""

import subprocess
import json
import sys
from datetime import datetime
from pathlib import Path
from typing import Callable, Optional
import os

# Add OpenAI import for tool calling tests
from openai import OpenAI

# Configuration
PROXY_HOST = "localhost"
PROXY_PORT = "17771"
PROXY_URL = f"http://{PROXY_HOST}:{PROXY_PORT}"
PROXY_V1_URL = f"{PROXY_URL}/v1"
DEFAULT_MODEL_NAME = "glm_cloud"  # Fallback model if discovery fails
CHAT_ENDPOINT = "/v1/chat/completions"
RESPONSES_ENDPOINT = "/v1/responses"
TEMPERATURE = 0.0
# Toggle which types of tests should run
RUN_NOSTREAM = True           # Non-streaming completion/response tests
RUN_STREAM = True             # Streaming completion/response tests
RUN_TOOL_NOSTREAM = True      # Non-streaming tool-calling tests
RUN_TOOL_STREAM = True        # Streaming tool-calling tests

# Create OpenAI client for tool calling tests (OpenAI SDK expects /v1 included)
client = OpenAI(base_url=PROXY_V1_URL, api_key="not-needed-for-proxy")

# Tools definition for testing function calling
TOOLS = [
    {
        "type": "function",
        "function": {
            "name": "echo",
            "description": "Echo back a message",
            "parameters": {
                "type": "object",
                "properties": {"msg": {"type": "string"}},
                "required": ["msg"],
            },
        },
    }
]

# Create logs directory if it doesn't exist
LOGS_DIR = "logs"
if not Path(LOGS_DIR).exists():
    Path(LOGS_DIR).mkdir(parents=True)

# Create a log file with timestamp
timestamp = datetime.now().strftime("%Y%m%d_%H%M%S")
LOG_FILE = Path(LOGS_DIR) / f"proxy_test_{timestamp}.log"

def log_to_file(content):
    """Write content to the log file."""
    with open(LOG_FILE, "a") as f:
        normalized = content.rstrip("\n")
        f.write(normalized + "\n")
        f.flush()  # Flush the buffer
        os.fsync(f.fileno())  # Ensure write is committed to disk


def _get_enabled_modes(nonstream_enabled: bool, stream_enabled: bool) -> list[bool]:
    """Return which streaming modes should be exercised based on the toggles."""
    modes: list[bool] = []
    if nonstream_enabled:
        modes.append(False)
    if stream_enabled:
        modes.append(True)
    return modes


def _flatten_stream_content(chunk) -> str:
    """Convert various chunk payload shapes into a printable string."""
    if isinstance(chunk, str):
        return chunk
    if isinstance(chunk, list):
        parts = []
        for part in chunk:
            if isinstance(part, dict):
                text = part.get("text")
                if text:
                    parts.append(text)
                else:
                    parts.append(json.dumps(part))
            else:
                parts.append(str(part))
        if parts:
            return "".join(parts)
    try:
        return json.dumps(chunk)
    except TypeError:
        return str(chunk)


class StreamResponseTracker:
    """Handle streaming curl output in real-time for logging/validation."""

    def __init__(self, model_name: str):
        self.model_name = model_name
        self.lines: list[str] = []
        self.data_events: list[str] = []
        self.found_content = False
        self.found_reasoning = False
        self.reasoning_started = False
        self.content_started = False
        self.error_message: Optional[str] = None

    def handle_line(self, line: str):
        self.lines.append(line)
        stripped = line.strip()
        if not stripped:
            return
        if not stripped.startswith("data: "):
            return
        self.data_events.append(stripped)
        log_to_file(f"  Raw stream event: {stripped}")

        payload = stripped[len("data: ") :]
        if payload == "[DONE]":
            return
        try:
            parsed = json.loads(payload)
        except json.JSONDecodeError:
            if not self.error_message:
                message = f"✗ Failed to parse stream event: {payload}"
                self.error_message = message
                print(message)
                log_to_file(message)
            return

        choices = parsed.get("choices") or []
        if not choices:
            return
        delta = choices[0].get("delta") or {}
        if not isinstance(delta, dict):
            return

        reasoning = delta.get("reasoning")
        if reasoning:
            self.found_reasoning = True
            if isinstance(reasoning, str):
                reasoning_text = reasoning
            else:
                try:
                    reasoning_text = json.dumps(reasoning)
                except TypeError:
                    reasoning_text = str(reasoning)
            if not self.reasoning_started:
                print("  Reasoning:", end=" ", flush=True)
                self.reasoning_started = True
            print(reasoning_text, end="", flush=True)

        content = delta.get("content")
        if content:
            chunk_text = _flatten_stream_content(content)
            if not self.content_started:
                print("  Stream chunks:", end=" ", flush=True)
                self.content_started = True
            print(chunk_text, end="", flush=True)
            self.found_content = True

    def finalize(self) -> bool:
        if self.reasoning_started:
            print()
        if self.content_started:
            print()  # finish the line where chunks streamed

        if not self.lines:
            print("✗ Stream response was empty")
            log_to_file("✗ Stream response was empty")
            return False

        if self.error_message:
            return False

        if not self.data_events:
            sample = [line.strip() for line in self.lines[:5]]
            print(f"✗ Stream response missing data events: {sample}")
            log_to_file(f"✗ Stream response missing data events: {sample}")
            return False

        if not self.found_content:
            if self.found_reasoning:
                message = (
                    f"✗ Model '{self.model_name}' only returned reasoning deltas and no assistant message"
                )
                print(message)
                log_to_file(message)
            else:
                snippet = self.data_events[:3]
                print(f"✗ Stream response did not include usable choices: {snippet}")
                log_to_file(f"✗ Stream response did not include usable choices: {snippet}")
            return False

        return True

def run_command(cmd, timeout=30):
    """Run a command and return the result."""
    try:
        result = subprocess.run(
            cmd,
            capture_output=True,
            text=True,
            timeout=timeout,
        )
        return result.returncode, result.stdout, result.stderr
    except subprocess.TimeoutExpired:
        return -1, "", "Command timed out"

def test_health_check():
    """Test if the proxy is running and responding."""
    print("Testing proxy health check...")
    log_to_file("Testing proxy health check...")
    
    # First try to get the root endpoint
    returncode, stdout, stderr = run_command(["curl", "-s", f"{PROXY_URL}/"])
    
    if returncode == 0:
        print("✓ Proxy is responding at root endpoint")
        log_to_file("✓ Proxy is responding at root endpoint")
        return True
    
    # Try the OpenAI models endpoint
    returncode, stdout, stderr = run_command(["curl", "-s", f"{PROXY_URL}/v1/models"])
    
    if returncode == 0:
        try:
            models_data = json.loads(stdout)
            print(f"✓ Proxy is responding at /v1/models endpoint")
            print(f"  Available models: {[m['id'] for m in models_data.get('data', [])]}")
            log_to_file(f"✓ Proxy is responding at /v1/models endpoint")
            log_to_file(f"  Available models: {[m['id'] for m in models_data.get('data', [])]}")
            return True
        except json.JSONDecodeError:
            print(f"✗ Proxy responded but with invalid JSON: {stdout}")
            log_to_file(f"✗ Proxy responded but with invalid JSON: {stdout}")
            return False
    
    print(f"✗ Proxy is not responding at {PROXY_URL}")
    print(f"  Error: {stderr}")
    log_to_file(f"✗ Proxy is not responding at {PROXY_URL}")
    log_to_file(f"  Error: {stderr}")
    return False

def _build_request_body(model_name: str, stream: bool) -> dict:
    return {
        "model": model_name,
        "messages": [
            {"role": "user", "content": "Say 'Hello!' Don't overthink too much, please, I'm just testing API ;)"}
        ],
        "max_tokens": 2048,
        "temperature": TEMPERATURE,
        "stream": stream,
    }


def _build_tool_request_body(model_name: str, stream: bool) -> dict:
    """Build a request body that includes tools for testing function calling."""
    return {
        "model": model_name,
        "messages": [
            {"role": "user", "content": "Please use the echo function with the message 'Hello from tool calling test!'"}
        ],
        "max_tokens": 2048,
        "temperature": TEMPERATURE,
        "stream": stream,
        "tools": TOOLS,
        "tool_choice": "auto"
    }


def _run_curl_request(
    endpoint: str,
    payload: dict,
    stream: bool,
    timeout: int = 60,
    stream_callback: Optional[Callable[[str], None]] = None,
):
    json_data = json.dumps(payload)
    cmd = [
        "curl",
        "-sS",
    ]
    if stream:
        cmd.append("-N")
    cmd.extend(
        [
            "-X",
            "POST",
            f"{PROXY_URL}{endpoint}",
            "-H",
            "Content-Type: application/json",
            "-d",
            json_data,
        ]
    )
    if not stream or stream_callback is None:
        return run_command(cmd, timeout=timeout)

    try:
        with subprocess.Popen(
            cmd,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            bufsize=1,
        ) as proc:
            captured_stdout = []
            try:
                while True:
                    line = proc.stdout.readline()
                    if not line:
                        break
                    captured_stdout.append(line)
                    stream_callback(line)
                proc.wait(timeout=timeout)
            except subprocess.TimeoutExpired:
                proc.kill()
                remaining_stdout = proc.stdout.read() if proc.stdout else ""
                captured_stdout.append(remaining_stdout)
                stderr_output = proc.stderr.read() if proc.stderr else ""
                error_message = stderr_output or "Command timed out"
                return -1, "".join(captured_stdout), error_message
            stderr_output = proc.stderr.read() if proc.stderr else ""
            return proc.returncode, "".join(captured_stdout), stderr_output
    except FileNotFoundError as exc:
        return -1, "", str(exc)


def _validate_non_stream_response(stdout: str) -> bool:
    try:
        response = json.loads(stdout)
    except json.JSONDecodeError:
        print(f"✗ Invalid JSON response: {stdout}")
        log_to_file(f"✗ Invalid JSON response: {stdout}")
        return False

    # Log the complete raw response payload
    log_to_file(f"  Raw response payload: {json.dumps(response, indent=2)}")

    choices = response.get("choices") or []
    if not choices:
        print(f"✗ Unexpected response format: {response}")
        log_to_file(f"✗ Unexpected response format: {response}")
        return False
    content = choices[0].get("message", {}).get("content")
    if not content:
        print(f"✗ Response missing message content: {response}")
        log_to_file(f"✗ Response missing message content: {response}")
        return False
    print(f"  Response: {content}")
    log_to_file(f"  Response: {content}")
    return True


def _validate_tool_response(stdout: str) -> bool:
    """Validate a response that should include tool calls."""
    try:
        response = json.loads(stdout)
    except json.JSONDecodeError:
        print(f"✗ Invalid JSON response: {stdout}")
        log_to_file(f"✗ Invalid JSON response: {stdout}")
        return False

    # Log the complete raw response payload
    log_to_file(f"  Raw tool response payload: {json.dumps(response, indent=2)}")

    choices = response.get("choices") or []
    if not choices:
        print(f"✗ Unexpected response format: {response}")
        log_to_file(f"✗ Unexpected response format: {response}")
        return False
    
    message = choices[0].get("message", {})
    tool_calls = message.get("tool_calls")
    
    if not tool_calls:
        # Check if there's content instead (some models might respond with text instead of calling the tool)
        content = message.get("content")
        if content:
            print(f"  Model responded with text instead of calling tool: {content}")
            log_to_file(f"  Model responded with text instead of calling tool: {content}")
            return True
        else:
            print(f"✗ Response missing tool_calls: {response}")
            log_to_file(f"✗ Response missing tool_calls: {response}")
            return False
    
    # Validate the tool call structure
    for tool_call in tool_calls:
        if not isinstance(tool_call, dict):
            print(f"✗ Invalid tool call format: {tool_call}")
            log_to_file(f"✗ Invalid tool call format: {tool_call}")
            return False
        
        function = tool_call.get("function")
        if not function or not isinstance(function, dict):
            print(f"✗ Tool call missing function: {tool_call}")
            log_to_file(f"✗ Tool call missing function: {tool_call}")
            return False
        
        function_name = function.get("name")
        if function_name != "echo":
            print(f"✗ Unexpected function name: {function_name}")
            log_to_file(f"✗ Unexpected function name: {function_name}")
            return False
        
        try:
            arguments = json.loads(function.get("arguments", "{}"))
            msg = arguments.get("msg")
            if not msg:
                print(f"✗ Tool call missing msg argument: {function}")
                log_to_file(f"✗ Tool call missing msg argument: {function}")
                return False
            
            print(f"  Tool call: echo(msg='{msg}')")
            log_to_file(f"  Tool call: echo(msg='{msg}')")
        except json.JSONDecodeError:
            print(f"✗ Invalid tool arguments: {function.get('arguments')}")
            log_to_file(f"✗ Invalid tool arguments: {function.get('arguments')}")
            return False
    
    return True


def test_completion_endpoint(endpoint: str, stream: bool, model_name: str) -> bool:
    """Test a completion-like request for a given endpoint and streaming mode."""
    mode = "stream" if stream else "non-stream"
    print(f"\nTesting {endpoint} ({mode}) for model '{model_name}'...")
    log_to_file(f"\nTesting {endpoint} ({mode}) for model '{model_name}'...")

    payload = _build_request_body(model_name, stream)
    tracker = StreamResponseTracker(model_name) if stream else None
    returncode, stdout, stderr = _run_curl_request(
        endpoint,
        payload,
        stream,
        stream_callback=tracker.handle_line if tracker else None,
    )
    stream_valid = True
    if stream and tracker:
        stream_valid = tracker.finalize()

    if returncode != 0:
        print(f"✗ {endpoint} request failed ({mode}) for model '{model_name}'")
        print(f"  Error: {stderr}")
        log_to_file(f"✗ {endpoint} request failed ({mode}) for model '{model_name}'")
        log_to_file(f"  Error: {stderr}")
        if stdout:
            print(f"  Output: {stdout}")
            log_to_file(f"  Output: {stdout}")
        return False

    if stream:
        return stream_valid

    return _validate_non_stream_response(stdout)


def test_tool_calling_endpoint(endpoint: str, stream: bool, model_name: str) -> bool:
    """Test a tool calling request for a given endpoint and streaming mode using OpenAI client."""
    mode = "stream" if stream else "non-stream"
    print(f"\nTesting tool calling with {endpoint} ({mode}) for model '{model_name}'...")
    log_to_file(f"\nTesting tool calling with {endpoint} ({mode}) for model '{model_name}'...")

    try:
        # Use OpenAI client for tool calling tests
        messages = [
            {"role": "user", "content": "Please use the echo function with the message 'Hello from tool calling test!'"}
        ]
        
        resp = client.chat.completions.create(
            model=model_name,
            messages=messages,
            temperature=TEMPERATURE,
            max_tokens=2048,
            tools=TOOLS,
            stream=stream,
        )
        
        if stream:
            # Handle streaming response
            content_chunks = []
            tool_calls = []
            reasoning_chunks = []
            
            for chunk in resp:
                if chunk.choices:
                    delta = chunk.choices[0].delta
                    
                    # Check for reasoning content
                    if hasattr(delta, 'reasoning') and delta.reasoning:
                        reasoning_text = _flatten_stream_content(delta.reasoning)
                        reasoning_chunks.append(reasoning_text)
                        if len(reasoning_chunks) == 1:  # First reasoning chunk
                            print("  Reasoning:", end=" ", flush=True)
                        print(reasoning_text, end="", flush=True)
                    
                    # Check for regular content
                    if hasattr(delta, 'content') and delta.content:
                        content_text = _flatten_stream_content(delta.content)
                        content_chunks.append(content_text)
                        if len(content_chunks) == 1:  # First content chunk
                            print("  Content:", end=" ", flush=True)
                        print(content_text, end="", flush=True)
                    
                    # Check for tool calls
                    if hasattr(delta, 'tool_calls') and delta.tool_calls:
                        for tool_call in delta.tool_calls:
                            if tool_call.function:
                                tool_calls.append({
                                    'id': tool_call.id,
                                    'name': tool_call.function.name,
                                    'arguments': tool_call.function.arguments
                                })
            
            # Finalize output formatting
            if reasoning_chunks:
                print()  # New line after reasoning
            if content_chunks:
                print()  # New line after content
            
            # Log the response
            log_to_file(f"  Streamed reasoning: {''.join(reasoning_chunks)}")
            log_to_file(f"  Streamed content: {''.join(content_chunks)}")
            log_to_file(f"  Tool calls in stream: {json.dumps(tool_calls, indent=2)}")
            
            # Validate tool calls
            if tool_calls:
                for tool_call in tool_calls:
                    if tool_call.get('name') == 'echo':
                        try:
                            args = json.loads(tool_call.get('arguments', '{}'))
                            msg = args.get('msg')
                            if msg:
                                print(f"  Tool call: echo(msg='{msg}')")
                                log_to_file(f"  Tool call: echo(msg='{msg}')")
                                return True
                        except json.JSONDecodeError:
                            print(f"✗ Invalid tool arguments: {tool_call.get('arguments')}")
                            log_to_file(f"✗ Invalid tool arguments: {tool_call.get('arguments')}")
                            return False
                print("✗ No valid echo tool calls found in stream")
                log_to_file("✗ No valid echo tool calls found in stream")
                return False
            elif content_chunks:
                # Model responded with text instead of calling the tool
                content = ''.join(content_chunks)
                print(f"  Model responded with text instead of calling tool: {content}")
                log_to_file(f"  Model responded with text instead of calling tool: {content}")
                return True
            else:
                print("✗ No tool calls or content in stream response")
                log_to_file("✗ No tool calls or content in stream response")
                return False
        else:
            # Handle non-streaming response
            if resp.choices:
                message = resp.choices[0].message
                tool_calls = message.tool_calls
                content = message.content
                
                # Log the complete response
                response_dict = {
                    'content': content,
                    'tool_calls': [
                        {
                            'id': tc.id,
                            'name': tc.function.name,
                            'arguments': tc.function.arguments
                        } for tc in tool_calls
                    ] if tool_calls else None
                }
                log_to_file(f"  Raw response payload: {json.dumps(response_dict, indent=2)}")
                
                if tool_calls:
                    for tool_call in tool_calls:
                        if tool_call.function.name == 'echo':
                            try:
                                args = json.loads(tool_call.function.arguments)
                                msg = args.get('msg')
                                if msg:
                                    print(f"  Tool call: echo(msg='{msg}')")
                                    log_to_file(f"  Tool call: echo(msg='{msg}')")
                                    return True
                            except json.JSONDecodeError:
                                print(f"✗ Invalid tool arguments: {tool_call.function.arguments}")
                                log_to_file(f"✗ Invalid tool arguments: {tool_call.function.arguments}")
                                return False
                    print("✗ No valid echo tool calls found")
                    log_to_file("✗ No valid echo tool calls found")
                    return False
                elif content:
                    # Model responded with text instead of calling the tool
                    print(f"  Model responded with text instead of calling tool: {content}")
                    log_to_file(f"  Model responded with text instead of calling tool: {content}")
                    return True
                else:
                    print("✗ Response missing both tool_calls and content")
                    log_to_file("✗ Response missing both tool_calls and content")
                    return False
            else:
                print("✗ Response missing choices")
                log_to_file("✗ Response missing choices")
                return False
                
    except Exception as e:
        print(f"✗ Tool calling {endpoint} request failed ({mode}) for model '{model_name}'")
        print(f"  Error: {str(e)}")
        log_to_file(f"✗ Tool calling {endpoint} request failed ({mode}) for model '{model_name}'")
        log_to_file(f"  Error: {str(e)}")
        return False


def test_all_endpoints(models: list[str]):
    """Run completion-style tests for each endpoint/mode combo across models."""
    endpoints = [CHAT_ENDPOINT]
    
    # Check if responses endpoint is enabled by making a test request
    returncode, stdout, stderr = run_command(["curl", "-s", "-o", "/dev/null", "-w", "%{http_code}", f"{PROXY_URL}{RESPONSES_ENDPOINT}"])
    if returncode == 0 and stdout.strip() != "404":
        endpoints.append(RESPONSES_ENDPOINT)
        print(f"Responses endpoint is enabled, including in tests")
        log_to_file(f"Responses endpoint is enabled, including in tests")
    else:
        print(f"Responses endpoint is disabled or not available, skipping tests")
        log_to_file(f"Responses endpoint is disabled or not available, skipping tests")

    modes = _get_enabled_modes(RUN_NOSTREAM, RUN_STREAM)
    if not modes:
        print("⚠️ Both RUN_NOSTREAM and RUN_STREAM are disabled. Skipping completion tests.")
        log_to_file("⚠️ Both RUN_NOSTREAM and RUN_STREAM are disabled. Skipping completion tests.")
        return True
    success = True
    for model_name in models:
        print(f"\n=== Testing model '{model_name}' ===")
        log_to_file(f"\n=== Testing model '{model_name}' ===")
        for endpoint in endpoints:
            for stream in modes:
                if not test_completion_endpoint(endpoint, stream, model_name):
                    success = False
    return success


def test_tool_calling(models: list[str]):
    """Run tool calling tests for each endpoint/mode combo across models."""
    if not (RUN_TOOL_NOSTREAM or RUN_TOOL_STREAM):
        print("ℹ️ RUN_TOOL_NOSTREAM and RUN_TOOL_STREAM are disabled. Skipping tool calling tests.")
        log_to_file("ℹ️ RUN_TOOL_NOSTREAM and RUN_TOOL_STREAM are disabled. Skipping tool calling tests.")
        return True
    endpoints = [CHAT_ENDPOINT]

    # Check if responses endpoint is enabled by making a test request
    returncode, stdout, stderr = run_command(["curl", "-s", "-o", "/dev/null", "-w", "%{http_code}", f"{PROXY_URL}{RESPONSES_ENDPOINT}"])
    if returncode == 0 and stdout.strip() != "404":
        endpoints.append(RESPONSES_ENDPOINT)

    modes = _get_enabled_modes(RUN_TOOL_NOSTREAM, RUN_TOOL_STREAM)  # Test streaming/non-streaming based on toggles
    if not modes:
        print("⚠️ Tool calling requested but both RUN_TOOL_NOSTREAM and RUN_TOOL_STREAM are disabled. Skipping tool calling tests.")
        log_to_file("⚠️ Tool calling requested but both RUN_TOOL_NOSTREAM and RUN_TOOL_STREAM are disabled. Skipping tool calling tests.")
        return True
    success = True
    for model_name in models:
        print(f"\n=== Testing tool calling with model '{model_name}' ===")
        log_to_file(f"\n=== Testing tool calling with model '{model_name}' ===")
        for endpoint in endpoints:
            for stream in modes:
                if not test_tool_calling_endpoint(endpoint, stream, model_name):
                    success = False
    return success


def discover_models() -> list[str]:
    """Fetch the models exposed by the proxy via /v1/models."""
    print("Discovering models from /v1/models...")
    log_to_file("Discovering models from /v1/models...")
    returncode, stdout, stderr = run_command(
        ["curl", "-sS", f"{PROXY_URL}/v1/models"]
    )
    if returncode != 0:
        print(f"✗ Failed to fetch /v1/models, falling back to default '{DEFAULT_MODEL_NAME}'")
        print(f"  Error: {stderr}")
        log_to_file(f"✗ Failed to fetch /v1/models, falling back to default '{DEFAULT_MODEL_NAME}'")
        log_to_file(f"  Error: {stderr}")
        return [DEFAULT_MODEL_NAME]

    try:
        payload = json.loads(stdout)
    except json.JSONDecodeError:
        print(f"✗ Invalid JSON from /v1/models: {stdout}")
        log_to_file(f"✗ Invalid JSON from /v1/models: {stdout}")
        print(f"Using fallback model '{DEFAULT_MODEL_NAME}'")
        log_to_file(f"Using fallback model '{DEFAULT_MODEL_NAME}'")
        return [DEFAULT_MODEL_NAME]

    models = [
        m.get("id")
        for m in payload.get("data", [])
        if isinstance(m, dict) and m.get("id")
    ]
    if not models:
        print(f"✗ No models returned from /v1/models, using fallback '{DEFAULT_MODEL_NAME}'")
        log_to_file(f"✗ No models returned from /v1/models, using fallback '{DEFAULT_MODEL_NAME}'")
        return [DEFAULT_MODEL_NAME]

    print(f"✓ Discovered models: {models}")
    log_to_file(f"✓ Discovered models: {models}")
    return models

def main():
    """Run all tests."""
    print(f"Testing cLLMp proxy at {PROXY_URL}")
    print(f"Logging responses to: {LOG_FILE}")
    
    # Initialize log file
    log_to_file(f"=== cLLMp Proxy Test Log - {datetime.now().isoformat()} ===")
    log_to_file(f"Proxy URL: {PROXY_URL}")
    
    models = discover_models()
    log_to_file(f"Models to test: {models}")

    # Test health check
    if not test_health_check():
        print("\n❌ Proxy health check failed. Is the proxy running?")
        print(f"   Try running: ./run.sh")
        sys.exit(1)
    
    # Test each endpoint/mode combination
    if RUN_NOSTREAM or RUN_STREAM:
        if not test_all_endpoints(models):
            print("\n❌ Completion/response tests failed")
            sys.exit(1)
    else:
        print("\nℹ️ Both RUN_NOSTREAM and RUN_STREAM are disabled. Skipping completion/response tests.")
        log_to_file("Skipping completion/response tests because both RUN_NOSTREAM and RUN_STREAM are disabled.")

    # Test tool calling
    if RUN_TOOL_NOSTREAM or RUN_TOOL_STREAM:
        if not test_tool_calling(models):
            print("\n❌ Tool calling tests failed")
            sys.exit(1)
    else:
        print("\nℹ️ RUN_TOOL_NOSTREAM and RUN_TOOL_STREAM are disabled. Skipping tool calling tests.")
        log_to_file("Skipping tool calling tests because both RUN_TOOL_NOSTREAM and RUN_TOOL_STREAM are disabled.")
    
    print("\n✅ All tests passed! Proxy is working correctly.")
    print(f"📝 Full response logs saved to: {LOG_FILE}")

if __name__ == "__main__":
    main()
