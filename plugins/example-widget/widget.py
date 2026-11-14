#!/usr/bin/env python3
"""
Example Tash Plugin - System Stats Widget
Displays CPU, memory, and disk usage in a dashboard widget.
"""

import json
import sys
import time
import psutil
import os

def send_message(msg):
    """Send a JSON message to Tash via stdout."""
    sys.stdout.write(json.dumps(msg) + "\n")
    sys.stdout.flush()

def log(level, message):
    send_message({"type": "log", "level": level, "message": message})

def request(action, **kwargs):
    """Send a request to Tash API and wait for response."""
    req_id = str(time.time())
    send_message({"type": "request", "id": req_id, "action": action, **kwargs})
    # In a real plugin, you'd wait for response on stdin
    # For this example, we'll just fire and forget

def main():
    plugin_name = os.environ.get("TASH_PLUGIN_NAME", "example-widget")
    log("info", f"Starting {plugin_name}")

    # Register widget
    widget_spec = {
        "id": "system-stats",
        "title": "System Stats",
        "kind": "text",
        "position": "overview",
        "size": "medium",
        "refresh": 5,
        "content": "Loading..."
    }
    request("add_widget", widget=widget_spec)

    # Main loop
    while True:
        try:
            cpu = psutil.cpu_percent(interval=1)
            mem = psutil.virtual_memory()
            disk = psutil.disk_usage('/')

            content = f"""CPU: {cpu:.1f}%
Memory: {mem.percent:.1f}% ({mem.used//1024//1024}MB / {mem.total//1024//1024}MB)
Disk: {disk.percent:.1f}% ({disk.used//1024//1024//1024}GB / {disk.total//1024//1024//1024}GB)"""

            # Update widget content via event
            send_message({
                "type": "event",
                "event": "widget_update",
                "data": {
                    "widget_id": "system-stats",
                    "content": content
                }
            })

            time.sleep(5)
        except KeyboardInterrupt:
            break
        except Exception as e:
            log("error", str(e))
            time.sleep(5)

    log("info", "Plugin stopped")

if __name__ == "__main__":
    main()