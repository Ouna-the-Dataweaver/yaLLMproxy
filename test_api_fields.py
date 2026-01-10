import json
import urllib.request

url = "http://127.0.0.1:8000/api/logs?limit=1"
with urllib.request.urlopen(url) as response:
    data = json.loads(response.read().decode('utf-8', errors='replace'))
    
    if data.get('logs'):
        log = data['logs'][0]
        print("Fields in response:")
        for key in sorted(log.keys()):
            val = log[key]
            if isinstance(val, str) and len(val) > 50:
                print(f"  {key}: [{len(val)} chars]")
            elif isinstance(val, list) and len(val) > 5:
                print(f"  {key}: [{len(val)} items]")
            elif isinstance(val, dict):
                print(f"  {key}: [dict with {len(val)} keys]")
            elif val is None:
                print(f"  {key}: None")
            else:
                print(f"  {key}: {str(val)[:50]}")
        
        print("\nChecking for specific fields:")
        large_fields = ['body', 'full_response', 'stream_chunks']
        for field in large_fields:
            if field in log:
                print(f"  [PRESENT] {field}")
            else:
                print(f"  [ABSENT] {field}")
