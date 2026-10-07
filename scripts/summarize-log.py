"""Print safe gateway events and HTTP method/path/status from Cherry's private log."""
import re
import sys
from collections import Counter
from pathlib import Path
from urllib.parse import urlsplit

events = []
counts = Counter()
after = sys.argv[2] if len(sys.argv) > 2 else ""
before = sys.argv[3] if len(sys.argv) > 3 else ""
for line in Path(sys.argv[1]).open(encoding="utf-8", errors="replace"):
    stamp = line.split(" ", 2)
    when = stamp[1] if len(stamp) > 1 else "?"
    in_window = (not after or when >= after) and (not before or when <= before)
    if "OBS :10000 " in line:
        match = re.search(r"OBS :10000 (garden-login|garden-room-enter|garden-relay-start|data |direct |read |open-control|open-ack|client-event).*", line)
        if match and in_window:
            event = match.group(0).split(" remote=", 1)[0]
            fields = re.findall(r"\b(?:agent|msgid|bytes|result|map|status)=[^ ]+", line)
            events.append((when, event + " " + " ".join(fields)))
    if "REQ #" not in line:
        continue
    match = re.search(r'\bmethod=(\w+) url="([^"?#]*)[^\"]*" status=(\d+)', line)
    if not match:
        continue
    method, path, code = match.groups()
    path = urlsplit(path).path
    path = re.sub(r"[A-Za-z0-9_-]{24,}", "[id]", path)
    counts[(method, path, code)] += 1
    if in_window and (after or code.startswith(("4", "5")) or path in ("/v4/quest/status", "/v4/checkSession", "/v4/createSession")):
        events.append((when, f"HTTP {method} {path} status={code}"))

for when, event in (events if after else events[-120:]):
    print(when, event)
print("TOTAL ROUTES", len(counts), "RECENT EVENTS", len(events))
for (method, path, code), count in counts.most_common(25):
    print("COUNT", count, method, path, code)
print("ITEM_FILE_REQUESTS", sum(count for (_, path, _), count in counts.items() if path.startswith("/arts_item_")))
