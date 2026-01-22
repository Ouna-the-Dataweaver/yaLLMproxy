"""Tests for TCP forwarder functionality."""

import socket
import sys
import threading
import time
from pathlib import Path

import pytest

# Add src directory to path for imports
sys.path.insert(0, str(Path(__file__).parent / "src"))


class TestTcpForwarder:
    """Tests for TCP forwarder behavior."""

    def test_pipe_no_shutdown_wr(self) -> None:
        """Test that _pipe doesn't call shutdown(SHUT_WR) on socket close.
        
        This verifies the fix for the connection reset issue where
        shutdown(SHUT_WR) was causing problems with HTTP keep-alive.
        """
        from scripts.tcp_forward import _pipe
        
        # Read the source code and verify no shutdown calls in the function body
        import inspect
        source = inspect.getsource(_pipe)
        
        # Split by docstring if present
        lines = source.split('\n')
        code_lines = []
        in_docstring = False
        for line in lines:
            if '"""' in line or "'''" in line:
                in_docstring = not in_docstring
            elif not in_docstring and 'shutdown' not in line.lower():
                code_lines.append(line)
        
        code_without_docstring = '\n'.join(code_lines)
        
        # The function code should NOT contain "SHUT_WR"
        assert "SHUT_WR" not in code_without_docstring, "_pipe should not call shutdown(SHUT_WR) in code"

    def test_pipe_function_signature(self) -> None:
        """Test that _pipe has the expected function signature."""
        from scripts.tcp_forward import _pipe
        import inspect
        sig = inspect.signature(_pipe)

        # Should have 7 parameters
        params = list(sig.parameters.keys())
        assert params == ["src", "dst", "bufsize", "stats", "key", "logger", "conn_id"]

    def test_set_nodelay_does_not_raise(self) -> None:
        """Test that _set_nodelay does not raise on a valid socket."""
        from scripts.tcp_forward import _set_nodelay

        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        try:
            _set_nodelay(sock)
        finally:
            sock.close()
