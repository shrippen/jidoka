#!/bin/sh
# Probe for scripts/image-test.sh: what a job may and may not do (Z3).
echo "uid=$(id -u)"
if cat /data/jidoka.db >/dev/null 2>&1; then echo "db=readable"; else echo "db=denied"; fi
if ls /data >/dev/null 2>&1; then echo "datadir=listable"; else echo "datadir=denied"; fi
srv=$(pgrep -f "jidoka serve" | head -1)
if cat "/proc/$srv/environ" >/dev/null 2>&1; then echo "serverenv=readable"; else echo "serverenv=denied"; fi
if /usr/local/bin/jidoka validate /jobs >/dev/null 2>&1; then echo "serverbin=runs"; else echo "serverbin=denied"; fi
if echo x > "$JIDOKA_STATE/probe"; then echo "state=writable"; else echo "state=denied"; fi
if echo x > "$TMPDIR/probe"; then echo "tmp=writable"; else echo "tmp=denied"; fi
python3 -c 'print("python=ok")'
uv --version >/dev/null && echo "uv=ok"
./pep723.py || echo "pep723=failed"

# The holder job (see testdata/image/holder) runs at the same time as
# another user: its environment, state and home stay closed (Z3).
other=""
for _ in $(seq 1 30); do
	other=$(pgrep -f "sleep 77" | head -1)
	[ -n "$other" ] && break
	sleep 1
done
if [ -z "$other" ]; then echo "otherjob=missing"; fi
if cat "/proc/$other/environ" >/dev/null 2>&1; then echo "otherenv=readable"; else echo "otherenv=denied"; fi
if cat /data/state/holder/private >/dev/null 2>&1; then echo "otherstate=readable"; else echo "otherstate=denied"; fi
if { echo x > /data/state/holder/probe; } 2>/dev/null; then echo "otherwrite=writable"; else echo "otherwrite=denied"; fi
if ls /data/home/holder >/dev/null 2>&1; then echo "otherhome=listable"; else echo "otherhome=denied"; fi
if [ "$(id -G)" = "$(id -g)" ]; then echo "groups=own"; else echo "groups=more"; fi
echo "probe done" > "$JIDOKA_SUMMARY"
