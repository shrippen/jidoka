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
echo "probe done" > "$JIDOKA_SUMMARY"
