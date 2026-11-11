#!/bin/sh
# Smoke test of the image: runs testdata/image/probe as a job and checks
# what a job may and may not do (Z3, J11, J22). Needs Docker.
#   scripts/image-test.sh [image]   (default: builds jidoka:test)
set -eu
cd "$(dirname "$0")/.."

image=${1:-jidoka:test}
if [ $# -eq 0 ]; then
	docker build -q -t "$image" . >/dev/null
fi

name=jidoka-image-test-$$
db=$(mktemp -d)
trap 'docker rm -f "$name" >/dev/null 2>&1; rm -rf "$db"' EXIT

docker run -d --name "$name" -e MASTER_KEY=image-test-only \
	-v "$PWD/testdata/image:/jobs:ro" "$image" >/dev/null

# The probe and the holder run every minute; wait for the probe's first run.
for _ in $(seq 1 90); do
	sleep 2
	docker exec "$name" sh -c 'ls /data/state/probe/probe' >/dev/null 2>&1 && break
done
sleep 2
docker cp "$name:/data/jidoka.db" "$db/j.db" >/dev/null
docker cp "$name:/data/jidoka.db-wal" "$db/j.db-wal" >/dev/null 2>&1 || true

out=$(python3 - "$db/j.db" <<'PY'
import sqlite3, sys
c = sqlite3.connect(sys.argv[1])
row = c.execute("select state, error, output from runs where job='probe' order by id limit 1").fetchone()
print(row[0] if row else "none")
print(row[1] if row else "")
print(row[2] if row else "")
PY
)
echo "$out"

fail=0
expect_re() {
	if ! printf '%s\n' "$out" | grep -qxE "$1"; then
		echo "FAIL: expected $1"
		fail=1
	fi
}
expect() {
	if ! printf '%s\n' "$out" | grep -qx "$1"; then
		echo "FAIL: expected $1"
		fail=1
	fi
}
expect ok
expect_re "uid=2000[0-9]"
expect db=denied
expect datadir=denied
expect serverenv=denied
expect serverbin=denied
expect state=writable
expect tmp=writable
expect otherenv=denied
expect otherstate=denied
expect otherwrite=denied
expect otherhome=denied
expect groups=own
expect python=ok
expect uv=ok

exit $fail
