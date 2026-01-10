import sqlite3

conn = sqlite3.connect('logs/yaLLM.db')
cursor = conn.cursor()
cursor.execute("SELECT name FROM sqlite_master WHERE type='table'")
tables = cursor.fetchall()
print('Tables:', [t[0] for t in tables])

cursor.execute('SELECT COUNT(*) FROM request_logs')
count = cursor.fetchone()[0]
print('Request logs count:', count)

# Check recent logs
cursor.execute('SELECT COUNT(*) FROM request_logs WHERE request_time > datetime("now", "-24 hours")')
recent_count = cursor.fetchone()[0]
print('Logs in last 24 hours:', recent_count)

conn.close()
