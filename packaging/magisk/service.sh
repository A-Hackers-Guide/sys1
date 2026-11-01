#!/system/bin/sh
# Starts sys0 as a daemon once Android has booted, and restarts it if it
# ever exits. Nothing runs until the device has joined a pool:
#   su -c "sys0 join <code> --data /data/adb/sys0 --no-service"
DATA=/data/adb/sys0
until [ "$(getprop sys.boot_completed)" = "1" ]; do sleep 5; done
mkdir -p "$DATA"
while true; do
  if [ -f "$DATA/config.json" ]; then
    /system/bin/sys0 run --data "$DATA" >/dev/null 2>&1
  fi
  sleep 15
done &
