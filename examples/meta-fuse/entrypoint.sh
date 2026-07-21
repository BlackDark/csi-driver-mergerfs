#!/bin/sh
set -eu
# Force fake /dev/fuse so libfuse mount fails with EPERM and calls fusermount3-proxy
rm -f /dev/fuse 2>/dev/null || true
touch /dev/fuse

SOCK="${FUSERMOUNT3PROXY_FDPASSING_SOCKPATH:-}"
if [ -n "$SOCK" ]; then
  i=0
  while [ ! -S "$SOCK" ] && [ "$i" -lt 120 ]; do
    echo "waiting for fd-passing socket: $SOCK ($i/120)" >&2
    i=$((i + 1))
    sleep 1
  done
  if [ ! -S "$SOCK" ]; then
    echo "ERROR: fd-passing socket missing: $SOCK" >&2
    ls -la "$(dirname "$SOCK")" >&2 || true
    exit 1
  fi
  echo "fd-passing socket ready: $SOCK" >&2
fi
exec "$@"
