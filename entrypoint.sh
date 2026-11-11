#!/bin/sh
# Starts as root only to repair /data ownership (a bind mount Docker
# created belongs to root), then runs Jidoka as uid 10001. The binary's
# file capabilities let it start each job as uid 10002 (Dockerfile).
set -e

if [ "$(id -u)" != "0" ]; then
	exec jidoka "$@"
fi

# Only touch what is not ours yet. Jidoka owns /data, state/ and tmp/
# and creates one folder per job or run in them, handed to the jobs user
# (runs.prepare); the uv cache and HOME belong to the jobs user.
#
#   /data            10001 0711   jobs pass through, list nothing
#   /data/state      10001 0711   /data/state/<job>   10002 (J22)
#   /data/tmp        10001 0711   /data/tmp/run-<id>  10002
#   /data/uv-cache   10002        /data/home          10002
mkdir -p "$DATA_DIR/state" "$DATA_DIR/tmp" "$DATA_DIR/uv-cache" "$DATA_DIR/home"
find "$DATA_DIR" -mindepth 1 -maxdepth 1 \( -name uv-cache -o -name home \) -prune \
	-o -path "$DATA_DIR/state/*" -prune -o -path "$DATA_DIR/tmp/*" -prune \
	-o ! -user 10001 -exec chown 10001:10001 {} +
chown -R 10002:10002 "$DATA_DIR/uv-cache" "$DATA_DIR/home"
chmod 711 "$DATA_DIR" "$DATA_DIR/state" "$DATA_DIR/tmp"

# Jidoka creates its own files 0600.
umask 077

exec su-exec 10001:10001 jidoka "$@"
