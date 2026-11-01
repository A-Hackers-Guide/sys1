#!/bin/sh
# Upgrade test: runs a node and a client from an earlier release, deploys
# an app, then starts this commit's binaries on the same data and checks
# that the pool is intact: same identity and join code, the machine back
# online, the app's record kept.
#   ci-upgrade.sh <old tag> <dir with new sys0 and sys1>
set -eu
OLD=$1 NEW=$(cd "$2" && pwd)
V=${OLD#v}
work=$(mktemp -d); cd "$work"
# Absolute paths: releases before v0.8.0 passed a relative data folder to
# Docker, which refuses it.
gh release download "$OLD" -R A-Hackers-Guide/sys1 -p "sys1_${V}_linux_amd64.tar.gz"
mkdir old && tar -xzf "sys1_${V}_linux_amd64.tar.gz" -C old

# Releases before v0.8.0 take the dashboard key as a cookie, later ones as
# a header; send both.
state() {
  curl -s -c jar -o /dev/null "http://127.0.0.1:7700/?key=$(cat node/dashboard.key)"
  curl -s -b jar -H "Authorization: Bearer $(cat node/dashboard.key)" http://127.0.0.1:7700/api/state
}
start() { # <bin dir>
  "$1/sys1" serve --no-open --foreground --no-local --data "$work/node" --listen 127.0.0.1:7443 --gui 127.0.0.1:7700 >node.log 2>&1 &
  echo $! > node.pid
  for i in $(seq 1 60); do state >/dev/null 2>&1 && [ -s node/join-code.txt ] && break; sleep 1; done
  [ -f client/config.json ] || "$1/sys0" join "$(cat node/join-code.txt)" --data "$work/client" --name upgrade-test --trust owner --no-service
  "$1/sys0" run --data "$work/client" >client.log 2>&1 &
  echo $! > client.pid
}
stop() { kill "$(cat client.pid)" "$(cat node.pid)"; sleep 3; }
wait_for() { # <python condition on s> <what>
  for i in $(seq 1 90); do
    state > s.json 2>/dev/null || true
    python3 -c "import json,sys; s=json.load(open('s.json')); sys.exit(0 if ($1) else 1)" 2>/dev/null && return 0
    sleep 2
  done
  echo "timed out waiting for: $2"; cat s.json; tail -n 20 node.log client.log; exit 1
}

echo "== $OLD"
start old
wait_for "any(a['name']=='upgrade-test' and a['online'] for a in s['agents'])" "the old client online"
curl -s -b jar -H "Authorization: Bearer $(cat node/dashboard.key)" -H 'Content-Type: application/json' -H 'X-Sys1: 1' -d '{"package":"hello"}' http://127.0.0.1:7700/api/deploy
wait_for "any(t['package']=='hello' and t['state']=='done' for t in s['tasks'])" "hello to finish"
python3 -c "import json; s=json.load(open('s.json')); print(s['fingerprint'], s['joinCode'])" > before.txt
stop

echo "== this commit"
start "$NEW"
wait_for "any(a['name']=='upgrade-test' and a['online'] and a['version']!='$V' for a in s['agents'])" "the upgraded client online"
wait_for "any(t['package']=='hello' and t['state']=='done' for t in s['tasks'])" "the app's record after upgrading"
python3 -c "import json; s=json.load(open('s.json')); print(s['fingerprint'], s['joinCode'])" > after.txt
diff before.txt after.txt || { echo "the node's identity or join code changed"; exit 1; }
stop
echo "upgrade from $OLD: ok"
