#!/bin/sh
# Used by the Android and iOS workflows: asks the node's dashboard API
# whether an online machine of the given OS has joined.
#   ci-check-machine.sh <os>
set -e
OS=$1
KEY=$(cat node/dashboard.key)
curl -s -c jar -o /dev/null "http://127.0.0.1:7700/?key=$KEY"
curl -s -b jar http://127.0.0.1:7700/api/state > state.json
OS=$OS python3 - <<'PY'
import json, os, sys
want = os.environ["OS"]
s = json.load(open("state.json"))
print("machines:", [(a["name"], a["resources"]["os"], a["online"]) for a in s["agents"]])
found = [a for a in s["agents"] if a["resources"]["os"] == want and a["online"]]
if not found:
    sys.exit("no online %s machine" % want)
print(want, "machine online:", found[0]["name"], found[0]["resources"]["arch"])
PY
