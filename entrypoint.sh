#!/bin/sh
# Starts as root only to repair /data ownership (a bind mount Docker
# created belongs to root), then runs Jidoka as uid 10001. The binary's
# file capabilities let it start each job as uid 10002 (Dockerfile).
set -e

if [ "$(id -u)" != "0" ]; then
	exec jidoka "$@"
fi

# Only touch what is not ours yet. Job state and the uv cache belong to
# the jobs user.
mkdir -p "$DATA_DIR/state" "$DATA_DIR/uv-cache" "$DATA_DIR/home"
find "$DATA_DIR" -path "$DATA_DIR/state" -prune -o -path "$DATA_DIR/uv-cache" -prune \
	-o -path "$DATA_DIR/home" -prune -o ! -user 10001 -exec chown 10001:10001 {} +
chown -R 10002:10002 "$DATA_DIR/state" "$DATA_DIR/uv-cache" "$DATA_DIR/home"

# Jobs may pass through /data to their folders but list or read nothing
# else; Jidoka creates its files 0600 (umask).
chmod 711 "$DATA_DIR"
umask 077

exec su-exec 10001:10001 jidoka "$@"
