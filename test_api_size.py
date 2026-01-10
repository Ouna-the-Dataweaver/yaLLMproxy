"""
Test the API response size and content.
"""
import json
import urllib.request

def test_api():
    url = "http://127.0.0.1:8000/api/logs?limit=5"
    with urllib.request.urlopen(url) as response:
        data = response.read().decode('utf-8', errors='replace')
        
        print(f"Response size: {len(data):,} characters")
        print(f"Response size: {len(data) / 1024:.2f} KB")
        
        # Parse the JSON
        parsed = json.loads(data)
        
        print(f"\nTotal logs: {parsed.get('total', 'N/A')}")
        print(f"Logs returned: {len(parsed.get('logs', []))}")
        
        if parsed.get('logs'):
            log = parsed['logs'][0]
            print(f"\nFirst log fields ({len(log)} total):")
            for key, value in log.items():
                if isinstance(value, str) and len(value) > 100:
                    print(f"  {key}: [{len(value):,} chars - TRUNCATED]")
                elif isinstance(value, list) and len(value) > 10:
                    print(f"  {key}: [{len(value)} items]")
                elif value is not None:
                    print(f"  {key}: {str(value)[:80]}")
                else:
                    print(f"  {key}: None")
            
            # Check for large fields
            large_fields = ['full_response', 'stream_chunks']
            found_large = [f for f in large_fields if f in log]
            if found_large:
                print(f"\nWARNING: Large fields still present: {found_large}")
            else:
                print(f"\nSUCCESS: Large fields excluded from list response")

if __name__ == "__main__":
    test_api()
