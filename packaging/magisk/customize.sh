# Magisk runs this while installing the module.
set_perm "$MODPATH/system/bin/sys0" 0 0 0755
set_perm "$MODPATH/service.sh" 0 0 0755
ui_print "- sys0 installed. Join a pool from a root shell:"
ui_print "    su -c 'sys0 join <code> --data /data/adb/sys0 --no-service'"
