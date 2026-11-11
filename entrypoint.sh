#!/bin/sh
# Starts as root only to repair /data ownership (a bind mount Docker
# created belongs to root), then runs Jidoka as uid 10001. The binary's
# file capabilities let it start each job under its own uid (Dockerfile).
set -e

if [ "$(id -u)" != "0" ]; then
	exec jidoka "$@"
fi

# Only touch what is not ours yet. Jidoka owns /data, state/, home/ and
# tmp/ and creates one folder per job or run in them, handed to that
# job's own uid, mode 0700 (runs.prepare, Z3).
#
#   /data            10001 0711   jobs pass through, list nothing
#   /data/state      10001 0711   /data/state/<job>   <job uid> 0700 (J22)
#   /data/home       10001 0711   /data/home/<job>    <job uid> 0700, uv cache inside
#   /data/tmp        10001 0711   /data/tmp/run-<id>  <job uid> 0700
mkdir -p "$DATA_DIR/state" "$DATA_DIR/tmp" "$DATA_DIR/home"
rm -rf "$DATA_DIR/uv-cache"
find "$DATA_DIR" -maxdepth 1 ! -user 10001 -exec chown 10001:10001 {} +
chmod 711 "$DATA_DIR" "$DATA_DIR/state" "$DATA_DIR/home" "$DATA_DIR/tmp"

# Jidoka creates its own files 0600.
umask 077

exec su-exec 10001:10001 jidoka "$@"
