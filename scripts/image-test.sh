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
trap 'docker rm -f "$name" "$name-cache" >/dev/null 2>&1; rm -rf "$db"' EXIT

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
expect pep723=ok

# J11: over the limit, the server deletes the uv cache of a job although
# it belongs to the job's uid. A second server on the same data (jobs
# removed, limit 1 byte) does it on its first tick.
cache=/data/home/probe/.cache/uv
if ! docker exec "$name" test -d "$cache"; then
	echo "FAIL: probe has no uv cache to prune"
	fail=1
fi
docker stop "$name" >/dev/null
# A crash leaves a TMPDIR of the job uid, with a read-only folder in it.
uid=$(printf '%s\n' "$out" | sed -n 's/^uid=//p')
docker run --rm --volumes-from "$name" alpine:3.24 sh -c "
	mkdir -p /data/tmp/run-999/ro && touch /data/tmp/run-999/ro/f &&
	chown -R $uid:$uid /data/tmp/run-999 && chmod 500 /data/tmp/run-999/ro &&
	chmod 700 /data/tmp/run-999"
mkdir "$db/nojobs"
cp testdata/image/jidoka.toml "$db/nojobs/"
chmod 755 "$db/nojobs"
chmod 644 "$db/nojobs/jidoka.toml"
docker run -d --name "$name-cache" --volumes-from "$name" -e MASTER_KEY=image-test-only \
	-e JIDOKA_CACHE_LIMIT=1 -v "$db/nojobs:/jobs:ro" "$image" >/dev/null
for _ in $(seq 1 30); do
	sleep 1
	docker logs "$name-cache" 2>&1 | grep -q 'uv cache removed.*job=probe' && break
done
# J23: the restart removes the job-owned TMPDIRs the runs left (0700,
# the server has no DAC_OVERRIDE) and starts.
if docker exec "$name-cache" sh -c '[ -z "$(ls -A /data/tmp)" ]' 2>/dev/null; then
	echo "restart=ok"
else
	echo "FAIL: restart left /data/tmp or did not start"
	docker logs "$name-cache" 2>&1 | tail -5
	fail=1
fi
if docker logs "$name-cache" 2>&1 | grep -q 'uv cache removed.*job=probe' &&
	! docker exec "$name-cache" test -e "$cache"; then
	echo "cache=pruned"
else
	echo "FAIL: uv cache of probe not pruned"
	docker logs "$name-cache" 2>&1 | tail -5
	fail=1
fi

exit $fail
