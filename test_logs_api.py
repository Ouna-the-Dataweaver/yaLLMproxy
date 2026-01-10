"""
Test script to verify the logs API and frontend rendering.
"""
import json
import urllib.request

def test_api():
    """Test the logs API endpoint."""
    url = "http://127.0.0.1:8000/api/logs?limit=1"
    with urllib.request.urlopen(url) as response:
        data = json.loads(response.read().decode())
        
        print("API Response Structure:")
        print(f"  Total logs: {data.get('total')}")
        print(f"  Logs returned: {len(data.get('logs', []))}")
        print(f"  Has 'logs' key: {'logs' in data}")
        print(f"  Has 'total' key: {'total' in data}")
        
        if data.get('logs'):
            log = data['logs'][0]
            print("\nFirst log fields:")
            for key, value in log.items():
                if key not in ['body', 'full_response', 'stream_chunks', 'headers', 'route', 'backend_attempts', 'errors', 'usage_stats']:
                    print(f"  {key}: {value}")
            
            print("\nRequired frontend fields:")
            required_fields = ['id', 'request_time', 'model_name', 'outcome', 'stop_reason', 'is_tool_call', 'duration_ms', 'usage_stats']
            for field in required_fields:
                exists = field in log
                print(f"  {field}: {'✓' if exists else '✗'}")
        
        return data

if __name__ == "__main__":
    test_api()
