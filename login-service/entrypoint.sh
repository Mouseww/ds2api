#!/bin/sh
# Start a virtual X server for headed Chromium, then run the login service.
# We do this manually instead of `xvfb-run` because the Xvfb build shipped in
# the Playwright image does not signal readiness the way xvfb-run expects,
# which left xvfb-run stuck waiting and never launched node.
set -e

Xvfb :99 -screen 0 1280x1024x24 -nolisten tcp -ac &
XVFB_PID=$!

# Wait for the X socket to appear.
tries=0
while [ $tries -lt 20 ]; do
  if [ -S /tmp/.X11-unix/X99 ]; then
    break
  fi
  sleep 0.1
  tries=$((tries + 1))
done

export DISPLAY=:99
exec node /app/index.js
