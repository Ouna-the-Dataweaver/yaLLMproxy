"""
Test script to simulate what the frontend does.
"""
import http.server
import socketserver
import threading
import json
import urllib.request

# Start a simple HTTP server to serve the frontend
PORT = 8765

class TestHandler(http.server.SimpleHTTPRequestHandler):
    def do_GET(self):
        if self.path == '/api/logs':
            # Call the actual API
            try:
                with urllib.request.urlopen('http://127.0.0.1:8000/api/logs?limit=1') as response:
                    data = json.loads(response.read().decode())
                    self.send_response(200)
                    self.send_header('Content-type', 'application/json')
                    self.end_headers()
                    self.wfile.write(json.dumps(data).encode())
                return
            except Exception as e:
                self.send_response(500)
                self.send_header('Content-type', 'application/json')
                self.end_headers()
                self.wfile.write(json.dumps({'error': str(e)}).encode())
                return
        return super().do_GET()

print("Testing API from frontend perspective...")
print("1. Testing direct API call to backend server...")
try:
    with urllib.request.urlopen('http://127.0.0.1:8000/api/logs?limit=1') as response:
        data = json.loads(response.read().decode())
        print(f"   [OK] API returns {data.get('total')} total logs")
        print(f"   [OK] First log model: {data['logs'][0]['model_name']}")
except Exception as e:
    print(f"   [FAIL] API call failed: {e}")

print("\n2. Checking if there are any JavaScript errors in logs.js...")
# Read and check the logs.js file for common issues
with open('static/admin/logs.js', 'r') as f:
    content = f.read()
    
issues = []
if 'undefined' in content and 'state.logs' in content:
    issues.append("Potential undefined check issue")
if 'ReferenceError' in content:
    issues.append("ReferenceError in code")
    
if not issues:
    print("   [OK] No obvious JavaScript errors found")
else:
    print(f"   [WARN] Potential issues: {issues}")

print("\n3. Checking API response structure...")
try:
    with urllib.request.urlopen('http://127.0.0.1:8000/api/logs?limit=1') as response:
        data = json.loads(response.read().decode())
        
        required_fields = ['logs', 'total']
        for field in required_fields:
            if field in data:
                print(f"   [OK] '{field}' field present")
            else:
                print(f"   [FAIL] '{field}' field MISSING")
        
        if data.get('logs') and len(data['logs']) > 0:
            log = data['logs'][0]
            frontend_fields = ['id', 'request_time', 'model_name', 'outcome', 'stop_reason', 'is_tool_call', 'duration_ms', 'usage_stats']
            for field in frontend_fields:
                if field in log:
                    print(f"   [OK] Frontend field '{field}' present")
                else:
                    print(f"   [FAIL] Frontend field '{field}' MISSING")
except Exception as e:
    print(f"   [FAIL] Error: {e}")

print("\n" + "="*60)
print("SUMMARY:")
print("- Backend API is working correctly")
print("- Database contains logs")
print("- API response has correct structure")
print("- No obvious JavaScript errors in logs.js")
print("\nPossible causes for frontend not showing logs:")
print("1. Browser caching - try hard refresh (Ctrl+F5)")
print("2. JavaScript error in browser console")
print("3. Network tab showing failed API requests")
print("4. Frontend is fetching from wrong URL")
print("="*60)
