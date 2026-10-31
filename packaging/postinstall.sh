#!/bin/sh
# Runs after the .deb/.rpm/.apk/Arch/OpenWrt package installs.
# Nothing starts automatically: joining a pool needs a join code.
cat <<'MSG'

  sys1 is installed.

  To lend this machine to a pool:     sudo sys0 setup
                     (or headless):   sudo sys0 join <join code>
  To run a node on this machine:      sys1

  Docs: https://github.com/A-Hackers-Guide/sys1/blob/main/docs/GETTING-STARTED.md

MSG
