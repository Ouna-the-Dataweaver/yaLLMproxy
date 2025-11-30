#!/usr/bin/env python3
"""
Tests for sensitive data masking in logs.

This module tests the _safe_json_dict method to ensure that sensitive information
like API keys, proxy hosts, and authorization tokens are properly masked in logs.
"""

import unittest
import json
from unittest.mock import patch, MagicMock
import sys
from pathlib import Path

# Add the parent directory to the path to import the proxy module
sys.path.insert(0, str(Path(__file__).parent.parent))

from proxy import RequestLogRecorder


class TestDataMasking(unittest.TestCase):
    """Test cases for sensitive data masking in logging."""

    def setUp(self):
        """Set up test fixtures."""
        self.recorder = RequestLogRecorder("test-model", False, "/test/path")

    def test_normal_headers_not_masked(self):
        """Test that normal headers are not modified."""
        headers = {
            "accept": "application/json",
            "content-type": "application/json",
            "user-agent": "test-client"
        }
        
        result = self.recorder._safe_json_dict(headers)
        result_dict = json.loads(result)
        
        # These headers should remain unchanged
        self.assertEqual(result_dict["accept"], "application/json")
        self.assertEqual(result_dict["content-type"], "application/json")
        self.assertEqual(result_dict["user-agent"], "test-client")

    def test_authorization_bearer_token_masked(self):
        """Test that Bearer tokens are properly masked."""
        headers = {
            "authorization": "Bearer sk-1234567890abcdef"
        }
        
        result = self.recorder._safe_json_dict(headers)
        result_dict = json.loads(result)
        
        # Should mask to first 3 characters + ****
        self.assertEqual(result_dict["authorization"], "Bearer sk-****")

    def test_authorization_empty_token_preserved(self):
        """Test that empty Bearer tokens are preserved as-is."""
        headers = {
            "authorization": "Bearer empty"
        }
        
        result = self.recorder._safe_json_dict(headers)
        result_dict = json.loads(result)
        
        # Empty tokens should be preserved
        self.assertEqual(result_dict["authorization"], "Bearer empty")

    def test_authorization_short_token_masked(self):
        """Test that short tokens are properly masked."""
        headers = {
            "authorization": "Bearer abc"
        }
        
        result = self.recorder._safe_json_dict(headers)
        result_dict = json.loads(result)
        
        # Short tokens should be masked appropriately
        self.assertEqual(result_dict["authorization"], "Bearer abc****")

    def test_host_replaced_with_proxy_host(self):
        """Test that host addresses are replaced with proxy_host."""
        headers = {
            "host": "188.242.65.1:6969"
        }
        
        result = self.recorder._safe_json_dict(headers)
        result_dict = json.loads(result)
        
        # Host should be replaced with proxy_host
        self.assertEqual(result_dict["host"], "proxy_host")

    def test_multiple_host_formats(self):
        """Test different host address formats."""
        test_hosts = [
            "192.168.1.1:8080",
            "localhost:3000",
            "example.com:443",
            "proxy.internal:3128"
        ]
        
        for host in test_hosts:
            with self.subTest(host=host):
                headers = {"host": host}
                result = self.recorder._safe_json_dict(headers)
                result_dict = json.loads(result)
                
                # All hosts should be replaced with proxy_host
                self.assertEqual(result_dict["host"], "proxy_host")

    def test_proxy_connection_masked(self):
        """Test that proxy-connection headers are masked."""
        headers = {
            "proxy-connection": "close"
        }
        
        result = self.recorder._safe_json_dict(headers)
        result_dict = json.loads(result)
        
        # Should mask to first 3 characters + ****
        self.assertEqual(result_dict["proxy-connection"], "clo****")

    def test_case_insensitive_header_matching(self):
        """Test that header matching is case-insensitive."""
        headers = {
            "Authorization": "Bearer secret-key",
            "HOST": "example.com:8080",
            "Proxy-Connection": "keep-alive"
        }
        
        result = self.recorder._safe_json_dict(headers)
        result_dict = json.loads(result)
        
        # All should be masked regardless of case
        self.assertEqual(result_dict["Authorization"], "Bearer sec****")
        self.assertEqual(result_dict["HOST"], "proxy_host")
        self.assertEqual(result_dict["Proxy-Connection"], "kee****")

    def test_real_world_headers_example(self):
        """Test with the real-world example from the issue."""
        headers = {
            "accept": "application/json, text/plain, */*",
            "accept-encoding": "gzip, compress, deflate, br",
            "authorization": "Bearer sk-1234567890abcdef",
            "connection": "close",
            "content-length": "181803",
            "content-type": "application/json",
            "host": "188.242.65.1:6969",
            "proxy-connection": "close",
            "user-agent": "axios/1.13.2"
        }
        
        result = self.recorder._safe_json_dict(headers)
        result_dict = json.loads(result)
        
        # Verify all sensitive fields are properly masked
        expected = {
            "accept": "application/json, text/plain, */*",
            "accept-encoding": "gzip, compress, deflate, br",
            "authorization": "Bearer sk-****",
            "connection": "close",
            "content-length": "181803",
            "content-type": "application/json",
            "host": "proxy_host",
            "proxy-connection": "clo****",
            "user-agent": "axios/1.13.2"
        }
        
        self.assertEqual(result_dict, expected)

    def test_mixed_case_sensitive_headers(self):
        """Test mixed case sensitive headers."""
        headers = {
            "Authorization": "Bearer token123456",
            "PROXY-CONNECTION": "upgrade",
            "Host": "192.168.1.100:9000"
        }
        
        result = self.recorder._safe_json_dict(headers)
        result_dict = json.loads(result)
        
        # All sensitive headers should be masked regardless of case
        self.assertEqual(result_dict["Authorization"], "Bearer tok****")
        self.assertEqual(result_dict["PROXY-CONNECTION"], "upg****")
        self.assertEqual(result_dict["Host"], "proxy_host")

    def test_invalid_json_fallback(self):
        """Test that invalid input falls back to string representation."""
        # Test with non-dict input
        result = self.recorder._safe_json_dict("invalid input")
        self.assertEqual(result, "invalid input")
        
        # Test with dict containing non-string values
        headers = {"key": 123, "value": None}
        result = self.recorder._safe_json_dict(headers)
        result_dict = json.loads(result)
        self.assertEqual(result_dict["key"], "123")
        self.assertEqual(result_dict["value"], "None")

    def test_empty_headers(self):
        """Test with empty headers."""
        headers = {}
        result = self.recorder._safe_json_dict(headers)
        result_dict = json.loads(result)
        self.assertEqual(result_dict, {})

    def test_only_sensitive_headers(self):
        """Test with only sensitive headers."""
        headers = {
            "authorization": "Bearer my-secret-api-key",
            "host": "sensitive-host.com:8080",
            "proxy-connection": "keep-alive"
        }
        
        result = self.recorder._safe_json_dict(headers)
        result_dict = json.loads(result)
        
        expected = {
            "authorization": "Bearer my-****",
            "host": "proxy_host",
            "proxy-connection": "kee****"
        }
        
        self.assertEqual(result_dict, expected)


if __name__ == "__main__":
    unittest.main()
