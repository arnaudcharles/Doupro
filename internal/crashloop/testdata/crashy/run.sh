#!/bin/sh
set -eu
if [ ! -f /state/first-crash ]; then
  touch /state/first-crash
  sleep 80
else
  sleep 15
fi
exit 1
