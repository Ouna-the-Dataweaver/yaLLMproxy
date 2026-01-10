import json
import urllib.request

url = "http://127.0.0.1:8000/api/logs?limit=2"
with urllib.request.urlopen(url) as response:
    data = json.loads(response.read().decode())
    print("Total logs:", data.get("total"))
    print("Logs returned:", len(data.get("logs", [])))
    if data.get("logs"):
        print("First log model:", data["logs"][0].get("model_name"))
        print("First log id:", str(data["logs"][0].get("id"))[:8])
