#!/bin/sh
# Used by the Android workflow: asks the node's dashboard API whether an
# online Android machine has joined.
set -e
KEY=$(cat node/dashboard.key)
curl -s -c jar -o /dev/null "http://127.0.0.1:7700/?key=$KEY"
curl -s -b jar http://127.0.0.1:7700/api/state > state.json
python3 - <<'PY'
import json, sys
s = json.load(open("state.json"))
android = [a for a in s["agents"] if a["resources"]["os"] == "android"]
print("machines:", [(a["name"], a["resources"]["os"], a["online"]) for a in s["agents"]])
if not android or not android[0]["online"]:
    sys.exit("no online Android machine")
print("Android machine online:", android[0]["name"], android[0]["resources"]["arch"])
PY
