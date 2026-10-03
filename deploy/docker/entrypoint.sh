#!/bin/sh
set -eu
mkdir -p "$NYATUNNEL_HOME"
chown -R nyatunnel:nyatunnel "$NYATUNNEL_HOME"
chmod 700 "$NYATUNNEL_HOME"
exec su-exec nyatunnel /usr/local/bin/nyatunnel "$@"
